package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Refusal is an edit lookout declines because of what was asked or how the
// file is written, as distinct from a failure to read or write it. Its message
// says what to change.
type Refusal struct{ msg string }

func (r *Refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return &Refusal{msg: fmt.Sprintf(format, a...)} }

// Document is a configuration file opened for edits that leave every line they
// do not change exactly as the user wrote it. yaml.v3 cannot re-encode a file
// without reformatting it, so each edit splices text and then checks that the
// result parses to the intended configuration.
type Document struct {
	located string // the path the file was found by, which names the setting
	target  string // the file a save replaces, with symlinks resolved
	mode    os.FileMode
	orig    []byte
	data    []byte
	cfg     *Config
}

// Open reads the configuration at path for editing. It refuses a file whose
// layout an edit could not keep intact.
func Open(path string) (*Document, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("%s: resolving the configuration file: %w", path, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("%s: reading configuration: %w", path, err)
	}
	cfg, err := Parse(data, path)
	if err != nil {
		return nil, err
	}
	if _, err := parseLayout(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Document{located: path, target: target, mode: info.Mode().Perm(), orig: data, data: data, cfg: cfg}, nil
}

// Config is the configuration as edited so far.
func (d *Document) Config() *Config { return d.cfg }

// Adopt adds member to the named workstream. With create, the workstream must
// not exist yet and is added at the end of the order.
func (d *Document) Adopt(member Member, workstream string, create bool) error {
	token := member.Input + ":" + member.ID
	if !slices.ContainsFunc(d.cfg.Inputs, func(i Input) bool { return i.Name == member.Input }) {
		return refuse("input %q is not in this configuration: add it under inputs first", member.Input)
	}
	for _, w := range d.cfg.Workstreams {
		if slices.Contains(w.Members, member) {
			if w.Name == workstream {
				return refuse("%s is already a member of workstream %q", token, w.Name)
			}
			return refuse("%s already belongs to workstream %q: remove it there first", token, w.Name)
		}
	}
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	switch {
	case idx < 0 && !create:
		return refuse("no workstream %q: pass --new to add it at the end of the order", workstream)
	case idx >= 0 && create:
		return refuse("workstream %q already exists: drop --new to add to it", workstream)
	}
	scalar, err := scalarText(token)
	if err != nil {
		return err
	}

	l, err := parseLayout(d.data)
	if err != nil {
		return err
	}
	want := d.cfg.clone()
	// Naming an item of an input states that the input should have records.
	want.Inputs[slices.IndexFunc(want.Inputs, func(i Input) bool { return i.Name == member.Input })].ExpectRecords = true
	var text string
	if idx >= 0 {
		want.Workstreams[idx].Members = append(want.Workstreams[idx].Members, member)
		text, err = l.addMember(idx, scalar)
	} else {
		name, nameErr := scalarText(workstream)
		if nameErr != nil {
			return nameErr
		}
		want.Workstreams = append(want.Workstreams, Workstream{Name: workstream, Members: []Member{member}})
		text, err = l.addWorkstream(name, scalar)
	}
	if err != nil {
		return err
	}
	return d.apply([]byte(text), want)
}

// Order moves the named workstreams to the front, in the order given; the rest
// keep their order behind them.
func (d *Document) Order(names []string) error {
	for i, n := range names {
		if slices.Contains(names[:i], n) {
			return refuse("workstream %q is named twice", n)
		}
		if !slices.ContainsFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == n }) {
			return refuse("no workstream %q", n)
		}
	}
	perm := make([]int, 0, len(d.cfg.Workstreams))
	for _, n := range names {
		perm = append(perm, slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == n }))
	}
	for i, w := range d.cfg.Workstreams {
		if !slices.Contains(names, w.Name) {
			perm = append(perm, i)
		}
	}

	l, err := parseLayout(d.data)
	if err != nil {
		return err
	}
	want, src := d.cfg.clone(), d.cfg.clone()
	for i, j := range perm {
		want.Workstreams[i] = src.Workstreams[j]
	}
	return d.apply([]byte(l.reorder(perm)), want)
}

// Complete marks the named workstream finished as of date, a YYYY-MM-DD local
// date.
func (d *Document) Complete(workstream, date string) error {
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	if idx < 0 {
		return refuse("no workstream %q", workstream)
	}
	if done := d.cfg.Workstreams[idx].Completed; done != "" {
		return refuse("workstream %q is already complete, since %s: delete its completed line to reopen it", workstream, done)
	}
	l, err := parseLayout(d.data)
	if err != nil {
		return err
	}
	want := d.cfg.clone()
	want.Workstreams[idx].Completed = date
	return d.apply([]byte(l.addKey(idx, "completed: "+date)), want)
}

// apply accepts edited text only when it parses to exactly want, so a splice
// that went wrong is caught before anything is written.
func (d *Document) apply(text []byte, want *Config) error {
	got, err := Parse(text, d.located)
	if err != nil {
		return fmt.Errorf("the edit would leave an invalid file, so nothing was written: %w", err)
	}
	if !reflect.DeepEqual(got, want) {
		return errors.New("the edit would change more than intended, so nothing was written")
	}
	d.data, d.cfg = text, got
	return nil
}

// beforeRename runs just before a save replaces the file; specs use it to
// change the file underneath an edit.
var beforeRename = func() {}

// Save writes the edits atomically. It refuses when the file changed since
// Open read it, so an editor's save or a second invocation is never lost.
func (d *Document) Save() (err error) {
	if bytes.Equal(d.data, d.orig) {
		return nil
	}
	dir := filepath.Dir(d.target)
	// A leading dot keeps a leftover temporary file from being taken for a
	// setting when lookout looks for configuration files.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(d.target)+".tmp-*")
	if err != nil {
		return fmt.Errorf("%s: writing configuration: %w", d.located, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeIfPresent(tmp.Name()))
		}
	}()
	if _, err = tmp.Write(d.data); err != nil {
		return errors.Join(fmt.Errorf("%s: writing configuration: %w", d.located, err), tmp.Close())
	}
	if err = tmp.Chmod(d.mode); err != nil {
		return errors.Join(fmt.Errorf("%s: writing configuration: %w", d.located, err), tmp.Close())
	}
	if err = tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("%s: writing configuration: %w", d.located, err), tmp.Close())
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("%s: writing configuration: %w", d.located, err)
	}

	beforeRename()
	current, err := os.ReadFile(d.target)
	if err != nil {
		return fmt.Errorf("%s: re-reading configuration: %w", d.located, err)
	}
	if !bytes.Equal(current, d.orig) {
		return refuse("%s changed while lookout was editing it, so nothing was written: run the command again", d.located)
	}
	if err = os.Rename(tmp.Name(), d.target); err != nil {
		return fmt.Errorf("%s: replacing configuration: %w", d.located, err)
	}
	// The rename is durable only once the directory entry is.
	return syncDir(dir)
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	return errors.Join(f.Sync(), f.Close())
}

func (c *Config) clone() *Config {
	out := *c
	out.Inputs = slices.Clone(c.Inputs)
	out.Workstreams = make([]Workstream, len(c.Workstreams))
	for i, w := range c.Workstreams {
		out.Workstreams[i] = w
		out.Workstreams[i].Members = slices.Clone(w.Members)
	}
	if c.Workstreams == nil {
		out.Workstreams = nil
	}
	return &out
}

// scalarText is s as one line of YAML, quoted only when it needs to be.
func scalarText(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", refuse("%q contains a line break, which a configuration value cannot hold on one line", s)
	}
	out, err := yaml.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encoding %q: %w", s, err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// layout is a configuration file split into lines, with the positions of the
// parts an edit touches.
type layout struct {
	lines []string // each with its line ending, except perhaps the last
	eol   string
	root  *yaml.Node
	// wsKey and wsValue are the workstreams entry, or nil when the file has none.
	wsKey, wsValue *yaml.Node
	items          []wsItem
	regionEnd      int // index of the first line after the workstreams entry
}

type wsItem struct {
	node      *yaml.Node
	dash      int    // index of the line holding the item's "- "
	prefix    string // the text before its first key, such as "  - "
	nameValue *yaml.Node
	members   *yaml.Node // nil when it has no members key
	membersK  *yaml.Node
}

var dashPrefix = regexp.MustCompile(`^ *- +$`)

// parseLayout reads the file's structure and refuses what an edit could not
// keep intact. Columns from yaml.v3 count characters, so they are used only
// against prefixes this check has confirmed are ASCII spaces and dashes.
func parseLayout(data []byte) (*layout, error) {
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return nil, refuse("the file starts with a byte-order mark: save it as UTF-8 without one")
	}
	for i, b := range data {
		if b == '\r' && (i+1 == len(data) || data[i+1] != '\n') {
			return nil, refuse("the file breaks lines with a bare carriage return: save it with \\n or \\r\\n line endings")
		}
	}
	for _, r := range []rune{'\u0085', ' ', ' '} {
		if bytes.ContainsRune(data, r) {
			return nil, refuse("the file contains a Unicode line or paragraph separator (U+%04X): replace it with an ordinary line break", r)
		}
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing configuration: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, refuse("the file holds more than one YAML document: lookout reads only the first, so remove the rest")
	}
	if err := plain(&doc); err != nil {
		return nil, err
	}

	l := &layout{eol: "\n", lines: strings.SplitAfter(string(data), "\n")}
	if l.lines[len(l.lines)-1] == "" {
		l.lines = l.lines[:len(l.lines)-1]
	}
	if bytes.Contains(data, []byte("\r\n")) {
		l.eol = "\r\n"
	}
	l.regionEnd = len(l.lines)
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, refuse("the file is not a mapping of keys such as inputs and workstreams")
	}
	l.root = doc.Content[0]
	if l.root.Style&yaml.FlowStyle != 0 {
		return nil, refuse("the file is written as one flow mapping: write each key on its own line")
	}
	for i := 0; i+1 < len(l.root.Content); i += 2 {
		if l.root.Content[i].Value != "workstreams" {
			continue
		}
		l.wsKey, l.wsValue = l.root.Content[i], l.root.Content[i+1]
		if i+2 < len(l.root.Content) {
			l.regionEnd = l.root.Content[i+2].Line - 1
		}
	}
	if l.wsValue == nil || l.wsValue.Kind != yaml.SequenceNode {
		return l, nil
	}
	if l.wsValue.Style&yaml.FlowStyle != 0 {
		if len(l.wsValue.Content) > 0 {
			return nil, refuse("workstreams is written as a flow list ([...]): write one workstream per line")
		}
		return l, nil
	}
	for _, n := range l.wsValue.Content {
		it, err := l.item(n)
		if err != nil {
			return nil, err
		}
		l.items = append(l.items, it)
	}
	return l, nil
}

// plain refuses YAML features that let one piece of text stand for several
// values, since splicing it would change all of them.
func plain(n *yaml.Node) error {
	switch {
	case n.Kind == yaml.AliasNode || n.Anchor != "":
		return refuse("line %d uses a YAML anchor or alias: write the value out in full", n.Line)
	case n.Style&yaml.TaggedStyle != 0:
		return refuse("line %d uses a YAML tag (%s): remove it", n.Line, n.Tag)
	case n.Kind == yaml.ScalarNode && n.Tag == "!!merge":
		return refuse("line %d uses a YAML merge key: write the keys out in full", n.Line)
	}
	for _, c := range n.Content {
		if err := plain(c); err != nil {
			return err
		}
	}
	return nil
}

func (l *layout) item(n *yaml.Node) (wsItem, error) {
	if n.Kind != yaml.MappingNode || n.Style&yaml.FlowStyle != 0 || len(n.Content) == 0 {
		return wsItem{}, refuse("the workstream at line %d is not written as a block mapping: start it with \"- name:\" and put each key on its own line", n.Line)
	}
	dash := n.Line - 1
	prefix := l.lines[dash][:min(n.Column-1, len(l.lines[dash]))]
	if !dashPrefix.MatchString(prefix) {
		return wsItem{}, refuse("the workstream at line %d does not start with \"- \" on the same line as its first key", n.Line)
	}
	it := wsItem{node: n, dash: dash, prefix: prefix}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "name":
			if v.Kind != yaml.ScalarNode || v.Line != k.Line || v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
				return wsItem{}, refuse("the workstream name at line %d must sit on the same line as name:", k.Line)
			}
			it.nameValue = v
		case "members":
			it.membersK, it.members = k, v
			if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0 && len(v.Content) > 0 {
				return wsItem{}, refuse("members at line %d is written as a flow list ([...]): write one member per line, each starting with \"- \"", k.Line)
			}
		}
	}
	if it.nameValue == nil {
		return wsItem{}, refuse("the workstream at line %d has no name", n.Line)
	}
	return it, nil
}

func isFiller(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// blocks splits the workstreams entry into one text block per workstream and
// a trailer. A block runs from the line after the previous block's last line
// of content, so the comments and blank lines above a workstream travel with
// it, to its own last line of content. What follows the last block stays put.
func (l *layout) blocks() (start int, spans [][2]int, trailer [2]int) {
	start = l.wsKey.Line // the line after the workstreams key
	from := start
	for i, it := range l.items {
		next := l.regionEnd
		if i+1 < len(l.items) {
			next = l.items[i+1].dash
		}
		end := it.dash
		for j := it.dash; j < next; j++ {
			if !isFiller(l.lines[j]) {
				end = j
			}
		}
		spans = append(spans, [2]int{from, end + 1})
		from = end + 1
	}
	return start, spans, [2]int{from, l.regionEnd}
}

func (l *layout) withEOL(line string) string {
	if strings.HasSuffix(line, "\n") {
		return line
	}
	return line + l.eol
}

func (l *layout) join(parts ...[]string) string {
	var b strings.Builder
	for _, p := range parts {
		for _, s := range p {
			b.WriteString(s)
		}
	}
	return b.String()
}

// insert places new lines before index at, ending the line before them first
// when it is the file's unterminated last line.
func (l *layout) insert(at int, add ...string) string {
	before := slices.Clone(l.lines[:at])
	if at > 0 {
		before[at-1] = l.withEOL(before[at-1])
	}
	return l.join(before, add, l.lines[at:])
}

func (l *layout) reorder(perm []int) string {
	start, spans, trailer := l.blocks()
	var moved []string
	for _, j := range perm {
		block := slices.Clone(l.lines[spans[j][0]:spans[j][1]])
		block[len(block)-1] = l.withEOL(block[len(block)-1])
		moved = append(moved, block...)
	}
	return l.join(l.lines[:start], moved, l.lines[trailer[0]:trailer[1]], l.lines[trailer[1]:])
}

// indent is the indentation of the keys inside a workstream item.
func (it wsItem) indent() string { return strings.Repeat(" ", len(it.prefix)) }

func (l *layout) addKey(idx int, key string) string {
	it := l.items[idx]
	return l.insert(it.nameValue.Line, it.indent()+key+l.eol)
}

func (l *layout) addMember(idx int, scalar string) (string, error) {
	it := l.items[idx]
	ind := it.indent()
	if it.members == nil {
		return l.insert(it.nameValue.Line, ind+"members:"+l.eol, ind+"  - "+scalar+l.eol), nil
	}
	if it.members.Kind == yaml.SequenceNode && len(it.members.Content) > 0 {
		last := it.members.Content[len(it.members.Content)-1]
		at := last.Line - 1
		prefix := l.lines[at][:min(last.Column-1, len(l.lines[at]))]
		if !dashPrefix.MatchString(prefix) || last.Kind != yaml.ScalarNode || last.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return "", refuse("the last member at line %d is not one \"- <input>:<id>\" line", last.Line)
		}
		return l.insert(at+1, prefix+scalar+l.eol), nil
	}
	head, err := l.emptyValueHead(it.membersK, it.members)
	if err != nil {
		return "", err
	}
	at := it.membersK.Line - 1
	return l.join(l.lines[:at], []string{head, ind + "  - " + scalar + l.eol}, l.lines[at+1:]), nil
}

func (l *layout) addWorkstream(name, scalar string) (string, error) {
	prefix, ind := "  - ", "    "
	if len(l.items) > 0 {
		prefix, ind = l.items[0].prefix, l.items[0].indent()
	}
	block := []string{prefix + "name: " + name + l.eol, ind + "members:" + l.eol, ind + "  - " + scalar + l.eol}
	switch {
	case l.wsKey == nil:
		return l.insert(len(l.lines), append([]string{"workstreams:" + l.eol}, block...)...), nil
	case len(l.items) > 0:
		_, _, trailer := l.blocks()
		return l.insert(trailer[0], block...), nil
	}
	head, err := l.emptyValueHead(l.wsKey, l.wsValue)
	if err != nil {
		return "", err
	}
	at := l.wsKey.Line - 1
	return l.join(l.lines[:at], append([]string{head}, block...), l.lines[at+1:]), nil
}

// emptyValueHead rewrites a "key: []" or bare "key:" line as "key:", keeping
// any comment that follows, so a list can start on the lines below it.
func (l *layout) emptyValueHead(key, value *yaml.Node) (string, error) {
	line := strings.TrimRight(l.lines[key.Line-1], "\r\n")
	colon := strings.Index(line, key.Value+":")
	empty := value.Kind == yaml.ScalarNode && value.Tag == "!!null" ||
		value.Kind == yaml.SequenceNode && len(value.Content) == 0
	if colon < 0 || !empty || value.Line != key.Line && value.Value != "" {
		return "", refuse("%s at line %d must be empty ([] or nothing) on its own line", key.Value, key.Line)
	}
	head := line[:colon+len(key.Value)+1]
	rest := strings.TrimLeft(line[len(head):], " \t")
	for _, v := range []string{"[]", "~", "null"} {
		if strings.HasPrefix(rest, v) {
			rest = line[len(head)+strings.Index(line[len(head):], v)+len(v):]
			break
		}
	}
	if t := strings.TrimSpace(rest); t != "" && !strings.HasPrefix(t, "#") {
		return "", refuse("%s at line %d must be empty ([] or nothing) on its own line", key.Value, key.Line)
	}
	if strings.TrimSpace(rest) == "" {
		rest = ""
	}
	return head + rest + l.eol, nil
}

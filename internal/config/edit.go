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

// Refuse builds a Refusal from a message that says what to change.
func Refuse(format string, a ...any) error { return &Refusal{msg: fmt.Sprintf(format, a...)} }

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
// not exist yet and is added at the end of the order. An item a completed
// workstream holds is moved out of it, since an item has one current home;
// Adopt returns the workstreams it was moved from.
func (d *Document) Adopt(member Member, workstream string, create bool) ([]string, error) {
	token := member.Input + ":" + member.ID
	if !slices.ContainsFunc(d.cfg.Inputs, func(i Input) bool { return i.Name == member.Input }) {
		return nil, Refuse("input %q is not in this configuration: add it under inputs first", member.Input)
	}
	var from []int
	for i, w := range d.cfg.Workstreams {
		if !slices.Contains(w.Members, member) {
			continue
		}
		switch {
		case w.Name == workstream:
			return nil, Refuse("%s is already a member of workstream %q", token, w.Name)
		case w.Completed == "":
			return nil, Refuse("%s already belongs to workstream %q: remove it there first", token, w.Name)
		}
		from = append(from, i)
	}
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	switch {
	case idx < 0 && !create:
		return nil, Refuse("no workstream %q: pass --new to add it at the end of the order", workstream)
	case idx >= 0 && create:
		return nil, Refuse("workstream %q already exists: drop --new to add to it", workstream)
	case idx >= 0 && d.cfg.Workstreams[idx].Completed != "":
		return nil, Refuse("workstream %q is complete, since %s: delete its completed line to reopen it first", workstream, d.cfg.Workstreams[idx].Completed)
	}
	scalar, err := scalarText(token)
	if err != nil {
		return nil, err
	}

	text := string(d.data)
	want := d.cfg.clone()
	want.Workstreams = Adopted(d.cfg.Workstreams, member, workstream)
	var names []string
	for _, i := range from {
		l, layoutErr := parseLayout([]byte(text))
		if layoutErr != nil {
			return nil, layoutErr
		}
		if text, err = l.removeMember(i, member.Input+":"+member.ID); err != nil {
			return nil, err
		}
		names = append(names, d.cfg.Workstreams[i].Name)
	}
	l, err := parseLayout([]byte(text))
	if err != nil {
		return nil, err
	}
	// Naming an item of a supplied input states that the input should have
	// records; Parse applies the same rule, and the two must agree.
	if in := &want.Inputs[slices.IndexFunc(want.Inputs, func(i Input) bool { return i.Name == member.Input })]; in.Kind == KindSupplied {
		in.ExpectRecords = true
	}
	if idx >= 0 {
		text, err = l.addMember(idx, scalar)
	} else {
		name, nameErr := scalarText(workstream)
		if nameErr != nil {
			return nil, nameErr
		}
		text, err = l.addWorkstream(name, scalar)
	}
	if err != nil {
		return nil, err
	}
	return names, d.apply([]byte(text), want)
}

// Order moves the named workstreams to the front, in the order given; the rest
// keep their order behind them.
func (d *Document) Order(names []string) error {
	for i, n := range names {
		if slices.Contains(names[:i], n) {
			return Refuse("workstream %q is named twice", n)
		}
		if !slices.ContainsFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == n }) {
			return Refuse("no workstream %q", n)
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
	want := d.cfg.clone()
	want.Workstreams = Ordered(d.cfg.Workstreams, names)
	return d.apply([]byte(l.reorder(perm)), want)
}

// Complete marks the named workstream finished as of date, a YYYY-MM-DD local
// date.
func (d *Document) Complete(workstream, date string) error {
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	if idx < 0 {
		return Refuse("no workstream %q", workstream)
	}
	if done := d.cfg.Workstreams[idx].Completed; done != "" {
		return Refuse("workstream %q is already complete, since %s: delete its completed line to reopen it", workstream, done)
	}
	l, err := parseLayout(d.data)
	if err != nil {
		return err
	}
	if l.items[idx].completed {
		return Refuse("workstream %q has an empty completed key: delete it, or give it a date such as %s", workstream, date)
	}
	want := d.cfg.clone()
	want.Workstreams = CompletedOn(d.cfg.Workstreams, workstream, date)
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

// ErrNotDurable reports a save that replaced the file but could not confirm
// the replacement reached the disk.
var ErrNotDurable = errors.New("the configuration was replaced, but syncing its directory failed")

// Save writes the edits atomically. It refuses when the file changed since
// Open read it, so an editor's save made meanwhile is not overwritten. The
// check and the rename are not one step; lookout's own invocations for a
// setting are kept apart by the caller holding that setting's state.
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
		return Refuse("%s changed while lookout was editing it, so nothing was written: run the command again", d.located)
	}
	if err = os.Rename(tmp.Name(), d.target); err != nil {
		return fmt.Errorf("%s: replacing configuration: %w", d.located, err)
	}
	// The rename is durable only once the directory entry is.
	if syncErr := syncDir(dir); syncErr != nil {
		return fmt.Errorf("%s: %w: %w", d.located, ErrNotDurable, syncErr)
	}
	return nil
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
	out.Workstreams = cloneWorkstreams(c.Workstreams)
	return &out
}

func cloneWorkstreams(ws []Workstream) []Workstream {
	if ws == nil {
		return nil
	}
	out := make([]Workstream, len(ws))
	for i, w := range ws {
		out[i] = w
		out[i].Members = slices.Clone(w.Members)
	}
	return out
}

// The functions below are what an accepted edit does to the workstreams, kept
// apart from the text splicing so that replaying a logged gesture reaches the
// same result the edit itself checked the file against. Each assumes the edit
// is one its Document method accepts, and leaves ws untouched.

// Adopted is ws with member added to the named workstream, or to a new one at
// the end when none has that name, and moved out of any completed workstream
// that held it.
func Adopted(ws []Workstream, member Member, workstream string) []Workstream {
	out := cloneWorkstreams(ws)
	for i := range out {
		if out[i].Completed == "" || out[i].Name == workstream {
			continue
		}
		out[i].Members = slices.DeleteFunc(out[i].Members, func(m Member) bool { return m == member })
		if len(out[i].Members) == 0 {
			out[i].Members = nil
		}
	}
	if idx := slices.IndexFunc(out, func(w Workstream) bool { return w.Name == workstream }); idx >= 0 {
		out[idx].Members = append(out[idx].Members, member)
		return out
	}
	return append(out, Workstream{Name: workstream, Members: []Member{member}})
}

// Ordered is ws with the named workstreams moved to the front, in the order
// given, and the rest behind them in their own order.
func Ordered(ws []Workstream, names []string) []Workstream {
	out := make([]Workstream, 0, len(ws))
	for _, n := range names {
		if i := slices.IndexFunc(ws, func(w Workstream) bool { return w.Name == n }); i >= 0 {
			out = append(out, ws[i])
		}
	}
	for _, w := range ws {
		if !slices.Contains(names, w.Name) {
			out = append(out, w)
		}
	}
	return cloneWorkstreams(out)
}

// CompletedOn is ws with the named workstream marked finished on date.
func CompletedOn(ws []Workstream, workstream, date string) []Workstream {
	out := cloneWorkstreams(ws)
	for i := range out {
		if out[i].Name == workstream {
			out[i].Completed = date
		}
	}
	return out
}

// scalarText is s as one line of YAML, quoted only when it needs to be.
func scalarText(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", Refuse("%q contains a line break, which a configuration value cannot hold on one line", s)
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
	completed bool // it has a completed key, even an empty one
}

var dashPrefix = regexp.MustCompile(`^ *- +$`)

// parseLayout reads the file's structure and refuses what an edit could not
// keep intact. Columns from yaml.v3 count characters, so they are used only
// against prefixes this check has confirmed are ASCII spaces and dashes.
func parseLayout(data []byte) (*layout, error) {
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return nil, Refuse("the file starts with a byte-order mark: save it as UTF-8 without one")
	}
	for i, b := range data {
		if b == '\r' && (i+1 == len(data) || data[i+1] != '\n') {
			return nil, Refuse("the file breaks lines with a bare carriage return: save it with \\n or \\r\\n line endings")
		}
	}
	for _, r := range []rune{'\u0085', ' ', ' '} {
		if bytes.ContainsRune(data, r) {
			return nil, Refuse("the file contains a Unicode line or paragraph separator (U+%04X): replace it with an ordinary line break", r)
		}
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing configuration: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, Refuse("the file holds more than one YAML document: lookout reads only the first, so remove the rest")
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
		return nil, Refuse("the file is not a mapping of keys such as inputs and workstreams")
	}
	l.root = doc.Content[0]
	if l.root.Style&yaml.FlowStyle != 0 {
		return nil, Refuse("the file is written as one flow mapping: write each key on its own line")
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
			return nil, Refuse("workstreams is written as a flow list ([...]): write one workstream per line")
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
		return Refuse("line %d uses a YAML anchor or alias: write the value out in full", n.Line)
	case n.Style&yaml.TaggedStyle != 0:
		return Refuse("line %d uses a YAML tag (%s): remove it", n.Line, n.Tag)
	case n.Kind == yaml.ScalarNode && n.Tag == "!!merge":
		return Refuse("line %d uses a YAML merge key: write the keys out in full", n.Line)
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
		return wsItem{}, Refuse("the workstream at line %d is not written as a block mapping: start it with \"- name:\" and put each key on its own line", n.Line)
	}
	dash := n.Line - 1
	prefix := l.lines[dash][:min(n.Column-1, len(l.lines[dash]))]
	if !dashPrefix.MatchString(prefix) {
		return wsItem{}, Refuse("the workstream at line %d does not start with \"- \" on the same line as its first key", n.Line)
	}
	it := wsItem{node: n, dash: dash, prefix: prefix}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "name":
			if v.Kind != yaml.ScalarNode || v.Line != k.Line || !l.onOneLine(v, false) {
				return wsItem{}, Refuse("the workstream name at line %d must be written on one line, on the same line as name:", k.Line)
			}
			it.nameValue = v
		case "completed":
			it.completed = true
		case "members":
			it.membersK, it.members = k, v
			if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0 && len(v.Content) > 0 {
				return wsItem{}, Refuse("members at line %d is written as a flow list ([...]): write one member per line, each starting with \"- \"", k.Line)
			}
		}
	}
	if it.nameValue == nil {
		return wsItem{}, Refuse("the workstream at line %d has no name", n.Line)
	}
	return it, nil
}

// onOneLine reports whether the scalar n is written wholly on the line it
// starts on. yaml.v3 gives only a scalar's start, and a quoted or plain scalar
// can continue onto later lines, so the rest of its line is parsed alone and
// must give the same value. It is parsed in the place it was written, as a
// mapping value or a sequence item, since at the start of a document text
// such as --- means something else. The text before n is ASCII by the
// callers' checks, so its character column is also its byte offset.
func (l *layout) onOneLine(n *yaml.Node, inSequence bool) bool {
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return false
	}
	line := strings.TrimRight(l.lines[n.Line-1], "\r\n")
	if n.Column-1 > len(line) {
		return false
	}
	rest := []byte(line[n.Column-1:])
	if inSequence {
		var alone []string
		err := yaml.Unmarshal(append([]byte("- "), rest...), &alone)
		return err == nil && len(alone) == 1 && alone[0] == n.Value
	}
	var alone map[string]string
	err := yaml.Unmarshal(append([]byte("k: "), rest...), &alone)
	return err == nil && len(alone) == 1 && alone["k"] == n.Value
}

func isComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func isFiller(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// blocks splits the workstreams entry into one text block per workstream and
// a trailer. A block runs from the line after the previous block's last line,
// so the comments and blank lines above a workstream travel with it, to its
// own last line of content plus any comment lines directly after it indented
// deeper than its dash, such as a commented-out member. What follows the last
// block stays put.
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
		dashIndent := len(it.prefix) - len(strings.TrimLeft(it.prefix, " "))
		for end+1 < next && isComment(l.lines[end+1]) && indentOf(l.lines[end+1]) > dashIndent {
			end++
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
		if !dashPrefix.MatchString(prefix) || last.Kind != yaml.ScalarNode || !l.onOneLine(last, true) {
			return "", Refuse("the last member at line %d is not written on one line as \"- <input>:<id>\"", last.Line)
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

// removeMember deletes the line holding the workstream's member token.
func (l *layout) removeMember(idx int, token string) (string, error) {
	it := l.items[idx]
	if it.members == nil || it.members.Kind != yaml.SequenceNode {
		return "", fmt.Errorf("workstream %q has no member list", it.nameValue.Value)
	}
	for _, m := range it.members.Content {
		if m.Kind != yaml.ScalarNode || m.Value != token {
			continue
		}
		at := m.Line - 1
		prefix := l.lines[at][:min(m.Column-1, len(l.lines[at]))]
		if !dashPrefix.MatchString(prefix) || !l.onOneLine(m, true) {
			return "", Refuse("the member at line %d is not written on one line as \"- <input>:<id>\"", m.Line)
		}
		return l.join(l.lines[:at], l.lines[at+1:]), nil
	}
	return "", fmt.Errorf("workstream %q does not list %s", it.nameValue.Value, token)
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
		return "", Refuse("%s at line %d must be empty ([] or nothing) on its own line", key.Value, key.Line)
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
		return "", Refuse("%s at line %d must be empty ([] or nothing) on its own line", key.Value, key.Line)
	}
	if strings.TrimSpace(rest) == "" {
		rest = ""
	}
	return head + rest + l.eol, nil
}

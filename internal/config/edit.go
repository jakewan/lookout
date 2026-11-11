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
	"unicode/utf8"

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
	if workstream == "" {
		return nil, Refuse("a workstream name cannot be empty")
	}
	at := slices.IndexFunc(d.cfg.Inputs, func(i Input) bool { return i.Name == member.Input })
	if at < 0 {
		return nil, Refuse("input %q is not in this configuration: add it under inputs first", member.Input)
	}
	if d.cfg.Inputs[at].Kind == KindAwaiting {
		return nil, Refuse("%q is a reply owed from awaiting input %q, which is never a workstream member", token, member.Input)
	}
	var from []int
	for i, w := range d.cfg.Workstreams {
		if !slices.Contains(w.Members, member) {
			continue
		}
		switch {
		case w.Name == workstream:
			return nil, Refuse("%q is already a member of workstream %q", token, w.Name)
		case w.Completed == "":
			return nil, Refuse("%q already belongs to workstream %q: use lookout move to move it", token, w.Name)
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
	if idx < 0 {
		if err := d.idFree(workstream, ""); err != nil {
			return nil, err
		}
	}
	scalar, err := scalarText(token)
	if err != nil {
		return nil, err
	}

	want := d.cfg.clone()
	want.Workstreams = Adopted(d.cfg.Workstreams, member, workstream)
	text, names, err := d.removed(member, from)
	if err != nil {
		return nil, err
	}
	l, err := parseLayout([]byte(text))
	if err != nil {
		return nil, err
	}
	want.implyExpectations()
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

// Move takes member out of every workstream that holds it and into the named
// one, created at the end of the order with create. The item must already be in
// a live workstream; one that is not is placed with Adopt. Move returns the
// workstreams it was taken out of.
func (d *Document) Move(member Member, workstream string, create bool) ([]string, error) {
	token := member.Input + ":" + member.ID
	if workstream == "" {
		return nil, Refuse("a workstream name cannot be empty")
	}
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	var from []int
	live, inTarget := false, false
	for i, w := range d.cfg.Workstreams {
		if !slices.Contains(w.Members, member) {
			continue
		}
		live = live || w.Completed == ""
		if i == idx {
			inTarget = true
			continue
		}
		from = append(from, i)
	}
	switch {
	case !live:
		return nil, Refuse("%q is not in a live workstream: use lookout adopt to place it", token)
	case inTarget && len(from) == 0:
		return nil, Refuse("%q is already a member of workstream %q", token, workstream)
	case idx < 0 && !create:
		return nil, Refuse("no workstream %q: pass --new to add it at the end of the order", workstream)
	case idx >= 0 && create:
		return nil, Refuse("workstream %q already exists: drop --new to move to it", workstream)
	case idx >= 0 && d.cfg.Workstreams[idx].Completed != "":
		return nil, Refuse("workstream %q is complete, since %s: delete its completed line to reopen it first", workstream, d.cfg.Workstreams[idx].Completed)
	}
	if idx < 0 {
		if err := d.idFree(workstream, ""); err != nil {
			return nil, err
		}
	}
	scalar, err := scalarText(token)
	if err != nil {
		return nil, err
	}

	text, names, err := d.removed(member, from)
	if err != nil {
		return nil, err
	}
	l, err := parseLayout([]byte(text))
	if err != nil {
		return nil, err
	}
	switch {
	case inTarget:
	case idx >= 0:
		text, err = l.addMember(idx, scalar)
	default:
		name, nameErr := scalarText(workstream)
		if nameErr != nil {
			return nil, nameErr
		}
		text, err = l.addWorkstream(name, scalar)
	}
	if err != nil {
		return nil, err
	}
	want := d.cfg.clone()
	want.Workstreams = Moved(d.cfg.Workstreams, member, workstream)
	want.implyExpectations()
	return names, d.apply([]byte(text), want)
}

// Remove takes member out of every workstream that holds it, live or
// completed, and returns their names. Once no workstream names an item of a
// supplied input, the input expects records only where the file says so.
func (d *Document) Remove(member Member) ([]string, error) {
	var from []int
	for i, w := range d.cfg.Workstreams {
		if slices.Contains(w.Members, member) {
			from = append(from, i)
		}
	}
	if len(from) == 0 {
		return nil, Refuse("%q is not in any workstream", member.Input+":"+member.ID)
	}
	text, names, err := d.removed(member, from)
	if err != nil {
		return nil, err
	}
	want := d.cfg.clone()
	want.Workstreams = Removed(d.cfg.Workstreams, member)
	want.implyExpectations()
	return names, d.apply([]byte(text), want)
}

// removed is the file's text with member's line deleted from each of the
// workstreams at the given positions, and their names.
func (d *Document) removed(member Member, from []int) (string, []string, error) {
	text := string(d.data)
	var names []string
	for _, i := range from {
		l, err := parseLayout([]byte(text))
		if err != nil {
			return "", nil, err
		}
		if text, err = l.removeMember(i, member.Input+":"+member.ID); err != nil {
			return "", nil, err
		}
		names = append(names, d.cfg.Workstreams[i].Name)
	}
	return text, names, nil
}

// Rename gives the named workstream, live or completed, a new id, changing
// only the text of its name.
func (d *Document) Rename(workstream, name string) error {
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	switch {
	case idx < 0:
		return Refuse("no workstream %q", workstream)
	case name == "":
		return Refuse("a workstream name cannot be empty")
	case name == workstream:
		return Refuse("workstream %q already has that name", workstream)
	case slices.ContainsFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == name }):
		return Refuse("workstream %q already exists: choose another name", name)
	}
	if err := d.idFree(name, workstream); err != nil {
		return err
	}
	scalar, err := scalarText(name)
	if err != nil {
		return err
	}
	l, err := parseLayout(d.data)
	if err != nil {
		return err
	}
	text, err := l.replaceValue(l.items[idx].nameValue, scalar, "workstream name")
	if err != nil {
		return err
	}
	want := d.cfg.clone()
	want.Workstreams = Renamed(d.cfg.Workstreams, workstream, name)
	return d.apply([]byte(text), want)
}

// SetTitle gives the named workstream, live or completed, title as the label a
// person reads, keeping its id.
func (d *Document) SetTitle(workstream, title string) error {
	idx, l, err := d.titleTarget(workstream)
	if err != nil {
		return err
	}
	if titleErr := CheckTitle(title); errors.Is(titleErr, ErrNoVisibleText) {
		return Refuse("%v: to show the id instead, use lookout rename --clear", titleErr)
	} else if titleErr != nil {
		return titleErr
	}
	if other, clash := titleClash(d.cfg.Workstreams, workstream, title); clash {
		return Refuse("%q is the id of workstream %q, so as a title it would name the wrong workstream: choose another title", title, other)
	}
	scalar, err := scalarText(title)
	if err != nil {
		return err
	}
	want := d.cfg.clone()
	want.Workstreams = Titled(d.cfg.Workstreams, workstream, title)
	it := l.items[idx]
	if it.titleValue == nil {
		return d.apply([]byte(l.addKey(idx, "title: "+scalar)), want)
	}
	text, err := l.replaceValue(it.titleValue, scalar, "workstream title")
	if err != nil {
		return err
	}
	return d.apply([]byte(text), want)
}

// ClearTitle removes the named workstream's title, so its id is shown instead.
// A workstream with none is left as it is.
func (d *Document) ClearTitle(workstream string) error {
	idx, l, err := d.titleTarget(workstream)
	if err != nil {
		return err
	}
	it := l.items[idx]
	if it.titleValue == nil {
		return nil
	}
	if !l.lineHoldsOnly(it.titleKey) {
		return Refuse("the title at line %d shares its line with the start of the workstream: move name: to the first line, then run the command again", it.titleKey.Line)
	}
	want := d.cfg.clone()
	want.Workstreams = Titled(d.cfg.Workstreams, workstream, "")
	at := it.titleKey.Line - 1
	return d.apply([]byte(l.join(l.lines[:at], l.lines[at+1:])), want)
}

func (d *Document) titleTarget(workstream string) (int, *layout, error) {
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	if idx < 0 {
		return 0, nil, Refuse("no workstream %q", workstream)
	}
	l, err := parseLayout(d.data)
	if err != nil {
		return 0, nil, err
	}
	return idx, l, nil
}

// idFree refuses an id that a workstream other than self already carries as
// its title, which a reader of a view could not tell apart from it; self is
// the workstream taking the id, or empty for one being created.
func (d *Document) idFree(id, self string) error {
	for _, w := range d.cfg.Workstreams {
		if w.Title == id && w.Name != id && w.Name != self {
			return Refuse("workstream %q has the title %q, so an id %q would read as that workstream: choose another id", w.Name, id, id)
		}
	}
	return nil
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

// Delete takes the named workstream out of the file. It refuses one that names
// any member, live or completed, since deleting it would let those items go
// unplaced without anyone deciding so, and a completed workstream's members
// are its history. Every join hangs from a member, so an empty workstream holds
// nothing a read could place there.
func (d *Document) Delete(workstream string) error {
	idx := slices.IndexFunc(d.cfg.Workstreams, func(w Workstream) bool { return w.Name == workstream })
	if idx < 0 {
		return Refuse("no workstream %q", workstream)
	}
	if n := len(d.cfg.Workstreams[idx].Members); n > 0 {
		if d.cfg.Workstreams[idx].Completed != "" {
			return Refuse("workstream %q is complete and names %d member(s): take them out with lookout remove or lookout move first, or leave it in place to keep its history", workstream, n)
		}
		return Refuse("workstream %q names %d member(s): take them out with lookout remove or lookout move first, or keep its history with lookout complete", workstream, n)
	}
	l, err := parseLayout(d.data)
	if err != nil {
		return err
	}
	// Only the workstream's own lines go, from its dash to the end of its block.
	// The comments and blank lines above the dash, which blocks() counts in the
	// block so that they travel with it on a reorder, may be a header for the
	// whole list or a commented-out neighbor, and a comment is not lookout's to
	// throw away, so they stay, even one describing this workstream, which then
	// reads as the next one's. Where a blank line stays above the dash, one
	// directly after the block goes with it, so blank separators don't pile up.
	_, spans, _ := l.blocks()
	from, to := l.items[idx].dash, spans[idx][1]
	if from > 0 && to < len(l.lines) && isBlank(l.lines[from-1]) && isBlank(l.lines[to]) {
		to++
	}
	want := d.cfg.clone()
	want.Workstreams = Deleted(d.cfg.Workstreams, workstream)
	return d.apply([]byte(l.join(l.lines[:from], l.lines[to:])), want)
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
// same result the edit itself checked the file against. Each leaves ws
// untouched. Each assumes the edit is one its Document method would accept on
// ws; a replay onto workstreams a hand edit has since changed can break that,
// and then hides the hand edit a later gesture overwrote rather than reporting
// one that never happened. An item placed by hand and then moved by command is
// hidden that way, and is not counted as arriving either, and so is a member
// removed by hand from a workstream then deleted by command, since Deleted takes
// the workstream whatever it still holds.

// Adopted is ws with member added to the named workstream, or to a new one at
// the end when none has that name, and moved out of any completed workstream
// that held it.
func Adopted(ws []Workstream, member Member, workstream string) []Workstream {
	out := cloneWorkstreams(ws)
	for i := range out {
		if out[i].Completed == "" || out[i].Name == workstream {
			continue
		}
		out[i].Members = without(out[i].Members, member)
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

// Moved is ws with member taken out of every workstream but the named one, and
// added to that one, or to a new one at the end when none has that name.
func Moved(ws []Workstream, member Member, workstream string) []Workstream {
	out := cloneWorkstreams(ws)
	for i := range out {
		if out[i].Name != workstream {
			out[i].Members = without(out[i].Members, member)
		}
	}
	idx := slices.IndexFunc(out, func(w Workstream) bool { return w.Name == workstream })
	switch {
	case idx < 0:
		return append(out, Workstream{Name: workstream, Members: []Member{member}})
	case !slices.Contains(out[idx].Members, member):
		out[idx].Members = append(out[idx].Members, member)
	}
	return out
}

// Removed is ws with member taken out of every workstream.
func Removed(ws []Workstream, member Member) []Workstream {
	out := cloneWorkstreams(ws)
	for i := range out {
		out[i].Members = without(out[i].Members, member)
	}
	return out
}

// Renamed is ws with the named workstream given a new name.
func Renamed(ws []Workstream, workstream, name string) []Workstream {
	out := cloneWorkstreams(ws)
	for i := range out {
		if out[i].Name == workstream {
			out[i].Name = name
		}
	}
	return out
}

// Deleted is ws without the named workstream; an emptied list is nil, as Parse
// reads a workstreams key with nothing under it.
func Deleted(ws []Workstream, workstream string) []Workstream {
	out := slices.DeleteFunc(cloneWorkstreams(ws), func(w Workstream) bool { return w.Name == workstream })
	if len(out) == 0 {
		return nil
	}
	return out
}

// Titled is ws with the named workstream given title, or none when it is empty.
func Titled(ws []Workstream, workstream, title string) []Workstream {
	out := cloneWorkstreams(ws)
	for i := range out {
		if out[i].Name == workstream {
			out[i].Title = title
		}
	}
	return out
}

// without is members less member; an emptied list is nil, as Parse reads a
// members key with nothing under it.
func without(members []Member, member Member) []Member {
	members = slices.DeleteFunc(members, func(m Member) bool { return m == member })
	if len(members) == 0 {
		return nil
	}
	return members
}

// scalarText is s as one line of YAML, quoted only when it needs to be.
func scalarText(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", Refuse("%q contains a line break, which a configuration value cannot hold on one line", s)
	}
	// yaml.Marshal writes text that is not UTF-8 as a !!binary tag, which
	// parseLayout then refuses, so every later edit would be refused too.
	if !utf8.ValidString(s) {
		return "", Refuse("%q is not valid UTF-8 text", s)
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
	// titleKey and titleValue are its title entry, or nil when it has none.
	titleKey, titleValue *yaml.Node
	members              *yaml.Node // nil when it has no members key
	membersK             *yaml.Node
	completed            bool // it has a completed key, even an empty one
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
	// A document end marker after the last key closes the workstreams region
	// too; what follows it, and a footer above it, are the file's, not the last
	// workstream's.
	if l.wsKey != nil {
		for j := l.wsKey.Line; j < l.regionEnd; j++ {
			if isDocumentEnd(l.lines[j]) {
				l.regionEnd = j
				break
			}
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
		case "title":
			if v.Kind != yaml.ScalarNode || v.Line != k.Line || !l.onOneLine(v, false) {
				return wsItem{}, Refuse("the workstream title at line %d must be written on one line, on the same line as title:", k.Line)
			}
			it.titleKey, it.titleValue = k, v
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

// lineHoldsOnly reports whether the line holding key starts with it, after
// spaces, so that, with its value on that line alone, deleting the line
// removes nothing else, as it would a workstream's "- " written before it.
func (l *layout) lineHoldsOnly(key *yaml.Node) bool {
	line := l.lines[key.Line-1]
	return key.Column-1 <= len(line) && strings.TrimLeft(line[:key.Column-1], " ") == ""
}

func isComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func isBlank(line string) bool { return strings.TrimSpace(line) == "" }

// isDocumentEnd reports YAML's document end marker: three dots at the start
// of a line, alone or followed by white space.
func isDocumentEnd(line string) bool {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "...")
	return ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t')
}

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
	return "", fmt.Errorf("workstream %q does not list %q", it.nameValue.Value, token)
}

// replaceValue replaces the text of a one-line mapping value, such as a
// workstream's name or title, keeping whatever follows it on the line, such as
// a comment, as written. The value's end is the first point at which the text
// before it reads alone as the value and only a comment or nothing follows;
// what names the value in a refusal.
func (l *layout) replaceValue(v *yaml.Node, scalar, what string) (string, error) {
	at := v.Line - 1
	line := l.lines[at]
	body := strings.TrimRight(line, "\r\n")
	start := v.Column - 1
	for end := start + 1; end <= len(body); end++ {
		rest := body[end:]
		if t := strings.TrimSpace(rest); t != "" && (!strings.HasPrefix(t, "#") || !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t")) {
			continue
		}
		var alone map[string]string
		if err := yaml.Unmarshal([]byte("k: "+body[start:end]), &alone); err != nil || len(alone) != 1 || alone["k"] != v.Value {
			continue
		}
		renamed := body[:start] + scalar + rest + line[len(body):]
		return l.join(l.lines[:at], []string{renamed}, l.lines[at+1:]), nil
	}
	return "", Refuse("the %s at line %d could not be told apart from what follows it on the line", what, v.Line)
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

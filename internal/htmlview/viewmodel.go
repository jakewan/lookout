package htmlview

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/jakewan/lookout/internal/viewrules"
)

// viewModel is the map the page's script draws: a tree of the setting, its
// workstreams and their items, and the cross-links the tree does not show.
// Every order, place, count and cue in it is decided here; the script only
// lays it out and shows or hides parts of it, so no rule is written twice.
// It is the page's own and no published contract.
type viewModel struct {
	Hub   int         `json:"hub"`
	Nodes []mapNode   `json:"nodes"`
	Links []crossLink `json:"links"`
}

type mapNode struct {
	// Kind is setting, workstream, item, or unread for a member nothing read.
	Kind string `json:"kind"`
	// Handle is what commands take, exactly as the readout gives it: a
	// workstream's id or an item's <input>:<id>, and empty for the setting.
	Handle rawText `json:"handle"`
	// Ref is the handle cleaned, for naming the node in words. Label is
	// shown on the node, Sub beside it, and Name is everything a screen
	// reader says for it, cues in words included. All are cleaned.
	Ref   string   `json:"ref"`
	Label string   `json:"label"`
	Sub   string   `json:"sub"`
	Name  string   `json:"name"`
	Words []string `json:"words"`
	// Flag is a mark for a workstream with a cue, so it stands out by shape
	// as well as color; empty for any other node.
	Flag     string `json:"flag"`
	State    string `json:"state"`
	Href     string `json:"href"`
	JoinedBy string `json:"joined_by"`
	// HiddenChars is set where cleaning changed the handle, so what is shown
	// is not what is copied.
	HiddenChars bool `json:"hidden_chars"`
	// Anchor is the workstream's lane link target, so following a lane
	// can focus its node.
	Anchor   string `json:"anchor"`
	Parent   int    `json:"parent"`
	Children []int  `json:"children"`
	// Weight is the number of leaves beneath the node, or 1 for a leaf: the
	// share of its parent's angle the layout gives it.
	Weight int `json:"weight"`
	// Rollup says what a collapsed node holds.
	Rollup         string `json:"rollup"`
	StartCollapsed bool   `json:"start_collapsed"`
}

// crossLink is a relation the tree does not show, between two nodes.
type crossLink struct {
	From  int    `json:"from"`
	To    int    `json:"to"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// rawText is readout text carried exactly, for copying. It is written with
// every character Go does not count as printable escaped, so the page's
// source holds no bidirectional override or zero-width character while the
// parsed string keeps them.
type rawText string

func (s rawText) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range string(s) {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case strconv.IsPrint(r):
			b.WriteRune(r)
		default:
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04x`, u)
			}
		}
	}
	b.WriteByte('"')
	return []byte(b.String()), nil
}

// entry is one thing a workstream holds, in the order its lists show them.
type entry struct {
	handle string
	input  string
	id     string
	placed *rawPlaced // nil for a member nothing read
}

func (e entry) join() *join {
	if e.placed == nil {
		return nil
	}
	return e.placed.Joined
}

// mapOf builds the map. shown holds the workstreams as the page lists them,
// in the same order as r.Workstreams.
func mapOf(r readout, shown []workstream, order viewrules.Order) viewModel {
	m := viewModel{Links: []crossLink{}}
	add := func(n mapNode) int {
		n.Children = []int{}
		m.Nodes = append(m.Nodes, n)
		return len(m.Nodes) - 1
	}
	setting := viewrules.Clean(r.Setting)
	m.Hub = add(mapNode{Kind: "setting", Ref: setting, Label: setting, Name: setting, Parent: -1})

	entries := make([][]entry, len(r.Workstreams))
	for i, ws := range r.Workstreams {
		entries[i] = entriesOf(ws, order)
	}
	home := homes(entries)

	wsNode := make([]int, len(r.Workstreams))
	// drawn is each workstream's nodes by handle, for what it draws itself.
	drawn := make([]map[string]int, len(r.Workstreams))
	// placed is every item's one node, wherever it is drawn.
	placed := map[string]int{}
	for i, ws := range r.Workstreams {
		wsNode[i] = add(workstreamNode(ws, shown[i], m.Hub))
		m.Nodes[m.Hub].Children = append(m.Nodes[m.Hub].Children, wsNode[i])
		drawn[i] = map[string]int{}
		for _, e := range entries[i] {
			if e.placed != nil && home[e.handle] != i {
				continue
			}
			n := add(itemNode(e))
			drawn[i][e.handle] = n
			if e.placed != nil {
				placed[e.handle] = n
			}
		}
	}
	links := linkSet{model: &m}
	for i := range r.Workstreams {
		place(&m, &links, entries[i], drawn[i], wsNode[i], placed)
	}
	for handle, holders := range holdersOf(entries) {
		for _, w := range holders {
			if w != home[handle] {
				links.add(placed[handle], wsNode[w], "shared")
			}
		}
	}
	byName := map[string]int{}
	for i, ws := range r.Workstreams {
		byName[ws.Name] = i
	}
	for _, l := range r.Links {
		from, okFrom := byName[l.From]
		to, okTo := byName[l.To]
		if !okFrom || !okTo {
			continue
		}
		switch l.Kind {
		case "shared_item":
			// Drawn from what each workstream holds above, once per holder
			// rather than once per pair of them.
		case "parent":
			for _, child := range l.Items {
				c, ok := placed[child]
				if !ok {
					continue
				}
				target := wsNode[from]
				if p := parentHandle(entries[to], child); p != "" {
					if n, ok := placed[p]; ok {
						target = n
					}
				}
				links.add(c, target, "parent")
			}
		default:
			for _, it := range l.Items {
				if n, ok := placed[it]; ok {
					links.add(n, wsNode[to], l.Kind)
				} else {
					links.add(wsNode[from], wsNode[to], l.Kind)
				}
			}
		}
	}
	weigh(&m, m.Hub)
	return m
}

// entriesOf lists what a workstream holds in its lists' order: by input in
// health's order, each input's members, then the members nothing read, then
// what it joined.
func entriesOf(ws rawStream, order viewrules.Order) []entry {
	var inputs []string
	members, unread, joins := map[string][]entry{}, map[string][]entry{}, map[string][]entry{}
	seen := func(in string) {
		if !slices.Contains(inputs, in) {
			inputs = append(inputs, in)
		}
	}
	for i := range ws.Items {
		p := &ws.Items[i]
		seen(p.Input)
		e := entry{handle: p.Input + ":" + p.Item.ID, input: p.Input, id: p.Item.ID, placed: p}
		if p.Joined != nil {
			joins[p.Input] = append(joins[p.Input], e)
			continue
		}
		members[p.Input] = append(members[p.Input], e)
	}
	for _, m := range ws.MembersUnread {
		in, id, _ := strings.Cut(m, ":")
		seen(in)
		unread[in] = append(unread[in], entry{handle: m, input: in, id: id})
	}
	slices.SortStableFunc(inputs, func(a, b string) int { return order.Rank(a) - order.Rank(b) })
	var out []entry
	for _, in := range inputs {
		out = slices.Concat(out, members[in], unread[in], joins[in])
	}
	return out
}

// holdersOf is, for each item more than one workstream holds, those
// workstreams in stated order.
func holdersOf(entries [][]entry) map[string][]int {
	all := map[string][]int{}
	for i, es := range entries {
		for _, e := range es {
			if e.placed != nil && !slices.Contains(all[e.handle], i) {
				all[e.handle] = append(all[e.handle], i)
			}
		}
	}
	for h, ws := range all {
		if len(ws) < 2 {
			delete(all, h)
		}
	}
	return all
}

// homes is the one workstream each item is drawn under: the first in stated
// order holding it as a member, since membership is what the user stated and
// a join is what lookout matched; failing that, the first holding it at all.
func homes(entries [][]entry) map[string]int {
	home := map[string]int{}
	member := map[string]bool{}
	for i, es := range entries {
		for _, e := range es {
			if e.placed == nil {
				continue
			}
			isMember := e.placed.Joined == nil
			if _, ok := home[e.handle]; !ok || (isMember && !member[e.handle]) {
				home[e.handle] = i
				member[e.handle] = isMember
			}
		}
	}
	return home
}

func parentHandle(es []entry, handle string) string {
	for _, e := range es {
		if e.handle == handle && e.placed != nil && e.placed.Item.Parent != "" {
			return e.input + ":" + e.placed.Item.Parent
		}
	}
	return ""
}

// pointer is the one place an entry hangs from, as an index into the
// workstream's entries, or -1 for the workstream itself, with the kind of
// edge it follows.
type pointer struct {
	to   int
	kind string
}

// place hangs each entry a workstream draws under one parent, the first that
// applies: the pull request a branch came through, the member a join matched,
// the item's parent where the same workstream holds it, or the workstream.
// What the tree cannot show of these becomes a cross-link.
func place(m *viewModel, links *linkSet, es []entry, drawn map[string]int, ws int, placed map[string]int) {
	at := map[string]int{}
	for i, e := range es {
		if _, ok := at[e.handle]; !ok {
			at[e.handle] = i
		}
	}
	ptr := make([]pointer, len(es))
	for i, e := range es {
		ptr[i] = pointer{to: -1}
		j := e.join()
		parent := -1
		if e.placed != nil && e.placed.Item.Parent != "" {
			if k, ok := at[e.input+":"+e.placed.Item.Parent]; ok && k != i {
				parent = k
			}
		}
		switch {
		case j != nil && j.By == "pull_request" && j.Via != "" && hasHandle(at, j.Via):
			ptr[i] = pointer{to: at[j.Via], kind: "joined"}
		case j != nil && hasHandle(at, j.Member):
			ptr[i] = pointer{to: at[j.Member], kind: "joined"}
		case parent >= 0:
			ptr[i] = pointer{to: parent, kind: "parent"}
		}
		// A join hangs by how it joined, so its parent, where this
		// workstream holds it, is drawn as a cross-link instead.
		if j != nil && parent >= 0 && ptr[i].to != parent {
			if from, ok := drawn[e.handle]; ok {
				links.add(from, nodeFor(es[parent], drawn, placed, ws), "parent")
			}
		}
	}
	breakCycles(es, ptr, func(from, to int, kind string) {
		if n, ok := drawn[es[from].handle]; ok {
			links.add(n, nodeFor(es[to], drawn, placed, ws), kind)
		}
	})
	for i, e := range es {
		n, ok := drawn[e.handle]
		if !ok || at[e.handle] != i {
			continue
		}
		parent := ws
		// An entry hangs from the nearest one up its pointers that this
		// workstream draws, so a shared item drawn elsewhere passes what
		// hangs from it here on to its own place.
		for k := ptr[i].to; k >= 0; k = ptr[k].to {
			if p, ok := drawn[es[k].handle]; ok {
				parent = p
				break
			}
		}
		m.Nodes[n].Parent = parent
		m.Nodes[parent].Children = append(m.Nodes[parent].Children, n)
	}
}

func hasHandle(at map[string]int, h string) bool { _, ok := at[h]; return ok }

// nodeFor is where an entry is drawn: here, or where its home draws it.
func nodeFor(e entry, drawn map[string]int, placed map[string]int, ws int) int {
	if n, ok := drawn[e.handle]; ok {
		return n
	}
	if n, ok := placed[e.handle]; ok {
		return n
	}
	return ws
}

// breakCycles cuts every cycle the pointers form, joins and parents together,
// at the cycle's entry earliest in the lists, which then hangs from the
// workstream; cut is told each edge given up.
func breakCycles(es []entry, ptr []pointer, cut func(from, to int, kind string)) {
	const unseen, onPath, done = 0, 1, 2
	state := make([]int, len(es))
	for start := range es {
		var path []int
		k := start
		for k >= 0 && state[k] == unseen {
			state[k] = onPath
			path = append(path, k)
			k = ptr[k].to
		}
		if k >= 0 && state[k] == onPath {
			cycle := path[slices.Index(path, k):]
			first := slices.Min(cycle)
			cut(first, ptr[first].to, ptr[first].kind)
			ptr[first] = pointer{to: -1}
		}
		for _, p := range path {
			state[p] = done
		}
	}
}

// linkSet adds each cross-link once, between two different nodes.
type linkSet struct {
	model *viewModel
	seen  map[[2]int]bool
}

func (s *linkSet) add(from, to int, kind string) {
	if from == to {
		return
	}
	if s.seen == nil {
		s.seen = map[[2]int]bool{}
	}
	if s.seen[[2]int{from, to}] {
		return
	}
	s.seen[[2]int{from, to}] = true
	s.model.Links = append(s.model.Links, crossLink{From: from, To: to, Kind: viewrules.Clean(kind), Label: linkLabel(kind)})
}

func linkLabel(kind string) string {
	switch kind {
	case "shared":
		return "shared"
	case "parent":
		return "parent"
	case "joined":
		return "joined"
	}
	return viewrules.Clean(kind)
}

func workstreamNode(ws rawStream, shown workstream, hub int) mapNode {
	n := mapNode{Kind: "workstream", Handle: rawText(ws.Name), Ref: shown.Name, Anchor: shown.Anchor, Parent: hub}
	n.Label = shown.Name
	if shown.Title != "" {
		n.Label, n.Sub = shown.Title, shown.Name
	}
	var flag []string
	if len(shown.Incomplete) > 0 {
		n.Words = append(n.Words, "draws on a failing input: "+strings.Join(shown.Incomplete, ", "))
		flag = append(flag, "!")
	}
	switch {
	case shown.Unread:
		n.Words = append(n.Words, "nothing read")
		flag = append(flag, "?")
	case shown.Stale:
		n.Words = append(n.Words, "stale")
		flag = append(flag, "\u25f7")
	}
	if shown.Behind {
		n.Words = append(n.Words, "behind its rank")
		flag = append(flag, "↓")
	}
	n.Flag = strings.Join(flag, "")
	n.HiddenChars = viewrules.Clean(ws.Name) != ws.Name
	n.Name = nameOf(n.Label, n.Sub, n.Words)
	return n
}

func itemNode(e entry) mapNode {
	label := viewrules.Clean(e.id)
	n := mapNode{Kind: "item", Handle: rawText(e.handle), Ref: viewrules.Clean(e.handle), Label: label, HiddenChars: viewrules.Clean(e.handle) != e.handle}
	if e.placed == nil {
		n.Kind = "unread"
		n.Words = []string{"not read: no input supplied it"}
		n.Name = nameOf(n.Ref, "", n.Words)
		return n
	}
	shown := itemOf(*e.placed)
	n.Sub, n.State, n.Href = shown.Title, shown.State, shown.Href
	if shown.State != "" {
		n.Words = append(n.Words, shown.State)
	}
	if shown.Join != nil {
		n.JoinedBy = shown.Join.By
		n.Words = append(n.Words, "joined by "+shown.Join.By)
	}
	n.Name = nameOf(n.Ref, n.Sub, n.Words)
	return n
}

func nameOf(label, sub string, words []string) string {
	name := label
	if sub != "" {
		name += " " + sub
	}
	if len(words) > 0 {
		name += ", " + strings.Join(words, ", ")
	}
	return name
}

// weigh sets each node's leaf weight and what it says when collapsed,
// returning its weight, the nodes beneath it, and how many of those are
// members nothing read.
func weigh(m *viewModel, n int) (weight, beneath, unread int) {
	node := &m.Nodes[n]
	for _, c := range node.Children {
		w, b, u := weigh(m, c)
		weight += w
		beneath += b + 1
		unread += u
		if m.Nodes[c].Kind == "unread" {
			unread++
		}
	}
	node = &m.Nodes[n]
	if len(node.Children) == 0 {
		node.Weight = 1
		return 1, 0, 0
	}
	node.Weight = weight
	switch node.Kind {
	case "setting":
		node.Rollup = count(len(node.Children), "workstream")
	default:
		node.Rollup = count(beneath, "item")
		if unread > 0 {
			node.Rollup += fmt.Sprintf(", %d not read", unread)
		}
	}
	return weight, beneath, unread
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// MarshalJSON keeps a node's lists from marshaling as null, so the script
// iterates every one without a check.
func (n mapNode) MarshalJSON() ([]byte, error) {
	type plain mapNode
	if n.Words == nil {
		n.Words = []string{}
	}
	if n.Children == nil {
		n.Children = []int{}
	}
	return json.Marshal(plain(n))
}

// Package readout assembles what `lookout read --json` emits, in the shape
// schema/readout.v1.json publishes.
package readout

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
)

// Item is one item an input reported. Raw is the item in supplied.v1's item
// shape as its input reported it, less its notes, passed through to the
// readout so no other source detail is lost.
type Item struct {
	ID          string
	Title       string
	LastTouched time.Time
	StartedAt   *time.Time
	// Key is the key the item carries, as the setting's key pattern finds it,
	// or empty. A supplied item carries none: its own id is the key.
	Key string
	// Keys is every key a pull request's description carries.
	Keys []string
	// Notes is a supplied item's notes, searched for every key they carry. It
	// never reaches the readout, since it can hold anything a work note says.
	Notes string
	// Head is the id a git or Claude Code input gives the branch an open pull
	// request was made from, or empty.
	Head string
	// Parent is the id of the item, in the same input, this one sits beneath,
	// or empty. It may name an item no input supplied, or the item itself.
	Parent string
	Raw    json.RawMessage
}

// Reading is what one input contributed: its health, and the items a
// workstream may use, or, from an awaiting input, the replies owed it reported.
type Reading struct {
	Health   health.Record
	Items    []Item
	Awaiting []Awaiting
}

// Awaiting is one reply owed as its input reported it: the facts only. Build
// alone decides whether it is still waiting, so every input that reports one
// is judged by the same rule.
type Awaiting struct {
	ID         string
	Title      string
	Kind       string
	AskedAt    time.Time
	AnsweredAt *time.Time
	// Subject is the item the thread concerns, as <input>:<id>, or empty.
	Subject string
	Links   json.RawMessage
	Attrs   json.RawMessage
}

// waiting reports whether a is still owed: the user has not answered since
// the latest ask. An answer in the same instant as the ask is the later word.
func (a Awaiting) waiting() bool {
	return a.AnsweredAt == nil || a.AnsweredAt.Before(a.AskedAt)
}

// AwaitingEntry is one reply still owed, as the readout publishes it, with
// where its subject is shown.
type AwaitingEntry struct {
	Input       string          `json:"input"`
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Kind        string          `json:"kind"`
	AskedAt     time.Time       `json:"asked_at"`
	AnsweredAt  *time.Time      `json:"answered_at"`
	Subject     string          `json:"subject,omitempty"`
	Links       json.RawMessage `json:"links"`
	Attrs       json.RawMessage `json:"attrs"`
	Workstreams []string        `json:"workstreams"`
	Unplaced    bool            `json:"unplaced"`
}

// Scope is what a setting admits from an input lookout reads itself.
type Scope struct {
	Owners []config.Owner
	// Since is the earliest touch an unclaimed item may have and still be
	// offered. An item Members names, and a git branch or a pull request a key
	// search found whose key Keys places, is read whatever its age; a Claude
	// Code session a key places, and a branch or session only its pull request
	// places, is read only since Since.
	Since   time.Time
	Members map[string]bool
	// Keys finds the key an item carries; nil where the setting has no key
	// pattern.
	Keys *config.Keys
}

// Readout is one setting's readout.
type Readout struct {
	Schema             string          `json:"schema"`
	Setting            string          `json:"setting"`
	GeneratedAt        time.Time       `json:"generated_at"`
	EventWindowSeconds int64           `json:"event_window_seconds"`
	Workstreams        []Workstream    `json:"workstreams"`
	Unplaced           []Placed        `json:"unplaced"`
	Health             []health.Record `json:"health"`
	DismissedCount     int             `json:"dismissed_count"`
	CompletedCount     int             `json:"completed_count"`
	WorkstreamLinks    []Link          `json:"workstream_links"`
	AwaitingInputs     []string        `json:"awaiting_inputs"`
	Awaiting           []AwaitingEntry `json:"awaiting"`
}

// Link relates two live workstreams through the items behind it, each as
// <input>:<id>.
type Link struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Kind  string   `json:"kind"`
	Items []string `json:"items"`
}

// The values of Link.Kind: both workstreams hold one item, or To holds an item
// whose parent From holds.
const (
	linkSharedItem = "shared_item"
	linkParent     = "parent"
)

// Workstream is one workstream's figures.
type Workstream struct {
	Name        string     `json:"name"`
	Title       string     `json:"title,omitempty"`
	LastTouched *time.Time `json:"last_touched"`
	IdleSeconds *int64     `json:"idle_seconds"`
	StartedAt   *time.Time `json:"started_at"`
	AgeSeconds  *int64     `json:"age_seconds"`
	Events      []Event    `json:"events"`
	// Active and Behind are decided here, once, so that no view compares an
	// idle time with the window, or one workstream with another, on its own.
	Active           bool     `json:"active"`
	Behind           bool     `json:"behind"`
	InputsIncomplete []string `json:"inputs_incomplete"`
	MembersUnread    []string `json:"members_unread"`
	Items            []Placed `json:"items"`
}

// Event is one touch within the event window.
type Event struct {
	Input  string    `json:"input"`
	ID     string    `json:"id"`
	Title  string    `json:"title,omitempty"`
	At     time.Time `json:"at"`
	Joined *Joined   `json:"joined,omitempty"`
}

// Placed is an item together with the input it came from. Joined is set on an
// item a workstream holds without naming it.
type Placed struct {
	Input  string          `json:"input"`
	Item   json.RawMessage `json:"item"`
	Joined *Joined         `json:"joined,omitempty"`
}

// Joined says why a workstream holds an item it does not name: By is how the
// item was matched, Member, as <input>:<id>, the member it was matched to, and
// Via the pull request it joined through, where it did.
type Joined struct {
	By     string `json:"by"`
	Member string `json:"member"`
	Via    string `json:"via,omitempty"`
}

// The values of Joined.By: an item whose title or branch key is a member's id,
// a pull request whose description names a member's id, a supplied item whose
// notes name a member's id, a branch or session an open pull request the
// workstream holds was made from, and an item whose parent, or an ancestor
// reached through parents, is a member.
const (
	byKey         = "key"
	byDescription = "description"
	byNotes       = "notes"
	byPullRequest = "pull_request"
	byParent      = "parent"
)

// Build assembles the readout for cfg from each expected input's reading,
// keyed by input name, as of now. dismissed maps an item, as <input>:<id>, to
// when the user last dismissed it.
func Build(cfg *config.Config, readings map[string]Reading, dismissed map[string]time.Time, now time.Time) Readout {
	// A completed date is a day on the user's own clock, so it ends in now's zone.
	loc := now.Location()
	now = now.UTC()
	out := Readout{
		Schema:             "readout.v1",
		Setting:            cfg.Setting,
		GeneratedAt:        now,
		EventWindowSeconds: int64(cfg.EventWindow / time.Second),
		Workstreams:        make([]Workstream, 0, len(cfg.Workstreams)),
		Unplaced:           make([]Placed, 0),
		Health:             make([]health.Record, 0, len(cfg.Inputs)),
	}

	type key struct{ input, id string }
	// claimed holds when each member's claim ends: never, the zero time, for a
	// live workstream's members, and the end of its completed date for a finished
	// one's, so an item that moves again is seen.
	claimed := map[key]time.Time{}
	for _, ws := range cfg.Workstreams {
		if ws.Completed == "" {
			for _, m := range ws.Members {
				claimed[key{m.Input, m.ID}] = time.Time{}
			}
			continue
		}
		out.CompletedCount++
		until := ws.CompletedThrough(loc)
		for _, m := range ws.Members {
			if prev, ok := claimed[key{m.Input, m.ID}]; ok && (prev.IsZero() || !until.After(prev)) {
				continue
			}
			claimed[key{m.Input, m.ID}] = until
		}
	}

	// liveOwner is the first live workstream, in stated order, naming each
	// member, so the nearest adopted ancestor of an item can place it.
	liveOwner := map[string]string{}
	for _, ws := range cfg.Workstreams {
		if ws.Completed != "" {
			continue
		}
		for _, m := range ws.Members {
			if _, ok := liveOwner[m.Input+":"+m.ID]; !ok {
				liveOwner[m.Input+":"+m.ID] = ws.Name
			}
		}
	}

	// Each item no claim holds is left out where a windowed supplier holds it
	// past the window and nothing joins it, kept out by a dismissal it has not
	// moved since, joined to every workstream a key places it in and to that
	// of its nearest adopted ancestor, at most once each, or offered.
	keys := cfg.Keys()
	type join struct {
		input  string
		item   Item
		joined Joined
	}
	joins := map[string][]join{}
	joinedAny := map[key]bool{}
	add := func(workstream, input string, it Item, joined Joined) {
		if slices.ContainsFunc(joins[workstream], func(j join) bool { return j.input == input && j.item.ID == it.ID }) {
			return
		}
		joins[workstream] = append(joins[workstream], join{input, it, joined})
		joinedAny[key{input, it.ID}] = true
	}
	type offer struct {
		input string
		item  Item
	}
	type placement struct {
		workstream string
		joined     Joined
	}
	discoveredSince := now.Add(-cfg.DiscoveryWindow)
	var free []offer
	for _, in := range cfg.Inputs {
		reading := readings[in.Name]
		out.Health = append(out.Health, reading.Health)
		byID := make(map[string]Item, len(reading.Items))
		for _, it := range reading.Items {
			byID[it.ID] = it
		}
		for _, it := range reading.Items {
			if until, ok := claimed[key{in.Name, it.ID}]; ok && (until.IsZero() || it.LastTouched.Before(until)) {
				continue
			}
			var places []placement
			if owner, ok := keys.Owner(it.Key); ok {
				places = append(places, placement{owner.Workstream, Joined{By: byKey, Member: owner.Member.Input + ":" + owner.Member.ID}})
			}
			for _, k := range it.Keys {
				if owner, ok := keys.Owner(k); ok {
					places = append(places, placement{owner.Workstream, Joined{By: byDescription, Member: owner.Member.Input + ":" + owner.Member.ID}})
				}
			}
			for _, k := range keys.All(it.Notes) {
				if owner, ok := keys.Owner(k); ok {
					places = append(places, placement{owner.Workstream, Joined{By: byNotes, Member: owner.Member.Input + ":" + owner.Member.ID}})
				}
			}
			// A key, description or notes join to the same workstream comes
			// first, and add keeps one join per workstream, so this marks only
			// what nothing else placed there.
			if ws, member, ok := ancestor(in.Name, it, byID, liveOwner); ok {
				places = append(places, placement{ws, Joined{By: byParent, Member: member}})
			}
			// A windowed supplier's item that nothing places and that has not
			// moved within the window is never offered, so neither is it counted
			// as dismissed, as an input lookout reads itself never reads one. A
			// supplied item follows no pull request, so these are every join that
			// could keep it.
			if in.Windowed && len(places) == 0 && it.LastTouched.Before(discoveredSince) {
				continue
			}
			if at, ok := dismissed[in.Name+":"+it.ID]; ok && !it.LastTouched.After(at) {
				out.DismissedCount++
				continue
			}
			free = append(free, offer{in.Name, it})
			for _, p := range places {
				add(p.workstream, in.Name, it, p.joined)
			}
		}
	}

	// A branch or session follows the open pull request it was made from into
	// each live workstream holding that pull request, whether as a member or as
	// a join, so work need not name its ticket in a branch.
	type source struct{ workstream, member, via string }
	heads := map[string][]source{}
	sourced := map[string]bool{}
	if keys != nil {
		for _, ws := range cfg.Workstreams {
			if ws.Completed != "" {
				continue
			}
			for _, m := range ws.Members {
				items := readings[m.Input].Items
				if i := slices.IndexFunc(items, func(it Item) bool { return it.ID == m.ID }); i >= 0 && items[i].Head != "" {
					token := m.Input + ":" + m.ID
					heads[items[i].Head] = append(heads[items[i].Head], source{ws.Name, token, token})
					sourced[ws.Name] = true
				}
			}
			for _, j := range joins[ws.Name] {
				if j.item.Head != "" {
					heads[j.item.Head] = append(heads[j.item.Head], source{ws.Name, j.joined.Member, j.input + ":" + j.item.ID})
					sourced[ws.Name] = true
				}
			}
		}
	}
	for _, o := range free {
		if kind := cfg.Inputs[inputIndex(cfg, o.input)].Kind; kind != config.KindGit && kind != config.KindClaudeCode {
			continue
		}
		for _, s := range heads[o.item.ID] {
			add(s.workstream, o.input, o.item, Joined{By: byPullRequest, Member: s.member, Via: s.via})
		}
	}
	unplaced := map[key]bool{}
	for _, o := range free {
		if !joinedAny[key{o.input, o.item.ID}] {
			out.Unplaced = append(out.Unplaced, Placed{Input: o.input, Item: o.item.Raw})
			unplaced[key{o.input, o.item.ID}] = true
		}
	}

	// held is every live workstream holding each item, in stated order, and
	// holds each workstream's items, so links can be read off placement.
	held := map[key][]string{}
	type holding struct {
		input string
		item  Item
	}
	holds := map[string][]holding{}
	for _, ws := range cfg.Workstreams {
		if ws.Completed != "" {
			continue
		}
		w := Workstream{Name: ws.Name, Title: ws.Title, Events: make([]Event, 0), InputsIncomplete: make([]string, 0), MembersUnread: make([]string, 0), Items: make([]Placed, 0, len(ws.Members))}
		incomplete := func(input string) {
			status := readings[input].Health.Status
			if (status == health.Error || status == health.Degraded) && !slices.Contains(w.InputsIncomplete, input) {
				w.InputsIncomplete = append(w.InputsIncomplete, input)
			}
		}
		place := func(input string, it Item, joined *Joined) {
			w.Items = append(w.Items, Placed{Input: input, Item: it.Raw, Joined: joined})
			if k := (key{input, it.ID}); !slices.Contains(held[k], ws.Name) {
				held[k] = append(held[k], ws.Name)
				holds[ws.Name] = append(holds[ws.Name], holding{input, it})
			}
			touched := it.LastTouched.UTC()
			if w.LastTouched == nil || touched.After(*w.LastTouched) {
				w.LastTouched = &touched
			}
			if it.StartedAt != nil {
				started := it.StartedAt.UTC()
				if w.StartedAt == nil || started.Before(*w.StartedAt) {
					w.StartedAt = &started
				}
			}
			if !touched.Before(now.Add(-cfg.EventWindow)) {
				w.Events = append(w.Events, Event{Input: input, ID: it.ID, Title: it.Title, At: touched, Joined: joined})
			}
		}
		for _, m := range ws.Members {
			incomplete(m.Input)
			reading := readings[m.Input]
			i := slices.IndexFunc(reading.Items, func(it Item) bool { return it.ID == m.ID })
			if i < 0 {
				w.MembersUnread = append(w.MembersUnread, m.Input+":"+m.ID)
				continue
			}
			place(m.Input, reading.Items[i], nil)
		}
		// A workstream a key or a pull request places work in draws on every
		// input that work can come from, so one that failed may have kept a
		// join from it. Apart from its parent, which is in its member's own
		// input, a supplied item joins only through the keys in its notes, so
		// only a workstream a key places work in draws on another supplied
		// input. An awaiting input places nothing, so no workstream draws on it.
		if placed := keys.Places(ws.Name); placed || sourced[ws.Name] {
			for _, in := range cfg.Inputs {
				if in.Kind != config.KindAwaiting && (placed || in.Kind != config.KindSupplied) {
					incomplete(in.Name)
				}
			}
		}
		joined := joins[ws.Name]
		slices.SortStableFunc(joined, func(a, b join) int {
			return cmp.Or(cmp.Compare(inputIndex(cfg, a.input), inputIndex(cfg, b.input)), cmp.Compare(a.item.ID, b.item.ID))
		})
		for _, j := range joined {
			place(j.input, j.item, &j.joined)
		}
		// Report inputs in the configuration's order rather than the order members
		// happened to name them, so the list reads the same across workstreams.
		slices.SortFunc(w.InputsIncomplete, func(a, b string) int {
			return cmp.Compare(inputIndex(cfg, a), inputIndex(cfg, b))
		})
		slices.SortStableFunc(w.Events, func(a, b Event) int { return b.At.Compare(a.At) })
		w.Active = len(w.Events) > 0
		w.IdleSeconds = since(now, w.LastTouched)
		w.AgeSeconds = since(now, w.StartedAt)
		out.Workstreams = append(out.Workstreams, w)
	}
	for i := range out.Workstreams {
		out.Workstreams[i].Behind = behind(out.Workstreams, i)
	}

	// A link gathers its items as it finds them, then each is sorted once. A
	// parent is looked up one step only, so a cycle of parents needs no
	// guarding, and an item naming itself its parent relates nothing.
	type linkKey struct{ from, to, kind string }
	found := map[linkKey][]string{}
	relate := func(from, to, kind, item string) {
		k := linkKey{from, to, kind}
		if !slices.Contains(found[k], item) {
			found[k] = append(found[k], item)
		}
	}
	for k, names := range held {
		for i, from := range names {
			for _, to := range names[i+1:] {
				relate(from, to, linkSharedItem, k.input+":"+k.id)
			}
		}
	}
	for _, w := range out.Workstreams {
		for _, h := range holds[w.Name] {
			if h.item.Parent == "" || h.item.Parent == h.item.ID {
				continue
			}
			for _, from := range held[key{h.input, h.item.Parent}] {
				if from != w.Name {
					relate(from, w.Name, linkParent, h.input+":"+h.item.ID)
				}
			}
		}
	}
	rank := func(name string) int {
		return slices.IndexFunc(out.Workstreams, func(w Workstream) bool { return w.Name == name })
	}
	out.WorkstreamLinks = make([]Link, 0, len(found))
	for k, items := range found {
		slices.Sort(items)
		out.WorkstreamLinks = append(out.WorkstreamLinks, Link{From: k.from, To: k.to, Kind: k.kind, Items: items})
	}
	slices.SortFunc(out.WorkstreamLinks, func(a, b Link) int {
		return cmp.Or(cmp.Compare(rank(a.From), rank(b.From)), cmp.Compare(rank(a.To), rank(b.To)), cmp.Compare(a.Kind, b.Kind))
	})

	out.AwaitingInputs = make([]string, 0)
	out.Awaiting = make([]AwaitingEntry, 0)
	for _, in := range cfg.Inputs {
		if in.Kind != config.KindAwaiting {
			continue
		}
		out.AwaitingInputs = append(out.AwaitingInputs, in.Name)
		var entries []AwaitingEntry
		for _, a := range readings[in.Name].Awaiting {
			if !a.waiting() {
				continue
			}
			e := AwaitingEntry{Input: in.Name, ID: a.ID, Title: a.Title, Kind: a.Kind, AskedAt: a.AskedAt.UTC(), Subject: a.Subject, Links: a.Links, Attrs: a.Attrs, Workstreams: make([]string, 0)}
			if a.AnsweredAt != nil {
				answered := a.AnsweredAt.UTC()
				e.AnsweredAt = &answered
			}
			// A subject is pointed at wherever the readout shows it. A member is
			// named even where its input failed this run, since the workstream
			// still holds it by configuration.
			if input, id, ok := strings.Cut(a.Subject, ":"); ok {
				k := key{input, id}
				for _, ws := range cfg.Workstreams {
					if ws.Completed == "" && (slices.Contains(ws.Members, config.Member{Input: input, ID: id}) || slices.Contains(held[k], ws.Name)) {
						e.Workstreams = append(e.Workstreams, ws.Name)
					}
				}
				e.Unplaced = unplaced[k]
			}
			entries = append(entries, e)
		}
		slices.SortStableFunc(entries, func(a, b AwaitingEntry) int {
			return cmp.Or(b.AskedAt.Compare(a.AskedAt), cmp.Compare(a.ID, b.ID))
		})
		out.Awaiting = append(out.Awaiting, entries...)
	}
	return out
}

// ancestor finds the nearest of its ancestors in the same input that a live
// workstream names, returning that workstream and the ancestor as
// <input>:<id>, the form liveOwner is keyed by. A parent is checked against
// liveOwner before it is looked up, so an adopted parent the input did not
// supply still places its children; one that is neither adopted nor supplied
// ends the chain, as does a loop. byID holds every item the input read,
// dismissed and claimed ones too, since a dismissal or a claim keeps out only
// the item it names, never what sits beneath it.
func ancestor(input string, it Item, byID map[string]Item, liveOwner map[string]string) (string, string, bool) {
	seen := map[string]bool{it.ID: true}
	for parent := it.Parent; parent != "" && !seen[parent]; {
		if ws, ok := liveOwner[input+":"+parent]; ok {
			return ws, input + ":" + parent, true
		}
		seen[parent] = true
		next, ok := byID[parent]
		if !ok {
			break
		}
		parent = next.Parent
	}
	return "", "", false
}

func inputIndex(cfg *config.Config, name string) int {
	return slices.IndexFunc(cfg.Inputs, func(i config.Input) bool { return i.Name == name })
}

// behind reports whether the workstream at i has gone quieter than one the
// stated order ranks below it. The vision ranks by stated order and lets
// observed staleness flag a workstream falling behind it; a workstream with
// nothing read has no touch to compare either way.
func behind(all []Workstream, i int) bool {
	idle := all[i].IdleSeconds
	if idle == nil {
		return false
	}
	return slices.ContainsFunc(all[i+1:], func(below Workstream) bool {
		return below.IdleSeconds != nil && *below.IdleSeconds < *idle
	})
}

// since is whole seconds from t to now, never negative: a touch stamped ahead
// of this machine's clock counts as just now.
func since(now time.Time, t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	s := max(int64(now.Sub(*t)/time.Second), 0)
	return &s
}

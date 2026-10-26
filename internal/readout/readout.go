// Package readout assembles what `lookout read --json` emits, in the shape
// schema/readout.v1.json publishes.
package readout

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
)

// Item is one item an input reported. Raw is the item in supplied.v1's item
// shape, exactly as its input reported it, passed through to the readout so no
// source detail is lost.
type Item struct {
	ID          string
	Title       string
	LastTouched time.Time
	StartedAt   *time.Time
	// Key is the key the item carries, as the setting's key pattern finds it,
	// or empty. A supplied item carries none: its own id is the key.
	Key string
	Raw json.RawMessage
}

// Reading is what one input contributed: its health, and the items a
// workstream may use.
type Reading struct {
	Health health.Record
	Items  []Item
}

// Scope is what a setting admits from an input lookout reads itself.
type Scope struct {
	Owners []config.Owner
	// Since is the earliest touch an unclaimed item may have and still be
	// offered; an item Members names, or one whose key Keys places, is read
	// whatever its age.
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
}

// Workstream is one workstream's figures.
type Workstream struct {
	Name             string     `json:"name"`
	LastTouched      *time.Time `json:"last_touched"`
	IdleSeconds      *int64     `json:"idle_seconds"`
	StartedAt        *time.Time `json:"started_at"`
	AgeSeconds       *int64     `json:"age_seconds"`
	Events           []Event    `json:"events"`
	InputsIncomplete []string   `json:"inputs_incomplete"`
	MembersUnread    []string   `json:"members_unread"`
	Items            []Placed   `json:"items"`
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
// item was matched, and Member, as <input>:<id>, the member it was matched to.
type Joined struct {
	By     string `json:"by"`
	Member string `json:"member"`
}

// byKey is the Joined.By of an item whose key is a member's id.
const byKey = "key"

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

	// Each item no claim holds is joined to the workstream its key places it
	// in, kept out by a dismissal it has not moved since, or offered.
	keys := cfg.Keys()
	type join struct {
		input string
		item  Item
		by    config.KeyOwner
	}
	joins := map[string][]join{}
	for _, in := range cfg.Inputs {
		reading := readings[in.Name]
		out.Health = append(out.Health, reading.Health)
		for _, it := range reading.Items {
			if until, ok := claimed[key{in.Name, it.ID}]; ok && (until.IsZero() || it.LastTouched.Before(until)) {
				continue
			}
			if at, ok := dismissed[in.Name+":"+it.ID]; ok && !it.LastTouched.After(at) {
				out.DismissedCount++
				continue
			}
			if owner, ok := keys.Owner(it.Key); ok {
				joins[owner.Workstream] = append(joins[owner.Workstream], join{in.Name, it, owner})
				continue
			}
			out.Unplaced = append(out.Unplaced, Placed{Input: in.Name, Item: it.Raw})
		}
	}

	for _, ws := range cfg.Workstreams {
		if ws.Completed != "" {
			continue
		}
		w := Workstream{Name: ws.Name, Events: make([]Event, 0), InputsIncomplete: make([]string, 0), MembersUnread: make([]string, 0), Items: make([]Placed, 0, len(ws.Members))}
		incomplete := func(input string) {
			status := readings[input].Health.Status
			if (status == health.Error || status == health.Degraded) && !slices.Contains(w.InputsIncomplete, input) {
				w.InputsIncomplete = append(w.InputsIncomplete, input)
			}
		}
		place := func(input string, it Item, joined *Joined) {
			w.Items = append(w.Items, Placed{Input: input, Item: it.Raw, Joined: joined})
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
		// A workstream a key places work in draws on every input that work can
		// come from, so one that failed may have kept a join from it.
		if keys.Places(ws.Name) {
			for _, in := range cfg.Inputs {
				if in.Kind != config.KindSupplied {
					incomplete(in.Name)
				}
			}
		}
		joined := joins[ws.Name]
		slices.SortStableFunc(joined, func(a, b join) int {
			return cmp.Or(cmp.Compare(inputIndex(cfg, a.input), inputIndex(cfg, b.input)), cmp.Compare(a.item.ID, b.item.ID))
		})
		for _, j := range joined {
			place(j.input, j.item, &Joined{By: byKey, Member: j.by.Member.Input + ":" + j.by.Member.ID})
		}
		// Report inputs in the configuration's order rather than the order members
		// happened to name them, so the list reads the same across workstreams.
		slices.SortFunc(w.InputsIncomplete, func(a, b string) int {
			return cmp.Compare(inputIndex(cfg, a), inputIndex(cfg, b))
		})
		slices.SortStableFunc(w.Events, func(a, b Event) int { return b.At.Compare(a.At) })
		w.IdleSeconds = since(now, w.LastTouched)
		w.AgeSeconds = since(now, w.StartedAt)
		out.Workstreams = append(out.Workstreams, w)
	}
	return out
}

func inputIndex(cfg *config.Config, name string) int {
	return slices.IndexFunc(cfg.Inputs, func(i config.Input) bool { return i.Name == name })
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

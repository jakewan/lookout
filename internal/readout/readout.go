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
	"github.com/jakewan/lookout/internal/supplied"
)

// Readout is one setting's readout.
type Readout struct {
	Schema             string          `json:"schema"`
	Setting            string          `json:"setting"`
	GeneratedAt        time.Time       `json:"generated_at"`
	EventWindowSeconds int64           `json:"event_window_seconds"`
	Workstreams        []Workstream    `json:"workstreams"`
	Unplaced           []Placed        `json:"unplaced"`
	Health             []health.Record `json:"health"`
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
}

// Event is one touch within the event window.
type Event struct {
	Input string    `json:"input"`
	ID    string    `json:"id"`
	Title string    `json:"title,omitempty"`
	At    time.Time `json:"at"`
}

// Placed is an item together with the input it came from.
type Placed struct {
	Input string          `json:"input"`
	Item  json.RawMessage `json:"item"`
}

// Build assembles the readout for cfg from each expected input's reading,
// keyed by input name, as of now.
func Build(cfg *config.Config, readings map[string]supplied.Reading, now time.Time) Readout {
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
	claimed := map[key]bool{}
	for _, ws := range cfg.Workstreams {
		w := Workstream{Name: ws.Name, Events: make([]Event, 0), InputsIncomplete: make([]string, 0)}
		for _, m := range ws.Members {
			claimed[key{m.Input, m.ID}] = true
			reading := readings[m.Input]
			unhealthy := reading.Health.Status == health.Error || reading.Health.Status == health.Degraded
			if unhealthy && !slices.Contains(w.InputsIncomplete, m.Input) {
				w.InputsIncomplete = append(w.InputsIncomplete, m.Input)
			}
			i := slices.IndexFunc(reading.Items, func(it supplied.Item) bool { return it.ID == m.ID })
			if i < 0 {
				continue
			}
			it := reading.Items[i]
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
				w.Events = append(w.Events, Event{Input: m.Input, ID: it.ID, Title: it.Title, At: touched})
			}
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

	for _, in := range cfg.Inputs {
		reading := readings[in.Name]
		out.Health = append(out.Health, reading.Health)
		for _, it := range reading.Items {
			if !claimed[key{in.Name, it.ID}] {
				out.Unplaced = append(out.Unplaced, Placed{Input: in.Name, Item: it.Raw})
			}
		}
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

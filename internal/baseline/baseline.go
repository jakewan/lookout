// Package baseline derives the record `lookout snapshot` keeps for each local
// date: the figures from which a later reading can count how often activity
// arrives, how it was handled, and how staleness, concurrency and completions
// move, without relying on anyone's recall.
package baseline

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
	"github.com/jakewan/lookout/internal/readout"
	"github.com/jakewan/lookout/internal/state"
)

// DateLayout is how a record's dates are written, a calendar date on the
// clock lookout runs under, as configuration dates are.
const DateLayout = "2006-01-02"

// Record is one local date's snapshot. Items are named by the <input>:<id>
// token adopt and dismiss take.
type Record struct {
	Date string `json:"date"`
	// PreviousDate is the date of the record before this one, so a skipped day
	// shows without a marker of its own.
	PreviousDate string    `json:"previous_date,omitempty"`
	At           time.Time `json:"at"`
	// Backlog, on the first record only, is what was already unplaced when
	// recording began; none of it ever counts as an arrival.
	Backlog                []string     `json:"backlog,omitempty"`
	ActiveWorkstreams      int          `json:"active_workstreams"`
	Staleness              Staleness    `json:"staleness"`
	CompletedSincePrevious int          `json:"completed_since_previous"`
	Unplaced               []string     `json:"unplaced"`
	Arrivals               []string     `json:"arrivals"`
	UnloggedEdit           bool         `json:"unlogged_edit"`
	Workstreams            []Workstream `json:"workstreams"`
	Health                 []Health     `json:"health"`
	Incomplete             bool         `json:"incomplete"`
}

// Staleness is time since last touched across the live workstreams with an
// item read. Those with none read have no such time, so they are counted
// rather than measured.
type Staleness struct {
	MedianSeconds     *int64 `json:"median_seconds,omitempty"`
	P85Seconds        *int64 `json:"p85_seconds,omitempty"`
	Workstreams       int    `json:"workstreams"`
	UnreadWorkstreams int    `json:"unread_workstreams"`
}

// Workstream is a workstream as the configuration held it, kept so the next
// record can tell whether the gestures logged in between explain every change.
type Workstream struct {
	Name      string   `json:"name"`
	Members   []string `json:"members"`
	Completed string   `json:"completed,omitempty"`
}

// Health is one input's status.
type Health struct {
	Input  string `json:"input"`
	Status string `json:"status"`
}

// Derive builds the record for now from the readout and the configuration it
// was built from. first and previous are the baseline's first and latest
// records, both nil when there are none yet; gestures is the whole gesture log.
func Derive(first, previous *Record, cfg *config.Config, out readout.Readout, gestures []state.Gesture, now time.Time) (Record, error) {
	r := Record{
		Date:        now.Format(DateLayout),
		At:          now.UTC(),
		Arrivals:    make([]string, 0),
		Workstreams: workstreams(cfg.Workstreams),
		Health:      make([]Health, 0, len(out.Health)),
	}
	unplaced, err := tokens(out.Unplaced)
	if err != nil {
		return Record{}, err
	}
	r.Unplaced = unplaced

	for _, w := range out.Workstreams {
		if len(w.Events) > 0 {
			r.ActiveWorkstreams++
		}
	}
	r.Staleness = staleness(out.Workstreams)
	for _, h := range out.Health {
		r.Health = append(r.Health, Health{Input: h.Input, Status: h.Status})
		r.Incomplete = r.Incomplete || h.Status == health.Error || h.Status == health.Degraded
	}

	// With no record before it, a completion counts only on its own date.
	after := now.AddDate(0, 0, -1).Format(DateLayout)
	if previous != nil {
		after = previous.Date
		r.PreviousDate = previous.Date
	}
	for _, w := range cfg.Workstreams {
		if w.Completed > after && w.Completed <= r.Date {
			r.CompletedSincePrevious++
		}
	}

	if previous == nil {
		r.Backlog = slices.Clone(unplaced)
		return r, nil
	}
	var since []state.Gesture
	for _, g := range gestures {
		if g.Outcome == state.OK && g.At.After(previous.At) {
			since = append(since, g)
		}
	}
	r.Arrivals = arrivals(first, previous, unplaced, since)
	r.UnloggedEdit = !slices.EqualFunc(workstreams(replay(previous.Workstreams, since, now.Location())), r.Workstreams, equalWorkstream)
	return r, nil
}

// arrivals is what is unplaced now, or was adopted or dismissed since the
// previous record, and was neither unplaced then nor in the first record's
// backlog. An item handled the day it appeared never reaches a snapshot's
// unplaced, so without the gestures the promptest upkeep would go uncounted.
func arrivals(first, previous *Record, unplaced []string, since []state.Gesture) []string {
	seen := map[string]bool{}
	for _, t := range previous.Unplaced {
		seen[t] = true
	}
	if first != nil {
		for _, t := range first.Backlog {
			seen[t] = true
		}
	}
	candidates := slices.Clone(unplaced)
	for _, g := range since {
		if g.Gesture == "adopt" || g.Gesture == "dismiss" {
			candidates = append(candidates, g.Item)
		}
	}
	out := make([]string, 0)
	for _, t := range candidates {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

// replay applies each logged configuration gesture to the workstreams a record
// held, giving what the configuration would hold had nothing else changed it.
// A complete gesture's date is the day it ran on, as complete itself writes.
func replay(held []Workstream, gestures []state.Gesture, loc *time.Location) []config.Workstream {
	ws := make([]config.Workstream, 0, len(held))
	for _, w := range held {
		cw := config.Workstream{Name: w.Name, Completed: w.Completed}
		for _, m := range w.Members {
			cw.Members = append(cw.Members, member(m))
		}
		ws = append(ws, cw)
	}
	for _, g := range gestures {
		switch g.Gesture {
		case "adopt":
			ws = config.Adopted(ws, member(g.Item), g.Workstream)
		case "order":
			ws = config.Ordered(ws, g.Workstreams)
		case "complete":
			ws = config.CompletedOn(ws, g.Workstream, g.At.In(loc).Format(DateLayout))
		}
	}
	return ws
}

// member reads a token; an input name never holds a colon, so the first one
// ends it.
func member(token string) config.Member {
	input, id, _ := strings.Cut(token, ":")
	return config.Member{Input: input, ID: id}
}

func workstreams(ws []config.Workstream) []Workstream {
	out := make([]Workstream, 0, len(ws))
	for _, w := range ws {
		members := make([]string, 0, len(w.Members))
		for _, m := range w.Members {
			members = append(members, m.Input+":"+m.ID)
		}
		out = append(out, Workstream{Name: w.Name, Members: members, Completed: w.Completed})
	}
	return out
}

func equalWorkstream(a, b Workstream) bool {
	return a.Name == b.Name && a.Completed == b.Completed && slices.Equal(a.Members, b.Members)
}

func tokens(placed []readout.Placed) ([]string, error) {
	out := make([]string, 0, len(placed))
	for _, p := range placed {
		var it struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p.Item, &it); err != nil {
			return nil, fmt.Errorf("reading an item of %s: %w", p.Input, err)
		}
		out = append(out, p.Input+":"+it.ID)
	}
	slices.Sort(out)
	return out, nil
}

// staleness takes the median and p85 by nearest rank, so each figure is a
// time some workstream actually had.
func staleness(ws []readout.Workstream) Staleness {
	var idle []int64
	s := Staleness{}
	for _, w := range ws {
		if w.IdleSeconds == nil {
			s.UnreadWorkstreams++
			continue
		}
		idle = append(idle, *w.IdleSeconds)
	}
	s.Workstreams = len(idle)
	if len(idle) == 0 {
		return s
	}
	slices.Sort(idle)
	rank := func(p float64) *int64 {
		v := idle[int(math.Ceil(p*float64(len(idle))))-1]
		return &v
	}
	s.MedianSeconds, s.P85Seconds = rank(0.5), rank(0.85)
	return s
}

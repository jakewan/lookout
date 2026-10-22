// Package baseline derives the record `lookout snapshot` keeps for each local
// date: the figures from which a later reading can count how often activity
// arrives, how it was handled, and how staleness, concurrency and completions
// move, without relying on anyone's recall.
package baseline

import (
	"crypto/sha256"
	"encoding/hex"
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
	// Backlog is what an input showed before it had a healthy record to measure
	// arrivals against: everything unplaced on the first record, and on a later
	// one, what an input failing until then, or newly configured, showed. None
	// of it ever counts as an arrival.
	Backlog                []string  `json:"backlog,omitempty"`
	ActiveWorkstreams      int       `json:"active_workstreams"`
	Staleness              Staleness `json:"staleness"`
	CompletedSincePrevious int       `json:"completed_since_previous"`
	Unplaced               []string  `json:"unplaced"`
	Arrivals               []string  `json:"arrivals"`
	// HandPlaced is the members added since the previous record that no logged
	// gesture added.
	HandPlaced   []string `json:"hand_placed"`
	UnloggedEdit bool     `json:"unlogged_edit"`
	// DiscoveryDigest stands for the configuration that decides what inputs
	// offer, and DiscoveryChanged reports that it differs from the previous
	// record's, so a change in what is unplaced can be told from activity.
	DiscoveryDigest  string       `json:"discovery_digest"`
	DiscoveryChanged bool         `json:"discovery_changed"`
	Workstreams      []Workstream `json:"workstreams"`
	Health           []Health     `json:"health"`
	Incomplete       bool         `json:"incomplete"`
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
// was built from. history is the baseline's records, oldest first, and
// gestures the whole gesture log.
func Derive(history []Record, cfg *config.Config, out readout.Readout, gestures []state.Gesture, now time.Time) (Record, error) {
	r := Record{
		Date:        now.Format(DateLayout),
		At:          now.UTC(),
		Arrivals:    make([]string, 0),
		HandPlaced:  make([]string, 0),
		Workstreams: workstreams(cfg.Workstreams),
		Health:      make([]Health, 0, len(out.Health)),
	}
	digest, err := discoveryDigest(cfg)
	if err != nil {
		return Record{}, err
	}
	r.DiscoveryDigest = digest
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

	if len(history) == 0 {
		// With no record before it, a completion counts only on its own date.
		for _, w := range cfg.Workstreams {
			if w.Completed == r.Date {
				r.CompletedSincePrevious++
			}
		}
		r.Backlog = slices.Clone(unplaced)
		return r, nil
	}
	previous := &history[len(history)-1]
	r.PreviousDate = previous.Date
	r.DiscoveryChanged = previous.DiscoveryDigest != r.DiscoveryDigest
	// A completion counts on the first record that finds it in effect, so one
	// made after the day's snapshot, or dated back by hand, is counted once and
	// never lost.
	for _, w := range cfg.Workstreams {
		if w.Completed != "" && w.Completed <= r.Date && !completedIn(previous, w.Name) {
			r.CompletedSincePrevious++
		}
	}

	var since []state.Gesture
	for _, g := range gestures {
		if g.Outcome == state.OK && g.At.After(previous.At) {
			since = append(since, g)
		}
	}
	replayed := replay(previous.Workstreams, since)
	r.UnloggedEdit = !slices.EqualFunc(workstreams(replayed), r.Workstreams, equalWorkstream)
	r.HandPlaced = handPlaced(replayed, cfg.Workstreams)

	candidates := slices.Concat(unplaced, r.HandPlaced)
	for _, g := range since {
		if g.Gesture == "adopt" || g.Gesture == "dismiss" {
			candidates = append(candidates, g.Item)
		}
	}
	r.Arrivals, r.Backlog = arrivals(history, candidates)
	return r, nil
}

// completedIn reports whether rec counted the named workstream as completed.
func completedIn(rec *Record, name string) bool {
	for _, w := range rec.Workstreams {
		if w.Name == name {
			return w.Completed != "" && w.Completed <= rec.Date
		}
	}
	return false
}

// arrivals is each candidate its input had not shown before, and backlog each
// candidate of an input with no healthy record before now, failing since the
// first or newly configured, which is taken as already there. An item handled
// the day it appeared never reaches a snapshot's unplaced, so the candidates
// include what was adopted, dismissed or placed by hand since the previous
// record. What an input had shown is everything unplaced from its last healthy
// record on, since a failed or partial reading hides items rather than
// removing them, and every record's backlog.
func arrivals(history []Record, candidates []string) (arrived, backlog []string) {
	seen := map[string]map[string]bool{}
	seenBy := func(input string) map[string]bool {
		if s, ok := seen[input]; ok {
			return s
		}
		var s map[string]bool
		for i := len(history) - 1; i >= 0; i-- {
			if !healthy(history[i], input) {
				continue
			}
			s = map[string]bool{}
			for _, rec := range history[i:] {
				for _, t := range rec.Unplaced {
					s[t] = true
				}
			}
			for _, rec := range history {
				for _, t := range rec.Backlog {
					s[t] = true
				}
			}
			break
		}
		seen[input] = s
		return s
	}
	arrived = make([]string, 0)
	for _, t := range candidates {
		s := seenBy(member(t).Input)
		switch {
		case s == nil:
			if !slices.Contains(backlog, t) {
				backlog = append(backlog, t)
			}
		case !s[t]:
			s[t] = true
			arrived = append(arrived, t)
		}
	}
	slices.Sort(arrived)
	slices.Sort(backlog)
	return arrived, backlog
}

func healthy(rec Record, input string) bool {
	return slices.Contains(rec.Health, Health{Input: input, Status: health.OK})
}

// handPlaced is each member the logged gestures do not account for having
// been placed: one no live workstream held after replay, that either no
// workstream held at all or a live one holds now. Moving an item out of a
// completed workstream places it as adopting it would, and so does adding one
// to a workstream completed since; moving one between live workstreams, or
// leaving a completed workstream's members as they were, places nothing new.
func handPlaced(replayed, current []config.Workstream) []string {
	live, held := map[config.Member]bool{}, map[config.Member]bool{}
	for _, w := range replayed {
		for _, m := range w.Members {
			held[m] = true
			live[m] = live[m] || w.Completed == ""
		}
	}
	out := make([]string, 0)
	for _, w := range current {
		for _, m := range w.Members {
			if !live[m] && (!held[m] || w.Completed == "") {
				out = append(out, m.Input+":"+m.ID)
			}
		}
	}
	slices.Sort(out)
	return out
}

// discoveryDigest stands for the configuration that decides what inputs offer:
// the inputs themselves, the discovery window and the owner fence. A change to
// any of them changes what is unplaced with no item having moved.
func discoveryDigest(cfg *config.Config) (string, error) {
	data, err := json.Marshal(struct {
		Inputs          []config.Input
		DiscoveryWindow time.Duration
		ForgeOwners     []config.Owner
	}{cfg.Inputs, cfg.DiscoveryWindow, cfg.ForgeOwners})
	if err != nil {
		return "", fmt.Errorf("encoding the discovery configuration: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// replay applies each logged configuration gesture to the workstreams a record
// held, giving what the configuration would hold had nothing else changed it.
// A complete gesture is replayed on the date it wrote, which its time alone
// cannot give once the zone has changed.
func replay(held []Workstream, gestures []state.Gesture) []config.Workstream {
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
			ws = config.CompletedOn(ws, g.Workstream, g.Date)
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

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
	// arrivals against: everything unplaced or joined on the first record, and
	// on a later one, what an input failing until then, or newly configured,
	// showed, and each item joined for the first time that last moved before
	// the discovery window opened. None of it ever counts as an arrival.
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
	// Joined is every item a key places in a workstream on this record, which
	// no gesture placed and no member names.
	Joined []string `json:"joined"`
	// DiscoveryDigest stands for the configuration that decides what inputs
	// offer, and DiscoveryChanged reports that it differs from the previous
	// record's, so a change in what is unplaced can be told from activity.
	DiscoveryDigest  string       `json:"discovery_digest"`
	DiscoveryChanged bool         `json:"discovery_changed"`
	Workstreams      []Workstream `json:"workstreams"`
	Health           []Health     `json:"health"`
	Incomplete       bool         `json:"incomplete"`
	// MergedPRs is absent where the setting reads no GitHub input.
	MergedPRs *Merged `json:"merged_prs,omitempty"`
}

// Merged is the authored pull requests that merged between Since and Through.
// A read that failed says why in Failed and lists nothing, so it cannot be
// read as a day with no merges; the next read that succeeds covers its
// interval.
type Merged struct {
	Since   time.Time  `json:"since"`
	Through time.Time  `json:"through"`
	PRs     []MergedPR `json:"prs,omitzero"`
	Failed  string     `json:"failed,omitempty"`
}

// MergedPR is one merged pull request, with the time it merged so a record
// that covers several days can be counted day by day.
type MergedPR struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	MergedAt    time.Time `json:"merged_at"`
	IntoDefault bool      `json:"into_default"`
}

// MergedRead is what a merged pull request search returned for the window
// MergedWindow gave.
type MergedRead struct {
	Since  time.Time
	Found  []MergedPR
	Failed string
	// Detail says why the read failed, for the person running the snapshot;
	// the record keeps only Failed.
	Detail string
}

const (
	// mergeMargin is how far before a window the search reaches. GitHub's
	// search index trails a merge by an amount it does not document, so a
	// merge from just before the previous record can first be found now.
	mergeMargin = 24 * time.Hour
	// mergeReach bounds how far back a window opens. A window that grows past
	// what one search returns would fail every day after, since a failed read
	// keeps its window open.
	mergeReach = 90 * 24 * time.Hour
)

// MergedWindow is the interval the next record's merged pull requests cover,
// opening where the last read that succeeded closed, and the time a search
// for them should reach back to.
func MergedWindow(history []Record, now time.Time) (since, from time.Time) {
	y, m, d := now.Date()
	since = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	for i := len(history) - 1; i >= 0; i-- {
		if p := history[i].MergedPRs; p != nil {
			since = p.Through
			if p.Failed != "" {
				since = p.Since
			}
			break
		}
	}
	if limit := now.Add(-mergeReach); since.Before(limit) {
		since = limit
	}
	from = since.Add(-mergeMargin)
	if origin := mergedOrigin(history, since); from.Before(origin) {
		from = origin
	}
	return since, from
}

// mergedOrigin is where the merged series begins: the first record that read
// merges, or since when none has.
func mergedOrigin(history []Record, since time.Time) time.Time {
	for _, rec := range history {
		if rec.MergedPRs != nil {
			return rec.MergedPRs.Since
		}
	}
	return since
}

// merged lists each pull request read that merged within the series and by
// now, and that no earlier record lists. It is matched by node id, since
// renaming a repository changes the readable id.
func merged(history []Record, read *MergedRead, now time.Time) *Merged {
	m := &Merged{Since: read.Since.UTC(), Through: now.UTC()}
	if read.Failed != "" {
		m.Failed = read.Failed
		return m
	}
	seen := map[string]bool{}
	for _, rec := range history {
		if rec.MergedPRs != nil {
			for _, p := range rec.MergedPRs.PRs {
				seen[p.NodeID] = true
			}
		}
	}
	origin := mergedOrigin(history, read.Since)
	m.PRs = make([]MergedPR, 0, len(read.Found))
	for _, p := range read.Found {
		if p.MergedAt.Before(origin) || p.MergedAt.After(now) || seen[p.NodeID] {
			continue
		}
		seen[p.NodeID] = true
		p.MergedAt = p.MergedAt.UTC()
		m.PRs = append(m.PRs, p)
	}
	slices.SortFunc(m.PRs, func(a, b MergedPR) int { return strings.Compare(a.ID, b.ID) })
	return m
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
// was built from. history is the baseline's records, oldest first, gestures
// the whole gesture log, and read the merged pull request search, nil where
// none was made.
func Derive(history []Record, cfg *config.Config, out readout.Readout, gestures []state.Gesture, read *MergedRead, now time.Time) (Record, error) {
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
	// A joined item that last moved before the discovery window opened is work
	// discovery would not have offered, so setting a pattern, or adopting a
	// ticket, over old work does not read as that work arriving.
	var joined []readout.Placed
	for _, w := range out.Workstreams {
		for _, p := range w.Items {
			if p.Joined != nil {
				joined = append(joined, p)
			}
		}
	}
	r.Joined, err = tokens(joined)
	if err != nil {
		return Record{}, err
	}
	var older []string
	for _, p := range joined {
		var it struct {
			ID          string    `json:"id"`
			LastTouched time.Time `json:"last_touched"`
		}
		if err := json.Unmarshal(p.Item, &it); err != nil {
			return Record{}, fmt.Errorf("reading an item of %s: %w", p.Input, err)
		}
		if it.LastTouched.Before(now.Add(-cfg.DiscoveryWindow)) {
			older = append(older, p.Input+":"+it.ID)
		}
	}

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
	if read != nil {
		r.MergedPRs = merged(history, read, now)
	}

	if len(history) == 0 {
		// With no record before it, a completion counts only on its own date.
		for _, w := range cfg.Workstreams {
			if w.Completed == r.Date {
				r.CompletedSincePrevious++
			}
		}
		r.Backlog = slices.Concat(unplaced, r.Joined)
		slices.Sort(r.Backlog)
		return r, nil
	}
	previous := &history[len(history)-1]
	r.PreviousDate = previous.Date
	r.DiscoveryChanged = previous.DiscoveryDigest != r.DiscoveryDigest
	var since []state.Gesture
	for _, g := range gestures {
		if g.Outcome == state.OK && g.At.After(previous.At) {
			since = append(since, g)
		}
	}
	// A completion counts on the first record that finds it in effect, so one
	// made after the day's snapshot, or dated back by hand, is counted once and
	// never lost. A workstream renamed by command since is looked up under the
	// name the previous record knew it by.
	before := namesBefore(since)
	for _, w := range cfg.Workstreams {
		if w.Completed == "" || w.Completed > r.Date {
			continue
		}
		if name, ok := before(w.Name); !ok || !completedIn(previous, name) {
			r.CompletedSincePrevious++
		}
	}
	replayed := replay(previous.Workstreams, since)
	r.UnloggedEdit = !slices.EqualFunc(workstreams(replayed), r.Workstreams, equalWorkstream)
	r.HandPlaced = handPlaced(replayed, cfg.Workstreams)

	candidates := slices.Concat(unplaced, r.HandPlaced)
	// A key places an item without anyone deciding to, so one any earlier
	// record lists as joined has arrived already, however it was held since:
	// joining it again, after it was adopted and removed, asks nothing new.
	joinedBefore := map[string]bool{}
	for _, rec := range history {
		for _, t := range rec.Joined {
			joinedBefore[t] = true
		}
	}
	for _, t := range r.Joined {
		if !slices.Contains(older, t) && !joinedBefore[t] {
			candidates = append(candidates, t)
		}
	}
	for _, g := range since {
		if g.Gesture == "adopt" || g.Gesture == "dismiss" {
			candidates = append(candidates, g.Item)
		}
	}
	older = slices.DeleteFunc(older, func(t string) bool { return joinedBefore[t] })
	r.Arrivals, r.Backlog = arrivals(history, candidates, older)
	return r, nil
}

// namesBefore maps a workstream's current name to the one it had before the
// logged renames, reporting false for a name a rename freed, which no longer
// stands for the workstream that held it.
func namesBefore(gestures []state.Gesture) func(string) (string, bool) {
	type origin struct {
		name string
		ok   bool
	}
	renamed := map[string]origin{}
	lookup := func(name string) (string, bool) {
		if o, found := renamed[name]; found {
			return o.name, o.ok
		}
		return name, true
	}
	for _, g := range gestures {
		if g.Gesture != "rename" {
			continue
		}
		name, ok := lookup(g.Workstream)
		renamed[g.To] = origin{name, ok}
		renamed[g.Workstream] = origin{}
	}
	return lookup
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
// removing them, and every record's backlog. A joined item counts as shown
// the same way an unplaced one does. Each of older not shown before is
// backlog: it was there before it could arrive.
func arrivals(history []Record, candidates, older []string) (arrived, backlog []string) {
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
				for _, t := range slices.Concat(rec.Unplaced, rec.Joined) {
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
	for _, t := range older {
		if s := seenBy(member(t).Input); (s == nil || !s[t]) && !slices.Contains(backlog, t) {
			backlog = append(backlog, t)
		}
	}
	slices.Sort(arrived)
	slices.Sort(backlog)
	return arrived, backlog
}

func healthy(rec Record, input string) bool {
	return slices.Contains(rec.Health, Health{Input: input, Status: health.OK})
}

// handPlaced is each item the configuration holds in a workstream that replay
// does not, where no live workstream held it after replay: a membership the
// logged gestures do not account for, of an item not already claimed. That
// counts moving an item out of a completed workstream, into a live one or one
// completed since, and adding one anywhere, as adopting it would; moving one
// between live workstreams, or leaving a completed workstream's members as
// they were, places nothing new. Renaming a completed workstream by hand reads
// as placing its members.
func handPlaced(replayed, current []config.Workstream) []string {
	type membership struct {
		workstream string
		member     config.Member
	}
	logged, live := map[membership]bool{}, map[config.Member]bool{}
	for _, w := range replayed {
		for _, m := range w.Members {
			logged[membership{w.Name, m}] = true
			live[m] = live[m] || w.Completed == ""
		}
	}
	placed := map[config.Member]bool{}
	for _, w := range current {
		for _, m := range w.Members {
			if !logged[membership{w.Name, m}] && !live[m] {
				placed[m] = true
			}
		}
	}
	out := make([]string, 0, len(placed))
	for m := range placed {
		out = append(out, m.Input+":"+m.ID)
	}
	slices.Sort(out)
	return out
}

// discoveryDigest stands for the configuration that decides what inputs offer
// and where a key places it: the inputs themselves, the discovery window, the
// owner fence and the key pattern. A change to any of them changes what is
// unplaced with no item having moved. Whether an input expects records is left
// out: it decides only the input's health, and adopting or removing a supplied
// input's member changes it.
func discoveryDigest(cfg *config.Config) (string, error) {
	inputs := slices.Clone(cfg.Inputs)
	for i := range inputs {
		inputs[i].ExpectRecords = false
	}
	data, err := json.Marshal(struct {
		Inputs          []config.Input
		DiscoveryWindow time.Duration
		ForgeOwners     []config.Owner
		// KeyPattern is left out where none is set, so a setting that sets
		// none keeps the digest it had before the key existed.
		KeyPattern string `json:",omitempty"`
	}{inputs, cfg.DiscoveryWindow, cfg.ForgeOwners, cfg.KeyPattern})
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
		case "move":
			ws = config.Moved(ws, member(g.Item), g.Workstream)
		case "remove":
			ws = config.Removed(ws, member(g.Item))
		case "rename":
			ws = config.Renamed(ws, g.Workstream, g.To)
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

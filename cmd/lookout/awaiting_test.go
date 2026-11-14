package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// entry is one awaiting.v1 entry asked at the given age, answered at the
// given age or, where answered is nil, never.
func entry(id, kind string, asked time.Duration, answered *time.Duration, extra map[string]any) map[string]any {
	e := map[string]any{
		"id":          id,
		"title":       "Question " + id,
		"kind":        kind,
		"asked_at":    stamp(asked),
		"answered_at": nil,
		"links":       []any{},
		"attrs":       map[string]any{},
	}
	if answered != nil {
		e["answered_at"] = stamp(*answered)
	}
	maps.Copy(e, extra)
	return e
}

func awaitingDoc(input string, entries []any, extra map[string]any) map[string]any {
	doc := map[string]any{
		"schema":          "awaiting.v1",
		"input":           input,
		"supplier":        "test-supplier",
		"fetched_at":      stamp(time.Hour),
		"attempted_at":    stamp(time.Hour),
		"supplier_status": "ok",
		"awaiting":        entries,
	}
	maps.Copy(doc, extra)
	return doc
}

// withAsks is a setting with one awaiting input beside a tracker, a workstream
// holding a tracker item and an item of a calendar whose file is missing.
const withAsks = `
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: calendar
    kind: supplied
    supplied: calendar.json
    max_age: 24h
  - name: asks
    kind: awaiting
    supplied: asks.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1", "calendar:renewal"]
`

func awaitingIDs(doc readoutDoc) []string {
	ids := []string{}
	if doc.Awaiting != nil {
		for _, a := range *doc.Awaiting {
			ids = append(ids, a.Input+":"+a.ID)
		}
	}
	return ids
}

func TestReadListsOnlyTheRepliesStillOwed(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", withAsks)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, nil))
	f.supplied("asks.json", awaitingDoc("asks", []any{
		entry("never-answered", "mentioned", 5*time.Hour, nil, map[string]any{
			"links": []any{map[string]any{"rel": "thread", "href": "https://tracker.example/ABC-5#c1"}},
			"attrs": map[string]any{"space": "DOCS"},
		}),
		entry("asked-again", "replied", 2*time.Hour, new(3*time.Hour), nil),
		entry("answered", "mentioned", 3*time.Hour, new(2*time.Hour), nil),
		// Asked and answered in the same second: the answer is the latest word.
		entry("answered-together", "review_requested", 4*time.Hour, new(4*time.Hour), nil),
		entry("a-kind-lookout-has-not-seen", "approval_requested", 5*time.Hour, nil, nil),
	}, nil))

	doc := f.read()

	if got := awaitingIDs(doc); !slices.Equal(got, []string{"asks:asked-again", "asks:a-kind-lookout-has-not-seen", "asks:never-answered"}) {
		t.Fatalf("awaiting = %v, want what is still waiting, latest ask first, then by id", got)
	}
	if doc.AwaitingInputs == nil || !slices.Equal(*doc.AwaitingInputs, []string{"asks"}) {
		t.Errorf("awaiting_inputs = %v, want the setting's one awaiting input", doc.AwaitingInputs)
	}
	e := (*doc.Awaiting)[2]
	if e.Title != "Question never-answered" || e.Kind != "mentioned" || e.AskedAt != stamp(5*time.Hour) || e.AnsweredAt != nil {
		t.Errorf("entry = %+v, want the supplier's facts carried", e)
	}
	if len(e.Links) != 1 || e.Attrs["space"] != "DOCS" {
		t.Errorf("links = %v, attrs = %v, want them passed through", e.Links, e.Attrs)
	}
	if e.Subject != nil || len(e.Workstreams) != 0 || e.Unplaced {
		t.Errorf("subject = %v, workstreams = %v, unplaced = %v, want an entry about nothing lookout holds", e.Subject, e.Workstreams, e.Unplaced)
	}
	if h := doc.health(t, "asks"); h.Status != "ok" || h.RecordCount != 5 {
		t.Errorf("health = %+v, want ok, counting every entry the file holds", h)
	}
}

func TestReadPointsAReplyOwedAtWhereItsSubjectIsShown(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", withAsks)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", time.Hour, nil),
		item("ABC-3", time.Hour, nil),
	}, nil))
	// calendar.json is missing, so billing's calendar member is unread.
	f.supplied("asks.json", awaitingDoc("asks", []any{
		entry("on-a-member", "mentioned", time.Hour, nil, map[string]any{"subject": "tracker:ABC-1"}),
		entry("on-an-unplaced-item", "mentioned", 2*time.Hour, nil, map[string]any{"subject": "tracker:ABC-3"}),
		entry("on-an-unread-member", "mentioned", 3*time.Hour, nil, map[string]any{"subject": "calendar:renewal"}),
		entry("on-something-unread", "mentioned", 4*time.Hour, nil, map[string]any{"subject": "tracker:ABC-77"}),
		// Ids are matched exactly, as a workstream's members are.
		entry("on-another-case", "mentioned", 5*time.Hour, nil, map[string]any{"subject": "tracker:abc-1"}),
	}, nil))

	doc := f.read()

	want := map[string]struct {
		workstreams []string
		unplaced    bool
	}{
		"on-a-member":         {[]string{"billing"}, false},
		"on-an-unplaced-item": {[]string{}, true},
		"on-an-unread-member": {[]string{"billing"}, false},
		"on-something-unread": {[]string{}, false},
		"on-another-case":     {[]string{}, false},
	}
	if doc.Awaiting == nil || len(*doc.Awaiting) != len(want) {
		t.Fatalf("awaiting = %v, want every entry", awaitingIDs(doc))
	}
	for _, e := range *doc.Awaiting {
		w := want[e.ID]
		if !slices.Equal(e.Workstreams, w.workstreams) || e.Unplaced != w.unplaced {
			t.Errorf("%s: workstreams = %v, unplaced = %v, want %v, %v", e.ID, e.Workstreams, e.Unplaced, w.workstreams, w.unplaced)
		}
		if e.Subject == nil {
			t.Errorf("%s: subject dropped", e.ID)
		}
	}
	// The thread is pointed at, never shown in the subject's place or beside it.
	if got := unplacedIDs(doc); !slices.Equal(got, []string{"tracker:ABC-3"}) {
		t.Errorf("unplaced = %v, want the tracker item alone", got)
	}
	if items := doc.Workstreams[0].Items; len(items) != 1 || items[0].Input != "tracker" {
		t.Errorf("billing items = %+v, want its tracker member alone", items)
	}
}

// TestReadAwaitingHealth pins that an awaiting input's health is judged as a
// supplied file's is, apart from an empty file, which is a quiet day.
func TestReadAwaitingHealth(t *testing.T) {
	one := []any{entry("T-1", "mentioned", time.Hour, nil, nil)}
	future := now.Add(time.Hour).Format(time.RFC3339)
	cases := []struct {
		name    string
		file    map[string]any
		status  string
		reasons []string
		missing []string
		waiting []string
	}{
		{name: "healthy", file: awaitingDoc("asks", one, nil), status: "ok", waiting: []string{"T-1"}},
		{name: "missing", file: nil, status: "error", reasons: []string{"missing"}},
		{name: "empty is a quiet day", file: awaitingDoc("asks", []any{}, nil), status: "ok"},
		{name: "older than max_age", file: awaitingDoc("asks", one, map[string]any{
			"fetched_at": stamp(25 * time.Hour), "attempted_at": stamp(25 * time.Hour),
		}), status: "degraded", reasons: []string{"stale"}, waiting: []string{"T-1"}},
		{name: "last attempt failed", file: awaitingDoc("asks", one, map[string]any{
			"fetched_at": stamp(2 * time.Hour), "attempted_at": stamp(time.Hour),
		}), status: "degraded", reasons: []string{"supplier_failed"}, waiting: []string{"T-1"}},
		{name: "supplier reports error", file: awaitingDoc("asks", one, map[string]any{"supplier_status": "error"}),
			status: "error", reasons: []string{"supplier_error"}, waiting: []string{"T-1"}},
		{name: "asked in the future", file: awaitingDoc("asks", []any{
			map[string]any{"id": "T-1", "title": "Q", "kind": "mentioned", "asked_at": future, "answered_at": nil, "links": []any{}, "attrs": map[string]any{}},
		}, nil), status: "degraded", reasons: []string{"clock_skew"}, waiting: []string{"T-1"}},
		// An answer stamped ahead of the clock is no evidence of an answer, so
		// it never hides the ask it would otherwise outdate.
		{name: "answered in the future", file: awaitingDoc("asks", []any{
			map[string]any{"id": "T-1", "title": "Q", "kind": "mentioned", "asked_at": stamp(time.Hour), "answered_at": future, "links": []any{}, "attrs": map[string]any{}},
		}, nil), status: "degraded", reasons: []string{"clock_skew"}, waiting: []string{"T-1"}},
		{name: "answered within clock tolerance", file: awaitingDoc("asks", []any{
			map[string]any{"id": "T-1", "title": "Q", "kind": "mentioned", "asked_at": stamp(time.Hour), "answered_at": now.Add(time.Minute).Format(time.RFC3339), "links": []any{}, "attrs": map[string]any{}},
		}, nil), status: "ok"},
		{name: "duplicate id", file: awaitingDoc("asks", []any{
			entry("T-1", "mentioned", time.Hour, nil, nil), entry("T-1", "replied", 2*time.Hour, nil, nil),
		}, nil), status: "degraded", reasons: []string{"duplicate_id"}, waiting: []string{"T-1", "T-1"}},
		// A supplier that cannot read the user's responses says so, and every
		// entry then shows as waiting rather than quietly answered.
		{name: "responses unreadable", file: awaitingDoc("asks", one, map[string]any{"missing_fields": []any{"answered_at"}}),
			status: "degraded", reasons: []string{"missing_fields"}, missing: []string{"answered_at"}, waiting: []string{"T-1"}},
		// Claiming responses unreadable while writing one would let it hide an
		// ask the claim says should show.
		{name: "responses unreadable yet one written", file: awaitingDoc("asks", []any{
			entry("T-1", "mentioned", 2*time.Hour, new(time.Hour), nil),
		}, map[string]any{"missing_fields": []any{"answered_at"}}), status: "degraded", reasons: []string{"schema"}},
		{name: "an entry without a title", file: awaitingDoc("asks", []any{
			map[string]any{"id": "T-1", "kind": "mentioned", "asked_at": stamp(time.Hour), "answered_at": nil, "links": []any{}, "attrs": map[string]any{}},
		}, nil), status: "degraded", reasons: []string{"schema"}},
		{name: "an empty title", file: awaitingDoc("asks", []any{entry("T-1", "mentioned", time.Hour, nil, map[string]any{"title": ""})}, nil),
			status: "degraded", reasons: []string{"schema"}},
		{name: "a subject that names no input", file: awaitingDoc("asks", []any{entry("T-1", "mentioned", time.Hour, nil, map[string]any{"subject": "ABC-1"})}, nil),
			status: "degraded", reasons: []string{"schema"}},
		{name: "a supplied.v1 file in its place", file: suppliedDoc("asks", []any{item("ABC-1", time.Hour, nil)}, nil),
			status: "degraded", reasons: []string{"schema"}},
		{name: "written for another input", file: awaitingDoc("chat", one, nil), status: "error", reasons: []string{"input_mismatch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", `
inputs:
  - name: asks
    kind: awaiting
    supplied: asks.json
    max_age: 24h
workstreams: []
`)
			if tc.file != nil {
				f.supplied("asks.json", tc.file)
			}

			doc := f.read()

			h := doc.health(t, "asks")
			want := tc.reasons
			if want == nil {
				want = []string{}
			}
			if h.Status != tc.status || !slices.Equal(h.Reasons, want) {
				t.Errorf("health = %s %v, want %s %v", h.Status, h.Reasons, tc.status, want)
			}
			if tc.missing != nil && !slices.Equal(h.MissingFields, tc.missing) {
				t.Errorf("missing_fields = %v, want %v", h.MissingFields, tc.missing)
			}
			ids := []string{}
			for _, e := range *doc.Awaiting {
				ids = append(ids, e.ID)
			}
			waiting := tc.waiting
			if waiting == nil {
				waiting = []string{}
			}
			if !slices.Equal(ids, waiting) {
				t.Errorf("awaiting = %v, want %v", ids, waiting)
			}
		})
	}
}

// TestAFailingAwaitingInputNeverMarksAWorkstream pins that replies owed feed
// no workstream, so their input failing says nothing about any workstream's
// figures, even one a key places work in from every input.
func TestAFailingAwaitingInputNeverMarksAWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
key_pattern: '([A-Z]+-[0-9]+)'
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: asks
    kind: awaiting
    supplied: asks.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
`)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, nil))
	// asks.json is missing.

	doc := f.read()

	if h := doc.health(t, "asks"); h.Status != "error" {
		t.Fatalf("asks health = %+v, want error", h)
	}
	if got := doc.Workstreams[0].InputsIncomplete; len(got) != 0 {
		t.Errorf("billing inputs_incomplete = %v, want none: replies owed feed no workstream", got)
	}
}

func TestReadWithNoAwaitingInputListsNone(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", oneInput)
	f.supplied("tracker.json", trackerItems())

	doc := f.read()

	if doc.AwaitingInputs == nil || len(*doc.AwaitingInputs) != 0 || doc.Awaiting == nil || len(*doc.Awaiting) != 0 {
		t.Errorf("awaiting_inputs = %v, awaiting = %v, want both present and empty", doc.AwaitingInputs, doc.Awaiting)
	}
}

// withAsksHandWritten is handWritten with an awaiting input added.
var withAsksHandWritten = strings.Replace(handWritten, "\nworkstreams:\n", `  - name: asks
    kind: awaiting
    supplied: asks.json
    max_age: 24h

workstreams:
`, 1)

// TestGesturesRefuseARepliesOwedEntry pins that a reply owed is never
// adopted, moved or dismissed: those gestures say where work belongs, and a
// reply owed is not work lookout places.
func TestGesturesRefuseARepliesOwedEntry(t *testing.T) {
	for _, args := range [][]string{
		{"adopt", "asks:T-1", "billing"},
		{"adopt", "--new", "asks:T-1", "replies"},
		{"move", "asks:T-1", "billing"},
		{"dismiss", "asks:T-1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", withAsksHandWritten)

			code, stdout, stderr := f.run(args...)

			if code != exitFailure || stdout != "" || !strings.Contains(stderr, "asks") {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a refusal naming the input", code, stdout, stderr)
			}
			if got := f.config("personal.yml"); got != withAsksHandWritten {
				t.Errorf("configuration changed by a refused %s:\n%s", args[0], got)
			}
			if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Outcome != "refused" || !strings.Contains(gs[0].Reason, "reply owed") {
				t.Errorf("gestures = %+v, want one refused gesture saying it is a reply owed", gs)
			}
		})
	}
}

// TestAFailingAwaitingInputLeavesTheBaselineSound pins that the day's record
// keeps the awaiting input's status but is not marked incomplete by it, since
// nothing the baseline counts is read from it, and that a refused dismissal of
// a reply owed is no arrival.
func TestAFailingAwaitingInputLeavesTheBaselineSound(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", withAsksHandWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	// asks.json is missing.
	f.snapshot()

	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	if code, _, _ := f.run("dismiss", "asks:T-1"); code != exitFailure {
		t.Fatalf("dismiss exit code = %d, want a refusal", code)
	}
	b := f.snapshot()

	if b.Incomplete {
		t.Error("incomplete = true, want false: the awaiting input feeds nothing the baseline counts")
	}
	found := false
	for _, h := range b.Health {
		found = found || (h.Input == "asks" && h.Status == "error")
	}
	if !found {
		t.Errorf("health = %+v, want the awaiting input's error kept", b.Health)
	}
	if len(b.Arrivals) != 0 {
		t.Errorf("arrivals = %v, want none", b.Arrivals)
	}
}

// TestAddingAnAwaitingInputLeavesDiscoveryUnchanged pins that an input whose
// entries are never offered does not change what is discovered.
func TestAddingAnAwaitingInputLeavesDiscoveryUnchanged(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.write("personal.yml", withAsksHandWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.DiscoveryChanged {
		t.Error("discovery_changed = true after adding an awaiting input")
	}
}

// TestAddingTheOnlyInputAsAwaitingLeavesDiscoveryUnchanged pins the same for a
// setting that had no input at all, whose digest must not tell an empty list
// from a list of awaiting inputs alone.
func TestAddingTheOnlyInputAsAwaitingLeavesDiscoveryUnchanged(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", "inputs: []\nworkstreams: []\n")
	f.snapshot()

	f.nextDay()
	f.write("personal.yml", "inputs:\n  - {name: asks, kind: awaiting, supplied: asks.json, max_age: 24h}\nworkstreams: []\n")
	if b := f.snapshot(); b.DiscoveryChanged {
		t.Error("discovery_changed = true after adding a setting's only input as an awaiting one")
	}
}

// TestShowKeepsAFailingAwaitingInputOffTheCounts pins that the footer's
// counts, read from inputs an awaiting input is not among, are not flagged by
// it, while its own line still says it failed.
func TestShowKeepsAFailingAwaitingInputOffTheCounts(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", withAsksHandWritten)
	f.supplied("tracker.json", trackerItems())

	code, stdout, stderr := f.run("show")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "  input asks: error (missing)\n") {
		t.Errorf("stdout lacks the awaiting input's warning:\n%s", stdout)
	}
	if strings.Contains(stdout, "\n! 1 unplaced") || !strings.Contains(stdout, "\n1 unplaced") {
		t.Errorf("footer flagged by an input it does not count:\n%s", stdout)
	}
}

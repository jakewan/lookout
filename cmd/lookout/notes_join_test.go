package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// noted is a setting with a second supplier, one that knows nothing about
// workstreams and passes along the notes each of its items was worked under.
// A member id in lower case shows a key in the notes matching it in any case.
const noted = `
key_pattern: '([A-Z][A-Z0-9]+-[0-9]+)'
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: agents
    kind: supplied
    supplied: agents.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
  - name: onboarding
    members: ["tracker:xyz-9"]
`

// notesMarker stands for whatever a work note says that must never leave
// lookout.
const notesMarker = "private-note-7f3a"

// notesKey finds notes written as a key, apart from the joined.by value that
// shares the name.
var notesKey = regexp.MustCompile(`"notes"\s*:`)

func (f *fixture) withNotedTracker() {
	f.t.Helper()
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", 5*time.Hour, nil), item("xyz-9", 5*time.Hour, nil)}, nil))
}

func TestNotesJoinASuppliedItemToEachAdoptedTicketTheyName(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", noted)
	f.withNotedTracker()
	f.supplied("agents.json", suppliedDoc("agents", []any{
		item("session-1", time.Hour, map[string]any{"notes": "Ticket: ABC-1, then XYZ-9; see QRS-77. " + notesMarker}),
		item("session-2", time.Hour, map[string]any{"notes": "Only QRS-77 here. " + notesMarker}),
		item("session-3", time.Hour, nil),
		item("session-4", time.Hour, map[string]any{"notes": ""}),
	}, nil))

	doc := f.read()

	by := func(member string) joinedDoc { return joinedDoc{By: "notes", Member: member} }
	if got, want := joins(t, doc, "billing"), map[string]joinedDoc{"agents:session-1": by("tracker:ABC-1")}; !equalJoins(got, want) {
		t.Errorf("billing joins = %v, want the session whose notes name ABC-1", got)
	}
	if got, want := joins(t, doc, "onboarding"), map[string]joinedDoc{"agents:session-1": by("tracker:xyz-9")}; !equalJoins(got, want) {
		t.Errorf("onboarding joins = %v, want the same session, its notes' XYZ-9 matching member xyz-9", got)
	}
	for _, w := range doc.Workstreams {
		touched := slices.ContainsFunc(w.Events, func(e eventDoc) bool {
			return e.Input == "agents" && e.ID == "session-1" && e.Joined != nil && e.Joined.By == "notes"
		})
		if !touched {
			t.Errorf("%s events = %+v, want the session counted as a touch in each workstream its notes name", w.Name, w.Events)
		}
	}
	unplaced := unplacedIDs(doc)
	slices.Sort(unplaced)
	if want := []string{"agents:session-2", "agents:session-3", "agents:session-4"}; !slices.Equal(unplaced, want) {
		t.Errorf("unplaced = %v, want %v: a key nobody adopted, no notes and empty notes place nothing", unplaced, want)
	}
	for _, u := range doc.Unplaced {
		if _, ok := u.Item["notes"]; ok {
			t.Errorf("unplaced %v carries notes, want them never emitted", u.Item["id"])
		}
	}

	b := f.snapshot()
	if !slices.Equal(b.Joined, []string{"agents:session-1"}) {
		t.Errorf("snapshot joined = %v, want the session listed once", b.Joined)
	}
	outputs := map[string]string{"baseline": readFile(t, f.baselinePath("personal"))}
	for _, args := range [][]string{{"read", "--json"}, {"show"}, {"unplaced"}} {
		code, stdout, stderr := f.run(args...)
		if code != 0 {
			t.Fatalf("%v: exit code = %d, stderr = %q", args, code, stderr)
		}
		outputs[strings.Join(args, " ")] = stdout + stderr
	}
	for name, out := range outputs {
		if strings.Contains(out, notesMarker) || notesKey.MatchString(out) {
			t.Errorf("%s carries the notes, want them never emitted:\n%s", name, out)
		}
	}
}

func TestOverLongNotesDegradeTheInputWithoutEchoingThem(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", noted)
	f.withNotedTracker()
	long := strings.Repeat("ABC-1 ", 700) + notesMarker
	f.supplied("agents.json", suppliedDoc("agents", []any{
		item("session-1", time.Hour, map[string]any{"notes": long}),
		item("session-2", time.Hour, nil),
	}, nil))

	doc := f.read()

	h := doc.health(t, "agents")
	if h.Status != "degraded" || !slices.Contains(h.Reasons, "schema") {
		t.Errorf("agents health = %+v, want degraded by schema: notes past the bound are a violation", h)
	}
	if got := joins(t, doc, "billing"); len(got) != 0 {
		t.Errorf("billing joins = %v, want nothing from a file whose items are withheld", got)
	}
	_, stdout, stderr := f.run("read", "--json")
	if strings.Contains(stdout+stderr, notesMarker) {
		t.Errorf("the violation echoes the notes, want only where and why:\n%s", stdout+stderr)
	}
}

func TestTheNotesBoundCountsCodePoints(t *testing.T) {
	for name, tc := range map[string]struct {
		points int
		status string
	}{
		"at the bound, though past it in bytes": {4096, "ok"},
		"one past the bound":                    {4097, "degraded"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", noted)
			f.withNotedTracker()
			f.supplied("agents.json", suppliedDoc("agents", []any{item("session-1", time.Hour, map[string]any{"notes": strings.Repeat("é", tc.points)})}, nil))

			if h := f.read().health(t, "agents"); h.Status != tc.status {
				t.Errorf("agents health = %+v, want %s", h, tc.status)
			}
		})
	}
}

func TestNotesNeverMoveAnItemAWorkstreamHoldsOrTheUserDismissed(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", noted+`  - name: review
    members: ["agents:session-1"]
`)
	f.withNotedTracker()
	f.supplied("agents.json", suppliedDoc("agents", []any{
		item("session-1", time.Hour, map[string]any{"notes": "ABC-1"}),
		item("session-2", time.Hour, map[string]any{"notes": "ABC-1"}),
	}, nil))
	f.mustRun("dismiss", "agents:session-2")

	doc := f.read()

	if got := joins(t, doc, "billing"); len(got) != 0 {
		t.Errorf("billing joins = %v, want neither the member of review nor the dismissed session", got)
	}
	if doc.DismissedCount != 1 {
		t.Errorf("dismissed_count = %d, want the dismissed session counted", doc.DismissedCount)
	}
}

func TestAKeyedWorkstreamMarksAFailingSupplierItsNotesCouldJoinFrom(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", noted)
	f.withNotedTracker()

	doc := f.read()

	for _, w := range doc.Workstreams {
		if !slices.Contains(w.InputsIncomplete, "agents") {
			t.Errorf("%s inputs_incomplete = %v, want agents: its missing file may have kept a join from it", w.Name, w.InputsIncomplete)
		}
	}
}

// TestAPullRequestsWorkstreamDoesNotDrawOnASupplier pins the other side: a
// workstream only a pull request feeds can gain nothing from a supplied item,
// whose notes join only a ticket's workstream, so a failing supplier does not
// mark it.
func TestAPullRequestsWorkstreamDoesNotDrawOnASupplier(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(described, "workstreams:", `  - name: agents
    kind: supplied
    supplied: agents.json
    max_age: 24h
workstreams:`, 1)+`  - name: review
    members: ["hub:alice/app#9"]
`)
	f.withDescribedInputs(ghNode{pr: true, repo: "alice/app", number: 9, title: "Review me", state: "OPEN", branch: "review-me", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true})
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "review-me", time.Hour)

	doc := f.read()

	for _, w := range doc.Workstreams {
		if got, want := slices.Contains(w.InputsIncomplete, "agents"), w.Name != "review"; got != want {
			t.Errorf("%s inputs_incomplete = %v, want agents listed only where a key places work", w.Name, w.InputsIncomplete)
		}
	}
}

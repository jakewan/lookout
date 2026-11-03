package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// epics is a setting built from a tracker's structure: one workstream per
// epic, and one that adopted a story directly. No key pattern is set, since a
// parent places work without one.
const epics = `
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: invoicing
    members: ["tracker:EPIC-1"]
  - name: exports
    members: ["tracker:EPIC-2", "tracker:STORY-9"]
  - name: archive
    members: ["tracker:EPIC-3"]
    completed: 2030-06-01
`

func byParent(member string) joinedDoc { return joinedDoc{By: "parent", Member: member} }

func TestAnItemJoinsTheWorkstreamOfItsAdoptedAncestor(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", epics)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("EPIC-1", 5*time.Hour, nil),
		item("STORY-1", 2*time.Hour, map[string]any{"parent": "EPIC-1"}),
		item("TASK-1", time.Hour, map[string]any{"parent": "STORY-1"}),
		// EPIC-2 is adopted but not supplied, as a sprint-only exporter leaves it.
		item("STORY-2", time.Hour, map[string]any{"parent": "EPIC-2"}),
		// STORY-9 is adopted in exports, under an epic invoicing adopted, so its
		// sub-task joins exports, the nearer ancestor.
		item("STORY-9", time.Hour, map[string]any{"parent": "EPIC-1"}),
		item("TASK-9", time.Hour, map[string]any{"parent": "STORY-9"}),
		item("LOOSE-1", time.Hour, map[string]any{"parent": "EPIC-404"}),
	}, nil))

	doc := f.read()

	if got, want := joins(t, doc, "invoicing"), map[string]joinedDoc{
		"tracker:STORY-1": byParent("tracker:EPIC-1"),
		"tracker:TASK-1":  byParent("tracker:EPIC-1"),
	}; !equalJoins(got, want) {
		t.Errorf("invoicing joins = %v, want %v: a story and its sub-task under the adopted epic", got, want)
	}
	if got, want := joins(t, doc, "exports"), map[string]joinedDoc{
		"tracker:STORY-2": byParent("tracker:EPIC-2"),
		"tracker:TASK-9":  byParent("tracker:STORY-9"),
	}; !equalJoins(got, want) {
		t.Errorf("exports joins = %v, want %v: a child of an unsupplied adopted epic, and the nearest adopted ancestor winning", got, want)
	}
	if got := unplacedIDs(doc); !slices.Equal(got, []string{"tracker:LOOSE-1"}) {
		t.Errorf("unplaced = %v, want only the item whose parent nobody adopted", got)
	}
	for _, w := range doc.Workstreams {
		if w.Name != "invoicing" {
			continue
		}
		if !slices.ContainsFunc(w.Events, func(e eventDoc) bool { return e.ID == "TASK-1" && e.Joined != nil && e.Joined.By == "parent" }) {
			t.Errorf("invoicing events = %+v, want the joined sub-task counted as a touch", w.Events)
		}
	}
}

func TestAParentLoopPlacesNothing(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", epics)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("EPIC-1", time.Hour, nil),
		item("LOOP-1", time.Hour, map[string]any{"parent": "LOOP-2"}),
		item("LOOP-2", time.Hour, map[string]any{"parent": "LOOP-1"}),
		item("SELF-1", time.Hour, map[string]any{"parent": "SELF-1"}),
	}, nil))

	doc := f.read()

	if got := joins(t, doc, "invoicing"); len(got) != 0 {
		t.Errorf("invoicing joins = %v, want none from a loop of parents", got)
	}
	got := unplacedIDs(doc)
	slices.Sort(got)
	if want := []string{"tracker:LOOP-1", "tracker:LOOP-2", "tracker:SELF-1"}; !slices.Equal(got, want) {
		t.Errorf("unplaced = %v, want %v", got, want)
	}
}

func TestAParentJoinKeepsToTheOtherJoinsLimits(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", epics)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("EPIC-1", time.Hour, nil),
		// A completed workstream's epic pulls nothing in, but the walk goes on
		// past it to a live ancestor.
		item("EPIC-3", time.Hour, map[string]any{"parent": "EPIC-1"}),
		item("STORY-3", time.Hour, map[string]any{"parent": "EPIC-3"}),
		// STORY-9 is a member of exports, so its parent cannot take it.
		item("STORY-9", time.Hour, map[string]any{"parent": "EPIC-1"}),
		item("STORY-4", time.Hour, map[string]any{"parent": "EPIC-1"}),
	}, nil))
	f.mustRun("dismiss", "tracker:STORY-4")

	doc := f.read()

	if got, want := joins(t, doc, "invoicing"), map[string]joinedDoc{
		"tracker:EPIC-3":  byParent("tracker:EPIC-1"),
		"tracker:STORY-3": byParent("tracker:EPIC-1"),
	}; !equalJoins(got, want) {
		t.Errorf("invoicing joins = %v, want %v: a completed workstream's moved member joins, its child joins the live ancestor, a member and a dismissed item do not", got, want)
	}
	if doc.DismissedCount != 1 {
		t.Errorf("dismissed_count = %d, want the dismissed child counted", doc.DismissedCount)
	}

	f.clock = f.clock.Add(2 * time.Hour)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("EPIC-1", time.Hour, nil),
		item("STORY-4", 0, map[string]any{"parent": "EPIC-1", "last_touched": f.clock.Add(-time.Minute).Format(time.RFC3339)}),
	}, nil))
	if got := joins(t, f.read(), "invoicing"); got["tracker:STORY-4"] != byParent("tracker:EPIC-1") {
		t.Errorf("invoicing joins = %v, want the dismissed child back once it moves", got)
	}
}

func TestAWindowedSupplierKeepsAnOldChildItsParentJoins(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(epics, "    max_age: 24h\n", "    max_age: 24h\n    windowed: true\n", 1)+"discovery_window: 72h\n")
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("EPIC-1", time.Hour, nil),
		item("DONE-1", 1000*time.Hour, map[string]any{"parent": "EPIC-1", "state": "closed"}),
		item("OLD-1", 1000*time.Hour, nil),
	}, nil))

	doc := f.read()

	if got := joins(t, doc, "invoicing"); got["tracker:DONE-1"] != byParent("tracker:EPIC-1") {
		t.Errorf("invoicing joins = %v, want the old closed child kept at any age, as a notes join is", got)
	}
	if got := unplacedIDs(doc); len(got) != 0 {
		t.Errorf("unplaced = %v, want the old item nothing places left out", got)
	}
}

func TestAParentHeldByAnotherWorkstreamLinksIt(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", noted+`  - name: stories
    members: ["tracker:EPIC-1"]
`)
	// STORY-1 is a child of the adopted EPIC-1 and its notes name ABC-1, so it
	// is held by stories and by billing; its own child joins stories, and so
	// relates billing to stories as a parent does.
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", time.Hour, nil),
		item("xyz-9", time.Hour, nil),
		item("EPIC-1", time.Hour, nil),
		item("STORY-1", time.Hour, map[string]any{"parent": "EPIC-1", "notes": "ABC-1"}),
		item("TASK-1", time.Hour, map[string]any{"parent": "STORY-1"}),
	}, nil))
	f.supplied("agents.json", suppliedDoc("agents", []any{}, nil))

	doc := f.read()

	if got := joins(t, doc, "stories"); got["tracker:TASK-1"] != byParent("tracker:EPIC-1") {
		t.Errorf("stories joins = %v, want the sub-task under the adopted epic", got)
	}
	want := []linkDoc{
		{From: "billing", To: "stories", Kind: "parent", Items: []string{"tracker:TASK-1"}},
		{From: "billing", To: "stories", Kind: "shared_item", Items: []string{"tracker:STORY-1"}},
		{From: "stories", To: "billing", Kind: "parent", Items: []string{"tracker:STORY-1"}},
	}
	if got := links(t, doc); !reflect.DeepEqual(got, want) {
		t.Errorf("workstream_links = %+v\nwant %+v", got, want)
	}
}

func TestAGitHubSubIssueJoinsItsAdoptedParentsWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubMembers("alice/plans#1"))
	f.withGitHub(
		ghNode{repo: "Alice/Plans", number: 1, title: "Epic", state: "OPEN", created: 72 * time.Hour, updated: time.Hour},
		ghNode{repo: "Alice/App", number: 4, title: "Story", state: "OPEN", created: 72 * time.Hour, updated: time.Hour, assigned: true, parentRepo: "Alice/Plans", parentNumber: 1},
		// Its parent, alice/app#5, is neither adopted nor read, since discovery
		// reads only open issues assigned to you, so the chain stops there.
		ghNode{repo: "Alice/App", number: 6, title: "Sub-task", state: "OPEN", created: 72 * time.Hour, updated: time.Hour, assigned: true, parentRepo: "Alice/App", parentNumber: 5},
	)

	doc := f.read()

	if got, want := joins(t, doc, "review"), map[string]joinedDoc{"hub:alice/app#4": byParent("hub:alice/plans#1")}; !equalJoins(got, want) {
		t.Errorf("review joins = %v, want %v", got, want)
	}
	unplacedItem(t, doc, "hub:alice/app#6")
}

func TestReadingParentJoinsWritesNothing(t *testing.T) {
	f := newFixture(t)
	path := f.write("personal.yml", epics)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("STORY-1", time.Hour, map[string]any{"parent": "EPIC-1"}),
	}, nil))

	for _, args := range [][]string{{"read", "--json"}, {"show"}} {
		if code, _, stderr := f.run(args...); code != 0 {
			t.Fatalf("%v: exit code = %d, stderr = %q", args, code, stderr)
		}
	}

	if got := readFile(t, path); got != epics {
		t.Errorf("configuration changed by a read:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(f.stateDir("personal"), "gestures.jsonl")); !os.IsNotExist(err) {
		t.Errorf("gesture log after reads: err = %v, want none written", err)
	}
}

func TestShowAndSnapshotCountAParentJoinAsAJoin(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", epics)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("EPIC-1", time.Hour, nil),
		item("STORY-1", time.Hour, map[string]any{"parent": "EPIC-1"}),
	}, nil))

	code, stdout, stderr := f.run("show")
	if code != 0 {
		t.Fatalf("show: exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "tracker 1 +1 joined") {
		t.Errorf("show =\n%s\nwant invoicing counting its child apart as joined", stdout)
	}

	b := f.snapshot()
	if !slices.Equal(b.Joined, []string{"tracker:STORY-1"}) || len(b.HandPlaced) != 0 {
		t.Errorf("snapshot joined = %v, hand_placed = %v, want the child joined and nothing placed by hand", b.Joined, b.HandPlaced)
	}
}

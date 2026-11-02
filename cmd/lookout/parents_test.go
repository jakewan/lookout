package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// parented is a setting whose tracker supplies an epic, its stories, and a
// task both teams share.
const parented = `
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1", "tracker:ABC-7"]
  - name: exports
    members: ["tracker:ABC-2", "tracker:ABC-3", "tracker:ABC-7"]
  - name: archive
    members: ["tracker:ABC-4"]
    completed: 2030-06-01
`

func (f *fixture) withParentedTracker() {
	f.t.Helper()
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", time.Hour, map[string]any{"title": "Invoicing epic"}),
		item("ABC-2", time.Hour, map[string]any{"parent": "ABC-1"}),
		item("ABC-3", time.Hour, map[string]any{"parent": "ABC-1"}),
		item("ABC-4", time.Hour, map[string]any{"parent": "ABC-1"}),
		item("ABC-7", time.Hour, map[string]any{"parent": "ABC-7"}),
	}, nil))
}

func placedItem(t *testing.T, doc readoutDoc, workstream, id string) map[string]any {
	t.Helper()
	for _, w := range doc.Workstreams {
		if w.Name != workstream {
			continue
		}
		for _, p := range w.Items {
			if p.Item["id"] == id {
				return p.Item
			}
		}
	}
	t.Fatalf("no item %s in workstream %s", id, workstream)
	return nil
}

func TestASuppliedParentReachesTheReadout(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", parented)
	f.withParentedTracker()

	doc := f.read()

	if got := placedItem(t, doc, "exports", "ABC-2")["parent"]; got != "ABC-1" {
		t.Errorf("ABC-2 parent = %v, want the id its supplier gave", got)
	}
	if _, ok := placedItem(t, doc, "billing", "ABC-1")["parent"]; ok {
		t.Errorf("ABC-1 carries a parent, want none where the supplier gave none")
	}
}

func TestABlankParentDegradesItsInput(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", parented)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-2", time.Hour, map[string]any{"parent": ""}),
	}, nil))

	doc := f.read()

	h := doc.health(t, "tracker")
	if h.Status != "degraded" || !reflect.DeepEqual(h.Reasons, []string{"schema"}) || len(h.SchemaViolations) == 0 {
		t.Errorf("health = %+v, want degraded by schema: a parent names an item", h)
	}
}

func TestWorkstreamLinksNameTheItemsBehindThem(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", parented)
	f.withParentedTracker()

	doc := f.read()

	want := []linkDoc{
		{From: "billing", To: "exports", Kind: "parent", Items: []string{"tracker:ABC-2", "tracker:ABC-3"}},
		{From: "billing", To: "exports", Kind: "shared_item", Items: []string{"tracker:ABC-7"}},
	}
	if got := links(t, doc); !reflect.DeepEqual(got, want) {
		t.Errorf("workstream_links = %+v\nwant %+v: a parent in one workstream with its children in another, and an item both hold; no link to a completed workstream, and none from an item that names itself its parent", got, want)
	}
}

func TestWorkstreamLinksFollowJoinsAndStatedOrder(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", noted)
	f.withNotedTracker()
	f.supplied("agents.json", suppliedDoc("agents", []any{
		item("session-1", time.Hour, map[string]any{"notes": "Ticket: ABC-1, then XYZ-9"}),
	}, nil))

	doc := f.read()

	want := []linkDoc{{From: "billing", To: "onboarding", Kind: "shared_item", Items: []string{"agents:session-1"}}}
	if got := links(t, doc); !reflect.DeepEqual(got, want) {
		t.Errorf("workstream_links = %+v, want %+v: a joined item counts as held, and the earlier workstream in stated order comes first", got, want)
	}
}

func TestWorkstreamLinksAreEmptyWhenNothingRelates(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", oneInput)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", time.Hour, nil),
		item("ABC-2", time.Hour, map[string]any{"parent": "ABC-1"}),
		item("ABC-9", time.Hour, map[string]any{"parent": "ABC-404"}),
	}, nil))

	doc := f.read()

	if got := links(t, doc); len(got) != 0 {
		t.Errorf("workstream_links = %v, want an empty list: a parent in the same workstream, or one no workstream holds, relates none", got)
	}
}

// links is the readout's workstream_links, which every readout carries.
func links(t *testing.T, doc readoutDoc) []linkDoc {
	t.Helper()
	if doc.WorkstreamLinks == nil {
		t.Fatal("workstream_links missing, want it always emitted")
	}
	return *doc.WorkstreamLinks
}

func TestAGitHubSubIssueCarriesItsParentInsideTheFence(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubMembers("alice/app#4", "alice/app#5"))
	f.withGitHub(
		ghNode{repo: "Alice/App", number: 4, title: "Child", state: "OPEN", created: 72 * time.Hour, updated: time.Hour, parentRepo: "Alice/Plans", parentNumber: 1},
		ghNode{repo: "Alice/App", number: 5, title: "Fenced child", state: "OPEN", created: 72 * time.Hour, updated: time.Hour, parentRepo: "Mallory/Secret-Plans", parentNumber: 9},
	)

	code, stdout, stderr := f.run("read", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	validate(t, "readout.v1", stdout)
	var doc readoutDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decoding readout: %v", err)
	}

	if got := placedItem(t, doc, "review", "alice/app#4")["parent"]; got != "alice/plans#1" {
		t.Errorf("alice/app#4 parent = %v, want its parent's item id", got)
	}
	if _, ok := placedItem(t, doc, "review", "alice/app#5")["parent"]; ok {
		t.Errorf("alice/app#5 carries a parent, want none whose owner is outside forge_owners")
	}
	if strings.Contains(strings.ToLower(stdout), "mallory") {
		t.Errorf("readout names an owner outside forge_owners:\n%s", stdout)
	}
}

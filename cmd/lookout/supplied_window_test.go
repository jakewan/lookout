package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// windowedAgents is the notes setting with a discovery window, whose agents
// supplier may or may not be windowed; review names one of its items.
func windowedAgents(windowed bool) string {
	cfg := strings.Replace(noted, "key_pattern:", "discovery_window: 72h\nkey_pattern:", 1)
	if windowed {
		cfg = strings.Replace(cfg, "supplied: agents.json\n", "supplied: agents.json\n    windowed: true\n", 1)
	}
	return cfg + `  - name: review
    members: ["agents:kept"]
`
}

// withAgedAgents writes an agents file holding items on each side of the
// window: an old member, an old item whose notes join a ticket, old ones
// nothing places, and a recent one.
func (f *fixture) withAgedAgents() {
	f.t.Helper()
	f.supplied("agents.json", suppliedDoc("agents", []any{
		item("kept", 100*time.Hour, nil),
		item("joined", 100*time.Hour, map[string]any{"notes": "ABC-1"}),
		item("old", 100*time.Hour, nil),
		item("old-dismissed", 100*time.Hour, nil),
		item("recent", time.Hour, nil),
	}, nil))
}

func TestAWindowedSupplierOffersOnlyItemsWithinTheDiscoveryWindow(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", windowedAgents(true))
	f.withNotedTracker()
	f.withAgedAgents()
	f.mustRun("dismiss", "agents:old-dismissed")

	doc := f.read()

	if got := unplacedIDs(doc); !slices.Equal(got, []string{"agents:recent"}) {
		t.Errorf("unplaced = %v, want only the item that moved within discovery_window", got)
	}
	if doc.DismissedCount != 0 {
		t.Errorf("dismissed_count = %d, want 0: an item too old to be offered is not counted as dismissed either", doc.DismissedCount)
	}
	if got, want := joins(t, doc, "billing"), map[string]joinedDoc{"agents:joined": {By: "notes", Member: "tracker:ABC-1"}}; !equalJoins(got, want) {
		t.Errorf("billing joins = %v, want the old item its notes join, at any age", got)
	}
	for _, w := range doc.Workstreams {
		if w.Name == "review" && !slices.ContainsFunc(w.Items, func(p placedDoc) bool { return p.Input == "agents" && p.Item["id"] == "kept" }) {
			t.Errorf("review items = %v, want its old member, read at any age", w.Items)
		}
	}
	if h := doc.health(t, "agents"); h.Status != "ok" || h.RecordCount != 5 {
		t.Errorf("agents health = %+v, want ok with every item counted: windowing decides what is offered, not what was read", h)
	}
}

func TestASupplierIsNotWindowedUnlessItSaysSo(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", windowedAgents(false))
	f.withNotedTracker()
	f.withAgedAgents()
	f.mustRun("dismiss", "agents:old-dismissed")

	doc := f.read()

	got := unplacedIDs(doc)
	slices.Sort(got)
	if want := []string{"agents:old", "agents:recent"}; !slices.Equal(got, want) {
		t.Errorf("unplaced = %v, want %v: a supplied item is offered at any age", got, want)
	}
	if doc.DismissedCount != 1 {
		t.Errorf("dismissed_count = %d, want the old dismissed item counted", doc.DismissedCount)
	}
}

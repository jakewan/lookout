package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// activeDoc is the two verdicts a readout publishes for each workstream, so
// every renderer and an agent reading the readout draw the same ones.
type activeDoc struct {
	Workstreams []struct {
		Name   string `json:"name"`
		Active *bool  `json:"active"`
		Behind *bool  `json:"behind"`
	} `json:"workstreams"`
}

func (f *fixture) verdicts() activeDoc {
	f.t.Helper()
	code, stdout, stderr := f.run("read", "--json")
	if code != 0 {
		f.t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	validate(f.t, "readout.v1", stdout)
	var doc activeDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		f.t.Fatalf("decoding readout: %v", err)
	}
	for _, w := range doc.Workstreams {
		if w.Active == nil || w.Behind == nil {
			f.t.Fatalf("workstream %s: active = %v, behind = %v, want both published", w.Name, w.Active, w.Behind)
		}
	}
	return doc
}

func TestReadPublishesWhetherEachWorkstreamIsActive(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
event_window: 48h
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
  - name: onboarding
    members: ["tracker:ABC-2"]
  - name: docs
    members: ["tracker:ABC-9"]
`)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", 24*time.Hour, nil),
		item("ABC-2", 72*time.Hour, nil),
	}, nil))

	var got []bool
	for _, w := range f.verdicts().Workstreams {
		got = append(got, *w.Active)
	}
	if want := []bool{true, false, false}; !slices.Equal(got, want) {
		t.Errorf("active = %v, want %v: billing moved within the window, onboarding before it, and docs had nothing read", got, want)
	}
}

// At the window's edge a touch can fall just before the window opens while
// its idle time, floored to whole seconds, equals the window exactly. Such a
// workstream has no event, so it is not active, and every view must then call
// it stale rather than neither.
func TestAWorkstreamTouchedJustBeforeTheWindowOpensIsStaleEverywhere(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
event_window: 48h
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
`)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", 48*time.Hour, nil)}, nil))
	f.clock = now.Add(500 * time.Millisecond)

	if w := f.verdicts().Workstreams[0]; *w.Active {
		t.Errorf("active = true, want false: the touch is before the window opens")
	}
	code, stdout, stderr := f.run("show")
	if code != 0 {
		t.Fatalf("show exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "  billing  ") || !strings.Contains(stdout, " stale ") {
		t.Errorf("show =\n%s\nwant billing marked stale", stdout)
	}
	if b := f.snapshot(); b.ActiveWorkstreams != 0 {
		t.Errorf("active_workstreams = %d, want 0", b.ActiveWorkstreams)
	}
}

// A workstream is behind when one the stated order ranks below it was touched
// more recently; the vision ranks by stated order and lets observed recency
// flag drift from it.
func TestReadPublishesWhetherEachWorkstreamIsBehindItsRank(t *testing.T) {
	for name, tc := range map[string]struct {
		// hours since each workstream's touch, in stated order; -1 is a
		// workstream with nothing read.
		hours []int
		want  []bool
	}{
		"freshest first":      {[]int{1, 2, 3, 4}, []bool{false, false, false, false}},
		"one overtaken":       {[]int{1, 5, 2, 3}, []bool{false, true, false, false}},
		"the top one quiet":   {[]int{9, 1, 2, 3}, []bool{true, false, false, false}},
		"a tie is not behind": {[]int{1, 2, 2, 3}, []bool{false, false, false, false}},
		// A workstream with nothing read has no touch to compare, so it
		// neither falls behind nor puts another behind.
		"unread on either side": {[]int{-1, 2, 1, -1}, []bool{false, true, false, false}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			var config strings.Builder
			config.WriteString(`
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
`)
			var items []any
			for i, h := range tc.hours {
				fmt.Fprintf(&config, "  - name: ws%d\n    members: [\"tracker:ABC-%d\"]\n", i, i)
				if h >= 0 {
					items = append(items, item(fmt.Sprintf("ABC-%d", i), time.Duration(h)*time.Hour, nil))
				}
			}
			f.write("personal.yml", config.String())
			f.supplied("tracker.json", suppliedDoc("tracker", items, nil))

			var got []bool
			for _, w := range f.verdicts().Workstreams {
				got = append(got, *w.Behind)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("behind = %v, want %v", got, tc.want)
			}
		})
	}
}

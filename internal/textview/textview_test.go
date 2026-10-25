package textview

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// generated is the instant every fixture readout was taken at.
var generated = time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)

const (
	minute = 60
	hour   = 60 * minute
	day    = 24 * hour
)

// unread marks a workstream whose last_touched is null.
const unread = -1

// lane is one workstream of a fixture readout. Members are <input>:<id>.
type lane struct {
	name       string
	idle       int64
	items      []string
	unread     []string
	incomplete []string
	extra      map[string]any
}

// fixture is a readout built for one case. Only the fields the view reads are
// varied; the rest are filled in as readout.v1 requires.
type fixture struct {
	setting   string
	window    int64
	lanes     []lane
	health    []map[string]any
	unplaced  int
	dismissed int
	completed int
	extra     map[string]any
}

func (f fixture) readout() map[string]any {
	setting := f.setting
	if setting == "" {
		setting = "personal"
	}
	lanes := make([]any, 0, len(f.lanes))
	for _, l := range f.lanes {
		items := make([]any, 0, len(l.items))
		for _, m := range l.items {
			input, id, _ := strings.Cut(m, ":")
			items = append(items, placed(input, id))
		}
		w := map[string]any{
			"name":              l.name,
			"last_touched":      nil,
			"idle_seconds":      nil,
			"started_at":        nil,
			"age_seconds":       nil,
			"events":            []any{},
			"inputs_incomplete": nonNil(l.incomplete),
			"members_unread":    nonNil(l.unread),
			"items":             items,
		}
		if l.idle != unread {
			w["last_touched"] = generated.Add(-time.Duration(l.idle) * time.Second).Format(time.RFC3339)
			w["idle_seconds"] = l.idle
		}
		maps.Copy(w, l.extra)
		lanes = append(lanes, w)
	}
	unplaced := make([]any, 0, f.unplaced)
	for i := range f.unplaced {
		unplaced = append(unplaced, placed("tracker", "XYZ-"+string(rune('1'+i))))
	}
	health := make([]any, 0, len(f.health))
	for _, h := range f.health {
		health = append(health, h)
	}
	doc := map[string]any{
		"schema":               "readout.v1",
		"setting":              setting,
		"generated_at":         generated.Format(time.RFC3339),
		"event_window_seconds": f.window,
		"workstreams":          lanes,
		"unplaced":             unplaced,
		"health":               health,
		"dismissed_count":      f.dismissed,
		"completed_count":      f.completed,
	}
	maps.Copy(doc, f.extra)
	return doc
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func placed(input, id string) map[string]any {
	return map[string]any{"input": input, "item": map[string]any{
		"id":           id,
		"last_touched": generated.Add(-time.Hour).Format(time.RFC3339),
		"state":        "open",
		"links":        []any{},
		"attrs":        map[string]any{},
	}}
}

func input(name, status string, reasons ...string) map[string]any {
	return map[string]any{
		"input":             name,
		"status":            status,
		"reasons":           nonNil(reasons),
		"fetched_at":        generated.Add(-time.Hour).Format(time.RFC3339),
		"record_count":      1,
		"missing_fields":    []string{},
		"schema_violations": []string{},
		"supplier_reason":   nil,
		"details":           []string{},
		"excluded_count":    0,
	}
}

func ok(names ...string) []map[string]any {
	hs := make([]map[string]any, 0, len(names))
	for _, n := range names {
		hs = append(hs, input(n, "ok"))
	}
	return hs
}

var cases = map[string]fixture{
	"health-states": {
		window: 7 * day,
		lanes:  []lane{{name: "billing", idle: 2 * hour, items: []string{"tracker:ABC-1"}}},
		health: []map[string]any{
			input("tracker", "ok"),
			input("github", "degraded", "truncated", "rate_limited"),
			input("git", "error", "missing"),
			input("notes", "absent_by_design"),
			input("sessions", "degraded", "some_new_reason"),
		},
	},
	"incomplete": {
		window: 7 * day,
		lanes: []lane{
			{name: "checkout", idle: 5 * minute, items: []string{"tracker:ABC-1", "tracker:ABC-2"}},
			{name: "billing", idle: 9 * day, items: []string{"github:owner/repo#2"}, incomplete: []string{"github"}},
		},
		health:    []map[string]any{input("tracker", "ok"), input("github", "degraded", "truncated")},
		unplaced:  3,
		dismissed: 2,
		completed: 1,
	},
	"empty": {window: 7 * day},
	"empty-completed": {
		window:    7 * day,
		health:    ok("tracker"),
		dismissed: 1,
		completed: 2,
	},
	"staleness": {
		window: hour,
		lanes: []lane{
			{name: "under", idle: hour - 1, items: []string{"tracker:ABC-1"}},
			{name: "equal", idle: hour, items: []string{"tracker:ABC-2"}},
			{name: "over", idle: hour + 1, items: []string{"tracker:ABC-3"}},
			{name: "nothing", idle: unread, unread: []string{"tracker:ABC-9"}},
		},
		health: ok("tracker"),
	},
	"ages": {
		window: 400 * day,
		lanes: []lane{
			{name: "s0", idle: 0, items: []string{"tracker:ABC-1"}},
			{name: "s59", idle: minute - 1, items: []string{"tracker:ABC-2"}},
			{name: "m1", idle: minute, items: []string{"tracker:ABC-3"}},
			{name: "m59", idle: hour - 1, items: []string{"tracker:ABC-4"}},
			{name: "h23", idle: day - 1, items: []string{"tracker:ABC-5"}},
			{name: "d1", idle: day, items: []string{"tracker:ABC-6"}},
			{name: "d365", idle: 365 * day, items: []string{"tracker:ABC-7"}},
		},
		health: ok("tracker"),
	},
	"window-zero": {
		window: 0,
		lanes: []lane{
			{name: "quiet", idle: 0, items: []string{"tracker:ABC-1"}},
			{name: "moved", idle: 5, items: []string{"tracker:ABC-2"}},
		},
		health: ok("tracker"),
	},
	"counts": {
		window: 7 * day,
		lanes: []lane{
			{
				name:   "mixed",
				idle:   minute,
				items:  []string{"github:owner/repo#1", "git:owner/repo:feature-x", "tracker:ABC-1", "tracker:ABC-2"},
				unread: []string{"tracker:ABC-3", "sessions:owner/repo:feature-x"},
			},
			{name: "empty-lane", idle: unread},
		},
		health: ok("tracker", "github", "git", "sessions"),
	},
	"width": {
		setting: "a-very-long-setting-name-indeed",
		window:  7 * day,
		lanes: []lane{
			{name: "the-quarterly-reconciliation-project", idle: hour, items: []string{"tracker:ABC-1"}},
			{name: "the-quarterly-reconciliation-rerun", idle: 2 * hour, items: []string{"tracker:ABC-2"}},
			{
				name: "wide",
				idle: minute,
				items: []string{
					"tracker:ABC-3", "tracker:ABC-4", "tracker:ABC-5",
					"github:owner/repo#1", "github:owner/repo#2",
					"git:owner/repo:wide", "sessions:owner/repo:wide", "notes:N-1",
					"an-input-with-a-name-far-beyond-limits:L-1",
				},
				unread:     []string{"an-input-with-a-name-far-beyond-limits:L-2", "an-input-with-a-name-far-beyond-limits:L-3"},
				incomplete: []string{"an-input-with-a-name-far-beyond-limits"},
			},
		},
		health: append(ok("tracker", "github", "git", "sessions", "notes"),
			input("an-input-with-a-name-far-beyond-limits", "degraded",
				"stale", "schema", "supplier_degraded", "missing_fields", "clock_skew", "duplicate_id",
				"reason_named_at_length_to_need_a_line_of_its_own_in_the_view")),
	},
	"sanitize": {
		window: 7 * day,
		lanes: []lane{
			{name: "tab\there", idle: minute, items: []string{"tracker:ABC-1"}},
			{name: "esc\x1b[31mred", idle: minute, items: []string{"tracker:ABC-2"}},
		},
		health: ok("tracker"),
	},
	"unknown-fields": {
		window: 7 * day,
		lanes:  []lane{{name: "billing", idle: minute, items: []string{"tracker:ABC-1"}, extra: map[string]any{"lane_color": "blue"}}},
		health: []map[string]any{func() map[string]any {
			h := input("tracker", "ok")
			h["trend"] = "up"
			return h
		}()},
		extra: map[string]any{"renderer_hint": "compact"},
	},
}

func TestRenderMatchesTheGoldenView(t *testing.T) {
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			data := encode(t, f.readout())
			var out bytes.Buffer
			if err := Render(&out, data); err != nil {
				t.Fatalf("Render: %v", err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != string(want) {
				t.Errorf("view =\n%s\nwant\n%s", out.String(), want)
			}
		})
	}
}

// TestGoldenViewsFitEightyColumns guards the goldens themselves, so a golden
// edited by hand cannot quietly promise a line the view should never print.
func TestGoldenViewsFitEightyColumns(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(cases) {
		t.Errorf("%d golden files for %d cases", len(paths), len(cases))
	}
	for _, p := range paths {
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			t.Fatal(readErr)
		}
		requireFits(t, p, string(data))
	}
}

// TestEveryViewFitsEightyColumnsAndKeepsEveryReason renders readouts with
// names, member lists and reason lists of every length, which the goldens
// sample only at a few points.
func TestEveryViewFitsEightyColumnsAndKeepsEveryReason(t *testing.T) {
	reasons := []string{
		"missing", "unreadable", "schema", "input_mismatch", "stale", "supplier_failed",
		"supplier_error", "supplier_degraded", "missing_fields", "empty", "clock_skew",
		"duplicate_id", "not_a_repository", "git_failed", "auth_failed", "rate_limited",
		"truncated", "github_failed", "unrecognized_format", strings.Repeat("r", 130),
	}
	for n := 1; n <= 70; n += 3 {
		for _, letter := range []string{"a", "é"} {
			name := strings.Repeat(letter, n)
			inputName := strings.Repeat("i", n)
			members := make([]string, 0, n)
			unreadMembers := make([]string, 0, n)
			for i := range n {
				members = append(members, inputName+":ABC-"+string(rune('a'+i%26)))
				unreadMembers = append(unreadMembers, "tracker:XYZ-"+string(rune('a'+i%26)))
			}
			f := fixture{
				setting: name,
				window:  day,
				lanes: []lane{
					{name: name, idle: 999 * day, items: members, unread: unreadMembers, incomplete: []string{inputName}},
					{name: name + "-twin", idle: unread, unread: unreadMembers, incomplete: []string{inputName}},
				},
				health: []map[string]any{input("tracker", "ok"), input(inputName, "error", reasons...)},
			}
			var out bytes.Buffer
			if err := Render(&out, encode(t, f.readout())); err != nil {
				t.Fatalf("Render: %v", err)
			}
			requireFits(t, name, out.String())
			flat := strings.Join(strings.Fields(out.String()), "")
			for _, r := range reasons {
				if !strings.Contains(flat, r) {
					t.Errorf("names %d long: reason %q is missing from the view\n%s", n, r, out.String())
				}
			}
		}
	}
}

func requireFits(t *testing.T, name, view string) {
	t.Helper()
	for i, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
		if n := utf8.RuneCountInString(line); n > 80 {
			t.Errorf("%s: line %d is %d columns: %q", name, i+1, n, line)
		}
		if strings.TrimRightFunc(line, unicode.IsSpace) != line {
			t.Errorf("%s: line %d ends in whitespace: %q", name, i+1, line)
		}
	}
}

// encode marshals a fixture readout and holds it to readout.v1, and to two
// facts the schema cannot state but every readout lookout writes keeps, so a
// golden never pins a view of a readout lookout could not produce.
func encode(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := schema.Compile("readout.v1")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("fixture violates readout.v1: %v", err)
	}
	var r struct {
		Workstreams []struct {
			Name             string   `json:"name"`
			LastTouched      *string  `json:"last_touched"`
			IdleSeconds      *int64   `json:"idle_seconds"`
			InputsIncomplete []string `json:"inputs_incomplete"`
		} `json:"workstreams"`
		Health []struct {
			Input  string `json:"input"`
			Status string `json:"status"`
		} `json:"health"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, h := range r.Health {
		status[h.Input] = h.Status
	}
	for _, w := range r.Workstreams {
		if (w.LastTouched == nil) != (w.IdleSeconds == nil) {
			t.Fatalf("fixture workstream %q: last_touched and idle_seconds disagree on null", w.Name)
		}
		for _, in := range w.InputsIncomplete {
			if s := status[in]; s != "error" && s != "degraded" {
				t.Fatalf("fixture workstream %q: incomplete input %q is %q, not error or degraded", w.Name, in, s)
			}
		}
	}
	return data
}

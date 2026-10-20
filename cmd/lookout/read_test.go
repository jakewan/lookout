package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// now is the instant every spec reads at, so ages and staleness are exact.
var now = time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)

func stamp(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

// fixture is one setting's configuration directory, built per spec.
type fixture struct {
	t   *testing.T
	dir string
	env map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "config", "lookout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, dir: dir, env: map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, "config"),
	}}
}

func (f *fixture) write(name, content string) string {
	f.t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) supplied(name string, doc map[string]any) {
	f.t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(name, string(data))
}

func (f *fixture) run(args ...string) (int, string, string) {
	f.t.Helper()
	var stdout, stderr bytes.Buffer
	e := environment{
		getenv: func(k string) string { return f.env[k] },
		now:    func() time.Time { return now },
	}
	code := run(args, e, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// read runs `lookout read --json`, requires it to succeed, checks the output
// against the published readout contract, and decodes it.
func (f *fixture) read(args ...string) readoutDoc {
	f.t.Helper()
	code, stdout, stderr := f.run(append([]string{"read", "--json"}, args...)...)
	if code != 0 {
		f.t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	validate(f.t, "readout.v1", stdout)
	var doc readoutDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		f.t.Fatalf("decoding readout: %v", err)
	}
	return doc
}

func validate(t *testing.T, contract, document string) {
	t.Helper()
	sch, err := schema.Compile(contract)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(document))
	if err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, document)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("output violates %s: %v\n%s", contract, err, document)
	}
}

// readoutDoc decodes only what the specs assert on; the contract check above
// covers the rest of the shape.
type readoutDoc struct {
	Setting            string `json:"setting"`
	EventWindowSeconds int64  `json:"event_window_seconds"`
	Workstreams        []struct {
		Name             string   `json:"name"`
		LastTouched      *string  `json:"last_touched"`
		IdleSeconds      *int64   `json:"idle_seconds"`
		StartedAt        *string  `json:"started_at"`
		AgeSeconds       *int64   `json:"age_seconds"`
		InputsIncomplete []string `json:"inputs_incomplete"`
		Events           []struct {
			Input string `json:"input"`
			ID    string `json:"id"`
			Title string `json:"title"`
			At    string `json:"at"`
		} `json:"events"`
	} `json:"workstreams"`
	Unplaced []struct {
		Input string         `json:"input"`
		Item  map[string]any `json:"item"`
	} `json:"unplaced"`
	Health []healthDoc `json:"health"`
}

type healthDoc struct {
	Input            string   `json:"input"`
	Status           string   `json:"status"`
	Reasons          []string `json:"reasons"`
	FetchedAt        *string  `json:"fetched_at"`
	RecordCount      int      `json:"record_count"`
	MissingFields    []string `json:"missing_fields"`
	SchemaViolations []string `json:"schema_violations"`
	SupplierReason   *string  `json:"supplier_reason"`
}

func (d readoutDoc) health(t *testing.T, input string) healthDoc {
	t.Helper()
	for _, h := range d.Health {
		if h.Input == input {
			return h
		}
	}
	t.Fatalf("no health record for %q in %+v", input, d.Health)
	return healthDoc{}
}

func item(id string, touched time.Duration, extra map[string]any) map[string]any {
	it := map[string]any{
		"id":           id,
		"last_touched": stamp(touched),
		"state":        "open",
		"links":        []any{},
		"attrs":        map[string]any{},
	}
	for k, v := range extra {
		it[k] = v
	}
	return it
}

func suppliedDoc(input string, items []any, extra map[string]any) map[string]any {
	doc := map[string]any{
		"schema":          "supplied.v1",
		"input":           input,
		"supplier":        "test-supplier",
		"fetched_at":      stamp(time.Hour),
		"attempted_at":    stamp(time.Hour),
		"supplier_status": "ok",
		"items":           items,
	}
	for k, v := range extra {
		doc[k] = v
	}
	return doc
}

const oneInput = `
event_window: 336h
inputs:
  - name: tracker
    supplied: tracker.json
    max_age: 24h
    expect_records: true
workstreams:
  - name: billing
    members: ["tracker:ABC-1", "tracker:ABC-2"]
  - name: onboarding
    members: ["tracker:ABC-9"]
`

func TestReadEmitsReadoutInStatedOrder(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", oneInput)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", 3*time.Hour, map[string]any{"title": "Invoice export", "started_at": stamp(10 * 24 * time.Hour)}),
		item("ABC-2", 30*time.Hour, nil),
		item("ABC-3", time.Hour, map[string]any{"title": "Not in any workstream"}),
	}, nil))

	doc := f.read()

	if doc.Setting != "personal" {
		t.Errorf("setting = %q, want the configuration file's name", doc.Setting)
	}
	if doc.EventWindowSeconds != int64((336 * time.Hour).Seconds()) {
		t.Errorf("event_window_seconds = %d", doc.EventWindowSeconds)
	}
	names := []string{}
	for _, w := range doc.Workstreams {
		names = append(names, w.Name)
	}
	if !slices.Equal(names, []string{"billing", "onboarding"}) {
		t.Fatalf("workstreams = %v, want the stated order", names)
	}

	billing := doc.Workstreams[0]
	if billing.LastTouched == nil || *billing.LastTouched != stamp(3*time.Hour) {
		t.Errorf("billing last_touched = %v, want the latest member touch", billing.LastTouched)
	}
	if billing.IdleSeconds == nil || *billing.IdleSeconds != int64((3*time.Hour).Seconds()) {
		t.Errorf("billing idle_seconds = %v", billing.IdleSeconds)
	}
	if billing.StartedAt == nil || *billing.StartedAt != stamp(10*24*time.Hour) {
		t.Errorf("billing started_at = %v, want the earliest member start", billing.StartedAt)
	}
	if billing.AgeSeconds == nil || *billing.AgeSeconds != int64((10*24*time.Hour).Seconds()) {
		t.Errorf("billing age_seconds = %v", billing.AgeSeconds)
	}
	if len(billing.Events) != 2 || billing.Events[0].ID != "ABC-1" || billing.Events[0].Title != "Invoice export" {
		t.Errorf("billing events = %+v, want both members, newest first", billing.Events)
	}
	if len(billing.InputsIncomplete) != 0 {
		t.Errorf("billing inputs_incomplete = %v, want none", billing.InputsIncomplete)
	}

	onboarding := doc.Workstreams[1]
	if onboarding.LastTouched != nil || onboarding.IdleSeconds != nil || len(onboarding.Events) != 0 {
		t.Errorf("onboarding = %+v, want no touches: its only member was not read", onboarding)
	}

	if len(doc.Unplaced) != 1 || doc.Unplaced[0].Input != "tracker" || doc.Unplaced[0].Item["id"] != "ABC-3" {
		t.Errorf("unplaced = %+v, want only ABC-3", doc.Unplaced)
	}

	h := doc.health(t, "tracker")
	if h.Status != "ok" || len(h.Reasons) != 0 || h.RecordCount != 3 {
		t.Errorf("health = %+v, want ok with three records", h)
	}
	if h.FetchedAt == nil || *h.FetchedAt != stamp(time.Hour) {
		t.Errorf("fetched_at = %v", h.FetchedAt)
	}
}

func TestReadEventsStopAtTheWindow(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
event_window: 48h
inputs:
  - name: tracker
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1", "tracker:ABC-2"]
`)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", 72*time.Hour, nil),
		item("ABC-2", 24*time.Hour, nil),
	}, nil))

	doc := f.read()

	w := doc.Workstreams[0]
	if len(w.Events) != 1 || w.Events[0].ID != "ABC-2" {
		t.Errorf("events = %+v, want only the touch inside the window", w.Events)
	}
	if w.LastTouched == nil || *w.LastTouched != stamp(24*time.Hour) {
		t.Errorf("last_touched = %v: the window bounds events, not recency", w.LastTouched)
	}
}

// TestReadHealth pins how an expected supplied input's health is computed:
// from what lookout can observe, with the supplier's own report able to lower
// the status but never raise it.
func TestReadHealth(t *testing.T) {
	healthyItems := []any{item("ABC-1", time.Hour, nil)}
	cases := []struct {
		name      string
		file      any // nil: no file at all; directory: a directory in its place; string: raw bytes; map: JSON document
		expect    bool
		status    string
		reasons   []string
		missing   []string
		violation bool
		foreign   bool // the record must carry nothing read from the file
		reason    string
	}{
		{name: "unreadable", file: directory{}, status: "error", reasons: []string{"unreadable"}},
		// Both forms are valid RFC 3339, so the schema passes them, but Go's time
		// parser rejects them; the input must degrade rather than fail the run.
		{name: "lowercase time separator", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"fetched_at": "2030-06-15t11:00:00Z", "attempted_at": "2030-06-15t11:00:00Z",
		}), status: "degraded", reasons: []string{"schema"}, violation: true, foreign: true},
		{name: "leap second in an item", file: suppliedDoc("tracker", []any{
			map[string]any{"id": "ABC-1", "last_touched": "2030-06-30T23:59:60Z", "state": "open", "links": []any{}, "attrs": map[string]any{}},
		}, nil), status: "degraded", reasons: []string{"schema"}, violation: true, foreign: true},
		{name: "written for another input that reports error", file: suppliedDoc("calendar", healthyItems, map[string]any{
			"supplier_status": "error", "fetched_at": stamp(30 * time.Hour),
		}), status: "degraded", reasons: []string{"input_mismatch"}, foreign: true},
		{name: "supplier reason is carried", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"supplier_status": "degraded", "reason": "estimates hidden by the source",
		}), status: "degraded", reasons: []string{"supplier_degraded"}, reason: "estimates hidden by the source"},
		{name: "healthy", file: suppliedDoc("tracker", healthyItems, nil), status: "ok"},
		{name: "expected but missing", file: nil, status: "error", reasons: []string{"missing"}},
		{name: "older than max_age", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"fetched_at": stamp(25 * time.Hour), "attempted_at": stamp(25 * time.Hour),
		}), status: "degraded", reasons: []string{"stale"}},
		{name: "last attempt failed", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"fetched_at": stamp(2 * time.Hour), "attempted_at": stamp(time.Hour),
		}), status: "degraded", reasons: []string{"supplier_failed"}},
		{name: "fails its schema", file: suppliedDoc("tracker", []any{
			map[string]any{"id": "ABC-1", "last_touched": stamp(time.Hour), "links": []any{}, "attrs": map[string]any{}},
		}, nil), status: "degraded", reasons: []string{"schema"}, violation: true},
		{name: "not JSON", file: "{not json", status: "degraded", reasons: []string{"schema"}, violation: true},
		{name: "written for another input", file: suppliedDoc("calendar", healthyItems, nil),
			status: "degraded", reasons: []string{"input_mismatch"}},
		{name: "supplier reports error", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"supplier_status": "error", "reason": "source refused the request",
		}), status: "error", reasons: []string{"supplier_error"}, reason: "source refused the request"},
		{name: "supplier error is never softened by freshness", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"supplier_status": "error", "fetched_at": stamp(30 * time.Hour), "attempted_at": stamp(time.Hour),
		}), status: "error", reasons: []string{"supplier_error", "stale", "supplier_failed"}},
		{name: "supplier reports degraded with missing fields", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"supplier_status": "degraded", "missing_fields": []any{"estimate"},
		}), status: "degraded", reasons: []string{"supplier_degraded", "missing_fields"}, missing: []string{"estimate"}},
		{name: "missing fields under an ok status", file: suppliedDoc("tracker", healthyItems, map[string]any{
			"missing_fields": []any{"estimate"},
		}), status: "degraded", reasons: []string{"missing_fields"}, missing: []string{"estimate"}},
		{name: "empty where records are expected", file: suppliedDoc("tracker", []any{}, nil), expect: true,
			status: "degraded", reasons: []string{"empty"}},
		{name: "empty where none are expected", file: suppliedDoc("tracker", []any{}, nil), status: "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", `
inputs:
  - name: tracker
    supplied: tracker.json
    max_age: 24h
    expect_records: `+map[bool]string{true: "true", false: "false"}[tc.expect]+`
workstreams: []
`)
			switch v := tc.file.(type) {
			case directory:
				if err := os.Mkdir(filepath.Join(f.dir, "tracker.json"), 0o755); err != nil {
					t.Fatal(err)
				}
			case string:
				f.write("tracker.json", v)
			case map[string]any:
				f.supplied("tracker.json", v)
			}

			h := f.read().health(t, "tracker")

			if h.Status != tc.status {
				t.Errorf("status = %q, want %q (reasons %v)", h.Status, tc.status, h.Reasons)
			}
			want := tc.reasons
			if want == nil {
				want = []string{}
			}
			if !slices.Equal(h.Reasons, want) {
				t.Errorf("reasons = %v, want %v", h.Reasons, want)
			}
			if tc.missing != nil && !slices.Equal(h.MissingFields, tc.missing) {
				t.Errorf("missing_fields = %v, want %v", h.MissingFields, tc.missing)
			}
			if tc.violation != (len(h.SchemaViolations) > 0) {
				t.Errorf("schema_violations = %v, want present: %v", h.SchemaViolations, tc.violation)
			}
			if tc.foreign && (h.FetchedAt != nil || h.RecordCount != 0) {
				t.Errorf("fetched_at = %v, record_count = %d, want nothing taken from a file lookout cannot trust", h.FetchedAt, h.RecordCount)
			}
			switch {
			case tc.reason != "" && (h.SupplierReason == nil || *h.SupplierReason != tc.reason):
				t.Errorf("supplier_reason = %v, want %q", h.SupplierReason, tc.reason)
			case tc.reason == "" && h.SupplierReason != nil:
				t.Errorf("supplier_reason = %q, want null", *h.SupplierReason)
			}
		})
	}
}

// directory marks a TestReadHealth case whose supplied path is a directory.
type directory struct{}

// TestReadIgnoresInputsTheSettingDoesNotExpect pins the hidden state: a
// supplied file the configuration does not name is never read or reported.
func TestReadIgnoresInputsTheSettingDoesNotExpect(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", oneInput)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, nil))
	f.supplied("calendar.json", suppliedDoc("calendar", []any{item("renewal", time.Hour, nil)}, nil))

	doc := f.read()

	if len(doc.Health) != 1 || doc.Health[0].Input != "tracker" {
		t.Errorf("health = %+v, want only the expected input", doc.Health)
	}
	for _, u := range doc.Unplaced {
		if u.Input == "calendar" {
			t.Errorf("unplaced carries %+v from an input the setting does not expect", u)
		}
	}
}

func TestReadMarksWorkstreamsFedByAnUnhealthyInput(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
inputs:
  - name: tracker
    supplied: tracker.json
    max_age: 24h
  - name: calendar
    supplied: calendar.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1", "calendar:renewal"]
  - name: onboarding
    members: ["tracker:ABC-2"]
  - name: taxes
    members: ["calendar:filing"]
`)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", time.Hour, nil), item("ABC-2", time.Hour, nil),
	}, nil))
	// calendar.json is absent, so that input is in error.

	doc := f.read()

	want := map[string][]string{"billing": {"calendar"}, "onboarding": {}, "taxes": {"calendar"}}
	for _, w := range doc.Workstreams {
		if !slices.Equal(w.InputsIncomplete, want[w.Name]) {
			t.Errorf("%s inputs_incomplete = %v, want %v", w.Name, w.InputsIncomplete, want[w.Name])
		}
	}
	if h := doc.health(t, "calendar"); h.Status != "error" {
		t.Errorf("calendar health = %+v, want error", h)
	}
}

// TestReadCarriesATrackerSuppliersDetail drives the tracker seam with an
// invented tracker: its own concepts travel in attrs and links, a ticket with
// none of them stays representable, and an unclaimed ticket is unplaced.
func TestReadCarriesATrackerSuppliersDetail(t *testing.T) {
	f := newFixture(t)
	f.write("work.yml", `
inputs:
  - name: tickets
    supplied: tickets.json
    max_age: 12h
    expect_records: true
workstreams:
  - name: search
    members: ["tickets:XYZ-10"]
`)
	f.supplied("tickets.json", suppliedDoc("tickets", []any{
		item("XYZ-10", 2*time.Hour, map[string]any{
			"title": "Ranking tweak", "state": "In Review",
			"attrs": map[string]any{"sprint": "Sprint 7", "points": 3},
			"links": []any{map[string]any{"rel": "pull_request", "href": "https://example.com/owner/repo/pull/12"}},
		}),
		item("XYZ-11", 5*time.Hour, map[string]any{"title": "No sprint, no link"}),
	}, map[string]any{"supplier_status": "degraded", "missing_fields": []any{"estimate"}, "reason": "estimates hidden"}))

	doc := f.read()

	h := doc.health(t, "tickets")
	if h.Status != "degraded" || !slices.Equal(h.MissingFields, []string{"estimate"}) {
		t.Errorf("health = %+v, want degraded naming the missing field", h)
	}
	if len(doc.Workstreams[0].Events) != 1 || doc.Workstreams[0].Events[0].ID != "XYZ-10" {
		t.Errorf("search events = %+v", doc.Workstreams[0].Events)
	}
	if !slices.Equal(doc.Workstreams[0].InputsIncomplete, []string{"tickets"}) {
		t.Errorf("search inputs_incomplete = %v, want the degraded input named", doc.Workstreams[0].InputsIncomplete)
	}
	if len(doc.Unplaced) != 1 {
		t.Fatalf("unplaced = %+v, want the ticket no workstream claims", doc.Unplaced)
	}
	u := doc.Unplaced[0].Item
	if u["id"] != "XYZ-11" || u["state"] != "open" {
		t.Errorf("unplaced item = %v", u)
	}
	if attrs, ok := u["attrs"].(map[string]any); !ok || len(attrs) != 0 {
		t.Errorf("unplaced attrs = %v, want an empty object for a ticket with no sprint", u["attrs"])
	}
}

func TestReadCarriesDatedObligations(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
inputs:
  - name: obligations
    supplied: obligations.json
    max_age: 168h
workstreams:
  - name: household
    members: ["obligations:passport"]
`)
	f.supplied("obligations.json", suppliedDoc("obligations", []any{
		item("passport", 48*time.Hour, map[string]any{"title": "Renew passport", "due": "2030-07-01", "state": "pending"}),
		item("cert", 24*time.Hour, map[string]any{"title": "Rotate certificate", "due": "2030-06-20T09:00:00Z", "state": "pending"}),
	}, nil))

	doc := f.read()

	if h := doc.health(t, "obligations"); h.Status != "ok" {
		t.Errorf("health = %+v, want both due forms accepted", h)
	}
	if e := doc.Workstreams[0].Events; len(e) != 1 || e[0].Title != "Renew passport" {
		t.Errorf("household events = %+v", e)
	}
	if len(doc.Unplaced) != 1 || doc.Unplaced[0].Item["due"] != "2030-06-20T09:00:00Z" {
		t.Errorf("unplaced = %+v, want the certificate with its due time", doc.Unplaced)
	}
}

func TestReadSelectsOneSetting(t *testing.T) {
	minimal := "inputs: []\nworkstreams: []\n"

	t.Run("the only configuration is used", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", minimal)
		if doc := f.read(); doc.Setting != "personal" {
			t.Errorf("setting = %q", doc.Setting)
		}
	})

	t.Run("several with none selected are refused, never merged", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", minimal)
		f.write("work.yml", minimal)
		code, stdout, stderr := f.run("read", "--json")
		if code == 0 {
			t.Errorf("exit code = 0, want a refusal")
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want nothing", stdout)
		}
		for _, want := range []string{"personal", "work", "--setting"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, want)
			}
		}
	})

	t.Run("--setting chooses among several", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", minimal)
		f.write("work.yml", minimal)
		if doc := f.read("--setting", "work"); doc.Setting != "work" {
			t.Errorf("setting = %q", doc.Setting)
		}
	})

	t.Run("LOOKOUT_CONFIG names one file", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", minimal)
		f.write("work.yml", minimal)
		f.env["LOOKOUT_CONFIG"] = f.write("elsewhere.yml", minimal)
		if doc := f.read(); doc.Setting != "elsewhere" {
			t.Errorf("setting = %q", doc.Setting)
		}
	})

	t.Run("an unknown setting is named", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", minimal)
		code, _, stderr := f.run("read", "--json", "--setting", "absent")
		if code == 0 || !strings.Contains(stderr, `"absent"`) {
			t.Errorf("exit code = %d, stderr = %q, want a refusal naming the setting", code, stderr)
		}
	})

	t.Run("no configuration at all", func(t *testing.T) {
		f := newFixture(t)
		code, _, stderr := f.run("read", "--json")
		if code == 0 || !strings.Contains(stderr, f.dir) {
			t.Errorf("exit code = %d, stderr = %q, want a refusal naming where it looked", code, stderr)
		}
	})
}

func TestReadRefusesAnInvalidConfiguration(t *testing.T) {
	cases := map[string]string{
		"member names an unknown input":    "inputs: []\nworkstreams:\n  - name: a\n    members: [\"tracker:ABC-1\"]\n",
		"member has no input prefix":       "inputs:\n  - {name: tracker, supplied: t.json, max_age: 1h}\nworkstreams:\n  - name: a\n    members: [\"ABC-1\"]\n",
		"duplicate input":                  "inputs:\n  - {name: tracker, supplied: t.json, max_age: 1h}\n  - {name: tracker, supplied: u.json, max_age: 1h}\nworkstreams: []\n",
		"duplicate workstream":             "inputs: []\nworkstreams:\n  - {name: a, members: []}\n  - {name: a, members: []}\n",
		"input without max_age":            "inputs:\n  - {name: tracker, supplied: t.json}\nworkstreams: []\n",
		"unknown key":                      "inputs: []\nworkstreams: []\nworkstream: []\n",
		"input name with a colon":          "inputs:\n  - {name: \"a:b\", supplied: t.json, max_age: 1h}\nworkstreams: []\n",
		"member listed twice":              "inputs:\n  - {name: tracker, supplied: t.json, max_age: 1h}\nworkstreams:\n  - name: a\n    members: [\"tracker:ABC-1\", \"tracker:ABC-1\"]\n",
		"event window finer than a second": "event_window: 1500ms\ninputs: []\nworkstreams: []\n",
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			path := f.write("personal.yml", config)
			code, stdout, stderr := f.run("read", "--json")
			if code != exitFailure {
				t.Errorf("exit code = %d, want %d", code, exitFailure)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, path) {
				t.Errorf("stderr = %q, want it to name the configuration file", stderr)
			}
		})
	}
}

func TestReadRefusesAnEmptySetting(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", "inputs: []\nworkstreams: []\n")
	code, stdout, stderr := f.run("read", "--json", "--setting=")
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, "--setting") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a usage error: an empty --setting selects nothing", code, stdout, stderr)
	}
}

func TestReadRequiresJSONOutput(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", "inputs: []\nworkstreams: []\n")
	code, stdout, stderr := f.run("read")
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, "--json") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a usage error naming --json", code, stdout, stderr)
	}
}

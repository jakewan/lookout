package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// handWritten is a configuration a person maintains: comments, blank lines and
// a trailing comment on one member, all of which an edit must leave alone.
const handWritten = `# Personal setting.
event_window: 336h

inputs:
  - name: tracker
    supplied: tracker.json
    max_age: 24h

workstreams:
  # Billing work for the spring release.
  - name: billing
    members:
      - tracker:ABC-1   # the export
      - tracker:ABC-2

  # Onboarding, paused.

  - name: onboarding
    members:
      - tracker:ABC-9
`

// gestureDoc is one line of the gesture log.
type gestureDoc struct {
	At          string   `json:"at"`
	Gesture     string   `json:"gesture"`
	Outcome     string   `json:"outcome"`
	Reason      string   `json:"reason"`
	Item        string   `json:"item"`
	Workstream  string   `json:"workstream"`
	Workstreams []string `json:"workstreams"`
	New         bool     `json:"new"`
	From        []string `json:"from"`
}

func (f *fixture) stateDir(setting string) string {
	return filepath.Join(f.env["HOME"], ".local", "state", "lookout", setting)
}

// gestures reads the setting's gesture log; a log that does not exist reads as
// no gestures.
func (f *fixture) gestures(setting string) []gestureDoc {
	f.t.Helper()
	file, err := os.Open(filepath.Join(f.stateDir(setting), "gestures.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			f.t.Error(cerr)
		}
	}()
	var out []gestureDoc
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var g gestureDoc
		if err := json.Unmarshal(scanner.Bytes(), &g); err != nil {
			f.t.Fatalf("gesture log line %q: %v", scanner.Text(), err)
		}
		out = append(out, g)
	}
	if err := scanner.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) config(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

// mustRun runs a command and requires it to succeed.
func (f *fixture) mustRun(args ...string) string {
	f.t.Helper()
	code, stdout, stderr := f.run(args...)
	if code != 0 {
		f.t.Fatalf("%v: exit code = %d, stderr = %q", args, code, stderr)
	}
	return stdout
}

func trackerItems() map[string]any {
	return suppliedDoc("tracker", []any{
		item("ABC-1", 3*time.Hour, nil),
		item("ABC-2", 30*time.Hour, nil),
		item("ABC-3", time.Hour, map[string]any{"title": "Not in any workstream"}),
		item("ABC-9", 48*time.Hour, nil),
	}, nil)
}

func unplacedIDs(doc readoutDoc) []string {
	ids := []string{}
	for _, u := range doc.Unplaced {
		ids = append(ids, u.Input+":"+u.Item["id"].(string))
	}
	return ids
}

func workstreamNames(doc readoutDoc) []string {
	names := []string{}
	for _, w := range doc.Workstreams {
		names = append(names, w.Name)
	}
	return names
}

func TestAdoptMovesAnItemIntoAWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("adopt", "tracker:ABC-3", "billing")

	want := strings.Replace(handWritten, "      - tracker:ABC-2\n", "      - tracker:ABC-2\n      - tracker:ABC-3\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after adopt:\n%s\nwant every other line unchanged:\n%s", got, want)
	}
	doc := f.read()
	if ids := unplacedIDs(doc); len(ids) != 0 {
		t.Errorf("unplaced = %v, want the adopted item gone", ids)
	}
	billing := doc.Workstreams[0]
	if len(billing.Items) != 3 || billing.Items[2].Item["id"] != "ABC-3" {
		t.Errorf("billing items = %+v, want ABC-3 last", billing.Items)
	}

	gs := f.gestures("personal")
	if len(gs) != 1 {
		t.Fatalf("gestures = %+v, want exactly one", gs)
	}
	g := gs[0]
	if g.At != now.Format(time.RFC3339) || g.Gesture != "adopt" || g.Outcome != "ok" || g.Item != "tracker:ABC-3" || g.Workstream != "billing" || g.New {
		t.Errorf("gesture = %+v", g)
	}
}

func TestAdoptFlagsMayFollowTheArguments(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.mustRun("adopt", "tracker:ABC-3", "support", "--new", "--setting", "personal")
	if doc := f.read(); !slices.Equal(workstreamNames(doc), []string{"billing", "onboarding", "support"}) {
		t.Errorf("workstreams = %v", workstreamNames(doc))
	}
}

func TestAdoptNewAppendsAWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("adopt", "--new", "tracker:ABC-3", "support")

	want := handWritten + "  - name: support\n    members:\n      - tracker:ABC-3\n"
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after adopt --new:\n%s\nwant:\n%s", got, want)
	}
	doc := f.read()
	if !slices.Equal(workstreamNames(doc), []string{"billing", "onboarding", "support"}) {
		t.Errorf("workstreams = %v, want the new one last", workstreamNames(doc))
	}
	if gs := f.gestures("personal"); len(gs) != 1 || !gs[0].New {
		t.Errorf("gestures = %+v, want one marked new", gs)
	}
}

func TestAdoptFillsAnEmptyOrMissingMemberList(t *testing.T) {
	head := "inputs:\n  - name: tracker\n    supplied: tracker.json\n    max_age: 24h\n"
	cases := map[string]struct {
		before, after string
		create        bool
	}{
		"empty flow list": {
			before: head + "workstreams:\n  - name: billing\n    members: []  # none yet\n",
			after:  head + "workstreams:\n  - name: billing\n    members:  # none yet\n      - tracker:ABC-3\n",
		},
		"no members key": {
			before: head + "workstreams:\n  - name: billing\n",
			after:  head + "workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-3\n",
		},
		"last line has no newline": {
			before: head + "workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1",
			after:  head + "workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n      - tracker:ABC-3\n",
		},
		"no workstreams yet": {
			before: head + "workstreams: []\n",
			after:  head + "workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-3\n",
			create: true,
		},
		"no workstreams key": {
			before: head,
			after:  head + "workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-3\n",
			create: true,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", c.before)
			args := []string{"adopt", "tracker:ABC-3", "billing"}
			if c.create {
				args = append(args, "--new")
			}
			f.mustRun(args...)
			if got := f.config("personal.yml"); got != c.after {
				t.Errorf("configuration:\n%q\nwant:\n%q", got, c.after)
			}
		})
	}
}

func TestAdoptQuotesAnIDThatNeedsIt(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("odd: #1", time.Hour, nil)}, nil))

	f.mustRun("adopt", "tracker:odd: #1", "billing")

	doc := f.read()
	if ids := unplacedIDs(doc); len(ids) != 0 {
		t.Errorf("unplaced = %v, want the item claimed under its exact id", ids)
	}
}

func TestAdoptRefusals(t *testing.T) {
	cases := map[string]struct {
		args    []string
		mention string
	}{
		"unknown workstream":           {[]string{"tracker:ABC-3", "support"}, "--new"},
		"input not configured":         {[]string{"calendar:X-1", "billing"}, "calendar"},
		"already a member":             {[]string{"tracker:ABC-1", "billing"}, "billing"},
		"member of another workstream": {[]string{"tracker:ABC-9", "billing"}, "onboarding"},
		"new but already exists":       {[]string{"--new", "tracker:ABC-3", "billing"}, "billing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			code, stdout, stderr := f.run(append([]string{"adopt"}, c.args...)...)
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, c.mention) {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a refusal mentioning %q", code, stdout, stderr, c.mention)
			}
			if got := f.config("personal.yml"); got != handWritten {
				t.Errorf("configuration changed by a refused adopt:\n%s", got)
			}
			gs := f.gestures("personal")
			if len(gs) != 1 || gs[0].Outcome != "refused" || gs[0].Reason == "" || gs[0].Gesture != "adopt" {
				t.Errorf("gestures = %+v, want one refused adopt with its reason", gs)
			}
		})
	}
}

func TestEditsRefuseFileShapesTheyCannotKeepIntact(t *testing.T) {
	head := "inputs:\n  - name: tracker\n    supplied: tracker.json\n    max_age: 24h\n"
	cases := map[string]struct{ config, mention string }{
		"alias":           {head + "workstreams:\n  - name: billing\n    members: &m\n      - tracker:ABC-1\n  - name: other\n    members: *m\n", "anchor"},
		"second document": {head + "workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n---\nworkstreams: []\n", "more than one YAML document"},
		"flow list":       {head + "workstreams:\n  - name: billing\n    members: [\"tracker:ABC-1\"]\n", "flow list"},
		"lone CR":         {strings.ReplaceAll(head+"workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n", "\n", "\r"), "carriage return"},
		"tag":             {head + "workstreams:\n  - name: !!str billing\n    members:\n      - tracker:ABC-1\n", "tag"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", c.config)
			code, _, stderr := f.run("adopt", "tracker:ABC-3", "billing")
			if code != exitFailure || !strings.Contains(stderr, c.mention) {
				t.Errorf("exit code = %d, stderr = %q, want a refusal mentioning %q", code, stderr, c.mention)
			}
			if got := f.config("personal.yml"); got != c.config {
				t.Errorf("configuration changed:\n%q", got)
			}
		})
	}
}

func TestOrderMovesWorkstreamsToTheFront(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)

	f.mustRun("order", "onboarding")

	want := `# Personal setting.
event_window: 336h

inputs:
  - name: tracker
    supplied: tracker.json
    max_age: 24h

workstreams:

  # Onboarding, paused.

  - name: onboarding
    members:
      - tracker:ABC-9
  # Billing work for the spring release.
  - name: billing
    members:
      - tracker:ABC-1   # the export
      - tracker:ABC-2
`
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after order:\n%s\nwant each workstream to carry its comments:\n%s", got, want)
	}
	if doc := f.read(); !slices.Equal(workstreamNames(doc), []string{"onboarding", "billing"}) {
		t.Errorf("workstreams = %v", workstreamNames(doc))
	}
	gs := f.gestures("personal")
	if len(gs) != 1 || gs[0].Gesture != "order" || !slices.Equal(gs[0].Workstreams, []string{"onboarding"}) {
		t.Errorf("gestures = %+v", gs)
	}
}

func TestOrderKeepsTheRestInTheirOrder(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", "inputs: []\nworkstreams:\n  - name: a\n  - name: b\n  - name: c\n  - name: d\n")
	f.mustRun("order", "c", "a")
	if doc := f.read(); !slices.Equal(workstreamNames(doc), []string{"c", "a", "b", "d"}) {
		t.Errorf("workstreams = %v", workstreamNames(doc))
	}
}

func TestOrderRefusals(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown":  {"order", "billing", "absent"},
		"repeated": {"order", "billing", "billing"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			if code, _, _ := f.run(args...); code != exitFailure {
				t.Errorf("exit code = %d, want a refusal", code)
			}
			if got := f.config("personal.yml"); got != handWritten {
				t.Errorf("configuration changed:\n%s", got)
			}
			if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Outcome != "refused" {
				t.Errorf("gestures = %+v", gs)
			}
		})
	}
}

func TestCompleteSetsAWorkstreamAside(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("complete", "billing")

	want := strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-15\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after complete:\n%s\nwant:\n%s", got, want)
	}
	doc := f.read()
	if !slices.Equal(workstreamNames(doc), []string{"onboarding"}) {
		t.Errorf("workstreams = %v, want the completed one left out", workstreamNames(doc))
	}
	if doc.CompletedCount != 1 {
		t.Errorf("completed_count = %d", doc.CompletedCount)
	}
	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"tracker:ABC-3"}) {
		t.Errorf("unplaced = %v, want the completed workstream's members still claimed", ids)
	}

	if code, _, _ := f.run("complete", "billing"); code != exitFailure {
		t.Errorf("completing it again: exit code = %d, want a refusal", code)
	}
}

func TestACompletedWorkstreamsItemResurfacesWhenItMoves(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-10\n", 1))
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", time.Hour, nil),      // touched after the completion date
		item("ABC-2", 6*24*time.Hour, nil), // touched on it, so still finished
		item("ABC-9", 48*time.Hour, nil),   // in a live workstream
		item("ABC-3", 7*24*time.Hour, nil), // in no workstream
	}, nil))

	doc := f.read()

	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"tracker:ABC-1", "tracker:ABC-3"}) {
		t.Errorf("unplaced = %v, want ABC-1 back beside ABC-3", ids)
	}
}

func TestDismissHidesAnItemUntilItMovesAgain(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("dismiss", "tracker:ABC-3")

	doc := f.read()
	if ids := unplacedIDs(doc); len(ids) != 0 {
		t.Errorf("unplaced = %v, want the dismissed item hidden", ids)
	}
	if doc.DismissedCount != 1 {
		t.Errorf("dismissed_count = %d", doc.DismissedCount)
	}
	if got := f.config("personal.yml"); got != handWritten {
		t.Errorf("dismiss changed the configuration:\n%s", got)
	}

	f.clock = now.Add(2 * time.Hour)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-3", -time.Hour, nil),
	}, map[string]any{"fetched_at": stamp(-time.Hour), "attempted_at": stamp(-time.Hour)}))

	doc = f.read()
	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"tracker:ABC-3"}) {
		t.Errorf("unplaced = %v, want ABC-3 back after a later touch", ids)
	}
	if doc.DismissedCount != 0 {
		t.Errorf("dismissed_count = %d, want an expired dismissal not counted", doc.DismissedCount)
	}
}

func TestDismissRefusals(t *testing.T) {
	for name, target := range map[string]string{
		"input not configured": "calendar:X-1",
		"already a member":     "tracker:ABC-1",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			if code, _, _ := f.run("dismiss", target); code != exitFailure {
				t.Errorf("exit code = %d, want a refusal", code)
			}
			if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Outcome != "refused" {
				t.Errorf("gestures = %+v", gs)
			}
		})
	}
}

func TestUnplacedListsTokensAdoptAccepts(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	out := f.mustRun("unplaced")

	want := "tracker:ABC-3\tNot in any workstream\t" + stamp(time.Hour) + "\n"
	if out != want {
		t.Errorf("unplaced output = %q, want %q", out, want)
	}
	if gs := f.gestures("personal"); len(gs) != 0 {
		t.Errorf("gestures = %+v, want a listing to record nothing", gs)
	}
	token, _, _ := strings.Cut(out, "\t")
	f.mustRun("adopt", token, "billing")
}

func TestUsageErrorsRecordNothing(t *testing.T) {
	for name, args := range map[string][]string{
		"adopt without a workstream": {"adopt", "tracker:ABC-3"},
		"adopt with a bare id":       {"adopt", "ABC-3", "billing"},
		"order with no names":        {"order"},
		"complete with two names":    {"complete", "a", "b"},
		"dismiss with no item":       {"dismiss"},
		"unknown flag":               {"dismiss", "--force", "tracker:ABC-3"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			code, _, stderr := f.run(args...)
			if code != exitUsage || !strings.Contains(stderr, "usage:") {
				t.Errorf("exit code = %d, stderr = %q, want a usage error", code, stderr)
			}
			if _, err := os.Stat(f.stateDir("personal")); !os.IsNotExist(err) {
				t.Errorf("state directory exists after a usage error: %v", err)
			}
		})
	}
}

func TestUsageListsEveryCommand(t *testing.T) {
	f := newFixture(t)
	code, _, stderr := f.run()
	if code != exitUsage {
		t.Errorf("exit code = %d", code)
	}
	for _, cmd := range []string{"read", "unplaced", "adopt", "dismiss", "order", "complete"} {
		if !strings.Contains(stderr, "lookout "+cmd) {
			t.Errorf("usage = %q, want it to list %q", stderr, cmd)
		}
	}
}

func TestStateIsPrivateAndBelongsToOneConfiguration(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.mustRun("dismiss", "tracker:ABC-3")

	info, err := os.Stat(f.stateDir("personal"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("state directory mode = %v, want 0700", info.Mode().Perm())
	}

	other := filepath.Join(t.TempDir(), "personal.yml")
	if err := os.WriteFile(other, []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}
	f.env["LOOKOUT_CONFIG"] = other
	code, _, stderr := f.run("read", "--json")
	// State names files by their resolved paths, and a temporary directory may
	// sit behind a symlink.
	resolve := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if code != exitFailure || !strings.Contains(stderr, resolve(other)) || !strings.Contains(stderr, resolve(filepath.Join(f.dir, "personal.yml"))) {
		t.Errorf("exit code = %d, stderr = %q, want a refusal naming both configuration files", code, stderr)
	}
}

func TestASettingNameMustBeValidWhateverTheRoute(t *testing.T) {
	for name, file := range map[string]string{
		"empty":     ".yml",
		"uppercase": "Personal.yml",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.env["LOOKOUT_CONFIG"] = f.write(file, "inputs: []\nworkstreams: []\n")
			code, stdout, _ := f.run("read", "--json")
			if code != exitFailure || stdout != "" {
				t.Errorf("exit code = %d, stdout = %q, want a refusal", code, stdout)
			}
		})
	}
}

func TestACorruptGestureLogFailsLoudly(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	f.mustRun("dismiss", "tracker:ABC-3")

	log := filepath.Join(f.stateDir("personal"), "gestures.jsonl")
	file, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"at": "2030-06-15T`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := f.run("read", "--json")
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "line 2") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a failure naming the bad line", code, stdout, stderr)
	}
}

func TestAResurfacedItemCanBeDismissed(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-10\n", 1))
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, nil))

	f.mustRun("dismiss", "tracker:ABC-1")

	if ids := unplacedIDs(f.read()); len(ids) != 0 {
		t.Errorf("unplaced = %v, want the resurfaced item dismissed", ids)
	}
}

func TestAdoptRefusesACompletedWorkstream(t *testing.T) {
	f := newFixture(t)
	completed := strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-10\n", 1)
	f.write("personal.yml", completed)
	code, _, stderr := f.run("adopt", "tracker:ABC-3", "billing")
	if code != exitFailure || !strings.Contains(stderr, "complete") {
		t.Errorf("exit code = %d, stderr = %q, want a refusal saying the workstream is complete", code, stderr)
	}
	if got := f.config("personal.yml"); got != completed {
		t.Errorf("configuration changed:\n%s", got)
	}
}

func TestCompleteRefusesAnEmptyCompletedKey(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed:\n", 1))
	code, _, _ := f.run("complete", "billing")
	if gs := f.gestures("personal"); code != exitFailure || len(gs) != 1 || gs[0].Outcome != "refused" {
		t.Errorf("exit code = %d, gestures = %+v, want one refused complete", code, gs)
	}
}

func TestAGestureAgainstAMissingConfigurationRecordsNothing(t *testing.T) {
	for name, target := range map[string]string{
		"no such file": "personal.yaml",
		"a directory":  "personal",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.Mkdir(filepath.Join(f.dir, "personal"), 0o755); err != nil {
				t.Fatal(err)
			}
			f.env["LOOKOUT_CONFIG"] = filepath.Join(f.dir, target)
			if code, _, _ := f.run("adopt", "tracker:ABC-3", "billing"); code != exitFailure {
				t.Errorf("exit code = %d, want a failure", code)
			}
			if _, err := os.Stat(f.stateDir("personal")); !os.IsNotExist(err) {
				t.Errorf("state directory exists, claimed by something that is not a configuration file: %v", err)
			}
		})
	}
}

func TestAnInvalidConfigurationIsAFailedGesture(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten+"surprise: true\n")
	if code, _, _ := f.run("adopt", "tracker:ABC-3", "billing"); code != exitFailure {
		t.Errorf("exit code = %d", code)
	}
	if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Outcome != "failed" || gs[0].Reason == "" {
		t.Errorf("gestures = %+v, want one failed gesture with its reason", gs)
	}
}

// unwritableLog leaves a readable gesture log that refuses appends, so a
// gesture can run but not be recorded.
func (f *fixture) unwritableLog() {
	f.t.Helper()
	f.mustRun("dismiss", "tracker:ABC-7")
	if err := os.Chmod(filepath.Join(f.stateDir("personal"), "gestures.jsonl"), 0o400); err != nil {
		f.t.Fatal(err)
	}
}

func TestADismissThatCannotBeRecordedSaysNothingWasDismissed(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.unwritableLog()
	code, _, stderr := f.run("dismiss", "tracker:ABC-3")
	if code != exitFailure || !strings.Contains(stderr, "nothing was dismissed") {
		t.Errorf("exit code = %d, stderr = %q, want it to say nothing was dismissed", code, stderr)
	}
}

func TestAnEditThatCannotBeRecordedSaysItLanded(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.unwritableLog()
	code, _, stderr := f.run("adopt", "tracker:ABC-3", "billing")
	if code != exitFailure || !strings.Contains(stderr, "not recorded") {
		t.Errorf("exit code = %d, stderr = %q, want it to say the edit landed unrecorded", code, stderr)
	}
	if !strings.Contains(f.config("personal.yml"), "tracker:ABC-3") {
		t.Errorf("the edit did not land")
	}
}

func TestOrderLeavesATrailingCommentWithItsWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", "inputs: []\nworkstreams:\n  - name: a\n    members: []\n    # - tracker:OLD-7\n  - name: b\n")
	f.mustRun("order", "b")
	want := "inputs: []\nworkstreams:\n  - name: b\n  - name: a\n    members: []\n    # - tracker:OLD-7\n"
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration:\n%q\nwant the commented-out member kept under a:\n%q", got, want)
	}
}

func TestStateFollowsTheFileThroughASymlinkedDirectory(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.mustRun("dismiss", "tracker:ABC-3")

	link := filepath.Join(t.TempDir(), "cfg")
	if err := os.Symlink(f.dir, link); err != nil {
		t.Fatal(err)
	}
	f.env["LOOKOUT_CONFIG"] = filepath.Join(link, "personal.yml")
	if code, _, stderr := f.run("read", "--json"); code != 0 {
		t.Errorf("exit code = %d, stderr = %q, want the same file recognized", code, stderr)
	}
}

func TestUnplacedKeepsAnAwkwardIDOnOneLine(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("two\tpart", time.Hour, nil)}, nil))
	out := f.mustRun("unplaced")
	if strings.Count(out, "\n") != 1 || strings.Count(out, "\t") != 2 {
		t.Errorf("unplaced output = %q, want one line of three fields", out)
	}
}

func TestADoubleDashEndsTheFlags(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", "inputs: []\nworkstreams:\n  - name: a\n  - name: -b\n  - name: -c\n")
	f.mustRun("order", "--", "-c", "-b")
	if doc := f.read(); !slices.Equal(workstreamNames(doc), []string{"-c", "-b", "a"}) {
		t.Errorf("workstreams = %v", workstreamNames(doc))
	}
}

func TestAdoptMovesAResurfacedItemOutOfItsCompletedWorkstream(t *testing.T) {
	completed := strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-10\n", 1)
	f := newFixture(t)
	f.write("personal.yml", completed)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, nil))

	f.mustRun("adopt", "tracker:ABC-1", "onboarding")

	want := strings.Replace(completed, "      - tracker:ABC-1   # the export\n", "", 1)
	want = strings.Replace(want, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-1\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration:\n%s\nwant the member moved:\n%s", got, want)
	}
	doc := f.read()
	if ids := unplacedIDs(doc); len(ids) != 0 {
		t.Errorf("unplaced = %v, want the moved item placed", ids)
	}
	gs := f.gestures("personal")
	if len(gs) != 1 || gs[0].Outcome != "ok" || !slices.Equal(gs[0].From, []string{"billing"}) {
		t.Errorf("gestures = %+v, want one adopt recording where the item came from", gs)
	}
}

func TestAdoptMovesACompletedWorkstreamsOnlyMember(t *testing.T) {
	head := "inputs:\n  - name: tracker\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n"
	f := newFixture(t)
	f.write("personal.yml", head+"  - name: old\n    completed: 2030-06-10\n    members:\n      - tracker:ABC-1\n  - name: new\n")

	f.mustRun("adopt", "tracker:ABC-1", "new")

	want := head + "  - name: old\n    completed: 2030-06-10\n    members:\n  - name: new\n    members:\n      - tracker:ABC-1\n"
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration:\n%q\nwant:\n%q", got, want)
	}
}

func TestAdoptStillRefusesAnItemALiveWorkstreamHolds(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	if code, _, stderr := f.run("adopt", "tracker:ABC-1", "onboarding"); code != exitFailure || !strings.Contains(stderr, "billing") {
		t.Errorf("exit code = %d, stderr = %q, want a refusal naming billing", code, stderr)
	}
}

func TestASettingThatIsADirectoryRecordsNothing(t *testing.T) {
	f := newFixture(t)
	if err := os.Mkdir(filepath.Join(f.dir, "personal.yml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.run("adopt", "--setting", "personal", "tracker:ABC-3", "billing"); code != exitFailure {
		t.Errorf("exit code = %d, want a failure", code)
	}
	if _, err := os.Stat(f.stateDir("personal")); !os.IsNotExist(err) {
		t.Errorf("state directory exists, claimed by a directory: %v", err)
	}
}

func TestATouchAtTheStartOfTheNextDayResurfaces(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-10\n", 1))
	touched := item("ABC-1", 0, nil)
	touched["last_touched"] = "2030-06-11T00:00:00Z"
	f.supplied("tracker.json", suppliedDoc("tracker", []any{touched}, nil))
	if ids := unplacedIDs(f.read()); !slices.Equal(ids, []string{"tracker:ABC-1"}) {
		t.Errorf("unplaced = %v, want a touch at the next day's start counted as after the completed date", ids)
	}
}

func TestAValueSpanningLinesIsRefused(t *testing.T) {
	head := "inputs:\n  - name: tracker\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n"
	for name, c := range map[string]struct {
		config string
		args   []string
	}{
		"quoted name":        {head + "  - name: \"bill\\\n      ing\"\n    members:\n      - tracker:ABC-1\n", []string{"complete", "billing"}},
		"plain name":         {head + "  - name: bill\n      ing\n    members:\n      - tracker:ABC-1\n", []string{"complete", "bill ing"}},
		"quoted last member": {head + "  - name: billing\n    members:\n      - \"tracker:ABC\\\n        -1\"\n", []string{"adopt", "tracker:ABC-3", "billing"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", c.config)
			code, _, stderr := f.run(c.args...)
			if code != exitFailure || !strings.Contains(stderr, "one line") {
				t.Errorf("exit code = %d, stderr = %q, want a refusal asking for one line", code, stderr)
			}
			if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Outcome != "refused" {
				t.Errorf("gestures = %+v, want one refused gesture", gs)
			}
			if got := f.config("personal.yml"); got != c.config {
				t.Errorf("configuration changed:\n%q", got)
			}
		})
	}
}

func TestAQuotedTokenFromTheListingSelectsTheOriginalID(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("two\tpart", time.Hour, nil)}, nil))

	token, _, _ := strings.Cut(f.mustRun("unplaced"), "\t")
	if token != `"tracker:two\tpart"` {
		t.Fatalf("token = %q, want it quoted", token)
	}
	f.mustRun("dismiss", token)

	if ids := unplacedIDs(f.read()); len(ids) != 0 {
		t.Errorf("unplaced = %v, want the original id dismissed", ids)
	}
	if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Item != "tracker:two\tpart" {
		t.Errorf("gestures = %+v, want the decoded item recorded", gs)
	}
}

func TestAMalformedQuotedTokenIsAUsageError(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	if code, _, stderr := f.run("dismiss", `"tracker:two\qpart"`); code != exitUsage || !strings.Contains(stderr, "quoted") {
		t.Errorf("exit code = %d, stderr = %q, want a usage error about the quoting", code, stderr)
	}
}

func TestAOneLineNameThatLooksLikeADocumentMarkerIsEditable(t *testing.T) {
	head := "inputs:\n  - name: tracker\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n"
	for _, name := range []string{"---", "...", "--- parked"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", head+"  - name: "+name+"\n    members:\n      - tracker:ABC-1\n")
			// Such a name reads as a flag unless -- ends the flags.
			f.mustRun("adopt", "--", "tracker:ABC-3", name)
		})
	}
}

// TestEditsAcceptAwkwardOneLineValues tests what the layout guard admits
// rather than assuming it: each value is written on one line, so every edit
// beside it must go through, and the edit's own check confirms the splice.
func TestEditsAcceptAwkwardOneLineValues(t *testing.T) {
	head := "inputs:\n  - name: tracker\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n"
	names := map[string]string{ // as written in YAML → the value it holds
		`---`:          "---",
		`...`:          "...",
		`--- parked`:   "--- parked",
		`'a: b'`:       "a: b",
		`"x # y"`:      "x # y",
		`billing # q3`: "billing",
		`"&a"`:         "&a",
		`'*b'`:         "*b",
		`"[x]"`:        "[x]",
		`a:b`:          "a:b",
		`café`:         "café",
		`'it''s'`:      "it's",
		`"tab\there"`:  "tab\there",
	}
	for written, value := range names {
		t.Run("name "+written, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", head+"  - name: "+written+"\n    members:\n      - tracker:ABC-1\n")
			f.mustRun("adopt", "--", "tracker:ABC-3", value)
			f.mustRun("complete", "--", value)
		})
	}
	members := map[string]string{
		`'tracker:x: y'`: "tracker:x: y",
		`"tracker:#1"`:   "tracker:#1",
		`tracker:a#b`:    "tracker:a#b",
		`tracker:a # c`:  "tracker:a",
		`"tracker:---"`:  "tracker:---",
		`tracker:é`:      "tracker:é",
	}
	for written, token := range members {
		t.Run("after member "+written, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", head+"  - name: billing\n    members:\n      - "+written+"\n")
			f.mustRun("adopt", "tracker:ABC-3", "billing")
		})
		t.Run("moving member "+written, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", head+"  - name: old\n    completed: 2030-06-01\n    members:\n      - "+written+"\n  - name: billing\n")
			f.mustRun("adopt", token, "billing")
		})
	}
}

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

// baselineDoc is one line of the baseline.
type baselineDoc struct {
	Date              string   `json:"date"`
	PreviousDate      string   `json:"previous_date"`
	At                string   `json:"at"`
	Backlog           []string `json:"backlog"`
	ActiveWorkstreams int      `json:"active_workstreams"`
	Staleness         struct {
		MedianSeconds     *int64 `json:"median_seconds"`
		P85Seconds        *int64 `json:"p85_seconds"`
		Workstreams       int    `json:"workstreams"`
		UnreadWorkstreams int    `json:"unread_workstreams"`
	} `json:"staleness"`
	CompletedSincePrevious int      `json:"completed_since_previous"`
	Unplaced               []string `json:"unplaced"`
	Arrivals               []string `json:"arrivals"`
	HandPlaced             []string `json:"hand_placed"`
	UnloggedEdit           bool     `json:"unlogged_edit"`
	DiscoveryChanged       bool     `json:"discovery_changed"`
	Workstreams            []struct {
		Name      string   `json:"name"`
		Members   []string `json:"members"`
		Completed string   `json:"completed"`
	} `json:"workstreams"`
	Health []struct {
		Input  string `json:"input"`
		Status string `json:"status"`
	} `json:"health"`
	Incomplete bool     `json:"incomplete"`
	Joined     []string `json:"joined"`
}

func (f *fixture) baselinePath(setting string) string {
	return filepath.Join(f.stateDir(setting), "baseline.jsonl")
}

// baseline reads the setting's baseline; one that does not exist reads as no
// records.
func (f *fixture) baseline(setting string) []baselineDoc {
	f.t.Helper()
	file, err := os.Open(f.baselinePath(setting))
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
	var out []baselineDoc
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var b baselineDoc
		if err := json.Unmarshal(scanner.Bytes(), &b); err != nil {
			f.t.Fatalf("baseline line %q: %v", scanner.Text(), err)
		}
		out = append(out, b)
	}
	if err := scanner.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// snapshot runs lookout snapshot, requires it to succeed with nothing on
// stdout, and returns the newest baseline record.
func (f *fixture) snapshot() baselineDoc {
	f.t.Helper()
	code, stdout, stderr := f.run("snapshot")
	if code != 0 || stdout != "" {
		f.t.Fatalf("snapshot: exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	records := f.baseline("personal")
	if len(records) == 0 {
		f.t.Fatal("snapshot wrote no record")
	}
	return records[len(records)-1]
}

// tracker writes the tracker's supplied file as fetched just now on the
// fixture's clock, with each item touched at the given instant.
func (f *fixture) tracker(touched map[string]time.Time) {
	f.t.Helper()
	items := make([]any, 0, len(touched))
	for id, at := range touched {
		items = append(items, map[string]any{
			"id":           id,
			"last_touched": at.Format(time.RFC3339),
			"state":        "open",
			"links":        []any{},
			"attrs":        map[string]any{},
		})
	}
	fetched := f.clock.Add(-time.Minute).Format(time.RFC3339)
	f.supplied("tracker.json", suppliedDoc("tracker", items, map[string]any{"fetched_at": fetched, "attempted_at": fetched}))
}

// nextDay moves the clock a day on.
func (f *fixture) nextDay() { f.clock = f.clock.AddDate(0, 0, 1) }

func TestSnapshotRecordsTheFirstDayAsBacklog(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	b := f.snapshot()

	if b.Date != "2030-06-15" || b.PreviousDate != "" {
		t.Errorf("date = %q, previous_date = %q", b.Date, b.PreviousDate)
	}
	if b.At != "2030-06-15T12:00:00Z" {
		t.Errorf("at = %q", b.At)
	}
	if !slices.Equal(b.Backlog, []string{"tracker:ABC-3"}) || !slices.Equal(b.Unplaced, []string{"tracker:ABC-3"}) {
		t.Errorf("backlog = %v, unplaced = %v, want the one unplaced item in both", b.Backlog, b.Unplaced)
	}
	if b.Arrivals == nil || len(b.Arrivals) != 0 {
		t.Errorf("arrivals = %#v, want an empty list: nothing arrives on the first day", b.Arrivals)
	}
	if b.ActiveWorkstreams != 2 || b.UnloggedEdit || b.Incomplete {
		t.Errorf("active = %d, unlogged_edit = %v, incomplete = %v", b.ActiveWorkstreams, b.UnloggedEdit, b.Incomplete)
	}
	if len(b.Workstreams) != 2 || b.Workstreams[0].Name != "billing" || !slices.Equal(b.Workstreams[0].Members, []string{"tracker:ABC-1", "tracker:ABC-2"}) {
		t.Errorf("workstreams = %+v", b.Workstreams)
	}
	if len(b.Health) != 1 || b.Health[0].Input != "tracker" || b.Health[0].Status != "ok" {
		t.Errorf("health = %+v", b.Health)
	}
	if len(f.gestures("personal")) != 0 {
		t.Error("snapshot recorded a gesture")
	}
}

func TestSnapshotCountsArrivalsHoweverTheyWereHandled(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	day1 := f.clock
	f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1})
	f.snapshot()

	f.nextDay()
	day2 := f.clock.Add(-time.Hour)
	f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1, "ABC-4": day2, "ABC-5": day2, "ABC-6": day2})
	f.mustRun("adopt", "tracker:ABC-5", "billing")
	f.mustRun("dismiss", "tracker:ABC-6")
	// Refused: neither names an item that came and went.
	if code, _, _ := f.run("adopt", "tracker:ABC-7", "nowhere"); code != exitFailure {
		t.Fatalf("adopt into an unknown workstream: exit code = %d", code)
	}
	if code, _, _ := f.run("dismiss", "tracker:ABC-1"); code != exitFailure {
		t.Fatalf("dismiss a member: exit code = %d", code)
	}

	b := f.snapshot()
	if want := []string{"tracker:ABC-4", "tracker:ABC-5", "tracker:ABC-6"}; !slices.Equal(b.Arrivals, want) {
		t.Errorf("day 2 arrivals = %v, want %v: new, adopted and dismissed alike, never the backlog", b.Arrivals, want)
	}
	if want := []string{"tracker:ABC-3", "tracker:ABC-4"}; !slices.Equal(b.Unplaced, want) {
		t.Errorf("day 2 unplaced = %v, want %v", b.Unplaced, want)
	}
	if b.Backlog != nil {
		t.Errorf("backlog = %v on a later record, want it on the first only", b.Backlog)
	}

	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1, "ABC-4": day2, "ABC-5": day2, "ABC-6": f.clock.Add(-time.Hour)})
	b = f.snapshot()
	if want := []string{"tracker:ABC-6"}; !slices.Equal(b.Arrivals, want) {
		t.Errorf("day 3 arrivals = %v, want %v: a dismissed item that moves asks again", b.Arrivals, want)
	}
}

func TestSnapshotNeverCountsWorkingThroughTheBacklog(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	day1 := f.clock
	f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1})
	f.snapshot()

	f.nextDay()
	f.mustRun("dismiss", "tracker:ABC-3")
	f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1})
	f.snapshot()

	f.nextDay()
	f.mustRun("adopt", "tracker:ABC-3", "billing")
	f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1})
	if b := f.snapshot(); len(b.Arrivals) != 0 {
		t.Errorf("arrivals = %v, want an item already present on the first day never counted", b.Arrivals)
	}
}

func TestSnapshotKeepsTheFirstRecordOfADate(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	f.snapshot()

	f.clock = now.Add(6 * time.Hour)
	beforeSnapshotLock = func() { t.Error("a date already recorded read its inputs again") }
	t.Cleanup(func() { beforeSnapshotLock = func() {} })
	code, stdout, stderr := f.run("snapshot")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "2030-06-15") {
		t.Errorf("second snapshot: exit code = %d, stdout = %q, stderr = %q, want a note naming the date", code, stdout, stderr)
	}
	if records := f.baseline("personal"); len(records) != 1 || records[0].At != "2030-06-15T12:00:00Z" {
		t.Errorf("baseline = %+v, want only the first record of the date", records)
	}
}

func TestSnapshotDatesARecordOnTheClocksOwnDay(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	zone := time.FixedZone("west", -7*60*60)
	f.clock = time.Date(2030, 6, 15, 23, 30, 0, 0, zone)
	f.tracker(map[string]time.Time{"ABC-1": f.clock})
	if b := f.snapshot(); b.Date != "2030-06-15" {
		t.Errorf("date = %q, want the local date, not UTC's", b.Date)
	}

	f.clock = time.Date(2030, 6, 16, 0, 10, 0, 0, zone)
	f.tracker(map[string]time.Time{"ABC-1": f.clock})
	if b := f.snapshot(); b.Date != "2030-06-16" || b.PreviousDate != "2030-06-15" || b.At != "2030-06-16T07:10:00Z" {
		t.Errorf("after midnight: date = %q, previous_date = %q, at = %q", b.Date, b.PreviousDate, b.At)
	}
}

func TestSnapshotNamesTheRecordBeforeASkippedDay(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": f.clock})
	f.snapshot()

	f.nextDay()
	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": f.clock})
	if b := f.snapshot(); b.Date != "2030-06-17" || b.PreviousDate != "2030-06-15" {
		t.Errorf("date = %q, previous_date = %q, want the gap visible", b.Date, b.PreviousDate)
	}
}

func TestSnapshotCountsOnlyActiveLiveWorkstreams(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
event_window: 24h
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
  - name: onboarding
    members: ["tracker:ABC-9"]
  - name: archive
    completed: 2030-06-01
    members: ["tracker:ABC-8"]
`)
	f.tracker(map[string]time.Time{"ABC-1": now.Add(-3 * time.Hour), "ABC-9": now.Add(-48 * time.Hour), "ABC-8": now.Add(-30 * 24 * time.Hour)})

	if b := f.snapshot(); b.ActiveWorkstreams != 1 {
		t.Errorf("active_workstreams = %d, want billing alone: onboarding is idle past the window and archive is complete", b.ActiveWorkstreams)
	}
}

func TestSnapshotMeasuresStalenessOverTheWorkstreamsItRead(t *testing.T) {
	t.Run("some read", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", `
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: a
    members: ["tracker:A"]
  - name: b
    members: ["tracker:B"]
  - name: c
    members: ["tracker:C"]
  - name: d
    members: ["tracker:D"]
  - name: unread
    members: ["tracker:GONE"]
  - name: done
    completed: 2030-06-01
    members: ["tracker:Z"]
`)
		f.tracker(map[string]time.Time{
			"A": now.Add(-time.Hour), "B": now.Add(-2 * time.Hour), "C": now.Add(-3 * time.Hour), "D": now.Add(-10 * time.Hour),
			"Z": now.Add(-40 * 24 * time.Hour),
		})

		s := f.snapshot().Staleness
		if s.Workstreams != 4 || s.UnreadWorkstreams != 1 {
			t.Errorf("workstreams = %d, unread = %d, want 4 measured and 1 counted aside", s.Workstreams, s.UnreadWorkstreams)
		}
		// Nearest rank: the 2nd of 4 for the median and the 4th for p85.
		if s.MedianSeconds == nil || *s.MedianSeconds != 2*60*60 || s.P85Seconds == nil || *s.P85Seconds != 10*60*60 {
			t.Errorf("median = %v, p85 = %v", s.MedianSeconds, s.P85Seconds)
		}
	})
	t.Run("none read", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", handWritten)
		f.tracker(map[string]time.Time{"ABC-3": now})

		s := f.snapshot().Staleness
		if s.MedianSeconds != nil || s.P85Seconds != nil || s.Workstreams != 0 || s.UnreadWorkstreams != 2 {
			t.Errorf("staleness = %+v, want no figures and both workstreams unread", s)
		}
	})
}

func TestSnapshotCountsCompletionsSinceThePreviousRecord(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten+`
  - name: archive
    completed: 2030-06-01
    members:
      - tracker:ABC-8
  - name: ahead
    completed: 2030-06-20
    members:
      - tracker:ABC-7
`)
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 0 {
		t.Errorf("first record: completed_since_previous = %d, want an old completion left out", b.CompletedSincePrevious)
	}

	f.nextDay()
	f.mustRun("complete", "billing")
	// Completed by hand rather than by command, which counts just the same.
	f.write("personal.yml", strings.Replace(f.config("personal.yml"), "  - name: onboarding\n", "  - name: onboarding\n    completed: 2030-06-16\n", 1))
	f.tracker(map[string]time.Time{"ABC-1": now})

	if b := f.snapshot(); b.CompletedSincePrevious != 2 {
		t.Errorf("completed_since_previous = %d, want both of the day's completions and not one dated ahead", b.CompletedSincePrevious)
	}
}

func TestSnapshotFindsEditsNoGestureExplains(t *testing.T) {
	withCompleted := strings.Replace(handWritten, "workstreams:\n", "workstreams:\n  - name: done\n    completed: 2030-06-01\n    members:\n      - tracker:ABC-3\n", 1)
	for name, tc := range map[string]struct {
		config  string
		between func(f *fixture)
		want    bool
	}{
		"every change logged": {handWritten, func(f *fixture) {
			f.mustRun("adopt", "tracker:ABC-3", "billing")
			f.mustRun("adopt", "tracker:ABC-4", "infra", "--new")
			f.mustRun("order", "onboarding")
			f.mustRun("complete", "onboarding")
		}, false},
		"a member added by hand": {handWritten, func(f *fixture) {
			f.write("personal.yml", strings.Replace(handWritten, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-3\n", 1))
		}, true},
		"a gesture beside a hand edit": {handWritten, func(f *fixture) {
			f.mustRun("order", "onboarding")
			f.write("personal.yml", strings.Replace(f.config("personal.yml"), "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-3\n", 1))
		}, true},
		"a refused gesture explains nothing": {handWritten, func(f *fixture) {
			if code, _, _ := f.run("adopt", "tracker:ABC-3", "infra"); code != exitFailure {
				f.t.Fatalf("adopt without --new: exit code = %d", code)
			}
			f.write("personal.yml", handWritten+"  - name: infra\n    members:\n      - tracker:ABC-3\n")
		}, true},
		"an adopt out of a completed workstream": {withCompleted, func(f *fixture) {
			f.mustRun("adopt", "tracker:ABC-3", "onboarding")
		}, false},
		"a move, a remove and a rename": {handWritten, func(f *fixture) {
			f.mustRun("move", "tracker:ABC-2", "onboarding")
			f.mustRun("remove", "tracker:ABC-9")
			f.mustRun("rename", "billing", "ledger")
		}, false},
		"a rename by hand": {handWritten, func(f *fixture) {
			f.write("personal.yml", strings.Replace(handWritten, "  - name: billing\n", "  - name: ledger\n", 1))
		}, true},
		"a move by hand": {handWritten, func(f *fixture) {
			moved := strings.Replace(handWritten, "      - tracker:ABC-2\n", "", 1)
			f.write("personal.yml", strings.Replace(moved, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-2\n", 1))
		}, true},
		"a removal by hand": {handWritten, func(f *fixture) {
			f.write("personal.yml", strings.Replace(handWritten, "      - tracker:ABC-2\n", "", 1))
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", tc.config)
			f.tracker(map[string]time.Time{"ABC-1": now})
			f.snapshot()

			f.nextDay()
			tc.between(f)
			f.tracker(map[string]time.Time{"ABC-1": now})
			if b := f.snapshot(); b.UnloggedEdit != tc.want {
				t.Errorf("unlogged_edit = %v, want %v", b.UnloggedEdit, tc.want)
			}
		})
	}
}

func TestSnapshotMarksARecordWhoseInputsFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		file       map[string]any
		status     string
		incomplete bool
	}{
		"missing": {nil, "error", true},
		"stale":   {suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, map[string]any{"fetched_at": stamp(48 * time.Hour), "attempted_at": stamp(48 * time.Hour)}), "degraded", true},
		"healthy": {trackerItems(), "ok", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			if tc.file != nil {
				f.supplied("tracker.json", tc.file)
			}

			b := f.snapshot()

			if b.Incomplete != tc.incomplete || len(b.Health) != 1 || b.Health[0].Status != tc.status {
				t.Errorf("incomplete = %v, health = %+v, want %s", b.Incomplete, b.Health, tc.status)
			}
		})
	}
}

func TestSnapshotStartsOverWhenAGestureLandsWhileItReads(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	once := false
	beforeSnapshotLock = func() {
		if !once {
			once = true
			f.mustRun("adopt", "tracker:ABC-3", "billing")
		}
	}
	t.Cleanup(func() { beforeSnapshotLock = func() {} })

	b := f.snapshot()

	if len(b.Unplaced) != 0 || !slices.Contains(b.Workstreams[0].Members, "tracker:ABC-3") {
		t.Errorf("unplaced = %v, workstreams = %+v, want the record taken after the adopt", b.Unplaced, b.Workstreams)
	}
}

func TestSnapshotStartsOverWhenADismissalLandsWhileItReads(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	once := false
	beforeSnapshotLock = func() {
		if !once {
			once = true
			f.mustRun("dismiss", "tracker:ABC-3")
		}
	}
	t.Cleanup(func() { beforeSnapshotLock = func() {} })

	if b := f.snapshot(); len(b.Unplaced) != 0 {
		t.Errorf("unplaced = %v, want the record taken after the dismissal", b.Unplaced)
	}
}

func TestTwoSnapshotsRacingRecordTheDateOnce(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	once := false
	beforeSnapshotLock = func() {
		if !once {
			once = true
			f.snapshot()
		}
	}
	t.Cleanup(func() { beforeSnapshotLock = func() {} })

	f.snapshot()

	if records := f.baseline("personal"); len(records) != 1 {
		t.Errorf("baseline has %d records, want one for the date", len(records))
	}
}

func TestACorruptBaselineFailsLoudly(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	f.snapshot()

	file, err := os.OpenFile(f.baselinePath("personal"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := file.WriteString(`{"date": "2030-06-`); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	before, err := os.ReadFile(f.baselinePath("personal"))
	if err != nil {
		t.Fatal(err)
	}

	f.nextDay()
	code, _, stderr := f.run("snapshot")
	if code != exitFailure || !strings.Contains(stderr, "line 2") {
		t.Errorf("exit code = %d, stderr = %q, want a failure naming the bad line", code, stderr)
	}
	if after, err := os.ReadFile(f.baselinePath("personal")); err != nil || string(after) != string(before) {
		t.Errorf("baseline changed after a failed snapshot: %v", err)
	}
}

func TestSnapshotIsPrivateAndBelongsToOneConfiguration(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	f.snapshot()

	info, err := os.Stat(f.baselinePath("personal"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("baseline mode = %v, want 0600", info.Mode().Perm())
	}

	other := filepath.Join(t.TempDir(), "personal.yml")
	if err := os.WriteFile(other, []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}
	f.env["LOOKOUT_CONFIG"] = other
	f.nextDay()
	if code, _, _ := f.run("snapshot"); code != exitFailure {
		t.Errorf("exit code = %d, want a refusal from state another configuration owns", code)
	}
	if records := f.baseline("personal"); len(records) != 1 {
		t.Errorf("baseline has %d records, want the refused snapshot to add none", len(records))
	}
}

func TestSnapshotUsage(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	code, _, stderr := f.run("snapshot", "extra")
	if code != exitUsage || !strings.Contains(stderr, "lookout snapshot") {
		t.Errorf("exit code = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(f.stateDir("personal")); !os.IsNotExist(err) {
		t.Errorf("state directory exists after a usage error: %v", err)
	}
}

func TestSnapshotCountsACompletionMadeAfterTheDaysSnapshot(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.clock = now.Add(5 * time.Hour)
	f.mustRun("complete", "billing")

	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 1 {
		t.Errorf("completed_since_previous = %d, want the completion made after the previous snapshot that same day", b.CompletedSincePrevious)
	}

	// A date written by hand after the fact is still a completion not yet counted.
	f.write("personal.yml", strings.Replace(f.config("personal.yml"), "  - name: onboarding\n", "  - name: onboarding\n    completed: 2030-06-01\n", 1))
	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 1 {
		t.Errorf("completed_since_previous = %d, want a back-dated completion counted once", b.CompletedSincePrevious)
	}
	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 0 {
		t.Errorf("completed_since_previous = %d, want nothing counted twice", b.CompletedSincePrevious)
	}
}

func TestSnapshotDoesNotCountWhatAnUnhealthyDayHid(t *testing.T) {
	t.Run("a failed day between healthy ones", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", handWritten)
		day1 := f.clock
		f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1, "ABC-4": day1})
		f.snapshot()
		f.nextDay()
		f.mustRun("dismiss", "tracker:ABC-4")
		f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1, "ABC-4": day1, "ABC-5": f.clock})
		f.snapshot()

		f.nextDay()
		if err := os.Remove(filepath.Join(f.dir, "tracker.json")); err != nil {
			t.Fatal(err)
		}
		if b := f.snapshot(); !b.Incomplete || len(b.Arrivals) != 0 {
			t.Fatalf("failed day: incomplete = %v, arrivals = %v", b.Incomplete, b.Arrivals)
		}

		f.nextDay()
		f.tracker(map[string]time.Time{"ABC-1": day1, "ABC-3": day1, "ABC-4": day1, "ABC-5": day1, "ABC-6": f.clock})
		if b := f.snapshot(); !slices.Equal(b.Arrivals, []string{"tracker:ABC-6"}) {
			t.Errorf("arrivals = %v, want only the item new since the input last read healthy", b.Arrivals)
		}
	})
	t.Run("an input failing on the first day", func(t *testing.T) {
		f := newFixture(t)
		f.write("personal.yml", handWritten)
		f.snapshot()

		f.nextDay()
		f.tracker(map[string]time.Time{"ABC-1": now, "ABC-3": now})
		if b := f.snapshot(); len(b.Arrivals) != 0 {
			t.Errorf("arrivals = %v, want an input's first healthy reading taken as its backlog", b.Arrivals)
		}

		f.nextDay()
		f.tracker(map[string]time.Time{"ABC-1": now, "ABC-3": now, "ABC-4": f.clock})
		if b := f.snapshot(); !slices.Equal(b.Arrivals, []string{"tracker:ABC-4"}) {
			t.Errorf("arrivals = %v", b.Arrivals)
		}
	})
}

func TestSnapshotCountsAnItemPlacedByHand(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-3": now})
	f.snapshot()

	f.nextDay()
	// ABC-5 appears and is placed by hand before any snapshot sees it, and the
	// backlog's ABC-3 is placed by hand too.
	f.write("personal.yml", strings.Replace(handWritten, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-5\n      - tracker:ABC-3\n", 1))
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-3": now, "ABC-5": f.clock})

	b := f.snapshot()
	if !slices.Equal(b.HandPlaced, []string{"tracker:ABC-3", "tracker:ABC-5"}) {
		t.Errorf("hand_placed = %v, want both members no gesture added", b.HandPlaced)
	}
	if !slices.Equal(b.Arrivals, []string{"tracker:ABC-5"}) {
		t.Errorf("arrivals = %v, want the new item placed by hand, never the backlog's", b.Arrivals)
	}
	if !b.UnloggedEdit {
		t.Error("unlogged_edit = false")
	}

	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.HandPlaced == nil || len(b.HandPlaced) != 0 {
		t.Errorf("hand_placed = %#v, want an empty list once nothing new was placed", b.HandPlaced)
	}
}

func TestSnapshotFlagsAChangeToWhatIsDiscovered(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.DiscoveryChanged {
		t.Error("first record: discovery_changed = true")
	}

	f.nextDay()
	f.write("personal.yml", strings.Replace(handWritten, "\nworkstreams:\n", `  - name: calendar
    kind: supplied
    supplied: calendar.json
    max_age: 24h

workstreams:
`, 1))
	f.tracker(map[string]time.Time{"ABC-1": now})
	fetched := f.clock.Add(-time.Minute).Format(time.RFC3339)
	f.supplied("calendar.json", suppliedDoc("calendar", []any{item("EV-1", time.Hour, nil)}, map[string]any{"fetched_at": fetched, "attempted_at": fetched}))
	b := f.snapshot()
	if !b.DiscoveryChanged || len(b.Arrivals) != 0 || b.UnloggedEdit {
		t.Errorf("discovery_changed = %v, arrivals = %v, unlogged_edit = %v, want a flagged change whose items are the new input's backlog", b.DiscoveryChanged, b.Arrivals, b.UnloggedEdit)
	}

	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.supplied("calendar.json", suppliedDoc("calendar", []any{item("EV-1", time.Hour, nil)}, map[string]any{"fetched_at": f.clock.Add(-time.Minute).Format(time.RFC3339), "attempted_at": f.clock.Add(-time.Minute).Format(time.RFC3339)}))
	if b := f.snapshot(); b.DiscoveryChanged {
		t.Error("discovery_changed = true with nothing changed")
	}
}

func TestSnapshotRecordsNothingForADateBeforeTheLast(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	f.nextDay()
	f.snapshot()

	f.clock = now
	beforeSnapshotLock = func() { t.Error("an earlier date read its inputs") }
	t.Cleanup(func() { beforeSnapshotLock = func() {} })
	code, stdout, stderr := f.run("snapshot")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "2030-06-15") || !strings.Contains(stderr, "2030-06-16") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a note naming both dates", code, stdout, stderr)
	}
	if records := f.baseline("personal"); len(records) != 1 {
		t.Errorf("baseline has %d records", len(records))
	}
}

func TestABaselineLineOfTheWrongShapeFailsLoudly(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	f.snapshot()
	file, err := os.OpenFile(f.baselinePath("personal"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := file.WriteString("[]\n"); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	f.nextDay()
	code, _, stderr := f.run("snapshot")
	if code != exitFailure || !strings.Contains(stderr, "line 2") || !strings.Contains(stderr, f.baselinePath("personal")) {
		t.Errorf("exit code = %d, stderr = %q, want a failure naming the file and the line", code, stderr)
	}
}

func TestSnapshotCountsACompletionDatedAheadOnceItsDateArrives(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(handWritten, "  - name: onboarding\n", "  - name: onboarding\n    completed: 2030-06-16\n", 1))
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 0 {
		t.Errorf("before its date: completed_since_previous = %d", b.CompletedSincePrevious)
	}
	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 1 {
		t.Errorf("on its date: completed_since_previous = %d, want 1", b.CompletedSincePrevious)
	}
}

func TestSnapshotCountsAnItemMovedByHandOutOfACompletedWorkstream(t *testing.T) {
	f := newFixture(t)
	withDone := strings.Replace(handWritten, "workstreams:\n", "workstreams:\n  - name: done\n    completed: 2030-06-01\n    members:\n      - tracker:ABC-7\n", 1)
	f.write("personal.yml", withDone)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	moved := strings.Replace(withDone, "    members:\n      - tracker:ABC-7\n", "", 1)
	moved = strings.Replace(moved, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-7\n", 1)
	f.write("personal.yml", moved)
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-7": f.clock})

	b := f.snapshot()
	if !slices.Equal(b.HandPlaced, []string{"tracker:ABC-7"}) || !slices.Equal(b.Arrivals, []string{"tracker:ABC-7"}) {
		t.Errorf("hand_placed = %v, arrivals = %v, want the item moved into a live workstream counted as an adopt would be", b.HandPlaced, b.Arrivals)
	}
}

func TestSnapshotKeepsTheBacklogOfAnInputFirstHealthyLater(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.snapshot()

	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-3": now})
	f.snapshot()

	f.nextDay()
	f.mustRun("dismiss", "tracker:ABC-3")
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-3": now})
	f.snapshot()

	f.nextDay()
	f.mustRun("adopt", "tracker:ABC-3", "billing")
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-3": now})
	if b := f.snapshot(); len(b.Arrivals) != 0 {
		t.Errorf("arrivals = %v, want working through an input's first healthy reading never counted", b.Arrivals)
	}
}

func TestSnapshotReplaysACompletionOnTheDateItWrote(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	// Completed late in the evening west of UTC, where it is already the next
	// day in UTC.
	f.clock = time.Date(2030, 6, 15, 23, 30, 0, 0, time.FixedZone("west", -7*60*60))
	f.mustRun("complete", "billing")

	f.clock = now.AddDate(0, 0, 1)
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.UnloggedEdit {
		t.Error("unlogged_edit = true, want a logged completion replayed on the date it wrote")
	}
}

func TestSnapshotCountsAnItemPlacedByHandInAWorkstreamCompletedSince(t *testing.T) {
	f := newFixture(t)
	withDone := strings.Replace(handWritten, "workstreams:\n", "workstreams:\n  - name: done\n    completed: 2030-06-01\n    members:\n      - tracker:ABC-8\n", 1)
	f.write("personal.yml", withDone)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.write("personal.yml", strings.Replace(withDone, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-5\n", 1))
	f.mustRun("complete", "onboarding")
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-5": f.clock.Add(-time.Hour)})

	b := f.snapshot()
	if !slices.Equal(b.HandPlaced, []string{"tracker:ABC-5"}) || !slices.Equal(b.Arrivals, []string{"tracker:ABC-5"}) {
		t.Errorf("hand_placed = %v, arrivals = %v, want the item placed by hand before its workstream was completed, and never the completed workstream's untouched member", b.HandPlaced, b.Arrivals)
	}
}

func TestSnapshotListsAnItemPlacedByHandInTwoWorkstreamsOnce(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	twice := strings.Replace(handWritten, "      - tracker:ABC-2\n", "      - tracker:ABC-2\n      - tracker:ABC-5\n", 1)
	f.write("personal.yml", strings.Replace(twice, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-5\n", 1))
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-5": f.clock})

	if b := f.snapshot(); !slices.Equal(b.HandPlaced, []string{"tracker:ABC-5"}) {
		t.Errorf("hand_placed = %v, want the item once however many workstreams hold it", b.HandPlaced)
	}
}

func TestSnapshotCountsAnItemMovedByHandBetweenCompletedWorkstreams(t *testing.T) {
	f := newFixture(t)
	withDone := strings.Replace(handWritten, "workstreams:\n", "workstreams:\n  - name: done\n    completed: 2030-06-01\n    members:\n      - tracker:ABC-7\n      - tracker:ABC-8\n", 1)
	f.write("personal.yml", withDone)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	moved := strings.Replace(withDone, "      - tracker:ABC-7\n", "", 1)
	f.write("personal.yml", strings.Replace(moved, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-7\n", 1))
	f.mustRun("complete", "onboarding")
	f.tracker(map[string]time.Time{"ABC-1": now, "ABC-7": f.clock.Add(-time.Hour)})

	b := f.snapshot()
	if !slices.Equal(b.HandPlaced, []string{"tracker:ABC-7"}) || !slices.Equal(b.Arrivals, []string{"tracker:ABC-7"}) {
		t.Errorf("hand_placed = %v, arrivals = %v, want the item moved into a workstream completed since counted as an adopt would be", b.HandPlaced, b.Arrivals)
	}
}

func TestSnapshotPlacesNothingForALoggedMoveOrRename(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten+"  - name: done\n    completed: 2030-06-01\n    members:\n      - tracker:ABC-7\n")
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.mustRun("move", "tracker:ABC-1", "onboarding")
	f.mustRun("rename", "done", "archive")
	f.mustRun("rename", "onboarding", "setup")
	f.tracker(map[string]time.Time{"ABC-1": now})

	b := f.snapshot()
	if b.UnloggedEdit || len(b.HandPlaced) != 0 || len(b.Arrivals) != 0 {
		t.Errorf("unlogged_edit = %v, hand_placed = %v, arrivals = %v, want logged gestures to explain every change", b.UnloggedEdit, b.HandPlaced, b.Arrivals)
	}
}

func TestSnapshotCountsALoggedRenameOfACompletedWorkstreamOnce(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.mustRun("complete", "onboarding")
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 1 {
		t.Fatalf("completed_since_previous = %d, want the completion counted", b.CompletedSincePrevious)
	}

	f.nextDay()
	f.mustRun("rename", "onboarding", "onboarding-2030")
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.CompletedSincePrevious != 0 {
		t.Errorf("completed_since_previous = %d, want a renamed completed workstream not counted again", b.CompletedSincePrevious)
	}
}

func TestSnapshotCountsARemovedItemThatReappearsAsArriving(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.mustRun("remove", "tracker:ABC-1")
	f.tracker(map[string]time.Time{"ABC-1": f.clock.Add(-time.Hour)})

	b := f.snapshot()
	if !slices.Equal(b.Arrivals, []string{"tracker:ABC-1"}) || b.UnloggedEdit {
		t.Errorf("arrivals = %v, unlogged_edit = %v, want the removed item asking for a decision again, by a logged gesture", b.Arrivals, b.UnloggedEdit)
	}
}

func TestSnapshotCountsACompletionUnderANameARenameFreed(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(handWritten, "  - name: onboarding\n", "  - name: onboarding\n    completed: 2030-06-01\n", 1))
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.mustRun("rename", "onboarding", "onboarding-2029")
	f.mustRun("adopt", "--new", "tracker:ABC-3", "onboarding")
	f.mustRun("complete", "onboarding")
	f.tracker(map[string]time.Time{"ABC-1": now})

	if b := f.snapshot(); b.CompletedSincePrevious != 1 {
		t.Errorf("completed_since_previous = %d, want the new workstream under the freed name counted", b.CompletedSincePrevious)
	}
}

func TestSnapshotDoesNotTakeAMembershipChangeForADiscoveryChange(t *testing.T) {
	lone := "inputs:\n  - name: tracker\n    kind: supplied\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n"
	f := newFixture(t)
	f.write("personal.yml", lone)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.mustRun("remove", "tracker:ABC-1")
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.DiscoveryChanged {
		t.Errorf("after removing the last member: discovery_changed = true, want a logged membership change kept apart from what inputs offer")
	}

	f.nextDay()
	f.mustRun("adopt", "tracker:ABC-1", "billing")
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); b.DiscoveryChanged {
		t.Errorf("after adopting the first member: discovery_changed = true, want a logged membership change kept apart from what inputs offer")
	}
}

// keyedCode is a setting whose tracker tickets' keys place git branches.
const keyedCode = `
discovery_window: 72h
key_pattern: '([A-Z]+-[0-9]+)'
forge_owners: [alice]
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: code
    kind: git
    repos: ["~/code/*"]
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
`

func TestSnapshotCountsAJoinedItemApartAndAsArrivingOnce(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", keyedCode)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.branch(app, "feature/ABC-1-export", -23*time.Hour)
	f.tracker(map[string]time.Time{"ABC-1": now})
	b := f.snapshot()
	if !slices.Equal(b.Joined, []string{"code:alice/app:feature/ABC-1-export"}) || !slices.Equal(b.Arrivals, []string{"code:alice/app:feature/ABC-1-export"}) {
		t.Errorf("joined = %v, arrivals = %v, want the joined branch listed apart and arriving", b.Joined, b.Arrivals)
	}
	if len(b.HandPlaced) != 0 || len(b.Unplaced) != 0 {
		t.Errorf("hand_placed = %v, unplaced = %v, want a join counted as neither", b.HandPlaced, b.Unplaced)
	}

	f.nextDay()
	f.tracker(map[string]time.Time{"ABC-1": now})
	if b := f.snapshot(); !slices.Equal(b.Joined, []string{"code:alice/app:feature/ABC-1-export"}) || len(b.Arrivals) != 0 {
		t.Errorf("next day: joined = %v, arrivals = %v, want it still joined and not arriving again", b.Joined, b.Arrivals)
	}
}

func TestSnapshotTakesOldWorkAPatternJoinsAsBacklog(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", strings.Replace(keyedCode, "key_pattern: '([A-Z]+-[0-9]+)'\n", "", 1))
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.branch(app, "feature/ABC-1-old", 10*24*time.Hour)
	f.tracker(map[string]time.Time{"ABC-1": now})
	f.snapshot()

	f.nextDay()
	f.write("personal.yml", keyedCode)
	f.tracker(map[string]time.Time{"ABC-1": now})
	b := f.snapshot()
	if !b.DiscoveryChanged {
		t.Error("discovery_changed = false, want setting a pattern flagged as a change to what is discovered")
	}
	if !slices.Equal(b.Joined, []string{"code:alice/app:feature/ABC-1-old"}) || len(b.Arrivals) != 0 || !slices.Contains(b.Backlog, "code:alice/app:feature/ABC-1-old") {
		t.Errorf("joined = %v, arrivals = %v, backlog = %v, want work older than the discovery window joined as backlog, never arriving", b.Joined, b.Arrivals, b.Backlog)
	}
}

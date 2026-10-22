package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// refusedUnchanged requires a command to be refused with a message mentioning
// mention, to leave the configuration as it was, and to be logged once as a
// refused gesture with its reason.
func (f *fixture) refusedUnchanged(before, gesture, mention string, args ...string) {
	f.t.Helper()
	code, stdout, stderr := f.run(append([]string{gesture}, args...)...)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, mention) {
		f.t.Errorf("%v: exit code = %d, stdout = %q, stderr = %q, want a refusal mentioning %q", args, code, stdout, stderr, mention)
	}
	if got := f.config("personal.yml"); got != before {
		f.t.Errorf("configuration changed by a refused %s:\n%s", gesture, got)
	}
	gs := f.gestures("personal")
	if len(gs) != 1 || gs[0].Gesture != gesture || gs[0].Outcome != "refused" || gs[0].Reason == "" {
		f.t.Errorf("gestures = %+v, want one refused %s with its reason", gs, gesture)
	}
}

func itemIDs(items []struct {
	Input string         `json:"input"`
	Item  map[string]any `json:"item"`
}) []string {
	ids := []string{}
	for _, it := range items {
		ids = append(ids, it.Item["id"].(string))
	}
	return ids
}

func TestMoveTakesAnItemToAnotherWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("move", "tracker:ABC-2", "onboarding")

	want := strings.Replace(handWritten, "      - tracker:ABC-2\n", "", 1)
	want = strings.Replace(want, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-2\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after move:\n%s\nwant every other line unchanged:\n%s", got, want)
	}
	doc := f.read()
	if ids := itemIDs(doc.Workstreams[0].Items); !slices.Equal(ids, []string{"ABC-1"}) {
		t.Errorf("billing items = %v, want only ABC-1", ids)
	}
	if ids := itemIDs(doc.Workstreams[1].Items); !slices.Equal(ids, []string{"ABC-9", "ABC-2"}) {
		t.Errorf("onboarding items = %v, want ABC-2 added", ids)
	}
	gs := f.gestures("personal")
	if len(gs) != 1 {
		t.Fatalf("gestures = %+v, want exactly one", gs)
	}
	if g := gs[0]; g.At != now.Format(time.RFC3339) || g.Gesture != "move" || g.Outcome != "ok" || g.Item != "tracker:ABC-2" || g.Workstream != "onboarding" || !slices.Equal(g.From, []string{"billing"}) || g.New {
		t.Errorf("gesture = %+v", g)
	}
}

func TestMoveNewAppendsTheTarget(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)

	f.mustRun("move", "--new", "tracker:ABC-9", "support")

	want := strings.Replace(handWritten, "      - tracker:ABC-9\n", "", 1) + "  - name: support\n    members:\n      - tracker:ABC-9\n"
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after move --new:\n%q\nwant:\n%q", got, want)
	}
	if gs := f.gestures("personal"); len(gs) != 1 || !gs[0].New || !slices.Equal(gs[0].From, []string{"onboarding"}) {
		t.Errorf("gestures = %+v, want one move marked new, from onboarding", gs)
	}
}

// twiceHeld lists one item in two workstreams, which only a hand edit makes.
var twiceHeld = strings.Replace(handWritten, "      - tracker:ABC-9\n", "      - tracker:ABC-9\n      - tracker:ABC-1\n", 1) + "  - name: support\n"

func TestMoveTakesAnItemOutOfEveryWorkstreamHoldingIt(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", twiceHeld)

	f.mustRun("move", "tracker:ABC-1", "support")

	want := strings.Replace(twiceHeld, "      - tracker:ABC-1   # the export\n", "", 1)
	want = strings.Replace(want, "      - tracker:ABC-9\n      - tracker:ABC-1\n", "      - tracker:ABC-9\n", 1)
	want += "    members:\n      - tracker:ABC-1\n"
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration:\n%q\nwant:\n%q", got, want)
	}
	if gs := f.gestures("personal"); len(gs) != 1 || !slices.Equal(gs[0].From, []string{"billing", "onboarding"}) {
		t.Errorf("gestures = %+v, want one move from both workstreams", gs)
	}
}

func TestMoveIntoAWorkstreamAlreadyHoldingTheItemLeavesItThere(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", twiceHeld)

	f.mustRun("move", "tracker:ABC-1", "billing")

	want := strings.Replace(twiceHeld, "      - tracker:ABC-9\n      - tracker:ABC-1\n", "      - tracker:ABC-9\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration:\n%q\nwant:\n%q", got, want)
	}
	if gs := f.gestures("personal"); len(gs) != 1 || !slices.Equal(gs[0].From, []string{"onboarding"}) {
		t.Errorf("gestures = %+v, want one move from onboarding only", gs)
	}
}

func TestMoveRefusals(t *testing.T) {
	withDone := handWritten + "  - name: done\n    completed: 2030-06-01\n    members:\n      - tracker:ABC-7\n"
	cases := map[string]struct {
		config, mention string
		args            []string
	}{
		"not in any workstream":             {handWritten, "lookout adopt", []string{"tracker:ABC-3", "onboarding"}},
		"only in a completed workstream":    {withDone, "lookout adopt", []string{"tracker:ABC-7", "onboarding"}},
		"already in the target":             {handWritten, "billing", []string{"tracker:ABC-1", "billing"}},
		"target completed":                  {withDone, "complete", []string{"tracker:ABC-1", "done"}},
		"unknown target":                    {handWritten, "--new", []string{"tracker:ABC-1", "support"}},
		"new but the target already exists": {handWritten, "onboarding", []string{"--new", "tracker:ABC-1", "onboarding"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", c.config)
			f.refusedUnchanged(c.config, "move", c.mention, c.args...)
		})
	}
}

func TestRemoveTakesAnItemOutOfItsWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("remove", "tracker:ABC-2")

	want := strings.Replace(handWritten, "      - tracker:ABC-2\n", "", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after remove:\n%s\nwant every other line unchanged:\n%s", got, want)
	}
	doc := f.read()
	if ids := unplacedIDs(doc); !slices.Contains(ids, "tracker:ABC-2") {
		t.Errorf("unplaced = %v, want the removed item offered again", ids)
	}
	gs := f.gestures("personal")
	if len(gs) != 1 {
		t.Fatalf("gestures = %+v, want exactly one", gs)
	}
	if g := gs[0]; g.Gesture != "remove" || g.Outcome != "ok" || g.Item != "tracker:ABC-2" || !slices.Equal(g.From, []string{"billing"}) || g.Workstream != "" {
		t.Errorf("gesture = %+v", g)
	}
}

func TestRemoveTakesAnItemOutOfACompletedWorkstream(t *testing.T) {
	completed := strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-10\n", 1)
	f := newFixture(t)
	f.write("personal.yml", completed)

	f.mustRun("remove", "tracker:ABC-1")

	if got, want := f.config("personal.yml"), strings.Replace(completed, "      - tracker:ABC-1   # the export\n", "", 1); got != want {
		t.Errorf("configuration:\n%q\nwant:\n%q", got, want)
	}
}

func TestRemoveTakesAnItemOutOfEveryWorkstreamHoldingIt(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", twiceHeld)

	f.mustRun("remove", "tracker:ABC-1")

	want := strings.Replace(twiceHeld, "      - tracker:ABC-1   # the export\n", "", 1)
	want = strings.Replace(want, "      - tracker:ABC-9\n      - tracker:ABC-1\n", "      - tracker:ABC-9\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration:\n%q\nwant:\n%q", got, want)
	}
	if gs := f.gestures("personal"); len(gs) != 1 || !slices.Equal(gs[0].From, []string{"billing", "onboarding"}) {
		t.Errorf("gestures = %+v, want one remove from both workstreams", gs)
	}
}

func TestRemoveRefusesAnItemNoWorkstreamHolds(t *testing.T) {
	for _, token := range []string{"tracker:ABC-3", "calendar:X-1"} {
		t.Run(token, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			f.refusedUnchanged(handWritten, "remove", token, token)
		})
	}
}

func TestRemovingTheLastMemberEndsTheExpectationItImplied(t *testing.T) {
	for name, tc := range map[string]struct {
		written    string
		wantStatus string
	}{
		"implied by membership": {"", "ok"},
		"written out":           {"    expect_records: true\n", "degraded"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", "inputs:\n  - name: tracker\n    kind: supplied\n    supplied: tracker.json\n    max_age: 24h\n"+tc.written+
				"workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n")
			f.supplied("tracker.json", suppliedDoc("tracker", []any{}, nil))
			if h := f.read().Health[0]; h.Status != "degraded" {
				t.Fatalf("health before remove = %+v, want an empty input a member expects degraded", h)
			}

			f.mustRun("remove", "tracker:ABC-1")

			if h := f.read().Health[0]; h.Status != tc.wantStatus {
				t.Errorf("health after remove = %+v, want %s", h, tc.wantStatus)
			}
		})
	}
}

func TestRenameKeepsTheWorkstreamInPlace(t *testing.T) {
	commented := strings.Replace(handWritten, "  - name: billing\n", "  - name: billing   # spring release\n", 1)
	f := newFixture(t)
	f.write("personal.yml", commented)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("rename", "billing", "billing-2030")

	want := strings.Replace(commented, "  - name: billing   # spring release\n", "  - name: billing-2030   # spring release\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after rename:\n%s\nwant every other line unchanged:\n%s", got, want)
	}
	doc := f.read()
	if names := workstreamNames(doc); !slices.Equal(names, []string{"billing-2030", "onboarding"}) {
		t.Errorf("workstreams = %v, want the new name in the old place", names)
	}
	if ids := itemIDs(doc.Workstreams[0].Items); !slices.Equal(ids, []string{"ABC-1", "ABC-2"}) {
		t.Errorf("items = %v, want the members kept", ids)
	}
	gs := f.gestures("personal")
	if len(gs) != 1 {
		t.Fatalf("gestures = %+v, want exactly one", gs)
	}
	if g := gs[0]; g.Gesture != "rename" || g.Outcome != "ok" || g.Workstream != "billing" || g.To != "billing-2030" {
		t.Errorf("gesture = %+v", g)
	}
}

func TestRenameQuotesANameThatNeedsIt(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)

	f.mustRun("rename", "onboarding", "q: #2")

	if names := workstreamNames(f.read()); !slices.Equal(names, []string{"billing", "q: #2"}) {
		t.Errorf("workstreams = %v, want the name read back exactly", names)
	}
}

func TestRenameRefusals(t *testing.T) {
	cases := map[string]struct {
		args    []string
		mention string
	}{
		"unknown workstream": {[]string{"support", "infra"}, "support"},
		"name taken":         {[]string{"billing", "onboarding"}, "onboarding"},
		"same name":          {[]string{"billing", "billing"}, "already has that name"},
		"empty name":         {[]string{"billing", ""}, "name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			f.refusedUnchanged(handWritten, "rename", c.mention, c.args...)
		})
	}
}

func TestNamesAndTokensAreRefusedWhenNotUTF8OrEmpty(t *testing.T) {
	for name, args := range map[string][]string{
		"rename to bytes that are not UTF-8":    {"rename", "billing", "bill\xffing"},
		"move to a new unnamed workstream":      {"move", "--new", "tracker:ABC-1", ""},
		"adopt into a new unnamed workstream":   {"adopt", "--new", "tracker:ABC-3", ""},
		"adopt into a new non-UTF-8 workstream": {"adopt", "--new", "tracker:ABC-3", "\xff"},
		"adopt a non-UTF-8 id":                  {"adopt", "tracker:ABC-\xff", "billing"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			f.refusedUnchanged(handWritten, args[0], "", args[1:]...)
		})
	}
}

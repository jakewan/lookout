package main

import (
	"slices"
	"strings"
	"testing"
)

// withSpares is a hand-written file whose first and last workstreams name no
// members, with the comments a person leaves around them: a header under the
// workstreams key, a commented-out member under a workstream, a commented-out
// workstream at dash level, blank separators and a trailer.
const withSpares = `inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h

workstreams:
  # One workstream per epic.
  - name: spare
    title: Left over from the re-seed
    # - tracker:OLD-1
  # - name: retired

  - name: billing
    members:
      - tracker:ABC-1
      # - tracker:ABC-8

  - name: later   # nothing yet
    members: []

# Edited by hand and by lookout.
`

func TestDeleteRemovesAnEmptyWorkstreamAndKeepsEveryOtherComment(t *testing.T) {
	for name, tc := range map[string]struct {
		workstream string
		owned      string
	}{
		"the first, under the header":  {"spare", "  - name: spare\n    title: Left over from the re-seed\n    # - tracker:OLD-1\n"},
		"the last, before the trailer": {"later", "  - name: later   # nothing yet\n    members: []\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", withSpares)
			f.supplied("tracker.json", trackerItems())

			f.mustRun("delete", tc.workstream)

			want := strings.Replace(withSpares, tc.owned, "", 1)
			if got := f.config("personal.yml"); got != want {
				t.Errorf("configuration after delete:\n%s\nwant only the workstream's own lines gone:\n%s", got, want)
			}
			if names := workstreamNames(f.read()); slices.Contains(names, tc.workstream) {
				t.Errorf("workstreams = %v, want %q gone", names, tc.workstream)
			}
			gs := f.gestures("personal")
			if len(gs) != 1 || gs[0].Gesture != "delete" || gs[0].Outcome != "ok" || gs[0].Workstream != tc.workstream {
				t.Errorf("gestures = %+v, want one logged delete of %q", gs, tc.workstream)
			}
		})
	}
}

func TestDeleteRefusesAWorkstreamThatNamesAMember(t *testing.T) {
	completed := strings.Replace(withSpares, "  - name: billing\n", "  - name: billing\n    completed: 2030-06-01\n", 1)
	for name, tc := range map[string]struct {
		config, workstream, says string
	}{
		"a live workstream":      {withSpares, "billing", "remove"},
		"a completed workstream": {completed, "billing", "remove"},
		"an unknown name":        {withSpares, "absent", "no workstream"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", tc.config)
			code, _, stderr := f.run("delete", tc.workstream)
			if code != exitFailure || !strings.Contains(stderr, tc.says) {
				t.Errorf("exit code = %d, stderr = %q, want a refusal saying %q", code, stderr, tc.says)
			}
			if got := f.config("personal.yml"); got != tc.config {
				t.Errorf("configuration changed:\n%s", got)
			}
			if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Gesture != "delete" || gs[0].Outcome != "refused" {
				t.Errorf("gestures = %+v, want one refused delete", gs)
			}
		})
	}
}

func TestTheMemberRefusalNamesTheWaysOut(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", withSpares)
	_, _, stderr := f.run("delete", "billing")
	for _, way := range []string{"remove", "move", "complete"} {
		if !strings.Contains(stderr, way) {
			t.Errorf("stderr = %q, want it to name %q", stderr, way)
		}
	}
}

func TestDeletingTheOnlyWorkstreamLeavesAFileAdoptCanGrow(t *testing.T) {
	f := newFixture(t)
	only := "inputs:\n  - name: tracker\n    kind: supplied\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n  - name: spare\n"
	f.write("personal.yml", only)
	f.supplied("tracker.json", trackerItems())

	f.mustRun("delete", "spare")
	if names := workstreamNames(f.read()); len(names) != 0 {
		t.Errorf("workstreams = %v, want none", names)
	}

	f.mustRun("adopt", "--new", "tracker:ABC-3", "billing")
	if names := workstreamNames(f.read()); !slices.Equal(names, []string{"billing"}) {
		t.Errorf("workstreams = %v, want the one adopt created", names)
	}
}

func TestDeleteTakesAnEmptyCompletedWorkstream(t *testing.T) {
	f := newFixture(t)
	done := strings.Replace(withSpares, "  - name: later   # nothing yet\n", "  - name: later   # nothing yet\n    completed: 2030-06-01\n", 1)
	f.write("personal.yml", done)

	f.mustRun("delete", "later")

	want := strings.Replace(done, "  - name: later   # nothing yet\n    completed: 2030-06-01\n    members: []\n", "", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after delete:\n%s\nwant:\n%s", got, want)
	}
}

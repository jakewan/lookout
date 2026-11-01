package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// titledConfig is handWritten with a title on billing, written the way a person
// would add one by hand, with a comment after it.
var titledConfig = strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    title: Billing export rework   # for the review\n", 1)

func titleOf(t *testing.T, doc readoutDoc, name string) *string {
	t.Helper()
	for _, w := range doc.Workstreams {
		if w.Name == name {
			return w.Title
		}
	}
	t.Fatalf("no workstream %q in %v", name, workstreamNames(doc))
	return nil
}

func TestATitleReachesTheReadoutBesideTheID(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", titledConfig)
	f.supplied("tracker.json", trackerItems())

	doc := f.read()
	if names := workstreamNames(doc); !slices.Equal(names, []string{"billing", "onboarding"}) {
		t.Errorf("workstreams = %v, want ids unchanged by a title", names)
	}
	if got := titleOf(t, doc, "billing"); got == nil || *got != "Billing export rework" {
		t.Errorf("billing title = %v, want the configured title", got)
	}
	if got := titleOf(t, doc, "onboarding"); got != nil {
		t.Errorf("onboarding title = %q, want none for a workstream without one", *got)
	}
}

func TestANewWorkstreamTakesATitle(t *testing.T) {
	for _, gesture := range []string{"adopt", "move"} {
		t.Run(gesture, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			f.supplied("tracker.json", trackerItems())
			item := "tracker:ABC-3"
			before := handWritten
			if gesture == "move" {
				item = "tracker:ABC-2"
				before = strings.Replace(handWritten, "      - tracker:ABC-2\n", "", 1)
			}

			f.mustRun(gesture, "--new", "--title", "Support rota", item, "support")

			want := before + "  - name: support\n    title: Support rota\n    members:\n      - " + item + "\n"
			if got := f.config("personal.yml"); got != want {
				t.Errorf("configuration:\n%s\nwant:\n%s", got, want)
			}
			if got := titleOf(t, f.read(), "support"); got == nil || *got != "Support rota" {
				t.Errorf("support title = %v", got)
			}
			if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Gesture != gesture || !gs[0].New || gs[0].To != "Support rota" {
				t.Errorf("gestures = %+v, want one %s --new recording its title", gs, gesture)
			}
		})
	}
}

func TestATitleWithoutNewIsAUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"adopt", "--title", "Billing", "tracker:ABC-3", "billing"},
		{"move", "--title", "Onboarding", "tracker:ABC-1", "onboarding"},
	} {
		t.Run(args[0], func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			code, _, stderr := f.run(args...)
			if code != exitUsage || !strings.Contains(stderr, "--title applies only with --new") {
				t.Errorf("exit code = %d, stderr = %q, want a usage error pointing at lookout rename", code, stderr)
			}
			if _, err := os.Stat(f.stateDir("personal")); !os.IsNotExist(err) {
				t.Errorf("state directory exists after a usage error: %v", err)
			}
		})
	}
}

func TestRenameSetsTheTitleAndKeepsTheID(t *testing.T) {
	commented := strings.Replace(handWritten, "  - name: billing\n", "  - name: billing   # spring release\n", 1)
	f := newFixture(t)
	f.write("personal.yml", commented)
	f.supplied("tracker.json", trackerItems())

	code, stdout, stderr := f.run("rename", "billing", "Billing export rework")
	if code != 0 || stdout != "" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "rename --id") {
		t.Errorf("stderr = %q, want a note that the id is unchanged and how to change it", stderr)
	}
	want := strings.Replace(commented, "  - name: billing   # spring release\n", "  - name: billing   # spring release\n    title: Billing export rework\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration after rename:\n%s\nwant every other line unchanged:\n%s", got, want)
	}
	doc := f.read()
	if names := workstreamNames(doc); !slices.Equal(names, []string{"billing", "onboarding"}) {
		t.Errorf("workstreams = %v, want the id kept", names)
	}
	if got := titleOf(t, doc, "billing"); got == nil || *got != "Billing export rework" {
		t.Errorf("billing title = %v", got)
	}
	gs := f.gestures("personal")
	if len(gs) != 1 {
		t.Fatalf("gestures = %+v, want exactly one", gs)
	}
	if g := gs[0]; g.Gesture != "retitle" || g.Outcome != "ok" || g.Workstream != "billing" || g.To != "Billing export rework" {
		t.Errorf("gesture = %+v", g)
	}
}

func TestRenameReplacesATitleKeepingItsComment(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", titledConfig)

	f.mustRun("rename", "billing", "q: #2")

	want := strings.Replace(titledConfig, "    title: Billing export rework   # for the review\n", "    title: 'q: #2'   # for the review\n", 1)
	if got := f.config("personal.yml"); got != want {
		t.Errorf("configuration:\n%s\nwant:\n%s", got, want)
	}
	if got := titleOf(t, f.read(), "billing"); got == nil || *got != "q: #2" {
		t.Errorf("billing title = %v, want it read back exactly", got)
	}
}

func TestRenameClearRemovesTheTitle(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", titledConfig)

	f.mustRun("rename", "--clear", "billing")

	if got := f.config("personal.yml"); got != handWritten {
		t.Errorf("configuration:\n%s\nwant the title line gone and nothing else changed:\n%s", got, handWritten)
	}
	if got := titleOf(t, f.read(), "billing"); got != nil {
		t.Errorf("billing title = %q, want none", *got)
	}
	if gs := f.gestures("personal"); len(gs) != 1 || gs[0].Gesture != "retitle" || gs[0].Outcome != "ok" || gs[0].To != "" {
		t.Errorf("gestures = %+v, want one retitle with no title", gs)
	}
}

func TestRenameClearOfAnUntitledWorkstreamChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.mustRun("rename", "--clear", "billing")
	if got := f.config("personal.yml"); got != handWritten {
		t.Errorf("configuration changed:\n%s", got)
	}
}

func TestRenameUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"clear with a title":  {"rename", "--clear", "billing", "Billing"},
		"clear and id":        {"rename", "--clear", "--id", "billing", "ledger"},
		"id with one name":    {"rename", "--id", "billing"},
		"clear with no names": {"rename", "--clear"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			code, _, stderr := f.run(args...)
			if code != exitUsage || !strings.Contains(stderr, "usage:") {
				t.Errorf("exit code = %d, stderr = %q, want a usage error", code, stderr)
			}
		})
	}
}

func TestATitleStartingWithADashFollowsADoubleDash(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.mustRun("rename", "--", "billing", "-WIP-")
	if got := titleOf(t, f.read(), "billing"); got == nil || *got != "-WIP-" {
		t.Errorf("billing title = %v", got)
	}
}

func TestTitleRefusals(t *testing.T) {
	cases := map[string]struct {
		config  string
		args    []string
		gesture string
		mention string
	}{
		"empty title":                {handWritten, []string{"rename", "billing", ""}, "retitle", "title"},
		"blank title":                {handWritten, []string{"rename", "billing", "   "}, "retitle", "title"},
		"title with a line break":    {handWritten, []string{"rename", "billing", "a\nb"}, "retitle", "line break"},
		"title not UTF-8":            {handWritten, []string{"rename", "billing", "bill\xffing"}, "retitle", "UTF-8"},
		"unknown workstream":         {handWritten, []string{"rename", "support", "Support"}, "retitle", "support"},
		"title is another id":        {handWritten, []string{"rename", "billing", "onboarding"}, "retitle", "onboarding"},
		"clear an unknown":           {handWritten, []string{"rename", "--clear", "support"}, "retitle", "support"},
		"new title is another id":    {handWritten, []string{"adopt", "--new", "--title", "billing", "tracker:ABC-3", "support"}, "adopt", "billing"},
		"an id another title uses":   {strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    title: ledger\n", 1), []string{"rename", "--id", "onboarding", "ledger"}, "rename", "ledger"},
		"a new id another title has": {strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    title: support\n", 1), []string{"adopt", "--new", "tracker:ABC-3", "support"}, "adopt", "support"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", c.config)
			code, stdout, stderr := f.run(c.args...)
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, c.mention) {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a refusal mentioning %q", code, stdout, stderr, c.mention)
			}
			// Diagnostics name the command the user typed, not the gesture it logs.
			if !strings.HasPrefix(stderr, "lookout "+c.args[0]+": ") {
				t.Errorf("stderr = %q, want it to start with lookout %s:", stderr, c.args[0])
			}
			if got := f.config("personal.yml"); got != c.config {
				t.Errorf("configuration changed by a refusal:\n%s", got)
			}
			gs := f.gestures("personal")
			if len(gs) != 1 || gs[0].Gesture != c.gesture || gs[0].Outcome != "refused" || gs[0].Reason == "" {
				t.Errorf("gestures = %+v, want one refused %s with its reason", gs, c.gesture)
			}
		})
	}
}

func TestAHandWrittenTitleMustBeUsable(t *testing.T) {
	for name, c := range map[string]struct{ title, mention string }{
		"blank":           {`"   "`, "visible text"},
		"empty":           {`""`, "visible text"},
		"line break":      {`"a\nb"`, "line break"},
		"another's id":    {"onboarding", `is the id of workstream "onboarding"`},
		"not a string":    {"[a, b]", "must be text"},
		"a mapping value": {"{a: b}", "must be text"},
		"a number":        {"42", "must be text"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", strings.Replace(handWritten, "  - name: billing\n", "  - name: billing\n    title: "+c.title+"\n", 1))
			code, stdout, stderr := f.run("read", "--json")
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, `workstream "billing" title`) || !strings.Contains(stderr, c.mention) {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q, want the billing title refused mentioning %q", code, stdout, stderr, c.mention)
			}
		})
	}
}

func TestAWorkstreamMayKeepItsOwnIDAsItsTitle(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.mustRun("rename", "billing", "billing")
	if got := titleOf(t, f.read(), "billing"); got == nil || *got != "billing" {
		t.Errorf("billing title = %v", got)
	}
}

func TestShowLeadsWithTheTitleAndEndsWithTheID(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", titledConfig)
	f.supplied("tracker.json", trackerItems())
	stdout := f.mustRun("show")
	for _, want := range []string{
		"\n  Billing export rework    3h          tracker 2 [billing]\n",
		"\n  onboarding               2d          tracker 1\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
}

func TestShowKeepsALongIDWhole(t *testing.T) {
	long := "onboarding-for-the-spring-2030-cohort"
	for name, config := range map[string]string{
		"titled":   strings.Replace(handWritten, "  - name: onboarding\n", "  - name: "+long+"\n    title: Onboarding\n", 1),
		"untitled": strings.Replace(handWritten, "  - name: onboarding\n", "  - name: "+long+"\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", config)
			f.supplied("tracker.json", trackerItems())
			stdout := f.mustRun("show")
			if !strings.Contains(stdout, "["+long+"]") {
				t.Errorf("stdout =\n%s\nwant the whole id %q in brackets", stdout, long)
			}
		})
	}
}

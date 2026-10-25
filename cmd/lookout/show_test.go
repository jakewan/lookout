package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShowRendersTheSettingForAPerson(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", 2*time.Hour, nil),
		item("ABC-2", 3*time.Hour, nil),
		item("ABC-5", time.Hour, nil),
	}, nil))
	code, stdout, stderr := f.run("show")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	want := `personal - 2030-06-15 12:00 UTC

Workstreams
  billing       2h          tracker 2
  onboarding    --  unread  tracker 0 (1 unread)

1 unplaced
`
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestShowSucceedsOverAnUnhealthyInputAndSaysSo(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	code, stdout, stderr := f.run("show")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"\n! billing ", "  input tracker: error (missing)\n", "  workstream billing: tracker may be incomplete\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
}

func TestShowSelectsANamedExplicitOnlySetting(t *testing.T) {
	for name, route := range map[string]func(f *fixture) []string{
		"--setting": func(*fixture) []string { return []string{"--setting", "personal"} },
		"LOOKOUT_CONFIG": func(f *fixture) []string {
			f.env["LOOKOUT_CONFIG"] = filepath.Join(f.dir, "personal.yml")
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", explicitOnly)
			code, stdout, stderr := f.run(append([]string{"show"}, route(f)...)...)
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr)
			}
			if !strings.HasPrefix(stdout, "personal - ") {
				t.Errorf("stdout = %q, want the personal setting's view", stdout)
			}
		})
	}
}

func TestShowRefusesAnArgument(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	code, stdout, stderr := f.run("show", "billing")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, `unexpected argument "billing"`) {
		t.Errorf("stderr = %q, want it to name the argument", stderr)
	}
}

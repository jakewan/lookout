package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// viewPage runs view with args, requires it to succeed, and returns the path it
// printed and the page written there.
func (f *fixture) viewPage(args ...string) (string, string) {
	f.t.Helper()
	code, stdout, stderr := f.run(append([]string{"view"}, args...)...)
	if code != 0 {
		f.t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if stderr != "" {
		f.t.Errorf("stderr = %q, want nothing", stderr)
	}
	path := strings.TrimSuffix(stdout, "\n")
	if strings.Contains(path, "\n") {
		f.t.Fatalf("stdout = %q, want one line naming the page", stdout)
	}
	page, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatalf("reading the page view printed: %v", err)
	}
	return path, string(page)
}

func TestViewWritesThePageUnderTheCacheDirectoryAndPrintsItsPath(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{
		item("ABC-1", 2*time.Hour, nil),
	}, nil))
	path, page := f.viewPage()
	want := filepath.Join(f.env["HOME"], ".cache", "lookout", "personal", "view.html")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	for _, name := range []string{"billing", "onboarding"} {
		if !strings.Contains(page, name) {
			t.Errorf("page does not name workstream %q", name)
		}
	}
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700|os.ModeDir)
}

func TestViewHonorsXDGCacheHome(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.env["XDG_CACHE_HOME"] = filepath.Join(f.env["HOME"], "elsewhere")
	path, _ := f.viewPage()
	want := filepath.Join(f.env["HOME"], "elsewhere", "lookout", "personal", "view.html")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestViewTightensAnExistingPageAndReplacesIt(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	dir := filepath.Join(f.env["HOME"], ".cache", "lookout", "personal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "view.html")
	if err := os.WriteFile(old, []byte("an older page"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, page := f.viewPage()
	if strings.Contains(page, "an older page") {
		t.Error("the older page was not replaced")
	}
	assertMode(t, path, 0o600)
	assertMode(t, dir, 0o700|os.ModeDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"view.html", "view.json"}) {
		t.Errorf("cache directory holds %v, want only the page and its readout", names)
	}
}

// view.json is the read the page was drawn from, so an agent reading it and a
// person reading the page can tell they are looking at the same read: the
// page names its generated_at exactly.
func TestViewWritesTheReadoutItDrewBesideThePage(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", 2*time.Hour, nil)}, nil))
	path, page := f.viewPage()
	beside := filepath.Join(filepath.Dir(path), "view.json")
	data, err := os.ReadFile(beside)
	if err != nil {
		t.Fatalf("no readout beside the page: %v", err)
	}
	assertMode(t, beside, 0o600)
	var doc struct {
		Schema      string `json:"schema"`
		GeneratedAt string `json:"generated_at"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "readout.v1" || doc.GeneratedAt == "" {
		t.Fatalf("view.json = %.200s, want a readout", data)
	}
	if !strings.Contains(page, doc.GeneratedAt) {
		t.Errorf("the page does not name the read in view.json, %s", doc.GeneratedAt)
	}
}

func TestViewWritesASavedReadoutBesideThePageAsGiven(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	_, saved, _ := f.run("read", "--json")
	file := filepath.Join(t.TempDir(), "saved.json")
	if err := os.WriteFile(file, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := f.viewPage("--readout", file)
	got, err := os.ReadFile(filepath.Join(filepath.Dir(path), "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != saved {
		t.Error("view.json is not the saved readout byte for byte")
	}
}

func TestViewSelectsANamedExplicitOnlySetting(t *testing.T) {
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
			path, _ := f.viewPage(route(f)...)
			if filepath.Base(filepath.Dir(path)) != "personal" {
				t.Errorf("path = %q, want the personal setting's page", path)
			}
		})
	}
}

func TestViewSucceedsOverAnUnhealthyInput(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	_, page := f.viewPage()
	if !strings.Contains(page, "missing") {
		t.Error("page does not give the missing input's reason")
	}
}

func TestViewRendersASavedReadout(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", 2*time.Hour, nil)}, nil))
	_, saved, _ := f.run("read", "--json")
	file := filepath.Join(t.TempDir(), "saved.json")
	if err := os.WriteFile(file, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	// No configuration is read, so none needs to exist.
	if err := os.Remove(filepath.Join(f.dir, "personal.yml")); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() (string, string){
		"file":  func() (string, string) { return f.viewPage("--readout", file) },
		"stdin": func() (string, string) { f.stdin = saved; return f.viewPage("--readout", "-") },
	} {
		t.Run(name, func(t *testing.T) {
			path, page := run()
			if filepath.Base(filepath.Dir(path)) != "personal" {
				t.Errorf("path = %q, want it under the readout's setting", path)
			}
			if !strings.Contains(page, "billing") {
				t.Error("page does not name workstream billing")
			}
		})
	}
}

func TestViewRefusesAReadoutItCannotTrust(t *testing.T) {
	valid := func(t *testing.T) map[string]any {
		f := newFixture(t)
		f.write("personal.yml", handWritten)
		_, saved, _ := f.run("read", "--json")
		var doc map[string]any
		if err := json.Unmarshal([]byte(saved), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"not a readout": {
			edit: func(doc map[string]any) { delete(doc, "workstreams") },
			want: "readout.v1",
		},
		"a setting that would leave the cache directory": {
			edit: func(doc map[string]any) { doc["setting"] = "../../escaped" },
			want: "setting",
		},
		// A workstream's verdicts must agree with its own figures, so no
		// renderer can show a count its lanes contradict.
		"active with nothing read": {
			edit: func(doc map[string]any) { firstWorkstream(doc)["active"] = true },
			want: "readout.v1",
		},
		"behind with nothing read": {
			edit: func(doc map[string]any) { firstWorkstream(doc)["behind"] = true },
			want: "readout.v1",
		},
		"active with no event": {
			edit: func(doc map[string]any) {
				w := firstWorkstream(doc)
				w["last_touched"], w["idle_seconds"], w["active"] = stamp(time.Hour), 3600, true
			},
			want: "readout.v1",
		},
		"not active with an event": {
			edit: func(doc map[string]any) {
				w := firstWorkstream(doc)
				w["last_touched"], w["idle_seconds"] = stamp(time.Hour), 3600
				w["events"] = []any{map[string]any{"input": "tracker", "id": "ABC-1", "at": stamp(time.Hour)}}
			},
			want: "readout.v1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			doc := valid(t)
			tc.edit(doc)
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t)
			file := filepath.Join(t.TempDir(), "saved.json")
			if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := f.run("view", "--readout", file)
			if code != exitFailure {
				t.Errorf("exit code = %d, want %d", code, exitFailure)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, tc.want)
			}
			if _, err := os.Stat(filepath.Join(f.env["HOME"], ".cache")); !os.IsNotExist(err) {
				t.Errorf("a refused readout wrote under the cache directory: %v", err)
			}
		})
	}
}

// firstWorkstream is a decoded readout's first workstream, for a spec to edit.
func firstWorkstream(doc map[string]any) map[string]any {
	return doc["workstreams"].([]any)[0].(map[string]any)
}

func TestViewRefusesBadUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"an argument":              {"view", "billing"},
		"--readout and --setting":  {"view", "--readout", "saved.json", "--setting", "personal"},
		"--readout without a file": {"view", "--readout", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", handWritten)
			code, stdout, stderr := f.run(args...)
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, "usage:") {
				t.Errorf("stderr = %q, want the usage", stderr)
			}
		})
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModePerm | os.ModeDir); got != want {
		t.Errorf("mode of %s = %v, want %v", path, got, want)
	}
}

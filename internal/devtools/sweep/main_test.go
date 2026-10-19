package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo is a throwaway git repository the sweep runs inside. Commits are
// unsigned and global config is ignored, so the specs behave the same on any
// machine and never touch the developer's own git setup.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	t.Chdir(r.dir)
	return r
}

func (r *repo) git(args ...string) {
	r.t.Helper()
	base := []string{"-c", "user.name=Example", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = r.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *repo) write(name, content string) {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(message string, files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		r.write(name, content)
	}
	r.git("add", "--all")
	r.git("commit", "--quiet", "--allow-empty", "-m", message)
}

// setPatterns writes the off-repo pattern file where the sweep looks for it.
func setPatterns(t *testing.T, content string) {
	t.Helper()
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	dir := filepath.Join(config, "lookout-sweep")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "patterns"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sweep(args ...string) (int, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func TestMissingPatternFileWarnsLoudlyAndPasses(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	code, out := sweep()

	if code != exitClean {
		t.Errorf("exit code = %d, want %d", code, exitClean)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, filepath.Join("lookout-sweep", "patterns")) {
		t.Errorf("output = %q, want a loud warning naming the missing pattern file", out)
	}
}

func TestCleanRepositoryPassesSilently(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})
	setPatterns(t, "acme corp\n")

	code, out := sweep()

	if code != exitClean {
		t.Errorf("exit code = %d, want %d; output %q", code, exitClean, out)
	}
	if out != "" {
		t.Errorf("output = %q, want nothing", out)
	}
}

func TestBlankLinesAndCommentsInPatternFileAreIgnored(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})
	// A blank line read as a pattern would match every line of every file.
	setPatterns(t, "# employer names\n\n   \nacme corp\n")

	if code, out := sweep(); code != exitClean {
		t.Errorf("exit code = %d, want %d; output %q", code, exitClean, out)
	}
}

func TestMatchesAreReportedAndFailThePush(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(r *repo)
		wantName string
	}{
		{
			name: "tracked file, matched case-insensitively",
			setup: func(r *repo) {
				r.commit("initial", map[string]string{"docs/notes.md": "line one\nworks at ACME Corp\n"})
			},
			wantName: "docs/notes.md:2",
		},
		{
			name: "unpushed commit message",
			setup: func(r *repo) {
				r.commit("initial", map[string]string{"README.md": "hello\n"})
				r.commit("fix the Acme Corp import", nil)
			},
			wantName: "commit",
		},
		{
			name: "content added then removed before pushing",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"notes.md": "acme corp\n"})
				r.commit("scrub notes", map[string]string{"notes.md": "clean\n"})
			},
			wantName: "commit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(newRepo(t))
			setPatterns(t, "acme corp\n")

			code, out := sweep()

			if code != exitMatch {
				t.Errorf("exit code = %d, want %d; output %q", code, exitMatch, out)
			}
			if !strings.Contains(out, tt.wantName) {
				t.Errorf("output = %q, want it to locate the match (%q)", out, tt.wantName)
			}
		})
	}
}

// TestPathsAreSwept covers names as well as contents: pushing a file publishes
// its path, so a clean file under a denylisted name leaks the name.
func TestPathsAreSwept(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(r *repo)
		wantName string
	}{
		{
			name: "tracked path",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"docs/Acme-Corp-rollout.md": "clean\n"})
			},
			wantName: "path docs/Acme-Corp-rollout.md",
		},
		{
			name: "path renamed away before pushing",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"acme-corp.md": "clean\n"})
				r.git("mv", "acme-corp.md", "notes.md")
				r.commit("rename notes", nil)
			},
			wantName: "path",
		},
		{
			name: "tracked path deleted from the working tree",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"acme-corp.md": "clean\n"})
				if err := os.Remove(filepath.Join(r.dir, "acme-corp.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantName: "path acme-corp.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(newRepo(t))
			setPatterns(t, "acme-corp\n")

			code, out := sweep()

			if code != exitMatch {
				t.Errorf("exit code = %d, want %d; output %q", code, exitMatch, out)
			}
			if !strings.Contains(out, tt.wantName) {
				t.Errorf("output = %q, want it to locate the match (%q)", out, tt.wantName)
			}
		})
	}
}

// TestRenamingAPublishedPathAwayIsAllowed pins the direction of the history
// path check: the push that removes a published leak must not be the one the
// leak refuses.
func TestRenamingAPublishedPathAwayIsAllowed(t *testing.T) {
	r := newRepo(t)
	r.commit("add notes", map[string]string{"acme-corp.md": "clean\n"})
	remote := t.TempDir()
	r.git("init", "--quiet", "--bare", remote)
	r.git("remote", "add", "origin", remote)
	r.git("push", "--quiet", "origin", "main")
	r.git("mv", "acme-corp.md", "notes.md")
	r.commit("rename notes", nil)
	setPatterns(t, "acme-corp\n")

	if code, out := sweep(); code != exitClean {
		t.Errorf("exit code = %d, want %d; output %q", code, exitClean, out)
	}
}

func TestCommitsAlreadyOnARemoteAreNotRescanned(t *testing.T) {
	r := newRepo(t)
	r.commit("mention acme corp", map[string]string{"README.md": "hello\n"})
	remote := t.TempDir()
	r.git("init", "--quiet", "--bare", remote)
	r.git("remote", "add", "origin", remote)
	r.git("push", "--quiet", "origin", "main")
	setPatterns(t, "acme corp\n")

	if code, out := sweep(); code != exitClean {
		t.Errorf("exit code = %d, want %d: a published commit cannot be unpublished by failing a later push; output %q", code, exitClean, out)
	}
}

func TestFileModeScansOnlyTheNamedFiles(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", map[string]string{"notes.md": "acme corp\n"})
	setPatterns(t, "acme corp\n")
	clean := filepath.Join(t.TempDir(), "clean-body.md")
	leaky := filepath.Join(t.TempDir(), "leaky-body.md")
	if err := os.WriteFile(clean, []byte("Closes #1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaky, []byte("Overview\nfor the Acme Corp rollout\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, out := sweep("--file", clean); code != exitClean {
		t.Errorf("clean body: exit code = %d, want %d; output %q", code, exitClean, out)
	}
	code, out := sweep("--file", leaky)
	if code != exitMatch {
		t.Errorf("leaky body: exit code = %d, want %d; output %q", code, exitMatch, out)
	}
	if !strings.Contains(out, leaky+":2") {
		t.Errorf("leaky body: output = %q, want it to locate the match", out)
	}
}

func TestPatternsAreNeverEchoed(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"notes.md": "acme corp\n"})
	setPatterns(t, "acme corp\n")

	_, out := sweep()

	if strings.Contains(strings.ToLower(out), "acme corp") {
		t.Errorf("output = %q: it repeats the matched term, which would carry it into CI logs or pasted output", out)
	}
}

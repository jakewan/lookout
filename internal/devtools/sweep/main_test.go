package main

import (
	"bytes"
	"context"
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
	clearRepositoryEnvironment(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	t.Chdir(r.dir)
	return r
}

// clearRepositoryEnvironment unsets the variables git exports to its hooks
// (githooks(5): "GIT_DIR, GIT_WORK_TREE, etc., are exported"). The pre-push
// hook runs this suite, and with them inherited every git command below would
// act on the repository being pushed instead of the throwaway one. The list
// comes from git itself, so a variable a later git adds is covered too.
func clearRepositoryEnvironment(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Fatalf("listing git's repository variables: %v", err)
	}
	for name := range strings.FieldsSeq(string(out)) {
		t.Setenv(name, "") // registers restoration at cleanup
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	base := []string{"-c", "user.name=Example", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
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

// remote adds a bare repository as a remote and pushes the given branches to
// it, so their commits count as published.
func (r *repo) remote(name string, branches ...string) {
	r.t.Helper()
	dir := r.t.TempDir()
	r.git("init", "--quiet", "--bare", dir)
	r.git("remote", "add", name, dir)
	for _, b := range branches {
		r.git("push", "--quiet", name, b)
	}
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

func sweepWithInput(stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func sweep(args ...string) (int, string) {
	code, stdout, stderr := sweepWithInput("", args...)
	return code, stdout + stderr
}

func expectRefused(t *testing.T, code int, out, location string) {
	t.Helper()
	if code != exitMatch {
		t.Errorf("exit code = %d, want %d; output %q", code, exitMatch, out)
	}
	if !strings.Contains(out, "sweep: "+location) {
		t.Errorf("output = %q, want a line locating the match at %q", out, location)
	}
}

func expectClean(t *testing.T, code int, out string) {
	t.Helper()
	if code != exitClean {
		t.Errorf("exit code = %d, want %d; output %q", code, exitClean, out)
	}
}

func TestHelperIgnoresInheritedRepositoryEnvironment(t *testing.T) {
	decoy := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", decoy).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))

	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})

	out, err := exec.Command("git", "--git-dir", filepath.Join(decoy, ".git"), "rev-list", "--all", "--count").Output()
	if err != nil {
		t.Fatalf("counting the decoy's commits: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "0" {
		t.Fatalf("the decoy repository named by GIT_DIR gained %s commit(s); the helper must act only on its throwaway repository", got)
	}
}

func TestMissingPatternFileWarnsLoudlyAndPasses(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	code, out := sweep()

	expectClean(t, code, out)
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, filepath.Join("lookout-sweep", "patterns")) {
		t.Errorf("output = %q, want a loud warning naming the missing pattern file", out)
	}
}

func TestPatternFileWithNoTermsWarnsLoudlyAndPasses(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "acme corp\n"})
	setPatterns(t, "# nothing yet\n\n")

	code, out := sweep()

	expectClean(t, code, out)
	if !strings.Contains(out, "WARNING") {
		t.Errorf("output = %q, want a loud warning: a denylist with no terms checks nothing", out)
	}
}

func TestCleanRepositoryPassesSilently(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})
	setPatterns(t, "acme corp\n")

	code, stdout, stderr := sweepWithInput("")

	expectClean(t, code, stdout+stderr)
	if stdout != "" || stderr != "" {
		t.Errorf("stdout = %q, stderr = %q; want nothing on either", stdout, stderr)
	}
}

func TestBlankLinesAndCommentsInPatternFileAreIgnored(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})
	// A blank line read as a pattern would match every line of every file.
	setPatterns(t, "# employer names\n\n   \nacme corp\n")

	code, out := sweep()
	expectClean(t, code, out)
}

func TestContentMatchesAreRefused(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(r *repo)
		location string
	}{
		{
			name: "tracked file, matched case-insensitively",
			setup: func(r *repo) {
				r.commit("initial", map[string]string{"docs/notes.md": "line one\nworks at ACME Corp\n"})
			},
			location: "docs/notes.md:2",
		},
		{
			name: "unpushed commit message",
			setup: func(r *repo) {
				r.commit("initial", map[string]string{"README.md": "hello\n"})
				r.commit("fix the Acme Corp import", nil)
			},
			location: "commit ",
		},
		{
			name: "content added then removed before pushing",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"notes.md": "acme corp\n"})
				r.commit("scrub notes", map[string]string{"notes.md": "clean\n"})
			},
			location: "commit ",
		},
		{
			name: "message line after one that looks like a diff header",
			setup: func(r *repo) {
				r.commit("initial", map[string]string{"README.md": "hello\n"})
				r.commit("quote a patch\n\ndiff --git a/x b/x\nfor acme corp\n", nil)
			},
			location: "commit ",
		},
		{
			name: "added line whose content begins with ++, removed before pushing",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"notes.md": "++acme corp\n"})
				r.commit("scrub notes", map[string]string{"notes.md": "clean\n"})
			},
			location: "commit ",
		},
		{
			name: "tracked symlink whose target names the term",
			setup: func(r *repo) {
				if err := os.Symlink("acme-corp/notes.md", filepath.Join(r.dir, "link")); err != nil {
					t.Fatal(err)
				}
				r.commit("add link", nil)
				r.remote("origin", "main")
			},
			location: "link:1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(newRepo(t))
			setPatterns(t, "acme corp\nacme-corp\n")

			code, out := sweep()
			expectRefused(t, code, out, tt.location)
		})
	}
}

// TestCommitIdentityIsSwept covers the names and email domains a commit
// publishes. A commit made under a work identity publishes the employer's
// domain even when every line it adds is clean.
func TestCommitIdentityIsSwept(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(r *repo)
		location string
	}{
		{
			name: "author email domain",
			setup: func(r *repo) {
				r.write("README.md", "hello\n")
				r.git("add", "README.md")
				r.git("commit", "--quiet", "-m", "initial", "--author", "Example <dev@acme-corp.example>")
			},
			location: "commit ",
		},
		{
			name: "committer name",
			setup: func(r *repo) {
				r.write("README.md", "hello\n")
				r.git("add", "README.md")
				r.git("-c", "user.name=Acme-Corp Bot", "commit", "--quiet", "-m", "initial")
			},
			location: "commit ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(newRepo(t))
			setPatterns(t, "acme-corp\n")

			code, out := sweep()
			expectRefused(t, code, out, tt.location)
			if !strings.Contains(out, "author") && !strings.Contains(out, "committer") {
				t.Errorf("output = %q, want it to say the match is in the commit's identity", out)
			}
		})
	}
}

// TestEmailLocalPartsAreNotSwept pins the other half of the identity check:
// git authorship publishes the author's own address by design, so a
// denylist term guarding that address in prose must not refuse every commit.
func TestEmailLocalPartsAreNotSwept(t *testing.T) {
	r := newRepo(t)
	r.write("README.md", "hello\n")
	r.git("add", "README.md")
	r.git("commit", "--quiet", "-m", "initial", "--author", "Example <devlocal@example.com>")
	setPatterns(t, "devlocal@\n")

	code, out := sweep()
	expectClean(t, code, out)
}

func TestPrePushSweepsATaggersIdentity(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", map[string]string{"README.md": "hello\n"})
	r.remote("origin", "main")
	r.git("-c", "user.email=release@acme-corp.example", "tag", "-a", "v0.1.0", "-m", "first release")
	sha := r.git("rev-parse", "v0.1.0")
	setPatterns(t, "acme-corp\n")

	code, stdout, stderr := sweepWithInput("refs/tags/v0.1.0 "+sha+" refs/tags/v0.1.0 "+strings.Repeat("0", 40)+"\n", "--pre-push", "origin")
	expectRefused(t, code, stdout+stderr, "tag refs/tags/v0.1.0 tagger")
}

// TestMergedTagHeadersAreSwept covers the copy of a signed tag a merge commit
// embeds in its mergetag header: the tag's name, tagger and message are
// published inside the commit itself, and none of them need appear in the
// commit's message. git writes the header only when merging a signed tag, and
// a spec has no key to sign with, so the commit object is written directly in
// the shape git gives it (continuation lines indented by one space).
func TestMergedTagHeadersAreSwept(t *testing.T) {
	tests := []struct {
		name string
		tag  string
	}{
		{"tag name", " type commit\n tag acme-corp-v1\n tagger Example <dev@example.com> 0 +0000\n \n first release\n"},
		{"tagger", " type commit\n tag v1\n tagger Example <dev@acme-corp.example> 0 +0000\n \n first release\n"},
		{"tag message", " type commit\n tag v1\n tagger Example <dev@example.com> 0 +0000\n \n cut for acme-corp\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRepo(t)
			r.commit("initial", map[string]string{"README.md": "hello\n"})
			r.remote("origin", "main")
			parent := r.git("rev-parse", "HEAD")
			raw := "tree " + r.git("rev-parse", "HEAD^{tree}") + "\n" +
				"parent " + parent + "\n" +
				"author Example <dev@example.com> 0 +0000\n" +
				"committer Example <dev@example.com> 0 +0000\n" +
				"mergetag object " + parent + "\n" + tt.tag +
				"\nMerge tag 'v1'\n"
			cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
			cmd.Dir = r.dir
			cmd.Stdin = strings.NewReader(raw)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("writing the commit object: %v", err)
			}
			r.git("update-ref", "refs/heads/main", strings.TrimSpace(string(out)))
			setPatterns(t, "acme-corp\n")

			code, sweepOut := sweep()
			expectRefused(t, code, sweepOut, "commit ")
			if !strings.Contains(sweepOut, "mergetag") {
				t.Errorf("output = %q, want it to say the match is in the merged tag", sweepOut)
			}
		})
	}
}

// TestMergeResolutionsAreSwept covers the one place a merge commit carries
// content of its own: lines written while resolving a conflict.
func TestMergeResolutionsAreSwept(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", map[string]string{"notes.md": "base\n"})
	r.git("checkout", "--quiet", "-b", "topic")
	r.commit("topic edit", map[string]string{"notes.md": "topic\n"})
	r.git("checkout", "--quiet", "main")
	r.commit("main edit", map[string]string{"notes.md": "main\n"})
	r.remote("origin", "main", "topic")
	cmd := exec.Command("git", "-c", "user.name=Example", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false", "merge", "--quiet", "topic")
	cmd.Dir = r.dir
	if cmd.Run() == nil {
		t.Fatal("the merge succeeded; the spec needs it to stop on a conflict")
	}
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("no merge in progress after the conflicting merge: %v", err)
	}
	r.write("notes.md", "acme corp\n")
	r.git("add", "notes.md")
	r.git("commit", "--quiet", "-m", "merge topic")
	if parents := strings.Fields(r.git("log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("the resolution commit has %d parent(s), want a merge commit", len(parents))
	}
	r.commit("scrub notes", map[string]string{"notes.md": "clean\n"})
	setPatterns(t, "acme corp\n")

	code, out := sweep()
	expectRefused(t, code, out, "commit ")
}

// TestPathsAreSwept covers names as well as contents: pushing a file publishes
// its path, so a clean file under a denylisted name leaks the name.
func TestPathsAreSwept(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(r *repo)
		location string
	}{
		{
			// Already on the remote, so only the tree check can see it.
			name: "tracked path",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"docs/Acme-Corp-rollout.md": "clean\n"})
				r.remote("origin", "main")
			},
			location: "path docs/Acme-Corp-rollout.md",
		},
		{
			name: "tracked path deleted from the working tree",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"acme-corp.md": "clean\n"})
				r.remote("origin", "main")
				if err := os.Remove(filepath.Join(r.dir, "acme-corp.md")); err != nil {
					t.Fatal(err)
				}
			},
			location: "path acme-corp.md",
		},
		{
			name: "path renamed away before pushing",
			setup: func(r *repo) {
				r.commit("add notes", map[string]string{"acme-corp.md": "clean\n"})
				r.git("mv", "acme-corp.md", "notes.md")
				r.commit("rename notes", nil)
			},
			location: "commit ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(newRepo(t))
			setPatterns(t, "acme-corp\n")

			code, out := sweep()
			expectRefused(t, code, out, tt.location)
		})
	}
}

// TestRenamingAPublishedPathAwayIsAllowed pins the direction of the history
// path check: the push that removes a published leak must not be the one the
// leak refuses.
func TestRenamingAPublishedPathAwayIsAllowed(t *testing.T) {
	r := newRepo(t)
	r.commit("add notes", map[string]string{"acme-corp.md": "clean\n"})
	r.remote("origin", "main")
	r.git("mv", "acme-corp.md", "notes.md")
	r.commit("rename notes", nil)
	setPatterns(t, "acme-corp\n")

	code, out := sweep()
	expectClean(t, code, out)
}

func TestCommitsAlreadyOnARemoteAreNotRescanned(t *testing.T) {
	r := newRepo(t)
	r.commit("mention acme corp", map[string]string{"README.md": "hello\n"})
	r.remote("origin", "main")
	setPatterns(t, "acme corp\n")

	code, out := sweep()
	expectClean(t, code, out)
}

func TestRunsFromASubdirectoryOverTheWholeTree(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", map[string]string{"docs/readme.md": "hello\n", "notes.md": "acme corp\n"})
	r.remote("origin", "main")
	t.Chdir(filepath.Join(r.dir, "docs"))
	setPatterns(t, "acme corp\n")

	code, out := sweep()
	expectRefused(t, code, out, "notes.md:1")
}

func TestRefusalSaysALaterCommitCannotClearIt(t *testing.T) {
	r := newRepo(t)
	r.commit("add notes", map[string]string{"notes.md": "acme corp\n"})
	r.commit("scrub notes", map[string]string{"notes.md": "clean\n"})
	setPatterns(t, "acme corp\n")

	_, out := sweep()

	if !strings.Contains(out, "rewrite") {
		t.Errorf("output = %q, want it to say the unpushed commit must be rewritten, since a new commit cannot clear the match", out)
	}
}

func TestPrePushSweepsTheRefsBeingPushed(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(r *repo) string // returns the hook's stdin
		location string
	}{
		{
			name: "a branch other than HEAD",
			setup: func(r *repo) string {
				r.commit("initial", map[string]string{"README.md": "hello\n"})
				r.remote("origin", "main")
				r.git("checkout", "--quiet", "-b", "other")
				r.commit("mention acme corp", nil)
				sha := r.git("rev-parse", "HEAD")
				r.git("checkout", "--quiet", "main")
				return "refs/heads/other " + sha + " refs/heads/other " + strings.Repeat("0", 40) + "\n"
			},
			location: "commit ",
		},
		{
			name: "the name of the ref being pushed",
			setup: func(r *repo) string {
				r.commit("initial", map[string]string{"README.md": "hello\n"})
				r.remote("origin", "main")
				r.git("branch", "acme-corp-fix")
				sha := r.git("rev-parse", "HEAD")
				return "refs/heads/acme-corp-fix " + sha + " refs/heads/acme-corp-fix " + strings.Repeat("0", 40) + "\n"
			},
			location: "ref refs/heads/acme-corp-fix",
		},
		{
			name: "an annotated tag's message",
			setup: func(r *repo) string {
				r.commit("initial", map[string]string{"README.md": "hello\n"})
				r.remote("origin", "main")
				r.git("tag", "-a", "v0.1.0", "-m", "for acme corp")
				sha := r.git("rev-parse", "v0.1.0")
				return "refs/tags/v0.1.0 " + sha + " refs/tags/v0.1.0 " + strings.Repeat("0", 40) + "\n"
			},
			location: "tag refs/tags/v0.1.0",
		},
		{
			name: "commits another remote holds but the destination does not",
			setup: func(r *repo) string {
				r.commit("mention acme corp", map[string]string{"README.md": "hello\n"})
				r.remote("backup", "main")
				r.remote("origin")
				sha := r.git("rev-parse", "HEAD")
				return "refs/heads/main " + sha + " refs/heads/main " + strings.Repeat("0", 40) + "\n"
			},
			location: "commit ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdin := tt.setup(newRepo(t))
			setPatterns(t, "acme corp\nacme-corp\n")

			code, stdout, stderr := sweepWithInput(stdin, "--pre-push", "origin")
			expectRefused(t, code, stdout+stderr, tt.location)
		})
	}
}

// TestBinaryContentIsSweptAsBytes covers files a line scan cannot read: text
// embedded in a binary file is published like any other.
func TestBinaryContentIsSweptAsBytes(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(r *repo)
		location string
	}{
		{
			name: "tracked binary file",
			setup: func(r *repo) {
				r.commit("add blob", map[string]string{"data.bin": "header\x00ACME Corp\n"})
				r.remote("origin", "main")
			},
			location: "data.bin (binary)",
		},
		{
			name: "binary file added then removed before pushing",
			setup: func(r *repo) {
				r.commit("add blob", map[string]string{"data.bin": "header\x00ACME Corp\n"})
				r.git("rm", "--quiet", "data.bin")
				r.commit("remove blob", nil)
			},
			location: "commit ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(newRepo(t))
			setPatterns(t, "acme corp\n")

			code, out := sweep()
			expectRefused(t, code, out, tt.location)
		})
	}
}

func TestPrePushSweepsAnAnnotatedTagsOwnName(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", map[string]string{"README.md": "hello\n"})
	r.remote("origin", "main")
	r.git("tag", "-a", "acme-corp-release", "-m", "first release")
	sha := r.git("rev-parse", "acme-corp-release")
	r.git("update-ref", "refs/tags/release", sha)
	r.git("tag", "-d", "acme-corp-release")
	setPatterns(t, "acme-corp\n")

	code, stdout, stderr := sweepWithInput("refs/tags/release "+sha+" refs/tags/release "+strings.Repeat("0", 40)+"\n", "--pre-push", "origin")
	expectRefused(t, code, stdout+stderr, "tag refs/tags/release name")
}

// TestTrackingRefsCountOnlyForTheURLTheyWereFetchedFrom pins where the
// "already published" evidence comes from: a remote's tracking refs describe
// its fetch URL, so they say nothing about a different URL being pushed to.
func TestTrackingRefsCountOnlyForTheURLTheyWereFetchedFrom(t *testing.T) {
	r := newRepo(t)
	r.commit("mention acme corp", map[string]string{"README.md": "hello\n"})
	r.remote("origin", "main")
	fetchURL := r.git("remote", "get-url", "origin")
	public := t.TempDir()
	r.git("init", "--quiet", "--bare", public)
	sha := r.git("rev-parse", "HEAD")
	stdin := "refs/heads/main " + sha + " refs/heads/main " + strings.Repeat("0", 40) + "\n"
	setPatterns(t, "acme corp\n")

	code, stdout, stderr := sweepWithInput(stdin, "--pre-push", "origin", "--url", fetchURL)
	expectClean(t, code, stdout+stderr)

	code, stdout, stderr = sweepWithInput(stdin, "--pre-push", "origin", "--url", public)
	expectRefused(t, code, stdout+stderr, "commit ")
}

func TestPrePushIgnoresDeletions(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", map[string]string{"README.md": "hello\n"})
	r.remote("origin", "main")
	setPatterns(t, "acme corp\n")
	zero := strings.Repeat("0", 40)

	code, stdout, stderr := sweepWithInput("(delete) "+zero+" refs/heads/old "+r.git("rev-parse", "HEAD")+"\n", "--pre-push", "origin")
	expectClean(t, code, stdout+stderr)
}

func TestFileModeScansOnlyTheNamedFiles(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", map[string]string{"notes.md": "acme corp\n"})
	setPatterns(t, "acme corp\n")
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean-body.md")
	leaky := filepath.Join(dir, "leaky-body.md")
	if err := os.WriteFile(clean, []byte("Closes #1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaky, []byte("Overview\nfor the Acme Corp rollout\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out := sweep(clean)
	expectClean(t, code, out)

	code, out = sweep(clean, leaky)
	expectRefused(t, code, out, leaky+":2")
}

func TestFileModeRefusesABinaryFile(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"README.md": "hello\n"})
	setPatterns(t, "acme corp\n")
	body := filepath.Join(t.TempDir(), "body.md")
	// UTF-16 text reads as binary to a byte scan, and skipping it would pass a
	// body that was never actually checked.
	if err := os.WriteFile(body, []byte("a\x00c\x00m\x00e\x00"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, out := sweep(body); code != exitError {
		t.Errorf("exit code = %d, want %d; output %q", code, exitError, out)
	}
}

func TestContentMatchesNeverEchoTheTerm(t *testing.T) {
	newRepo(t).commit("initial", map[string]string{"notes.md": "acme corp\n"})
	setPatterns(t, "acme corp\n")

	_, out := sweep()

	if strings.Contains(strings.ToLower(out), "acme corp") {
		t.Errorf("output = %q: it repeats the matched term from a file's contents", out)
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withGit lets the invocation find git, and keeps the machine's own git
// configuration from reaching it.
func (f *fixture) withGit() *fixture {
	f.t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		f.t.Skip("git is not installed")
	}
	f.env["PATH"] = os.Getenv("PATH")
	f.env["GIT_CONFIG_GLOBAL"] = os.DevNull
	f.env["GIT_CONFIG_NOSYSTEM"] = "1"
	return f
}

// home is the fixture's home directory, where ~/ in a repos entry points.
func (f *fixture) home(rel string) string { return filepath.Join(f.env["HOME"], rel) }

// git runs git in dir for building fixtures. Its environment is set in full
// rather than inherited, so a hook running these specs, which exports its own
// repository's variables, cannot redirect it.
func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	return f.gitAt(dir, time.Hour, args...)
}

func (f *fixture) gitAt(dir string, ago time.Duration, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + f.env["HOME"],
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_AUTHOR_DATE=" + stamp(ago), "GIT_COMMITTER_DATE=" + stamp(ago),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitStdin is git with input on stdin, for fixtures plumbing builds.
func (f *fixture) gitStdin(dir, input string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.env["HOME"], "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo creates a repository at ~/rel whose origin is origin, or which has no
// remote when origin is empty.
func (f *fixture) repo(rel, origin string) string {
	f.t.Helper()
	dir := f.home(rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "init", "--quiet", "--initial-branch=main")
	if origin != "" {
		f.git(dir, "remote", "add", "origin", origin)
	}
	return dir
}

// branch points refs/heads/name at a new commit made ago, without touching any
// checkout.
func (f *fixture) branch(dir, name string, ago time.Duration) {
	f.t.Helper()
	tree := f.git(dir, "mktree")
	commit := f.gitAt(dir, ago, "commit-tree", tree, "-m", name)
	f.git(dir, "update-ref", "refs/heads/"+name, commit)
}

const fencedCode = `
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: ["~/code/*"]
workstreams: []
`

func TestReadSurfacesLocalBranchesOfFencedRepos(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.branch(app, "main", 100*time.Hour)
	f.branch(app, "feature/x", 2*time.Hour)

	doc := f.read()

	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"code:alice/app:feature/x"}) {
		t.Fatalf("unplaced = %v, want the feature branch and never the default branch", ids)
	}
	it := doc.Unplaced[0].Item
	if it["title"] != "feature/x" || it["state"] != "local" || it["last_touched"] != stamp(2*time.Hour) {
		t.Errorf("item = %v", it)
	}
	if attrs, _ := it["attrs"].(map[string]any); attrs["repo"] != app || attrs["branch"] != "feature/x" {
		t.Errorf("attrs = %v, want the repository's path and the branch", it["attrs"])
	}
	if links, _ := it["links"].([]any); len(links) != 0 {
		t.Errorf("links = %v, want none: a remote URL can carry credentials", links)
	}
	h := doc.health(t, "code")
	if h.Status != "ok" || len(h.Reasons) != 0 || h.RecordCount != 1 || h.FetchedAt == nil || *h.FetchedAt != now.Format(time.RFC3339) {
		t.Errorf("health = %+v, want ok, read now, with one record", h)
	}
}

// TestReadKeepsReposOutsideTheOwnerFence is the owner fence: a repository
// whose origin belongs to an owner, or a host, the setting does not list never
// reaches the readout, and is counted rather than named.
func TestReadKeepsReposOutsideTheOwnerFence(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", `
forge_owners: [ALICE]
inputs:
  - name: code
    kind: git
    repos: ["~/code/*"]
workstreams: []
`)
	for rel, origin := range map[string]string{
		"code/app":            "git@github.com:alice/app.git",
		"code/mallory-tool":   "https://github.com/mallory/tool.git",
		"code/mallory-notes":  "",
		"code/mallory-mirror": "git@gitlab.example:alice/mirror.git",
	} {
		f.branch(f.repo(rel, origin), "feat", time.Hour)
	}
	// A worktree of a fenced-out repository is the same repository, counted once.
	f.git(f.home("code/mallory-tool"), "branch", "other", "feat")
	f.git(f.home("code/mallory-tool"), "worktree", "add", "--quiet", f.home("code/mallory-tool-wt"), "other")

	code, stdout, stderr := f.run("read", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	doc := f.read()

	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"code:alice/app:feat"}) {
		t.Errorf("unplaced = %v, want only the github.com repository alice owns", ids)
	}
	h := doc.health(t, "code")
	if h.Status != "ok" || h.ExcludedCount == nil || *h.ExcludedCount != 3 {
		t.Errorf("health = %+v, want ok with three repositories excluded", h)
	}
	for _, leak := range []string{"mallory", "gitlab.example"} {
		if strings.Contains(stdout, leak) {
			t.Errorf("the readout names %q, which the fence kept out:\n%s", leak, stdout)
		}
	}
}

func TestReadHidesEachRepositorysDefaultBranch(t *testing.T) {
	cases := []struct {
		name    string
		build   func(f *fixture, dir string)
		visible []string
	}{
		{name: "origin/HEAD names it", build: func(f *fixture, dir string) {
			f.git(dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
			f.branch(dir, "trunk", time.Hour)
			f.branch(dir, "main", time.Hour)
		}, visible: []string{"code:alice/app:main", "code:alice/app:x"}},
		{name: "main without origin/HEAD", build: func(f *fixture, dir string) {
			f.branch(dir, "main", time.Hour)
		}, visible: []string{"code:alice/app:x"}},
		{name: "master without main", build: func(f *fixture, dir string) {
			f.branch(dir, "master", time.Hour)
		}, visible: []string{"code:alice/app:x"}},
		{name: "neither", build: func(f *fixture, dir string) {
			f.branch(dir, "develop", time.Hour)
		}, visible: []string{"code:alice/app:develop", "code:alice/app:x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t).withGit()
			f.write("personal.yml", fencedCode)
			dir := f.repo("code/app", "git@github.com:alice/app.git")
			f.branch(dir, "x", time.Hour)
			tc.build(f, dir)

			if ids := unplacedIDs(f.read()); !slices.Equal(ids, tc.visible) {
				t.Errorf("unplaced = %v, want %v", ids, tc.visible)
			}
		})
	}
}

// TestReadOffersOnlyCheckedOutBranchesOfABareRepository pins the bare-clone
// layout: cloning bare copies every branch of the remote into the local
// branches, so only those a worktree has checked out are the user's own work.
func TestReadOffersOnlyCheckedOutBranchesOfABareRepository(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", `
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: ["~/code/app.git"]
workstreams: []
`)
	dir := f.home("code/app.git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(dir, "init", "--quiet", "--bare", "--initial-branch=develop")
	f.git(dir, "remote", "add", "origin", "git@github.com:alice/app.git")
	for _, b := range []string{"develop", "colleague/work", "mine"} {
		f.branch(dir, b, time.Hour)
	}
	f.git(dir, "worktree", "add", "--quiet", f.home("wt/develop"), "develop")
	f.git(dir, "worktree", "add", "--quiet", f.home("wt/mine"), "mine")

	if ids := unplacedIDs(f.read()); !slices.Equal(ids, []string{"code:alice/app:mine"}) {
		t.Errorf("unplaced = %v, want only the checked-out branch that is not the default", ids)
	}
}

func TestReadRecordsWhereEachBranchIsCheckedOut(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	dir := f.repo("code/app", "git@github.com:alice/app.git")
	for _, b := range []string{"main", "here", "there", "nowhere"} {
		f.branch(dir, b, time.Hour)
	}
	f.git(dir, "symbolic-ref", "HEAD", "refs/heads/here")
	there := f.home("wt/there")
	f.git(dir, "worktree", "add", "--quiet", there, "there")
	f.git(dir, "worktree", "add", "--quiet", "--detach", f.home("wt/detached"), "nowhere")
	f.branch(dir, "pruned", time.Hour)
	f.git(dir, "worktree", "add", "--quiet", f.home("wt/pruned"), "pruned")
	if err := os.RemoveAll(f.home("wt/pruned")); err != nil {
		t.Fatal(err)
	}
	f.repo("code/empty", "git@github.com:alice/empty.git")

	doc := f.read()

	want := map[string][]string{
		"alice/app:here":    {dir},
		"alice/app:there":   {there},
		"alice/app:nowhere": {},
		"alice/app:pruned":  {},
	}
	got := map[string][]string{}
	for _, u := range doc.Unplaced {
		attrs, _ := u.Item["attrs"].(map[string]any)
		paths := []string{}
		for _, p := range attrs["worktrees"].([]any) {
			resolved, err := filepath.EvalSymlinks(p.(string))
			if err != nil {
				t.Fatal(err)
			}
			paths = append(paths, resolved)
		}
		got[u.Item["id"].(string)] = paths
	}
	for id, paths := range want {
		for i, p := range paths {
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				t.Fatal(err)
			}
			paths[i] = resolved
		}
		if !slices.Equal(got[id], paths) {
			t.Errorf("%s worktrees = %v, want %v", id, got[id], paths)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unplaced = %v, want %d branches: a detached or deleted worktree and an empty repository add none", got, len(want))
	}
	if h := doc.health(t, "code"); h.Status != "ok" {
		t.Errorf("health = %+v, want ok: a repository with no commits is not a failure", h)
	}
}

func TestReadIgnoresConfiguredBranchPatterns(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", `
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: ["~/code/*"]
    ignore_branches: ["dependabot/*"]
workstreams: []
`)
	dir := f.repo("code/app", "git@github.com:alice/app.git")
	f.branch(dir, "dependabot/x", time.Hour)
	f.branch(dir, "dependabot/a/b", time.Hour)

	if ids := unplacedIDs(f.read()); !slices.Equal(ids, []string{"code:alice/app:dependabot/a/b"}) {
		t.Errorf("unplaced = %v, want only the branch * does not reach: it stops at a slash", ids)
	}
}

func TestReadGivesOneItemPerRemoteBranch(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	older := f.repo("code/app", "git@github.com:alice/app.git")
	f.branch(older, "feat", 5*time.Hour)
	newer := f.repo("code/app-copy", "https://github.com/Alice/App.git")
	f.branch(newer, "feat", 2*time.Hour)
	f.branch(newer, "other", 3*time.Hour)
	f.git(newer, "worktree", "add", "--quiet", f.home("code/app-copy-wt"), "other")

	doc := f.read()

	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"code:alice/app:feat", "code:alice/app:other"}) {
		t.Fatalf("unplaced = %v, want each branch once across clones and worktrees", ids)
	}
	it := doc.Unplaced[0].Item
	if attrs, _ := it["attrs"].(map[string]any); it["last_touched"] != stamp(2*time.Hour) || attrs["repo"] != newer {
		t.Errorf("feat = %v, want the clone that touched it last", it)
	}
}

func TestReadDiscoversOnlyRecentBranchesButReadsMembersAtAnyAge(t *testing.T) {
	t.Run("discovery_window", func(t *testing.T) {
		f := newFixture(t).withGit()
		f.write("personal.yml", `
discovery_window: 72h
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: ["~/code/*"]
workstreams:
  - name: old-work
    members: ["code:alice/app:kept"]
`)
		dir := f.repo("code/app", "git@github.com:alice/app.git")
		f.branch(dir, "recent", 71*time.Hour)
		f.branch(dir, "stale", 100*time.Hour)
		f.branch(dir, "kept", 100*time.Hour)

		doc := f.read()

		if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"code:alice/app:recent"}) {
			t.Errorf("unplaced = %v, want only the branch inside the window", ids)
		}
		if items := doc.Workstreams[0].Items; len(items) != 1 || items[0].Item["id"] != "alice/app:kept" {
			t.Errorf("old-work items = %+v, want the member read whatever its age", items)
		}
	})
	t.Run("defaults to event_window", func(t *testing.T) {
		f := newFixture(t).withGit()
		f.write("personal.yml", "event_window: 48h\n"+fencedCode)
		dir := f.repo("code/app", "git@github.com:alice/app.git")
		f.branch(dir, "recent", 47*time.Hour)
		f.branch(dir, "older", 49*time.Hour)

		if ids := unplacedIDs(f.read()); !slices.Equal(ids, []string{"code:alice/app:recent"}) {
			t.Errorf("unplaced = %v, want the event window to bound discovery", ids)
		}
	})
}

// TestReadGitHealth pins how a git input's health is computed: a failure is
// named by the entry the configuration wrote, never by a path the fence has
// not cleared, and other repositories' branches are still reported.
func TestReadGitHealth(t *testing.T) {
	cases := []struct {
		name    string
		repos   string
		build   func(f *fixture)
		members string
		expect  bool
		status  string
		reasons []string
		details []string
		items   int
		fetched bool
	}{
		{name: "one entry missing", repos: `["~/code/app", "~/code/gone"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
		}, status: "degraded", reasons: []string{"missing"}, details: []string{"~/code/gone: no such directory"}, items: 1, fetched: true},
		{name: "every entry missing", repos: `["~/code/a", "~/code/b"]`,
			status: "error", reasons: []string{"missing"}, details: []string{"~/code/a: no such directory", "~/code/b: no such directory"}},
		{name: "pattern matching no repository", repos: `["~/nowhere/*"]`, build: func(f *fixture) {
			if err := os.MkdirAll(f.home("nowhere/notes"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "error", reasons: []string{"missing"}, details: []string{"~/nowhere/*: matches no repository"}},
		{name: "literal directory that is not a repository", repos: `["~/code/notes"]`, build: func(f *fixture) {
			if err := os.MkdirAll(f.home("code/notes"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "error", reasons: []string{"not_a_repository"}, details: []string{"~/code/notes: not a git repository"}},
		{name: "a pattern skips directories that are not repositories", repos: `["~/code/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			if err := os.MkdirAll(f.home("code/notes"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "ok", items: 1, fetched: true},
		{name: "a repository git cannot read is counted, not named", repos: `["~/code/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			broken := f.repo("code/mallory-broken", "git@github.com:mallory/x.git")
			if err := os.WriteFile(filepath.Join(broken, ".git", "config"), []byte("[[[\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*: 1 repository could not be read"}, items: 1, fetched: true},
		{name: "a broken repository inside another is never read as the outer one", repos: `["~/code/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			f.branch(f.repo("", "git@github.com:alice/home.git"), "outer", time.Hour)
			if err := os.MkdirAll(f.home("code/broken/.git"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*: 1 repository could not be read"}, items: 1, fetched: true},
		{name: "a literal entry with no origin says so", repos: `["~/code/solo"]`, build: func(f *fixture) {
			f.branch(f.repo("code/solo", ""), "feat", time.Hour)
		}, status: "ok", details: []string{"~/code/solo: no origin remote, so the owner fence keeps it out"}, fetched: true},
		{name: "a literal entry outside the fence says so", repos: `["~/code/theirs"]`, build: func(f *fixture) {
			f.branch(f.repo("code/theirs", "git@github.com:bob/theirs.git"), "feat", time.Hour)
		}, status: "ok", details: []string{"~/code/theirs: its origin's owner is not in forge_owners"}, fetched: true},
		{name: "a commit dated ahead of the clock", repos: `["~/code/app"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "future", -time.Hour)
		}, status: "degraded", reasons: []string{"clock_skew"}, details: []string{"alice/app:future: its last commit is dated ahead of this machine's clock"}, items: 1, fetched: true},
		{name: "a commit dated past year 9999", repos: `["~/code/app"]`, build: func(f *fixture) {
			dir := f.repo("code/app", "git@github.com:alice/app.git")
			f.branch(dir, "feat", time.Hour)
			tree := f.git(dir, "mktree")
			commit := f.git(dir, "-c", "core.abbrev=no", "commit-tree", tree, "-m", "far", "--no-gpg-sign")
			// hash-object accepts any committer line git can parse, a date past
			// what a readout's timestamps can hold included.
			body := f.git(dir, "cat-file", "commit", commit)
			body = regexp.MustCompile(`(?m)^(committer .*> )\d+`).ReplaceAllString(body, "${1}253402300800")
			far := f.gitStdin(dir, body+"\n", "hash-object", "-t", "commit", "-w", "--stdin")
			f.git(dir, "update-ref", "refs/heads/far", far)
		}, status: "degraded", reasons: []string{"clock_skew"}, details: []string{"alice/app:far: its last commit is dated ahead of this machine's clock"}, items: 1, fetched: true},
		{name: "a member branch that was deleted", repos: `["~/code/app"]`, build: func(f *fixture) {
			f.repo("code/app", "git@github.com:alice/app.git")
		}, members: `["code:alice/app:merged"]`, status: "ok", fetched: true},
		{name: "no branches where expect_records says there are", repos: `["~/code/app"]`, build: func(f *fixture) {
			f.repo("code/app", "git@github.com:alice/app.git")
		}, expect: true, status: "degraded", reasons: []string{"empty"}, fetched: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t).withGit()
			// A member makes the workstream draw on the input, so its
			// inputs_incomplete shows the input's health.
			members := tc.members
			if members == "" {
				members = `["code:alice/app:feat"]`
			}
			f.write("personal.yml", `
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: `+tc.repos+`
    expect_records: `+strconv.FormatBool(tc.expect)+`
workstreams:
  - name: work
    members: `+members+`
`)
			if tc.build != nil {
				tc.build(f)
			}

			code, stdout, stderr := f.run("read", "--json")
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr)
			}
			doc := f.read()
			h := doc.health(t, "code")

			if h.Status != tc.status {
				t.Errorf("status = %q, want %q (reasons %v, details %v)", h.Status, tc.status, h.Reasons, h.Details)
			}
			want := tc.reasons
			if want == nil {
				want = []string{}
			}
			if !slices.Equal(h.Reasons, want) {
				t.Errorf("reasons = %v, want %v", h.Reasons, want)
			}
			wantDetails := tc.details
			if wantDetails == nil {
				wantDetails = []string{}
			}
			if !slices.Equal(h.Details, wantDetails) {
				t.Errorf("details = %q, want %q", h.Details, wantDetails)
			}
			if h.RecordCount != tc.items {
				t.Errorf("record_count = %d, want %d", h.RecordCount, tc.items)
			}
			if tc.fetched != (h.FetchedAt != nil) {
				t.Errorf("fetched_at = %v, want set: %v", h.FetchedAt, tc.fetched)
			}
			if strings.Contains(stdout, "mallory") {
				t.Errorf("the readout names a repository whose owner was never checked:\n%s", stdout)
			}
			incomplete := tc.status != "ok"
			if got := slices.Contains(doc.Workstreams[0].InputsIncomplete, "code"); got != incomplete {
				t.Errorf("inputs_incomplete = %v, want code listed: %v", doc.Workstreams[0].InputsIncomplete, incomplete)
			}
		})
	}
}

func TestReadReportsGitMissingFromPath(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
	// The invocation's PATH, not the one this process runs with, is where
	// lookout looks for git.
	f.env["PATH"] = t.TempDir()

	h := f.read().health(t, "code")

	if h.Status != "error" || !slices.Equal(h.Reasons, []string{"git_failed"}) || !slices.Equal(h.Details, []string{"git: not found on PATH"}) {
		t.Errorf("health = %+v, want an error saying git was not found", h)
	}
}

// TestReadIgnoresTheCallersRepositoryVariables pins that git is never pointed
// at another repository by variables a hook or wrapper exported, such as a
// pre-push hook running these specs.
func TestReadIgnoresTheCallersRepositoryVariables(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
	decoy := f.repo("decoy", "git@github.com:alice/decoy.git")
	f.branch(decoy, "decoy-branch", time.Hour)
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	f.env["GIT_DIR"] = filepath.Join(decoy, ".git")

	if ids := unplacedIDs(f.read()); !slices.Equal(ids, []string{"code:alice/app:feat"}) {
		t.Errorf("unplaced = %v, want the configured repository's branch, not the decoy's", ids)
	}
}

func TestUnplacedBranchTokensAreAdoptable(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feature/x", time.Hour)

	code, stdout, stderr := f.run("unplaced")
	if code != 0 || !strings.HasPrefix(stdout, "code:alice/app:feature/x\tfeature/x\t") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	token, _, _ := strings.Cut(stdout, "\t")
	if code, _, stderr := f.run("adopt", "--new", token, "search"); code != 0 {
		t.Fatalf("adopt exit code = %d, stderr = %q", code, stderr)
	}

	doc := f.read()
	if len(doc.Unplaced) != 0 || len(doc.Workstreams) != 1 || len(doc.Workstreams[0].Items) != 1 {
		t.Errorf("readout = %+v, want the branch in the new workstream", doc)
	}
}

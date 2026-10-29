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

// git runs git in dir for building fixtures.
func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	return f.gitAt(dir, time.Hour, args...)
}

func (f *fixture) gitAt(dir string, ago time.Duration, args ...string) string {
	f.t.Helper()
	return f.gitWith(dir, ago, "", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
}

// gitStdin is git with input on stdin, for fixtures plumbing builds.
func (f *fixture) gitStdin(dir, input string, args ...string) string {
	f.t.Helper()
	return f.gitWith(dir, time.Hour, input, args...)
}

// gitWith runs git in dir with stdin, its commits dated ago. Its environment
// is set in full rather than inherited, so a hook running these specs, which
// exports its own repository's variables, cannot redirect it.
func (f *fixture) gitWith(dir string, ago time.Duration, stdin string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
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
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// rawCommitBranch points refs/heads/name at a commit whose committer
// timestamp is written as given, which git stores without checking.
func (f *fixture) rawCommitBranch(dir, name, timestamp string) {
	f.t.Helper()
	tree := f.git(dir, "mktree")
	body := "tree " + tree + "\nauthor A <a@example.com> 0 +0000\ncommitter A <a@example.com> " + timestamp + " +0000\n\n" + name + "\n"
	commit := f.gitStdin(dir, body, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	f.git(dir, "update-ref", "refs/heads/"+name, commit)
}

// locked removes every permission from dir for the rest of the spec, so
// nothing inside it can be listed or read.
func (f *fixture) locked(dir string) {
	f.t.Helper()
	if os.Geteuid() == 0 {
		f.t.Skip("permissions do not bind the superuser")
	}
	if err := os.Chmod(dir, 0); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			f.t.Error(err)
		}
	})
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
	commit := f.gitAt(dir, ago, "commit-tree", tree, "-m", "fixture")
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
		{name: "origin/HEAD names a branch ending in a non-breaking space", build: func(f *fixture, dir string) {
			f.git(dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk\u00a0")
			f.branch(dir, "trunk\u00a0", time.Hour)
			f.branch(dir, "trunk", time.Hour)
			f.branch(dir, "main", time.Hour)
		}, visible: []string{"code:alice/app:main", "code:alice/app:trunk", "code:alice/app:x"}},
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

// TestReadKnowsABareRepositoryThroughItsWorktree pins that bareness belongs to
// the repository, not to whichever of its directories a pattern reaches
// first: git reports a linked worktree of a bare repository as not bare.
func TestReadKnowsABareRepositoryThroughItsWorktree(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	dir := f.home("code/app.git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(dir, "init", "--quiet", "--bare", "--initial-branch=develop")
	f.git(dir, "remote", "add", "origin", "git@github.com:alice/app.git")
	for _, b := range []string{"develop", "main", "colleague-work", "feat"} {
		f.branch(dir, b, time.Hour)
	}
	// app-feature sorts before app.git, so the pattern reaches the worktree first.
	f.git(dir, "worktree", "add", "--quiet", f.home("code/app-feature"), "feat")
	f.git(dir, "worktree", "add", "--quiet", f.home("wt/main"), "main")

	if ids := unplacedIDs(f.read()); !slices.Equal(ids, []string{"code:alice/app:feat", "code:alice/app:main"}) {
		t.Errorf("unplaced = %v, want only the checked-out branches other than the bare repository's HEAD", ids)
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

// TestReadBreaksTiesBetweenClonesInConfiguredOrder pins that two clones
// holding a branch at the same moment always report the same one: the first
// the configuration reaches.
func TestReadBreaksTiesBetweenClonesInConfiguredOrder(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", `
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: ["~/code/b", "~/code/a", "~/code/c"]
workstreams: []
`)
	for _, rel := range []string{"code/a", "code/b", "code/c"} {
		f.branch(f.repo(rel, "git@github.com:alice/app.git"), "feat", time.Hour)
	}
	// Each read walks its sources afresh, so an order that varied between
	// runs would show within a few.
	for range 20 {
		doc := f.read()
		if attrs, _ := doc.Unplaced[0].Item["attrs"].(map[string]any); attrs["repo"] != f.home("code/b") {
			t.Fatalf("repo = %v, want the clone listed first", attrs["repo"])
		}
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
	t.Run("a completed workstream's member moving again", func(t *testing.T) {
		f := newFixture(t).withGit()
		f.write("personal.yml", `
discovery_window: 72h
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: ["~/code/*"]
workstreams:
  - name: done
    members: ["code:alice/app:shipped"]
    completed: 2030-06-01
`)
		f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "shipped", 120*time.Hour)

		if ids := unplacedIDs(f.read()); len(ids) != 0 {
			t.Errorf("unplaced = %v, want nothing: it moved after its workstream was completed, but not within the window", ids)
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
		// mayBeEmpty sets repos_may_be_empty.
		mayBeEmpty bool
		status     string
		reasons    []string
		details    []string
		items      int
		fetched    bool
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
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*: 1 match could not be read"}, items: 1, fetched: true},
		{name: "a broken repository inside another is never read as the outer one", repos: `["~/code/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			f.branch(f.repo("", "git@github.com:alice/home.git"), "outer", time.Hour)
			if err := os.MkdirAll(f.home("code/broken/.git"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*: 1 match could not be read"}, items: 1, fetched: true},
		{name: "a repository whose branches git cannot read is not read", repos: `["~/code/app"]`, build: func(f *fixture) {
			// With HEAD on a branch that exists, git can open the repository and
			// name its origin, and fails only once it lists the branches.
			dir := f.repo("code/app", "git@github.com:alice/app.git")
			f.branch(dir, "main", time.Hour)
			f.branch(dir, "feat", time.Hour)
			if err := os.WriteFile(filepath.Join(dir, ".git", "packed-refs"), []byte("garbage\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, status: "error", reasons: []string{"git_failed"}, details: []string{"~/code/app: git could not read it"}},
		{name: "a repository that fails is counted once across its worktrees", repos: `["~/code/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			dir := f.repo("code/broken", "git@github.com:alice/broken.git")
			f.branch(dir, "main", time.Hour)
			f.branch(dir, "side", time.Hour)
			f.git(dir, "worktree", "add", "--quiet", f.home("code/broken-wt"), "side")
			if err := os.WriteFile(filepath.Join(dir, ".git", "packed-refs"), []byte("garbage\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*: 1 match could not be read"}, items: 1, fetched: true},
		{name: "a branch whose date git prints past any time is left out alone", repos: `["~/code/app"]`, build: func(f *fixture) {
			dir := f.repo("code/app", "git@github.com:alice/app.git")
			f.branch(dir, "feat", time.Hour)
			f.rawCommitBranch(dir, "huge", "9300000000000000000")
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"alice/app:huge: git printed a commit date lookout cannot read"}, items: 1, fetched: true},
		{name: "a branch name that is not UTF-8 is left out", repos: `["~/code/app"]`, build: func(f *fixture) {
			dir := f.repo("code/app", "git@github.com:alice/app.git")
			f.branch(dir, "feat", time.Hour)
			f.branch(dir, "caf\xe9", time.Hour)
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"alice/app: a branch whose name is not UTF-8 was left out"}, items: 1, fetched: true},
		{name: "no recent branches is not empty", repos: `["~/code/app"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "old", 1000*time.Hour)
		}, expect: true, status: "ok", fetched: true},
		{name: "a literal entry a pattern reached first still says why", repos: `["~/code/*", "~/code/solo"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			f.branch(f.repo("code/solo", ""), "feat", time.Hour)
		}, status: "ok", details: []string{"~/code/solo: no origin remote, so the owner fence keeps it out"}, items: 1, fetched: true},
		{name: "a relative entry is named as written", repos: `["../../code/solo"]`, build: func(f *fixture) {
			f.branch(f.repo("code/solo", ""), "feat", time.Hour)
		}, status: "ok", details: []string{"../../code/solo: no origin remote, so the owner fence keeps it out"}, fetched: true},
		{name: "a literal entry whose origin is no forge says so", repos: `["~/code/mirror"]`, build: func(f *fixture) {
			f.branch(f.repo("code/mirror", "/srv/mirrors/alice/app.git"), "feat", time.Hour)
		}, status: "ok", details: []string{"~/code/mirror: its origin is not a forge address, so the owner fence keeps it out"}, fetched: true},
		{name: "a directory lookout cannot look inside is counted, not skipped", repos: `["~/code/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			f.locked(f.repo("code/mallory-locked", "git@github.com:mallory/x.git"))
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*: 1 match could not be read"}, items: 1, fetched: true},
		{name: "a file where a pattern expects a directory is no match", repos: `["~/code/*/app"]`, build: func(f *fixture) {
			f.branch(f.repo("code/team/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			if err := os.WriteFile(f.home("code/README"), []byte("notes\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, status: "ok", items: 1, fetched: true},
		{name: "a file where a pattern expects a directory to search is no match", repos: `["~/code/*/repos/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/team/repos/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			if err := os.WriteFile(f.home("code/README"), []byte("notes\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, status: "ok", items: 1, fetched: true},
		{name: "a directory a pattern cannot search is counted, not skipped", repos: `["~/code/*/*"]`, build: func(f *fixture) {
			f.branch(f.repo("code/alice/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			f.branch(f.repo("code/mallory/tool", "git@github.com:mallory/tool.git"), "feat", time.Hour)
			f.locked(f.home("code/mallory"))
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*/*: 1 match could not be read"}, items: 1, fetched: true},
		{name: "a literal directory lookout cannot look inside is not called a non-repository", repos: `["~/code/app", "~/code/locked"]`, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			f.locked(f.repo("code/locked", "git@github.com:alice/locked.git"))
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/locked: git could not read it"}, items: 1, fetched: true},
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
		{name: "a pattern that may be empty matching nothing", repos: `["~/code/*"]`, mayBeEmpty: true, build: func(f *fixture) {
			if err := os.MkdirAll(f.home("code/notes"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "ok", fetched: true},
		{name: "a pattern that may be empty beside one that reads", repos: `["~/code/app", "~/spare/*"]`, mayBeEmpty: true, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			if err := os.MkdirAll(f.home("spare"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "ok", items: 1, fetched: true},
		{name: "a pattern that may be empty under a directory that is gone", repos: `["~/nowhere/*/src"]`, mayBeEmpty: true,
			status: "error", reasons: []string{"missing"}, details: []string{"~/nowhere/*/src: no such directory"}},
		{name: "a pattern that may be empty still counts a match git cannot read", repos: `["~/code/*"]`, mayBeEmpty: true, build: func(f *fixture) {
			f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
			broken := f.repo("code/mallory-broken", "git@github.com:mallory/x.git")
			if err := os.WriteFile(filepath.Join(broken, ".git", "config"), []byte("[[[\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"~/code/*: 1 match could not be read"}, items: 1, fetched: true},
		{name: "a missing path is missing where patterns may be empty", repos: `["~/code/*", "~/code/gone"]`, mayBeEmpty: true, build: func(f *fixture) {
			if err := os.MkdirAll(f.home("code"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, status: "error", reasons: []string{"missing"}, details: []string{"~/code/gone: no such directory"}},
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
    repos_may_be_empty: `+strconv.FormatBool(tc.mayBeEmpty)+`
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

// TestReadTakesTheConfigurationsDirectoryLiterally pins that pattern syntax is
// read only in the entry the user wrote, never in the directory a relative
// entry is read from.
func TestReadTakesTheConfigurationsDirectoryLiterally(t *testing.T) {
	f := newFixture(t).withGit()
	base := f.home("settings[1]")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	f.env["LOOKOUT_CONFIG"] = filepath.Join(base, "personal.yml")
	if err := os.WriteFile(f.env["LOOKOUT_CONFIG"], []byte(`
forge_owners: [alice]
inputs:
  - name: code
    kind: git
    repos: [app, "code/*", gone]
workstreams: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.branch(f.repo("settings[1]/app", "git@github.com:alice/app.git"), "feat", time.Hour)
	f.branch(f.repo("settings[1]/code/tool", "git@github.com:alice/tool.git"), "feat", time.Hour)
	// The directory the brackets would match as a pattern.
	f.branch(f.repo("settings1/app", "git@github.com:alice/decoy.git"), "feat", time.Hour)

	doc := f.read()

	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"code:alice/app:feat", "code:alice/tool:feat"}) {
		t.Errorf("unplaced = %v, want the repositories under the configuration's own directory", ids)
	}
	// A literal entry stays literal, so a missing one is reported as missing
	// rather than as a pattern that matched nothing.
	if h := doc.health(t, "code"); h.Status != "degraded" || !slices.Equal(h.Details, []string{"gone: no such directory"}) {
		t.Errorf("health = %+v, want only the missing literal entry reported", h)
	}
}

// TestReadSearchesOnlyAbsolutePathDirectories pins that a relative or empty
// PATH element never finds a git in whatever directory lookout runs from.
func TestReadSearchesOnlyAbsolutePathDirectories(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
	here := t.TempDir()
	if err := os.WriteFile(filepath.Join(here, "git"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(here)
	f.env["PATH"] = ".::" + t.TempDir()

	h := f.read().health(t, "code")

	if !slices.Equal(h.Details, []string{"git: not found on PATH"}) {
		t.Errorf("details = %q, want git not found: the only git is in the working directory", h.Details)
	}
}

// TestReadSaysWhenItCouldNotSearchThePath pins that a PATH directory lookout
// cannot look into is not taken for one with no git in it.
func TestReadSaysWhenItCouldNotSearchThePath(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
	shut := t.TempDir()
	f.locked(shut)
	f.env["PATH"] = shut + ":" + t.TempDir()

	h := f.read().health(t, "code")

	if h.Status != "error" || !slices.Equal(h.Details, []string{"git: not found on PATH, and 1 directory on it could not be searched"}) {
		t.Errorf("health = %+v, want git not found, saying a directory could not be searched", h)
	}
}

// TestReadTakesAFileOnThePathForNoDirectory pins that a PATH element naming a
// file holds no git, rather than being a directory lookout could not search.
func TestReadTakesAFileOnThePathForNoDirectory(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", fencedCode)
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feat", time.Hour)
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"] = file + ":" + t.TempDir()

	if h := f.read().health(t, "code"); !slices.Equal(h.Details, []string{"git: not found on PATH"}) {
		t.Errorf("details = %q, want git simply not found", h.Details)
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

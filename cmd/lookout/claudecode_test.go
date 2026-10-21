package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const claudeOnly = `
forge_owners: [alice]
inputs:
  - name: claude
    kind: claude_code
    config_dirs: ["~/.claude"]
workstreams: []
`

// Fixture session ids, in the shape Claude Code names transcripts by.
const (
	sessionA = "0a000000-0000-4000-8000-00000000000a"
	sessionB = "0b000000-0000-4000-8000-00000000000b"
	sessionC = "0c000000-0000-4000-8000-00000000000c"
)

// projectDir is the directory Claude Code keeps a session launched in cwd
// under: every character that is not a letter or digit becomes a dash.
var projectDirName = regexp.MustCompile(`[^A-Za-z0-9]`)

func projectDir(cwd string) string { return projectDirName.ReplaceAllString(cwd, "-") }

// turn is one conversation record of session id, made in cwd on branch ago.
func turn(id, cwd, branch string, ago time.Duration) string {
	return jsonLine(map[string]any{
		"type": "user", "sessionId": id, "cwd": cwd, "gitBranch": branch,
		"timestamp": now.Add(-ago).Format("2006-01-02T15:04:05.000Z"), "isSidechain": false,
		"uuid": "u", "parentUuid": nil, "message": map[string]any{"role": "user", "content": "fixture"},
	})
}

func titled(id, title string) string {
	return jsonLine(map[string]any{"type": "ai-title", "sessionId": id, "aiTitle": title})
}

func jsonLine(fields map[string]any) string {
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// transcript writes session id's lines under configDir as a session launched
// in launch would have them, last modified ago, and returns its path.
func (f *fixture) transcript(configDir, launch, id string, modified time.Duration, lines ...string) string {
	f.t.Helper()
	path := filepath.Join(configDir, "projects", projectDir(launch), id+".jsonl")
	f.writeAt(path, strings.Join(lines, "\n")+"\n", modified)
	return path
}

// writeAt writes content to path, last modified ago.
func (f *fixture) writeAt(path, content string, modified time.Duration) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	at := now.Add(-modified)
	if err := os.Chtimes(path, at, at); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) mkdir(rel string) string {
	f.t.Helper()
	dir := f.home(rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// sessionItem is the unplaced item with id, or nil.
func sessionItem(doc readoutDoc, input, id string) map[string]any {
	for _, u := range doc.Unplaced {
		if u.Input == input && u.Item["id"] == id {
			return u.Item
		}
	}
	return nil
}

func sessionsOf(it map[string]any) []string {
	attrs, _ := it["attrs"].(map[string]any)
	raw, _ := attrs["sessions"].([]any)
	out := []string{}
	for _, s := range raw {
		out = append(out, s.(string))
	}
	return out
}

func TestReadSurfacesClaudeCodeSessionsOfFencedRepos(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.transcript(f.home(".claude"), app, sessionA, 2*time.Hour,
		jsonLine(map[string]any{"type": "mode", "sessionId": sessionA}),
		turn(sessionA, app, "feature/x", 3*time.Hour),
		turn(sessionA, app, "feature/x", 2*time.Hour),
		titled(sessionA, "Tidy the export"),
	)

	doc := f.read()

	got := sessionItem(doc, "claude", "alice/app:feature/x")
	if got == nil {
		t.Fatalf("unplaced = %v, want alice/app:feature/x", unplacedIDs(doc))
	}
	if got["title"] != "Tidy the export" || got["last_touched"] != stamp(2*time.Hour) || got["state"] != "local" {
		t.Errorf("item = %v, want its session's title, its newest record and state local", got)
	}
	attrs, _ := got["attrs"].(map[string]any)
	if attrs["branch"] != "feature/x" || attrs["repo"] != app || !slices.Equal(sessionsOf(got), []string{sessionA}) {
		t.Errorf("attrs = %v, want the branch, the repository and the session id", attrs)
	}
	if _, ok := got["started_at"]; ok {
		t.Errorf("item = %v, want no started_at", got)
	}
	h := doc.health(t, "claude")
	if h.Status != "ok" || h.RecordCount != 1 || h.ExcludedCount == nil || *h.ExcludedCount != 0 || h.FetchedAt == nil {
		t.Errorf("health = %+v, want ok with one record", h)
	}
}

func TestReadRollsClaudeCodeSessionsUpByBranch(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	lib := f.repo("code/lib", "git@github.com:alice/lib.git")
	sub := f.mkdir("code/app/internal/x")
	f.branch(app, "feature/w", 5*time.Hour)
	worktree := f.home("code/app-w")
	f.git(app, "worktree", "add", "--quiet", worktree, "feature/w")
	claude := f.home(".claude")

	f.transcript(claude, app, sessionA, 5*time.Hour, turn(sessionA, app, "feature/x", 5*time.Hour))
	// Timestamps in a transcript are not in order, so the newest is not the
	// last line's.
	f.transcript(claude, sub, sessionB, 2*time.Hour,
		turn(sessionB, sub, "feature/x", 2*time.Hour),
		turn(sessionB, sub, "feature/x", 4*time.Hour),
	)
	// A session that moves to another repository touches both.
	f.transcript(claude, app, sessionC, time.Hour,
		turn(sessionC, worktree, "feature/w", 3*time.Hour),
		turn(sessionC, lib, "feature/y", time.Hour),
	)

	doc := f.read()

	x := sessionItem(doc, "claude", "alice/app:feature/x")
	if x == nil || x["last_touched"] != stamp(2*time.Hour) || !slices.Equal(sessionsOf(x), []string{sessionB, sessionA}) {
		t.Errorf("alice/app:feature/x = %v, want one item touched at the newest record, sessions newest first", x)
	}
	if w := sessionItem(doc, "claude", "alice/app:feature/w"); w == nil || w["last_touched"] != stamp(3*time.Hour) {
		t.Errorf("alice/app:feature/w = %v, want the linked worktree's session under its repository", w)
	}
	if y := sessionItem(doc, "claude", "alice/lib:feature/y"); y == nil || y["last_touched"] != stamp(time.Hour) {
		t.Errorf("alice/lib:feature/y = %v, want the second repository the session moved to", y)
	}
	if len(doc.Unplaced) != 3 {
		t.Errorf("unplaced = %v, want three items", unplacedIDs(doc))
	}
}

func TestReadTitlesAClaudeCodeItemByItsNewestSession(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	claude := f.home(".claude")
	f.transcript(claude, app, sessionA, 5*time.Hour, turn(sessionA, app, "feature/x", 5*time.Hour), titled(sessionA, "Last week's work"))
	// A run with no generated title, such as claude -p, is still the newest.
	f.transcript(claude, app, sessionB, time.Hour, turn(sessionB, app, "feature/x", time.Hour))

	it := sessionItem(f.read(), "claude", "alice/app:feature/x")

	if it == nil || it["title"] != "feature/x" {
		t.Errorf("item = %v, want the branch name, since the newest session has no title", it)
	}
}

func TestReadKeepsClaudeCodeSessionsOutsideTheOwnerFence(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	other := f.repo("code/mallory-x", "git@github.com:mallory/x.git")
	plain := f.mkdir("scratch/mallory-notes")
	gone := f.home("code/mallory-gone")
	claude := f.home(".claude")

	f.transcript(claude, other, sessionA, time.Hour, turn(sessionA, other, "mallory-branch", time.Hour), titled(sessionA, "mallory work"))
	f.transcript(claude, plain, sessionB, time.Hour, turn(sessionB, plain, "", time.Hour))
	f.transcript(claude, gone, sessionC, time.Hour, turn(sessionC, gone, "mallory-old", time.Hour))
	// A session that crossed into the fence from outside it keeps its generated
	// title, which summarizes the whole session, to itself.
	crossing := "0d000000-0000-4000-8000-00000000000d"
	f.transcript(claude, other, crossing, time.Hour,
		turn(crossing, other, "mallory-branch", 2*time.Hour),
		turn(crossing, app, "feature/x", time.Hour),
		titled(crossing, "Port the mallory importer"),
	)

	code, stdout, stderr := f.run("read", "--json")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(strings.ToLower(stdout), "mallory") {
		t.Errorf("stdout names work outside the fence:\n%s", stdout)
	}
	var doc readoutDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(unplacedIDs(doc), []string{"claude:alice/app:feature/x"}) {
		t.Errorf("unplaced = %v, want only alice's branch", unplacedIDs(doc))
	}
	if it := sessionItem(doc, "claude", "alice/app:feature/x"); it != nil && it["title"] != "feature/x" {
		t.Errorf("title = %v, want the branch name, since the session's title covers work outside the fence", it["title"])
	}
	h := doc.health(t, "claude")
	if h.ExcludedCount == nil || *h.ExcludedCount != 1 {
		t.Errorf("excluded_count = %v, want the one repository the fence kept out", h.ExcludedCount)
	}
	if h.Status != "ok" || !slices.Equal(h.Details, []string{"2 working directories no longer exist or are in no repository"}) {
		t.Errorf("health = %+v, want ok, counting the unattributable directories", h)
	}
}

func TestReadNeverLetsAHomeRepositoryClaimClaudeCodeSessions(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	f.repo("", "git@github.com:alice/home.git")
	notes := f.mkdir("notes")
	f.transcript(f.home(".claude"), notes, sessionA, time.Hour, turn(sessionA, notes, "main", time.Hour))

	doc := f.read()

	if len(doc.Unplaced) != 0 {
		t.Errorf("unplaced = %v, want nothing claimed by the home directory's repository", unplacedIDs(doc))
	}
	if h := doc.health(t, "claude"); !slices.Equal(h.Details, []string{"1 working directory no longer exists or is in no repository"}) {
		t.Errorf("details = %v", h.Details)
	}
}

func TestReadRecoversRemovedWorktreeSessions(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	other := f.repo("code/mallory-x", "git@github.com:mallory/x.git")
	claude := f.home(".claude")
	worktreeState := func(id, original, path string) string {
		return jsonLine(map[string]any{"type": "worktree-state", "sessionId": id, "worktreeSession": map[string]any{
			"originalCwd": original, "worktreePath": path, "worktreeName": "w", "sessionId": id,
		}})
	}

	// Claude Code's own worktree layout, removed on exit.
	inApp := filepath.Join(app, ".claude", "worktrees", "x")
	f.transcript(claude, app, sessionA, time.Hour, turn(sessionA, filepath.Join(inApp, "cmd"), "worktree-x", time.Hour))
	// A worktree elsewhere, known only from the session's own record of it.
	elsewhere := f.home("wt/app-y")
	f.transcript(claude, app, sessionB, time.Hour, worktreeState(sessionB, app, elsewhere), turn(sessionB, elsewhere, "feature/y", time.Hour))
	// Recovery is still fenced.
	outside := f.home("wt/mallory-z")
	f.transcript(claude, other, sessionC, time.Hour, worktreeState(sessionC, other, outside), turn(sessionC, outside, "mallory-z", time.Hour))
	// Without either, or with a record lookout cannot read, it stays counted.
	lost, reshaped := "0d000000-0000-4000-8000-00000000000d", "0e000000-0000-4000-8000-00000000000e"
	f.transcript(claude, app, lost, time.Hour, turn(lost, f.home("wt/lost"), "feature/l", time.Hour))
	f.transcript(claude, app, reshaped, time.Hour,
		jsonLine(map[string]any{"type": "worktree-state", "sessionId": reshaped, "worktreeSession": "w"}),
		turn(reshaped, f.home("wt/reshaped"), "feature/r", time.Hour))

	code, stdout, stderr := f.run("read", "--json")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "mallory") {
		t.Errorf("stdout names work outside the fence:\n%s", stdout)
	}
	var doc readoutDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if got := unplacedIDs(doc); !slices.Equal(got, []string{"claude:alice/app:feature/y", "claude:alice/app:worktree-x"}) {
		t.Errorf("unplaced = %v, want both removed worktrees under alice/app", got)
	}
	h := doc.health(t, "claude")
	if h.Status != "ok" || h.ExcludedCount == nil || *h.ExcludedCount != 1 ||
		!slices.Equal(h.Details, []string{"2 working directories no longer exist or are in no repository"}) {
		t.Errorf("health = %+v, want ok, one fenced out and two counted", h)
	}
}

func TestReadSkipsSidechainsAndStrayTranscripts(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	claude := f.home(".claude")
	project := filepath.Join(claude, "projects", projectDir(app))

	sidechain := jsonLine(map[string]any{"type": "assistant", "sessionId": sessionA, "cwd": app, "gitBranch": "side",
		"timestamp": now.Add(-time.Hour).Format(time.RFC3339), "isSidechain": true})
	f.transcript(claude, app, sessionA, time.Hour, turn(sessionA, app, "feature/x", 2*time.Hour), sidechain)
	f.writeAt(filepath.Join(project, sessionA, "subagents", "agent-a1.jsonl"), turn(sessionA, app, "subagent", time.Hour)+"\n", time.Hour)
	f.writeAt(filepath.Join(project, sessionB+".orphaned-1-abc.jsonl"), turn(sessionB, app, "orphan", time.Hour)+"\n", time.Hour)
	f.writeAt(filepath.Join(project, "notes.jsonl"), turn(sessionB, app, "stray", time.Hour)+"\n", time.Hour)
	// A transcript Claude Code moved mid-read is seen twice, and counts once.
	f.transcript(claude, f.home("code"), sessionA, time.Hour, turn(sessionA, app, "feature/x", 2*time.Hour))

	doc := f.read()

	if got := unplacedIDs(doc); !slices.Equal(got, []string{"claude:alice/app:feature/x"}) {
		t.Errorf("unplaced = %v, want only the main transcript's branch", got)
	}
	if it := sessionItem(doc, "claude", "alice/app:feature/x"); it == nil || it["last_touched"] != stamp(2*time.Hour) || !slices.Equal(sessionsOf(it), []string{sessionA}) {
		t.Errorf("item = %v, want the sidechain record left out and the session listed once", it)
	}
}

func TestReadDiscoversRecentSessionsButReadsMembersAtAnyAge(t *testing.T) {
	old := 20 * 24 * time.Hour
	setup := func(t *testing.T, members string) *fixture {
		f := newFixture(t).withGit()
		f.write("personal.yml", strings.Replace(claudeOnly, "workstreams: []", "workstreams:\n  - name: search\n    members: "+members, 1))
		app := f.repo("code/app", "git@github.com:alice/app.git")
		claude := f.home(".claude")
		f.transcript(claude, app, sessionA, time.Hour, turn(sessionA, app, "feature/new", time.Hour))
		f.transcript(claude, app, sessionB, old, turn(sessionB, app, "feature/old", old))
		return f
	}

	t.Run("an old session is not offered", func(t *testing.T) {
		doc := setup(t, "[]").read()
		if got := unplacedIDs(doc); !slices.Equal(got, []string{"claude:alice/app:feature/new"}) {
			t.Errorf("unplaced = %v, want only the recent branch", got)
		}
	})
	t.Run("an old record in a recent transcript is not offered", func(t *testing.T) {
		f := setup(t, "[]")
		app := f.home("code/app")
		f.transcript(f.home(".claude"), app, sessionC, time.Hour, turn(sessionC, app, "feature/stale", old), turn(sessionC, app, "feature/new", time.Hour))
		if got := unplacedIDs(f.read()); !slices.Equal(got, []string{"claude:alice/app:feature/new"}) {
			t.Errorf("unplaced = %v, want only the recently touched branch", got)
		}
	})
	t.Run("a member is read from an old transcript", func(t *testing.T) {
		f := setup(t, `["claude:alice/app:feature/old"]`)
		// What the member sends lookout back for is not counted, so health does
		// not change with whether a member is unread.
		gone := f.home("code/gone")
		f.transcript(f.home(".claude"), gone, sessionC, old, turn(sessionC, gone, "feature/old", old))
		doc := f.read()
		if len(doc.Workstreams) != 1 || len(doc.Workstreams[0].Items) != 1 || len(doc.Workstreams[0].MembersUnread) != 0 {
			t.Errorf("workstreams = %+v, want the old member read", doc.Workstreams)
		}
		if h := doc.health(t, "claude"); h.Status != "ok" || len(h.Details) != 0 {
			t.Errorf("health = %+v, want nothing counted from the older transcripts", h)
		}
	})
	t.Run("an old transcript no member needs is never opened", func(t *testing.T) {
		f := setup(t, "[]")
		app := f.home("code/app")
		f.locked(f.transcript(f.home(".claude"), app, sessionC, old, turn(sessionC, app, "feature/older", old)))
		if h := f.read().health(t, "claude"); h.Status != "ok" {
			t.Errorf("health = %+v, want ok", h)
		}
	})
	t.Run("a member whose branch JSON escapes is read from an old transcript", func(t *testing.T) {
		f := setup(t, `['claude:alice/app:a"b']`)
		app := f.home("code/app")
		f.transcript(f.home(".claude"), app, sessionC, old, turn(sessionC, app, `a"b`, old))
		doc := f.read()
		if len(doc.Workstreams) != 1 || len(doc.Workstreams[0].MembersUnread) != 0 {
			t.Errorf("workstreams = %+v, want the member read", doc.Workstreams)
		}
	})
	t.Run("sessions read for a member join an item already found", func(t *testing.T) {
		f := setup(t, `["claude:alice/app:feature/gone"]`)
		app := f.home("code/app")
		f.transcript(f.home(".claude"), app, sessionC, old, turn(sessionC, app, "feature/new", old), turn(sessionC, app, "feature/gone", old))
		it := sessionItem(f.read(), "claude", "alice/app:feature/new")
		if it == nil || it["last_touched"] != stamp(time.Hour) || !slices.Equal(sessionsOf(it), []string{sessionA, sessionC}) {
			t.Errorf("item = %v, want the older session added and the newer touch kept", it)
		}
	})
	t.Run("a repository git cannot read behind a member is reported", func(t *testing.T) {
		f := setup(t, `["claude:alice/broken:feature/old"]`)
		broken := f.repo("code/broken", "git@github.com:alice/broken.git")
		if err := os.WriteFile(filepath.Join(broken, ".git", "config"), []byte("[[[\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.transcript(f.home(".claude"), broken, sessionC, old, turn(sessionC, broken, "feature/old", old))
		h := f.read().health(t, "claude")
		if h.Status != "degraded" || !slices.Equal(h.Reasons, []string{"git_failed"}) || !slices.Equal(h.Details, []string{"1 working directory is in a repository git could not read"}) {
			t.Errorf("health = %+v, want the failure behind the member reported", h)
		}
	})
	t.Run("an older transcript read for a member does not stand in for the window", func(t *testing.T) {
		f := setup(t, `["claude:alice/app:feature/old"]`)
		f.locked(filepath.Join(f.home(".claude"), "projects", projectDir(f.home("code/app")), sessionA+".jsonl"))
		if h := f.read().health(t, "claude"); h.Status != "error" || !slices.Equal(h.Reasons, []string{"unreadable"}) {
			t.Errorf("health = %+v, want error: no transcript in the window could be read", h)
		}
	})
	t.Run("expect_records looks only at the window", func(t *testing.T) {
		for _, members := range []string{"[]", `["claude:alice/app:feature/old"]`} {
			f := newFixture(t).withGit()
			f.write("personal.yml", strings.Replace(strings.Replace(claudeOnly, "workstreams: []", "workstreams:\n  - name: search\n    members: "+members, 1),
				"kind: claude_code", "kind: claude_code\n    expect_records: true", 1))
			app := f.repo("code/app", "git@github.com:alice/app.git")
			f.transcript(f.home(".claude"), app, sessionB, old, turn(sessionB, app, "feature/old", old))
			if h := f.read().health(t, "claude"); h.Status != "degraded" || !slices.Equal(h.Reasons, []string{"empty"}) {
				t.Errorf("members %s: health = %+v, want empty either way", members, h)
			}
		}
	})
	t.Run("an old transcript naming no unread member is not decoded", func(t *testing.T) {
		f := setup(t, `["claude:alice/app:feature/gone"]`)
		f.writeAt(filepath.Join(f.home(".claude"), "projects", "-elsewhere", sessionC+".jsonl"), "not json at all\n", old)
		h := f.read().health(t, "claude")
		if h.Status != "ok" {
			t.Errorf("health = %+v, want ok, since the file cannot hold the member", h)
		}
	})
}

func TestReadNamesClaudeCodeBranchesAsSessionsRecordThem(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", strings.Replace(claudeOnly, `config_dirs: ["~/.claude"]`, `config_dirs: ["~/.claude"]
    ignore_branches: ["scratch/*"]`, 1))
	app := f.repo("code/app", "git@github.com:alice/app.git")
	claude := f.home(".claude")
	f.transcript(claude, app, sessionA, time.Hour, turn(sessionA, app, "HEAD", time.Hour))
	f.transcript(claude, app, sessionB, time.Hour, turn(sessionB, app, "main", time.Hour))
	f.transcript(claude, app, sessionC, time.Hour, turn(sessionC, app, "scratch/a", time.Hour))

	doc := f.read()

	if got := unplacedIDs(doc); !slices.Equal(got, []string{"claude:alice/app", "claude:alice/app:main"}) {
		t.Errorf("unplaced = %v, want the detached session under the bare repository and main kept", got)
	}
	if it := sessionItem(doc, "claude", "alice/app"); it != nil {
		if attrs, _ := it["attrs"].(map[string]any); attrs["branch"] != nil || it["title"] != "alice/app" {
			t.Errorf("detached item = %v, want no branch and the repository as its title", it)
		}
	}
}

func TestReadMergesClaudeCodeConfigDirectories(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", strings.Replace(claudeOnly, `["~/.claude"]`, `["~/.claude", "~/.claude-two"]`, 1))
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.transcript(f.home(".claude"), app, sessionA, 3*time.Hour, turn(sessionA, app, "feature/x", 3*time.Hour))
	f.transcript(f.home(".claude-two"), app, sessionB, time.Hour, turn(sessionB, app, "feature/x", time.Hour))

	doc := f.read()

	it := sessionItem(doc, "claude", "alice/app:feature/x")
	if len(doc.Unplaced) != 1 || it == nil || it["last_touched"] != stamp(time.Hour) || !slices.Equal(sessionsOf(it), []string{sessionB, sessionA}) {
		t.Errorf("unplaced = %v, want one item from both directories", doc.Unplaced)
	}
}

func TestReadClaudeCodeHealth(t *testing.T) {
	cases := []struct {
		name    string
		dirs    string
		expect  bool
		build   func(f *fixture, app string)
		status  string
		reasons []string
		details []string
		items   int
	}{
		{name: "every config dir missing", dirs: `["~/.claude"]`,
			status: "error", reasons: []string{"missing"}, details: []string{"~/.claude: no such directory"}},
		{name: "one config dir missing", dirs: `["~/.claude", "~/.claude-gone"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour, turn(sessionA, app, "feat", time.Hour))
		}, status: "degraded", reasons: []string{"missing"}, details: []string{"~/.claude-gone: no such directory"}, items: 1},
		{name: "a config dir with no projects", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.mkdir(".claude")
		}, status: "ok", details: []string{"~/.claude: has no projects directory, so it holds no sessions"}},
		{name: "a config dir with no projects where records are expected", dirs: `["~/.claude"]`, expect: true, build: func(f *fixture, app string) {
			f.mkdir(".claude")
		}, status: "degraded", reasons: []string{"empty"}, details: []string{"~/.claude: has no projects directory, so it holds no sessions"}},
		{name: "a transcript that cannot be read", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour, turn(sessionA, app, "feat", time.Hour))
			f.locked(f.transcript(f.home(".claude"), app, sessionB, time.Hour, turn(sessionB, app, "other", time.Hour)))
		}, status: "degraded", reasons: []string{"unreadable"}, details: []string{"~/.claude: 1 transcript could not be read"}, items: 1},
		{name: "records without a cwd or a branch", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour,
				turn(sessionA, app, "feat", time.Hour),
				jsonLine(map[string]any{"type": "user", "sessionId": sessionA, "gitBranch": "feat", "timestamp": now.Add(-time.Hour).Format(time.RFC3339)}),
				jsonLine(map[string]any{"type": "assistant", "sessionId": sessionA, "cwd": app, "timestamp": now.Add(-time.Hour).Format(time.RFC3339)}),
			)
		}, status: "degraded", reasons: []string{"unrecognized_format"}, details: []string{"~/.claude: 2 transcript lines are not in a shape lookout reads"}, items: 1},
		{name: "a line that is not JSON mid-file", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour, "{not json", turn(sessionA, app, "feat", time.Hour))
		}, status: "degraded", reasons: []string{"unrecognized_format"}, details: []string{"~/.claude: 1 transcript line is not in a shape lookout reads"}, items: 1},
		{name: "transcripts with no conversation record", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour,
				jsonLine(map[string]any{"type": "speech", "sessionId": sessionA, "cwd": app, "gitBranch": "feat", "timestamp": now.Add(-time.Hour).Format(time.RFC3339)}))
		}, status: "error", reasons: []string{"unrecognized_format"}, details: []string{"~/.claude: 1 transcript holds no conversation record lookout recognizes"}},
		{name: "a transcript in a shape lookout does not know beside one it does", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			// Right after Claude Code changes its format, transcripts in both
			// shapes are in the window together.
			f.transcript(f.home(".claude"), app, sessionA, time.Hour, turn(sessionA, app, "feat", time.Hour))
			f.transcript(f.home(".claude"), app, sessionB, time.Hour,
				jsonLine(map[string]any{"type": "speech", "sessionId": sessionB, "cwd": app, "gitBranch": "other", "timestamp": now.Add(-time.Hour).Format(time.RFC3339)}),
				jsonLine(map[string]any{"type": "ai-title", "sessionId": sessionB, "aiTitle": "New format"}))
		}, status: "degraded", reasons: []string{"unrecognized_format"}, details: []string{"~/.claude: 1 transcript holds no conversation record lookout recognizes"}, items: 1},
		{name: "a transcript of a session not yet prompted", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour,
				jsonLine(map[string]any{"type": "mode", "sessionId": sessionA}),
				jsonLine(map[string]any{"type": "permission-mode", "sessionId": sessionA}))
		}, status: "ok"},
		{name: "a metadata record of a type lookout does not read, in a shape it does not expect", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour,
				turn(sessionA, app, "feat", time.Hour),
				jsonLine(map[string]any{"type": "pr-link", "sessionId": sessionA, "timestamp": 12345, "cwd": []any{}}))
		}, status: "ok", items: 1},
		{name: "a record dated where no readout can carry it", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour,
				turn(sessionA, app, "feat", time.Hour),
				jsonLine(map[string]any{"type": "user", "sessionId": sessionA, "cwd": app, "gitBranch": "early", "timestamp": "0000-01-01T00:00:00+01:00"}))
		}, status: "degraded", reasons: []string{"unrecognized_format"}, details: []string{"~/.claude: 1 transcript line is not in a shape lookout reads"}, items: 1},
		{name: "a project directory that cannot be looked into", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour, turn(sessionA, app, "feat", time.Hour))
			other := f.home("code/other")
			f.transcript(f.home(".claude"), other, sessionB, time.Hour, turn(sessionB, other, "x", time.Hour))
			f.locked(filepath.Join(f.home(".claude"), "projects", projectDir(other)))
		}, status: "degraded", reasons: []string{"unreadable"}, details: []string{"~/.claude: 1 project directory could not be looked into"}, items: 1},
		{name: "a partial last line and an unknown record type", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			path := f.transcript(f.home(".claude"), app, sessionA, time.Hour,
				jsonLine(map[string]any{"type": "speech", "sessionId": sessionA}), turn(sessionA, app, "feat", time.Hour))
			fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				f.t.Fatal(err)
			}
			if _, err := fh.WriteString(`{"type":"user","cwd":`); err != nil {
				f.t.Fatal(err)
			}
			if err := fh.Close(); err != nil {
				f.t.Fatal(err)
			}
			at := now.Add(-time.Hour)
			if err := os.Chtimes(path, at, at); err != nil {
				f.t.Fatal(err)
			}
		}, status: "ok", items: 1},
		{name: "a line longer than any default buffer", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			long := jsonLine(map[string]any{"type": "user", "sessionId": sessionA, "cwd": app, "gitBranch": "feat",
				"timestamp": now.Add(-time.Hour).Format(time.RFC3339), "message": strings.Repeat("x", 2<<20)})
			f.transcript(f.home(".claude"), app, sessionA, time.Hour, long)
		}, status: "ok", items: 1},
		{name: "a record dated ahead of the clock", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, 0, turn(sessionA, app, "feat", -time.Hour))
		}, status: "degraded", reasons: []string{"clock_skew"}, details: []string{"alice/app:feat: a session record is dated ahead of this machine's clock"}, items: 1},
		{name: "a repository git cannot read is counted, not named", dirs: `["~/.claude"]`, build: func(f *fixture, app string) {
			f.transcript(f.home(".claude"), app, sessionA, time.Hour, turn(sessionA, app, "feat", time.Hour))
			broken := f.repo("code/mallory-broken", "git@github.com:mallory/x.git")
			if err := os.WriteFile(filepath.Join(broken, ".git", "config"), []byte("[[[\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
			f.transcript(f.home(".claude"), broken, sessionB, time.Hour, turn(sessionB, broken, "mallory", time.Hour))
		}, status: "degraded", reasons: []string{"git_failed"}, details: []string{"1 working directory is in a repository git could not read"}, items: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t).withGit()
			config := strings.Replace(claudeOnly, `["~/.claude"]`, tc.dirs, 1)
			if tc.expect {
				config = strings.Replace(config, "kind: claude_code", "kind: claude_code\n    expect_records: true", 1)
			}
			f.write("personal.yml", config)
			app := f.repo("code/app", "git@github.com:alice/app.git")
			if tc.build != nil {
				tc.build(f, app)
			}

			code, stdout, stderr := f.run("read", "--json")
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr)
			}
			validate(t, "readout.v1", stdout)
			if strings.Contains(stdout, "mallory") {
				t.Errorf("stdout names work outside the fence:\n%s", stdout)
			}
			var doc readoutDoc
			if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
				t.Fatal(err)
			}
			h := doc.health(t, "claude")
			if h.Status != tc.status {
				t.Errorf("status = %q, want %q (%+v)", h.Status, tc.status, h)
			}
			if want := tc.reasons; !slices.Equal(h.Reasons, append([]string{}, want...)) {
				t.Errorf("reasons = %v, want %v", h.Reasons, want)
			}
			if want := tc.details; !slices.Equal(h.Details, append([]string{}, want...)) {
				t.Errorf("details = %v, want %v", h.Details, want)
			}
			if h.RecordCount != tc.items || len(doc.Unplaced) != tc.items {
				t.Errorf("record_count = %d, unplaced = %v, want %d", h.RecordCount, unplacedIDs(doc), tc.items)
			}
		})
	}
}

func TestReadReportsGitMissingBehindAClaudeCodeMember(t *testing.T) {
	old := 20 * 24 * time.Hour
	f := newFixture(t).withGit()
	f.write("personal.yml", strings.Replace(strings.Replace(claudeOnly, "workstreams: []", "workstreams:\n  - name: search\n    members: [\"claude:alice/app:feature/old\"]", 1),
		`["~/.claude"]`, `["~/.claude", "~/.claude-gone"]`, 1))
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.transcript(f.home(".claude"), app, sessionA, old, turn(sessionA, app, "feature/old", old))
	f.env["PATH"] = t.TempDir()

	h := f.read().health(t, "claude")

	if h.Status != "error" || !slices.Contains(h.Reasons, "git_failed") ||
		!slices.Contains(h.Details, "git: not found on PATH") || !slices.Contains(h.Details, "~/.claude-gone: no such directory") {
		t.Errorf("health = %+v, want git missing reported beside the other findings", h)
	}
}

func TestReadReportsGitMissingForClaudeCode(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.transcript(f.home(".claude"), app, sessionA, time.Hour, turn(sessionA, app, "feat", time.Hour))
	f.env["PATH"] = t.TempDir()

	h := f.read().health(t, "claude")

	if h.Status != "error" || !slices.Equal(h.Reasons, []string{"git_failed"}) || !slices.Equal(h.Details, []string{"git: not found on PATH"}) {
		t.Errorf("health = %+v, want an error saying git was not found", h)
	}
}

func TestReadDoesNotExpectRecordsOfAClaudeCodeMember(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", strings.Replace(claudeOnly, "workstreams: []", "workstreams:\n  - name: search\n    members: [\"claude:alice/app:feature/gone\"]", 1))
	f.mkdir(".claude/projects")

	doc := f.read()

	if h := doc.health(t, "claude"); h.Status != "ok" {
		t.Errorf("health = %+v, want ok: a member whose sessions aged out is not a failure", h)
	}
	if len(doc.Workstreams) != 1 || !slices.Equal(doc.Workstreams[0].MembersUnread, []string{"claude:alice/app:feature/gone"}) {
		t.Errorf("workstreams = %+v, want the member unread", doc.Workstreams)
	}
}

func TestUnplacedClaudeCodeTokensAreAdoptable(t *testing.T) {
	f := newFixture(t).withGit()
	f.write("personal.yml", claudeOnly)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.transcript(f.home(".claude"), app, sessionA, time.Hour, turn(sessionA, app, "feature/x", time.Hour))

	code, stdout, stderr := f.run("unplaced")
	if code != 0 || !strings.HasPrefix(stdout, "claude:alice/app:feature/x\tfeature/x\t") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	token, _, _ := strings.Cut(stdout, "\t")
	if code, _, stderr := f.run("adopt", "--new", token, "search"); code != 0 {
		t.Fatalf("adopt exit code = %d, stderr = %q", code, stderr)
	}

	doc := f.read()
	if len(doc.Unplaced) != 0 || len(doc.Workstreams) != 1 || len(doc.Workstreams[0].Items) != 1 {
		t.Errorf("readout = %+v, want the session item in the new workstream", doc)
	}
}

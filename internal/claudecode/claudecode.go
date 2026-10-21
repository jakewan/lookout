// Package claudecode reads the sessions Claude Code keeps in its
// configuration directories as one input's items, one per repository and
// branch, admitting only sessions whose working directory lies in a
// repository a setting's owner fence allows.
//
// Claude Code's documentation calls its transcript format internal and says
// it changes between versions (code.claude.com/docs/en/sessions). So what
// lookout relies on is checked as it is read: a transcript whose records have
// moved out of the shape below lowers health to unrecognized_format rather
// than reading as fewer sessions.
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/fsx"
	"github.com/jakewan/lookout/internal/git"
	"github.com/jakewan/lookout/internal/health"
	"github.com/jakewan/lookout/internal/readout"
)

// Reader reads one invocation's Claude Code inputs.
type Reader struct {
	getenv func(string) string
	git    *git.Reader
}

// NewReader finds git on the invocation's PATH, which places each session's
// working directory in its repository.
func NewReader(getenv func(string) string) *Reader {
	return &Reader{getenv: getenv, git: git.NewReader(getenv)}
}

// transcriptName is a top-level session transcript: Claude Code names one by
// its session id. Subagent transcripts live a directory further down, and an
// orphaned copy carries a suffix, so neither matches.
var transcriptName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)

// conversational are the record types that carry a session's working
// directory, branch and time. Other types are metadata whose set grows
// between versions, so they are skipped rather than counted as unknown.
var conversational = []string{"user", "assistant", "attachment", "system"}

// line is the part of one transcript line lookout reads.
type line struct {
	Type            string          `json:"type"`
	SessionID       string          `json:"sessionId"`
	Cwd             *string         `json:"cwd"`
	GitBranch       *string         `json:"gitBranch"`
	Timestamp       *string         `json:"timestamp"`
	IsSidechain     bool            `json:"isSidechain"`
	AITitle         string          `json:"aiTitle"`
	WorktreeSession json.RawMessage `json:"worktreeSession"`
}

// place is one working directory and branch a session's records were made in.
type place struct{ cwd, branch string }

// worktree is a worktree a session recorded entering: where it was made from,
// and where it lived.
type worktree struct{ original, path string }

// session is what one or more transcripts of one session id say.
type session struct {
	id        string
	touches   map[place]time.Time
	title     string
	worktrees []worktree
}

// transcript is one listed transcript file of a configured directory.
type transcript struct {
	entry    int
	path     string
	modified time.Time
}

// tally is what reading one configured directory found that health reports.
type tally struct {
	listed        bool
	inWindow      int
	recognized    bool
	unreadable    int
	unrecognized  int
	missing       string
	notListed     string
	noProjectsDir bool
}

// Read reports in's items. Transcripts last modified within the discovery
// window are read first; one modified before it cannot hold a newer record,
// so it is opened only while a member is still unread.
func (r *Reader) Read(ctx context.Context, in config.Input, sc readout.Scope, now time.Time) readout.Reading {
	out := readout.Reading{Health: health.New(in.Name), Items: make([]readout.Item, 0)}
	tallies := make([]tally, len(in.ConfigDirs))
	var recent, old []transcript
	for i, entry := range in.ConfigDirs {
		files := r.list(entry, in.Dir, &tallies[i])
		for _, f := range files {
			f.entry = i
			if f.modified.Before(sc.Since) {
				old = append(old, f)
			} else {
				recent = append(recent, f)
				tallies[i].inWindow++
			}
		}
	}

	sessions := map[string]*session{}
	for _, f := range recent {
		if ctx.Err() != nil {
			return out
		}
		r.readFile(f, nil, sessions, &tallies[f.entry])
	}

	if len(sessions) > 0 && r.git.Missing() != "" {
		out.Health.Lower(health.Error, health.GitFailed)
		out.Health.Details = append(out.Health.Details, r.git.Missing())
		return out
	}
	places := newPlacer(r.git, sc.Owners)
	found := places.items(ctx, sessions, in.IgnoreBranches)
	// Only the transcripts in the window are counted, so whether the members
	// sent lookout further back never changes what health reports.
	fenced, unplaced, failed := places.counts()

	if unread := unreadMembers(sc.Members, found); len(unread) > 0 {
		older := map[string]*session{}
		for _, f := range old {
			if ctx.Err() != nil {
				return out
			}
			r.readFile(f, unread, older, &tallies[f.entry])
		}
		for id, it := range places.items(ctx, older, in.IgnoreBranches) {
			if prev, ok := found[id]; !ok || it.touched.After(prev.touched) {
				found[id] = it
			}
		}
	}
	if ctx.Err() != nil {
		return out
	}

	readAny := false
	var failures []string
	for i, entry := range in.ConfigDirs {
		t := tallies[i]
		switch {
		case t.missing != "":
			failures = append(failures, health.Missing)
			out.Health.Details = append(out.Health.Details, entry+": "+t.missing)
			continue
		case t.notListed != "":
			failures = append(failures, health.Unreadable)
			out.Health.Details = append(out.Health.Details, entry+": "+t.notListed)
			continue
		case t.noProjectsDir:
			out.Health.Details = append(out.Health.Details, entry+": has no projects directory, so it holds no sessions")
		}
		if t.listed && (t.inWindow == 0 || t.recognized) {
			readAny = true
		}
		if t.unreadable > 0 {
			failures = append(failures, health.Unreadable)
			out.Health.Details = append(out.Health.Details, entry+": "+plural(t.unreadable, "transcript could not be read", "transcripts could not be read"))
		}
		if t.unrecognized > 0 {
			failures = append(failures, health.UnrecognizedFormat)
			out.Health.Details = append(out.Health.Details, entry+": "+plural(t.unrecognized, "transcript line is not in a shape lookout reads", "transcript lines are not in a shape lookout reads"))
		}
		if t.inWindow > 0 && !t.recognized && t.unreadable < t.inWindow {
			failures = append(failures, health.UnrecognizedFormat)
			out.Health.Details = append(out.Health.Details, entry+": no transcript holds a conversation record lookout recognizes")
		}
	}
	out.Health.ExcludedCount = fenced
	if unplaced > 0 {
		out.Health.Details = append(out.Health.Details, plural(unplaced, "working directory no longer exists or is in no repository", "working directories no longer exist or are in no repository"))
	}
	if failed > 0 {
		failures = append(failures, health.GitFailed)
		out.Health.Details = append(out.Health.Details, plural(failed, "working directory is in a repository git could not read", "working directories are in repositories git could not read"))
	}
	status := health.Degraded
	if !readAny {
		status = health.Error
	}
	for _, reason := range failures {
		out.Health.Lower(status, reason)
	}
	if readAny {
		at := now.UTC()
		out.Health.FetchedAt = &at
	}

	ids := make([]string, 0, len(found))
	for id, it := range found {
		if it.touched.Before(sc.Since) && !sc.Members[id] {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	horizon := now.Add(health.ClockTolerance)
	for _, id := range ids {
		it := found[id]
		if it.touched.After(horizon) {
			out.Health.Lower(health.Degraded, health.ClockSkew)
			out.Health.Details = append(out.Health.Details, id+": a session record is dated ahead of this machine's clock")
		}
		raw, err := json.Marshal(itemShape{
			ID: id, Title: it.title, LastTouched: it.touched.UTC(), State: "local", Links: make([]any, 0),
			Attrs: attrs{Repo: it.repo, Branch: it.branch, Sessions: it.sessionIDs()},
		})
		if err != nil {
			// A timestamp outside years 0000 to 9999 parses as RFC 3339 nowhere,
			// so a session item always encodes.
			continue
		}
		out.Items = append(out.Items, readout.Item{ID: id, Title: it.title, LastTouched: it.touched, Raw: raw})
	}
	out.Health.RecordCount = len(out.Items)
	if in.ExpectRecords && readAny && len(found) == 0 {
		out.Health.Lower(health.Degraded, health.Empty)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

type itemShape struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	LastTouched time.Time `json:"last_touched"`
	State       string    `json:"state"`
	Links       []any     `json:"links"`
	Attrs       attrs     `json:"attrs"`
}

type attrs struct {
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch,omitempty"`
	Sessions []string `json:"sessions"`
}

// list finds the transcripts under one configured directory. What it could
// not look into is tallied rather than taken for a directory with nothing in
// it.
func (r *Reader) list(entry, base string, t *tally) []transcript {
	dir := r.expand(entry, base)
	info, err := os.Stat(dir)
	switch {
	case fsx.Absent(err):
		t.missing = "no such directory"
		return nil
	case err != nil:
		t.notListed = "lookout could not look into it"
		return nil
	case !info.IsDir():
		t.missing = "not a directory"
		return nil
	}
	projects := filepath.Join(dir, "projects")
	entries, err := os.ReadDir(projects)
	switch {
	case fsx.Absent(err):
		t.listed, t.noProjectsDir = true, true
		return nil
	case err != nil:
		t.notListed = "lookout could not look into its projects directory"
		return nil
	}
	t.listed = true
	var out []transcript
	for _, project := range entries {
		if !project.IsDir() {
			continue
		}
		files, readErr := os.ReadDir(filepath.Join(projects, project.Name()))
		if readErr != nil {
			if !fsx.Absent(readErr) {
				t.unreadable++
			}
			continue
		}
		for _, f := range files {
			if !f.Type().IsRegular() || !transcriptName.MatchString(f.Name()) {
				continue
			}
			fi, infoErr := f.Info()
			if infoErr != nil {
				// Claude Code moves a transcript when its session enters or
				// leaves a worktree, so one listed a moment ago may be gone.
				if !fsx.Absent(infoErr) {
					t.unreadable++
				}
				continue
			}
			out = append(out, transcript{path: filepath.Join(projects, project.Name(), f.Name()), modified: fi.ModTime()})
		}
	}
	return out
}

// expand resolves a config_dirs entry as written: ~/ is the home directory,
// and a relative entry is read from the configuration's directory.
func (r *Reader) expand(entry, base string) string {
	switch {
	case entry == "~":
		return r.getenv("HOME")
	case strings.HasPrefix(entry, "~/"):
		return filepath.Join(r.getenv("HOME"), entry[2:])
	case !filepath.IsAbs(entry):
		return filepath.Join(base, entry)
	}
	return entry
}

// readFile adds one transcript's sessions to sessions. With branches given,
// a file whose bytes name none of them cannot hold the members being looked
// for and is skipped undecoded; a bare name can only match more often than
// the JSON-encoded one, so no file holding a member is skipped.
func (r *Reader) readFile(f transcript, branches []string, sessions map[string]*session, t *tally) {
	fh, err := os.Open(f.path)
	if err != nil {
		if !fsx.Absent(err) {
			t.unreadable++
		}
		return
	}
	defer func() {
		// A file opened only for reading has nothing a failed close loses.
		if cerr := fh.Close(); cerr != nil {
			return
		}
	}()
	var src io.Reader = fh
	if branches != nil {
		data, readErr := io.ReadAll(fh)
		if readErr != nil {
			t.unreadable++
			return
		}
		if !slices.ContainsFunc(branches, func(b string) bool { return bytes.Contains(data, []byte(b)) }) {
			return
		}
		src = bytes.NewReader(data)
	}
	if err := parse(src, strings.TrimSuffix(filepath.Base(f.path), ".jsonl"), sessions, t); err != nil {
		t.unreadable++
	}
}

// parse reads transcript lines into sessions. Lines are read whole however
// long they are; a real transcript holds lines of over a megabyte.
func parse(src io.Reader, fileID string, sessions map[string]*session, t *tally) error {
	br := bufio.NewReader(src)
	for {
		raw, err := br.ReadBytes('\n')
		last := errors.Is(err, io.EOF)
		if err != nil && !last {
			return err
		}
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 {
			var l line
			if jsonErr := json.Unmarshal(trimmed, &l); jsonErr != nil {
				// A last line without its newline is a session still being
				// written, or one that stopped mid-write, and loses one record.
				if !last {
					t.unrecognized++
				}
			} else {
				add(l, fileID, sessions, t)
			}
		}
		if last {
			return nil
		}
	}
}

// add takes one decoded line into its session.
func add(l line, fileID string, sessions map[string]*session, t *tally) {
	id := l.SessionID
	if id == "" {
		id = fileID
	}
	s := sessions[id]
	if s == nil {
		s = &session{id: id, touches: map[place]time.Time{}}
		sessions[id] = s
	}
	switch {
	case slices.Contains(conversational, l.Type):
		if l.IsSidechain {
			return
		}
		var at time.Time
		var tsErr error
		if l.Timestamp != nil {
			at, tsErr = time.Parse(time.RFC3339, *l.Timestamp)
		}
		if l.Cwd == nil || l.GitBranch == nil || l.Timestamp == nil || tsErr != nil || *l.Cwd == "" {
			t.unrecognized++
			return
		}
		t.recognized = true
		p := place{cwd: filepath.Clean(*l.Cwd), branch: *l.GitBranch}
		if prev, ok := s.touches[p]; !ok || at.After(prev) {
			s.touches[p] = at
		}
	case l.Type == "ai-title" && l.AITitle != "":
		s.title = l.AITitle
	case l.Type == "worktree-state" && len(l.WorktreeSession) > 0:
		// The record is internal and optional, so one in a shape lookout does
		// not know only leaves a removed worktree unrecovered.
		var w struct {
			OriginalCwd  string `json:"originalCwd"`
			WorktreePath string `json:"worktreePath"`
		}
		if json.Unmarshal(l.WorktreeSession, &w) == nil && w.OriginalCwd != "" && w.WorktreePath != "" {
			s.worktrees = append(s.worktrees, worktree{original: filepath.Clean(w.OriginalCwd), path: filepath.Clean(w.WorktreePath)})
		}
	}
}

// unreadMembers is the branch names of members found is missing, or nil when
// none is missing. A member with no branch could be in any transcript, so it
// stops the skip in readFile with an empty name every file contains.
func unreadMembers(members map[string]bool, found map[string]*item) []string {
	var out []string
	for id := range members {
		if _, ok := found[id]; ok {
			continue
		}
		_, branch, _ := strings.Cut(id, ":")
		out = append(out, branch)
	}
	return out
}

// item is one repository and branch, as the sessions on it touched it.
type item struct {
	title, repo, branch string
	touched             time.Time
	// sessions maps a session id to its newest record on this item.
	sessions map[string]time.Time
	// titledAt is when the session the title came from last touched it.
	titledAt time.Time
}

func (it *item) sessionIDs() []string {
	ids := make([]string, 0, len(it.sessions))
	for id := range it.sessions {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := it.sessions[b].Compare(it.sessions[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return ids
}

// placer places working directories relative to the owner fence, each once
// per read, and counts what it found.
type placer struct {
	git    *git.Reader
	owners []config.Owner
	cache  map[string]placement
	// seen holds each directory a session ran in, by what became of it, so a
	// directory is counted once however many sessions ran there.
	seen map[string]git.Placement
}

// placement is where one directory was placed, or failed set where git
// could not say.
type placement struct {
	loc    git.Location
	failed bool
}

func newPlacer(g *git.Reader, owners []config.Owner) *placer {
	return &placer{git: g, owners: owners, cache: map[string]placement{}, seen: map[string]git.Placement{}}
}

func (p *placer) locate(ctx context.Context, dir string) placement {
	if got, ok := p.cache[dir]; ok {
		return got
	}
	loc, err := p.git.Locate(ctx, dir, p.owners)
	got := placement{loc: loc, failed: err != nil}
	p.cache[dir] = got
	return got
}

// failedPlacement marks a directory git could not read in seen.
const failedPlacement = git.Placement(-1)

// place places cwd for s. A directory that no longer exists is tried again
// as a removed worktree: Claude Code removes the worktree of a session made
// with --worktree, which lives in <repository>/.claude/worktrees/<name>, when
// that session ends (code.claude.com/docs/en/worktrees), and a session that
// entered a worktree elsewhere may have recorded where it was made from.
func (p *placer) place(ctx context.Context, s *session, cwd string) placement {
	got := p.locate(ctx, cwd)
	if !got.failed && got.loc.Placement == git.Gone {
		for _, dir := range recoveries(s, cwd) {
			if again := p.locate(ctx, dir); again.failed || (again.loc.Placement != git.Gone && again.loc.Placement != git.Outside) {
				got = again
				break
			}
		}
	}
	if got.failed {
		p.seen[cwd] = failedPlacement
	} else {
		p.seen[cwd] = got.loc.Placement
	}
	return got
}

// recoveries are where a removed working directory's repository may still
// be found, most certain first.
func recoveries(s *session, cwd string) []string {
	var out []string
	marker := string(filepath.Separator) + filepath.Join(".claude", "worktrees") + string(filepath.Separator)
	if i := strings.Index(cwd+string(filepath.Separator), marker); i > 0 {
		out = append(out, cwd[:i])
	}
	for _, w := range s.worktrees {
		if cwd == w.path || strings.HasPrefix(cwd, w.path+string(filepath.Separator)) {
			out = append(out, w.original)
		}
	}
	return out
}

func (p *placer) counts() (fenced, unplaced, failed int) {
	for _, pl := range p.seen {
		switch pl {
		case git.Fenced:
			fenced++
		case git.Gone, git.Outside:
			unplaced++
		case failedPlacement:
			failed++
		}
	}
	return fenced, unplaced, failed
}

// items turns sessions into items, one per repository and branch.
func (p *placer) items(ctx context.Context, sessions map[string]*session, ignore []string) map[string]*item {
	found := map[string]*item{}
	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, sid := range ids {
		if ctx.Err() != nil {
			return found
		}
		s := sessions[sid]
		// A generated title summarizes the whole session, so it is only this
		// item's when every record of the session is.
		itemIDs := map[string]bool{}
		single := true
		type touch struct {
			id, repo, branch string
			at               time.Time
		}
		var touches []touch
		for pl, at := range s.touches {
			got := p.place(ctx, s, pl.cwd)
			if got.failed || got.loc.Placement != git.Placed {
				single = false
				continue
			}
			branch := pl.branch
			if branch == "HEAD" {
				branch = ""
			}
			if branch != "" && ignored(branch, ignore) {
				single = false
				continue
			}
			id := got.loc.Prefix
			if branch != "" {
				id += ":" + branch
			}
			itemIDs[id] = true
			touches = append(touches, touch{id: id, repo: got.loc.Root, branch: branch, at: at})
		}
		single = single && len(itemIDs) == 1
		for _, tc := range touches {
			it := found[tc.id]
			if it == nil {
				title := tc.branch
				if title == "" {
					title = strings.SplitN(tc.id, ":", 2)[0]
				}
				it = &item{title: title, branch: tc.branch, sessions: map[string]time.Time{}}
				found[tc.id] = it
			}
			if tc.at.After(it.touched) || it.repo == "" {
				it.touched = maxTime(it.touched, tc.at)
				if !tc.at.Before(it.touched) {
					it.repo = tc.repo
				}
			}
			if prev, ok := it.sessions[s.id]; !ok || tc.at.After(prev) {
				it.sessions[s.id] = tc.at
			}
			if single && s.title != "" && !tc.at.Before(it.titledAt) {
				it.title, it.titledAt = s.title, tc.at
			}
		}
	}
	return found
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ignored reports whether a branch matches one of the configured patterns,
// where * stops at a slash as path.Match has it.
func ignored(name string, patterns []string) bool {
	for _, p := range patterns {
		// Config.Parse refused a malformed pattern, so a Match error is only
		// reached by an Input built some other way, and ignores nothing.
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

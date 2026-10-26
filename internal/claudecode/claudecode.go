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

// line is one transcript line, each field kept raw so that a metadata record
// whose fields change type is skipped like any other metadata, and only the
// records lookout reads are held to a shape.
type line struct {
	Type            json.RawMessage `json:"type"`
	SessionID       json.RawMessage `json:"sessionId"`
	Cwd             json.RawMessage `json:"cwd"`
	GitBranch       json.RawMessage `json:"gitBranch"`
	Timestamp       json.RawMessage `json:"timestamp"`
	IsSidechain     json.RawMessage `json:"isSidechain"`
	AITitle         json.RawMessage `json:"aiTitle"`
	WorktreeSession json.RawMessage `json:"worktreeSession"`
}

// text is raw as a JSON string, or false where it is absent or anything else,
// null included, which json.Unmarshal would take for an empty string.
func text(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
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

// newest is s's latest record.
func (s *session) newest() time.Time {
	var at time.Time
	for _, t := range s.touches {
		if t.After(at) {
			at = t
		}
	}
	return at
}

// transcript is one listed transcript file of a configured directory.
type transcript struct {
	entry    int
	path     string
	modified time.Time
}

// tally is what reading one configured directory found that health reports.
// readable counts only the window's transcripts, since whether the input was
// read at all is judged there; failures found in older transcripts read for a
// member still count against it.
type tally struct {
	listed         bool
	noProjectsDir  bool
	missing        string
	notListed      string
	inWindow       int
	readable       int
	projectDirs    int
	listedDirs     int
	drifted        int
	unreadable     int
	unreadableDirs int
	unrecognized   int
}

// Read reports in's items. Transcripts last modified within the discovery
// window are read first; one modified before it cannot hold a newer record,
// so it is opened only while a member is still unread.
func (r *Reader) Read(ctx context.Context, in config.Input, sc readout.Scope, now time.Time) readout.Reading {
	out := readout.Reading{Health: health.New(in.Name), Items: make([]readout.Item, 0)}
	tallies := make([]tally, len(in.ConfigDirs))
	var recent, old []transcript
	for i, entry := range in.ConfigDirs {
		for _, f := range r.list(entry, in.Dir, &tallies[i]) {
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
		r.readFile(f, nil, true, sessions, &tallies[f.entry])
	}
	places := newPlacer(r.git, sc.Owners)
	found := places.items(ctx, sessions, in.IgnoreBranches)
	// Only the window's transcripts are counted, so whether the members sent
	// lookout further back never changes these counts.
	fenced, unplaced := places.counts()
	inWindowItems := len(found)

	older := map[string]*session{}
	if unread := unreadMembers(sc.Members, found); len(unread) > 0 {
		for _, f := range old {
			if ctx.Err() != nil {
				return out
			}
			r.readFile(f, unread, false, older, &tallies[f.entry])
		}
		merge(found, places.items(ctx, older, in.IgnoreBranches))
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
		// A window with no transcripts in it was read only if the project
		// directories it could hold them in were.
		if t.listed && (t.readable > 0 || (t.inWindow == 0 && (t.projectDirs == 0 || t.listedDirs > 0))) {
			readAny = true
		}
		if t.unreadableDirs > 0 {
			failures = append(failures, health.Unreadable)
			out.Health.Details = append(out.Health.Details, entry+": "+plural(t.unreadableDirs, "project directory could not be looked into", "project directories could not be looked into"))
		}
		if t.unreadable > 0 {
			failures = append(failures, health.Unreadable)
			out.Health.Details = append(out.Health.Details, entry+": "+plural(t.unreadable, "transcript could not be read", "transcripts could not be read"))
		}
		if t.unrecognized > 0 {
			failures = append(failures, health.UnrecognizedFormat)
			out.Health.Details = append(out.Health.Details, entry+": "+plural(t.unrecognized, "transcript line is not in a shape lookout reads", "transcript lines are not in a shape lookout reads"))
		}
		if t.drifted > 0 {
			failures = append(failures, health.UnrecognizedFormat)
			out.Health.Details = append(out.Health.Details, entry+": "+plural(t.drifted, "transcript holds no conversation record lookout recognizes", "transcripts hold no conversation record lookout recognizes"))
		}
	}
	out.Health.ExcludedCount = fenced
	if unplaced > 0 {
		out.Health.Details = append(out.Health.Details, plural(unplaced, "working directory no longer exists or is in no repository", "working directories no longer exist or are in no repository"))
	}
	status := health.Degraded
	if !readAny {
		status = health.Error
	}
	switch {
	case r.git.Missing() != "" && len(sessions)+len(older) > 0:
		// No session can be placed without git, so nothing it would have
		// shown was read.
		out.Health.Lower(health.Error, health.GitFailed)
		out.Health.Details = append(out.Health.Details, r.git.Missing())
	case places.failed() > 0:
		failures = append(failures, health.GitFailed)
		out.Health.Details = append(out.Health.Details, plural(places.failed(), "working directory is in a repository git could not read", "working directories are in repositories git could not read"))
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
			// add refuses a record dated outside years 0001 to 9999, the range
			// a readout timestamp can carry, so this is not expected; were it
			// reached, the item is reported rather than silently dropped.
			out.Health.Lower(health.Degraded, health.UnrecognizedFormat)
			out.Health.Details = append(out.Health.Details, id+": its sessions' dates cannot be written in a readout")
			continue
		}
		out.Items = append(out.Items, readout.Item{ID: id, Title: it.title, LastTouched: it.touched, Key: sc.Keys.Of(it.branch), Raw: raw})
	}
	out.Health.RecordCount = len(out.Items)
	// Older transcripts are read only for a member, so judging emptiness on
	// the window alone keeps it from depending on what the workstreams name.
	if in.ExpectRecords && readAny && inWindowItems == 0 {
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
				t.projectDirs++
				t.unreadableDirs++
			}
			continue
		}
		t.projectDirs++
		t.listedDirs++
		for _, f := range files {
			if !f.Type().IsRegular() || !transcriptName.MatchString(f.Name()) {
				continue
			}
			fi, infoErr := f.Info()
			if infoErr != nil {
				// A transcript listed a moment ago may have moved; see readFile.
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

// readFile adds one transcript's sessions to sessions and tallies what it
// found. With branches given, a file whose bytes name none of them cannot hold
// the members being looked for and is skipped undecoded. inWindow says the
// file is one of the window's, which alone decide whether the input was read.
func (r *Reader) readFile(f transcript, branches []string, inWindow bool, sessions map[string]*session, t *tally) {
	// Claude Code moves a transcript when its session enters or leaves a
	// worktree, so one listed a moment ago may be gone.
	read, err := readTranscript(f.path, branches, sessions, t)
	switch {
	case err != nil && !fsx.Absent(err):
		t.unreadable++
	case err != nil:
	case read.drifted():
		t.drifted++
	case inWindow:
		t.readable++
	}
}

func readTranscript(path string, branches []string, sessions map[string]*session, t *tally) (fileRead, error) {
	fh, err := os.Open(path)
	if err != nil {
		return fileRead{}, err
	}
	var src io.Reader = fh
	if branches != nil {
		data, readErr := io.ReadAll(fh)
		if readErr != nil {
			return fileRead{}, errors.Join(readErr, fh.Close())
		}
		if !slices.ContainsFunc(needles(branches), func(n []byte) bool { return bytes.Contains(data, n) }) {
			return fileRead{}, fh.Close()
		}
		src = bytes.NewReader(data)
	}
	read, parseErr := parse(src, strings.TrimSuffix(filepath.Base(path), ".jsonl"), sessions, t)
	return read, errors.Join(parseErr, fh.Close())
}

// needles are the byte strings a transcript holding one of branches must
// contain: each name as written and as JSON escapes it in a string, since a
// branch name may hold a quote or a backslash.
func needles(branches []string) [][]byte {
	var out [][]byte
	for _, b := range branches {
		out = append(out, []byte(b))
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(b) == nil {
			out = append(out, bytes.TrimSuffix(bytes.TrimSuffix(bytes.TrimPrefix(buf.Bytes(), []byte(`"`)), []byte("\n")), []byte(`"`)))
		}
	}
	return out
}

// fileRead is what one transcript's lines held, for judging the file as a
// whole: one with no conversation record, but with records that only a
// conversation makes, has had its conversation renamed out from under
// lookout. A transcript of a session nobody has prompted yet holds neither.
type fileRead struct {
	conversation, promptedOnly bool
}

func (f fileRead) drifted() bool { return !f.conversation && f.promptedOnly }

// parse reads transcript lines into sessions. Lines are read whole however
// long they are; a real transcript holds lines of over a megabyte. A session
// joins sessions only once one of its conversation records is read, so a
// transcript of metadata alone names no session to place.
func parse(src io.Reader, fileID string, sessions map[string]*session, t *tally) (fileRead, error) {
	var read fileRead
	local := map[string]*session{}
	br := bufio.NewReader(src)
	for {
		raw, err := br.ReadBytes('\n')
		last := errors.Is(err, io.EOF)
		if err != nil && !last {
			return read, err
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
				add(l, fileID, local, &read, t)
			}
		}
		if last {
			break
		}
	}
	for id, s := range local {
		if len(s.touches) == 0 {
			continue
		}
		into := sessions[id]
		if into == nil {
			sessions[id] = s
			continue
		}
		if s.title != "" && (into.title == "" || s.newest().After(into.newest())) {
			into.title = s.title
		}
		for p, at := range s.touches {
			if prev, ok := into.touches[p]; !ok || at.After(prev) {
				into.touches[p] = at
			}
		}
		into.worktrees = append(into.worktrees, s.worktrees...)
	}
	return read, nil
}

// add takes one decoded line into its session.
func add(l line, fileID string, sessions map[string]*session, read *fileRead, t *tally) {
	kind, ok := text(l.Type)
	if !ok {
		t.unrecognized++
		return
	}
	id, ok := text(l.SessionID)
	if !ok || id == "" {
		id = fileID
	}
	s := sessions[id]
	if s == nil {
		s = &session{id: id, touches: map[place]time.Time{}}
		sessions[id] = s
	}
	switch {
	case slices.Contains(conversational, kind):
		var sidechain bool
		if len(l.IsSidechain) > 0 && json.Unmarshal(l.IsSidechain, &sidechain) != nil {
			t.unrecognized++
			return
		}
		if sidechain {
			return
		}
		cwd, cwdOK := text(l.Cwd)
		branch, branchOK := text(l.GitBranch)
		stamp, stampOK := text(l.Timestamp)
		at, tsErr := time.Parse(time.RFC3339, stamp)
		// A readout carries years 0001 to 9999, and RFC 3339 admits year 0000
		// and offsets that carry a time out of that range.
		year := at.UTC().Year()
		if !cwdOK || cwd == "" || !branchOK || !stampOK || tsErr != nil || year < 1 || year > 9999 {
			t.unrecognized++
			return
		}
		read.conversation = true
		p := place{cwd: filepath.Clean(cwd), branch: branch}
		if prev, seen := s.touches[p]; !seen || at.After(prev) {
			s.touches[p] = at
		}
	case kind == "ai-title":
		read.promptedOnly = true
		if title, titled := text(l.AITitle); titled && title != "" {
			s.title = title
		}
	case kind == "last-prompt":
		read.promptedOnly = true
	case kind == "worktree-state" && len(l.WorktreeSession) > 0:
		// The record is internal and optional, so one in a shape lookout does
		// not know only leaves a removed worktree unrecovered.
		var w struct {
			OriginalCwd  string `json:"originalCwd"`
			WorktreePath string `json:"worktreePath"`
		}
		if json.Unmarshal(l.WorktreeSession, &w) == nil && w.OriginalCwd != "" && w.WorktreePath != "" {
			s.worktrees = append(s.worktrees, worktree{original: filepath.Clean(w.OriginalCwd), path: filepath.Clean(w.WorktreePath)})
		}
	default:
		// A record of a type lookout does not know that carries a working
		// directory is most likely a conversation record under a new name.
		if _, hasCwd := text(l.Cwd); hasCwd {
			read.promptedOnly = true
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
	// newestSession is when the session the title was chosen from last
	// touched this item.
	newestSession time.Time
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

// fallback is the title of an item no session gives a title of its own.
func (it *item) fallback(id string) string {
	if it.branch != "" {
		return it.branch
	}
	prefix, _, _ := strings.Cut(id, ":")
	return prefix
}

// merge adds the items in more to found. An item both hold keeps its newest
// touch and title and gains the other's sessions.
func merge(found, more map[string]*item) {
	for id, it := range more {
		into := found[id]
		if into == nil {
			found[id] = it
			continue
		}
		if it.touched.After(into.touched) {
			into.touched, into.repo = it.touched, it.repo
		}
		if it.newestSession.After(into.newestSession) {
			into.title, into.newestSession = it.title, it.newestSession
		}
		for sid, at := range it.sessions {
			if prev, ok := into.sessions[sid]; !ok || at.After(prev) {
				into.sessions[sid] = at
			}
		}
	}
}

// placer places working directories relative to the owner fence, each once
// per read, and counts what it found.
type placer struct {
	git    *git.Reader
	owners []config.Owner
	cache  map[string]placement
	// seen holds each directory a session ran in by the best that became of
	// it in any session, so a directory one session's worktree record places
	// is not counted as lost because another session's cannot.
	seen map[string]int
}

// placement is where one directory was placed, or failed set where git
// could not say.
type placement struct {
	loc    git.Location
	failed bool
}

// Ranks of what became of a directory, best last.
const (
	rankLost = iota
	rankFailed
	rankFenced
	rankPlaced
)

func (p placement) rank() int {
	switch {
	case p.failed:
		return rankFailed
	case p.loc.Placement == git.Placed:
		return rankPlaced
	case p.loc.Placement == git.Fenced:
		return rankFenced
	}
	return rankLost
}

func newPlacer(g *git.Reader, owners []config.Owner) *placer {
	return &placer{git: g, owners: owners, cache: map[string]placement{}, seen: map[string]int{}}
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

// place places cwd for s. A directory that no longer exists is tried again
// as a removed worktree. By default, Claude Code keeps the worktree of a
// session made with --worktree in <repository>/.claude/worktrees/<name>, and
// removes it when an unnamed session with no changes ends
// (code.claude.com/docs/en/worktrees); a session that entered a worktree
// elsewhere may have recorded where it was made from.
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
	if prev, ok := p.seen[cwd]; !ok || got.rank() > prev {
		p.seen[cwd] = got.rank()
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

// counts is how many directories the sessions placed so far ran in that the
// fence kept out, and that are lost: gone, or in no repository.
func (p *placer) counts() (fenced, lost int) {
	for _, rank := range p.seen {
		switch rank {
		case rankFenced:
			fenced++
		case rankLost:
			lost++
		}
	}
	return fenced, lost
}

// failed is how many directories any session ran in lie in a repository git
// could not read. It is counted over every transcript read, the older ones a
// member sent lookout to included, since a failure there can hide the member.
func (p *placer) failed() int {
	n := 0
	for _, rank := range p.seen {
		if rank == rankFailed {
			n++
		}
	}
	return n
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
		pls := make([]place, 0, len(s.touches))
		for pl := range s.touches {
			pls = append(pls, pl)
		}
		slices.SortFunc(pls, func(a, b place) int {
			if c := strings.Compare(a.cwd, b.cwd); c != 0 {
				return c
			}
			return strings.Compare(a.branch, b.branch)
		})
		// A generated title summarizes the whole session, so it is only this
		// item's when every record of the session is.
		itemIDs := map[string]bool{}
		single := true
		type touch struct {
			id, repo, branch string
			at               time.Time
		}
		var touches []touch
		for _, pl := range pls {
			at := s.touches[pl]
			got := p.place(ctx, s, pl.cwd)
			if got.failed || got.loc.Placement != git.Placed {
				single = false
				continue
			}
			branch := pl.branch
			if branch == "HEAD" {
				branch = ""
			}
			if branch != "" && git.Ignored(branch, ignore) {
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
				it = &item{branch: tc.branch, sessions: map[string]time.Time{}}
				it.title = it.fallback(tc.id)
				found[tc.id] = it
			}
			if tc.at.After(it.touched) || it.repo == "" {
				it.touched, it.repo = tc.at, tc.repo
			}
			if prev, ok := it.sessions[s.id]; !ok || tc.at.After(prev) {
				it.sessions[s.id] = tc.at
			}
			// The title is the newest session's, or the item's own name where
			// that session gives none of its own.
			if !tc.at.Before(it.newestSession) {
				it.newestSession = tc.at
				it.title = it.fallback(tc.id)
				if single && s.title != "" {
					it.title = s.title
				}
			}
		}
	}
	return found
}

// Package git reads the local branches of git repositories as one input's
// items, admitting only repositories whose origin a setting's owner fence
// allows.
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
	"github.com/jakewan/lookout/internal/readout"
)

// Scope is what a setting admits from a git input.
type Scope struct {
	Owners []config.Owner
	// Since is the earliest touch an unclaimed branch may have and still be
	// offered; a branch Members names is read whatever its age.
	Since   time.Time
	Members map[string]bool
}

// Reader runs git for one invocation.
type Reader struct {
	getenv func(string) string
	git    string
}

// NewReader finds git on the invocation's PATH. exec.Command would search the
// PATH this process started with instead, which is not the environment the
// invocation was given.
func NewReader(getenv func(string) string) *Reader {
	r := &Reader{getenv: getenv}
	for _, dir := range filepath.SplitList(getenv("PATH")) {
		// A relative or empty element names the working directory, where a
		// git nobody installed may sit; os/exec refuses those for the same
		// reason.
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, "git")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			r.git = candidate
			break
		}
	}
	return r
}

// branch is one local branch as one repository holds it.
type branch struct {
	id        string
	name      string
	touched   time.Time
	repo      string
	worktrees []string
}

// outcome is what reading one repository came to. Every source ends in exactly
// one, so a source lookout could not look at is never counted as one it looked
// at and found nothing in.
type outcome int

const (
	// admitted: the owner fence let it in and its branches were read.
	admitted outcome = iota
	// excluded: it was read far enough for the fence to keep it out.
	excluded
	// failed: lookout could not read it far enough to say.
	failed
)

// source is one repository's outcome, keyed by its common directory once that
// is known, so a repository reached through several worktrees or entries is
// one source.
type source struct {
	outcome  outcome
	cause    string // why the fence kept it out, for an excluded source
	branches []branch
	problems []string
}

// Read reports in's branches. Each repos entry resolves to sources, each source
// to one outcome, and the health record is drawn from those outcomes in one
// place below. A source is named only as the configuration names it or once
// the owner fence admits it, so a repository the fence keeps out never
// reaches the readout by its path.
func (r *Reader) Read(ctx context.Context, in config.Input, sc Scope, now time.Time) readout.Reading {
	out := readout.Reading{Health: health.New(in.Name), Items: make([]readout.Item, 0)}
	if r.git == "" {
		out.Health.Lower(health.Error, health.GitFailed)
		out.Health.Details = append(out.Health.Details, "git: not found on PATH")
		return out
	}
	if problem := r.checkVersion(ctx); problem != "" {
		out.Health.Lower(health.Error, health.GitFailed)
		out.Health.Details = append(out.Health.Details, problem)
		return out
	}

	// entryFailures are the reasons configured entries could not be read in
	// full; whether they make the input degraded or in error depends on
	// whether any source was read at all, which is known only at the end.
	var entryFailures []string
	sources := map[string]*source{}
	for _, entry := range in.Repos {
		if ctx.Err() != nil {
			return out
		}
		dirs, unreadable, literal, reason, cause := r.resolve(entry, in.Dir)
		if reason != "" {
			entryFailures = append(entryFailures, reason)
			out.Health.Details = append(out.Health.Details, entry+": "+cause)
			continue
		}
		for _, dir := range dirs {
			src, first := r.classify(ctx, dir, sc, in.IgnoreBranches, sources)
			switch {
			case src.outcome == failed && (first || literal):
				unreadable++
			case src.outcome == excluded && literal:
				out.Health.Details = append(out.Health.Details, entry+": "+src.cause)
			}
		}
		if unreadable > 0 {
			entryFailures = append(entryFailures, health.GitFailed)
			switch {
			case literal:
				out.Health.Details = append(out.Health.Details, entry+": git could not read it")
			case unreadable == 1:
				out.Health.Details = append(out.Health.Details, entry+": 1 match could not be read")
			default:
				out.Health.Details = append(out.Health.Details, fmt.Sprintf("%s: %d matches could not be read", entry, unreadable))
			}
		}
	}

	readAny := false
	found := map[string]branch{}
	for _, src := range sources {
		switch src.outcome {
		case excluded:
			readAny = true
			out.Health.ExcludedCount++
		case admitted:
			readAny = true
			for _, p := range src.problems {
				out.Health.Lower(health.Degraded, health.GitFailed)
				out.Health.Details = append(out.Health.Details, p)
			}
			for _, b := range src.branches {
				if prev, ok := found[b.id]; !ok || b.touched.After(prev.touched) {
					found[b.id] = b
				}
			}
		}
	}
	// A configured source that failed makes what was read incomplete; when
	// nothing could be read, the input as a whole cannot be.
	status := health.Degraded
	if !readAny {
		status = health.Error
	}
	for _, reason := range entryFailures {
		out.Health.Lower(status, reason)
	}
	if readAny {
		at := now.UTC()
		out.Health.FetchedAt = &at
	}

	ids := make([]string, 0, len(found))
	for id, b := range found {
		if b.touched.Before(sc.Since) && !sc.Members[id] {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	horizon := now.Add(health.ClockTolerance)
	for _, id := range ids {
		b := found[id]
		skewed := b.touched.After(horizon)
		if skewed {
			out.Health.Lower(health.Degraded, health.ClockSkew)
			out.Health.Details = append(out.Health.Details, id+": its last commit is dated ahead of this machine's clock")
		}
		raw, err := json.Marshal(itemShape{
			ID: id, Title: b.name, LastTouched: b.touched.UTC(), State: "local",
			Links: make([]any, 0), Attrs: attrs{Repo: b.repo, Branch: b.name, Worktrees: b.worktrees},
		})
		if err != nil {
			// git accepts a commit date outside years 0000 to 9999, which no
			// readout timestamp can carry, so the branch is left out. One dated
			// past 9999 is already reported as skew.
			if !skewed {
				out.Health.Lower(health.Degraded, health.GitFailed)
				out.Health.Details = append(out.Health.Details, id+": its last commit's date cannot be written in a readout")
			}
			continue
		}
		out.Items = append(out.Items, readout.Item{ID: id, Title: b.name, LastTouched: b.touched, Raw: raw})
	}
	out.Health.RecordCount = len(out.Items)
	// Branches older than the discovery window are still branches, so a quiet
	// stretch is not an empty input.
	if in.ExpectRecords && readAny && len(found) == 0 {
		out.Health.Lower(health.Degraded, health.Empty)
	}
	return out
}

// classify reads the repository at dir to its outcome. A repository already
// classified through another of its directories is returned as it was, with
// first false, so it is counted once; a directory git could not even open
// has no common directory to recognize it by, and stands alone.
func (r *Reader) classify(ctx context.Context, dir string, sc Scope, ignore []string, sources map[string]*source) (*source, bool) {
	repo, err := r.open(ctx, dir)
	if err != nil {
		return &source{outcome: failed}, true
	}
	if src, ok := sources[repo.common]; ok {
		return src, false
	}
	src := &source{}
	sources[repo.common] = src
	if src.cause = fenceCause(repo, sc.Owners); src.cause != "" {
		src.outcome = excluded
		return src, true
	}
	if src.branches, src.problems, err = r.branches(ctx, dir, repo, ignore); err != nil {
		src.outcome, src.branches, src.problems = failed, nil, nil
		return src, true
	}
	src.outcome = admitted
	return src, true
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
	Repo      string   `json:"repo"`
	Branch    string   `json:"branch"`
	Worktrees []string `json:"worktrees"`
}

// resolve finds the repositories one repos entry names: a literal path must be
// one, and a pattern keeps only the matches that are. Where it finds none, it
// returns the health reason and the cause to report against the entry.
// unreadable counts the places a pattern could not look into, so they are
// reported rather than taken for places with nothing in them.
func (r *Reader) resolve(entry, base string) (dirs []string, unreadable int, literal bool, reason, cause string) {
	prefix, rest := r.split(entry, base)
	if !hasMeta(rest) {
		path := filepath.Join(prefix, rest)
		switch kind, err := probe(path); {
		case errors.Is(err, fs.ErrNotExist):
			return nil, 0, true, health.Missing, "no such directory"
		case err != nil:
			return nil, 0, true, health.GitFailed, "git could not read it"
		case kind != repositoryDir:
			return nil, 0, true, health.NotARepository, "not a git repository"
		}
		return []string{path}, 0, true, "", ""
	}
	candidates, unreadable := match(prefix, rest)
	for _, c := range candidates {
		switch kind, err := probe(c); {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			unreadable++
		case kind == repositoryDir:
			dirs = append(dirs, c)
		}
	}
	if len(dirs) == 0 && unreadable == 0 {
		return nil, 0, false, health.Missing, "matches no repository"
	}
	return dirs, unreadable, false, "", ""
}

// match expands pattern, read from the directory prefix, one path element at a
// time. filepath.Glob is not used because it skips a directory it cannot
// read without saying so, and every repository beneath one would vanish
// unreported; match counts each such directory in unreadable instead. Only
// pattern is read as syntax, so prefix matches itself whatever its name
// holds.
func match(prefix, pattern string) (paths []string, unreadable int) {
	if prefix == "" {
		prefix = string(filepath.Separator)
	}
	paths = []string{prefix}
	for elem := range strings.SplitSeq(strings.Trim(pattern, string(filepath.Separator)), string(filepath.Separator)) {
		if elem == "" {
			continue
		}
		var next []string
		for _, dir := range paths {
			if !hasMeta(elem) {
				next = append(next, filepath.Join(dir, elem))
				continue
			}
			info, err := os.Stat(dir)
			switch {
			case errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()):
				continue
			case err != nil:
				unreadable++
				continue
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				unreadable++
				continue
			}
			for _, e := range entries {
				// Config.Parse refused a malformed pattern, so a Match error is
				// only reached by an Input built some other way, and matches
				// nothing.
				if ok, matchErr := filepath.Match(elem, e.Name()); matchErr == nil && ok {
					next = append(next, filepath.Join(dir, e.Name()))
				}
			}
		}
		paths = next
	}
	return paths, unreadable
}

// split separates the directory an entry is read from, its home directory for
// ~/ or the configuration's directory for a relative entry, from the entry as
// written, since only the latter may be a pattern.
func (r *Reader) split(entry, base string) (prefix, rest string) {
	switch {
	case entry == "~":
		return r.getenv("HOME"), ""
	case strings.HasPrefix(entry, "~/"):
		return r.getenv("HOME"), entry[2:]
	case !filepath.IsAbs(entry):
		return base, entry
	}
	return "", entry
}

func hasMeta(p string) bool { return strings.ContainsAny(p, `*?[\`) }

// dirKind is what probe finds at a path.
type dirKind int

const (
	otherDir dirKind = iota
	repositoryDir
)

// probe decides from the disk whether dir is a repository, not from git's exit
// status: git exits 128 alike for a plain directory and for a repository it
// refuses to read, and a broken repository must be reported rather than
// skipped. A checkout or linked worktree has a .git entry; a bare repository
// has HEAD and objects of its own. An error other than a missing entry means
// lookout could not look, which is not the same as finding nothing.
func probe(dir string) (dirKind, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return otherDir, err
	}
	if !info.IsDir() {
		return otherDir, nil
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return repositoryDir, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return otherDir, err
	}
	head, headErr := os.Stat(filepath.Join(dir, "HEAD"))
	objects, objErr := os.Stat(filepath.Join(dir, "objects"))
	for _, e := range []error{headErr, objErr} {
		if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return otherDir, e
		}
	}
	if headErr == nil && objErr == nil && head.Mode().IsRegular() && objects.IsDir() {
		return repositoryDir, nil
	}
	return otherDir, nil
}

// fenceCause says why the owner fence keeps repo out, or is "" where it admits
// it. The cause names no owner or host, since it may be about another
// context's repository.
func fenceCause(repo repository, owners []config.Owner) string {
	switch {
	case repo.noOrigin:
		return "no origin remote, so the owner fence keeps it out"
	case repo.origin == (remote{}):
		return "its origin is not a forge address, so the owner fence keeps it out"
	case !slices.Contains(owners, config.Owner{Host: repo.origin.host, Name: repo.origin.owner()}):
		return "its origin's owner is not in forge_owners"
	}
	return ""
}

// repository is what open learns about one repository before the owner fence
// decides whether its branches are read at all.
type repository struct {
	common   string
	bare     bool
	noOrigin bool
	origin   remote
}

func (r *Reader) open(ctx context.Context, dir string) (repository, error) {
	var repo repository
	out, err := r.run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repo, err
	}
	if repo.common, err = filepath.EvalSymlinks(strings.TrimSuffix(out, "\n")); err != nil {
		return repo, err
	}
	// Run in a linked worktree, --is-bare-repository describes the worktree,
	// which is never bare, so ask the repository itself.
	bare, err := r.run(ctx, dir, "--git-dir="+repo.common, "rev-parse", "--is-bare-repository")
	if err != nil {
		return repo, err
	}
	repo.bare = strings.TrimSpace(bare) == "true"

	names, err := r.run(ctx, dir, "remote")
	if err != nil {
		return repo, err
	}
	if !slices.Contains(strings.Fields(names), "origin") {
		repo.noOrigin = true
		return repo, nil
	}
	// get-url applies url.<base>.insteadOf, so the fence sees the address git
	// itself would fetch from.
	u, err := r.run(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return repo, err
	}
	var ok bool
	if repo.origin, ok = parseRemote(strings.TrimSpace(u)); !ok {
		// An origin that names no forge, such as a local mirror, belongs to no
		// owner the fence could admit.
		repo.origin = remote{}
	}
	return repo, nil
}

// branches lists repo's branches. A branch it cannot represent is left out and
// described in problems, so one odd branch does not hide the rest.
func (r *Reader) branches(ctx context.Context, dir string, repo repository, ignore []string) (_ []branch, problems []string, _ error) {
	def, err := r.defaultBranch(ctx, dir, repo.common, repo.bare)
	if err != nil {
		return nil, nil, err
	}
	refs, err := r.run(ctx, dir, "for-each-ref", "--format=%(refname)%00%(committerdate:unix)%00%(objecttype)", "refs/heads/")
	if err != nil {
		return nil, nil, err
	}
	worktrees, err := r.worktrees(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	prefix := repo.origin.path
	if repo.origin.host != "github.com" {
		prefix = repo.origin.host + "/" + prefix
	}

	var out []branch
	nonUTF8 := false
	// Refnames cannot hold a newline or NUL (git-check-ref-format(1)), so the
	// record and field separators cannot occur inside one.
	for line := range strings.SplitSeq(strings.TrimSpace(refs), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 || !strings.HasPrefix(fields[0], "refs/heads/") {
			return nil, nil, fmt.Errorf("for-each-ref printed %q", line)
		}
		if fields[2] != "commit" {
			continue
		}
		name := strings.TrimPrefix(fields[0], "refs/heads/")
		if name == def || ignored(name, ignore) {
			continue
		}
		// A refname may hold any bytes, but a readout is JSON, which would
		// rewrite invalid ones, leaving an id no command could name.
		if !utf8.ValidString(name) {
			if !nonUTF8 {
				problems = append(problems, prefix+": a branch whose name is not UTF-8 was left out")
				nonUTF8 = true
			}
			continue
		}
		// git prints a stored timestamp unsigned, so one past what int64 holds
		// is possible.
		secs, convErr := strconv.ParseInt(fields[1], 10, 64)
		if convErr != nil {
			problems = append(problems, prefix+":"+name+": git printed a commit date lookout cannot read")
			continue
		}
		where := worktrees[name]
		// A bare clone copies every branch of its remote into its own, so only
		// a branch a worktree has checked out is the user's own work.
		if repo.bare && len(where) == 0 {
			continue
		}
		if where == nil {
			where = make([]string, 0)
		}
		out = append(out, branch{id: prefix + ":" + name, name: name, touched: time.Unix(secs, 0), repo: dir, worktrees: where})
	}
	return out, problems, nil
}

// defaultBranch is the branch a repository's other work merges into, which is
// never itself a workstream's item: origin/HEAD where a fetch or clone set it,
// a bare repository's own HEAD, or else main or master. A repository with
// none of these hides no branch.
func (r *Reader) defaultBranch(ctx context.Context, dir, common string, bare bool) (string, error) {
	if out, ok, err := r.symbolicRef(ctx, dir, "", "refs/remotes/origin/HEAD"); err != nil || ok {
		return strings.TrimPrefix(out, "refs/remotes/origin/"), err
	}
	if bare {
		// The repository's own HEAD, not that of the worktree dir may be.
		if out, ok, err := r.symbolicRef(ctx, dir, "--git-dir="+common, "HEAD"); err != nil || ok {
			return strings.TrimPrefix(out, "refs/heads/"), err
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, err := r.run(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return name, nil
		} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
			return "", err
		}
	}
	return "", nil
}

// symbolicRef reads a symbolic ref; with --quiet, git-symbolic-ref(1) exits 1,
// and prints nothing, when the ref is not symbolic or does not exist.
func (r *Reader) symbolicRef(ctx context.Context, dir, gitDir, ref string) (string, bool, error) {
	args := []string{"symbolic-ref", "--quiet", ref}
	if gitDir != "" {
		args = append([]string{gitDir}, args...)
	}
	out, err := r.run(ctx, dir, args...)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// A refname may end in whitespace other than ASCII, so only the line
	// terminator git adds is removed.
	return strings.TrimSuffix(out, "\n"), true, nil
}

// worktrees maps each checked-out branch to the worktrees that have it. With
// -z, git-worktree(1) ends every line with NUL and every record with an empty
// line, so a path holding a newline cannot split a record. A detached or
// prunable worktree has no branch the readout can show.
func (r *Reader) worktrees(ctx context.Context, dir string) (map[string][]string, error) {
	out, err := r.run(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	found := map[string][]string{}
	var where, name string
	prunable := false
	for field := range strings.SplitSeq(out, "\x00") {
		switch {
		case field == "":
			if name != "" && !prunable {
				found[name] = append(found[name], where)
			}
			where, name, prunable = "", "", false
		case strings.HasPrefix(field, "worktree "):
			where = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "branch refs/heads/"):
			name = strings.TrimPrefix(field, "branch refs/heads/")
		case field == "prunable" || strings.HasPrefix(field, "prunable "):
			prunable = true
		}
	}
	return found, nil
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

// run runs one git command in dir. Its environment is built rather than
// inherited: hooks and wrappers export repository-local variables (those
// git rev-parse --local-env-vars lists, such as GIT_DIR), and any of them
// would point the command at another repository. GIT_CEILING_DIRECTORIES
// stops git from taking a directory inside some other checkout, such as a
// home directory kept in git, for part of that checkout.
func (r *Reader) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.git, append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{"LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)}
	for _, key := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"} {
		if v := r.getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.String(), nil
}

// minimumVersion is the oldest git that has worktree list -z, which branches
// relies on (git 2.36 release notes).
var minimumVersion = [2]int{2, 36}

// checkVersion reports a git too old for the commands lookout runs, so its
// failures are named for what they are; it says nothing where the version
// cannot be read, leaving those commands to fail on their own.
func (r *Reader) checkVersion(ctx context.Context) string {
	out, err := r.run(ctx, os.TempDir(), "version")
	if err != nil {
		return ""
	}
	v, ok := parseVersion(out)
	if !ok || v[0] > minimumVersion[0] || (v[0] == minimumVersion[0] && v[1] >= minimumVersion[1]) {
		return ""
	}
	return fmt.Sprintf("git: version %d.%d is older than the %d.%d lookout needs", v[0], v[1], minimumVersion[0], minimumVersion[1])
}

// parseVersion reads git version's "git version 2.53.0" line, ignoring any
// suffix a packager adds.
func parseVersion(out string) ([2]int, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(out), "git version ")
	if !ok {
		return [2]int{}, false
	}
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) < 2 {
		return [2]int{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return [2]int{}, false
	}
	digits := parts[1]
	if i := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		digits = digits[:i]
	}
	minor, err := strconv.Atoi(digits)
	if err != nil {
		return [2]int{}, false
	}
	return [2]int{major, minor}, true
}

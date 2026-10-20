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

// Read reports in's branches. Each problem lowers the health record rather
// than failing the run, and a source is named only as the configuration names
// it or once the owner fence admits it, so a repository the fence keeps out
// never reaches the readout by its path.
func (r *Reader) Read(ctx context.Context, in config.Input, sc Scope, now time.Time) readout.Reading {
	out := readout.Reading{Health: health.New(in.Name), Items: make([]readout.Item, 0)}
	if r.git == "" {
		out.Health.Lower(health.Error, health.GitFailed)
		out.Health.Details = append(out.Health.Details, "git: not found on PATH")
		return out
	}

	var failures []string
	readAny := false
	seen := map[string]bool{}
	found := map[string]branch{}
	fail := func(reason, detail string) {
		failures = append(failures, reason)
		out.Health.Details = append(out.Health.Details, detail)
	}

	for _, entry := range in.Repos {
		if ctx.Err() != nil {
			return out
		}
		dirs, literal, reason, cause := r.resolve(entry)
		if reason != "" {
			fail(reason, entry+": "+cause)
			continue
		}

		unreadable := 0
		for _, dir := range dirs {
			repo, err := r.open(ctx, dir)
			if err != nil {
				unreadable++
				continue
			}
			readAny = true
			if seen[repo.common] {
				continue
			}
			seen[repo.common] = true
			if repo.noOrigin || !admitted(repo.origin, sc.Owners) {
				out.Health.ExcludedCount++
				if literal {
					cause := "its origin's owner is not in forge_owners"
					if repo.noOrigin {
						cause = "no origin remote, so the owner fence keeps it out"
					}
					out.Health.Details = append(out.Health.Details, entry+": "+cause)
				}
				continue
			}
			branches, err := r.branches(ctx, dir, repo, in.IgnoreBranches)
			if err != nil {
				unreadable++
				continue
			}
			for _, b := range branches {
				if prev, ok := found[b.id]; !ok || b.touched.After(prev.touched) {
					found[b.id] = b
				}
			}
		}
		if unreadable > 0 {
			switch {
			case literal:
				fail(health.GitFailed, entry+": git could not read it")
			case unreadable == 1:
				fail(health.GitFailed, entry+": 1 repository could not be read")
			default:
				fail(health.GitFailed, fmt.Sprintf("%s: %d repositories could not be read", entry, unreadable))
			}
		}
	}

	// A configured source that failed makes what was read incomplete; when
	// nothing could be read, the input as a whole cannot be.
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
	if in.ExpectRecords && readAny && len(out.Items) == 0 {
		out.Health.Lower(health.Degraded, health.Empty)
	}
	return out
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
func (r *Reader) resolve(entry string) (dirs []string, literal bool, reason, cause string) {
	expanded := r.expand(entry)
	if !hasMeta(expanded) {
		info, err := os.Stat(expanded)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, true, health.Missing, "no such directory"
		case err != nil || !info.IsDir() || !looksLikeRepository(expanded):
			return nil, true, health.NotARepository, "not a git repository"
		}
		return []string{expanded}, true, "", ""
	}
	// Config.Parse refused a malformed pattern, so this is only reached by an
	// Input built some other way.
	matches, err := filepath.Glob(expanded)
	if err != nil {
		return nil, false, health.Missing, "not a valid pattern"
	}
	for _, m := range matches {
		if info, statErr := os.Stat(m); statErr == nil && info.IsDir() && looksLikeRepository(m) {
			dirs = append(dirs, m)
		}
	}
	if len(dirs) == 0 {
		return nil, false, health.Missing, "matches no repository"
	}
	return dirs, false, "", ""
}

// expand resolves a leading ~ to the invocation's home directory.
func (r *Reader) expand(entry string) string {
	home := r.getenv("HOME")
	switch {
	case entry == "~":
		return home
	case strings.HasPrefix(entry, "~/"):
		return filepath.Join(home, entry[2:])
	}
	return entry
}

func hasMeta(p string) bool { return strings.ContainsAny(p, `*?[\`) }

// looksLikeRepository decides from the disk whether dir is a repository, not
// from git's exit status: git exits 128 alike for a plain directory and for a
// repository it refuses to read, and a broken repository must be reported
// rather than skipped. A checkout or linked worktree has a .git entry; a bare
// repository has HEAD and objects of its own.
func looksLikeRepository(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	head, headErr := os.Stat(filepath.Join(dir, "HEAD"))
	objects, objErr := os.Stat(filepath.Join(dir, "objects"))
	return headErr == nil && objErr == nil && head.Mode().IsRegular() && objects.IsDir()
}

func admitted(o remote, owners []config.Owner) bool {
	return slices.Contains(owners, config.Owner{Host: o.host, Name: o.owner()})
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
	out, err := r.run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir", "--is-bare-repository")
	if err != nil {
		return repo, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return repo, fmt.Errorf("rev-parse printed %d lines", len(lines))
	}
	if repo.common, err = filepath.EvalSymlinks(lines[0]); err != nil {
		return repo, err
	}
	repo.bare = lines[1] == "true"

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

func (r *Reader) branches(ctx context.Context, dir string, repo repository, ignore []string) ([]branch, error) {
	def, err := r.defaultBranch(ctx, dir, repo.bare)
	if err != nil {
		return nil, err
	}
	refs, err := r.run(ctx, dir, "for-each-ref", "--format=%(refname)%00%(committerdate:unix)%00%(objecttype)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	worktrees, err := r.worktrees(ctx, dir)
	if err != nil {
		return nil, err
	}
	prefix := repo.origin.path
	if repo.origin.host != "github.com" {
		prefix = repo.origin.host + "/" + prefix
	}

	var out []branch
	// Refnames cannot hold a newline or NUL (git-check-ref-format(1)), so the
	// record and field separators cannot occur inside one.
	for line := range strings.SplitSeq(strings.TrimSpace(refs), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 || !strings.HasPrefix(fields[0], "refs/heads/") {
			return nil, fmt.Errorf("for-each-ref printed %q", line)
		}
		if fields[2] != "commit" {
			continue
		}
		name := strings.TrimPrefix(fields[0], "refs/heads/")
		if name == def || ignored(name, ignore) {
			continue
		}
		secs, convErr := strconv.ParseInt(fields[1], 10, 64)
		if convErr != nil {
			return nil, fmt.Errorf("for-each-ref printed date %q", fields[1])
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
	return out, nil
}

// defaultBranch is the branch a repository's other work merges into, which is
// never itself a workstream's item: origin/HEAD where a fetch or clone set it,
// a bare repository's own HEAD, or else main or master. A repository with
// none of these hides no branch.
func (r *Reader) defaultBranch(ctx context.Context, dir string, bare bool) (string, error) {
	if out, ok, err := r.symbolicRef(ctx, dir, "refs/remotes/origin/HEAD"); err != nil || ok {
		return strings.TrimPrefix(out, "refs/remotes/origin/"), err
	}
	if bare {
		if out, ok, err := r.symbolicRef(ctx, dir, "HEAD"); err != nil || ok {
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
func (r *Reader) symbolicRef(ctx context.Context, dir, ref string) (string, bool, error) {
	out, err := r.run(ctx, dir, "symbolic-ref", "--quiet", ref)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
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

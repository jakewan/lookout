// Command sweep is a development tool that fails a push carrying a term from a
// denylist kept outside the repository. It is not part of the lookout binary.
//
// The denylist lives outside the repository because a committed one would
// publish every name it exists to keep private. Each non-blank line not
// starting with # is a term, matched as a case-insensitive substring.
//
// It runs in three modes:
//
//   - sweep: the tracked tree, names and contents, plus every commit reachable
//     from HEAD that no remote holds.
//   - sweep --pre-push <remote>: the same tree, plus what git's pre-push hook
//     names on stdin — each ref being pushed, its name, an annotated tag's
//     own name, tagger and message, and every commit the destination remote
//     does not hold.
//   - sweep <file>...: only the named files, which is how a drafted pull
//     request or issue body is checked before it is posted.
//
// For each commit it checks the message, the author's and committer's names and
// email domains, any signed tag a merge embeds, the names it adds, copies to or
// renames to, and the lines it adds, including lines a merge's conflict
// resolution adds. Added names and
// lines matter even when a later commit removes them, because pushing
// publishes every commit, not only the last.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	exitClean = 0
	exitMatch = 1
	exitError = 2
)

// commitMarker opens each commit in a history listing. git emits the NUL
// itself from %x00 in the format, and a NUL cannot occur in a commit message
// or a text diff, so the marker is unambiguous; if git ever emitted it
// differently, no line would match and every commit would be read as one,
// mislabeling matches rather than missing them.
const commitMarker = "\x00commit "

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run sweeps and reports to stderr. A content match names its file and line,
// or its commit, and never the matched text. A name match prints the name,
// which contains the term, so sweep output is for the terminal, not for
// pasting anywhere public.
func run(ctx context.Context, args []string, stdin io.Reader, _, stderr io.Writer) int {
	diag := log.New(stderr, "sweep: ", 0)

	flags := flag.NewFlagSet("sweep", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prePush := flags.String("pre-push", "", "sweep what git's pre-push hook names on stdin, for this destination remote")
	pushURL := flags.String("url", "", "with --pre-push, the URL being pushed to (the hook's second argument)")
	if err := flags.Parse(args); err != nil {
		return exitError
	}
	files := flags.Args()
	if *prePush != "" && len(files) > 0 {
		diag.Print("--pre-push takes no file arguments")
		return exitError
	}

	path, err := patternPath()
	if err != nil {
		diag.Print(err)
		return exitError
	}
	terms, err := readTerms(path)
	if errors.Is(err, os.ErrNotExist) {
		diag.Printf("WARNING: no pattern file at %s", path)
		diag.Print("WARNING: nothing was swept; create it to enable the disclosure check")
		return exitClean
	}
	if err != nil {
		diag.Print(err)
		return exitError
	}
	if len(terms) == 0 {
		diag.Printf("WARNING: the pattern file at %s lists no terms", path)
		diag.Print("WARNING: nothing was swept; add terms to enable the disclosure check")
		return exitClean
	}

	s := &sweeper{ctx: ctx, terms: terms, diag: diag}
	if len(files) > 0 {
		err = s.files(files)
	} else {
		err = s.repository(stdin, *prePush, *pushURL)
	}
	if err != nil {
		diag.Print(err)
		return exitError
	}

	if s.matches > 0 {
		diag.Printf("%d match(es) against %s; refusing", s.matches, path)
		if s.historyMatches > 0 {
			diag.Print("a later commit cannot clear a match in an unpushed commit: rewrite the commit named above, then push again")
		}
		return exitMatch
	}
	return exitClean
}

// patternPath resolves the XDG config location by hand rather than through
// os.UserConfigDir, which on macOS answers ~/Library/Application Support and
// would put the file somewhere other than the documented path. A relative
// XDG_CONFIG_HOME is ignored, as the XDG base directory spec requires.
func patternPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating the pattern file: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "lookout-sweep", "patterns"), nil
}

func readTerms(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var terms []string
	for line := range strings.Lines(string(data)) {
		term := strings.TrimSpace(line)
		if term == "" || strings.HasPrefix(term, "#") {
			continue
		}
		terms = append(terms, strings.ToLower(term))
	}
	return terms, nil
}

type sweeper struct {
	ctx            context.Context
	terms          []string
	diag           *log.Logger
	matches        int
	historyMatches int
}

func (s *sweeper) check(location, text string) bool {
	lower := strings.ToLower(text)
	for _, term := range s.terms {
		if strings.Contains(lower, term) {
			s.diag.Printf("%s: matches a denylisted term", location)
			s.matches++
			return true
		}
	}
	return false
}

func (s *sweeper) checkHistory(location, text string) bool {
	if s.check(location, text) {
		s.historyMatches++
		return true
	}
	return false
}

func (s *sweeper) checkLines(label, text string) {
	n := 0
	for line := range strings.Lines(text) {
		n++
		s.check(fmt.Sprintf("%s:%d", label, n), line)
	}
}

// files sweeps the named files only. A file holding a NUL is refused rather
// than skipped: UTF-16 text reads that way, and passing it would report a body
// as checked when it never was.
func (s *sweeper) files(paths []string) error {
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return fmt.Errorf("%s is not plain text (it contains a NUL byte), so it cannot be swept; save it as UTF-8", p)
		}
		s.checkLines(p, string(data))
	}
	return nil
}

func (s *sweeper) repository(stdin io.Reader, prePush, pushURL string) error {
	top, err := s.git("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	// ls-files and the file reads below are relative to the working directory,
	// so a run from a subdirectory would otherwise cover only that subtree.
	if cerr := os.Chdir(strings.TrimSpace(top)); cerr != nil {
		return fmt.Errorf("changing to the repository root: %w", cerr)
	}
	if terr := s.tree(); terr != nil {
		return terr
	}

	if prePush == "" {
		if _, verr := s.git("rev-parse", "--verify", "--quiet", "HEAD"); verr != nil {
			return nil
		}
		return s.history([]string{"HEAD"}, []string{"--remotes"})
	}
	return s.pushed(stdin, prePush, pushURL)
}

// tree sweeps every tracked name and the content behind it. Binary content is
// matched as raw bytes, which catches text embedded in it but not text a format
// compresses or encodes as UTF-16. A symlink's published content is its target
// text, so that is what is checked; a submodule's directory holds nothing this
// repository publishes.
func (s *sweeper) tree() error {
	listing, err := s.git("ls-files", "-z")
	if err != nil {
		return err
	}
	for name := range strings.SplitSeq(strings.TrimSuffix(listing, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		s.check("path "+name, name)

		info, lerr := os.Lstat(name)
		switch {
		case errors.Is(lerr, fs.ErrNotExist):
			// Deleted in the working tree: its history is the commit sweep's.
			continue
		case lerr != nil:
			return fmt.Errorf("reading %s: %w", name, lerr)
		case info.Mode()&fs.ModeSymlink != 0:
			target, rerr := os.Readlink(name)
			if rerr != nil {
				return fmt.Errorf("reading %s: %w", name, rerr)
			}
			s.check(name+":1", target)
			continue
		case info.IsDir():
			continue
		}

		data, rerr := os.ReadFile(name)
		if rerr != nil {
			return fmt.Errorf("reading %s: %w", name, rerr)
		}
		if bytes.IndexByte(data, 0) >= 0 {
			s.check(name+" (binary)", string(data))
			continue
		}
		s.checkLines(name, string(data))
	}
	return nil
}

// pushed reads git's pre-push stdin — one "<local ref> <local sha> <remote
// ref> <remote sha>" line per ref (githooks(5)) — and sweeps each ref's name,
// an annotated tag's name, tagger and message, and every commit the
// destination does not hold.
//
// What the destination holds is asked of the destination itself, by listing
// its refs at the URL being pushed to. Local tracking refs are not consulted:
// they record where history was fetched from, which a separate push URL or a
// changed remote URL makes a different repository. Where the listing fails,
// nothing is excluded and everything outgoing is swept.
func (s *sweeper) pushed(stdin io.Reader, remote, pushURL string) error {
	var tips []string
	exclude := s.published(remote, pushURL)
	scanner := bufio.NewScanner(stdin)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 4 {
			continue
		}
		localRef, localSHA, remoteRef, remoteSHA := fields[0], fields[1], fields[2], fields[3]
		if isZero(localSHA) {
			continue // a deletion publishes nothing
		}
		s.check("ref "+localRef, localRef)
		if remoteRef != localRef {
			s.check("ref "+remoteRef, remoteRef)
		}
		if err := s.tag(localRef, localSHA); err != nil {
			return err
		}
		tips = append(tips, localSHA)
		if !isZero(remoteSHA) && s.exists(remoteSHA) {
			exclude = append(exclude, remoteSHA)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading the refs being pushed: %w", err)
	}
	if len(tips) == 0 {
		return nil
	}
	return s.history(tips, exclude)
}

// published lists the commits the destination's refs point at, among those
// this repository has, for use as history exclusions. Terminal prompts are
// off so an authentication challenge fails the listing instead of hanging the
// hook; the push itself authenticates separately.
func (s *sweeper) published(remote, pushURL string) []string {
	target := pushURL
	if target == "" {
		target = remote
	}
	cmd := exec.CommandContext(s.ctx, "git", "ls-remote", target)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		s.diag.Printf("could not list the refs at %s (%v); sweeping everything outgoing", target, err)
		return nil
	}
	var exclude []string
	for line := range strings.Lines(string(out)) {
		sha, _, ok := strings.Cut(line, "\t")
		if ok && s.exists(sha) {
			exclude = append(exclude, sha)
		}
	}
	return exclude
}

func isZero(sha string) bool { return strings.Trim(sha, "0") == "" }

func (s *sweeper) exists(sha string) bool {
	_, err := s.git("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

func (s *sweeper) tag(ref, sha string) error {
	kind, err := s.git("cat-file", "-t", sha)
	if err != nil {
		return err
	}
	if strings.TrimSpace(kind) != "tag" {
		return nil
	}
	body, err := s.git("cat-file", "tag", sha)
	if err != nil {
		return err
	}
	s.tagBody("tag "+ref, body, s.check)
	return nil
}

// tagBody sweeps a tag object's text. Its header carries the tag's own name
// ("tag <name>"), which need not match any ref pointing at it and is published
// with the object, and its tagger ("tagger Name <email> time zone"); the
// message follows the first blank line.
func (s *sweeper) tagBody(location, body string, check func(location, text string) bool) {
	header, message, _ := strings.Cut(body, "\n\n")
	for line := range strings.Lines(header) {
		line = strings.TrimSuffix(line, "\n")
		if name, ok := strings.CutPrefix(line, "tag "); ok {
			check(location+" name", name)
		}
		if rest, ok := strings.CutPrefix(line, "tagger "); ok {
			name, email, _ := strings.Cut(rest, " <")
			email, _, _ = strings.Cut(email, ">")
			s.identity(location+" tagger", name, email, check)
		}
	}
	for line := range strings.Lines(message) {
		check(location+" message", line)
	}
}

// identity sweeps a name and the domain of an email address. The local part is
// left out: git authorship publishes the author's own address by design, and a
// term that guards that address in prose would otherwise refuse every commit.
// The domain is what a work identity gives away.
func (s *sweeper) identity(location, name, email string, check func(location, text string) bool) {
	check(location+" name", name)
	if at := strings.LastIndex(email, "@"); at >= 0 {
		check(location+" email domain", email[at+1:])
	}
}

// history sweeps the commits reachable from tips and not from exclude, in
// three listings so no commit message can be mistaken for diff output.
func (s *sweeper) history(tips, exclude []string) error {
	rangeArgs := append(append([]string{}, tips...), append([]string{"--not"}, exclude...)...)

	messages, err := s.git(append([]string{"log", "--format=%x00commit %h%n%B"}, rangeArgs...)...)
	if err != nil {
		return err
	}
	var commit string
	for line := range strings.Lines(messages) {
		line = strings.TrimSuffix(line, "\n")
		if c, ok := strings.CutPrefix(line, commitMarker); ok {
			commit = c
			continue
		}
		s.checkHistory("commit "+commit+" message", line)
	}

	people, err := s.git(append([]string{"log", "--format=%h%x00%an%x00%ae%x00%cn%x00%ce"}, rangeArgs...)...)
	if err != nil {
		return err
	}
	for line := range strings.Lines(people) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), "\x00")
		if len(f) != 5 {
			continue
		}
		s.identity("commit "+f[0]+" author", f[1], f[2], s.checkHistory)
		s.identity("commit "+f[0]+" committer", f[3], f[4], s.checkHistory)
	}

	if merr := s.mergeTags(rangeArgs); merr != nil {
		return merr
	}

	// remerge shows what a merge's resolution adds beyond an automatic merge,
	// which is the only content a merge commit carries of its own.
	// --no-textconv keeps a configured converter from changing what is read,
	// and --text diffs binary files as text, so their bytes reach the hunks
	// instead of a one-line "Binary files differ".
	patch, err := s.git(append([]string{"log", "--patch", "--text", "--no-color", "--no-ext-diff", "--no-textconv", "--diff-merges=remerge", "--format=%x00commit %h"}, rangeArgs...)...)
	if err != nil {
		return err
	}
	inHunk := false
	for line := range strings.Lines(patch) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, commitMarker):
			commit = strings.TrimPrefix(line, commitMarker)
			inHunk = false
		case strings.HasPrefix(line, "diff "):
			inHunk = false // a file header, up to the first hunk
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			s.checkHistory("commit "+commit+" added line", line)
		}
	}

	return s.historyPaths(rangeArgs)
}

// mergeTags sweeps the signed tags merge commits embed in a mergetag header.
// None of the tag's text need appear in the commit's message, so the other
// listings never see it. In the raw format a commit starts at "commit <sha>"
// in column 0, its message is indented by four spaces, and a multi-line
// header continues on lines indented by one, so the three cannot be confused.
func (s *sweeper) mergeTags(rangeArgs []string) error {
	out, err := s.git(append([]string{"log", "--pretty=raw", "--no-patch"}, rangeArgs...)...)
	if err != nil {
		return err
	}
	var commit string
	var tag *strings.Builder
	flush := func() {
		if tag != nil {
			s.tagBody("commit "+commit+" mergetag", tag.String(), s.checkHistory)
			tag = nil
		}
	}
	for line := range strings.Lines(out) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "commit "):
			flush()
			commit = strings.TrimPrefix(line, "commit ")
			if len(commit) > 7 {
				commit = commit[:7]
			}
		case strings.HasPrefix(line, "mergetag "):
			flush()
			tag = &strings.Builder{}
			tag.WriteString(strings.TrimPrefix(line, "mergetag "))
			tag.WriteByte('\n')
		case tag != nil && strings.HasPrefix(line, " "):
			tag.WriteString(strings.TrimPrefix(line, " "))
			tag.WriteByte('\n')
		default:
			flush()
		}
	}
	flush()
	return nil
}

// historyPaths sweeps the names each commit adds, copies to, or renames to.
// Only the resulting name is checked: a rename's old name was either published
// already or added by an earlier commit in the range, which is swept on its
// own, so checking it here would refuse a push that removes a leak.
// quotePath is off so a non-ASCII name is listed as written rather than as
// octal escapes no term would match.
func (s *sweeper) historyPaths(rangeArgs []string) error {
	out, err := s.git(append([]string{"-c", "core.quotePath=false", "log", "--name-only", "--diff-filter=ACR", "--diff-merges=remerge", "--format=%x00commit %h"}, rangeArgs...)...)
	if err != nil {
		return err
	}
	var commit string
	for line := range strings.Lines(out) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, commitMarker):
			commit = strings.TrimPrefix(line, commitMarker)
		case line != "":
			s.checkHistory("commit "+commit+" path "+line, line)
		}
	}
	return nil
}

func (s *sweeper) git(args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(s.ctx, "git", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", subcommand(args), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// subcommand names the git subcommand in args for an error message, skipping
// the -c options that precede it.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

// Command sweep is a development tool that fails a push carrying a term from a
// denylist kept outside the repository. It is not part of the lookout binary.
//
// The denylist lives outside the repository because a committed one would
// publish every name it exists to keep private. Each non-blank line not
// starting with # is a term, matched as a case-insensitive substring.
//
// With no arguments it sweeps the tracked tree, names and contents, plus every
// commit not yet on any remote: each commit's message, the names it adds, and
// the lines it adds. Added names and lines matter even when a later commit
// removes them, because pushing publishes every commit, not only the last. With
// --file it sweeps only the named files, which is how a drafted pull request or
// issue body is checked before it is posted.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
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

// commitMarker opens each commit in the history listing. The NUL byte cannot
// occur in a commit message or a text diff, so the marker is unambiguous.
const commitMarker = "\x00commit "

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type fileList []string

func (f *fileList) String() string { return strings.Join(*f, ",") }

func (f *fileList) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// run sweeps and reports to stderr. A match names its location and never the
// term itself, so the output can be pasted or logged without leaking it.
func run(args []string, _, stderr io.Writer) int {
	diag := log.New(stderr, "sweep: ", 0)

	var files fileList
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&files, "file", "sweep only this file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() > 0 {
		diag.Printf("unexpected argument %q", fs.Arg(0))
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

	s := &sweeper{terms: terms, diag: diag}
	if len(files) > 0 {
		for _, f := range files {
			if serr := s.file(f, f); serr != nil {
				diag.Print(serr)
				return exitError
			}
		}
	} else if serr := s.repository(); serr != nil {
		diag.Print(serr)
		return exitError
	}

	if s.matches > 0 {
		diag.Printf("%d match(es) against %s; refusing", s.matches, path)
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
	terms   []string
	diag    *log.Logger
	matches int
}

func (s *sweeper) check(location, text string) {
	lower := strings.ToLower(text)
	for _, term := range s.terms {
		if strings.Contains(lower, term) {
			s.diag.Printf("%s: matches a denylisted term", location)
			s.matches++
			return
		}
	}
}

// file sweeps one file, skipping binary content, which carries no prose to leak
// and would only yield matches nobody can read.
func (s *sweeper) file(path, label string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", label, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	n := 0
	for line := range strings.Lines(string(data)) {
		n++
		s.check(fmt.Sprintf("%s:%d", label, n), line)
	}
	return nil
}

func (s *sweeper) repository() error {
	listing, err := git("ls-files", "-z")
	if err != nil {
		return err
	}
	for name := range strings.SplitSeq(strings.TrimSuffix(listing, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		s.check("path "+name, name)
		// A tracked file deleted in the working tree has nothing left to leak
		// from here; its history is the commit sweep's concern.
		if _, statErr := os.Stat(name); errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if ferr := s.file(name, name); ferr != nil {
			return ferr
		}
	}
	return s.history()
}

// history sweeps commits reachable from HEAD that no remote has. A commit any
// remote already holds is published, and failing this push cannot change that.
func (s *sweeper) history() error {
	if _, err := git("rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return nil
	}
	// %x00 asks git to emit the NUL itself; a NUL cannot travel in an argument.
	out, err := git("log", "--patch", "--no-color", "--no-ext-diff", "--format=%x00commit %h%n%B", "HEAD", "--not", "--remotes")
	if err != nil {
		return err
	}

	var commit string
	inDiff := false
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, commitMarker):
			commit = strings.TrimPrefix(line, commitMarker)
			inDiff = false
		case strings.HasPrefix(line, "diff --git "):
			inDiff = true
		case !inDiff:
			s.check("commit "+commit+" message", line)
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			s.check("commit "+commit+" added line", line)
		}
	}
	if serr := scanner.Err(); serr != nil {
		return fmt.Errorf("reading history: %w", serr)
	}
	return s.historyPaths()
}

// historyPaths sweeps the names each unpushed commit adds, copies to, or
// renames to. Only the resulting name is checked: a rename's old name was
// either published already or added by an earlier unpushed commit, which is
// swept on its own, so checking it here would refuse a push that removes a leak.
// quotePath is off so a non-ASCII name is listed as written rather than as
// octal escapes no term would match.
func (s *sweeper) historyPaths() error {
	out, err := git("-c", "core.quotePath=false", "log", "--name-only", "--diff-filter=ACR", "--format=%x00commit %h", "HEAD", "--not", "--remotes")
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
			s.check("commit "+commit+" path "+line, line)
		}
	}
	return nil
}

func git(args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Package hookspec runs a package's specs the way the pre-push hook runs them,
// so a spec that reaches past its own fixtures into the repository being
// pushed fails where every run can see it. Not part of the binary.
package hookspec

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// reentered marks the inner run, so the spec calling LeavesRepositoryAlone
// does not start another run from inside the one it started.
const reentered = "LOOKOUT_HOOKSPEC_REENTERED"

// LeavesRepositoryAlone reruns every spec in the calling package's test binary
// from a linked worktree of a throwaway repository, with GIT_DIR set as git
// exports it to a pre-push hook run there: the worktree's absolute git
// directory (githooks(5): "GIT_DIR, GIT_WORK_TREE, etc., are exported"; from
// the main checkout git leaves it unset). It fails if the rerun fails or
// changes the throwaway repository's config. A git init or git config that
// inherits that GIT_DIR writes the shared config, and core.bare=true there
// leaves the main checkout's files no longer following HEAD. If git stopped
// exporting GIT_DIR from a worktree, this spec would pass while guarding a
// condition the hook no longer produces.
func LeavesRepositoryAlone(t *testing.T) {
	t.Helper()
	if os.Getenv(reentered) != "" {
		t.Skip("inside the rerun this spec started")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	env := withoutRepositoryVariables(t)

	pushed := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "worktree")
	git(t, env, pushed, "init", "--quiet", "--initial-branch=main")
	git(t, env, pushed, "commit", "--quiet", "--allow-empty", "-m", "initial")
	git(t, env, pushed, "worktree", "add", "--quiet", "-b", "topic", worktree)
	gitDir := git(t, env, worktree, "rev-parse", "--absolute-git-dir")
	config := filepath.Join(pushed, ".git", "config")
	before := readFile(t, config)

	cmd := exec.Command(os.Args[0], "-test.count=1")
	cmd.Dir = worktree
	cmd.Env = slices.Concat(env, []string{"GIT_DIR=" + gitDir, reentered + "=1"})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the specs failed when run as a pre-push hook in a linked worktree runs them: %v\n%s", err, out)
	}
	if after := readFile(t, config); !bytes.Equal(before, after) {
		t.Fatalf("run as a pre-push hook in a linked worktree runs them, the specs rewrote the pushed repository's config:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// withoutRepositoryVariables is the process environment minus the variables
// git reads to locate a repository, which a hook running these specs exports.
// The list comes from git itself, so a variable a later git adds is covered.
func withoutRepositoryVariables(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Fatalf("listing git's repository variables: %v", err)
	}
	drop := map[string]bool{}
	for name := range strings.FieldsSeq(string(out)) {
		drop[name] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !drop[name] {
			env = append(env, kv)
		}
	}
	return env
}

// git builds the throwaway repository. Global and system config are ignored
// and commits are unsigned, so setup behaves the same on any machine.
func git(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	base := []string{"-c", "user.name=Example", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = slices.Concat(env, []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

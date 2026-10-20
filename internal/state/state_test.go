package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "personal.yml")
	if err := os.WriteFile(path, []byte("inputs: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func homeEnv(t *testing.T) (string, func(string) string) {
	home := t.TempDir()
	return home, func(k string) string { return map[string]string{"HOME": home}[k] }
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestAReaderWaitsForAGestureInProgress pins the lock that serializes one
// setting's invocations: a gesture holds it from reading state until it has
// recorded, so nothing it reads can change underneath it.
func TestAReaderWaitsForAGestureInProgress(t *testing.T) {
	_, getenv := homeEnv(t)
	cfg := newConfig(t)
	writer, err := Open(getenv, "personal", cfg, Write)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		reader, openErr := Open(getenv, "personal", cfg, Read)
		if openErr == nil {
			openErr = reader.Close()
		}
		done <- openErr
	}()
	select {
	case early := <-done:
		t.Fatalf("a reader opened the state while a gesture held it (%v)", early)
	case <-time.After(200 * time.Millisecond):
	}
	closeStore(t, writer)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("reader after the gesture: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reader never got the state once the gesture finished")
	}
}

func TestTwoConfigurationsCannotShareOneLog(t *testing.T) {
	home, getenv := homeEnv(t)
	a, b := newConfig(t), newConfig(t)
	first, err := Open(getenv, "personal", a, Write)
	if err != nil {
		t.Fatal(err)
	}
	if appendErr := first.Append(Gesture{At: time.Now(), Gesture: "dismiss", Outcome: OK, Item: "a:1"}); appendErr != nil {
		t.Fatal(appendErr)
	}
	closeStore(t, first)

	if _, openErr := Open(getenv, "personal", b, Write); openErr == nil || !strings.Contains(openErr.Error(), "belongs to") {
		t.Errorf("second configuration's Open = %v, want a refusal naming the owner", openErr)
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "state", "lookout", "personal", logName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "b:1") {
		t.Errorf("log = %s, want only the owner's gestures", data)
	}
}

// TestAReadDoesNotLoadAnotherConfigurationsLog covers a read of a setting with
// no state yet, which takes no lock, losing a race to another configuration's
// first gesture before it reads the log.
func TestAReadDoesNotLoadAnotherConfigurationsLog(t *testing.T) {
	_, getenv := homeEnv(t)
	a, b := newConfig(t), newConfig(t)
	beforeLogRead = func() {
		beforeLogRead = func() {}
		s, err := Open(getenv, "personal", a, Write)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Append(Gesture{At: time.Now(), Gesture: "dismiss", Outcome: OK, Item: "a:1"}); err != nil {
			t.Fatal(err)
		}
		closeStore(t, s)
	}
	t.Cleanup(func() { beforeLogRead = func() {} })

	if s, err := Open(getenv, "personal", b, Read); err == nil {
		closeStore(t, s)
		t.Errorf("Open succeeded on a directory another configuration claimed during it")
	}
}

// TestAClaimInProgressIsNeverSeenHalfWritten covers anything that looks at the
// owner without the lock while a claim is being published.
func TestAClaimInProgressIsNeverSeenHalfWritten(t *testing.T) {
	_, getenv := homeEnv(t)
	cfg := newConfig(t)
	first, err := Open(getenv, "personal", cfg, Write)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, first)
	var during error
	beforeOwnerPublished = func() {
		beforeOwnerPublished = func() {}
		during = (&Store{dir: first.dir, config: first.config}).checkOwner()
	}
	t.Cleanup(func() { beforeOwnerPublished = func() {} })

	if err := first.Claim(); err != nil {
		t.Fatal(err)
	}
	if during != nil && !errors.Is(during, fs.ErrNotExist) {
		t.Errorf("a look while the claim was being published failed: %v", during)
	}
}

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTwoConfigurationsCannotShareOneLog covers two first gestures from
// different files with the same setting name, both opened before either
// records: the second must not write into the first's log.
func TestTwoConfigurationsCannotShareOneLog(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	configs := []string{filepath.Join(t.TempDir(), "personal.yml"), filepath.Join(t.TempDir(), "personal.yml")}
	var stores []*Store
	for _, c := range configs {
		if err := os.WriteFile(c, []byte("inputs: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(getenv, "personal", c)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, s)
	}

	if err := stores[0].Claim(); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := stores[0].Append(Gesture{At: time.Now(), Gesture: "dismiss", Outcome: OK, Item: "a:1"}); err != nil {
		t.Fatal(err)
	}
	if err := stores[1].Claim(); err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Errorf("second claim = %v, want a refusal naming the owner", err)
	}
	if err := stores[1].Append(Gesture{At: time.Now(), Gesture: "dismiss", Outcome: OK, Item: "b:1"}); err == nil {
		t.Errorf("second append succeeded into another configuration's log")
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "state", "lookout", "personal", logName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "b:1") {
		t.Errorf("log = %s, want only the owner's gestures", data)
	}
}

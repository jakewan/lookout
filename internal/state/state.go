// Package state keeps one setting's tool history: the gesture log, which
// records every command that shapes the setting, and the dismissals replayed
// from it. Nothing here is meant to be edited by hand.
package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Outcomes a gesture can have. A refused gesture still cost the user a
// command, so it is recorded beside the ones that succeeded.
const (
	OK      = "ok"
	Refused = "refused"
	Failed  = "failed"
)

// Gesture is one command that shaped the setting, or tried to.
type Gesture struct {
	At          time.Time `json:"at"`
	Gesture     string    `json:"gesture"`
	Outcome     string    `json:"outcome"`
	Reason      string    `json:"reason,omitempty"`
	Item        string    `json:"item,omitempty"`
	Workstream  string    `json:"workstream,omitempty"`
	Workstreams []string  `json:"workstreams,omitempty"`
	New         bool      `json:"new,omitempty"`
}

const (
	logName = "gestures.jsonl"
	// ownerName records which configuration file the directory belongs to, since
	// two files with the same name in different places would otherwise share it.
	ownerName = "config"
)

// Store is one setting's state directory.
type Store struct {
	dir      string
	config   string
	gestures []Gesture
}

// Open reads the state for the setting configured at configPath. A directory
// that does not exist yet reads as empty. A log line that cannot be read fails
// the open rather than being skipped, which would silently undo dismissals.
func Open(getenv func(string) string, setting, configPath string) (*Store, error) {
	base, err := root(getenv)
	if err != nil {
		return nil, err
	}
	config, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", configPath, err)
	}
	s := &Store{dir: filepath.Join(base, setting), config: config}

	owner, err := os.ReadFile(filepath.Join(s.dir, ownerName))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("reading state: %w", err)
	case strings.TrimSpace(string(owner)) != config:
		return nil, fmt.Errorf("state directory %s belongs to configuration %s, not %s: give one of the two files a different name", s.dir, strings.TrimSpace(string(owner)), config)
	}

	data, err := os.ReadFile(filepath.Join(s.dir, logName))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the gesture log: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, len(data)+1)
	for n := 1; scanner.Scan(); n++ {
		var g Gesture
		if err := json.Unmarshal(scanner.Bytes(), &g); err != nil {
			return nil, fmt.Errorf("%s line %d cannot be read: lookout writes this file itself, so restore it from a backup or remove the line: %w", filepath.Join(s.dir, logName), n, err)
		}
		s.gestures = append(s.gestures, g)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading the gesture log: %w", err)
	}
	return s, nil
}

func root(getenv func(string) string) (string, error) {
	if base := getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "lookout"), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", "lookout"), nil
	}
	return "", errors.New("cannot find the state directory: neither XDG_STATE_HOME nor HOME is set")
}

// Append records g, creating the state directory on first use. The directory
// is private to its user because item ids name the user's own work.
func (s *Store) Append(g Gesture) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating the state directory: %w", err)
	}
	owner := filepath.Join(s.dir, ownerName)
	if _, err := os.Stat(owner); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(owner, []byte(s.config+"\n"), 0o600); err != nil {
			return fmt.Errorf("writing state: %w", err)
		}
	}
	g.At = g.At.UTC()
	line, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("encoding a gesture: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(s.dir, logName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening the gesture log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return errors.Join(fmt.Errorf("writing the gesture log: %w", err), f.Close())
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("writing the gesture log: %w", err)
	}
	s.gestures = append(s.gestures, g)
	return nil
}

// Dismissals maps each dismissed item, as <input>:<id>, to when it was last
// dismissed.
func (s *Store) Dismissals() map[string]time.Time {
	out := map[string]time.Time{}
	for _, g := range s.gestures {
		if g.Gesture == "dismiss" && g.Outcome == OK && g.At.After(out[g.Item]) {
			out[g.Item] = g.At
		}
	}
	return out
}

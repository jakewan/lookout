// Package state keeps one setting's tool history: the gesture log, which
// records every command that shapes the setting, the dismissals replayed from
// it, and the baseline of daily snapshots. Nothing here is meant to be edited
// by hand.
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
	"slices"
	"strings"
	"time"
)

// Outcomes a gesture can have. A refused gesture still cost the user a
// command, so it is recorded beside the ones that succeeded. Refused means
// lookout declined what was asked or how the file is written; failed is any
// other error, such as a configuration that does not parse or a file that
// cannot be written.
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
	// From names the workstreams an item was taken out of: the completed ones
	// an adopt moved it from, or every one a move or remove took it from.
	From []string `json:"from,omitempty"`
	// To is the name a rename gave the workstream it names.
	To string `json:"to,omitempty"`
	// Date is the local date a complete wrote, which its UTC time alone cannot
	// recover once the zone has changed.
	Date string `json:"date,omitempty"`
}

const (
	logName      = "gestures.jsonl"
	baselineName = "baseline.jsonl"
	// ownerName records which configuration file the directory belongs to, since
	// two files with the same name in different places would otherwise share it.
	ownerName = "config"
)

// beforeLogRead and beforeOwnerPublished run at the two points another
// invocation can interleave with this one; specs use them to do so.
var (
	beforeLogRead        = func() {}
	beforeOwnerPublished = func() {}
)

// Mode is how an invocation holds a setting's state.
type Mode int

const (
	// Read holds the state shared with other reads, and takes nothing where no
	// state exists yet, so a read never creates it.
	Read Mode = iota
	// Write holds the state alone, creating its directory, from reading it
	// until the gesture is recorded, so one setting's gestures never interleave.
	Write
)

// Store is one setting's state directory, held until Close.
type Store struct {
	dir      string
	config   string
	gestures []Gesture
	lock     *os.File
}

// Appeared reports whether state was created after a Read that found none
// and so holds no lock: what was read alongside it may already be out of date.
func (s *Store) Appeared() bool {
	if s.lock != nil {
		return false
	}
	_, err := os.Stat(s.dir)
	return err == nil
}

// Close releases the state.
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}

// Open reads the state for the setting configured at configPath and holds it
// as mode says until Close. A directory that does not exist yet reads as
// empty. A log line that cannot be read fails the open rather than being
// skipped, which would silently undo dismissals.
func Open(getenv func(string) string, setting, configPath string, mode Mode) (_ *Store, err error) {
	base, err := root(getenv)
	if err != nil {
		return nil, err
	}
	// The resolved path names the file itself, so a relative path or a symlinked
	// directory reaching the same file finds the same owner.
	config, err := filepath.EvalSymlinks(configPath)
	if err == nil {
		config, err = filepath.Abs(config)
	}
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", configPath, err)
	}
	s := &Store{dir: filepath.Join(base, setting), config: config}
	if mode == Write {
		if mkErr := os.MkdirAll(s.dir, 0o700); mkErr != nil {
			return nil, fmt.Errorf("creating the state directory: %w", mkErr)
		}
	}
	if s.lock, err = lockDir(s.dir, mode == Write); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()

	// The owner is checked after the log is read: a log is written only once
	// its owner is in place, so an owner absent after the read means the log
	// read was empty, and one that appeared meanwhile is still caught.
	beforeLogRead()
	data, err := os.ReadFile(filepath.Join(s.dir, logName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading the gesture log: %w", err)
	}
	if ownerErr := s.checkOwner(); ownerErr != nil && !errors.Is(ownerErr, fs.ErrNotExist) {
		return nil, ownerErr
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, len(data)+1)
	for n := 1; scanner.Scan(); n++ {
		var g Gesture
		if jsonErr := json.Unmarshal(scanner.Bytes(), &g); jsonErr != nil {
			return nil, fmt.Errorf("%s line %d cannot be read: lookout writes this file itself, so restore it from a backup or remove the line: %w", filepath.Join(s.dir, logName), n, jsonErr)
		}
		s.gestures = append(s.gestures, g)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("reading the gesture log: %w", scanErr)
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

// checkOwner reports whether the directory belongs to this store's
// configuration, wrapping fs.ErrNotExist when no configuration owns it yet.
func (s *Store) checkOwner() error {
	path := filepath.Join(s.dir, ownerName)
	owner, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("state directory %s has no owner yet: %w", s.dir, err)
	case err != nil:
		return fmt.Errorf("reading state: %w", err)
	case strings.TrimSpace(string(owner)) != s.config:
		return fmt.Errorf("state directory %s belongs to configuration %s, not %s: if these are two settings, give one file a different name; if the file moved, delete %s so the state follows it", s.dir, strings.TrimSpace(string(owner)), s.config, path)
	}
	return nil
}

// Claim makes the state directory this configuration's, creating it on first
// use, or reports that another configuration already owns it. The owner record
// is written in full under a temporary name and then hard-linked into place,
// which fails rather than replaces when an owner exists, so of two first
// gestures racing from different files only one can win, and no one ever
// reads a half-written owner. The directory is private to its user because
// item ids name the user's own work.
func (s *Store) Claim() (err error) {
	if mkErr := os.MkdirAll(s.dir, 0o700); mkErr != nil {
		return fmt.Errorf("creating the state directory: %w", mkErr)
	}
	if ownerErr := s.checkOwner(); !errors.Is(ownerErr, fs.ErrNotExist) {
		return ownerErr
	}
	tmp, err := os.CreateTemp(s.dir, "."+ownerName+".tmp-*")
	if err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	defer func() {
		if rmErr := os.Remove(tmp.Name()); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
	}()
	_, writeErr := tmp.WriteString(s.config + "\n")
	if syncErr := errors.Join(writeErr, tmp.Sync(), tmp.Close()); syncErr != nil {
		return fmt.Errorf("writing state: %w", syncErr)
	}
	beforeOwnerPublished()
	if linkErr := os.Link(tmp.Name(), filepath.Join(s.dir, ownerName)); linkErr != nil {
		if errors.Is(linkErr, fs.ErrExist) {
			return s.checkOwner()
		}
		return fmt.Errorf("writing state: %w", linkErr)
	}
	return nil
}

// Append records g in a directory this configuration owns, claiming it first
// if no configuration does yet.
func (s *Store) Append(g Gesture) error {
	if err := s.Claim(); err != nil {
		return err
	}
	g.At = g.At.UTC()
	line, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("encoding a gesture: %w", err)
	}
	if err := appendLine(filepath.Join(s.dir, logName), "gesture log", line); err != nil {
		return err
	}
	s.gestures = append(s.gestures, g)
	return nil
}

// appendLine adds line to the file at path, durably, naming the file as what
// in any error.
func appendLine(path, what string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening the %s: %w", what, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return errors.Join(fmt.Errorf("writing the %s: %w", what, err), f.Close())
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("writing the %s: %w", what, err)
	}
	return nil
}

// Gestures is the gesture log, oldest first.
func (s *Store) Gestures() []Gesture { return slices.Clone(s.gestures) }

// Baseline is the baseline's records, oldest first, one line each as it was
// written. Reading what a line holds is the caller's, which knows its shape.
func (s *Store) Baseline() ([][]byte, error) {
	data, err := os.ReadFile(s.BaselinePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the baseline: %w", err)
	}
	return bytes.SplitAfter(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")), nil
}

// AppendBaseline adds one record to the baseline of a directory this
// configuration owns, claiming it first if no configuration does yet.
func (s *Store) AppendBaseline(record json.RawMessage) error {
	if err := s.Claim(); err != nil {
		return err
	}
	return appendLine(s.BaselinePath(), "baseline", record)
}

// BaselinePath is where the baseline is kept.
func (s *Store) BaselinePath() string { return filepath.Join(s.dir, baselineName) }

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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"reflect"
	"time"

	"github.com/jakewan/lookout/internal/baseline"
	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/state"
)

// beforeSnapshotLock runs between reading the inputs and taking the state to
// record them; specs use it to land a gesture in between.
var beforeSnapshotLock = func() {}

// snapshot records the day's baseline record. It writes nothing to stdout: the
// record is state, read back later, and stderr says what was recorded.
func snapshot(ctx context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("snapshot")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) > 0 {
		diag.Printf("lookout snapshot: unexpected argument %q\n%s", pos[0], usage)
		return exitUsage
	}
	// A second attempt starts after the change that ended the first, so a
	// third is reached only if the setting keeps changing under it.
	for range 3 {
		done, err := snapshotOnce(ctx, env, *setting, diag)
		if err != nil {
			diag.Printf("lookout snapshot: %v", err)
			return exitFailure
		}
		if done {
			return 0
		}
	}
	diag.Print("lookout snapshot: the setting kept changing while lookout read it: run the command again")
	return exitFailure
}

// snapshotOnce records the day, or reports that the setting changed while its
// inputs were read and the record must be taken again. Inputs are read without
// holding the state, as read does; once it is held, the configuration and the
// dismissals must still be the ones the readout was built from, or the record
// would pair one moment's workstreams with another's gestures.
func snapshotOnce(ctx context.Context, env environment, setting string, diag *log.Logger) (bool, error) {
	// A date already recorded is answered before the inputs, which are the slow
	// part, are read.
	if _, done, err := alreadyRecorded(env, setting, env.now(), diag); err != nil || done {
		return done, err
	}
	r, again, err := readSetting(ctx, env, setting)
	if err != nil || again {
		return false, err
	}
	beforeSnapshotLock()

	loc, err := locate(env, setting, state.Write)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := loc.store.Close(); closeErr != nil {
			diag.Printf("lookout snapshot: releasing the state: %v", closeErr)
		}
	}()
	cfg, err := config.Load(loc.path)
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(cfg, r.cfg) || !maps.EqualFunc(loc.store.Dismissals(), r.dismissed, time.Time.Equal) {
		return false, nil
	}
	history, err := records(loc.store)
	if err != nil {
		return false, err
	}
	if recordedThrough(history, r.now, diag) {
		return true, nil
	}
	rec, err := baseline.Derive(history, r.cfg, r.out, loc.store.Gestures(), r.now)
	if err != nil {
		return false, err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return false, fmt.Errorf("encoding the record: %w", err)
	}
	if err := loc.store.AppendBaseline(line); err != nil {
		return false, err
	}
	diag.Printf("lookout snapshot: recorded %s: %d unplaced, %d arrived", rec.Date, len(rec.Unplaced), len(rec.Arrivals))
	return true, nil
}

// alreadyRecorded reads the baseline and reports whether it needs no record
// for now's date, saying why when it does not.
func alreadyRecorded(env environment, setting string, now time.Time, diag *log.Logger) (history []baseline.Record, done bool, err error) {
	loc, err := locate(env, setting, state.Read)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, loc.store.Close()) }()
	history, err = records(loc.store)
	if err != nil {
		return nil, false, err
	}
	return history, recordedThrough(history, now, diag), nil
}

// recordedThrough reports whether the baseline already reaches now's date,
// saying so when it does. A clock reading a date before the last record's,
// after a change of zone or a clock set back, records nothing, since a record
// out of order would be counted against the wrong interval.
func recordedThrough(history []baseline.Record, now time.Time, diag *log.Logger) bool {
	if len(history) == 0 {
		return false
	}
	today, last := now.Format(baseline.DateLayout), history[len(history)-1].Date
	switch {
	case today == last:
		diag.Printf("lookout snapshot: %s is already recorded; the first snapshot of a date is kept", last)
	case today < last:
		diag.Printf("lookout snapshot: today is %s, before the last record's %s, so nothing was recorded", today, last)
	default:
		return false
	}
	return true
}

// records reads the baseline. A line that cannot be read fails the read rather
// than being skipped, since a lost record would read as a gap.
func records(store *state.Store) ([]baseline.Record, error) {
	lines, err := store.Baseline()
	if err != nil || len(lines) == 1 && len(lines[0]) == 0 {
		return nil, err
	}
	out := make([]baseline.Record, 0, len(lines))
	for i, line := range lines {
		var rec baseline.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("%s line %d cannot be read: lookout writes this file itself, so restore it from a backup or remove the line: %w", store.BaselinePath(), i+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

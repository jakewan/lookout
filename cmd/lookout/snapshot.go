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
	if done, err := alreadyRecorded(env, setting, env.now(), diag); err != nil || done {
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
	records, err := records(loc.store)
	if err != nil {
		return false, err
	}
	var first, previous *baseline.Record
	if len(records) > 0 {
		first, previous = &records[0], &records[len(records)-1]
		if previous.Date == r.now.Format(baseline.DateLayout) {
			diag.Printf("lookout snapshot: %s is already recorded; the first snapshot of a date is kept", previous.Date)
			return true, nil
		}
	}
	rec, err := baseline.Derive(first, previous, r.cfg, r.out, loc.store.Gestures(), r.now)
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

// alreadyRecorded reports whether the baseline already holds a record for
// now's date, saying so when it does.
func alreadyRecorded(env environment, setting string, now time.Time, diag *log.Logger) (done bool, err error) {
	loc, err := locate(env, setting, state.Read)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, loc.store.Close()) }()
	records, err := records(loc.store)
	if err != nil || len(records) == 0 {
		return false, err
	}
	if last := records[len(records)-1].Date; last == now.Format(baseline.DateLayout) {
		diag.Printf("lookout snapshot: %s is already recorded; the first snapshot of a date is kept", last)
		return true, nil
	}
	return false, nil
}

func records(store *state.Store) ([]baseline.Record, error) {
	lines, err := store.Baseline()
	if err != nil {
		return nil, err
	}
	out := make([]baseline.Record, 0, len(lines))
	for i, line := range lines {
		var rec baseline.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("baseline record %d cannot be read: %w", i+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

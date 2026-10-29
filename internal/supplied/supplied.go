// Package supplied reads a file an outside supplier writes in the
// schema/supplied.v1.json contract and judges the input's health from it.
package supplied

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
	"github.com/jakewan/lookout/internal/readout"
	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type document struct {
	Input          string            `json:"input"`
	FetchedAt      time.Time         `json:"fetched_at"`
	AttemptedAt    time.Time         `json:"attempted_at"`
	SupplierStatus string            `json:"supplier_status"`
	Reason         string            `json:"reason"`
	MissingFields  []string          `json:"missing_fields"`
	Items          []json.RawMessage `json:"items"`
}

type itemFields struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	LastTouched time.Time  `json:"last_touched"`
	StartedAt   *time.Time `json:"started_at"`
	Notes       *string    `json:"notes"`
}

// Reader reads supplied files against the compiled supplied.v1 contract.
type Reader struct {
	schema *jsonschema.Schema
}

// NewReader compiles the embedded contract once, for every file a run reads.
func NewReader() (*Reader, error) {
	sch, err := schema.Compile("supplied.v1")
	if err != nil {
		return nil, err
	}
	return &Reader{schema: sch}, nil
}

// Read judges the input in at now. Every problem with the file is reported in
// the returned health record, so one bad file never stops a run. Items is
// empty when the file cannot be trusted to belong to this input at all. A
// cancelled ctx reads nothing; the caller reports the interruption.
func (rd *Reader) Read(ctx context.Context, in config.Input, now time.Time) readout.Reading {
	r := readout.Reading{Health: health.New(in.Name), Items: make([]readout.Item, 0)}
	if ctx.Err() != nil {
		return r
	}

	data, err := os.ReadFile(in.Supplied)
	if errors.Is(err, fs.ErrNotExist) {
		r.Health.Lower(health.Error, health.Missing)
		return r
	}
	if err != nil {
		r.Health.Lower(health.Error, health.Unreadable)
		return r
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		r.Health.Lower(health.Degraded, health.Schema)
		r.Health.SchemaViolations = append(r.Health.SchemaViolations, "not JSON: "+err.Error())
		return r
	}
	if validateErr := rd.schema.Validate(inst); validateErr != nil {
		return untrusted(r, in, inst, violations(validateErr))
	}

	// The schema accepts some RFC 3339 forms Go's time parser refuses, such as a
	// lowercase "t" or a leap second, so a file can pass supplied.v1 and still
	// fail to decode. That is a problem with the file, not with lookout, so it
	// degrades the input rather than failing the run.
	var doc document
	items := make([]readout.Item, 0)
	decodeErr := json.Unmarshal(data, &doc)
	if decodeErr == nil {
		decodeErr = encodable(doc.FetchedAt, &doc.AttemptedAt)
	}
	for i := 0; decodeErr == nil && i < len(doc.Items); i++ {
		var f itemFields
		if decodeErr = json.Unmarshal(doc.Items[i], &f); decodeErr == nil {
			decodeErr = encodable(f.LastTouched, f.StartedAt)
		}
		raw := doc.Items[i]
		if decodeErr == nil && f.Notes != nil {
			raw, decodeErr = withoutNotes(raw)
		}
		if decodeErr == nil {
			it := readout.Item{ID: f.ID, Title: f.Title, LastTouched: f.LastTouched, StartedAt: f.StartedAt, Raw: raw}
			if f.Notes != nil {
				it.Notes = *f.Notes
			}
			items = append(items, it)
		}
	}
	if decodeErr != nil {
		return untrusted(r, in, inst, []string{"a value lookout cannot decode: " + decodeErr.Error()})
	}

	// A file written for another input says nothing about this one, so none of
	// its own claims, its supplier status included, reach this record. It
	// contributes nothing, as a missing file does, so it ranks with one.
	if doc.Input != in.Name {
		r.Health.Lower(health.Error, health.InputMismatch)
		return r
	}
	fetched := doc.FetchedAt.UTC()
	r.Health.FetchedAt = &fetched
	r.Health.RecordCount = len(doc.Items)
	if doc.Reason != "" {
		r.Health.SupplierReason = &doc.Reason
	}

	if doc.SupplierStatus == "error" {
		r.Health.Lower(health.Error, health.SupplierError)
	}
	if now.Sub(doc.FetchedAt) > in.MaxAge {
		r.Health.Lower(health.Degraded, health.Stale)
	}
	if doc.AttemptedAt.After(doc.FetchedAt) {
		r.Health.Lower(health.Degraded, health.SupplierFailed)
	}
	// A time ahead of this machine's clock would read as fresh indefinitely, so
	// beyond a tolerance for ordinary drift it is a finding, not a freshness.
	// Activity times count too, since a future touch would pin a workstream at
	// zero idle. A due date is meant to lie ahead, so it does not.
	horizon := now.Add(health.ClockTolerance)
	skewed := doc.FetchedAt.After(horizon) || doc.AttemptedAt.After(horizon)
	for _, it := range items {
		skewed = skewed || it.LastTouched.After(horizon) || (it.StartedAt != nil && it.StartedAt.After(horizon))
	}
	if skewed {
		r.Health.Lower(health.Degraded, health.ClockSkew)
	}
	if doc.SupplierStatus == "degraded" {
		r.Health.Lower(health.Degraded, health.SupplierDegraded)
	}
	if len(doc.MissingFields) > 0 {
		r.Health.Lower(health.Degraded, health.MissingFields)
		r.Health.MissingFields = append(r.Health.MissingFields, doc.MissingFields...)
	}
	if in.ExpectRecords && len(doc.Items) == 0 {
		r.Health.Lower(health.Degraded, health.Empty)
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if seen[it.ID] {
			r.Health.Lower(health.Degraded, health.DuplicateID)
			break
		}
		seen[it.ID] = true
	}

	r.Items = items
	return r
}

// untrusted records a file whose contents cannot be used. Its items are
// withheld, but where the envelope still says plainly which input it is for,
// or that its supplier failed, that stronger evidence is kept rather than
// softened into a schema problem.
func untrusted(r readout.Reading, in config.Input, inst any, problems []string) readout.Reading {
	// Read each property by its exact name from the parsed document, so neither
	// a mistyped neighbor nor a key differing only in case can erase it.
	obj, _ := inst.(map[string]any)
	input, _ := obj["input"].(string)
	status, _ := obj["supplier_status"].(string)
	if input != "" && input != in.Name {
		r.Health.Lower(health.Error, health.InputMismatch)
		return r
	}
	r.Health.Lower(health.Degraded, health.Schema)
	r.Health.SchemaViolations = append(r.Health.SchemaViolations, problems...)
	if status == "error" {
		r.Health.Lower(health.Error, health.SupplierError)
	}
	return r
}

// withoutNotes is raw with its notes removed. Notes can carry anything a work
// note says, so they are read for keys and never passed on to a readout. Only
// the exact key is removed: Go's decoder would also read a key such as "Notes"
// as notes, and it is the closed item schema, checked first, that refuses one.
func withoutNotes(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "notes")
	return json.Marshal(fields)
}

// encodable reports a time the readout could not write: RFC 3339 admits an
// offset that carries a year-9999 time past year 9999 in UTC, which JSON
// timestamps cannot express.
func encodable(t time.Time, more ...*time.Time) error {
	check := func(t time.Time) error {
		if y := t.UTC().Year(); y < 0 || y > 9999 {
			return fmt.Errorf("%s falls outside years 0000 to 9999 in UTC", t.Format(time.RFC3339))
		}
		return nil
	}
	if err := check(t); err != nil {
		return err
	}
	for _, m := range more {
		if m == nil {
			continue
		}
		if err := check(*m); err != nil {
			return err
		}
	}
	return nil
}

// violations flattens a validation failure into one line per failed keyword,
// each naming where in the file it failed.
func violations(err error) []string {
	ve, ok := errors.AsType[*jsonschema.ValidationError](err)
	if !ok {
		return []string{err.Error()}
	}
	out := make([]string, 0)
	var walk func(u jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		if u.Error != nil && len(u.Errors) == 0 {
			loc := u.InstanceLocation
			if loc == "" {
				loc = "/"
			}
			out = append(out, loc+": "+u.Error.String())
		}
		for _, child := range u.Errors {
			walk(child)
		}
	}
	walk(*ve.BasicOutput())
	if len(out) == 0 {
		out = append(out, ve.Error())
	}
	return out
}

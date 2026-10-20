// Package supplied reads a file an outside supplier writes in the
// schema/supplied.v1.json contract and judges the input's health from it.
package supplied

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Item is one supplied item. Raw is the item exactly as the supplier wrote it,
// passed through to the readout so no source detail is lost.
type Item struct {
	ID          string
	Title       string
	LastTouched time.Time
	StartedAt   *time.Time
	Raw         json.RawMessage
}

// Reading is what one supplied input contributed: its health, and the items a
// workstream may use. Items is empty when the file cannot be trusted to belong
// to this input at all.
type Reading struct {
	Health health.Record
	Items  []Item
}

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
// the returned health record, so one bad file never stops a run.
func (rd *Reader) Read(in config.Input, now time.Time) Reading {
	r := Reading{Health: health.New(in.Name), Items: make([]Item, 0)}

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
		r.Health.Lower(health.Degraded, health.Schema)
		r.Health.SchemaViolations = append(r.Health.SchemaViolations, violations(validateErr)...)
		return r
	}

	// The schema accepts some RFC 3339 forms Go's time parser refuses, such as a
	// lowercase "t" or a leap second, so a file can pass supplied.v1 and still
	// fail to decode. That is a problem with the file, not with lookout, so it
	// degrades the input rather than failing the run.
	var doc document
	items := make([]Item, 0)
	decodeErr := json.Unmarshal(data, &doc)
	for i := 0; decodeErr == nil && i < len(doc.Items); i++ {
		var f itemFields
		if decodeErr = json.Unmarshal(doc.Items[i], &f); decodeErr == nil {
			items = append(items, Item{ID: f.ID, Title: f.Title, LastTouched: f.LastTouched, StartedAt: f.StartedAt, Raw: doc.Items[i]})
		}
	}
	if decodeErr != nil {
		r.Health.Lower(health.Degraded, health.Schema)
		r.Health.SchemaViolations = append(r.Health.SchemaViolations, "a value lookout cannot decode: "+decodeErr.Error())
		return r
	}

	// A file written for another input says nothing about this one, so none of
	// its own claims, its supplier status included, reach this record.
	if doc.Input != in.Name {
		r.Health.Lower(health.Degraded, health.InputMismatch)
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

	r.Items = items
	return r
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

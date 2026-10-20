// Package health holds lookout's judgment of one expected input, in the shape
// schema/health.v1.json publishes.
package health

import "time"

// Status values, as health.v1 enumerates them.
const (
	OK       = "ok"
	Degraded = "degraded"
	Error    = "error"
)

// Reasons, as health.v1 enumerates them. Each names one cause that lowered a
// status.
const (
	Missing          = "missing"
	Unreadable       = "unreadable"
	Schema           = "schema"
	InputMismatch    = "input_mismatch"
	Stale            = "stale"
	SupplierFailed   = "supplier_failed"
	SupplierError    = "supplier_error"
	SupplierDegraded = "supplier_degraded"
	MissingFields    = "missing_fields"
	Empty            = "empty"
	ClockSkew        = "clock_skew"
	DuplicateID      = "duplicate_id"
)

// Record is one input's health.
type Record struct {
	Input            string     `json:"input"`
	Status           string     `json:"status"`
	Reasons          []string   `json:"reasons"`
	FetchedAt        *time.Time `json:"fetched_at"`
	RecordCount      int        `json:"record_count"`
	MissingFields    []string   `json:"missing_fields"`
	SchemaViolations []string   `json:"schema_violations"`
	SupplierReason   *string    `json:"supplier_reason"`
}

// New returns an ok record for input with every list empty rather than nil,
// since a nil slice marshals to null and health.v1 requires arrays.
func New(input string) Record {
	return Record{
		Input:            input,
		Status:           OK,
		Reasons:          make([]string, 0, 1),
		MissingFields:    make([]string, 0),
		SchemaViolations: make([]string, 0),
	}
}

// Lower records reason and moves the status to status unless it is already
// worse, so no later finding can soften an earlier one.
func (r *Record) Lower(status, reason string) {
	r.Reasons = append(r.Reasons, reason)
	if rank(status) > rank(r.Status) {
		r.Status = status
	}
}

func rank(status string) int {
	switch status {
	case Error:
		return 2
	case Degraded:
		return 1
	}
	return 0
}

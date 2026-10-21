package health

import (
	"slices"
	"testing"
)

func TestLowerRecordsEachReasonOnce(t *testing.T) {
	r := New("code")
	r.Lower(Error, Missing)
	r.Lower(Degraded, Missing)

	if !slices.Equal(r.Reasons, []string{Missing}) {
		t.Errorf("reasons = %v, want %q once: health.v1 requires reasons to be unique", r.Reasons, Missing)
	}
	if r.Status != Error {
		t.Errorf("status = %q, want %q: a repeated reason never softens the status", r.Status, Error)
	}
}

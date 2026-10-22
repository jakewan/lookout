package baseline

import (
	"testing"

	"github.com/jakewan/lookout/internal/readout"
)

func TestStalenessTakesObservedValuesByNearestRank(t *testing.T) {
	for name, tc := range map[string]struct {
		idle        []int64
		median, p85 int64
	}{
		"one":    {[]int64{7}, 7, 7},
		"two":    {[]int64{9, 3}, 3, 9},
		"seven":  {[]int64{70, 10, 60, 20, 50, 30, 40}, 40, 60},
		"six":    {[]int64{1, 2, 3, 4, 5, 6}, 3, 6},
		"twenty": {[]int64{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}, 10, 17},
	} {
		t.Run(name, func(t *testing.T) {
			ws := make([]readout.Workstream, 0, len(tc.idle))
			for _, v := range tc.idle {
				ws = append(ws, readout.Workstream{IdleSeconds: &v})
			}
			s := staleness(ws)
			if s.MedianSeconds == nil || *s.MedianSeconds != tc.median || s.P85Seconds == nil || *s.P85Seconds != tc.p85 {
				t.Errorf("median = %v, p85 = %v, want %d and %d", deref(s.MedianSeconds), deref(s.P85Seconds), tc.median, tc.p85)
			}
		})
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

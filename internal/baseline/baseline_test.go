package baseline

import (
	"slices"
	"testing"

	"github.com/jakewan/lookout/internal/config"
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

// TestHandPlacementOverEveryHolding decides each pairing of where an item sat
// after replay with where it sits now. The configuration lets an item be listed
// by more than one workstream, so each side can hold it more than once.
func TestHandPlacementOverEveryHolding(t *testing.T) {
	x := config.Member{Input: "tracker", ID: "X"}
	sides := map[string][]bool{ // one entry per workstream holding the item: live or not
		"nowhere":            nil,
		"live":               {true},
		"completed":          {false},
		"live and completed": {true, false},
		"two live":           {true, true},
		"two completed":      {false, false},
	}
	build := func(holds []bool) []config.Workstream {
		ws := []config.Workstream{{Name: "other", Members: []config.Member{{Input: "tracker", ID: "Y"}}}}
		for i, live := range holds {
			w := config.Workstream{Name: string(rune('a' + i)), Members: []config.Member{x}}
			if !live {
				w.Completed = "2030-06-01"
			}
			ws = append(ws, w)
		}
		return ws
	}
	// placed[before][now]: what an adopt would count, decided cell by cell.
	placed := map[string]map[string]bool{
		"nowhere":            {"live": true, "completed": true, "live and completed": true, "two live": true, "two completed": true},
		"live":               {"live": false, "completed": false, "live and completed": false, "two live": false, "two completed": false},
		"completed":          {"live": true, "completed": false, "live and completed": true, "two live": true, "two completed": false},
		"live and completed": {"live": false, "completed": false, "live and completed": false, "two live": false, "two completed": false},
		"two live":           {"live": false, "completed": false, "live and completed": false, "two live": false, "two completed": false},
		"two completed":      {"live": true, "completed": false, "live and completed": true, "two live": true, "two completed": false},
	}
	for before, row := range placed {
		for now, want := range row {
			t.Run(before+" then "+now, func(t *testing.T) {
				got := handPlaced(build(sides[before]), build(sides[now]))
				wantList := []string{}
				if want {
					wantList = []string{"tracker:X"}
				}
				if !slices.Equal(got, wantList) {
					t.Errorf("hand_placed = %v, want %v", got, wantList)
				}
			})
		}
	}
}

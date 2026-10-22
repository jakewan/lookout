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

// holdingSide is where an item sits on one side of a comparison: each
// workstream holding it, by name, and whether that workstream is live.
type holdingSide map[string]bool

func (h holdingSide) workstreams() []config.Workstream {
	x := config.Member{Input: "tracker", ID: "X"}
	ws := []config.Workstream{{Name: "other", Members: []config.Member{{Input: "tracker", ID: "Y"}}}}
	for _, name := range []string{"a", "b", "c"} {
		live, ok := h[name]
		if !ok {
			continue
		}
		w := config.Workstream{Name: name, Members: []config.Member{x}}
		if !live {
			w.Completed = "2030-06-01"
		}
		ws = append(ws, w)
	}
	return ws
}

// TestHandPlacementInNamedCases decides, case by case, what a person doing each
// thing by hand would expect counted, as the matching adopt and complete
// gestures would count it.
func TestHandPlacementInNamedCases(t *testing.T) {
	for name, tc := range map[string]struct {
		before, now holdingSide
		placed      bool
	}{
		"added to a live workstream":                       {holdingSide{}, holdingSide{"a": true}, true},
		"added, then its workstream completed":             {holdingSide{}, holdingSide{"a": false}, true},
		"moved out of a completed workstream into a live":  {holdingSide{"a": false}, holdingSide{"b": true}, true},
		"moved between completed workstreams":              {holdingSide{"a": false}, holdingSide{"b": false}, true},
		"copied from a completed workstream into a live":   {holdingSide{"a": false}, holdingSide{"a": false, "b": true}, true},
		"added to two live workstreams":                    {holdingSide{}, holdingSide{"a": true, "b": true}, true},
		"left in its completed workstream":                 {holdingSide{"a": false}, holdingSide{"a": false}, false},
		"its completed workstream reopened":                {holdingSide{"a": false}, holdingSide{"a": true}, false},
		"left in its live workstream":                      {holdingSide{"a": true}, holdingSide{"a": true}, false},
		"its live workstream completed":                    {holdingSide{"a": true}, holdingSide{"a": false}, false},
		"moved between live workstreams":                   {holdingSide{"a": true}, holdingSide{"b": true}, false},
		"moved from a live workstream into a completed":    {holdingSide{"a": true}, holdingSide{"b": false}, false},
		"also listed by a live workstream already holding": {holdingSide{"a": true, "b": false}, holdingSide{"a": true, "b": false}, false},
		"moved on while a live workstream still holds it":  {holdingSide{"a": true, "b": false}, holdingSide{"a": true, "c": false}, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := handPlaced(tc.before.workstreams(), tc.now.workstreams())
			if want := tc.placed; slices.Equal(got, []string{"tracker:X"}) != want || (!want && len(got) != 0) {
				t.Errorf("hand_placed = %v, want placed %v", got, want)
			}
		})
	}
}

// TestHandPlacementOverEveryHolding checks every pairing of holdings across
// three named workstreams against the definition: placed when the item is now
// in a workstream it was not in after replay and no live workstream held it
// then. It is the definition restated over names, so its worth is in reaching
// the pairings the named cases leave out, and in listing an item once.
func TestHandPlacementOverEveryHolding(t *testing.T) {
	var sides []holdingSide
	// Each workstream is 0: not holding the item, 1: holding it live, or 2:
	// holding it completed; three let an item join one while two others held it.
	for code := range 27 {
		h := holdingSide{}
		for i, name := range []string{"a", "b", "c"} {
			if state := code / []int{1, 3, 9}[i] % 3; state > 0 {
				h[name] = state == 1
			}
		}
		sides = append(sides, h)
	}
	for _, before := range sides {
		for _, now := range sides {
			wasLive, joined := false, false
			for _, live := range before {
				wasLive = wasLive || live
			}
			for name := range now {
				if _, ok := before[name]; !ok {
					joined = true
				}
			}
			want := []string{}
			if joined && !wasLive {
				want = []string{"tracker:X"}
			}
			if got := handPlaced(before.workstreams(), now.workstreams()); !slices.Equal(got, want) {
				t.Errorf("before %v, now %v: hand_placed = %v, want %v", before, now, got, want)
			}
		}
	}
}

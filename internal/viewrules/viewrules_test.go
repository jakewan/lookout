package viewrules

import "testing"

func TestAgeIsTheLargestWholeUnitFloored(t *testing.T) {
	for _, tc := range []struct {
		idle *int64
		want string
	}{
		{nil, "--"},
		{new(int64(0)), "0s"},
		{new(int64(59)), "59s"},
		{new(int64(60)), "1m"},
		{new(int64(60*60 - 1)), "59m"},
		{new(int64(60 * 60)), "1h"},
		{new(int64(24*60*60 - 1)), "23h"},
		{new(int64(24 * 60 * 60)), "1d"},
		{new(int64(400 * 24 * 60 * 60)), "400d"},
	} {
		if got := Age(tc.idle); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.idle, got, tc.want)
		}
	}
}

// Staleness is read from the readout's active verdict, so no view can call a
// workstream stale that the readout counts as active, nor leave one neither.
func TestMarkerTellsUnreadFromStale(t *testing.T) {
	for _, tc := range []struct {
		name   string
		idle   *int64
		active bool
		want   string
	}{
		{"nothing read", nil, false, Unread},
		{"active", new(int64(50)), true, ""},
		{"read but not active", new(int64(100)), false, Stale},
	} {
		if got := Marker(tc.idle, tc.active); got != tc.want {
			t.Errorf("%s: Marker = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUnhealthyIsAnyStatusButOkAndAbsentByDesign(t *testing.T) {
	for status, want := range map[string]bool{
		"ok":               false,
		"absent_by_design": false,
		"degraded":         true,
		"error":            true,
		"a later status":   true,
	} {
		if got := Unhealthy(status); got != want {
			t.Errorf("Unhealthy(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestHiddenIsOnlyAbsentByDesign(t *testing.T) {
	for status, want := range map[string]bool{
		"absent_by_design": true,
		"ok":               false,
		"error":            false,
		"a later status":   false,
	} {
		if got := Hidden(status); got != want {
			t.Errorf("Hidden(%q) = %v, want %v", status, got, want)
		}
	}
}

// A span labels a whole window, so unlike Age it never rounds: a scale
// labelled 1d over a 36-hour window would misstate it by half.
func TestSpanIsExact(t *testing.T) {
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{0, "0s"},
		{45, "45s"},
		{90 * 60, "1h30m"},
		{24 * 60 * 60, "1d"},
		{36 * 60 * 60, "1d12h"},
		{7 * 24 * 60 * 60, "7d"},
		{14*24*60*60 + 61, "14d1m1s"},
	} {
		if got := Span(tc.seconds); got != tc.want {
			t.Errorf("Span(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestRankPutsUnlistedInputsLast(t *testing.T) {
	order := NewOrder([]string{"tracker", "code"})
	if order.Rank("tracker") >= order.Rank("code") {
		t.Error("tracker does not rank before code")
	}
	if order.Rank("code") >= order.Rank("elsewhere") {
		t.Error("an input health does not list does not rank after those it does")
	}
}

func TestTokenIsWhatAdoptAndDismissTake(t *testing.T) {
	for _, tc := range []struct{ input, id, want string }{
		{"hub", "owner/app#60", "hub:owner/app#60"},
		{"code", "owner/app:spike/cache", "code:owner/app:spike/cache"},
		{"code", "fix/\u200bcache", `"code:fix/\u200bcache"`},
		{"tracker", "tab\there", `"tracker:tab\there"`},
		{"tracker", "bad\xffutf8", `"tracker:bad\xffutf8"`},
	} {
		if got := Token(tc.input, tc.id); got != tc.want {
			t.Errorf("Token(%q, %q) = %q, want %q", tc.input, tc.id, got, tc.want)
		}
	}
}

func TestCleanShowsWhatItWouldOtherwiseObey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain name", "plain name"},
		{"tab\tand\nbreak", "tab and break"},
		{"bidi\u202eoverride", "\"bidi\\u202eoverride\""},
		{"zero\u200bwidth", "\"zero\\u200bwidth\""},
		{"bell\a", `"bell\a"`},
	} {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

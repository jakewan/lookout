package viewrules

import "testing"

func ptr(v int64) *int64 { return &v }

func TestAgeIsTheLargestWholeUnitFloored(t *testing.T) {
	for _, tc := range []struct {
		idle *int64
		want string
	}{
		{nil, "--"},
		{ptr(0), "0s"},
		{ptr(59), "59s"},
		{ptr(60), "1m"},
		{ptr(60*60 - 1), "59m"},
		{ptr(60 * 60), "1h"},
		{ptr(24*60*60 - 1), "23h"},
		{ptr(24 * 60 * 60), "1d"},
		{ptr(400 * 24 * 60 * 60), "400d"},
	} {
		if got := Age(tc.idle); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.idle, got, tc.want)
		}
	}
}

func TestMarkerTellsUnreadFromStale(t *testing.T) {
	for _, tc := range []struct {
		name   string
		idle   *int64
		window int64
		want   string
	}{
		{"nothing read", nil, 100, Unread},
		{"inside the window", ptr(50), 100, ""},
		{"at the window", ptr(100), 100, ""},
		{"past the window", ptr(101), 100, Stale},
		{"a zero window", ptr(1), 0, Stale},
	} {
		if got := Marker(tc.idle, tc.window); got != tc.want {
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

func TestRankPutsUnlistedInputsLast(t *testing.T) {
	order := NewOrder([]string{"tracker", "code"})
	if order.Rank("tracker") >= order.Rank("code") {
		t.Error("tracker does not rank before code")
	}
	if order.Rank("code") >= order.Rank("elsewhere") {
		t.Error("an input health does not list does not rank after those it does")
	}
}

func TestCleanShowsWhatItWouldOtherwiseObey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain name", "plain name"},
		{"tab\tand\nbreak", "tab and break"},
		{"bidi‮override", "\"bidi\\u202eoverride\""},
		{"zero​width", "\"zero\\u200bwidth\""},
		{"bell\a", `"bell\a"`},
	} {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

package htmlview

import (
	"slices"
	"time"

	"github.com/jakewan/lookout/internal/viewrules"
)

// DefaultReadOldAfter is how long after its read a page marks the read old,
// where Options gives no other.
const DefaultReadOldAfter = 2 * time.Hour

// Options are the choices a caller makes about a page beyond the readout it
// draws.
type Options struct {
	// ReadOldAfter is how long after its read the page marks the read old;
	// zero takes DefaultReadOldAfter.
	ReadOldAfter time.Duration
}

// ageStep is what the page says about its read's age from At on, until the
// next step's moment. The page is a snapshot that may stay open for days, so
// Go writes the whole schedule ahead and the script only shows the latest
// step whose moment has passed: the age's wording and when a read turns old
// stay rules of Go's, never a second copy in the script.
type ageStep struct {
	At   string `json:"at"`
	Text string `json:"text"`
	Old  bool   `json:"old"`
}

// The schedule's horizon and grain: each minute for the first hour, each hour
// to two days, then each day to a month, after which the last step stands.
// Each step changes what the page says, so none is wasted, and the whole
// schedule stays a few kilobytes inline.
const (
	ageMinutes = 59
	ageHours   = 47
	ageDays    = 30
)

// readAge is the schedule for a page read at read, which turns old after
// oldAfter.
func readAge(read time.Time, oldAfter time.Duration) []ageStep {
	offsets := []time.Duration{0, oldAfter}
	for m := 1; m <= ageMinutes; m++ {
		offsets = append(offsets, time.Duration(m)*time.Minute)
	}
	for h := 1; h <= ageHours; h++ {
		offsets = append(offsets, time.Duration(h)*time.Hour)
	}
	for d := 2; d <= ageDays; d++ {
		offsets = append(offsets, time.Duration(d)*24*time.Hour)
	}
	slices.Sort(offsets)
	offsets = slices.Compact(offsets)
	steps := make([]ageStep, 0, len(offsets))
	for _, off := range offsets {
		old := off >= oldAfter
		steps = append(steps, ageStep{
			At:   read.Add(off).UTC().Format(time.RFC3339),
			Text: ageText(off, old),
			Old:  old,
		})
	}
	return steps
}

func ageText(off time.Duration, old bool) string {
	text := "read under a minute ago"
	if off >= time.Minute {
		s := int64(off / time.Second)
		text = "read " + viewrules.Age(&s) + " ago"
	}
	if old {
		// The page belongs to every lookout user, most with no timer
		// rewriting it, so it names the one refresh every user has.
		text += "; run lookout view again, then reload, to see the latest"
	}
	return text
}

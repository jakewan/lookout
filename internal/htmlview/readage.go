package htmlview

import (
	"slices"
	"strconv"
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
	// zero or less takes DefaultReadOldAfter.
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

// The schedule's grain follows viewrules.Age's units, a step wherever the age
// it prints changes, up to ageDays; the step at ageDays stands for every age
// after it, so it says so. That keeps the whole schedule a few kilobytes
// inline.
const (
	ageMinutes = 59
	ageHours   = 23
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
	for d := 1; d <= ageDays; d++ {
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

// ageText sits beside the page's as-of time, which it qualifies, so it names
// only the age. How to refresh is the header's note, shown with script off as
// well, so an old read says only that it is old.
func ageText(off time.Duration, old bool) string {
	text := "under a minute ago"
	switch {
	case off >= ageDays*24*time.Hour:
		text = strconv.Itoa(ageDays) + "d or more ago"
	case off >= time.Minute:
		s := int64(off / time.Second)
		text = viewrules.Age(&s) + " ago"
	}
	if old {
		text += ", old"
	}
	return text
}

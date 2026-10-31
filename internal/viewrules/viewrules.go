// Package viewrules holds the rules every view of a readout draws the same
// way, so the plain-text and the browser views cannot come to disagree on how
// old a workstream is, whether it is stale, or which inputs are worth a
// warning.
package viewrules

import (
	"strconv"
	"strings"
)

// The markers a workstream can carry beside its age.
const (
	// Unread marks a workstream nothing was read for on this read, which
	// says nothing of its past.
	Unread = "unread"
	// Stale marks a workstream idle for longer than the event window.
	Stale = "stale"
)

// Age is idle seconds in the largest whole unit, floored, or -- where nothing
// was read.
func Age(idle *int64) string {
	if idle == nil {
		return "--"
	}
	s := *idle
	switch {
	case s < 60:
		return strconv.FormatInt(s, 10) + "s"
	case s < 60*60:
		return strconv.FormatInt(s/60, 10) + "m"
	case s < 24*60*60:
		return strconv.FormatInt(s/(60*60), 10) + "h"
	}
	return strconv.FormatInt(s/(24*60*60), 10) + "d"
}

// Marker is Unread, Stale, or empty for a workstream idle for idle seconds
// against an event window of window seconds.
func Marker(idle *int64, window int64) string {
	switch {
	case idle == nil:
		return Unread
	case *idle > window:
		return Stale
	}
	return ""
}

// Unhealthy reports whether an input's health status is worth a warning.
// absent_by_design is a setting expecting the input to be absent, which the
// vision shows as nothing at all rather than as a fault. Any other status but
// ok, including one a later version adds, is worth a look.
func Unhealthy(status string) bool {
	return status != "ok" && status != "absent_by_design"
}

// Order ranks inputs by their place in a readout's health list, which
// readout.v1 keeps in the configuration's order, so every view lists a
// workstream's inputs the same way.
type Order map[string]int

// NewOrder ranks inputs in the order given.
func NewOrder(inputs []string) Order {
	o := make(Order, len(inputs))
	for i, in := range inputs {
		o[in] = i
	}
	return o
}

// Rank is an input's place; one health does not list sorts after those it
// does.
func (o Order) Rank(input string) int {
	if i, ok := o[input]; ok {
		return i
	}
	return len(o)
}

// Clean keeps text from the readout from breaking its line or being obeyed.
// Tabs and line breaks become spaces, as unplaced prints them; any other rune
// Go does not count as printable, a control, a bidirectional override or a
// zero-width character among them, quotes the whole string so it is shown, not
// obeyed.
func Clean(s string) string {
	s = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
	if strings.ContainsFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}

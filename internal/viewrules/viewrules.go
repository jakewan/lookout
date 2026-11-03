// Package viewrules holds the rules every view of a readout draws the same
// way, so the plain-text and the browser views cannot come to disagree on how
// old a workstream is, whether it is stale, or which inputs are worth a
// warning.
package viewrules

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// The markers a workstream can carry beside its age.
const (
	// Unread marks a workstream nothing was read for on this read, which
	// says nothing of its past.
	Unread = "unread"
	// Stale marks a workstream read but with no touch within the event window.
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

// Marker is Unread, Stale, or empty for a workstream idle for idle seconds,
// with active the readout's verdict on it. Staleness follows that verdict
// rather than comparing idle with the window: idle is floored to whole
// seconds, so a touch just before the window opens would read neither active
// nor stale.
func Marker(idle *int64, active bool) string {
	switch {
	case idle == nil:
		return Unread
	case !active:
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

// Hidden reports whether a view leaves an input out altogether: the vision
// shows an input the setting expects to be absent as nothing at all, so a
// setting without it leaves no gap where it would be.
func Hidden(status string) bool { return status == "absent_by_design" }

// Span is a length of seconds written exactly in days, hours, minutes and
// seconds, leaving out the units it does not need, for labelling a window
// rather than estimating an age.
func Span(seconds int64) string {
	if seconds == 0 {
		return "0s"
	}
	var b strings.Builder
	for _, u := range []struct {
		size int64
		name string
	}{{24 * 60 * 60, "d"}, {60 * 60, "h"}, {60, "m"}, {1, "s"}} {
		if n := seconds / u.size; n > 0 {
			b.WriteString(strconv.FormatInt(n, 10) + u.name)
			seconds -= n * u.size
		}
	}
	return b.String()
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

// Token is the <input>:<id> handle adopt, move, remove and dismiss take for an
// item, as lookout unplaced prints it. One holding anything Go does not count
// as printable, a tab included, or that is not valid UTF-8 is quoted whole,
// which those commands decode, so a view can show the handle a command will
// accept without the text splitting a line or driving a terminal.
func Token(input, id string) string {
	token := input + ":" + id
	if !utf8.ValidString(token) || strings.ContainsFunc(token, func(r rune) bool { return !strconv.IsPrint(r) }) {
		return strconv.Quote(token)
	}
	return token
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

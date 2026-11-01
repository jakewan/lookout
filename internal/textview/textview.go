// Package textview renders a readout as plain text for a person at a terminal.
// It reads the readout only through its published JSON, as any renderer
// outside lookout would. It leans on one fact readout.v1 does not state: an
// input's name holds no colon, which the configuration requires of every input
// and supplied.v1 of every supplied one.
package textview

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jakewan/lookout/internal/viewrules"
)

const (
	// width is the terminal width lines wrap at, counted in runes; only a
	// workstream id too long for a line of its own passes it.
	width = 80
	// nameCap is the most runes any name takes, so a long workstream or input
	// name cannot crowd out the figures beside it.
	nameCap = 24
	// ageWidth fits the widest age a year-old workstream shows, such as 365d.
	ageWidth = 4
	// markerWidth fits the widest marker, unread.
	markerWidth = 6
)

// view is the part of a readout the view shows. Fields readout.v1 may gain
// are ignored, as the contract asks of a renderer.
type view struct {
	Setting            string       `json:"setting"`
	GeneratedAt        time.Time    `json:"generated_at"`
	EventWindowSeconds int64        `json:"event_window_seconds"`
	Workstreams        []workstream `json:"workstreams"`
	Unplaced           []struct{}   `json:"unplaced"`
	Health             []health     `json:"health"`
	DismissedCount     int          `json:"dismissed_count"`
	CompletedCount     int          `json:"completed_count"`
}

type workstream struct {
	Name             string   `json:"name"`
	Title            string   `json:"title"`
	IdleSeconds      *int64   `json:"idle_seconds"`
	InputsIncomplete []string `json:"inputs_incomplete"`
	MembersUnread    []string `json:"members_unread"`
	Items            []struct {
		Input string `json:"input"`
		// Joined is present on an item the workstream holds without naming it.
		Joined *struct{} `json:"joined"`
	} `json:"items"`
}

type health struct {
	Input   string   `json:"input"`
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

// Render writes the view of readout, a readout.v1 document, to w.
func Render(w io.Writer, readout []byte) error {
	var v view
	if err := json.Unmarshal(readout, &v); err != nil {
		return fmt.Errorf("reading the readout: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s - %s UTC\n\n", name(v.Setting), v.GeneratedAt.UTC().Format("2006-01-02 15:04"))
	writeWorkstreams(&b, v)
	writeWarnings(&b, v)
	b.WriteString("\n")
	b.WriteString(footer(v))
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("writing the view: %w", err)
	}
	return nil
}

func writeWorkstreams(b *strings.Builder, v view) {
	b.WriteString("Workstreams\n")
	if len(v.Workstreams) == 0 {
		b.WriteString("  none")
		if v.CompletedCount > 0 {
			fmt.Fprintf(b, " (%d completed)", v.CompletedCount)
		}
		b.WriteString("\n")
		return
	}
	names := make([]string, 0, len(v.Workstreams))
	ids := make([]string, 0, len(v.Workstreams))
	nameWidth := 0
	for _, ws := range v.Workstreams {
		n := label(ws)
		names = append(names, n)
		nameWidth = max(nameWidth, utf8.RuneCountInString(n))
		// Commands take the id, so wherever the row's label is not the whole id,
		// the id follows it whole.
		id := ""
		if n != clean(ws.Name) {
			id = "[" + clean(ws.Name) + "]"
		}
		ids = append(ids, id)
	}
	inputs := make([]string, 0, len(v.Health))
	for _, h := range v.Health {
		inputs = append(inputs, h.Input)
	}
	order := viewrules.NewOrder(inputs)
	for i, ws := range v.Workstreams {
		// A workstream fed by an input that is failing may understate, so its
		// row says so where it is read, not only in the warnings below.
		flag := "  "
		if len(ws.InputsIncomplete) > 0 {
			flag = "! "
		}
		lead := fmt.Sprintf("%s%-*s  %*s  %-*s  ", flag, nameWidth, names[i], ageWidth, viewrules.Age(ws.IdleSeconds), markerWidth, viewrules.Marker(ws.IdleSeconds, v.EventWindowSeconds))
		cont := strings.Repeat(" ", utf8.RuneCountInString(lead))
		b.WriteString(whole(wrap(counts(ws, order), lead, cont), ids[i], cont))
	}
}

// idIndent starts a line that holds only an id too long to follow the figures.
const idIndent = "    "

// whole ends the wrapped text with unit, never broken, since a workstream's id
// is copied into commands: on the last line where it fits, else on a line of
// its own after cont, else after idIndent, and past width only when it is
// longer than a line after idIndent.
func whole(wrapped, unit, cont string) string {
	if unit == "" {
		return wrapped
	}
	body := strings.TrimSuffix(wrapped, "\n")
	last := body[strings.LastIndex(body, "\n")+1:]
	n := utf8.RuneCountInString(unit)
	switch {
	case utf8.RuneCountInString(last)+1+n <= width:
		return body + " " + unit + "\n"
	case utf8.RuneCountInString(cont)+n <= width:
		return body + "\n" + cont + unit + "\n"
	}
	return body + "\n" + idIndent + unit + "\n"
}

func unhealthy(h health) bool { return viewrules.Unhealthy(h.Status) }

func writeWarnings(b *strings.Builder, v view) {
	// Each warning is a list of units to wrap. A name is one unit, so the
	// spaces inside it survive and it reads as it does in its row.
	var lines [][]string
	for _, h := range v.Health {
		if !unhealthy(h) {
			continue
		}
		units := []string{"input", name(h.Input) + ":", clean(h.Status)}
		if len(h.Reasons) > 0 {
			reasons := make([]string, 0, len(h.Reasons))
			for _, r := range h.Reasons {
				reasons = append(reasons, clean(r))
			}
			units = append(units, strings.Fields("("+strings.Join(reasons, ", ")+")")...)
		}
		lines = append(lines, units)
	}
	for _, ws := range v.Workstreams {
		if len(ws.InputsIncomplete) == 0 {
			continue
		}
		inputs := make([]string, 0, len(ws.InputsIncomplete))
		for _, in := range ws.InputsIncomplete {
			inputs = append(inputs, name(in))
		}
		// A warning names the workstream as its row does, so the two read together;
		// the row carries the whole id.
		units := []string{"workstream", label(ws) + ":"}
		units = append(units, strings.Fields(strings.Join(inputs, ", ")+" may be incomplete")...)
		lines = append(lines, units)
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("\nWarnings\n")
	for _, units := range lines {
		b.WriteString(wrap(units, "  ", "    "))
	}
}

func footer(v view) string {
	// The counts are drawn from every input, so a failing one can leave them
	// short; the line says so where it is read, as a row does.
	flag := ""
	if slices.ContainsFunc(v.Health, unhealthy) {
		flag = "! "
	}
	parts := []string{fmt.Sprintf("%d unplaced", len(v.Unplaced))}
	// An empty unplaced list beside items the user dismissed is not a quiet
	// day, so the count of what was set aside rides along with it.
	if v.DismissedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d dismissed", v.DismissedCount))
	}
	// completed_count counts workstreams, not items like the counts before it.
	switch {
	case v.CompletedCount == 1:
		parts = append(parts, "1 workstream completed")
	case v.CompletedCount > 1:
		parts = append(parts, fmt.Sprintf("%d workstreams completed", v.CompletedCount))
	}
	for i := range parts[:len(parts)-1] {
		parts[i] += ","
	}
	return wrap(parts, flag, "  ")
}

// counts is a workstream's items per input, as the units a row wraps between:
// its members read and unread, and apart from them the items it joined, so a
// join never reads as a membership.
func counts(ws workstream, order viewrules.Order) []string {
	type tally struct{ read, unread, joined int }
	tallies := map[string]*tally{}
	var inputs []string
	at := func(in string) *tally {
		if t, ok := tallies[in]; ok {
			return t
		}
		inputs = append(inputs, in)
		tallies[in] = &tally{}
		return tallies[in]
	}
	for _, it := range ws.Items {
		if it.Joined != nil {
			at(it.Input).joined++
			continue
		}
		at(it.Input).read++
	}
	for _, m := range ws.MembersUnread {
		// Input names cannot hold a colon, so the first one ends it.
		in, _, _ := strings.Cut(m, ":")
		at(in).unread++
	}
	if len(inputs) == 0 {
		return []string{"no members"}
	}
	// Inputs health does not list keep the order the members first named them.
	slices.SortStableFunc(inputs, func(a, b string) int { return order.Rank(a) - order.Rank(b) })
	units := make([]string, 0, len(inputs))
	for i, in := range inputs {
		t := tallies[in]
		u := name(in) + " " + strconv.Itoa(t.read)
		if t.unread > 0 {
			u += " (" + strconv.Itoa(t.unread) + " unread)"
		}
		if t.joined > 0 {
			u += " +" + strconv.Itoa(t.joined) + " joined"
		}
		if i < len(inputs)-1 {
			u += ","
		}
		units = append(units, u)
	}
	return units
}

// label is what a workstream is shown as: its title, or its id where it has
// none, made safe to print and cut to nameCap.
func label(ws workstream) string {
	if ws.Title != "" {
		return name(ws.Title)
	}
	return name(ws.Name)
}

// name is a name from the readout made safe to print and cut to nameCap.
func name(s string) string {
	s = clean(s)
	if utf8.RuneCountInString(s) <= nameCap {
		return s
	}
	kept := []rune(s)[:nameCap]
	kept[len(kept)-1] = '~'
	return string(kept)
}

// clean keeps text from the readout from breaking its line or driving the
// terminal.
func clean(s string) string { return viewrules.Clean(s) }

// wrap lays units out after first, separated by spaces, continuing on lines
// that start with cont whenever the next unit would pass width. A unit too
// long for a line of its own breaks at its spaces, and a word too long for one
// breaks across lines, so nothing is ever cut.
func wrap(units []string, first, cont string) string {
	var b strings.Builder
	line := first
	used := 0
	room := func() int { return width - utf8.RuneCountInString(line) }
	newline := func() {
		b.WriteString(line + "\n")
		line, used = cont, 0
	}
	var place func(u string)
	place = func(u string) {
		n := utf8.RuneCountInString(u)
		sep := 0
		if used > 0 {
			sep = 1
		}
		switch {
		case sep+n <= room():
		case used > 0 && n <= width-utf8.RuneCountInString(cont):
			newline()
			sep = 0
		case strings.Contains(u, " "):
			for word := range strings.FieldsSeq(u) {
				place(word)
			}
			return
		default:
			if used > 0 {
				newline()
			}
			r := []rune(u)
			for len(r) > room() {
				fit := room()
				line += string(r[:fit])
				used++
				r = r[fit:]
				newline()
			}
			u, sep = string(r), 0
		}
		if sep == 1 {
			line += " "
		}
		line += u
		used++
	}
	for _, u := range units {
		place(u)
	}
	b.WriteString(line + "\n")
	return b.String()
}

// Package htmlview renders a readout as one self-contained HTML page for a
// person to open in a browser. Like textview, it reads the readout only through
// its published JSON, as any renderer outside lookout would. The page loads
// nothing: its style, drawings and one script are inline, and a content
// security policy pins that script by its hash and blocks every fetch, so
// opening it reaches no network whatever the readout holds. The script only
// lays out and shows or hides a map Go has already decided.
package htmlview

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jakewan/lookout/internal/viewrules"
)

//go:embed page.html.tmpl
var pageSource string

// pageStyle is inlined into the page's one <style> element, which is why it
// must never hold that element's closing tag.
//
//go:embed page.css
var pageStyle string

// pageScript is inlined into the page's one executable <script> element,
// which is why it must never hold that element's closing tag or open a
// comment.
//
//go:embed page.js
var pageScript string

// scriptHash is what the page's content security policy admits: the script
// above and nothing else.
func scriptHash() string {
	sum := sha256.Sum256([]byte(pageScript))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// The hash goes into the template's own text before parsing, so it is part of
// the policy as written rather than a value html/template escapes into it.
var pageTemplate = template.Must(template.New("page").Funcs(template.FuncMap{
	// The stylesheet and the script are lookout's own files, not readout
	// text, so they are trusted rather than escaped.
	"style":  func() template.CSS { return template.CSS(pageStyle) },
	"script": func() template.JS { return template.JS(pageScript) },
}).Parse(strings.Replace(pageSource, "SCRIPT_HASH", scriptHash(), 1)))

// readout is the part of a readout.v1 document the page shows. Fields the
// contract may gain are ignored, as it asks of a renderer.
type readout struct {
	Setting            string      `json:"setting"`
	GeneratedAt        time.Time   `json:"generated_at"`
	EventWindowSeconds int64       `json:"event_window_seconds"`
	Workstreams        []rawStream `json:"workstreams"`
	Links              []rawLink   `json:"workstream_links"`
	Unplaced           []struct{}  `json:"unplaced"`
	Health             []rawHealth `json:"health"`
	DismissedCount     int         `json:"dismissed_count"`
	CompletedCount     int         `json:"completed_count"`
}

type rawStream struct {
	Name             string      `json:"name"`
	Title            string      `json:"title"`
	IdleSeconds      *int64      `json:"idle_seconds"`
	AgeSeconds       *int64      `json:"age_seconds"`
	Events           []rawEvent  `json:"events"`
	InputsIncomplete []string    `json:"inputs_incomplete"`
	MembersUnread    []string    `json:"members_unread"`
	Items            []rawPlaced `json:"items"`
}

type rawEvent struct {
	Input  string    `json:"input"`
	ID     string    `json:"id"`
	Title  string    `json:"title"`
	At     time.Time `json:"at"`
	Joined *join     `json:"joined"`
}

type rawPlaced struct {
	Input string `json:"input"`
	Item  struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Parent string `json:"parent"`
		Links  []struct {
			Href string `json:"href"`
		} `json:"links"`
	} `json:"item"`
	Joined *join `json:"joined"`
}

type rawLink struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Kind  string   `json:"kind"`
	Items []string `json:"items"`
}

type rawHealth struct {
	Input          string     `json:"input"`
	Status         string     `json:"status"`
	Reasons        []string   `json:"reasons"`
	FetchedAt      *time.Time `json:"fetched_at"`
	RecordCount    int        `json:"record_count"`
	MissingFields  []string   `json:"missing_fields"`
	SupplierReason *string    `json:"supplier_reason"`
	Details        []string   `json:"details"`
	ExcludedCount  int        `json:"excluded_count"`
}

// page is what the template draws. Every string in it taken from the readout
// has been made safe to show by viewrules.Clean, except an item's link
// address, which is admitted only as a web address and left to the
// template's URL escaping.
type page struct {
	Setting string
	AsOf    string
	// Read is the readout's generated_at exactly as written, so a person
	// and an agent reading view.json can tell they are on the same read.
	Read   string
	Window string
	// Map is what the page's script draws; html/template writes it into the
	// page's data block escaped as JSON.
	Map viewModel
	// Active counts the workstreams with an event within the event window,
	// as snapshot does, the measure the vision sets beside a
	// work-in-progress limit.
	Active       int
	Health       []input
	Workstreams  []workstream
	Unplaced     int
	Dismissed    int
	Completed    int
	AnyUnhealthy bool
}

type input struct {
	Input string
	// Class is ok, degraded, error, or fault for a status this version does
	// not know, which is shown as a fault rather than trusted as healthy.
	Class          string
	Label          string
	Unhealthy      bool
	Reasons        []string
	Details        []string
	MissingFields  []string
	SupplierReason string
	Excluded       int
	Records        int
	FetchedAt      string
}

type workstream struct {
	// Anchor links the workstream's overview lane to its detail.
	Anchor string
	// Behind marks a workstream quieter than one ranked below it.
	Behind bool
	// Name is the id commands take; Title is what the user called it, or
	// empty, in which case the id is the label.
	Name  string
	Title string
	Idle  string
	Age   string
	// Unread and Stale are viewrules.Marker's verdicts, so the template
	// branches on them rather than on the marker's spelling.
	Unread     bool
	Stale      bool
	Incomplete []string
	Groups     []group
	Lane       []mark
}

type group struct {
	Input string
	Items []item
}

type item struct {
	ID     string
	Title  string
	State  string
	Href   string
	Unread bool
	Join   *join
}

// join is why a workstream holds an item it does not name. By is shown as
// given, so a kind of join this version does not know still reads as a join.
type join struct {
	By     string `json:"by"`
	Member string `json:"member"`
	Via    string `json:"via"`
}

// mark is one item's latest touch on a workstream's lane, at X across the
// event window from its start, 0, to the readout's moment, 1.
type mark struct {
	X      float64
	Joined bool
	Label  string
}

// Left is the mark's place across its lane, as a CSS percentage.
func (m mark) Left() string { return fmt.Sprintf("%.2f%%", m.X*100) }

// Render writes the page for data, a readout.v1 document, to w.
func Render(w io.Writer, data []byte) error {
	p, err := build(data)
	if err != nil {
		return err
	}
	if err := pageTemplate.Execute(w, p); err != nil {
		return fmt.Errorf("writing the page: %w", err)
	}
	return nil
}

func build(data []byte) (page, error) {
	var r readout
	if err := json.Unmarshal(data, &r); err != nil {
		return page{}, fmt.Errorf("reading the readout: %w", err)
	}
	var as struct {
		GeneratedAt string `json:"generated_at"`
	}
	if err := json.Unmarshal(data, &as); err != nil {
		return page{}, fmt.Errorf("reading the readout: %w", err)
	}
	window := r.EventWindowSeconds
	p := page{
		Setting:   viewrules.Clean(r.Setting),
		AsOf:      stamp(r.GeneratedAt),
		Read:      viewrules.Clean(as.GeneratedAt),
		Window:    viewrules.Span(window),
		Unplaced:  len(r.Unplaced),
		Dismissed: r.DismissedCount,
		Completed: r.CompletedCount,
	}
	names := make([]string, 0, len(r.Health))
	for _, h := range r.Health {
		names = append(names, h.Input)
		p.AnyUnhealthy = p.AnyUnhealthy || viewrules.Unhealthy(h.Status)
		if viewrules.Hidden(h.Status) {
			continue
		}
		p.Health = append(p.Health, healthOf(h))
	}
	order := viewrules.NewOrder(names)
	for i, ws := range r.Workstreams {
		shown := workstreamOf(ws, order, r.GeneratedAt, window)
		shown.Anchor = fmt.Sprintf("ws-%d", i+1)
		shown.Behind = behind(r.Workstreams, i)
		if len(ws.Events) > 0 {
			p.Active++
		}
		p.Workstreams = append(p.Workstreams, shown)
	}
	p.Map = mapOf(r, p.Workstreams, order)
	return p, nil
}

// behind reports whether the workstream at i has gone quieter than one the
// stated order ranks below it. The vision ranks by stated order and lets
// observed staleness flag a workstream falling behind it; a workstream with
// nothing read has no touch to compare either way.
func behind(all []rawStream, i int) bool {
	idle := all[i].IdleSeconds
	if idle == nil {
		return false
	}
	return slices.ContainsFunc(all[i+1:], func(below rawStream) bool {
		return below.IdleSeconds != nil && *below.IdleSeconds < *idle
	})
}

func healthOf(h rawHealth) input {
	in := input{
		Input:         viewrules.Clean(h.Input),
		Class:         h.Status,
		Label:         viewrules.Clean(h.Status),
		Unhealthy:     viewrules.Unhealthy(h.Status),
		Reasons:       cleanAll(h.Reasons),
		Details:       cleanAll(h.Details),
		MissingFields: cleanAll(h.MissingFields),
		Excluded:      h.ExcludedCount,
		Records:       h.RecordCount,
	}
	switch h.Status {
	case "ok", "degraded", "error":
	default:
		in.Class = "fault"
	}
	if h.SupplierReason != nil {
		in.SupplierReason = viewrules.Clean(*h.SupplierReason)
	}
	if h.FetchedAt != nil {
		in.FetchedAt = stamp(*h.FetchedAt)
	}
	return in
}

func workstreamOf(ws rawStream, order viewrules.Order, generated time.Time, window int64) workstream {
	marker := viewrules.Marker(ws.IdleSeconds, window)
	out := workstream{
		Name:       viewrules.Clean(ws.Name),
		Title:      viewrules.Clean(ws.Title),
		Idle:       viewrules.Age(ws.IdleSeconds),
		Age:        viewrules.Age(ws.AgeSeconds),
		Unread:     marker == viewrules.Unread,
		Stale:      marker == viewrules.Stale,
		Incomplete: cleanAll(ws.InputsIncomplete),
	}
	// The lists and the map take what a workstream holds in one order, so
	// the two views of it cannot come to disagree.
	last := ""
	for _, e := range entriesOf(ws, order) {
		shown := item{ID: viewrules.Clean(e.id), Unread: true}
		if e.placed != nil {
			shown = itemOf(*e.placed)
		}
		if len(out.Groups) == 0 || e.input != last {
			out.Groups = append(out.Groups, group{Input: viewrules.Clean(e.input)})
			last = e.input
		}
		g := &out.Groups[len(out.Groups)-1]
		g.Items = append(g.Items, shown)
	}
	for _, e := range ws.Events {
		out.Lane = append(out.Lane, mark{
			X:      position(generated.Sub(e.At), window),
			Joined: e.Joined != nil,
			Label:  viewrules.Clean(e.Input + ":" + e.ID + " " + stamp(e.At)),
		})
	}
	return out
}

func itemOf(p rawPlaced) item {
	it := item{
		ID:    viewrules.Clean(p.Item.ID),
		Title: viewrules.Clean(p.Item.Title),
		State: viewrules.Clean(p.Item.State),
	}
	for _, l := range p.Item.Links {
		// Only a web address is linked; anything else a supplier wrote, a
		// script address among them, is left unlinked.
		if strings.HasPrefix(l.Href, "https://") || strings.HasPrefix(l.Href, "http://") {
			it.Href = l.Href
			break
		}
	}
	if p.Joined != nil {
		j := join{By: viewrules.Clean(p.Joined.By), Member: viewrules.Clean(p.Joined.Member), Via: viewrules.Clean(p.Joined.Via)}
		it.Join = &j
	}
	return it
}

// position places a touch ago before the readout across a window of window
// seconds. A touch the readout dates after itself, which a skewed clock can
// produce, sits at the readout's end, and with no window to spread over every
// touch does.
func position(ago time.Duration, window int64) float64 {
	if window <= 0 {
		return 1
	}
	x := 1 - ago.Seconds()/float64(window)
	return min(max(x, 0), 1)
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }

func cleanAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, viewrules.Clean(s))
	}
	return out
}

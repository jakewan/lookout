// Package htmlview renders a readout as one self-contained HTML page for a
// person to open in a browser. Like textview, it reads the readout only through
// its published JSON, as any renderer outside lookout would. The page loads
// nothing: its style and drawings are inline and it carries no script, and a
// content security policy holds it to that, so opening it reaches no network
// whatever the readout holds.
package htmlview

import (
	_ "embed"
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

var pageTemplate = template.Must(template.New("page").Parse(pageSource))

// readout is the part of a readout.v1 document the page shows. Fields the
// contract may gain are ignored, as it asks of a renderer.
type readout struct {
	Setting            string      `json:"setting"`
	GeneratedAt        time.Time   `json:"generated_at"`
	EventWindowSeconds int64       `json:"event_window_seconds"`
	Workstreams        []rawStream `json:"workstreams"`
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
		ID    string `json:"id"`
		Title string `json:"title"`
		State string `json:"state"`
		Links []struct {
			Href string `json:"href"`
		} `json:"links"`
	} `json:"item"`
	Joined *join `json:"joined"`
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
	Window  string
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
	window := r.EventWindowSeconds
	p := page{
		Setting:   viewrules.Clean(r.Setting),
		AsOf:      stamp(r.GeneratedAt),
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
	// Each input's members come first, in the configuration's order, then the
	// members no input supplied, then what the workstream joined; so a join
	// never reads as a membership.
	members := map[string][]item{}
	unread := map[string][]item{}
	joins := map[string][]item{}
	var inputs []string
	seen := func(in string) {
		if !slices.Contains(inputs, in) {
			inputs = append(inputs, in)
		}
	}
	for _, it := range ws.Items {
		seen(it.Input)
		shown := itemOf(it)
		if it.Joined != nil {
			joins[it.Input] = append(joins[it.Input], shown)
			continue
		}
		members[it.Input] = append(members[it.Input], shown)
	}
	for _, m := range ws.MembersUnread {
		// readout.v1 writes a member as <input>:<id>, and an input's name
		// holds no colon, so the first one ends it.
		in, id, _ := strings.Cut(m, ":")
		seen(in)
		unread[in] = append(unread[in], item{ID: viewrules.Clean(id), Unread: true})
	}
	slices.SortStableFunc(inputs, func(a, b string) int { return order.Rank(a) - order.Rank(b) })
	for _, in := range inputs {
		items := slices.Concat(members[in], unread[in], joins[in])
		out.Groups = append(out.Groups, group{Input: viewrules.Clean(in), Items: items})
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

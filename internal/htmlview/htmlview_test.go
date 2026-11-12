package htmlview

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// busy is an invented readout shaped like a busy work setting: an epic with
// children, joins of every kind, a degraded and a failing supplied input, one
// absent by design, a stale workstream and an unread one.
func busy(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "busy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// encode marshals a fixture readout, failing the spec where it breaks
// readout.v1 or the facts the contract leaves to lookout: last_touched and
// idle_seconds are null together, and an incomplete input is unhealthy.
func encode(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := schema.Compile("readout.v1")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("fixture violates readout.v1: %v", err)
	}
	var r struct {
		Workstreams []struct {
			Name             string   `json:"name"`
			LastTouched      *string  `json:"last_touched"`
			IdleSeconds      *int64   `json:"idle_seconds"`
			InputsIncomplete []string `json:"inputs_incomplete"`
		} `json:"workstreams"`
		Health []struct {
			Input  string `json:"input"`
			Status string `json:"status"`
		} `json:"health"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, h := range r.Health {
		status[h.Input] = h.Status
	}
	for _, w := range r.Workstreams {
		if (w.LastTouched == nil) != (w.IdleSeconds == nil) {
			t.Fatalf("fixture workstream %q: last_touched and idle_seconds disagree on null", w.Name)
		}
		for _, in := range w.InputsIncomplete {
			if s := status[in]; s != "error" && s != "degraded" {
				t.Fatalf("fixture workstream %q: incomplete input %q is %q, not error or degraded", w.Name, in, s)
			}
		}
	}
	return data
}

func pageOf(t *testing.T, doc map[string]any) page {
	t.Helper()
	p, err := build(encode(t, doc), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func render(t *testing.T, doc map[string]any) string {
	t.Helper()
	var b bytes.Buffer
	if err := Render(&b, encode(t, doc), Options{}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func lane(t *testing.T, p page, name string) workstream {
	t.Helper()
	for _, ws := range p.Workstreams {
		if ws.Name == name {
			return ws
		}
	}
	t.Fatalf("no workstream %q", name)
	return workstream{}
}

func health(doc map[string]any) []any      { return doc["health"].([]any) }
func workstreams(doc map[string]any) []any { return doc["workstreams"].([]any) }
func record(v any) map[string]any          { return v.(map[string]any) }
func withStatus(h any, s string) any       { record(h)["status"] = s; return h }

func TestHeaderSaysWhichSettingAndWhenItWasRead(t *testing.T) {
	p := pageOf(t, busy(t))
	if p.Setting != "example" {
		t.Errorf("setting = %q, want example", p.Setting)
	}
	if p.AsOf != "2030-06-15 12:00 UTC" {
		t.Errorf("as of = %q, want 2030-06-15 12:00 UTC", p.AsOf)
	}
}

func TestWorkstreamsKeepTheStatedOrder(t *testing.T) {
	p := pageOf(t, busy(t))
	var got []string
	for _, ws := range p.Workstreams {
		got = append(got, ws.Name)
	}
	want := []string{"search-revamp", "billing-export", "release-train", "infra-upgrade", "docs-site", "onboarding"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestUnplacedItemsListNewestTouchFirstByTheTokenAdoptTakes(t *testing.T) {
	p := pageOf(t, busy(t))
	want := []unplacedItem{
		{Token: "sessions:owner/tools:main", State: "open", Touched: "30m"},
		{Token: `"code:owner/app:fix/\u200bcache"`, ShellQuote: true, Title: "Cache the build", State: "open", Touched: "2h"},
		{Token: "code:owner/app:spike/cache", State: "open", Touched: "3h"},
		{Token: "hub:owner/app#60", Title: "Bump the linter", State: "open", Touched: "8h", Href: "https://example.com/owner/app/pull/60"},
	}
	if !slices.Equal(p.UnplacedItems, want) {
		t.Errorf("unplaced items =\n%+v\nwant\n%+v", p.UnplacedItems, want)
	}
}

func TestATokenHoldingASpaceIsMarkedForShellQuoting(t *testing.T) {
	doc := busy(t)
	record(record(doc["unplaced"].([]any)[0])["item"])["id"] = "owner/app:two  spaces"
	p := pageOf(t, doc)
	i := slices.IndexFunc(p.UnplacedItems, func(u unplacedItem) bool { return u.Token == "code:owner/app:two  spaces" })
	if i < 0 || !p.UnplacedItems[i].ShellQuote {
		t.Errorf("unplaced items = %+v, want code:owner/app:two  spaces kept as written and marked for shell quoting", p.UnplacedItems)
	}
}

func TestUnplacedItemsTouchedTogetherKeepTheReadoutsOrder(t *testing.T) {
	doc := busy(t)
	for _, u := range doc["unplaced"].([]any) {
		record(record(u)["item"])["last_touched"] = "2030-06-15T09:00:00Z"
	}
	p := pageOf(t, doc)
	var got []string
	for _, u := range p.UnplacedItems {
		got = append(got, u.Token)
	}
	want := []string{"code:owner/app:spike/cache", "hub:owner/app#60", `"code:owner/app:fix/\u200bcache"`, "sessions:owner/tools:main"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// readout.v1 admits an RFC 3339 time Go's parser refuses, such as one written
// with a lowercase t and z, so the page shows how long since such an item was
// touched as unknown rather than refusing the readout.
func TestATouchGoCannotParseLeavesTheItemListedLast(t *testing.T) {
	doc := busy(t)
	record(record(record(workstreams(doc)[0])["items"].([]any)[0])["item"])["last_touched"] = "2030-06-15t09:00:00z"
	unplaced := doc["unplaced"].([]any)
	record(record(unplaced[3])["item"])["last_touched"] = "2030-06-15t11:59:00z"
	p := pageOf(t, doc)
	last := p.UnplacedItems[len(p.UnplacedItems)-1]
	if last.Token != "sessions:owner/tools:main" || last.Touched != "--" {
		t.Errorf("last unplaced item = %+v, want sessions:owner/tools:main touched --", last)
	}
	if out := render(t, doc); !strings.Contains(out, "last touch unreadable") || strings.Contains(out, "-- ago") {
		t.Error("an item whose touch cannot be read is not said to be so")
	}
}

func TestNoUnplacedSectionWhereNothingIsUnplaced(t *testing.T) {
	doc := busy(t)
	doc["unplaced"] = []any{}
	if out := render(t, doc); strings.Contains(out, `id="unplaced"`) {
		t.Error("the page draws an unplaced section with nothing in it")
	}
	if out := render(t, busy(t)); !strings.Contains(out, `id="unplaced"`) {
		t.Error("the page draws no unplaced section where items are unplaced")
	}
}

func TestAgeAndMarkerTellFreshStaleAndUnread(t *testing.T) {
	p := pageOf(t, busy(t))
	for _, tc := range []struct {
		name, idle, started string
		stale, unread       bool
	}{
		{"search-revamp", "40m", "20d", false, false},
		{"docs-site", "9d", "30d", true, false},
		{"onboarding", "--", "--", false, true},
	} {
		ws := lane(t, p, tc.name)
		if ws.Idle != tc.idle || ws.Age != tc.started || ws.Stale != tc.stale || ws.Unread != tc.unread {
			t.Errorf("%s: idle %q, age %q, stale %v, unread %v; want %q, %q, %v, %v", tc.name, ws.Idle, ws.Age, ws.Stale, ws.Unread, tc.idle, tc.started, tc.stale, tc.unread)
		}
	}
}

func TestHealthShowsEachExpectedStateDistinctlyAndHidesAbsentByDesign(t *testing.T) {
	p := pageOf(t, busy(t))
	got := map[string]string{}
	var order []string
	for _, h := range p.Health {
		got[h.Input] = h.Class
		order = append(order, h.Input)
	}
	want := map[string]string{"code": "ok", "hub": "ok", "sessions": "ok", "tracker": "degraded", "chat": "error"}
	for in, class := range want {
		if got[in] != class {
			t.Errorf("input %s class = %q, want %q", in, got[in], class)
		}
	}
	if _, shown := got["calendar"]; shown {
		t.Error("an input absent by design is shown; the vision hides it")
	}
	if !slices.Equal(order, []string{"code", "hub", "sessions", "tracker", "chat"}) {
		t.Errorf("health order = %v, want the readout's", order)
	}
}

func TestAnUnknownHealthStatusIsAFaultNeverOk(t *testing.T) {
	doc := busy(t)
	// readout.v1 lists today's statuses, so a later one cannot pass encode.
	withStatus(health(doc)[0], "a_later_status")
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := build(data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := p.Health[0]
	if h.Class != "fault" || h.Label != "a_later_status" {
		t.Errorf("class %q label %q, want fault and the status itself", h.Class, h.Label)
	}
	if !p.AnyUnhealthy {
		t.Error("an unknown status does not count as unhealthy")
	}
}

func TestAnUnhealthyInputSaysWhy(t *testing.T) {
	doc := busy(t)
	tracker := record(health(doc)[3])
	tracker["supplier_reason"] = "export timed out"
	tracker["excluded_count"] = 4
	tracker["reasons"] = append(tracker["reasons"].([]any), "a_later_reason")
	p := pageOf(t, doc)
	var h input
	for _, x := range p.Health {
		if x.Input == "tracker" {
			h = x
		}
	}
	for _, want := range []string{"stale", "missing_fields", "a_later_reason"} {
		if !slices.Contains(h.Reasons, want) {
			t.Errorf("reasons = %v, want %q", h.Reasons, want)
		}
	}
	if !slices.Contains(h.Details, "tracker.json was written 30h ago; max_age is 24h") {
		t.Errorf("details = %v", h.Details)
	}
	if !slices.Contains(h.MissingFields, "due") {
		t.Errorf("missing fields = %v, want due", h.MissingFields)
	}
	if h.SupplierReason != "export timed out" || h.Excluded != 4 {
		t.Errorf("supplier reason %q, excluded %d", h.SupplierReason, h.Excluded)
	}
	if h.FetchedAt != "2030-06-15 11:55 UTC" {
		t.Errorf("fetched at = %q", h.FetchedAt)
	}
}

func TestItemsAreGroupedByInputMembersFirstThenUnreadThenJoins(t *testing.T) {
	p := pageOf(t, busy(t))
	ws := lane(t, p, "search-revamp")
	var inputs []string
	for _, g := range ws.Groups {
		inputs = append(inputs, g.Input)
	}
	if !slices.Equal(inputs, []string{"code", "hub", "sessions", "tracker"}) {
		t.Errorf("groups = %v, want health's order", inputs)
	}
	tracker := ws.Groups[3]
	var ids []string
	for _, it := range tracker.Items {
		ids = append(ids, it.ID)
	}
	if !slices.Equal(ids, []string{"ABC-12", "ABC-13", "ABC-14", "WORK-7"}) {
		t.Errorf("tracker items = %v, want members then the join", ids)
	}
	if tracker.Items[3].Join == nil || tracker.Items[0].Join != nil {
		t.Error("a member reads as a join, or a join as a member")
	}

	billing := lane(t, p, "billing-export")
	hub := billing.Groups[1]
	if hub.Input != "hub" || len(hub.Items) != 2 || hub.Items[1].ID != "owner/billing#5" || !hub.Items[1].Unread {
		t.Errorf("billing's hub group = %+v, want its member then the unread one", hub)
	}

	onboarding := lane(t, p, "onboarding")
	if len(onboarding.Groups) != 1 || onboarding.Groups[0].Input != "tracker" || !onboarding.Groups[0].Items[0].Unread {
		t.Errorf("onboarding groups = %+v, want its unread member", onboarding.Groups)
	}
}

func TestAJoinSaysHowAndThroughWhat(t *testing.T) {
	p := pageOf(t, busy(t))
	find := func(ws, input, id string) item {
		for _, g := range lane(t, p, ws).Groups {
			for _, it := range g.Items {
				if g.Input == input && it.ID == id {
					return it
				}
			}
		}
		t.Fatalf("no %s:%s in %s", input, id, ws)
		return item{}
	}
	for _, tc := range []struct {
		ws, input, id string
		want          join
	}{
		{"search-revamp", "hub", "owner/app#41", join{By: "key", Member: "tracker:ABC-13"}},
		{"search-revamp", "hub", "owner/app#42", join{By: "description", Member: "tracker:ABC-12"}},
		{"search-revamp", "tracker", "WORK-7", join{By: "notes", Member: "tracker:ABC-12"}},
		{"search-revamp", "code", "owner/app:feature/result-filters", join{By: "pull_request", Member: "tracker:ABC-13", Via: "hub:owner/app#41"}},
		{"search-revamp", "sessions", "owner/app:feature/result-filters", join{By: "pull_request", Member: "tracker:ABC-13", Via: "hub:owner/app#41"}},
		{"infra-upgrade", "hub", "owner/infra#4", join{By: "milestone", Member: "hub:owner/infra#3"}},
	} {
		it := find(tc.ws, tc.input, tc.id)
		if it.Join == nil || *it.Join != tc.want {
			t.Errorf("%s:%s join = %+v, want %+v", tc.input, tc.id, it.Join, tc.want)
		}
	}
}

// A parent join is listed as the other joins are, by how it joined and the
// adopted ancestor it joined, though the readout names no via.
func TestAParentJoinIsListedByTheAncestorItJoined(t *testing.T) {
	doc := busy(t)
	i := slices.IndexFunc(workstreams(doc), func(w any) bool { return record(w)["name"] == "search-revamp" })
	if i < 0 {
		t.Fatal("busy has no search-revamp workstream")
	}
	parentJoined(doc, i, "TASK-77", "ABC-12", "tracker:ABC-12")
	out := render(t, doc)
	if want := `joined by parent &rarr; <bdi>tracker:ABC-12</bdi></span>`; !strings.Contains(out, want) {
		t.Errorf("page lacks %q for the parent-joined item", want)
	}
}

func TestAWorkstreamOnAFailingInputSaysWhichInputs(t *testing.T) {
	p := pageOf(t, busy(t))
	if got := lane(t, p, "search-revamp").Incomplete; !slices.Equal(got, []string{"tracker", "chat"}) {
		t.Errorf("incomplete = %v", got)
	}
	if got := lane(t, p, "billing-export").Incomplete; len(got) != 0 {
		t.Errorf("billing incomplete = %v, want none", got)
	}
}

// The page draws the readout's active and behind verdicts rather than
// deciding them again, so it agrees with show, snapshot and an agent reading
// the readout. Each verdict here is one the page would not reach from the
// workstream's idle time, so a page that re-derived them would fail.
func TestThePageDrawsTheReadoutsActiveAndBehindVerdicts(t *testing.T) {
	doc := busy(t)
	before := pageOf(t, doc).Active
	// docs-site was idle past the window; an event at the window's start makes
	// it active while its idle time, compared with the window, would call it
	// stale. search-revamp is the freshest, so no comparison of idle times
	// would put it behind.
	docs := record(readoutWorkstream(doc, "docs-site"))
	docs["active"] = true
	docs["events"] = []any{map[string]any{"input": "tracker", "id": "DOC-1", "at": "2030-06-08T12:00:00Z"}}
	record(workstreams(doc)[0])["behind"] = true
	p := pageOf(t, doc)
	if got := lane(t, p, "docs-site"); got.Stale {
		t.Error("docs-site is marked stale, but the readout calls it active")
	}
	if got := lane(t, p, "search-revamp"); !got.Behind {
		t.Error("search-revamp is not marked behind, but the readout says it is")
	}
	if p.Active != before+1 {
		t.Errorf("active = %d, want %d: the readout calls docs-site active", p.Active, before+1)
	}
}

// readoutWorkstream is the readout's workstream named name.
func readoutWorkstream(doc map[string]any, name string) any {
	for _, w := range workstreams(doc) {
		if record(w)["name"] == name {
			return w
		}
	}
	panic("no workstream " + name)
}

func TestTheWindowIsLabelledExactly(t *testing.T) {
	doc := busy(t)
	doc["event_window_seconds"] = 36 * 60 * 60
	if got := pageOf(t, doc).Window; got != "1d12h" {
		t.Errorf("window = %q, want 1d12h", got)
	}
}

func TestCountsAreFlaggedWhileAnyInputIsUnhealthy(t *testing.T) {
	doc := busy(t)
	p := pageOf(t, doc)
	if len(p.UnplacedItems) != 4 || p.Dismissed != 2 || p.Completed != 1 || !p.AnyUnhealthy {
		t.Errorf("counts %d/%d/%d flagged %v", len(p.UnplacedItems), p.Dismissed, p.Completed, p.AnyUnhealthy)
	}
	for _, h := range health(doc) {
		if record(h)["status"] != "absent_by_design" {
			withStatus(h, "ok")
			record(h)["reasons"] = []any{}
		}
	}
	for _, w := range workstreams(doc) {
		record(w)["inputs_incomplete"] = []any{}
	}
	if pageOf(t, doc).AnyUnhealthy {
		t.Error("counts flagged with every input healthy or absent by design")
	}
}

// TestAFailingAwaitingInputDoesNotFlagTheCounts pins that the counts, which
// no awaiting input feeds, are not flagged by one failing, while its health
// still shows.
func TestAFailingAwaitingInputDoesNotFlagTheCounts(t *testing.T) {
	doc := busy(t)
	for _, h := range health(doc) {
		if record(h)["status"] != "absent_by_design" {
			withStatus(h, "ok")
			record(h)["reasons"] = []any{}
		}
	}
	for _, w := range workstreams(doc) {
		record(w)["inputs_incomplete"] = []any{}
	}
	doc["health"] = append(health(doc), map[string]any{
		"input": "asks", "status": "error", "reasons": []any{"missing"}, "fetched_at": nil, "record_count": 0,
		"missing_fields": []any{}, "schema_violations": []any{}, "supplier_reason": nil, "details": []any{}, "excluded_count": 0,
	})
	doc["awaiting_inputs"] = []any{"asks"}
	doc["awaiting"] = []any{}

	p := pageOf(t, doc)

	if p.AnyUnhealthy {
		t.Error("counts flagged by an awaiting input, which feeds none of them")
	}
	if !slices.ContainsFunc(p.Health, func(in input) bool { return in.Input == "asks" }) {
		t.Errorf("health = %+v, want the failing awaiting input listed", p.Health)
	}
}

func TestTheLanePlotsEachItemsLastTouchAcrossTheWindow(t *testing.T) {
	p := pageOf(t, busy(t))
	marks := lane(t, p, "search-revamp").Lane
	if len(marks) != 8 {
		t.Fatalf("%d marks, want one per event", len(marks))
	}
	// The newest touch, 40 minutes before a week's window ends.
	if got, want := marks[0].X, 1-2400.0/604800; got != want {
		t.Errorf("x = %v, want %v", got, want)
	}
	if !marks[0].Joined || marks[3].Joined {
		t.Error("joined events are not told from member events")
	}
	if got := lane(t, p, "docs-site").Lane; len(got) != 0 {
		t.Errorf("a workstream with no events in the window has marks %v", got)
	}
}

func TestTheLaneHoldsEveryMarkInsideIt(t *testing.T) {
	for name, tc := range map[string]struct {
		window int64
		at     string
		want   float64
	}{
		"a zero window":                 {0, "2030-06-15T11:00:00Z", 1},
		"a touch after the readout":     {604800, "2030-06-15T13:00:00Z", 1},
		"a touch at the window's start": {3600, "2030-06-15T11:00:00Z", 0},
	} {
		t.Run(name, func(t *testing.T) {
			doc := busy(t)
			doc["event_window_seconds"] = tc.window
			w := record(workstreams(doc)[1])
			w["events"] = []any{map[string]any{"input": "hub", "id": "owner/billing#7", "at": tc.at}}
			marks := lane(t, pageOf(t, doc), "billing-export").Lane
			if len(marks) != 1 || marks[0].X != tc.want {
				t.Errorf("marks = %+v, want one at %v", marks, tc.want)
			}
		})
	}
}

// fetchers are markup that would make a browser reach the network on its own
// when the page opens. An anchor is absent on purpose: it is followed only
// when clicked.
var fetchers = regexp.MustCompile(`(?i)<script|<link|<iframe|<object|<embed|<form|<image|<use|<img|<video|<audio|<source|\ssrc=|srcset|@import|url\(|http-equiv="?refresh`)

// The page's own script and its data block are the two script elements it
// may hold, pinned by TestTheOneScriptIsPinnedByItsHash. Both are left out of
// this scan: the data block's readout text can say url( without being markup,
// and TestReadoutTextCannotEscapeTheDataBlock holds it inert; the script is
// held by TestTheScriptReachesNoSinkNavigationOrConnection. The rest of the
// page is held to fetching nothing.
func TestThePageCannotFetchAnything(t *testing.T) {
	out := render(t, busy(t))
	script, data := scriptOf(t, out)
	out = strings.Replace(out, script, "", 1)
	out = strings.Replace(out, data, "", 1)
	out = strings.Replace(out, `<script type="module"></script>`, "", 1)
	out = strings.Replace(out, `<script type="application/json" id="lookout-map"></script>`, "", 1)
	for _, want := range []string{
		`<meta http-equiv="x-dns-prefetch-control" content="off">`,
		`<meta name="referrer" content="no-referrer">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if m := fetchers.FindString(out); m != "" {
		t.Errorf("page holds %q, which fetches when the page opens", m)
	}
}

// The stylesheet is inlined as trusted CSS, so html/template does not stop it
// closing its own element; a closing tag in it would turn the rest of the
// file into markup the content security policy was never written for.
func TestTheStylesheetCannotCloseItsElement(t *testing.T) {
	if strings.Contains(strings.ToLower(pageStyle), "</style") {
		t.Error("page.css holds </style, which would close the element it is inlined into")
	}
}

func TestTheTitleNamesTheSettingAndNoWorkstream(t *testing.T) {
	out := render(t, busy(t))
	if !strings.Contains(out, "<title>example &middot; lookout</title>") {
		t.Error("the title does not lead with the setting")
	}
	// A workstream's own title is no more fit for the browser tab than its id.
	if strings.Contains(out, "<title>Billing") {
		t.Error("a workstream title reached the page's title")
	}
}

// Render does not check the setting's name; its caller does. The title
// still shows whatever name it is given rather than obeying it.
func TestTheSettingIsShownInTheTitleNotObeyed(t *testing.T) {
	doc := busy(t)
	doc["setting"] = "</title><b>x"
	out := render(t, doc)
	if strings.Contains(out, "<b>x") {
		t.Error("markup in the setting reached the page unescaped")
	}
	if !strings.Contains(out, "<title>&lt;/title&gt;&lt;b&gt;x &middot; lookout</title>") {
		t.Error("the setting is not shown escaped in the title")
	}
}

func TestAWorkstreamTitleLeadsAndItsIDStaysBesideIt(t *testing.T) {
	p := pageOf(t, busy(t))
	billing := p.Workstreams[1]
	if billing.Title != "Billing export to the ledger" || billing.Name != "billing-export" {
		t.Errorf("billing = title %q, name %q, want the readout's title and id", billing.Title, billing.Name)
	}
	if search := p.Workstreams[0]; search.Title != "" {
		t.Errorf("search-revamp title = %q, want none for a workstream without one", search.Title)
	}
	out := render(t, busy(t))
	for _, want := range []string{`<bdi>Billing export to the ledger</bdi>`, `<span class="wsid"><bdi>billing-export</bdi></span>`} {
		if n := strings.Count(out, want); n != 2 {
			t.Errorf("page shows %s %d times, want once in the overview and once in the detail", want, n)
		}
	}
}

func TestTitlesAreShownNotObeyed(t *testing.T) {
	doc := busy(t)
	record(workstreams(doc)[1])["title"] = "<b>bold</b>\u202eevil"
	out := render(t, doc)
	if strings.Contains(out, "<b>bold</b>") || strings.Contains(out, "\u202e") {
		t.Error("markup or a bidirectional override in a title reached the page")
	}
	if !strings.Contains(out, `<bdi>&#34;&lt;b&gt;bold&lt;/b&gt;\u202eevil&#34;</bdi>`) {
		t.Error("the title is not shown escaped and isolated")
	}
}

func TestNamesAreShownNotObeyed(t *testing.T) {
	doc := busy(t)
	w := record(workstreams(doc)[0])
	w["name"] = "<b>bold</b>\u202eevil"
	out := render(t, doc)
	if strings.Contains(out, "<b>bold</b>") {
		t.Error("markup in a name reached the page unescaped")
	}
	if strings.Contains(out, "\u202e") {
		t.Error("a bidirectional override in a name reached the page")
	}
	if !strings.Contains(out, `<bdi>&#34;&lt;b&gt;bold&lt;/b&gt;\u202eevil&#34;</bdi>`) {
		t.Error("the name is not shown escaped and isolated")
	}
}

func TestItemLinksAreOnlyWebAddresses(t *testing.T) {
	doc := busy(t)
	it := record(record(record(workstreams(doc)[1])["items"].([]any)[0])["item"])
	it["links"] = []any{map[string]any{"rel": "self", "href": "javascript:alert(1)"}}
	out := render(t, doc)
	if strings.Contains(out, "javascript:") {
		t.Error("a script address reached the page")
	}
	if !strings.Contains(out, `href="https://github.com/owner/app/pull/41"`) {
		t.Error("a web address was not linked")
	}
}

// The model specs above pin what the page computes; the golden pins what the
// template draws from it, such as each marker, flag, join badge and mark
// position, which no model spec sees.
func TestRenderMatchesTheGoldenPage(t *testing.T) {
	out := render(t, busy(t))
	want, err := os.ReadFile(filepath.Join("testdata", "busy.html.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Errorf("page differs from testdata/busy.html.golden:\n%s", out)
	}
}

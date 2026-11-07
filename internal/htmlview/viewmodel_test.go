package htmlview

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mapReadout is the invented readout the browser specs open too: every shape
// the map has to place, from parent chains and cycles to an item three
// workstreams share.
func mapReadout(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("browser", "fixtures", "map.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// nodeAt finds the one node carrying handle, failing the spec where the map
// draws it twice or not at all.
func nodeAt(t *testing.T, m viewModel, handle string) (int, mapNode) {
	t.Helper()
	found := -1
	for i, n := range m.Nodes {
		if string(n.Handle) == handle {
			if found >= 0 {
				t.Fatalf("the map draws %q more than once", handle)
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("the map does not draw %q", handle)
	}
	return found, m.Nodes[found]
}

func handlesOf(m viewModel, ids []int) []string {
	out := make([]string, 0, len(ids))
	for _, i := range ids {
		out = append(out, string(m.Nodes[i].Handle))
	}
	return out
}

func TestTheMapCentersOnTheSettingWithEveryWorkstreamInStatedOrder(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	hub := m.Nodes[m.Hub]
	if hub.Kind != "setting" || hub.Label != "example" || hub.Parent != -1 {
		t.Errorf("hub = %+v, want the setting at the root", hub)
	}
	if got := handlesOf(m, hub.Children); !slices.Equal(got, []string{"platform", "billing", "ops", "docs", "intake"}) {
		t.Errorf("workstreams = %v, want the stated order", got)
	}
}

func TestTheMapDrawsEachItemOnceUnderOneParent(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	for _, tc := range []struct{ handle, parent, why string }{
		{"tracker:P-1", "platform", "a member with no parent sits at its workstream"},
		{"tracker:P-2", "tracker:P-1", "a member hangs under its parent in the same workstream"},
		{"tracker:P-3", "tracker:P-2", "and so down a chain"},
		{"tracker:P-9", "platform", "a parent the readout lacks leaves the member at its workstream"},
		{"tracker:C-1", "platform", "a cycle breaks at its earliest node"},
		{"tracker:C-2", "tracker:C-1", "the rest of the cycle hangs from there"},
		{"tracker:J-1", "platform", "a member whose parent joined through it breaks that cycle"},
		{"tracker:J-0", "tracker:J-1", "the join hangs under its member"},
		{"hub:owner/app#10", "tracker:P-2", "a join hangs under its member"},
		{"code:owner/app:feature/split", "hub:owner/app#10", "a branch hangs under the pull request it came through"},
		{"hub:owner/app#20", "billing", "a shared item is drawn where it is a member"},
		{"tracker:B-1", "billing", "a parent another workstream holds is a cross-link, not a place"},
		{"tracker:B-3", "hub:owner/app#20", "a join comes before a parent"},
		{"hub:owner/app#21", "billing", "a member nothing read is a placeholder"},
		{"tracker:O-1", "ops", "a member at its workstream"},
		{"code:owner/app:feature/rollout", "tracker:O-1", "what joined through a shared item drawn elsewhere hangs where that item would"},
		{"tracker:I-1", "intake", "a workstream with nothing read shows what it names"},
	} {
		_, n := nodeAt(t, m, tc.handle)
		if got := string(m.Nodes[n.Parent].Handle); got != tc.parent {
			t.Errorf("%s hangs under %q, want %q: %s", tc.handle, got, tc.parent, tc.why)
		}
	}
}

func TestSiblingsKeepTheGroupOrder(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	for handle, want := range map[string][]string{
		"platform":    {"tracker:P-1", "tracker:P-9", "tracker:C-1", "tracker:J-1"},
		"tracker:P-2": {"tracker:P-3", "hub:owner/app#10"},
		"billing":     {"tracker:B-1", "tracker:B-\u200b2", "hub:owner/app#20", "hub:owner/app#21"},
	} {
		_, n := nodeAt(t, m, handle)
		if got := handlesOf(m, n.Children); !slices.Equal(got, want) {
			t.Errorf("%s children = %v, want %v", handle, got, want)
		}
	}
}

func TestCrossLinksCarryEveryEdgeTheTreeDoesNotShowOnce(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	var got []string
	for _, l := range m.Links {
		got = append(got, string(m.Nodes[l.From].Handle)+" "+l.Kind+" "+string(m.Nodes[l.To].Handle))
	}
	slices.Sort(got)
	want := []string{
		"code:owner/app:feature/rollout joined hub:owner/app#20",
		"hub:owner/app#20 joined tracker:O-1",
		"hub:owner/app#20 joined tracker:P-1",
		"tracker:B-1 parent tracker:P-1",
		"tracker:B-3 parent tracker:B-1",
		"tracker:C-1 parent tracker:C-2",
		"tracker:J-1 parent tracker:J-0",
		"tracker:O-1 blocks platform",
	}
	if !slices.Equal(got, want) {
		t.Errorf("cross-links =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// member adds a supplied tracker item to the workstream at index i, as a
// member, with parent where it is not empty.
func member(doc map[string]any, i int, id, parent string) {
	w := record(workstreams(doc)[i])
	it := map[string]any{"id": id, "last_touched": "2030-06-10T12:00:00Z", "state": "To Do", "links": []any{}, "attrs": map[string]any{}}
	if parent != "" {
		it["parent"] = parent
	}
	w["items"] = append(w["items"].([]any), map[string]any{"input": "tracker", "item": it})
}

// A parent two workstreams hold is drawn once; where the child hangs under it
// in the tree, the readout's parent link between the two workstreams says
// nothing the tree does not, so no cross-link repeats it.
func TestAParentLinkIsNotDrawnOverTheBranchThatShowsIt(t *testing.T) {
	doc := mapReadout(t)
	member(doc, 1, "E-1", "")
	member(doc, 1, "E-2", "E-1")
	member(doc, 2, "E-1", "")
	doc["workstream_links"] = append(doc["workstream_links"].([]any),
		map[string]any{"from": "ops", "to": "billing", "kind": "parent", "items": []any{"tracker:E-2"}})
	m := pageOf(t, doc).Map
	child, n := nodeAt(t, m, "tracker:E-2")
	if got := string(m.Nodes[n.Parent].Handle); got != "tracker:E-1" {
		t.Fatalf("E-2 hangs under %q, want its parent", got)
	}
	for _, l := range m.Links {
		if l.From == child && l.To == n.Parent {
			t.Errorf("a %s cross-link repeats the branch from E-2 to its parent", l.Kind)
		}
	}
}

// Where what an item hangs from is drawn under another workstream, the item
// hangs from the nearest node drawn here, and a cross-link keeps the edge it
// actually followed.
func TestAnEdgeSkippedOverAnItemDrawnElsewhereIsKept(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	from, _ := nodeAt(t, m, "code:owner/app:feature/rollout")
	to, _ := nodeAt(t, m, "hub:owner/app#20")
	for _, l := range m.Links {
		if l.From == from && l.To == to && l.Kind == "joined" {
			return
		}
	}
	t.Error("no joined cross-link from the rollout branch to the pull request it came through")
}

// An item drawn under one workstream still shows why each other holder holds
// it: the member or pull request it joined through there, or, where it follows
// nothing there, a shared link to that workstream.
func TestASharedItemShowsWhyEachOtherWorkstreamHoldsIt(t *testing.T) {
	doc := mapReadout(t)
	member(doc, 3, "B-1", "")
	m := pageOf(t, doc).Map
	b1, _ := nodeAt(t, m, "tracker:B-1")
	docs, _ := nodeAt(t, m, "docs")
	found := false
	for _, l := range m.Links {
		if l.From == b1 && l.To == docs && l.Kind == "shared" {
			found = true
		}
	}
	if !found {
		t.Error("a member held by docs with nothing to hang from there has no shared link to docs")
	}
}

// Where an item and what it joined through are both drawn under other
// workstreams, the link still names what it joined through, not the nearer
// node the tree would hang it from.
func TestASharedItemLinksToWhatItFollowsNotToANearerNode(t *testing.T) {
	doc := mapReadout(t)
	w := record(workstreams(doc)[3])
	w["items"] = append(w["items"].([]any), map[string]any{"input": "code", "item": map[string]any{
		"id": "owner/app:feature/rollout", "last_touched": "2030-06-04T12:00:00Z", "state": "open", "links": []any{}, "attrs": map[string]any{},
	}})
	m := pageOf(t, doc).Map
	rollout, n := nodeAt(t, m, "code:owner/app:feature/rollout")
	if got := string(m.Nodes[n.Parent].Handle); got != "docs" {
		t.Fatalf("rollout is drawn under %q, want docs, where it is a member", got)
	}
	pr, _ := nodeAt(t, m, "hub:owner/app#20")
	o1, _ := nodeAt(t, m, "tracker:O-1")
	var toPR, toO1 bool
	for _, l := range m.Links {
		toPR = toPR || (l.From == rollout && l.To == pr && l.Kind == "joined")
		toO1 = toO1 || (l.From == rollout && l.To == o1)
	}
	if !toPR || toO1 {
		t.Errorf("rollout links to the pull request it came through: %v; to O-1, which it did not follow: %v", toPR, toO1)
	}
}

// parentJoined adds a supplied tracker item to the workstream at index i,
// joined by parent to member, with parent as its own parent.
func parentJoined(doc map[string]any, i int, id, parent, member string) {
	w := record(workstreams(doc)[i])
	it := map[string]any{"id": id, "last_touched": "2030-06-10T12:00:00Z", "state": "To Do", "links": []any{}, "attrs": map[string]any{}, "parent": parent}
	w["items"] = append(w["items"].([]any), map[string]any{"input": "tracker", "item": it, "joined": map[string]any{"by": "parent", "member": member}})
}

// An item a parent joined hangs under its own parent where the workstream
// holds it, so a sub-task sits under its story rather than beside it under
// the epic the two joined.
func TestAParentJoinHangsUnderItsParent(t *testing.T) {
	doc := mapReadout(t)
	member(doc, 1, "E-1", "")
	parentJoined(doc, 1, "E-2", "E-1", "tracker:E-1")
	parentJoined(doc, 1, "E-3", "E-2", "tracker:E-1")
	parentJoined(doc, 1, "E-4", "E-404", "tracker:E-1")
	m := pageOf(t, doc).Map
	for handle, want := range map[string]string{"tracker:E-2": "tracker:E-1", "tracker:E-3": "tracker:E-2", "tracker:E-4": "tracker:E-1"} {
		_, n := nodeAt(t, m, handle)
		if got := string(m.Nodes[n.Parent].Handle); got != want {
			t.Errorf("%s hangs under %q, want %q", handle, got, want)
		}
	}
}

// held adds a supplied item to the workstream at index i, joined through
// member (and via, where it is not empty) when member is not empty.
func held(doc map[string]any, i int, input, id, member, via string) {
	w := record(workstreams(doc)[i])
	placed := map[string]any{"input": input, "item": map[string]any{
		"id": id, "last_touched": "2030-06-10T12:00:00Z", "state": "open", "links": []any{}, "attrs": map[string]any{},
	}}
	if member != "" {
		j := map[string]any{"by": "key", "member": member}
		if via != "" {
			j["by"], j["via"] = "pull_request", via
		}
		placed["joined"] = j
	}
	w["items"] = append(w["items"].([]any), placed)
}

// Every edge an item occurrence follows, in any workstream, reaches the map:
// as the branch it hangs by, or as a cross-link to the very node it follows,
// of its kind. The relations are derived here from the readout alone, over
// readouts that put each kind of occurrence in each kind of place: drawn here
// or under another workstream; following nothing, a node drawn here, one
// drawn elsewhere, or a member nothing read; by a pull request, a member or a
// parent; cut from a cycle; and a join whose parent its workstream holds.
func TestEveryEdgeAnOccurrenceFollowsIsDrawn(t *testing.T) {
	variants := map[string]func(map[string]any){
		"the fixture": func(map[string]any) {},
		"an item and what it joined through both drawn elsewhere": func(doc map[string]any) {
			held(doc, 3, "code", "owner/app:feature/rollout", "", "")
		},
		"a member held twice, with nothing to hang from in the second": func(doc map[string]any) {
			held(doc, 3, "tracker", "B-1", "", "")
		},
		"a member held twice, under its parent in both": func(doc map[string]any) {
			member(doc, 1, "E-1", "")
			member(doc, 1, "E-2", "E-1")
			member(doc, 2, "E-1", "")
			member(doc, 2, "E-2", "E-1")
		},
		"a chain a parent joined": func(doc map[string]any) {
			member(doc, 1, "E-1", "")
			parentJoined(doc, 1, "E-2", "E-1", "tracker:E-1")
			parentJoined(doc, 1, "E-3", "E-2", "tracker:E-1")
		},
		"a join through a member nothing read": func(doc map[string]any) {
			held(doc, 4, "hub", "owner/app#99", "tracker:I-1", "")
		},
		"a branch through a pull request drawn elsewhere, its member drawn here": func(doc map[string]any) {
			held(doc, 2, "code", "owner/app:feature/other", "tracker:O-1", "hub:owner/app#20")
		},
	}
	for name, vary := range variants {
		t.Run(name, func(t *testing.T) {
			doc := mapReadout(t)
			vary(doc)
			m := pageOf(t, doc).Map
			has := func(from, to int, kind string) bool {
				if m.Nodes[from].Parent == to {
					return true
				}
				return slices.ContainsFunc(m.Links, func(l crossLink) bool { return l.From == from && l.To == to && l.Kind == kind })
			}
			for _, w := range workstreams(doc) {
				ws := record(w)
				wsAt, _ := nodeAt(t, m, ws["name"].(string))
				holds := map[string]bool{}
				for _, it := range ws["items"].([]any) {
					p := record(it)
					holds[p["input"].(string)+":"+record(p["item"])["id"].(string)] = true
				}
				for _, u := range ws["members_unread"].([]any) {
					holds[u.(string)] = true
				}
				for _, it := range ws["items"].([]any) {
					p := record(it)
					item := record(p["item"])
					handle := p["input"].(string) + ":" + item["id"].(string)
					from, _ := nodeAt(t, m, handle)
					parent := ""
					if s, ok := item["parent"].(string); ok && p["input"].(string)+":"+s != handle && holds[p["input"].(string)+":"+s] {
						parent = p["input"].(string) + ":" + s
					}
					follows, kind := "", ""
					if j, ok := p["joined"].(map[string]any); ok {
						via, _ := j["via"].(string)
						switch {
						case j["by"] == "pull_request" && via != "" && holds[via]:
							follows, kind = via, "joined"
						case j["by"] == "parent" && parent != "":
							follows, kind = parent, "parent"
						case holds[j["member"].(string)]:
							follows, kind = j["member"].(string), "joined"
						}
						if parent != "" && parent != follows {
							to, _ := nodeAt(t, m, parent)
							if !has(from, to, "parent") {
								t.Errorf("%s in %s: its parent %s is neither its branch nor a cross-link", handle, ws["name"], parent)
							}
						}
					}
					if follows == "" && parent != "" {
						follows, kind = parent, "parent"
					}
					if follows == "" {
						if !has(from, wsAt, "shared") {
							t.Errorf("%s in %s: being held there is neither its branch nor a shared link", handle, ws["name"])
						}
						continue
					}
					to, _ := nodeAt(t, m, follows)
					if !has(from, to, kind) {
						t.Errorf("%s in %s: it follows %s (%s), which is neither its branch nor a cross-link", handle, ws["name"], follows, kind)
					}
				}
			}
		})
	}
}

// The same readout draws the same map: the order of its cross-links, which
// the page draws and lists, does not vary between renders.
func TestTheMapIsTheSameEveryTime(t *testing.T) {
	doc := mapReadout(t)
	member(doc, 3, "B-1", "")
	member(doc, 3, "P-9", "")
	member(doc, 4, "O-1", "")
	first, err := json.Marshal(pageOf(t, doc).Map)
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		again, err := json.Marshal(pageOf(t, doc).Map)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatal("the same readout drew a different map")
		}
	}
}

// Collapsed, a node still says what is flagged beneath it, so folding the map
// never hides that a workstream needs attention.
func TestARollupSaysWhatIsFlaggedBeneathIt(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	hub := m.Nodes[m.Hub]
	const want = "5 workstreams: 1 on a failing input, 2 behind their rank, 1 with nothing read"
	if hub.Rollup != want || hub.RollupTone != "fault" {
		t.Errorf("setting rollup %q tone %q, want %q and fault", hub.Rollup, hub.RollupTone, want)
	}
	_, docs := nodeAt(t, m, "docs")
	if docs.RollupTone != "" {
		t.Errorf("docs rollup tone %q, want none for a workstream with no cue", docs.RollupTone)
	}
}

// A screen reader user hears the label a sighted user sees, hidden
// characters and all.
func TestANodesNameHoldsItsLabel(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	for _, n := range m.Nodes {
		shown := n.Label
		if n.Sub != "" {
			shown += " " + n.Sub
		}
		if !strings.HasPrefix(n.Name, shown) {
			t.Errorf("%q: name %q does not open with what the node shows, %q", n.Handle, n.Name, shown)
		}
	}
}

func TestWorkstreamsCarryTheirCuesInWordsAndAsAMark(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	for _, tc := range []struct {
		handle string
		words  []string
	}{
		{"platform", []string{"draws on a failing input: tracker"}},
		{"billing", []string{"behind its rank"}},
		{"ops", []string{"stale", "behind its rank"}},
		{"docs", nil},
		{"intake", []string{"nothing read"}},
	} {
		_, n := nodeAt(t, m, tc.handle)
		if !slices.Equal(n.Words, tc.words) {
			t.Errorf("%s words = %q, want %q", tc.handle, n.Words, tc.words)
		}
		if (n.Flag != "") != (len(tc.words) > 0) {
			t.Errorf("%s flag = %q with words %q: a flagged workstream needs a mark, and only it", tc.handle, n.Flag, n.Words)
		}
		if !strings.Contains(n.Name, n.Label) || !strings.Contains(n.Name, strings.Join(tc.words, ", ")) {
			t.Errorf("%s name = %q, want its label and its cues in words", tc.handle, n.Name)
		}
	}
	_, platform := nodeAt(t, m, "platform")
	if platform.Label != "Platform rework" || platform.Sub != "platform" {
		t.Errorf("platform label %q sub %q, want its title with its id beside it", platform.Label, platform.Sub)
	}
}

// A cue's loudness follows what it asks of the reader: a failing input may
// make the numbers wrong, behind its rank is drift from the stated order, a
// workstream nothing was read for may be gone, and stale is only quiet, so it
// fades rather than calls out. On a setting where every workstream is stale,
// that keeps the rest standing out.
func TestEachCueHasItsOwnLoudnessAndABranchCarriesIt(t *testing.T) {
	for _, tc := range []struct {
		readout func(*testing.T) map[string]any
		handle  string
		tone    string
	}{
		{mapReadout, "platform", "fault"},
		{mapReadout, "tracker:P-3", "fault"},
		{mapReadout, "ops", "behind"},
		{mapReadout, "code:owner/app:feature/rollout", "behind"},
		{mapReadout, "intake", "unread"},
		{mapReadout, "tracker:I-1", "unread"},
		{mapReadout, "docs", ""},
		{mapReadout, "hub:owner/app#21", "unread"},
		{busy, "docs-site", "stale"},
		{busy, "code:owner/docs:draft/guides", "stale"},
		{busy, "onboarding", "fault"},
		{busy, "", ""},
	} {
		_, n := nodeAt(t, pageOf(t, tc.readout(t)).Map, tc.handle)
		if n.Tone != tc.tone {
			t.Errorf("%q tone = %q, want %q", tc.handle, n.Tone, tc.tone)
		}
	}
}

func TestEachNodeSaysHowMuchItHoldsAndWeighsItsLeaves(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	for _, tc := range []struct {
		handle string
		weight int
		rollup string
	}{
		{"tracker:P-2", 2, "3 items"},
		{"tracker:P-3", 1, ""},
		{"billing", 4, "5 items, 1 not read"},
		{"", 12, "5 workstreams: 1 on a failing input, 2 behind their rank, 1 with nothing read"},
	} {
		_, n := nodeAt(t, m, tc.handle)
		if n.Weight != tc.weight || n.Rollup != tc.rollup {
			t.Errorf("%q weight %d rollup %q, want %d and %q", tc.handle, n.Weight, n.Rollup, tc.weight, tc.rollup)
		}
	}
}

func TestAHandleIsCopiedExactlyAndShownSafely(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	_, n := nodeAt(t, m, "tracker:B-\u200b2")
	if !n.HiddenChars || n.Sub != `"B-\u200b2"` {
		t.Errorf("id %q hidden %v, want the id quoted and the handle flagged", n.Sub, n.HiddenChars)
	}
	_, plain := nodeAt(t, m, "tracker:B-1")
	if plain.HiddenChars || plain.Sub != "B-1" {
		t.Errorf("id %q hidden %v, want the plain id unflagged", plain.Sub, plain.HiddenChars)
	}
}

// An item is labelled on its branch by its title, as a hand-drawn map is by
// words, with the id commands take beside it; one without a title shows its
// id.
func TestAnItemIsLabelledByItsTitleWithItsIDBesideIt(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	for _, tc := range []struct{ handle, label, sub string }{
		{"tracker:P-2", "Split the scheduler", "P-2"},
		{"code:owner/app:feature/split", "owner/app:feature/split", ""},
		{"hub:owner/app#21", "owner/app#21", ""},
	} {
		_, n := nodeAt(t, m, tc.handle)
		if n.Label != tc.label || n.Sub != tc.sub {
			t.Errorf("%s label %q id %q, want %q and %q", tc.handle, n.Label, n.Sub, tc.label, tc.sub)
		}
	}
}

func TestTheReadIsNamedExactly(t *testing.T) {
	doc := mapReadout(t)
	p := pageOf(t, doc)
	if p.Read != "2030-06-15T12:00:00.123456789Z" {
		t.Errorf("read = %q, want the readout's generated_at as written", p.Read)
	}
	// The exact read rides on the as-of time, as its machine-readable value
	// and its tooltip, so the header shows one time while an agent reading
	// the page and a person hovering it can still match it to view.json.
	want := `<time class="asof" datetime="2030-06-15T12:00:00.123456789Z" title="read 2030-06-15T12:00:00.123456789Z, as in view.json beside this page">as of 2030-06-15 12:00 UTC</time>`
	if !strings.Contains(render(t, doc), want) {
		t.Errorf("the page does not carry the exact read on its as-of time, want %s", want)
	}
}

// Every string the readout carries reaches the map's data block; none of them
// may end it or open a comment there, whatever it holds.
func TestReadoutTextCannotEscapeTheDataBlock(t *testing.T) {
	const hostile = "</script><!--<script>"
	doc := mapReadout(t)
	doc["setting"] = "ex" + hostile
	w := record(workstreams(doc)[0])
	w["title"] = hostile
	placed := record(w["items"].([]any)[0])
	it := record(placed["item"])
	it["title"] = hostile
	it["state"] = hostile
	it["id"] = "P-1" + hostile
	joined := record(record(w["items"].([]any)[7])["joined"])
	joined["member"] = "tracker:P-2" + hostile
	record(workstreams(doc)[1])["members_unread"] = []any{"hub:owner/app#21" + hostile}
	links := doc["workstream_links"].([]any)
	record(links[len(links)-1])["kind"] = "blocks" + hostile
	out := render(t, doc)
	if strings.Contains(out, hostile) {
		t.Error("readout text reached the page able to end the data block")
	}
	if n := strings.Count(strings.ToLower(out), "</script"); n != 2 {
		t.Errorf("the page closes %d script elements, want the data block and the script", n)
	}
}

// scriptOf is the one executable script and the one data block the page holds.
func scriptOf(t *testing.T, out string) (script, data string) {
	t.Helper()
	const module, block = `<script type="module">`, `<script type="application/json" id="lookout-map">`
	if strings.Count(out, module) != 1 || strings.Count(out, block) != 1 || strings.Count(strings.ToLower(out), "<script") != 2 {
		t.Fatalf("want exactly one module script and one data block:\n%s", out)
	}
	cut := func(open string) string {
		rest := out[strings.Index(out, open)+len(open):]
		return rest[:strings.Index(rest, "</script>")]
	}
	return cut(module), cut(block)
}

func TestTheOneScriptIsPinnedByItsHash(t *testing.T) {
	out := render(t, busy(t))
	script, _ := scriptOf(t, out)
	if script != pageScript {
		t.Error("the page's script is not page.js byte for byte")
	}
	sum := sha256.Sum256([]byte(script))
	hash := base64.StdEncoding.EncodeToString(sum[:])
	csp := `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'sha256-` + hash + `'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'; require-trusted-types-for 'script'; trusted-types 'none'">`
	head := strings.TrimLeft(out[strings.Index(out, "<head>")+len("<head>"):], " \n\t")
	if !strings.HasPrefix(head, csp) {
		t.Errorf("the head does not open with the content security policy pinning the script:\nwant %s\n%.400s", csp, head)
	}
}

// sinks are the words by which a script writes markup or code from a string,
// navigates, or opens a connection. The content security policy does not stop
// navigation or WebRTC, and page-sinks.grit matches only the call shapes it
// names, so a word scan backs it: a call routed through a helper or a
// property, such as attrs(a, { href: … }), still has to name one of these.
var sinks = regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|srcdoc|DOMParser|createContextualFragment|setHTMLUnsafe|document\.write|\beval\b|\bFunction\(|\blocation\b|\.open\(|\.click\(|dispatchEvent|RTCPeerConnection|sendBeacon|WebSocket|EventSource|\bfetch\(|XMLHttpRequest|\bon[a-z]+["']?\s*:|["']?\b(?:href|src|action|formaction)["']?\s*:`)

func TestTheScriptReachesNoSinkNavigationOrConnection(t *testing.T) {
	for i, line := range strings.Split(pageScript, "\n") {
		if m := sinks.FindString(line); m != "" {
			t.Errorf("page.js:%d names %q, which the page's script may not use", i+1, m)
		}
	}
}

// The script is inlined as trusted code, so html/template does not stop it
// closing its own element or opening a comment that would swallow the rest.
func TestTheScriptCannotCloseItsElement(t *testing.T) {
	lower := strings.ToLower(pageScript)
	for _, bad := range []string{"</script", "<!--"} {
		if strings.Contains(lower, bad) {
			t.Errorf("page.js holds %s, which would break out of the element it is inlined into", bad)
		}
	}
}

// The browser specs read the view model through the script's JSDoc types,
// checked by tsc against this golden; regenerate it by hand from the spec's
// output when the model changes on purpose.
func TestTheViewModelMatchesItsGolden(t *testing.T) {
	m := pageOf(t, mapReadout(t)).Map
	got, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "map.viewmodel.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("view model differs from testdata/map.viewmodel.json:\n%s", got)
	}
}

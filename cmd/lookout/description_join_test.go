package main

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// described is a setting whose tickets are named in pull request descriptions
// as well as titles and branches, under the README's upper-case pattern.
const described = `
discovery_window: 72h
key_pattern: '([A-Z][A-Z0-9]+-[0-9]+)'
forge_owners: [alice]
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: code
    kind: git
    repos: ["~/code/*"]
  - name: hub
    kind: github
  - name: claude
    kind: claude_code
    config_dirs: ["~/.claude"]
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
  - name: onboarding
    members: ["tracker:XYZ-9"]
  - name: search
    members: ["tracker:QRS-4"]
`

// withDescribedInputs writes the tracker's file and lets the invocation find
// both git and the fake gh.
func (f *fixture) withDescribedInputs(nodes ...ghNode) *githubFake {
	f.t.Helper()
	fake := f.withKeyedInputs(nodes...)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil), item("XYZ-9", time.Hour, nil), item("QRS-4", time.Hour, nil)}, nil))
	return fake
}

// joins is each item the named workstream joined, as <input>:<id>, with its
// provenance; members are left out.
func joins(t *testing.T, doc readoutDoc, workstream string) map[string]joinedDoc {
	t.Helper()
	for _, w := range doc.Workstreams {
		if w.Name != workstream {
			continue
		}
		out := map[string]joinedDoc{}
		for _, it := range w.Items {
			token := it.Input + ":" + it.Item["id"].(string)
			if _, twice := out[token]; twice {
				t.Errorf("%s holds %s twice", workstream, token)
			}
			if it.Joined != nil {
				out[token] = *it.Joined
			}
		}
		return out
	}
	t.Fatalf("no workstream %q in %v", workstream, workstreamNames(doc))
	return nil
}

func equalJoins(a, b map[string]joinedDoc) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestADescriptionJoinsAPullRequestToEachAdoptedTicketItNames(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", described)
	f.withDescribedInputs(
		ghNode{pr: true, repo: "alice/app", number: 1, title: "Tidy the exporter", state: "OPEN", draft: true, branch: "tidy-exporter", body: "Part of ABC-1. Follow-up to ABC-122.", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 2, title: "Release notes", state: "OPEN", branch: "release", body: "Ships ABC-1, XYZ-9 and QRS-4.", created: 5 * time.Hour, updated: 10 * time.Minute, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 3, title: "ABC-1: the export", state: "OPEN", branch: "export", body: "Also covers ABC-1 and XYZ-9.", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 4, title: "Small fix", state: "OPEN", branch: "small-fix", body: "Follow-up to ABC-122.", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
	)

	doc := f.read()

	desc := func(member string) joinedDoc { return joinedDoc{By: "description", Member: member} }
	if got, want := joins(t, doc, "billing"), map[string]joinedDoc{
		"hub:alice/app#1": desc("tracker:ABC-1"),
		"hub:alice/app#2": desc("tracker:ABC-1"),
		"hub:alice/app#3": {By: "key", Member: "tracker:ABC-1"},
	}; !equalJoins(got, want) {
		t.Errorf("billing joins = %v, want the draft and the release notes joined by their descriptions, and #3 once, by its title's key", got)
	}
	if got, want := joins(t, doc, "onboarding"), map[string]joinedDoc{
		"hub:alice/app#2": desc("tracker:XYZ-9"),
		"hub:alice/app#3": desc("tracker:XYZ-9"),
	}; !equalJoins(got, want) {
		t.Errorf("onboarding joins = %v, want every pull request whose description names XYZ-9", got)
	}
	if got, want := joins(t, doc, "search"), map[string]joinedDoc{"hub:alice/app#2": desc("tracker:QRS-4")}; !equalJoins(got, want) {
		t.Errorf("search joins = %v, want the release notes", got)
	}
	for _, w := range doc.Workstreams {
		touched := slices.ContainsFunc(w.Events, func(e eventDoc) bool {
			return e.Input == "hub" && e.ID == "alice/app#2" && e.Joined != nil && e.Joined.By == "description"
		})
		if !touched {
			t.Errorf("%s events = %+v, want the release notes counted as a touch in every workstream it names", w.Name, w.Events)
		}
		if at, err := time.Parse(time.RFC3339, *w.LastTouched); err != nil || !at.Equal(now.Add(-10*time.Minute)) {
			t.Errorf("%s last_touched = %s, want the release notes' update, the newest touch it holds", w.Name, *w.LastTouched)
		}
	}
	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"hub:alice/app#4"}) {
		t.Errorf("unplaced = %v, want the pull request naming only a ticket no workstream adopted", ids)
	}
}

func TestABranchAndItsSessionsFollowTheirOpenPullRequest(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", described+`  - name: review
    members: ["hub:alice/app#9"]
`)
	old := 30 * 24 * time.Hour
	fake := f.withDescribedInputs(
		ghNode{pr: true, repo: "alice/app", number: 5, title: "Export polish", state: "OPEN", draft: true, branch: "export-polish", body: "ABC-1", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 6, title: "Old fix", state: "MERGED", branch: "patch-1", body: "ABC-1", created: old, updated: old},
		ghNode{pr: true, repo: "alice/app", number: 7, title: "From a fork", state: "OPEN", branch: "fork-work", headRepo: "bob/app", body: "ABC-1", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 8, title: "Set aside", state: "OPEN", branch: "set-aside", body: "ABC-1", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 9, title: "Review me", state: "OPEN", branch: "review-me", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
	)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	lib := f.repo("code/lib", "git@github.com:alice/lib.git")
	for _, b := range []string{"export-polish", "patch-1", "fork-work", "set-aside", "review-me"} {
		f.branch(app, b, time.Hour)
	}
	f.branch(lib, "export-polish", time.Hour)
	f.transcript(f.home(".claude"), app, sessionA, 2*time.Hour, turn(sessionA, app, "export-polish", 2*time.Hour))
	f.mustRun("dismiss", "code:alice/app:set-aside")

	doc := f.read()

	through := joinedDoc{By: "pull_request", Member: "tracker:ABC-1", Via: "hub:alice/app#5"}
	if got := joins(t, doc, "billing"); got["code:alice/app:export-polish"] != through || got["claude:alice/app:export-polish"] != through {
		t.Errorf("billing joins = %v, want the branch and its session joined through #5", got)
	}
	if got, want := joins(t, doc, "review"), map[string]joinedDoc{
		"code:alice/app:review-me": {By: "pull_request", Member: "hub:alice/app#9", Via: "hub:alice/app#9"},
	}; !equalJoins(got, want) {
		t.Errorf("review joins = %v, want the adopted pull request's branch following it", got)
	}
	unplaced := unplacedIDs(doc)
	slices.Sort(unplaced)
	if want := []string{"code:alice/app:fork-work", "code:alice/app:patch-1", "code:alice/lib:export-polish"}; !slices.Equal(unplaced, want) {
		t.Errorf("unplaced = %v, want %v: a merged pull request's reused branch name, a fork's head and another repository's branch of the same name join nothing", unplaced, want)
	}
	if doc.DismissedCount != 1 {
		t.Errorf("dismissed_count = %d, want the dismissed branch kept out though its pull request joined", doc.DismissedCount)
	}
	// The contract requires via wherever by is pull_request, so a renderer can
	// always name the pull request a branch came through.
	_, stdout, _ := f.run("read", "--json")
	via := regexp.MustCompile(`,\s*"via":\s*"hub:alice/app#5"`)
	if !via.MatchString(stdout) {
		t.Fatalf("readout = %s, want via on the joins this check strips", stdout)
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(via.ReplaceAllString(stdout, "")))
	if err != nil {
		t.Fatal(err)
	}
	sch, err := schema.Compile("readout.v1")
	if err != nil {
		t.Fatal(err)
	}
	if sch.Validate(inst) == nil {
		t.Error("a pull_request join without via validates against readout.v1, want it refused")
	}
	lookups := 0
	for _, r := range fake.requests {
		if _, search := r.variables["q"]; search {
			continue
		}
		lookups++
		if strings.Contains(r.query, "body") || !strings.Contains(r.query, "headRepository") {
			t.Errorf("lookup query %q, want the head repository and no description, which never joins a member", r.query)
		}
	}
	if lookups == 0 {
		t.Error("no member lookup sent")
	}
}

func TestAKeySearchReadsDescriptionsButNeverAdmitsOnABranch(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", described)
	old := 30 * 24 * time.Hour
	fake := f.withDescribedInputs(
		ghNode{pr: true, repo: "alice/app", number: 20, title: "Ship it", state: "MERGED", branch: "misc", body: "Closes ABC-1", created: old, updated: old, association: "MEMBER"},
		ghNode{pr: true, repo: "alice/app", number: 21, title: "abc-1 in lower case", state: "OPEN", branch: "ABC-1-x", created: old, updated: old, association: "MEMBER"},
		ghNode{pr: true, repo: "alice/app", number: 22, title: "Refactor", state: "OPEN", branch: "XYZ-9-cleanup", body: "Part of ABC-1", created: old, updated: old, association: "MEMBER"},
		ghNode{pr: true, repo: "alice/app", number: 23, title: "Split", state: "OPEN", branch: "ABC-1-split", body: "see abc 1", anyKey: true, created: old, updated: old, association: "MEMBER"},
		ghNode{pr: true, repo: "alice/app", number: 24, title: "Cleanup", state: "OPEN", branch: "XYZ-9-mine", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true},
	)

	doc := f.read()

	if got, want := joins(t, doc, "billing"), map[string]joinedDoc{
		"hub:alice/app#20": {By: "description", Member: "tracker:ABC-1"},
		"hub:alice/app#22": {By: "description", Member: "tracker:ABC-1"},
	}; !equalJoins(got, want) {
		t.Errorf("billing joins = %v, want the colleagues' pull requests whose descriptions name ABC-1, and none whose key only its branch carries", got)
	}
	if got, want := joins(t, doc, "onboarding"), map[string]joinedDoc{"hub:alice/app#24": {By: "key", Member: "tracker:XYZ-9"}}; !equalJoins(got, want) {
		t.Errorf("onboarding joins = %v, want your own open pull request joined by its branch, and not #22, whose branch alone names XYZ-9", got)
	}
	if ids := unplacedIDs(doc); len(ids) != 0 {
		t.Errorf("unplaced = %v, want no key-search hit admitted on its branch", ids)
	}
	keySearches := 0
	for _, r := range fake.requests {
		q, search := r.variables["q"].(string)
		switch {
		case !search:
		case strings.Contains(q, "is:issue"):
			if strings.Contains(r.query, "body") || !strings.Contains(r.query, "first: 100") {
				t.Errorf("issue search query %q, want no descriptions and full pages", r.query)
			}
		case strings.Contains(q, `"`):
			keySearches++
			if !strings.Contains(q, "in:title,body") {
				t.Errorf("key search %q, want it to look in titles and descriptions", q)
			}
			fallthrough
		default:
			if !strings.Contains(r.query, "body") || !strings.Contains(r.query, "first: 50") {
				t.Errorf("pull request search query %q, want descriptions selected in pages of 50", r.query)
			}
		}
	}
	if keySearches == 0 {
		t.Error("no key search sent")
	}
}

// TestWithoutAKeyPatternNothingFollowsAPullRequest pins the gate that keeps a
// setting with no pattern on the joins, and the discovery digest, it had: an
// adopted pull request's branch stays unplaced, and no search pays for
// descriptions nothing will read.
func TestWithoutAKeyPatternNothingFollowsAPullRequest(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(described, "key_pattern: '([A-Z][A-Z0-9]+-[0-9]+)'\n", "", 1)+`  - name: review
    members: ["hub:alice/app#9"]
`)
	fake := f.withDescribedInputs(ghNode{pr: true, repo: "alice/app", number: 9, title: "Review me", state: "OPEN", branch: "review-me", body: "ABC-1", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true})
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "review-me", time.Hour)

	doc := f.read()

	if got := joins(t, doc, "review"); len(got) != 0 {
		t.Errorf("review joins = %v, want none with no key_pattern", got)
	}
	if ids := unplacedIDs(doc); !slices.Equal(ids, []string{"code:alice/app:review-me"}) {
		t.Errorf("unplaced = %v, want the branch", ids)
	}
	for _, r := range fake.requests {
		if _, search := r.variables["q"]; search && (strings.Contains(r.query, "body") || !strings.Contains(r.query, "first: 100")) {
			t.Errorf("search query %q, want no descriptions and full pages with no key_pattern", r.query)
		}
	}
}

// TestSnapshotFlagsAnUpgradeThatChangesTheJoinRules pins what an upgrade that
// places more work does to the baseline: work it newly reaches, moved within
// the discovery window, arrives as on any change to discovery, and the record
// says discovery changed, so a reader can discount it. The previous record's
// digest stands in for the one the earlier rules gave.
func TestSnapshotFlagsAnUpgradeThatChangesTheJoinRules(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", described)
	fake := f.withDescribedInputs()
	f.snapshot()

	path := f.baselinePath("personal")
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, path))), &rec); err != nil {
		t.Fatal(err)
	}
	rec["discovery_digest"] = "the earlier rules' digest"
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	f.nextDay()
	fake.nodes = append(fake.nodes, ghNode{pr: true, repo: "alice/app", number: 30, title: "Exporter cleanup", state: "OPEN", branch: "cleanup", body: "Part of ABC-1", created: 5 * time.Hour, updated: 3 * time.Hour, association: "MEMBER"})
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", -23*time.Hour, nil), item("XYZ-9", -23*time.Hour, nil), item("QRS-4", -23*time.Hour, nil)}, nil))
	b := f.snapshot()
	if !b.DiscoveryChanged || !slices.Equal(b.Arrivals, []string{"hub:alice/app#30"}) {
		t.Errorf("discovery_changed = %v, arrivals = %v, want the newly reached pull request arriving on a record flagged as a change to discovery", b.DiscoveryChanged, b.Arrivals)
	}
}

func TestSnapshotListsAnItemInSeveralWorkstreamsOnce(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", described)
	f.withDescribedInputs(ghNode{pr: true, repo: "alice/app", number: 1, title: "Both", state: "OPEN", branch: "both", body: "ABC-1 and XYZ-9", created: 5 * time.Hour, updated: 3 * time.Hour, authored: true})
	if b := f.snapshot(); !slices.Equal(b.Joined, []string{"hub:alice/app#1"}) {
		t.Errorf("joined = %v, want the pull request listed once though two workstreams hold it", b.Joined)
	}
}

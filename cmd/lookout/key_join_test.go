package main

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// joinedDoc is the provenance a joined item carries.
type joinedDoc struct {
	By     string `json:"by"`
	Member string `json:"member"`
}

// keyed is a setting whose tracker tickets' keys, written in branch names,
// pull request titles and session branches, place that work.
const keyed = `
discovery_window: 72h
key_pattern: '([A-Za-z]+-[0-9]+)'
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
`

// withKeyedInputs writes the tracker's file and lets the invocation find both
// git and the fake gh.
func (f *fixture) withKeyedInputs(nodes ...ghNode) *githubFake {
	f.t.Helper()
	f.withGit()
	fake := f.withGitHub(nodes...)
	f.env["PATH"] = filepath.Join(f.env["HOME"], "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil), item("XYZ-9", time.Hour, nil)}, nil))
	return fake
}

// placed is each item of the named workstream as <input>:<id>, with the
// member the join matched, or "" for a member.
func placed(t *testing.T, doc readoutDoc, workstream string) map[string]string {
	t.Helper()
	for _, w := range doc.Workstreams {
		if w.Name != workstream {
			continue
		}
		out := map[string]string{}
		for _, it := range w.Items {
			via := ""
			if it.Joined != nil {
				if it.Joined.By != "key" {
					t.Errorf("%s: joined.by = %q, want key", it.Item["id"], it.Joined.By)
				}
				via = it.Joined.Member
			}
			out[it.Input+":"+it.Item["id"].(string)] = via
		}
		return out
	}
	t.Fatalf("no workstream %q in %v", workstream, workstreamNames(doc))
	return nil
}

func equalMaps(a, b map[string]string) bool {
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

func TestAKeyJoinsBranchesPullRequestsAndSessionsToTheTicketsWorkstream(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", keyed)
	f.withKeyedInputs(
		ghNode{pr: true, repo: "alice/app", number: 1, title: "ABC-1 and XYZ-9: the export", state: "OPEN", branch: "misc", created: 5 * time.Hour, updated: 2 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 2, title: "Tidy the docs", state: "OPEN", branch: "XYZ-9-docs", created: 5 * time.Hour, updated: 2 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 3, title: "QQQ-5 something else", state: "OPEN", branch: "other", created: 5 * time.Hour, updated: 2 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 4, title: "No key at all", state: "OPEN", branch: "plain", created: 5 * time.Hour, updated: 2 * time.Hour, authored: true},
	)
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.branch(app, "feature/abc-1-export", time.Hour)
	f.branch(app, "feature/qqq-5-x", time.Hour)
	f.branch(app, "chore/tidy", time.Hour)
	f.transcript(f.home(".claude"), app, sessionA, 2*time.Hour, turn(sessionA, app, "ABC-1-session", 2*time.Hour))

	doc := f.read()

	want := map[string]string{
		"tracker:ABC-1":                       "",
		"code:alice/app:feature/abc-1-export": "tracker:ABC-1",
		"hub:alice/app#1":                     "tracker:ABC-1",
		"claude:alice/app:ABC-1-session":      "tracker:ABC-1",
	}
	if got := placed(t, doc, "billing"); !equalMaps(got, want) {
		t.Errorf("billing = %v, want its member and the branch, pull request and session carrying its key, each marked as joined by it", got)
	}
	if got := placed(t, doc, "onboarding"); !equalMaps(got, map[string]string{"tracker:XYZ-9": "", "hub:alice/app#2": "tracker:XYZ-9"}) {
		t.Errorf("onboarding = %v, want the pull request whose branch alone carries XYZ-9, and not #1, whose title names ABC-1 first", got)
	}
	billing := doc.Workstreams[0]
	var order []string
	for _, it := range billing.Items {
		order = append(order, it.Input+":"+it.Item["id"].(string))
	}
	if !slices.Equal(order, []string{"tracker:ABC-1", "code:alice/app:feature/abc-1-export", "hub:alice/app#1", "claude:alice/app:ABC-1-session"}) {
		t.Errorf("billing items = %v, want the members, then joined items in input order", order)
	}
	for _, e := range billing.Events {
		joined := e.Joined != nil && e.Joined.By == "key" && e.Joined.Member == "tracker:ABC-1"
		if joined != (e.Input != "tracker") {
			t.Errorf("event %s:%s joined = %+v, want provenance on joined items' events alone", e.Input, e.ID, e.Joined)
		}
	}
	if len(billing.Events) != 4 {
		t.Errorf("billing events = %+v, want one per item, joined ones included", billing.Events)
	}
	unplaced := unplacedIDs(doc)
	slices.Sort(unplaced)
	if !slices.Equal(unplaced, []string{"code:alice/app:chore/tidy", "code:alice/app:feature/qqq-5-x", "hub:alice/app#3", "hub:alice/app#4"}) {
		t.Errorf("unplaced = %v, want what carries no member's key and nothing that joined", unplaced)
	}
}

func TestAKeySearchFetchesTrustedPullRequestsAtAnyAgeOrState(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
discovery_window: 72h
key_pattern: '([A-Za-z]+-[0-9]+)'
forge_owners: [alice]
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: hub
    kind: github
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
`)
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, nil))
	old := 30 * 24 * time.Hour
	fake := f.withGitHub(
		ghNode{pr: true, repo: "alice/app", number: 10, title: "ABC-1: ship the export", state: "MERGED", created: old, updated: old, association: "MEMBER"},
		ghNode{pr: true, repo: "alice/app", number: 11, title: "[abc-1] follow-up", state: "OPEN", created: old, updated: 10 * 24 * time.Hour, association: "COLLABORATOR"},
		ghNode{pr: true, repo: "alice/app", number: 12, title: "ABC-12 another ticket", state: "OPEN", created: old, updated: old},
		ghNode{pr: true, repo: "alice/app", number: 13, title: "ABC-1 drive-by", state: "OPEN", created: old, updated: old, association: "CONTRIBUTOR"},
		ghNode{pr: true, repo: "alice/app", number: 14, title: "ABC-1 from a stranger", state: "OPEN", created: old, updated: old, association: "NONE"},
	)

	doc := f.read()

	want := map[string]string{"tracker:ABC-1": "", "hub:alice/app#10": "tracker:ABC-1", "hub:alice/app#11": "tracker:ABC-1"}
	if got := placed(t, doc, "billing"); !equalMaps(got, want) {
		t.Errorf("billing = %v, want the merged and the colleague's pull request, both older than the window, and none whose key differs or whose author the repository does not trust", got)
	}
	if ids := unplacedIDs(doc); len(ids) != 0 {
		t.Errorf("unplaced = %v, want nothing a key search found that did not join", ids)
	}
	var keySearches []string
	for _, q := range fake.searches() {
		if strings.Contains(q, "in:title") {
			keySearches = append(keySearches, q)
		}
	}
	if len(keySearches) == 0 {
		t.Fatalf("searches = %v, want one by the key in titles", fake.searches())
	}
	for _, q := range keySearches {
		terms := strings.Fields(q)
		if !slices.Contains(terms, `"ABC-1"`) || !slices.Contains(terms, "is:pr") || !slices.Contains(terms, "user:alice") {
			t.Errorf("key search %q, want the quoted key, pull requests, and the owner", q)
		}
		if strings.Contains(q, "author:") || strings.Contains(q, "is:open") || strings.Contains(q, "updated:") {
			t.Errorf("key search %q, want no author, state or date limit", q)
		}
	}
}

func TestAKeySearchAsksForSeveralKeysAtOnce(t *testing.T) {
	keys := []string{"ABC-1", "ABC-2", "ABC-3", "ABC-4", "ABC-5", "ABC-6", "ABC-7"}
	members := make([]string, 0, len(keys))
	items := make([]any, 0, len(keys))
	nodes := make([]ghNode, 0, len(keys))
	for i, k := range keys {
		members = append(members, `"tracker:`+k+`"`)
		items = append(items, item(k, time.Hour, nil))
		nodes = append(nodes, ghNode{pr: true, repo: "alice/app", number: 20 + i, title: k + ": a change", state: "MERGED", created: 30 * 24 * time.Hour, updated: 30 * 24 * time.Hour, association: "MEMBER"})
	}
	f := newFixture(t)
	f.write("personal.yml", `
key_pattern: '([A-Z]+-[0-9]+)'
forge_owners: [alice]
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: hub
    kind: github
workstreams:
  - name: billing
    members: [`+strings.Join(members, ", ")+`]
`)
	f.supplied("tracker.json", suppliedDoc("tracker", items, nil))
	fake := f.withGitHub(nodes...)

	doc := f.read()

	if got := placed(t, doc, "billing"); len(got) != 2*len(keys) {
		t.Errorf("billing = %v, want every key's pull request joined", got)
	}
	var keySearches []string
	for _, q := range fake.searches() {
		if strings.Contains(q, "in:title") {
			keySearches = append(keySearches, q)
		}
	}
	if len(keySearches) != 2 {
		t.Errorf("key searches = %q, want the seven keys asked for in two", keySearches)
	}
}

func TestNothingJoinsWithoutAKeyPattern(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(keyed, "key_pattern: '([A-Za-z]+-[0-9]+)'\n", "", 1))
	fake := f.withKeyedInputs(
		ghNode{pr: true, repo: "alice/app", number: 1, title: "ABC-1: the export", state: "OPEN", created: 5 * time.Hour, updated: 2 * time.Hour, authored: true},
	)
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feature/ABC-1-export", time.Hour)

	doc := f.read()

	if got := placed(t, doc, "billing"); !equalMaps(got, map[string]string{"tracker:ABC-1": ""}) {
		t.Errorf("billing = %v, want its member alone", got)
	}
	if ids := unplacedIDs(doc); len(ids) != 2 {
		t.Errorf("unplaced = %v, want the branch and the pull request", ids)
	}
	for _, q := range fake.searches() {
		if strings.Contains(q, "in:title") {
			t.Errorf("search %q sent with no pattern configured", q)
		}
	}
}

func TestAReadThatJoinsWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", keyed)
	f.withKeyedInputs(ghNode{pr: true, repo: "alice/app", number: 1, title: "ABC-1: the export", state: "OPEN", created: 5 * time.Hour, updated: 2 * time.Hour, authored: true})
	f.branch(f.repo("code/app", "git@github.com:alice/app.git"), "feature/ABC-1-export", time.Hour)
	f.mustRun("dismiss", "code:alice/app:unrelated")
	log := filepath.Join(f.stateDir("personal"), "gestures.jsonl")
	config, before := f.config("personal.yml"), readFile(t, log)

	doc := f.read()
	if len(placed(t, doc, "billing")) != 3 {
		t.Fatalf("billing = %v, want the joins this spec relies on", placed(t, doc, "billing"))
	}
	f.mustRun("show")

	if got := f.config("personal.yml"); got != config {
		t.Errorf("configuration = %q, want it unchanged by a read", got)
	}
	if got := readFile(t, log); got != before {
		t.Errorf("gesture log = %q, want it unchanged by a read", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAKeyNeverMovesAnItemAWorkstreamHolds(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", `
discovery_window: 72h
key_pattern: '([A-Za-z]+-[0-9]+)'
forge_owners: [alice]
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: ops
    kind: supplied
    supplied: ops.json
    max_age: 24h
  - name: code
    kind: git
    repos: ["~/code/*"]
workstreams:
  - name: billing
    members:
      - tracker:ABC-1
      - code:alice/app:feature/ABC-1-removed
  - name: review
    members:
      - code:alice/app:feature/ABC-1-reviewed
  - name: ops-work
    members:
      - ops:ABC-1
  - name: done
    completed: 2030-06-01
    members:
      - code:alice/app:feature/ABC-1-old
      - code:alice/app:feature/ABC-1-again
`)
	f.withGit()
	f.supplied("tracker.json", suppliedDoc("tracker", []any{item("ABC-1", time.Hour, nil)}, nil))
	f.supplied("ops.json", suppliedDoc("ops", []any{item("ABC-1", time.Hour, nil)}, nil))
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.branch(app, "feature/ABC-1-reviewed", time.Hour)
	f.branch(app, "feature/ABC-1-old", 20*24*time.Hour)
	f.branch(app, "feature/ABC-1-again", time.Hour)
	f.branch(app, "feature/ABC-1-dismissed", 2*time.Hour)
	f.branch(app, "feature/ABC-1-removed", time.Hour)
	f.mustRun("dismiss", "code:alice/app:feature/ABC-1-dismissed")
	f.mustRun("remove", "code:alice/app:feature/ABC-1-removed")

	doc := f.read()

	want := map[string]string{
		"tracker:ABC-1":                        "",
		"code:alice/app:feature/ABC-1-again":   "tracker:ABC-1",
		"code:alice/app:feature/ABC-1-removed": "tracker:ABC-1",
	}
	if got := placed(t, doc, "billing"); !equalMaps(got, want) {
		t.Errorf("billing = %v, want the completed workstream's member touched since and the removed member joined, and nothing another workstream holds or a dismissal keeps out", got)
	}
	if got := placed(t, doc, "review"); !equalMaps(got, map[string]string{"code:alice/app:feature/ABC-1-reviewed": ""}) {
		t.Errorf("review = %v, want its member kept there", got)
	}
	if got := placed(t, doc, "ops-work"); !equalMaps(got, map[string]string{"ops:ABC-1": ""}) {
		t.Errorf("ops-work = %v, want no joins: billing names the key first", got)
	}
	if ids := unplacedIDs(doc); len(ids) != 0 {
		t.Errorf("unplaced = %v, want nothing", ids)
	}
	if doc.DismissedCount != 1 {
		t.Errorf("dismissed_count = %d, want the dismissed keyed branch counted", doc.DismissedCount)
	}

	f.branch(app, "feature/ABC-1-dismissed", -30*time.Second)
	f.clock = f.clock.Add(time.Minute)
	if got := placed(t, f.read(), "billing"); got["code:alice/app:feature/ABC-1-dismissed"] != "tracker:ABC-1" {
		t.Errorf("billing = %v, want the dismissed branch joined once touched again", got)
	}
}

func TestAKeyJoinsAtAnyAgeInAnyCaseAndMarksWhatItDrawsOn(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", keyed)
	fake := f.withKeyedInputs()
	fake.respond = func(w http.ResponseWriter, _ ghRequest) bool {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return true
	}
	app := f.repo("code/app", "git@github.com:alice/app.git")
	f.branch(app, "feature/abc-1-old", 40*24*time.Hour)

	doc := f.read()

	if got := placed(t, doc, "billing"); got["code:alice/app:feature/abc-1-old"] != "tracker:ABC-1" {
		t.Errorf("billing = %v, want the branch older than the discovery window, keyed in lower case, joined", got)
	}
	for _, w := range doc.Workstreams {
		if !slices.Contains(w.InputsIncomplete, "hub") {
			t.Errorf("%s inputs_incomplete = %v, want hub: work it would join may be missing", w.Name, w.InputsIncomplete)
		}
	}
}

func TestAKeyPatternMustCaptureOneNonEmptyKey(t *testing.T) {
	for name, pattern := range map[string]string{
		"does not compile":         `'([A-Z]+-[0-9]+'`,
		"no capture group":         `'[A-Z]+-[0-9]+'`,
		"two capture groups":       `'([A-Z]+)-([0-9]+)'`,
		"matches the empty string": `'([A-Z]*)'`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", strings.Replace(oneInput, "event_window: 336h\n", "event_window: 336h\nkey_pattern: "+pattern+"\n", 1))
			code, stdout, stderr := f.run("read", "--json")
			if code == 0 || stdout != "" || !strings.Contains(stderr, "key_pattern") || strings.Contains(stderr, "not found in type") {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q, want a refusal naming key_pattern", code, stdout, stderr)
			}
		})
	}
}

func TestAGestureEditsASettingWithAKeyPattern(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", strings.Replace(handWritten, "event_window: 336h\n", "event_window: 336h\nkey_pattern: '([A-Z]+-[0-9]+)'\n", 1))
	f.mustRun("adopt", "tracker:ABC-3", "billing")
	if !strings.Contains(f.config("personal.yml"), "tracker:ABC-3") {
		t.Errorf("configuration = %q, want the adopted member", f.config("personal.yml"))
	}
}

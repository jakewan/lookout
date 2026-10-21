package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureToken is what the fake gh prints when GH_TOKEN is not set.
const fixtureToken = "fixture-token-not-a-secret"

// ghNode is one pull request or issue the fake GitHub holds.
type ghNode struct {
	pr       bool
	repo     string // as GitHub spells it, owner/name
	number   int
	title    string
	state    string // OPEN, CLOSED or MERGED
	draft    bool
	created  time.Duration
	updated  time.Duration
	branch   string
	authored bool
	assigned bool
	// formerly is a name a lookup by number still finds the node under, as
	// for a repository renamed or transferred since.
	formerly string
	// forbidden makes a lookup of it fail as an organization refusing the
	// token does.
	forbidden bool
}

// ghRequest is one request the fake received.
type ghRequest struct {
	auth      string
	query     string
	variables map[string]any
}

// githubFake answers lookout's GraphQL queries from a fixed set of nodes and
// records every request, so a spec can assert on what was never asked.
type githubFake struct {
	t     *testing.T
	mu    sync.Mutex
	nodes []ghNode
	// page is how many nodes a search page holds; 0 means 100.
	page int
	// issueCount, when set, is the count a search reports in place of the
	// matches.
	issueCount int
	// stuckCursor makes every search page claim another page under the same
	// cursor.
	stuckCursor bool
	// ignoreSince returns search matches whatever their updated date.
	ignoreSince bool
	// respond, when set, answers a request in place of the dataset; it
	// returns false to let the dataset answer.
	respond  func(w http.ResponseWriter, r ghRequest) bool
	requests []ghRequest
}

// withGitHub points the invocation at a fake GitHub, and puts a fake gh that
// prints fixtureToken alone on its PATH.
func (f *fixture) withGitHub(nodes ...ghNode) *githubFake {
	f.t.Helper()
	fake := &githubFake{t: f.t, nodes: nodes}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	f.t.Cleanup(srv.Close)
	f.api = srv.URL + "/graphql"
	f.gh(`printf '%s\n' "${GH_TOKEN:-` + fixtureToken + `}"`)
	return fake
}

// gh replaces the fake gh's body, run after it checks its arguments. The
// environment the script sees is written to gh.env beside it.
func (f *fixture) gh(body string) {
	f.t.Helper()
	bin := filepath.Join(f.env["HOME"], "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		f.t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"/usr/bin/env > '" + filepath.Join(bin, "gh.env") + "'\n" +
		`[ "$*" = "auth token --hostname github.com" ] || { echo "unexpected arguments: $*" >&2; exit 2; }` + "\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.env["PATH"] = bin
}

// ghEnv is the environment the fake gh last ran with.
func (f *fixture) ghEnv() []string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.env["HOME"], "bin", "gh.env"))
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (g *githubFake) serve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
		g.t.Errorf("request %s %s, want POST /graphql", r.Method, r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		g.t.Errorf("request body: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req := ghRequest{auth: r.Header.Get("Authorization"), query: body.Query, variables: body.Variables}
	g.mu.Lock()
	g.requests = append(g.requests, req)
	g.mu.Unlock()
	if g.respond != nil && g.respond(w, req) {
		return
	}
	if req.auth != "bearer "+fixtureToken {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if q, ok := req.variables["q"].(string); ok {
		g.search(w, q, req.variables["after"])
		return
	}
	g.members(w, req.variables)
}

func (g *githubFake) search(w http.ResponseWriter, q string, after any) {
	var matches []ghNode
	for _, n := range g.nodes {
		if g.matches(n, q) {
			matches = append(matches, n)
		}
	}
	page := g.page
	if page == 0 {
		page = 100
	}
	start := 0
	// A cursor the fake did not hand out, such as stuckCursor's, starts over.
	if c, ok := after.(string); ok {
		if n, err := strconv.Atoi(c); err == nil {
			start = n
		}
	}
	end := min(start+page, len(matches))
	nodes := make([]any, 0, end-start)
	for _, n := range matches[start:end] {
		nodes = append(nodes, n.json())
	}
	count := len(matches)
	if g.issueCount != 0 {
		count = g.issueCount
	}
	cursor, more := strconv.Itoa(end), end < len(matches)
	if g.stuckCursor {
		cursor, more = "stuck", true
	}
	writeJSON(g.t, w, map[string]any{"data": map[string]any{
		"search": map[string]any{
			"issueCount": count,
			"pageInfo":   map[string]any{"hasNextPage": more, "endCursor": cursor},
			"nodes":      nodes,
		},
		"rateLimit": rateLimit(),
	}})
}

// matches applies the search qualifiers lookout sends, so a query that lost
// one would find nodes it should not.
func (g *githubFake) matches(n ghNode, q string) bool {
	for _, term := range strings.Fields(q) {
		key, value, _ := strings.Cut(term, ":")
		switch key {
		case "is":
			switch value {
			case "pr":
				if !n.pr {
					return false
				}
			case "issue":
				if n.pr {
					return false
				}
			case "open":
				if n.state != "OPEN" {
					return false
				}
			default:
				g.t.Errorf("search %q: unexpected is:%s", q, value)
			}
		case "author":
			if value != "@me" || !n.authored {
				return false
			}
		case "assignee":
			if value != "@me" || !n.assigned {
				return false
			}
		case "user":
			owner, _, _ := strings.Cut(n.repo, "/")
			if !strings.EqualFold(owner, value) {
				return false
			}
		case "updated":
			since, err := time.Parse(time.RFC3339, strings.TrimPrefix(value, ">="))
			if err != nil {
				g.t.Errorf("search %q: updated:%s does not parse: %v", q, value, err)
			}
			if !g.ignoreSince && now.Add(-n.updated).Before(since) {
				return false
			}
		default:
			g.t.Errorf("search %q: unexpected term %q", q, term)
		}
	}
	return true
}

// members answers a lookup whose variables are o<i>, n<i> and k<i> for each
// alias m<i>.
func (g *githubFake) members(w http.ResponseWriter, vars map[string]any) {
	data := map[string]any{"rateLimit": rateLimit()}
	var errs []any
	for i := 0; ; i++ {
		owner, ok := vars["o"+strconv.Itoa(i)].(string)
		if !ok {
			break
		}
		name, _ := vars["n"+strconv.Itoa(i)].(string)
		number, _ := vars["k"+strconv.Itoa(i)].(float64)
		alias := "m" + strconv.Itoa(i)
		var found map[string]any
		forbidden := false
		for _, n := range g.nodes {
			asked := owner + "/" + name
			if (strings.EqualFold(n.repo, asked) || strings.EqualFold(n.formerly, asked)) && n.number == int(number) {
				found, forbidden = n.json(), n.forbidden
			}
		}
		if forbidden {
			data[alias] = nil
			errs = append(errs, map[string]any{
				"type":    "FORBIDDEN",
				"path":    []any{alias},
				"message": "Resource protected by organization SAML enforcement.",
			})
			continue
		}
		if found == nil {
			data[alias] = map[string]any{"issueOrPullRequest": nil}
			errs = append(errs, map[string]any{
				"type":    "NOT_FOUND",
				"path":    []any{alias, "issueOrPullRequest"},
				"message": fmt.Sprintf("Could not resolve to an issue or pull request with the number of %d.", int(number)),
			})
			continue
		}
		data[alias] = map[string]any{"issueOrPullRequest": found}
	}
	doc := map[string]any{"data": data}
	if errs != nil {
		doc["errors"] = errs
	}
	writeJSON(g.t, w, doc)
}

func (n ghNode) json() map[string]any {
	owner, _, _ := strings.Cut(n.repo, "/")
	kind, path := "Issue", "issues"
	if n.pr {
		kind, path = "PullRequest", "pull"
	}
	doc := map[string]any{
		"__typename": kind,
		"number":     n.number,
		"title":      n.title,
		"url":        fmt.Sprintf("https://github.com/%s/%s/%d", n.repo, path, n.number),
		"state":      n.state,
		"createdAt":  stamp(n.created),
		"updatedAt":  stamp(n.updated),
		"repository": map[string]any{"nameWithOwner": n.repo, "owner": map[string]any{"login": owner}},
	}
	if n.pr {
		doc["isDraft"] = n.draft
		doc["headRefName"] = n.branch
	}
	return doc
}

func rateLimit() map[string]any {
	return map[string]any{"remaining": 4999, "resetAt": stamp(-time.Hour)}
}

func writeJSON(t *testing.T, w http.ResponseWriter, doc any) {
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		t.Errorf("writing response: %v", err)
	}
}

// searches is every search string the fake was sent.
func (g *githubFake) searches() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var qs []string
	for _, r := range g.requests {
		if q, ok := r.variables["q"].(string); ok {
			qs = append(qs, q)
		}
	}
	return qs
}

// asked reports whether any request's variables mention s.
func (g *githubFake) asked(s string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.requests {
		data, err := json.Marshal(r.variables)
		if err != nil {
			g.t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(s)) {
			return true
		}
	}
	return false
}

const githubOnly = `
forge_owners: [alice, gitlab.example/carol]
inputs:
  - name: hub
    kind: github
workstreams: []
`

// githubMembers is githubOnly with one workstream naming members.
func githubMembers(members ...string) string {
	quoted := make([]string, 0, len(members))
	for _, m := range members {
		quoted = append(quoted, strconv.Quote("hub:"+m))
	}
	return strings.Replace(githubOnly, "workstreams: []", "workstreams:\n  - name: review\n    members: ["+strings.Join(quoted, ", ")+"]", 1)
}

func unplacedItem(t *testing.T, doc readoutDoc, token string) map[string]any {
	t.Helper()
	for _, u := range doc.Unplaced {
		if u.Input+":"+u.Item["id"].(string) == token {
			return u.Item
		}
	}
	t.Fatalf("unplaced = %v, want %s", unplacedIDs(doc), token)
	return nil
}

func TestReadSurfacesAuthoredPullRequests(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub(ghNode{
		pr: true, repo: "Alice/App", number: 7, title: "Add search", state: "OPEN",
		created: 72 * time.Hour, updated: time.Hour, branch: "feature/search", authored: true,
	})

	doc := f.read()
	got := unplacedItem(t, doc, "hub:alice/app#7")
	want := map[string]any{
		"id":           "alice/app#7",
		"title":        "Add search",
		"last_touched": stamp(time.Hour),
		"started_at":   stamp(72 * time.Hour),
		"state":        "open",
		"links":        []any{map[string]any{"rel": "html", "href": "https://github.com/Alice/App/pull/7"}},
		"attrs":        map[string]any{"type": "pull_request", "repo": "Alice/App", "number": float64(7), "branch": "feature/search"},
	}
	gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, want)
	if gotJSON != wantJSON {
		t.Errorf("item = %s\nwant   %s", gotJSON, wantJSON)
	}
	h := doc.health(t, "hub")
	if h.Status != "ok" || h.RecordCount != 1 || h.FetchedAt == nil || *h.FetchedAt != now.Format(time.RFC3339) {
		t.Errorf("health = %+v, want ok with one record fetched now", h)
	}
	for _, r := range fake.requests {
		if r.auth != "bearer "+fixtureToken {
			t.Errorf("Authorization = %q, want the token gh printed", r.auth)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReadSurfacesAssignedIssuesAndNamesEachState(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub(
		ghNode{repo: "alice/app", number: 3, title: "Crash on start", state: "OPEN", updated: time.Hour, assigned: true},
		ghNode{repo: "alice/app", number: 4, title: "Not mine", state: "OPEN", updated: time.Hour},
		ghNode{pr: true, repo: "alice/app", number: 5, title: "Draft", state: "OPEN", draft: true, updated: time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 6, title: "Someone else's", state: "OPEN", updated: time.Hour},
	)

	doc := f.read()
	if got, want := unplacedIDs(doc), []string{"hub:alice/app#3", "hub:alice/app#5"}; !slices.Equal(got, want) {
		t.Fatalf("unplaced = %v, want %v", got, want)
	}
	issue := unplacedItem(t, doc, "hub:alice/app#3")
	if issue["state"] != "open" || issue["attrs"].(map[string]any)["type"] != "issue" {
		t.Errorf("issue = %v, want an open issue", issue)
	}
	if _, ok := issue["attrs"].(map[string]any)["branch"]; ok {
		t.Errorf("issue attrs = %v, want no branch", issue["attrs"])
	}
	if draft := unplacedItem(t, doc, "hub:alice/app#5"); draft["state"] != "draft" {
		t.Errorf("state = %v, want draft", draft["state"])
	}
}

func TestReadKeepsGitHubWorkOutsideTheOwnerFence(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub(
		ghNode{pr: true, repo: "alice/app", number: 1, state: "OPEN", updated: time.Hour, authored: true},
		ghNode{pr: true, repo: "carol/tool", number: 2, state: "OPEN", updated: time.Hour, authored: true},
	)
	// A repository transferred out of alice's account still answers her
	// search, under its new owner.
	fake.respond = func(w http.ResponseWriter, r ghRequest) bool {
		q, ok := r.variables["q"].(string)
		if !ok || !strings.Contains(q, "is:pr") {
			return false
		}
		moved := ghNode{pr: true, repo: "mallory/loot", number: 9, title: "mallory", state: "OPEN", updated: time.Hour, authored: true}
		writeJSON(t, w, map[string]any{"data": map[string]any{
			"search": map[string]any{
				"issueCount": 2,
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": "2"},
				"nodes":      []any{fake.nodes[0].json(), moved.json()},
			},
			"rateLimit": rateLimit(),
		}})
		return true
	}

	code, stdout, stderr := f.run("read", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(strings.ToLower(stdout), "mallory") {
		t.Errorf("stdout names a repository the fence keeps out:\n%s", stdout)
	}
	var doc readoutDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if got, want := unplacedIDs(doc), []string{"hub:alice/app#1"}; !slices.Equal(got, want) {
		t.Errorf("unplaced = %v, want %v", got, want)
	}
	if h := doc.health(t, "hub"); h.ExcludedCount == nil || *h.ExcludedCount != 1 || h.Status != "ok" {
		t.Errorf("health = %+v, want ok with one excluded", h)
	}
	for _, q := range fake.searches() {
		if strings.Contains(q, "carol") || !strings.Contains(q, "user:alice") {
			t.Errorf("search %q, want it limited to alice, the only github.com owner", q)
		}
	}
}

func TestReadReadsGitHubMembersAtAnyAgeAndState(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubMembers("alice/app#40", "mallory/loot#1", "Alice/App#41", "alice/app#99", "alice/app"))
	fake := f.withGitHub(
		ghNode{pr: true, repo: "alice/app", number: 40, title: "Old work", state: "MERGED", created: 900 * time.Hour, updated: 800 * time.Hour},
		ghNode{repo: "alice/app", number: 41, title: "Old issue", state: "CLOSED", updated: 800 * time.Hour},
		ghNode{pr: true, repo: "mallory/loot", number: 1, state: "OPEN", updated: time.Hour},
	)

	doc := f.read()
	ws := doc.Workstreams[0]
	if len(ws.Items) != 1 || ws.Items[0].Item["id"] != "alice/app#40" || ws.Items[0].Item["state"] != "merged" {
		t.Errorf("items = %+v, want the merged pull request", ws.Items)
	}
	wantUnread := []string{"hub:mallory/loot#1", "hub:Alice/App#41", "hub:alice/app#99", "hub:alice/app"}
	if !slices.Equal(ws.MembersUnread, wantUnread) {
		t.Errorf("members_unread = %v, want %v", ws.MembersUnread, wantUnread)
	}
	if fake.asked("mallory") {
		t.Errorf("a member outside the owner fence was requested")
	}
	h := doc.health(t, "hub")
	if h.Status != "ok" {
		t.Errorf("status = %q, want ok: an unread member is the workstream's to show", h.Status)
	}
	for _, want := range []string{
		"hub:mallory/loot#1: its owner is not in forge_owners",
		"hub:Alice/App#41: write it as hub:alice/app#41",
		"hub:alice/app#99: not found, or the token cannot see it",
		"hub:alice/app: not a pull request or issue id, such as owner/repo#12",
	} {
		if !slices.Contains(h.Details, want) {
			t.Errorf("details = %q, want %q", h.Details, want)
		}
	}
	if len(ws.InputsIncomplete) != 0 {
		t.Errorf("inputs_incomplete = %v, want none", ws.InputsIncomplete)
	}
}

func TestReadDiscoversOnlyRecentGitHubWork(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", "discovery_window: 48h\n"+githubOnly)
	fake := f.withGitHub(
		ghNode{pr: true, repo: "alice/app", number: 1, state: "OPEN", updated: time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/app", number: 2, state: "OPEN", updated: 72 * time.Hour, authored: true},
	)
	fake.ignoreSince = true

	doc := f.read()
	if got, want := unplacedIDs(doc), []string{"hub:alice/app#1"}; !slices.Equal(got, want) {
		t.Errorf("unplaced = %v, want %v", got, want)
	}
	since := "updated:>=" + now.Add(-48*time.Hour).Format(time.RFC3339)
	for _, q := range fake.searches() {
		if !strings.Contains(q, since) {
			t.Errorf("search %q, want it to carry %s", q, since)
		}
	}
}

func TestReadFollowsSearchPages(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub(
		ghNode{pr: true, repo: "alice/a", number: 1, state: "OPEN", updated: time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/a", number: 2, state: "OPEN", updated: 2 * time.Hour, authored: true},
		ghNode{pr: true, repo: "alice/a", number: 3, state: "OPEN", updated: 3 * time.Hour, authored: true},
	)
	fake.page = 2

	doc := f.read()
	if got := len(doc.Unplaced); got != 3 {
		t.Errorf("unplaced = %v, want all three across pages", unplacedIDs(doc))
	}
	if h := doc.health(t, "hub"); h.Status != "ok" {
		t.Errorf("health = %+v, want ok", h)
	}
}

func TestReadGitHubHealth(t *testing.T) {
	authored := ghNode{pr: true, repo: "alice/app", number: 1, state: "OPEN", updated: time.Hour, authored: true}
	limited := func(w http.ResponseWriter, header map[string]string, status int, body string) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}
	// onSearch answers searches with fn and leaves member lookups to the
	// dataset.
	onSearch := func(fn func(w http.ResponseWriter)) func(http.ResponseWriter, ghRequest) bool {
		return func(w http.ResponseWriter, r ghRequest) bool {
			if _, ok := r.variables["q"]; !ok {
				return false
			}
			fn(w)
			return true
		}
	}
	always := func(fn func(w http.ResponseWriter)) func(http.ResponseWriter, ghRequest) bool {
		return func(w http.ResponseWriter, _ ghRequest) bool { fn(w); return true }
	}
	cases := []struct {
		name    string
		config  string
		gh      string // the fake gh's body, when not the default
		setup   func(g *githubFake)
		status  string
		reasons []string
		detail  string
		items   int
		fetched bool
	}{
		{
			name: "gh not logged in", gh: `echo "not logged in to any hosts" >&2; exit 1`,
			status: "error", reasons: []string{"auth_failed"}, detail: "gh: not logged in to github.com: run gh auth login --hostname github.com",
		},
		{
			name: "gh prints nothing", gh: `exit 0`,
			status: "error", reasons: []string{"auth_failed"}, detail: "gh: printed no token for github.com",
		},
		{
			name: "token refused",
			setup: func(g *githubFake) {
				g.respond = always(func(w http.ResponseWriter) { limited(w, nil, http.StatusUnauthorized, `{"message":"Bad credentials"}`) })
			},
			status: "error", reasons: []string{"auth_failed"}, detail: "github.com refused the token",
		},
		{
			name: "rate limited by status",
			setup: func(g *githubFake) {
				g.respond = always(func(w http.ResponseWriter) {
					limited(w, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}, http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
				})
			},
			status: "error", reasons: []string{"rate_limited"}, detail: "rate limited until " + now.Add(time.Hour).Format(time.RFC3339),
		},
		{
			name: "secondary rate limit",
			setup: func(g *githubFake) {
				g.respond = always(func(w http.ResponseWriter) {
					limited(w, map[string]string{"Retry-After": "60"}, http.StatusTooManyRequests, `{"message":"You have exceeded a secondary rate limit"}`)
				})
			},
			status: "error", reasons: []string{"rate_limited"}, detail: "rate limited",
		},
		{
			name:   "search rate limited after members read",
			config: githubMembers("alice/app#1"),
			setup: func(g *githubFake) {
				g.respond = onSearch(func(w http.ResponseWriter) {
					writeJSON(t, w, map[string]any{"data": nil, "errors": []any{map[string]any{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}}})
				})
			},
			status: "degraded", reasons: []string{"rate_limited"}, detail: "rate limited", items: 1, fetched: true,
		},
		{
			name: "server error",
			setup: func(g *githubFake) {
				g.respond = always(func(w http.ResponseWriter) { limited(w, nil, http.StatusBadGateway, "upstream") })
			},
			status: "error", reasons: []string{"github_failed"}, detail: "github.com answered 502",
		},
		{
			name: "malformed response",
			setup: func(g *githubFake) {
				g.respond = always(func(w http.ResponseWriter) { limited(w, nil, http.StatusOK, "{") })
			},
			status: "error", reasons: []string{"github_failed"}, detail: "github.com sent a response lookout could not read",
		},
		{
			name: "more matches than search returns",
			setup: func(g *githubFake) {
				g.issueCount = 1500
			},
			status: "degraded", reasons: []string{"truncated"}, detail: "1500 matches", items: 1, fetched: true,
		},
		{
			name: "cursor that does not advance",
			setup: func(g *githubFake) {
				g.stuckCursor = true
			},
			status: "degraded", reasons: []string{"truncated"}, detail: "repeated a page", items: 1, fetched: true,
		},
		{
			name: "touched ahead of the clock",
			setup: func(g *githubFake) {
				g.nodes = append(g.nodes, ghNode{pr: true, repo: "alice/app", number: 2, state: "OPEN", updated: -time.Hour, authored: true})
			},
			status: "degraded", reasons: []string{"clock_skew"}, detail: "alice/app#2", items: 2, fetched: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			config := tc.config
			if config == "" {
				config = githubOnly
			}
			f.write("personal.yml", config)
			fake := f.withGitHub(authored)
			if tc.gh != "" {
				f.gh(tc.gh)
			}
			if tc.setup != nil {
				tc.setup(fake)
			}

			code, stdout, stderr := f.run("read", "--json")
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr)
			}
			if strings.Contains(stdout+stderr, fixtureToken) {
				t.Errorf("the token reached the output")
			}
			validate(t, "readout.v1", stdout)
			var doc readoutDoc
			if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
				t.Fatal(err)
			}
			h := doc.health(t, "hub")
			if h.Status != tc.status || !slices.Equal(h.Reasons, tc.reasons) {
				t.Errorf("health = %s %v, want %s %v (details %q)", h.Status, h.Reasons, tc.status, tc.reasons, h.Details)
			}
			if !slices.ContainsFunc(h.Details, func(d string) bool { return strings.Contains(d, tc.detail) }) {
				t.Errorf("details = %q, want one saying %q", h.Details, tc.detail)
			}
			if h.RecordCount != tc.items {
				t.Errorf("record_count = %d, want %d", h.RecordCount, tc.items)
			}
			if (h.FetchedAt != nil) != tc.fetched {
				t.Errorf("fetched_at = %v, want set: %v", h.FetchedAt, tc.fetched)
			}
		})
	}
}

func TestReadReportsGHMissingFromPath(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub()
	f.env["PATH"] = t.TempDir()

	h := f.read().health(t, "hub")
	if h.Status != "error" || !slices.Equal(h.Reasons, []string{"auth_failed"}) || !slices.Contains(h.Details, "gh: not found on PATH") {
		t.Errorf("health = %+v, want error naming the missing gh", h)
	}
	if len(fake.requests) != 0 {
		t.Errorf("GitHub was asked %d times without a token", len(fake.requests))
	}
}

func TestReadGivesGHOnlyTheVariablesItNeeds(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub(ghNode{pr: true, repo: "alice/app", number: 1, state: "OPEN", updated: time.Hour, authored: true})
	f.env["GH_TOKEN"] = fixtureToken
	f.gh(`printf '%s\n' "$GH_TOKEN"`)
	t.Setenv("GH_ENTERPRISE_TOKEN", "decoy")
	f.env["GH_HOST"] = "decoy.example"

	if got := len(f.read().Unplaced); got != 1 {
		t.Fatalf("unplaced = %d, want the pull request read with GH_TOKEN", got)
	}
	for _, kv := range f.ghEnv() {
		if strings.Contains(kv, "decoy") {
			t.Errorf("gh saw %q, which lookout does not pass on", kv)
		}
	}
}

func TestUnplacedGitHubTokensAreAdoptable(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub(ghNode{pr: true, repo: "alice/app", number: 7, title: "Add search", state: "OPEN", updated: time.Hour, authored: true})

	code, stdout, stderr := f.run("unplaced")
	if code != 0 || !strings.HasPrefix(stdout, "hub:alice/app#7\tAdd search\t") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	token, _, _ := strings.Cut(stdout, "\t")
	if code, _, stderr := f.run("adopt", "--new", token, "search"); code != 0 {
		t.Fatalf("adopt exit code = %d, stderr = %q", code, stderr)
	}
	// A # inside a plain scalar starts no comment, so the token needs no
	// quoting to survive a reload; the read below is what proves it does.
	if !strings.Contains(f.config("personal.yml"), "hub:alice/app#7") {
		t.Errorf("configuration = %q, want the token", f.config("personal.yml"))
	}
	doc := f.read()
	if len(doc.Unplaced) != 0 || len(doc.Workstreams) != 1 || len(doc.Workstreams[0].Items) != 1 {
		t.Errorf("readout = %+v, want the pull request in the new workstream", doc)
	}
}

func TestReadDoesNotExpectRecordsOfAGitHubInputItsMembersName(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubMembers("alice/app#99"))
	f.withGitHub()

	if h := f.read().health(t, "hub"); h.Status != "ok" {
		t.Errorf("health = %+v, want ok: membership does not imply records for github", h)
	}
}

func TestReadKeepsTheOtherMembersWhenOneIsRefused(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubMembers("alice/app#1", "alice/secret#2", "alice/app#99999999999"))
	f.withGitHub(
		ghNode{pr: true, repo: "alice/app", number: 1, state: "OPEN", updated: time.Hour},
		ghNode{pr: true, repo: "alice/secret", number: 2, state: "OPEN", updated: time.Hour, forbidden: true},
	)

	doc := f.read()
	ws := doc.Workstreams[0]
	if len(ws.Items) != 1 || ws.Items[0].Item["id"] != "alice/app#1" {
		t.Errorf("items = %+v, want the member GitHub answered for", ws.Items)
	}
	if want := []string{"hub:alice/secret#2", "hub:alice/app#99999999999"}; !slices.Equal(ws.MembersUnread, want) {
		t.Errorf("members_unread = %v, want %v", ws.MembersUnread, want)
	}
	h := doc.health(t, "hub")
	if h.Status != "degraded" || !slices.Equal(h.Reasons, []string{"github_failed"}) {
		t.Errorf("health = %s %v, want degraded github_failed", h.Status, h.Reasons)
	}
	for _, want := range []string{
		"hub:alice/secret#2: github.com reported FORBIDDEN",
		"hub:alice/app#99999999999: not a pull request or issue id, such as owner/repo#12",
	} {
		if !slices.Contains(h.Details, want) {
			t.Errorf("details = %q, want %q", h.Details, want)
		}
	}
}

func TestReadNamesAMemberWhoseRepositoryMoved(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubMembers("alice/old#3", "alice/gone#4"))
	f.withGitHub(
		ghNode{pr: true, repo: "alice/new", formerly: "alice/old", number: 3, state: "MERGED", updated: 900 * time.Hour},
		ghNode{pr: true, repo: "mallory/loot", formerly: "alice/gone", number: 4, state: "OPEN", updated: time.Hour},
	)

	code, stdout, stderr := f.run("read", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(strings.ToLower(stdout), "mallory") {
		t.Errorf("stdout names a repository the fence keeps out:\n%s", stdout)
	}
	var doc readoutDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Unplaced) != 0 {
		t.Errorf("unplaced = %v, want none: a moved member is not discovered work", unplacedIDs(doc))
	}
	if want := []string{"hub:alice/old#3", "hub:alice/gone#4"}; !slices.Equal(doc.Workstreams[0].MembersUnread, want) {
		t.Errorf("members_unread = %v, want %v", doc.Workstreams[0].MembersUnread, want)
	}
	h := doc.health(t, "hub")
	for _, want := range []string{
		"hub:alice/old#3: now at hub:alice/new#3",
		"hub:alice/gone#4: moved to an owner outside forge_owners",
	} {
		if !slices.Contains(h.Details, want) {
			t.Errorf("details = %q, want %q", h.Details, want)
		}
	}
	if h.ExcludedCount == nil || *h.ExcludedCount != 1 {
		t.Errorf("excluded_count = %v, want 1", h.ExcludedCount)
	}
}

func TestReadStopsAtASecondaryRateLimitNamedOnlyInTheBody(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub()
	fake.respond = func(w http.ResponseWriter, _ ghRequest) bool {
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`)); err != nil {
			t.Error(err)
		}
		return true
	}

	h := f.read().health(t, "hub")
	if h.Status != "error" || !slices.Equal(h.Reasons, []string{"rate_limited"}) {
		t.Errorf("health = %s %v, want error rate_limited", h.Status, h.Reasons)
	}
	if n := len(fake.requests); n != 1 {
		t.Errorf("requests = %d, want lookout to stop at the first limited one", n)
	}
}

func TestReadSaysWhenAGraphQLRateLimitEnds(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub()
	reset := now.Add(30 * time.Minute)
	fake.respond = func(w http.ResponseWriter, _ ghRequest) bool {
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		writeJSON(t, w, map[string]any{"data": nil, "errors": []any{map[string]any{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}}})
		return true
	}

	h := f.read().health(t, "hub")
	if want := "github.com: rate limited until " + reset.Format(time.RFC3339); !slices.Contains(h.Details, want) {
		t.Errorf("details = %q, want %q", h.Details, want)
	}
}

func TestReadNeverFollowsARedirectWithTheToken(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	elsewhere := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
	}))
	t.Cleanup(other.Close)
	fake := f.withGitHub()
	fake.respond = func(w http.ResponseWriter, _ ghRequest) bool {
		w.Header().Set("Location", other.URL+"/graphql")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return true
	}

	h := f.read().health(t, "hub")
	if elsewhere != 0 {
		t.Errorf("the redirect target was asked %d times", elsewhere)
	}
	if h.Status != "error" || !slices.Equal(h.Reasons, []string{"github_failed"}) || !slices.Contains(h.Details, "github.com answered 307") {
		t.Errorf("health = %+v, want error github_failed naming the redirect", h)
	}
}

func TestReadTellsAGHThatCannotRunFromOneNotLoggedIn(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub()
	// An executable file that is no program the kernel can start.
	if err := os.WriteFile(filepath.Join(f.env["PATH"], "gh"), []byte{0x7f, 0x00, 0x01}, 0o755); err != nil {
		t.Fatal(err)
	}

	h := f.read().health(t, "hub")
	if !slices.Contains(h.Details, "gh: could not run") {
		t.Errorf("details = %q, want gh: could not run", h.Details)
	}
}

func TestReadNamesGHTokenWhenGitHubRefusesIt(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub()
	f.env["GH_TOKEN"] = "refused-token-not-a-secret"

	h := f.read().health(t, "hub")
	if !slices.Contains(h.Details, "github.com refused the token in GH_TOKEN") {
		t.Errorf("details = %q, want the refusal to name GH_TOKEN", h.Details)
	}
}

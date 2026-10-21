// Package github reads pull requests and issues on github.com as one input's
// items, asking only about owners a setting's owner fence lists and admitting
// only what those owners hold.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/execpath"
	"github.com/jakewan/lookout/internal/health"
	"github.com/jakewan/lookout/internal/readout"
)

const (
	// host is the only forge this package reads. The token gh gives for it is
	// sent nowhere else.
	host = "github.com"
	// searchCap is the most results GitHub's search returns for one query
	// (GitHub's search API documentation); a query matching more is read
	// short.
	searchCap = 1000
	// pageSize is the most nodes one GraphQL connection page may hold.
	pageSize = 100
	// memberChunk bounds how many members one lookup asks for, keeping each
	// query well inside GitHub's per-query node limit.
	memberChunk = 50
	ghTimeout   = 10 * time.Second
	httpTimeout = 30 * time.Second
	// maxResponse bounds what lookout reads of one response.
	maxResponse = 16 << 20
)

// Reader reads GitHub for one invocation. The token is asked of gh once, when
// the first github input is read.
type Reader struct {
	getenv     func(string) string
	gh         string
	unsearched int
	api        string
	client     *http.Client

	asked    bool
	token    string
	tokenErr string
}

// NewReader finds gh on the invocation's PATH and sends queries to api.
func NewReader(getenv func(string) string, api string) *Reader {
	r := &Reader{getenv: getenv, api: api, client: &http.Client{Timeout: httpTimeout}}
	r.gh, r.unsearched = execpath.Find(getenv, "gh")
	return r
}

// failure is one request that did not give an answer, as a health reason and
// the details line that says why.
type failure struct {
	reason string
	detail string
}

// stops reports a failure every later request would meet too.
func (f *failure) stops() bool {
	return f.reason == health.AuthFailed || f.reason == health.RateLimited
}

// Read reports in's pull requests and issues: each member asked for by
// number, at any age and in any state, and the open work discovered within
// the owner fence. A member outside the fence is never asked for, and a node
// the fence does not admit is counted, never named.
func (r *Reader) Read(ctx context.Context, in config.Input, sc readout.Scope, now time.Time) readout.Reading {
	out := readout.Reading{Health: health.New(in.Name), Items: make([]readout.Item, 0)}
	detail := func(line string) {
		if !slices.Contains(out.Health.Details, line) {
			out.Health.Details = append(out.Health.Details, line)
		}
	}
	owners := make([]string, 0, len(sc.Owners))
	for _, o := range sc.Owners {
		if o.Host == host {
			owners = append(owners, o.Name)
		}
	}

	var wanted []ref
	members := make([]string, 0, len(sc.Members))
	for id := range sc.Members {
		members = append(members, id)
	}
	slices.Sort(members)
	for _, id := range members {
		token := in.Name + ":" + id
		m, ok := parseID(id)
		switch {
		case !ok:
			detail(token + ": not a pull request or issue id, such as owner/repo#12")
		case m.id() != id:
			detail(token + ": write it as " + in.Name + ":" + m.id())
		case !slices.Contains(owners, m.owner):
			detail(token + ": its owner is not in forge_owners")
		default:
			wanted = append(wanted, m)
		}
	}

	token, problem := r.credential(ctx)
	if problem != "" {
		out.Health.Lower(health.Error, health.AuthFailed)
		detail(problem)
		return out
	}

	found := map[string]node{}
	fenced := map[string]bool{}
	var failures []failure
	readAny := false
	admit := func(n node) {
		id := n.id()
		owner := strings.ToLower(n.Repository.Owner.Login)
		if !slices.Contains(owners, owner) {
			fenced[id] = true
			return
		}
		found[id] = n
	}

	for chunk := range slices.Chunk(wanted, memberChunk) {
		nodes, missing, f := r.lookup(ctx, token, chunk)
		if f != nil {
			failures = append(failures, *f)
			if f.stops() {
				break
			}
			continue
		}
		readAny = true
		for _, n := range nodes {
			admit(n)
		}
		for _, m := range missing {
			detail(in.Name + ":" + m.id() + ": not found, or the token cannot see it")
		}
	}

	searches := make([]string, 0, 2*len(owners))
	since := sc.Since.UTC().Format(time.RFC3339)
	for _, o := range owners {
		searches = append(searches,
			"is:pr is:open author:@me user:"+o+" updated:>="+since,
			"is:issue is:open assignee:@me user:"+o+" updated:>="+since)
	}
	stopped := slices.ContainsFunc(failures, func(f failure) bool { return f.stops() })
	for _, q := range searches {
		if stopped {
			break
		}
		nodes, short, f := r.search(ctx, token, q)
		if f != nil {
			failures = append(failures, *f)
			stopped = f.stops()
			continue
		}
		readAny = true
		if short != "" {
			out.Health.Lower(health.Degraded, health.Truncated)
			detail(short)
		}
		for _, n := range nodes {
			if n.updated().Before(sc.Since) {
				continue
			}
			admit(n)
		}
	}

	// A request that failed makes what was read incomplete; when nothing
	// could be read, the input as a whole cannot be.
	status := health.Degraded
	if !readAny {
		status = health.Error
	}
	for _, f := range failures {
		out.Health.Lower(status, f.reason)
		detail(f.detail)
	}
	if readAny {
		at := now.UTC()
		out.Health.FetchedAt = &at
	}
	for id := range fenced {
		if _, ok := found[id]; !ok {
			out.Health.ExcludedCount++
		}
	}

	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	horizon := now.Add(health.ClockTolerance)
	for _, id := range ids {
		n := found[id]
		item, ok := n.item()
		if !ok {
			out.Health.Lower(health.Degraded, health.GitHubFailed)
			detail(id + ": github.com sent it without the fields lookout reads")
			continue
		}
		if item.LastTouched.After(horizon) {
			out.Health.Lower(health.Degraded, health.ClockSkew)
			detail(id + ": its last update is dated ahead of this machine's clock")
		}
		out.Items = append(out.Items, item)
	}
	out.Health.RecordCount = len(out.Items)
	if in.ExpectRecords && readAny && len(found) == 0 {
		out.Health.Lower(health.Degraded, health.Empty)
	}
	return out
}

// credential asks gh for the github.com token, once per invocation. gh's own
// output is never repeated: its messages can carry account detail.
func (r *Reader) credential(ctx context.Context) (string, string) {
	if r.asked {
		return r.token, r.tokenErr
	}
	r.asked = true
	r.token, r.tokenErr = r.askGH(ctx)
	return r.token, r.tokenErr
}

func (r *Reader) askGH(ctx context.Context) (string, string) {
	const login = "run gh auth login --hostname " + host
	if r.gh == "" {
		switch r.unsearched {
		case 0:
			return "", "gh: not found on PATH"
		case 1:
			return "", "gh: not found on PATH, and 1 directory on it could not be searched"
		default:
			return "", fmt.Sprintf("gh: not found on PATH, and %d directories on it could not be searched", r.unsearched)
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	// --hostname keeps gh from answering for GH_HOST or the only host it is
	// logged in to, whose token would then be sent to github.com.
	cmd := exec.CommandContext(runCtx, r.gh, "auth", "token", "--hostname", host)
	// The environment is built rather than inherited, so only what gh needs
	// to find its own login reaches it: its configuration, the Linux keyring
	// it stores tokens in, and the token variables it honors in place of a
	// login.
	for _, key := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "GH_CONFIG_DIR", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := r.getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	if cmd.Env == nil {
		// A nil Env inherits this process's environment.
		cmd.Env = []string{}
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return "", "gh: interrupted"
	case runCtx.Err() != nil:
		return "", "gh: gave no token within " + ghTimeout.String() + ": check gh auth status --hostname " + host
	case err != nil:
		return "", "gh: not logged in to " + host + ": " + login
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", "gh: printed no token for " + host + ": " + login
	}
	return token, ""
}

// ref is a pull request or issue named by owner, repository and number, all
// as an id spells them.
type ref struct {
	owner, repo string
	number      int
}

func (m ref) id() string { return m.owner + "/" + m.repo + "#" + strconv.Itoa(m.number) }

// parseID reads owner/repo#N. Owner and repository are lowercased, so a caller
// can tell an id written in another case by comparing it with ref.id.
func parseID(id string) (ref, bool) {
	repo, num, ok := strings.Cut(id, "#")
	if !ok {
		return ref{}, false
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.ContainsAny(name, "/ ") || strings.Contains(owner, " ") {
		return ref{}, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 || strconv.Itoa(n) != num {
		return ref{}, false
	}
	return ref{owner: strings.ToLower(owner), repo: strings.ToLower(name), number: n}, true
}

// nodeFields selects what an item is built from, for a pull request or an
// issue alike.
const nodeFields = `__typename
... on PullRequest { number title url state isDraft createdAt updatedAt headRefName repository { nameWithOwner owner { login } } }
... on Issue { number title url state createdAt updatedAt repository { nameWithOwner owner { login } } }`

// node is a pull request or issue as GitHub's GraphQL API returns it.
type node struct {
	Typename    string `json:"__typename"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	HeadRefName string `json:"headRefName"`
	Repository  struct {
		NameWithOwner string `json:"nameWithOwner"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

func (n node) id() string {
	return strings.ToLower(n.Repository.NameWithOwner) + "#" + strconv.Itoa(n.Number)
}

// updated is the node's last update, or the zero time where it does not
// parse, which item then refuses.
func (n node) updated() time.Time {
	t, err := time.Parse(time.RFC3339, n.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

type itemShape struct {
	ID          string     `json:"id"`
	Title       string     `json:"title,omitempty"`
	LastTouched time.Time  `json:"last_touched"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	State       string     `json:"state"`
	Links       []link     `json:"links"`
	Attrs       attrs      `json:"attrs"`
}

type link struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

type attrs struct {
	Type   string `json:"type"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Branch string `json:"branch,omitempty"`
}

// item builds the readout item, or reports false for a node missing what an
// item needs.
func (n node) item() (readout.Item, bool) {
	touched := n.updated()
	if touched.IsZero() || n.Number <= 0 || n.Repository.NameWithOwner == "" {
		return readout.Item{}, false
	}
	shape := itemShape{
		ID: n.id(), Title: n.Title, LastTouched: touched.UTC(),
		Links: make([]link, 0, 1),
		Attrs: attrs{Repo: n.Repository.NameWithOwner, Number: n.Number},
	}
	if created, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
		created = created.UTC()
		shape.StartedAt = &created
	}
	// Only a github.com page is linked, so an answer from elsewhere cannot
	// point a renderer at another site.
	if strings.HasPrefix(n.URL, "https://"+host+"/") {
		shape.Links = append(shape.Links, link{Rel: "html", Href: n.URL})
	}
	switch n.Typename {
	case "PullRequest":
		shape.Attrs.Type, shape.Attrs.Branch = "pull_request", n.HeadRefName
		shape.State = strings.ToLower(n.State)
		if n.State == "OPEN" && n.IsDraft {
			shape.State = "draft"
		}
	case "Issue":
		shape.Attrs.Type, shape.State = "issue", strings.ToLower(n.State)
	default:
		return readout.Item{}, false
	}
	if shape.State == "" {
		return readout.Item{}, false
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		return readout.Item{}, false
	}
	return readout.Item{ID: shape.ID, Title: n.Title, LastTouched: touched, StartedAt: shape.StartedAt, Raw: raw}, true
}

// lookup asks for each member by number in one query, one alias each. A
// member GitHub cannot find is returned as missing rather than failing the
// lookup, so the others still count.
func (r *Reader) lookup(ctx context.Context, token string, refs []ref) ([]node, []ref, *failure) {
	var params, fields strings.Builder
	vars := map[string]any{}
	for i, m := range refs {
		s := strconv.Itoa(i)
		fmt.Fprintf(&params, ", $o%s: String!, $n%s: String!, $k%s: Int!", s, s, s)
		fmt.Fprintf(&fields, "m%s: repository(owner: $o%s, name: $n%s) { issueOrPullRequest(number: $k%s) { %s } }\n", s, s, s, s, nodeFields)
		vars["o"+s], vars["n"+s], vars["k"+s] = m.owner, m.repo, m.number
	}
	query := "query(" + strings.TrimPrefix(params.String(), ", ") + ") {\nrateLimit { remaining resetAt }\n" + fields.String() + "}"
	var data map[string]json.RawMessage
	errs, f := r.query(ctx, token, query, vars, &data)
	if f != nil {
		return nil, nil, f
	}
	notFound := map[string]bool{}
	for _, e := range errs {
		if e.Type != "NOT_FOUND" || len(e.Path) == 0 {
			return nil, nil, e.failure()
		}
		alias, _ := e.Path[0].(string)
		notFound[alias] = true
	}
	var nodes []node
	var missing []ref
	for i, m := range refs {
		alias := "m" + strconv.Itoa(i)
		var repo struct {
			Item *node `json:"issueOrPullRequest"`
		}
		raw := data[alias]
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &repo); err != nil {
				return nil, nil, unreadable()
			}
		}
		if repo.Item == nil || notFound[alias] {
			missing = append(missing, m)
			continue
		}
		nodes = append(nodes, *repo.Item)
	}
	return nodes, missing, nil
}

var searchQuery = `query($q: String!, $after: String) {
rateLimit { remaining resetAt }
search(type: ISSUE, query: $q, first: ` + strconv.Itoa(pageSize) + `, after: $after) {
issueCount
pageInfo { hasNextPage endCursor }
nodes { ` + nodeFields + ` }
}
}`

// search pages through one search. short, when not empty, says why the
// results stop before the matches do.
func (r *Reader) search(ctx context.Context, token, q string) (nodes []node, short string, f *failure) {
	label := strings.Fields(q)[0] + " search of " + owner(q)
	var after any
	// searchCap/pageSize pages reach the cap; the two beyond let the last
	// page say there is no next one.
	for range searchCap/pageSize + 2 {
		var data struct {
			Search *struct {
				IssueCount int `json:"issueCount"`
				PageInfo   struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []node `json:"nodes"`
			} `json:"search"`
		}
		errs, qf := r.query(ctx, token, searchQuery, map[string]any{"q": q, "after": after}, &data)
		if qf != nil {
			return nil, "", qf
		}
		if len(errs) > 0 {
			return nil, "", errs[0].failure()
		}
		if data.Search == nil {
			return nil, "", unreadable()
		}
		nodes = append(nodes, data.Search.Nodes...)
		if data.Search.IssueCount > searchCap {
			short = fmt.Sprintf("%s: %d matches, more than the %d GitHub's search returns", label, data.Search.IssueCount, searchCap)
		}
		page := data.Search.PageInfo
		if !page.HasNextPage {
			return nodes, short, nil
		}
		if page.EndCursor == "" || page.EndCursor == after {
			return nodes, label + ": github.com repeated a page, so lookout stopped paging", nil
		}
		after = page.EndCursor
	}
	if short == "" {
		short = label + ": stopped paging at GitHub's search limit"
	}
	return nodes, short, nil
}

// owner is the user: qualifier's value in a search lookout built.
func owner(q string) string {
	for _, term := range strings.Fields(q) {
		if v, ok := strings.CutPrefix(term, "user:"); ok {
			return v
		}
	}
	return ""
}

// gqlError is one entry of a GraphQL response's errors.
type gqlError struct {
	Type    string `json:"type"`
	Path    []any  `json:"path"`
	Message string `json:"message"`
}

func (e gqlError) failure() *failure {
	if e.Type == "RATE_LIMITED" {
		return &failure{health.RateLimited, "github.com: rate limited"}
	}
	// The type is GitHub's code for the error; the message can quote the
	// query, so it is not repeated.
	cause := e.Type
	if cause == "" {
		cause = "an error"
	}
	return &failure{health.GitHubFailed, "github.com reported " + cause}
}

func unreadable() *failure {
	return &failure{health.GitHubFailed, "github.com sent a response lookout could not read"}
}

// query sends one GraphQL request and decodes its data into into. A failure
// is a request that gave no answer; errs are what an answered request
// reported alongside its data.
func (r *Reader) query(ctx context.Context, token, query string, vars map[string]any, into any) ([]gqlError, *failure) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, &failure{health.GitHubFailed, "lookout could not encode a query"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.api, bytes.NewReader(body))
	if err != nil {
		return nil, &failure{health.GitHubFailed, "lookout could not build a request"}
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "lookout")
	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &failure{health.GitHubFailed, "github.com: interrupted"}
		}
		return nil, &failure{health.GitHubFailed, "github.com could not be reached"}
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if closeErr := resp.Body.Close(); readErr == nil {
		readErr = closeErr
	}
	if f := classify(resp); f != nil {
		return nil, f
	}
	if readErr != nil {
		return nil, unreadable()
	}
	var doc struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, unreadable()
	}
	if len(doc.Data) == 0 || string(doc.Data) == "null" {
		if len(doc.Errors) > 0 {
			return nil, doc.Errors[0].failure()
		}
		return nil, unreadable()
	}
	if err := json.Unmarshal(doc.Data, into); err != nil {
		return nil, unreadable()
	}
	return doc.Errors, nil
}

// classify reads a response's status. GitHub signals a rate limit as 429, or
// as 403 with no requests remaining or a Retry-After (GitHub's REST and
// GraphQL rate-limit documentation); any other 403 is a refusal.
func classify(resp *http.Response) *failure {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return &failure{health.AuthFailed, "github.com refused the token: run gh auth login --hostname " + host}
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""):
		detail := "github.com: rate limited"
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			detail += " until " + time.Unix(reset, 0).UTC().Format(time.RFC3339)
		}
		return &failure{health.RateLimited, detail}
	}
	return &failure{health.GitHubFailed, "github.com answered " + strconv.Itoa(resp.StatusCode)}
}

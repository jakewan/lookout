// Package github reads pull requests and issues on github.com as one input's
// items, asking only about owners a setting's owner fence lists and admitting
// only what those owners hold.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	// describedPage is the nodes a page holds where each pull request brings
	// its description. GitHub allows a description of 65,536 characters
	// (GitHub's documentation on issue and pull request bodies), so a full page
	// of long ones could pass maxResponse, and a page past it fails every read,
	// since the same search returns the same page. This page stays under it
	// while descriptions average fewer than about five bytes a character as
	// JSON, which UTF-8 text does; one dense with characters JSON escapes, such
	// as control characters, could still pass it.
	describedPage = 50
	// memberChunk bounds how many members one lookup asks for, keeping each
	// query well inside GitHub's per-query node limit.
	memberChunk = 50
	// keysPerSearch and keyText bound one key search: GitHub's search
	// documentation refuses a query with more than five AND, OR or NOT
	// operators, or longer than 256 characters not counting its qualifiers.
	// A query past either is refused, which would fail the search.
	keysPerSearch = 6
	keyText       = 256
	ghTimeout     = 10 * time.Second
	httpTimeout   = 30 * time.Second
	// maxResponse bounds what lookout reads of one response.
	maxResponse = 16 << 20
)

// Reader reads GitHub for one invocation. The token is asked of gh once, when
// the first github input is read.
type Reader struct {
	getenv  func(string) string
	gh      string
	missing string // why there is no gh, when there is none
	api     string
	client  *http.Client

	asked    bool
	token    string
	tokenErr string
}

// NewReader finds gh on the invocation's PATH and sends queries to api.
func NewReader(getenv func(string) string, api string) *Reader {
	r := &Reader{getenv: getenv, api: api, client: &http.Client{
		Timeout: httpTimeout,
		// A redirect is answered as the response it is, never followed, so
		// the token goes to the one endpoint lookout names and nowhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	r.gh, r.missing = execpath.Find(getenv, "gh")
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
// number, at any age and in any state, the open work discovered within the
// owner fence, and the pull requests a search finds carrying a key. A member
// outside the fence is never asked for, and a node the fence does not admit
// is counted, never named.
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
	// listed is each node a member lookup or discovery found, which may carry
	// its key in its branch alone; one a key search alone found carries a key
	// only where its title or description names it.
	listed := map[string]bool{}
	fenced := map[string]bool{}
	var failures []failure
	readAny := false
	admit := func(n node, list bool) {
		id := n.id()
		owner := strings.ToLower(n.Repository.Owner.Login)
		if !slices.Contains(owners, owner) {
			fenced[id] = true
			return
		}
		found[id] = n
		listed[id] = listed[id] || list
	}

	for chunk := range slices.Chunk(wanted, memberChunk) {
		answers, f := r.lookup(ctx, token, chunk)
		if f != nil {
			failures = append(failures, *f)
			if f.stops() {
				break
			}
			continue
		}
		readAny = true
		for _, a := range answers {
			member := in.Name + ":" + a.ref.id()
			switch {
			case a.refused != "":
				failures = append(failures, failure{health.GitHubFailed, member + ": github.com reported " + a.refused})
			case a.node == nil:
				detail(member + ": not found, or the token cannot see it")
			case a.node.id() != a.ref.id():
				// A member is matched by the id the configuration names, so
				// one found under another name is not that member. Where the
				// new owner is outside the fence it is counted, never named.
				if slices.Contains(owners, strings.ToLower(a.node.Repository.Owner.Login)) {
					detail(member + ": now at " + in.Name + ":" + a.node.id())
				} else {
					detail(member + ": moved to an owner outside forge_owners")
					fenced[a.node.id()] = true
				}
			default:
				admit(*a.node, true)
			}
		}
	}

	type search struct {
		query    searchText
		q, label string
	}
	// Only a setting with a key pattern reads descriptions, so only its pull
	// request searches ask for them.
	prSearch := plainSearch
	if sc.Keys != nil {
		prSearch = describedSearch
	}
	searches := make([]search, 0, 2*len(owners))
	since := sc.Since.UTC().Format(time.RFC3339)
	for _, o := range owners {
		searches = append(searches,
			search{prSearch, "is:pr is:open author:@me user:" + o + " updated:>=" + since, "pull request search of " + o},
			search{plainSearch, "is:issue is:open assignee:@me user:" + o + " updated:>=" + since, "issue search of " + o})
	}
	stopped := slices.ContainsFunc(failures, func(f failure) bool { return f.stops() })
	run := func(s search) []node {
		if stopped {
			return nil
		}
		nodes, read, short, f := searchAll[node](ctx, r, token, s.query, s.q, s.label)
		if f != nil {
			failures = append(failures, *f)
			stopped = f.stops()
		}
		readAny = readAny || read
		if short != "" {
			out.Health.Lower(health.Degraded, health.Truncated)
			detail(short)
		}
		return nodes
	}
	for _, s := range searches {
		for _, n := range run(s) {
			if n.updated().Before(sc.Since) {
				continue
			}
			admit(n, true)
		}
	}
	// A key search finds the pull requests that name a ticket in any state, at
	// any age and by any author the repository trusts, so merged work and a
	// colleague's join the ticket's workstream. GitHub's search finds the key
	// anywhere in a title or description, in any case and in text its
	// tokenizer splits, so a hit is admitted only where its title's key, or a
	// key its description names, is one asked for: never its branch's, which
	// joins only your own pull request discovery lists. Several keys go in one
	// search, since each search is a round trip on every read. That rests on
	// GitHub applying is:pr, in:title,body and user: to every alternative OR
	// joins, which read-only queries against github.com showed; were it to
	// apply them to one alternative only, hits from elsewhere would be fetched,
	// and the checks here and in admit would still keep them out.
	groups, skipped := keyGroups(sc.Keys.Searchable())
	for _, key := range skipped {
		detail("key " + key + ": too long for GitHub's search, so it is not searched for")
	}
	for _, keys := range groups {
		asked := strings.Join(keys, `" OR "`)
		for _, o := range owners {
			for _, n := range run(search{describedSearch, `is:pr in:title,body "` + asked + `" user:` + o, "key search for " + strings.Join(keys, ", ") + " in " + o}) {
				named := append(sc.Keys.All(n.Body), n.key(sc.Keys, false))
				if n.Typename == "PullRequest" && trusted[n.AuthorAssociation] && slices.ContainsFunc(keys, func(k string) bool {
					return slices.ContainsFunc(named, func(c string) bool { return strings.EqualFold(k, c) })
				}) {
					admit(n, false)
				}
			}
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
		item.Key = n.key(sc.Keys, listed[id])
		if n.Typename == "PullRequest" {
			item.Keys = sc.Keys.All(n.Body)
		}
		item.Head = n.head()
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
		return "", r.missing
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
	_, exited := errors.AsType[*exec.ExitError](err)
	switch {
	case ctx.Err() != nil:
		return "", "gh: interrupted"
	case runCtx.Err() != nil:
		return "", "gh: gave no token within " + ghTimeout.String() + ": check gh auth status --hostname " + host
	case err != nil && !exited:
		return "", "gh: could not run"
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
	// GraphQL's Int is 32-bit, so a larger number cannot be asked for.
	if err != nil || n <= 0 || n > math.MaxInt32 || strconv.Itoa(n) != num {
		return ref{}, false
	}
	return ref{owner: strings.ToLower(owner), repo: strings.ToLower(name), number: n}, true
}

// keyGroups splits keys into the groups one search each asks for, each within
// keysPerSearch keys and keyText characters as the query quotes and joins
// them. A key too long to fit a search alone is skipped, never asked for.
func keyGroups(keys []string) (groups [][]string, skipped []string) {
	var group []string
	text := 0
	for _, k := range keys {
		size := len(`""`) + len(k)
		if size > keyText {
			skipped = append(skipped, k)
			continue
		}
		if len(group) > 0 {
			size += len(" OR ")
		}
		if len(group) == keysPerSearch || text+size > keyText {
			groups, group, text = append(groups, group), nil, 0
			size = len(`""`) + len(k)
		}
		group = append(group, k)
		text += size
	}
	if len(group) > 0 {
		groups = append(groups, group)
	}
	return groups, skipped
}

// trusted is each author association, as GitHub names how an author relates to
// a repository, whose pull request a key search may join: the owner, a member
// of the owning organization, or a collaborator. Anyone can open a pull
// request on a public repository and title it with a key.
var trusted = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

// nodeFields selects what an item is built from, for a pull request or an
// issue alike, and describedFields its pull requests' descriptions as well,
// which only a pull request search under a key pattern asks for: a member is
// never joined by its description.
const (
	prFields        = `number title url state isDraft createdAt updatedAt headRefName headRepository { nameWithOwner } authorAssociation repository { nameWithOwner owner { login } }`
	issueFields     = `... on Issue { number title url state createdAt updatedAt repository { nameWithOwner owner { login } } }`
	nodeFields      = "__typename\n... on PullRequest { " + prFields + " }\n" + issueFields
	describedFields = "__typename\n... on PullRequest { " + prFields + " body }\n" + issueFields
)

// node is a pull request or issue as GitHub's GraphQL API returns it.
type node struct {
	Typename       string `json:"__typename"`
	Number         int    `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
	HeadRefName    string `json:"headRefName"`
	HeadRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	AuthorAssociation string `json:"authorAssociation"`
	Body              string `json:"body"`
	Repository        struct {
		NameWithOwner string `json:"nameWithOwner"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

func (n node) id() string { return itemID(n.Repository.NameWithOwner, n.Number) }

// key is the key a pull request carries, in its title or else, where branch
// says its branch counts, its branch; an issue carries none.
func (n node) key(keys *config.Keys, branch bool) string {
	if n.Typename != "PullRequest" {
		return ""
	}
	if !branch {
		return keys.Of(n.Title)
	}
	return keys.Of(n.Title, n.HeadRefName)
}

// head is the id a git or Claude Code input gives the branch an open pull
// request was made from, so that branch can follow it. A closed or merged
// one's is empty, since its branch name may since have been reused for other
// work, as is one whose head repository is gone, such as a deleted fork.
func (n node) head() string {
	if n.Typename != "PullRequest" || n.State != "OPEN" || n.HeadRepository == nil || n.HeadRepository.NameWithOwner == "" || n.HeadRefName == "" {
		return ""
	}
	return strings.ToLower(n.HeadRepository.NameWithOwner) + ":" + n.HeadRefName
}

// itemID is the id of number in the repository GitHub names nameWithOwner.
func itemID(nameWithOwner string, number int) string {
	return strings.ToLower(nameWithOwner) + "#" + strconv.Itoa(number)
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

// answer is what a lookup found for one member.
type answer struct {
	ref ref
	// node is set when GitHub answered for the member. Its id differs from
	// the ref's when the repository has been renamed or transferred since.
	node *node
	// refused is GitHub's error code when it answered for this member alone
	// with an error other than not finding it.
	refused string
}

// lookup asks for each member by number in one query, one alias each. An
// error GitHub ties to one alias is that member's alone, so the others still
// count.
func (r *Reader) lookup(ctx context.Context, token string, refs []ref) ([]answer, *failure) {
	var params, fields strings.Builder
	vars := map[string]any{}
	for i, m := range refs {
		s := strconv.Itoa(i)
		fmt.Fprintf(&params, ", $o%s: String!, $n%s: String!, $k%s: Int!", s, s, s)
		fmt.Fprintf(&fields, "m%s: repository(owner: $o%s, name: $n%s) { issueOrPullRequest(number: $k%s) { %s } }\n", s, s, s, s, nodeFields)
		vars["o"+s], vars["n"+s], vars["k"+s] = m.owner, m.repo, m.number
	}
	query := "query(" + strings.TrimPrefix(params.String(), ", ") + ") {\n" + fields.String() + "}"
	var data map[string]json.RawMessage
	errs, f := r.query(ctx, token, query, vars, &data)
	if f != nil {
		return nil, f
	}
	refused := map[string]string{}
	for _, e := range errs {
		alias := ""
		if len(e.Path) > 0 {
			alias, _ = e.Path[0].(string)
		}
		if _, ok := data[alias]; !ok {
			return nil, e.failure()
		}
		if e.Type != "NOT_FOUND" {
			refused[alias] = e.code()
		}
	}
	answers := make([]answer, 0, len(refs))
	for i, m := range refs {
		alias := "m" + strconv.Itoa(i)
		a := answer{ref: m, refused: refused[alias]}
		var repo struct {
			Item *node `json:"issueOrPullRequest"`
		}
		if raw := data[alias]; a.refused == "" && len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &repo); err != nil {
				return nil, unreadable()
			}
			a.node = repo.Item
		}
		answers = append(answers, a)
	}
	return answers, nil
}

// searchText is a search query and the nodes each of its pages holds.
type searchText struct {
	text string
	page int
}

// searchQuery is a search selecting fields on each node, page nodes a page.
func searchQuery(fields string, page int) searchText {
	return searchText{`query($q: String!, $after: String) {
search(type: ISSUE, query: $q, first: ` + strconv.Itoa(page) + `, after: $after) {
issueCount
pageInfo { hasNextPage endCursor }
nodes { ` + fields + ` }
}
}`, page}
}

var (
	plainSearch     = searchQuery(nodeFields, pageSize)
	describedSearch = searchQuery(describedFields, describedPage)
)

// searchAll pages through one search, decoding each node as a T. short, when not
// empty, says why the results stop before the matches do, opening with label.
// A page that fails ends the search with the pages before it kept, and read
// says whether any page was.
func searchAll[T any](ctx context.Context, r *Reader, token string, query searchText, q, label string) (nodes []T, read bool, short string, f *failure) {
	var after any
	// searchCap/page pages reach the cap; the two beyond let the last page
	// say there is no next one.
	for range searchCap/query.page + 2 {
		var data struct {
			Search *struct {
				IssueCount int `json:"issueCount"`
				PageInfo   struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []T `json:"nodes"`
			} `json:"search"`
		}
		errs, qf := r.query(ctx, token, query.text, map[string]any{"q": q, "after": after}, &data)
		if qf != nil {
			return nodes, read, short, qf
		}
		if len(errs) > 0 {
			return nodes, read, short, errs[0].failure()
		}
		if data.Search == nil {
			return nodes, read, short, unreadable()
		}
		read = true
		nodes = append(nodes, data.Search.Nodes...)
		if data.Search.IssueCount > searchCap {
			short = fmt.Sprintf("%s: %d matches, more than the %d GitHub's search returns", label, data.Search.IssueCount, searchCap)
		}
		page := data.Search.PageInfo
		if !page.HasNextPage {
			return nodes, read, short, nil
		}
		if page.EndCursor == "" || page.EndCursor == after {
			return nodes, read, label + ": github.com repeated a page, so lookout stopped paging", nil
		}
		after = page.EndCursor
	}
	if short == "" {
		short = label + ": stopped paging at GitHub's search limit"
	}
	return nodes, read, short, nil
}

// gqlError is one entry of a GraphQL response's errors.
type gqlError struct {
	Type    string `json:"type"`
	Path    []any  `json:"path"`
	Message string `json:"message"`
}

// code is GitHub's code for the error. Its message can quote the query, so it
// is not repeated.
func (e gqlError) code() string {
	if e.Type == "" {
		return "an error"
	}
	return e.Type
}

func (e gqlError) failure() *failure {
	return &failure{health.GitHubFailed, "github.com reported " + e.code()}
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
	// An object created before GitHub's current id format can be served under
	// either format, chosen by this header's default (GitHub's guide to
	// migrating global node ids); asking for the current one keeps an id the
	// baseline stored from changing under it.
	req.Header.Set("X-Github-Next-Global-ID", "1")
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
	if f := r.classify(resp, data); f != nil {
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
	// GraphQL reports its own rate limit as an error in a 200 response.
	if slices.ContainsFunc(doc.Errors, func(e gqlError) bool { return e.Type == "RATE_LIMITED" }) {
		return nil, rateLimited(resp.Header)
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
// as 403 with no requests remaining, a Retry-After, or a body naming a
// secondary rate limit, any of which a secondary limit may send alone
// (GitHub's REST and GraphQL rate-limit documentation); any other 403 is a
// refusal.
func (r *Reader) classify(resp *http.Response, body []byte) *failure {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		// gh hands back GH_TOKEN, or else GITHUB_TOKEN, ahead of its login,
		// and refuses gh auth login while either is set.
		for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if r.getenv(key) != "" {
				return &failure{health.AuthFailed, "github.com refused the token in " + key}
			}
		}
		return &failure{health.AuthFailed, "github.com refused the token: run gh auth login --hostname " + host}
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" ||
			resp.Header.Get("Retry-After") != "" ||
			bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit"))):
		return rateLimited(resp.Header)
	}
	return &failure{health.GitHubFailed, "github.com answered " + strconv.Itoa(resp.StatusCode)}
}

// rateLimited says until when, where the response's headers say.
func rateLimited(h http.Header) *failure {
	detail := "github.com: rate limited"
	if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		detail += " until " + time.Unix(reset, 0).UTC().Format(time.RFC3339)
	}
	return &failure{health.RateLimited, detail}
}

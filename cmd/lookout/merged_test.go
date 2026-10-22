package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// mergedDoc is one merged pull request a record lists.
type mergedDoc struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	MergedAt    string `json:"merged_at"`
	IntoDefault bool   `json:"into_default"`
}

// mergedPRsDoc is a record's merged pull requests. PRs is a pointer so a
// missing list can be told from an empty one.
type mergedPRsDoc struct {
	Since   string       `json:"since"`
	Through string       `json:"through"`
	PRs     *[]mergedDoc `json:"prs"`
	Failed  string       `json:"failed"`
}

// mergedRecord runs a snapshot and returns its merged pull requests, failing
// when the record carries none.
func (f *fixture) mergedRecord() mergedPRsDoc {
	f.t.Helper()
	f.snapshot()
	return f.lastMerged()
}

func (f *fixture) lastMerged() mergedPRsDoc {
	f.t.Helper()
	data, err := os.ReadFile(f.baselinePath("personal"))
	if err != nil {
		f.t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var rec struct {
		MergedPRs *mergedPRsDoc `json:"merged_prs"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		f.t.Fatal(err)
	}
	if rec.MergedPRs == nil {
		f.t.Fatalf("record has no merged_prs:\n%s", lines[len(lines)-1])
	}
	return *rec.MergedPRs
}

// ids is the listed pull requests' ids, or fails when the read did not
// succeed.
func (m mergedPRsDoc) ids(t *testing.T) []string {
	t.Helper()
	if m.Failed != "" || m.PRs == nil {
		t.Fatalf("merged_prs = %+v, want a successful read", m)
	}
	out := make([]string, 0, len(*m.PRs))
	for _, p := range *m.PRs {
		out = append(out, p.ID)
	}
	return out
}

// mergedPR is an authored pull request in repo merged at the instant given.
func mergedPR(repo string, number int, at time.Time) ghNode {
	return ghNode{pr: true, repo: repo, number: number, state: "MERGED", authored: true, merged: at, updated: 1000 * time.Hour}
}

// day is a time on the fixture's first date, 2030-06-15 in UTC.
func day(offset int, hour int) time.Time {
	return time.Date(2030, 6, 15+offset, hour, 0, 0, 0, time.UTC)
}

// failMerged makes every merged search answer with status.
func failMerged(fake *githubFake, status int) {
	fake.respond = func(w http.ResponseWriter, r ghRequest) bool {
		q, _ := r.variables["q"].(string)
		if !strings.Contains(q, "is:merged") {
			return false
		}
		http.Error(w, `{"message":"no"}`, status)
		return true
	}
}

func TestSnapshotAsksGitHubNothingWithoutAGitHubInput(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	fake := f.withGitHub(mergedPR("alice/app", 1, day(0, 9)))

	f.snapshot()

	data, err := os.ReadFile(f.baselinePath("personal"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "merged_prs") {
		t.Errorf("record = %s, want no merged_prs without a github input", data)
	}
	if len(fake.requests) != 0 {
		t.Errorf("GitHub was asked %d times, want never", len(fake.requests))
	}
}

func TestSnapshotListsTodaysMergesOnTheFirstRecord(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub(
		mergedPR("Alice/App", 7, day(0, 9)),
		mergedPR("alice/app", 6, day(-1, 20)),
		ghNode{pr: true, repo: "alice/app", number: 8, state: "OPEN", authored: true, updated: time.Hour},
		ghNode{pr: true, repo: "alice/app", number: 9, state: "MERGED", merged: day(0, 10), updated: time.Hour},
	)

	m := f.mergedRecord()

	if got, want := m.ids(t), []string{"alice/app#7"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if m.Since != "2030-06-15T00:00:00Z" || m.Through != "2030-06-15T12:00:00Z" {
		t.Errorf("since = %q, through = %q, want the start of the date to now", m.Since, m.Through)
	}
	got := (*m.PRs)[0]
	if got.NodeID != "PR_Alice/App_7" || got.MergedAt != "2030-06-15T09:00:00Z" || !got.IntoDefault {
		t.Errorf("pr = %+v", got)
	}
	for _, q := range fake.searches() {
		if strings.Contains(q, "is:merged") && !strings.HasSuffix(q, " merged:>=2030-06-15T00:00:00Z") {
			t.Errorf("search %q, want it to reach back no further than where the series begins", q)
		}
	}
}

func TestSnapshotListsAnEmptyDayAsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub()

	m := f.mergedRecord()

	if m.PRs == nil || len(*m.PRs) != 0 || m.Failed != "" {
		t.Errorf("merged_prs = %+v, want an empty list, never a missing one", m)
	}
}

func TestSnapshotOpensEachWindowWhereTheLastEnded(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub(
		mergedPR("alice/app", 1, day(0, 11)),
		mergedPR("alice/app", 2, day(0, 18)),
	)

	if got := f.mergedRecord().ids(t); !slices.Equal(got, []string{"alice/app#1"}) {
		t.Fatalf("first record ids = %v", got)
	}
	f.nextDay()
	m := f.mergedRecord()

	if got, want := m.ids(t), []string{"alice/app#2"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v: the merge already listed is not listed again", got, want)
	}
	if m.Since != "2030-06-15T12:00:00Z" {
		t.Errorf("since = %q, want the previous record's through", m.Since)
	}
}

func TestSnapshotListsAMergeTheSearchIndexFoundLate(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	late := mergedPR("alice/app", 3, day(0, 11))
	late.heldBack = 1
	f.withGitHub(mergedPR("alice/app", 1, day(0, 10)), late)

	if got := f.mergedRecord().ids(t); !slices.Equal(got, []string{"alice/app#1"}) {
		t.Fatalf("first record ids = %v", got)
	}
	f.nextDay()
	if got, want := f.mergedRecord().ids(t), []string{"alice/app#3"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v, merged before the last record but indexed after it", got, want)
	}
	f.nextDay()
	if got := f.mergedRecord().ids(t); len(got) != 0 {
		t.Errorf("ids = %v, want none: each merge is listed once", got)
	}
}

func TestSnapshotRecordsAFailedMergedReadAndCatchesUp(t *testing.T) {
	cases := []struct {
		status int
		reason string
	}{
		{http.StatusInternalServerError, "github_failed"},
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusUnauthorized, "auth_failed"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", githubOnly)
			fake := f.withGitHub(mergedPR("alice/app", 1, day(0, 9)), mergedPR("alice/app", 2, day(1, 6)))
			f.mergedRecord()
			f.nextDay()
			failMerged(fake, c.status)

			m := f.mergedRecord()
			if m.Failed != c.reason || m.PRs != nil {
				t.Errorf("merged_prs = %+v, want failed %s with no list", m, c.reason)
			}
			if b := f.baseline("personal"); b[len(b)-1].Incomplete {
				t.Error("incomplete = true, want a failed merged read to leave the inputs' record sound")
			}

			f.nextDay()
			fake.respond = nil
			m = f.mergedRecord()
			if got, want := m.ids(t), []string{"alice/app#2"}; !slices.Equal(got, want) {
				t.Errorf("ids = %v, want %v, merged while the read failed", got, want)
			}
			if m.Since != "2030-06-15T12:00:00Z" {
				t.Errorf("since = %q, want the failed record's since", m.Since)
			}
		})
	}
}

func TestSnapshotCatchesUpAfterAFailedFirstRead(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub(mergedPR("alice/app", 1, day(0, 9)))
	failMerged(fake, http.StatusInternalServerError)
	if m := f.mergedRecord(); m.Failed == "" {
		t.Fatalf("merged_prs = %+v, want failed", m)
	}

	f.nextDay()
	fake.respond = nil
	m := f.mergedRecord()

	if got, want := m.ids(t), []string{"alice/app#1"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v, merged on the day the first read failed", got, want)
	}
	if m.Since != "2030-06-15T00:00:00Z" {
		t.Errorf("since = %q, want the failed first record's since", m.Since)
	}
}

func TestSnapshotTreatsATruncatedMergedSearchAsFailed(t *testing.T) {
	for name, setup := range map[string]func(*githubFake){
		"past the result cap": func(g *githubFake) { g.issueCount = 1001 },
		"a repeated cursor":   func(g *githubFake) { g.stuckCursor = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("personal.yml", githubOnly)
			setup(f.withGitHub(mergedPR("alice/app", 1, day(0, 9))))

			if m := f.mergedRecord(); m.Failed != "truncated" || m.PRs != nil {
				t.Errorf("merged_prs = %+v, want failed truncated with no list", m)
			}
		})
	}
}

func TestSnapshotCountsMergesOnlyWithinTheOwnerFence(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub()
	mine := mergedPR("alice/app", 1, day(0, 9))
	moved := mergedPR("mallory/loot", 2, day(0, 10))
	fake.respond = func(w http.ResponseWriter, r ghRequest) bool {
		q, _ := r.variables["q"].(string)
		if !strings.Contains(q, "is:merged") {
			return false
		}
		writeJSON(t, w, map[string]any{"data": map[string]any{
			"search": map[string]any{
				"issueCount": 2,
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": "2"},
				"nodes":      []any{mine.json(), moved.json()},
			},
		}})
		return true
	}

	m := f.mergedRecord()

	if got, want := m.ids(t), []string{"alice/app#1"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	data, err := os.ReadFile(f.baselinePath("personal"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "mallory") {
		t.Errorf("baseline names a repository the fence keeps out:\n%s", data)
	}
	var merged []string
	for _, q := range fake.searches() {
		if strings.Contains(q, "is:merged") {
			merged = append(merged, q)
		}
	}
	if len(merged) != 1 || !strings.Contains(merged[0], "user:alice") || !strings.Contains(merged[0], "author:@me") || strings.Contains(merged[0], "carol") {
		t.Errorf("merged searches = %q, want one of alice's authored work, the only github.com owner", merged)
	}
}

func TestSnapshotLeavesAMergeAfterTheRecordForTheNext(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub(mergedPR("alice/app", 1, day(0, 13)))

	if got := f.mergedRecord().ids(t); len(got) != 0 {
		t.Errorf("ids = %v, want none: it merged after the record's through", got)
	}
	f.nextDay()
	if got, want := f.mergedRecord().ids(t), []string{"alice/app#1"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestSnapshotAsksGitHubNothingOnADateAlreadyRecorded(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub(mergedPR("alice/app", 1, day(0, 9)))
	f.snapshot()
	asked := len(fake.requests)

	f.clock = f.clock.Add(time.Hour)
	if code, _, stderr := f.run("snapshot"); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if len(fake.requests) != asked {
		t.Errorf("GitHub was asked %d more times on a date already recorded", len(fake.requests)-asked)
	}
}

func TestSnapshotNeverCountsAMergeFromBeforeTheSeriesBegan(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub(mergedPR("alice/app", 1, day(-1, 20)), mergedPR("alice/app", 2, day(0, 9)))
	// GitHub answering with merges older than the search asked for still
	// counts none of them.
	fake.ignoreSince = true

	if got, want := f.mergedRecord().ids(t), []string{"alice/app#2"}; !slices.Equal(got, want) {
		t.Errorf("first record ids = %v, want %v", got, want)
	}
	f.nextDay()
	if got := f.mergedRecord().ids(t); len(got) != 0 {
		t.Errorf("ids = %v, want none: the earlier merge predates the series", got)
	}
}

func TestSnapshotListsAMergeOnceAcrossARename(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	pr := mergedPR("alice/old", 5, day(0, 11))
	pr.nodeID = "PR_kept"
	fake := f.withGitHub(pr)
	if got := f.mergedRecord().ids(t); !slices.Equal(got, []string{"alice/old#5"}) {
		t.Fatalf("first record ids = %v", got)
	}

	fake.nodes[0].repo = "alice/new"
	f.nextDay()

	if got := f.mergedRecord().ids(t); len(got) != 0 {
		t.Errorf("ids = %v, want none: the renamed repository's merge was already listed", got)
	}
}

func TestSnapshotStartsTheSeriesAtTheFirstRecordThatReadsMerges(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", handWritten)
	f.supplied("tracker.json", trackerItems())
	f.snapshot()

	f.nextDay()
	f.write("personal.yml", githubOnly)
	f.withGitHub(mergedPR("alice/app", 1, day(0, 20)), mergedPR("alice/app", 2, day(1, 1)))
	m := f.mergedRecord()

	if got, want := m.ids(t), []string{"alice/app#2"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if m.Since != "2030-06-16T00:00:00Z" {
		t.Errorf("since = %q, want the start of the first date that reads merges", m.Since)
	}
}

func TestSnapshotRecordsAMergedReadWithoutGH(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub()
	f.env["PATH"] = t.TempDir()

	if m := f.mergedRecord(); m.Failed != "auth_failed" || m.PRs != nil {
		t.Errorf("merged_prs = %+v, want failed auth_failed", m)
	}
}

func TestSnapshotFailsWhenInterruptedDuringTheMergedRead(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	fake := f.withGitHub()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.respond = func(w http.ResponseWriter, r ghRequest) bool {
		q, _ := r.variables["q"].(string)
		if !strings.Contains(q, "is:merged") {
			return false
		}
		cancel()
		http.Error(w, "gone", http.StatusServiceUnavailable)
		return true
	}

	code, stdout, _ := f.runIn(ctx, "snapshot")

	if code != exitFailure || stdout != "" {
		t.Errorf("exit code = %d, stdout = %q, want a failure", code, stdout)
	}
	if b := f.baseline("personal"); len(b) != 0 {
		t.Errorf("an interrupted snapshot wrote %d records", len(b))
	}
}

func TestSnapshotFailsAMergedReadMissingAMergeTime(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	// A node with no merge time matches no merged:>= search, so the fake
	// returns matches whatever their dates.
	f.withGitHub(mergedPR("alice/app", 1, time.Time{})).ignoreSince = true

	if m := f.mergedRecord(); m.Failed != "github_failed" || m.PRs != nil {
		t.Errorf("merged_prs = %+v, want failed github_failed", m)
	}
}

func TestSnapshotRecordsWhetherAMergeTargetedTheDefaultBranch(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	stacked := mergedPR("alice/app", 2, day(0, 10))
	stacked.base = "feature/lower"
	f.withGitHub(mergedPR("alice/app", 1, day(0, 9)), stacked)

	m := f.mergedRecord()

	into := map[string]bool{}
	for _, p := range *m.PRs {
		into[p.ID] = p.IntoDefault
	}
	if len(into) != 2 || !into["alice/app#1"] || into["alice/app#2"] {
		t.Errorf("into_default = %v, want #1 true and #2 false", into)
	}
}

func TestSnapshotBoundsHowFarBackAMergedReadReaches(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub()
	f.mergedRecord()

	f.clock = f.clock.AddDate(0, 0, 120)
	m := f.mergedRecord()

	if want := f.clock.AddDate(0, 0, -90).Format(time.RFC3339); m.Since != want {
		t.Errorf("since = %q, want %q, 90 days before through", m.Since, want)
	}
}

func TestSnapshotStartsOverWhenARecordLandsWhileItReads(t *testing.T) {
	f := newFixture(t)
	f.write("personal.yml", githubOnly)
	f.withGitHub(mergedPR("alice/app", 1, day(0, 9)))
	once := false
	// A snapshot run under a clock a day behind records its own date while
	// this one reads, which moves where this one's window opens.
	beforeSnapshotLock = func() {
		if !once {
			once = true
			f.clock = f.clock.AddDate(0, 0, -1)
			f.snapshot()
			f.clock = f.clock.AddDate(0, 0, 1)
		}
	}
	t.Cleanup(func() { beforeSnapshotLock = func() {} })

	m := f.mergedRecord()

	if m.Since != "2030-06-14T12:00:00Z" {
		t.Errorf("since = %q, want the through of the record that landed", m.Since)
	}
}

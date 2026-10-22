package github

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
)

// Merge is one authored pull request that merged.
type Merge struct {
	// ID is owner/repo#N, as a github input names the pull request.
	ID string
	// NodeID is GitHub's global id for it, which GitHub says can be kept as a
	// reference; renaming its repository changes ID. That it also survives a
	// transfer is not confirmed; were it to change, a transfer within a day of
	// the merge would list the merge twice.
	NodeID   string
	MergedAt time.Time
	// IntoDefault reports whether it targeted the branch that is its
	// repository's default when read, rather than, say, the branch of a pull
	// request below it. A default branch renamed since the merge reads as
	// another branch.
	IntoDefault bool
}

// mergedFields selects what a Merge is built from.
const mergedFields = `... on PullRequest { id number mergedAt baseRefName repository { nameWithOwner owner { login } defaultBranchRef { name } } }`

var mergedSearch = searchQuery(mergedFields)

type mergedNode struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	MergedAt    string `json:"mergedAt"`
	BaseRefName string `json:"baseRefName"`
	Repository  struct {
		NameWithOwner string `json:"nameWithOwner"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
		DefaultBranchRef *struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	} `json:"repository"`
}

// Merged returns the pull requests the token's account authored that merged
// at or after from, in each github.com owner the fence lists. A merge whose
// repository is now outside the fence is left out, never named. Where the
// read failed, reason is the health reason and detail says why for a person;
// a read that failed returns no merges, since a partial list would read as
// fewer merges.
func (r *Reader) Merged(ctx context.Context, owners []config.Owner, from time.Time) (merges []Merge, reason, detail string) {
	token, problem := r.credential(ctx)
	if problem != "" {
		return nil, health.AuthFailed, problem
	}
	var hub []string
	for _, o := range owners {
		if o.Host == host {
			hub = append(hub, o.Name)
		}
	}
	since := from.UTC().Format(time.RFC3339)
	out := make([]Merge, 0)
	for _, o := range hub {
		label := "merged pull request search of " + o
		nodes, _, short, f := searchAll[mergedNode](ctx, r, token, mergedSearch, "is:pr is:merged author:@me user:"+o+" merged:>="+since, label)
		switch {
		case f != nil:
			return nil, f.reason, label + ": " + f.detail
		case short != "":
			return nil, health.Truncated, short
		}
		for _, n := range nodes {
			at, err := time.Parse(time.RFC3339, n.MergedAt)
			if err != nil || n.ID == "" || n.Number <= 0 || n.Repository.NameWithOwner == "" {
				return nil, health.GitHubFailed, label + ": github.com sent a merge without the fields lookout reads"
			}
			if !slices.Contains(hub, strings.ToLower(n.Repository.Owner.Login)) {
				continue
			}
			def := n.Repository.DefaultBranchRef
			out = append(out, Merge{
				ID:          itemID(n.Repository.NameWithOwner, n.Number),
				NodeID:      n.ID,
				MergedAt:    at.UTC(),
				IntoDefault: def != nil && def.Name == n.BaseRefName,
			})
		}
	}
	slices.SortFunc(out, func(a, b Merge) int { return strings.Compare(a.ID, b.ID) })
	return out, "", ""
}

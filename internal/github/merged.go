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
	// NodeID is GitHub's global id for it, which a rename or transfer of its
	// repository leaves unchanged where ID changes.
	NodeID   string
	MergedAt time.Time
	// IntoDefault reports whether it merged into its repository's default
	// branch rather than, say, the branch of a pull request below it.
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
// repository is now outside the fence is left out, never named. The second
// result is the health reason the read failed for, or empty; a read that
// failed returns no merges, since a partial list would read as fewer merges.
func (r *Reader) Merged(ctx context.Context, owners []config.Owner, from time.Time) ([]Merge, string) {
	token, problem := r.credential(ctx)
	if problem != "" {
		return nil, health.AuthFailed
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
		nodes, _, short, f := searchAll[mergedNode](ctx, r, token, mergedSearch, "is:pr is:merged author:@me user:"+o+" merged:>="+since, "merged pull request search of "+o)
		switch {
		case f != nil:
			return nil, f.reason
		case short != "":
			return nil, health.Truncated
		}
		for _, n := range nodes {
			at, err := time.Parse(time.RFC3339, n.MergedAt)
			if err != nil || n.ID == "" || n.Number <= 0 || n.Repository.NameWithOwner == "" {
				return nil, health.GitHubFailed
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
	return out, ""
}

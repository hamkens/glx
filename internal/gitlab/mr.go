package gitlab

import (
	"context"
	"time"

	"github.com/hamkens/glx/internal/forge"
)

// connectionField maps a scope to the currentUser GraphQL field name.
func connectionField(s forge.Scope) string {
	switch s {
	case forge.ScopeReviewer:
		return "reviewRequestedMergeRequests"
	case forge.ScopeAuthored:
		return "authoredMergeRequests"
	default:
		return "assignedMergeRequests"
	}
}

// mrFields is the shared selection set for a merge-request node.
const mrFields = `
  iid
  title
  draft
  conflicts
  webUrl
  sourceBranch
  targetBranch
  updatedAt
  approved
  approvalsLeft
  detailedMergeStatus
  mergedAt
  project { fullPath }
  author { username }
  approvedBy { nodes { username } }
  reviewers { nodes { username mergeRequestInteraction { reviewState } } }
  headPipeline { id status }
`

// Changes fetches one page of MRs for the given scope. Pass an empty cursor for
// the first page; use the returned EndCursor for subsequent pages. First-page
// results are cached (TTL); pass a forge.WithForceRefresh context to skip.
func (c *Client) Changes(ctx context.Context, scope forge.Scope, cursor string, pageSize int) (*forge.ChangePage, error) {
	if pageSize <= 0 {
		pageSize = 30
	}
	field := connectionField(scope)

	// Only cache the first page (cursor==""); paginated pages are transient.
	cacheKey := string(scope)
	if cursor == "" && !forge.Forced(ctx) {
		if p, ok := c.mrListCache.Get(cacheKey, time.Now()); ok {
			return p, nil
		}
	}

	query := `query($first: Int!, $after: String) {
  currentUser {
    ` + field + `(state: opened, first: $first, after: $after, sort: UPDATED_DESC) {
      pageInfo { hasNextPage endCursor }
      nodes {` + mrFields + `}
    }
  }
}`

	vars := map[string]any{"first": pageSize}
	if cursor != "" {
		vars["after"] = cursor
	}

	var resp struct {
		CurrentUser map[string]mrConnection `json:"currentUser"`
	}

	if err := c.graphQL(ctx, query, vars, &resp); err != nil {
		return nil, err
	}

	page := resp.CurrentUser[field].toPage(c.username)
	if cursor == "" {
		c.mrListCache.Set(cacheKey, page, time.Now())
	}
	return page, nil
}

// mergedScopeField maps a scope to the connection used for *merged* MRs.
func mergedScopeField(s forge.Scope) string {
	if s == forge.ScopeReviewer {
		return "reviewRequestedMergeRequests"
	}
	return "authoredMergeRequests"
}

// MergedSince fetches MRs (for the given scope) merged at or after the given
// RFC3339 timestamp, newest first. Used for the recently-merged section.
func (c *Client) MergedSince(ctx context.Context, scope forge.Scope, since string, max int) ([]forge.Change, error) {
	if max <= 0 {
		max = 30
	}
	field := mergedScopeField(scope)

	cacheKey := "merged:" + string(scope) + ":" + since
	if !forge.Forced(ctx) {
		if p, ok := c.mrListCache.Get(cacheKey, time.Now()); ok {
			return p.Changes, nil
		}
	}

	query := `query($first: Int!, $after: Time!) {
  currentUser {
    ` + field + `(state: merged, mergedAfter: $after, first: $first, sort: MERGED_AT_DESC) {
      pageInfo { hasNextPage endCursor }
      nodes {` + mrFields + `}
    }
  }
}`
	vars := map[string]any{"first": max, "after": since}

	var resp struct {
		CurrentUser map[string]mrConnection `json:"currentUser"`
	}
	if err := c.graphQL(ctx, query, vars, &resp); err != nil {
		return nil, err
	}
	page := resp.CurrentUser[field].toPage(c.username)
	c.mrListCache.Set(cacheKey, page, time.Now())
	return page.Changes, nil
}

// mrConnection mirrors a GraphQL MR connection (pageInfo + nodes).
type mrConnection struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []mrNode `json:"nodes"`
}

type mrNode struct {
	IID                 string `json:"iid"`
	Title               string `json:"title"`
	Draft               bool   `json:"draft"`
	Conflicts           bool   `json:"conflicts"`
	WebURL              string `json:"webUrl"`
	SourceBranch        string `json:"sourceBranch"`
	TargetBranch        string `json:"targetBranch"`
	UpdatedAt           string `json:"updatedAt"`
	MergedAt            string `json:"mergedAt"`
	Approved            bool   `json:"approved"`
	ApprovalsLeft       int    `json:"approvalsLeft"`
	DetailedMergeStatus string `json:"detailedMergeStatus"`
	Project             struct {
		FullPath string `json:"fullPath"`
	} `json:"project"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	ApprovedBy struct {
		Nodes []struct {
			Username string `json:"username"`
		} `json:"nodes"`
	} `json:"approvedBy"`
	Reviewers struct {
		Nodes []struct {
			Username                string `json:"username"`
			MergeRequestInteraction struct {
				ReviewState string `json:"reviewState"`
			} `json:"mergeRequestInteraction"`
		} `json:"nodes"`
	} `json:"reviewers"`
	HeadPipeline *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"headPipeline"`
}

// toPage flattens the connection. me is the authenticated username, used to
// compute ApprovedByMe (pass "" if unknown).
func (conn mrConnection) toPage(me string) *forge.ChangePage {
	page := &forge.ChangePage{
		HasNextPage: conn.PageInfo.HasNextPage,
		EndCursor:   conn.PageInfo.EndCursor,
		Changes:     make([]forge.Change, 0, len(conn.Nodes)),
	}
	for _, n := range conn.Nodes {
		approvedByMe := false
		for _, a := range n.ApprovedBy.Nodes {
			if me != "" && a.Username == me {
				approvedByMe = true
				break
			}
		}
		reviewState := forge.ReviewStateNone
		for _, reviewer := range n.Reviewers.Nodes {
			if me != "" && reviewer.Username == me {
				reviewState = forge.ParseGitLabReviewState(reviewer.MergeRequestInteraction.ReviewState)
				break
			}
		}
		mr := forge.Change{
			ID:            n.IID,
			Repo:          n.Project.FullPath,
			Title:         n.Title,
			Draft:         n.Draft,
			Conflicts:     n.Conflicts,
			WebURL:        n.WebURL,
			SourceBranch:  n.SourceBranch,
			TargetBranch:  n.TargetBranch,
			Author:        n.Author.Username,
			Approved:      n.Approved,
			ApprovedByMe:  approvedByMe,
			ReviewState:   reviewState,
			ApprovalsLeft: n.ApprovalsLeft,
			MergeState:    forge.ParseGitLabMergeState(n.DetailedMergeStatus),
			UpdatedAt:     n.UpdatedAt,
			MergedAt:      n.MergedAt,
		}
		if n.HeadPipeline != nil {
			mr.Pipeline = forge.ParseGitLabStatus(n.HeadPipeline.Status)
			mr.PipelineID = parseGID(n.HeadPipeline.ID)
		}
		page.Changes = append(page.Changes, mr)
	}
	return page
}

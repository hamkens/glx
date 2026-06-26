package gitlab

import (
	"context"
	"time"
)

// Scope selects which set of merge requests to list, mapped onto the
// currentUser GraphQL connections.
type Scope string

const (
	ScopeAssigned Scope = "assigned" // assigned to me
	ScopeReviewer Scope = "reviewer" // review requested from me
	ScopeAuthored Scope = "authored" // I opened it
)

// connectionField maps a Scope to the currentUser GraphQL field name.
func (s Scope) connectionField() string {
	switch s {
	case ScopeReviewer:
		return "reviewRequestedMergeRequests"
	case ScopeAuthored:
		return "authoredMergeRequests"
	default:
		return "assignedMergeRequests"
	}
}

// MR is the flattened merge-request row the TUI renders.
type MR struct {
	IID            string
	Title          string
	Draft          bool
	Conflicts      bool
	WebURL         string
	ProjectPath    string
	SourceBranch   string
	TargetBranch   string
	Author         string
	Pipeline       string // GraphQL pipeline status, or "" if none
	Approved       bool   // fully approved (requirements met)
	ApprovedByMe   bool   // the authenticated user is among the approvers
	ApprovalsLeft  int
	DetailedStatus string // detailedMergeStatus, e.g. MERGEABLE, NEED_REBASE
	PipelineID     int    // head pipeline numeric ID, 0 if none
	UpdatedAt      string
	MergedAt       string // RFC3339, empty unless this MR is merged
}

// MRPage is one page of results plus the cursor to fetch the next.
type MRPage struct {
	MRs         []MR
	HasNextPage bool
	EndCursor   string
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
  headPipeline { id status }
`

// MergeRequests fetches one page of MRs for the given scope. Pass an empty
// cursor for the first page; use the returned EndCursor for subsequent pages.
// First-page results are cached (TTL); pass a WithForceRefresh context to skip.
func (c *Client) MergeRequests(ctx context.Context, scope Scope, cursor string, pageSize int) (*MRPage, error) {
	if pageSize <= 0 {
		pageSize = 30
	}
	field := scope.connectionField()

	// Only cache the first page (cursor==""); paginated pages are transient.
	cacheKey := string(scope)
	if cursor == "" && !forced(ctx) {
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

// mergedScopeField maps a Scope to the connection used for *merged* MRs.
func mergedScopeField(s Scope) string {
	if s == ScopeReviewer {
		return "reviewRequestedMergeRequests"
	}
	return "authoredMergeRequests"
}

// MergedSince fetches MRs (for the given scope) merged at or after the given
// RFC3339 timestamp, newest first. Used for the recently-merged section.
func (c *Client) MergedSince(ctx context.Context, scope Scope, since string, max int) ([]MR, error) {
	if max <= 0 {
		max = 30
	}
	field := mergedScopeField(scope)

	cacheKey := "merged:" + string(scope) + ":" + since
	if !forced(ctx) {
		if p, ok := c.mrListCache.Get(cacheKey, time.Now()); ok {
			return p.MRs, nil
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
	return page.MRs, nil
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
	HeadPipeline *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"headPipeline"`
}

// toPage flattens the connection. me is the authenticated username, used to
// compute ApprovedByMe (pass "" if unknown).
func (conn mrConnection) toPage(me string) *MRPage {
	page := &MRPage{
		HasNextPage: conn.PageInfo.HasNextPage,
		EndCursor:   conn.PageInfo.EndCursor,
		MRs:         make([]MR, 0, len(conn.Nodes)),
	}
	for _, n := range conn.Nodes {
		approvedByMe := false
		for _, a := range n.ApprovedBy.Nodes {
			if me != "" && a.Username == me {
				approvedByMe = true
				break
			}
		}
		mr := MR{
			IID:            n.IID,
			Title:          n.Title,
			Draft:          n.Draft,
			Conflicts:      n.Conflicts,
			WebURL:         n.WebURL,
			ProjectPath:    n.Project.FullPath,
			SourceBranch:   n.SourceBranch,
			TargetBranch:   n.TargetBranch,
			Author:         n.Author.Username,
			Approved:       n.Approved,
			ApprovedByMe:   approvedByMe,
			ApprovalsLeft:  n.ApprovalsLeft,
			DetailedStatus: n.DetailedMergeStatus,
			UpdatedAt:      n.UpdatedAt,
			MergedAt:       n.MergedAt,
		}
		if n.HeadPipeline != nil {
			mr.Pipeline = n.HeadPipeline.Status
			mr.PipelineID = parseGID(n.HeadPipeline.ID)
		}
		page.MRs = append(page.MRs, mr)
	}
	return page
}

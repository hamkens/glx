package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hamkens/glx/internal/forge"
)

// notArchived excludes pull requests in archived repositories from every list.
// An archived repo is read-only: its PRs can never be approved, merged or
// re-run, so they are pure noise in a work queue — and they accumulate fast,
// since archiving a repo leaves all its open PRs open forever. Filtering here
// rather than after the fetch keeps pagination honest: dropping rows client-side
// would return short pages and make "load more" skip real work.
const notArchived = "archived:false"

// searchQualifier maps a scope to the GitHub search qualifier that selects it.
// GitHub has no per-scope connection like GitLab's currentUser fields, so all
// three scopes go through the issue search API.
func searchQualifier(s forge.Scope) string {
	switch s {
	case forge.ScopeReviewer:
		return "review-requested:@me"
	case forge.ScopeAuthored:
		return "author:@me"
	default:
		return "assignee:@me"
	}
}

// prFields is the shared selection set for a pull-request node. It deliberately
// front-loads everything the list view needs (review decision, merge state,
// check rollup, head workflow run) so one query fills a whole page of rows.
const prFields = `
  number
  title
  isDraft
  url
  headRefName
  baseRefName
  updatedAt
  mergedAt
  mergeable
  mergeStateStatus
  reviewDecision
  repository { nameWithOwner }
  author { login }
  latestReviews(first: 50) { nodes { state author { login } } }
  reviewRequests(first: 50) {
    nodes { requestedReviewer { ... on User { login } } }
  }
  commits(last: 1) {
    nodes {
      commit {
        statusCheckRollup {
          state
          contexts(first: 50) {
            nodes {
              __typename
              ... on CheckRun {
                status
                conclusion
                checkSuite { workflowRun { databaseId } }
              }
              ... on StatusContext { state }
            }
          }
        }
      }
    }
  }
`

// Changes fetches one page of open PRs for the given scope. Pass an empty cursor
// for the first page; use the returned EndCursor for subsequent pages. First-page
// results are cached (TTL); pass a forge.WithForceRefresh context to skip.
func (c *Client) Changes(ctx context.Context, scope forge.Scope, cursor string, pageSize int) (*forge.ChangePage, error) {
	if pageSize <= 0 {
		pageSize = 30
	}

	// Only cache the first page (cursor==""); paginated pages are transient.
	cacheKey := string(scope)
	if cursor == "" && !forge.Forced(ctx) {
		if p, ok := c.prListCache.Get(cacheKey, time.Now()); ok {
			return p, nil
		}
	}

	q := "is:pr is:open sort:updated-desc " + notArchived + " " + searchQualifier(scope)
	page, err := c.search(ctx, q, cursor, pageSize)
	if err != nil {
		return nil, err
	}
	if cursor == "" {
		c.prListCache.Set(cacheKey, page, time.Now())
	}
	return page, nil
}

// MergedSince fetches PRs (for the given scope) merged at or after the given
// RFC3339 timestamp, newest first. Used for the recently-merged section.
func (c *Client) MergedSince(ctx context.Context, scope forge.Scope, since string, max int) ([]forge.Change, error) {
	if max <= 0 {
		max = 30
	}

	cacheKey := "merged:" + string(scope) + ":" + since
	if !forge.Forced(ctx) {
		if p, ok := c.prListCache.Get(cacheKey, time.Now()); ok {
			return p.Changes, nil
		}
	}

	// GitHub's search index has no "was requested to review, now merged" view
	// worth showing, so the reviewer scope falls back to PRs the user reviewed.
	qualifier := "author:@me"
	if scope == forge.ScopeReviewer {
		qualifier = "reviewed-by:@me"
	}
	q := fmt.Sprintf("is:pr is:merged sort:updated-desc merged:>=%s %s %s",
		searchTime(since), notArchived, qualifier)

	page, err := c.search(ctx, q, "", max)
	if err != nil {
		return nil, err
	}
	c.prListCache.Set(cacheKey, page, time.Now())
	return page.Changes, nil
}

// searchTime renders an RFC3339 timestamp for a GitHub search date qualifier,
// which rejects the fractional seconds some callers pass.
func searchTime(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05Z")
	}
	return ts
}

// search runs one page of the shared PR search query.
func (c *Client) search(ctx context.Context, searchQuery, cursor string, pageSize int) (*forge.ChangePage, error) {
	const query = `query($q: String!, $first: Int!, $after: String) {
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest {` + prFields + `} }
  }
}`

	vars := map[string]any{"q": searchQuery, "first": pageSize}
	if cursor != "" {
		vars["after"] = cursor
	}

	var resp struct {
		Search prConnection `json:"search"`
	}
	if err := c.graphQL(ctx, query, vars, &resp); err != nil {
		return nil, err
	}
	return resp.Search.toPage(c.username), nil
}

// prConnection mirrors a GraphQL search connection (pageInfo + nodes).
type prConnection struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []prNode `json:"nodes"`
}

type prNode struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	IsDraft          bool   `json:"isDraft"`
	URL              string `json:"url"`
	HeadRefName      string `json:"headRefName"`
	BaseRefName      string `json:"baseRefName"`
	UpdatedAt        string `json:"updatedAt"`
	MergedAt         string `json:"mergedAt"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	Repository       struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	LatestReviews struct {
		Nodes []struct {
			State  string `json:"state"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"latestReviews"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *rollup `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// rollup is the combined check/status state for the PR's head commit. The list
// and detail queries request the same shape so both can share the fold below.
type rollup struct {
	State    string `json:"state"`
	Contexts struct {
		Nodes []rollupContext `json:"nodes"`
	} `json:"contexts"`
}

// rollupContext is one entry in the rollup: an Actions CheckRun or a legacy
// commit StatusContext (CircleCI and friends report the latter).
type rollupContext struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	CheckSuite struct {
		WorkflowRun *struct {
			DatabaseID int64 `json:"databaseId"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
}

// status normalizes one context, whichever kind it is.
func (rc rollupContext) status() forge.Status {
	if rc.Typename == "CheckRun" {
		return forge.ParseGitHubStatus(rc.Status, rc.Conclusion)
	}
	return parseCommitStatusState(rc.State)
}

// runID is the Actions workflow run behind this context, or 0 for a commit
// status (CircleCI etc.), which glx cannot drill into.
func (rc rollupContext) runID() int64 {
	if rc.CheckSuite.WorkflowRun == nil {
		return 0
	}
	return rc.CheckSuite.WorkflowRun.DatabaseID
}

// fold reduces a rollup to the status glx displays plus the workflow run to open
// on "p". A PR usually spans several runs, so the run reported is the one behind
// the context that *determined* the status — drilling into the failing run, not
// whichever check the API happened to list first. Runs are only available for
// Actions; a PR checked solely by an external CI reports 0.
func (r *rollup) fold() (forge.Status, int64) {
	if r == nil {
		return forge.StatusNone, 0
	}
	worst, runID, fallback := forge.StatusNone, int64(0), int64(0)
	for _, rc := range r.Contexts.Nodes {
		s := rc.status()
		id := rc.runID()
		if fallback == 0 {
			fallback = id
		}
		if next := worseStatus(worst, s); next != worst {
			worst = next
			runID = id
		}
	}
	if worst == forge.StatusNone {
		// No contexts came back (truncated, or a rollup with only a summary).
		worst = parseCommitStatusState(r.State)
	}
	if runID == 0 {
		runID = fallback
	}
	return worst, runID
}

// toPage flattens the connection. me is the authenticated login, used to compute
// the viewer's own review state (pass "" if unknown).
func (conn prConnection) toPage(me string) *forge.ChangePage {
	page := &forge.ChangePage{
		HasNextPage: conn.PageInfo.HasNextPage,
		EndCursor:   conn.PageInfo.EndCursor,
		Changes:     make([]forge.Change, 0, len(conn.Nodes)),
	}
	for _, n := range conn.Nodes {
		// Search over type ISSUE returns issues too; those decode as zero nodes.
		if n.Number == 0 {
			continue
		}
		page.Changes = append(page.Changes, n.toChange(me))
	}
	return page
}

func (n prNode) toChange(me string) forge.Change {
	myReview := forge.ReviewStateNone
	approvedByMe := false
	for _, r := range n.LatestReviews.Nodes {
		if me == "" || r.Author.Login != me {
			continue
		}
		myReview = forge.ParseGitHubReviewState(r.State)
		approvedByMe = myReview == forge.ReviewStateApproved
		break
	}
	if myReview == forge.ReviewStateNone && me != "" {
		for _, r := range n.ReviewRequests.Nodes {
			if r.RequestedReviewer.Login == me {
				myReview = forge.ReviewStateRequested
				break
			}
		}
	}

	status, pipelineID := n.pipeline()

	// GitHub's reviewDecision is the only signal for "approvals satisfied"; it is
	// empty on repos with no review requirement, which counts as satisfied.
	decision := strings.ToUpper(n.ReviewDecision)
	approved := decision == "APPROVED" || decision == ""
	approvalsLeft := 0
	if decision == "REVIEW_REQUIRED" {
		approvalsLeft = 1
	}

	return forge.Change{
		ID:            fmt.Sprint(n.Number),
		Repo:          n.Repository.NameWithOwner,
		Title:         n.Title,
		Draft:         n.IsDraft,
		Conflicts:     strings.EqualFold(n.Mergeable, "CONFLICTING"),
		WebURL:        n.URL,
		SourceBranch:  n.HeadRefName,
		TargetBranch:  n.BaseRefName,
		Author:        n.Author.Login,
		Pipeline:      status,
		PipelineID:    pipelineID,
		Approved:      approved,
		ApprovedByMe:  approvedByMe,
		ReviewState:   myReview,
		ApprovalsLeft: approvalsLeft,
		MergeState: forge.ParseGitHubMergeState(
			n.MergeStateStatus,
			n.IsDraft,
			decision == "REVIEW_REQUIRED",
			decision == "CHANGES_REQUESTED",
		),
		UpdatedAt: n.UpdatedAt,
		MergedAt:  n.MergedAt,
	}
}

// pipeline folds the head commit's check rollup into one status plus the
// workflow-run id to drill into. A PR's checks can come from several workflow
// runs; the first run seen is the one the pipeline view opens.
func (n prNode) pipeline() (forge.Status, int64) {
	if len(n.Commits.Nodes) == 0 {
		return forge.StatusNone, 0
	}
	return n.Commits.Nodes[0].Commit.StatusCheckRollup.fold()
}

// parseCommitStatusState normalizes GraphQL's StatusState enum, used by legacy
// commit statuses and by the rollup summary.
func parseCommitStatusState(s string) forge.Status {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "SUCCESS":
		return forge.StatusSuccess
	case "FAILURE", "ERROR":
		return forge.StatusFailed
	case "PENDING":
		return forge.StatusPending
	case "EXPECTED":
		return forge.StatusPending
	default:
		return forge.StatusNone
	}
}

// worseStatus reduces many check states to the single one worth showing on a
// list row, keeping the same precedence GitLab applies to a pipeline: anything
// still in flight outranks a finished failure, so the row keeps polling until
// the whole rollup settles.
func worseStatus(a, b forge.Status) forge.Status {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// rank orders statuses by how much they should dominate a rollup.
func rank(s forge.Status) int {
	switch s {
	case forge.StatusNone:
		return 0
	case forge.StatusSkipped:
		return 1
	case forge.StatusSuccess:
		return 2
	case forge.StatusCanceled:
		return 3
	case forge.StatusManual:
		return 4
	case forge.StatusUnknown:
		return 5
	case forge.StatusFailed:
		return 6
	case forge.StatusPending:
		return 7
	case forge.StatusRunning:
		return 8
	default:
		return 0
	}
}

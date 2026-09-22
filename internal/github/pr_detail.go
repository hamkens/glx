package github

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hamkens/glx/internal/forge"
)

// ChangeDetail fetches the full detail for one PR via GraphQL. Results are
// cached (TTL); pass a forge.WithForceRefresh context to skip the cache.
//
// GitHub splits a PR's conversation across three streams — issue comments,
// review bodies, and review threads — so they are merged here into the single
// ordered discussion list the detail view renders.
func (c *Client) ChangeDetail(ctx context.Context, repo, number string) (*forge.ChangeDetail, error) {
	cacheKey := repo + "#" + number
	if !forge.Forced(ctx) {
		if d, ok := c.prDetailCache.Get(cacheKey, time.Now()); ok {
			return d, nil
		}
	}

	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	num, err := numberInt(number)
	if err != nil {
		return nil, err
	}

	const query = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title isDraft state url body
      headRefName baseRefName
      mergeable mergeStateStatus reviewDecision
      author { login }
      comments(first: 50) {
        nodes { author { login } body createdAt }
      }
      reviews(first: 50) {
        nodes {
          id state body createdAt author { login }
        }
      }
      reviewThreads(first: 50) {
        nodes {
          id isResolved
          comments(first: 20) {
            nodes { author { login } body createdAt path }
          }
        }
      }
      latestReviews(first: 50) { nodes { state author { login } } }
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup {
              state
              contexts(first: 50) {
                nodes {
                  __typename
                  ... on CheckRun {
                    name status conclusion
                    checkSuite { workflowRun { databaseId } }
                  }
                  ... on StatusContext { context state }
                }
              }
            }
          }
        }
      }
    }
  }
}`

	vars := map[string]any{"owner": owner, "name": name, "number": num}

	var resp struct {
		Repository struct {
			PullRequest *prDetailNode `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := c.graphQL(ctx, query, vars, &resp); err != nil {
		return nil, err
	}
	pr := resp.Repository.PullRequest
	if pr == nil {
		return nil, notFound(repo, number)
	}

	decision := strings.ToUpper(pr.ReviewDecision)
	d := &forge.ChangeDetail{
		ID:           number,
		Repo:         repo,
		Title:        pr.Title,
		Draft:        pr.IsDraft,
		State:        forge.ParseGitHubState(githubState(pr.State), strings.EqualFold(pr.State, "MERGED")),
		Description:  pr.Body,
		WebURL:       pr.URL,
		SourceBranch: pr.HeadRefName,
		TargetBranch: pr.BaseRefName,
		Author:       pr.Author.Login,
		MergeState: forge.ParseGitHubMergeState(
			pr.MergeStateStatus,
			pr.IsDraft,
			decision == "REVIEW_REQUIRED",
			decision == "CHANGES_REQUESTED",
		),
		// mergeStateStatus BEHIND is the only "your branch is stale" signal
		// GitHub gives; there is no separate flag as on GitLab.
		NeedsUpdate: strings.EqualFold(pr.MergeStateStatus, "BEHIND"),
		Approved:    decision == "APPROVED" || decision == "",
	}
	if decision == "REVIEW_REQUIRED" {
		d.ApprovalsRequired = 1
		d.ApprovalsLeft = 1
	}

	for _, r := range pr.LatestReviews.Nodes {
		if !strings.EqualFold(r.State, "APPROVED") {
			continue
		}
		d.ApprovedBy = append(d.ApprovedBy, r.Author.Login)
		if c.username != "" && r.Author.Login == c.username {
			d.ApprovedByMe = true
		}
	}

	d.Pipeline, d.PipelineID = pr.pipeline()
	d.PipelineLabel = pr.checkSummary()
	d.Discussions = pr.discussions()

	c.prDetailCache.Set(cacheKey, d, time.Now())
	return d, nil
}

// githubState maps GraphQL's PullRequestState ("OPEN"/"CLOSED"/"MERGED") onto
// the REST vocabulary ParseGitHubState expects.
func githubState(s string) string {
	if strings.EqualFold(s, "MERGED") {
		return "closed"
	}
	return strings.ToLower(s)
}

type prDetailNode struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	IsDraft          bool   `json:"isDraft"`
	State            string `json:"state"`
	URL              string `json:"url"`
	Body             string `json:"body"`
	HeadRefName      string `json:"headRefName"`
	BaseRefName      string `json:"baseRefName"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	Author           struct {
		Login string `json:"login"`
	} `json:"author"`
	Comments struct {
		Nodes []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body      string `json:"body"`
			CreatedAt string `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		Nodes []struct {
			ID        string `json:"id"`
			State     string `json:"state"`
			Body      string `json:"body"`
			CreatedAt string `json:"createdAt"`
			Author    struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		Nodes []struct {
			ID         string `json:"id"`
			IsResolved bool   `json:"isResolved"`
			Comments   struct {
				Nodes []struct {
					Author struct {
						Login string `json:"login"`
					} `json:"author"`
					Body      string `json:"body"`
					CreatedAt string `json:"createdAt"`
					Path      string `json:"path"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	LatestReviews struct {
		Nodes []struct {
			State  string `json:"state"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"latestReviews"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *rollup `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// pipeline folds the head commit's checks into one status plus the workflow-run
// id to drill into.
func (n prDetailNode) pipeline() (forge.Status, int64) {
	return n.rollup().fold()
}

// checkSummary is the human-readable label the detail header shows next to the
// status glyph. GitLab supplies one; on GitHub it is derived from the counts.
func (n prDetailNode) checkSummary() string {
	r := n.rollup()
	if r == nil {
		return ""
	}
	var total, done, failed int
	for _, ctxNode := range r.Contexts.Nodes {
		s := ctxNode.status()
		total++
		if s.Done() {
			done++
		}
		if s == forge.StatusFailed {
			failed++
		}
	}
	switch {
	case total == 0:
		return ""
	case failed > 0:
		return fmt.Sprintf("%d of %d checks failing", failed, total)
	case done < total:
		return fmt.Sprintf("%d of %d checks complete", done, total)
	default:
		return fmt.Sprintf("%d checks passed", total)
	}
}

func (n prDetailNode) rollup() *rollup {
	if len(n.Commits.Nodes) == 0 {
		return nil
	}
	return n.Commits.Nodes[0].Commit.StatusCheckRollup
}

// discussions merges GitHub's three conversation streams into one chronological
// list. Issue comments and review bodies become single-note threads (GitHub does
// not track resolution on them); review threads keep their resolved flag and all
// their replies.
func (n prDetailNode) discussions() []forge.Discussion {
	out := make([]forge.Discussion, 0,
		len(n.Comments.Nodes)+len(n.Reviews.Nodes)+len(n.ReviewThreads.Nodes))

	for i, cm := range n.Comments.Nodes {
		out = append(out, forge.Discussion{
			ID: fmt.Sprintf("comment-%d", i),
			Notes: []forge.Note{{
				Author:    cm.Author.Login,
				Body:      cm.Body,
				CreatedAt: cm.CreatedAt,
			}},
		})
	}

	for _, rv := range n.Reviews.Nodes {
		// A review with no body is just the verdict; render it as a system note
		// so the timeline still shows "approved" / "requested changes".
		body := rv.Body
		system := false
		if strings.TrimSpace(body) == "" {
			body = reviewVerdict(rv.State)
			system = true
			if body == "" {
				continue
			}
		}
		out = append(out, forge.Discussion{
			ID: rv.ID,
			Notes: []forge.Note{{
				Author:    rv.Author.Login,
				Body:      body,
				System:    system,
				CreatedAt: rv.CreatedAt,
			}},
		})
	}

	for _, th := range n.ReviewThreads.Nodes {
		d := forge.Discussion{
			ID: th.ID,
			// Every GitHub review thread carries a resolved flag — unlike
			// GitLab, where only some discussions are resolvable — so the UI
			// always shows the resolved/unresolved tag for these.
			Resolvable: true,
			Resolved:   th.IsResolved,
		}
		for i, cm := range th.Comments.Nodes {
			body := cm.Body
			// Prefix the first note with its file so an inline thread reads in
			// context, the way GitLab's diff notes already do.
			if i == 0 && cm.Path != "" {
				body = "`" + cm.Path + "`\n\n" + body
			}
			d.Notes = append(d.Notes, forge.Note{
				Author:    cm.Author.Login,
				Body:      body,
				CreatedAt: cm.CreatedAt,
			})
		}
		if len(d.Notes) == 0 {
			continue
		}
		out = append(out, d)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return firstCreatedAt(out[i]) < firstCreatedAt(out[j])
	})
	return out
}

func firstCreatedAt(d forge.Discussion) string {
	if len(d.Notes) == 0 {
		return ""
	}
	return d.Notes[0].CreatedAt
}

// reviewVerdict renders a bodiless review as the action it recorded.
func reviewVerdict(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "APPROVED":
		return "approved this pull request"
	case "CHANGES_REQUESTED":
		return "requested changes"
	case "DISMISSED":
		return "review dismissed"
	default:
		return ""
	}
}

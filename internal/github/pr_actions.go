package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/hamkens/glx/internal/forge"
)

// Writes go over REST where GitHub exposes them there. Auto-merge, the merge
// queue and the draft toggle are GraphQL-only, so those use the GraphQL backend.

// Approve records the authenticated user's approval as an APPROVE review.
func (c *Client) Approve(ctx context.Context, repo, number string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	num, err := numberInt(number)
	if err != nil {
		return err
	}
	review := &gogithub.PullRequestReviewRequest{Event: gogithub.Ptr("APPROVE")}
	_, _, err = c.rest.PullRequests.CreateReview(ctx, owner, name, num, review)
	if err != nil {
		// GitHub refuses to let an author approve their own PR; say so plainly
		// rather than surfacing the raw 422.
		if isUnprocessable(err) {
			return fmt.Errorf("GitHub rejected the approval (you cannot approve your own pull request): %w", err)
		}
		return err
	}
	c.invalidatePR(repo, number)
	return nil
}

// Unapprove withdraws the user's approval by dismissing their own most recent
// approving review, which is the closest GitHub equivalent. GitHub keeps the
// dismissed review in the timeline rather than deleting it.
func (c *Client) Unapprove(ctx context.Context, repo, number string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	num, err := numberInt(number)
	if err != nil {
		return err
	}
	if c.username == "" {
		return fmt.Errorf("cannot dismiss an approval before the current user is known")
	}

	reviewID, err := c.latestOwnApproval(ctx, owner, name, num)
	if err != nil {
		return err
	}
	if reviewID == 0 {
		return fmt.Errorf("you have no active approval on this pull request")
	}

	req := &gogithub.PullRequestReviewDismissalRequest{
		Message: gogithub.Ptr("Approval withdrawn via glx"),
	}
	_, _, err = c.rest.PullRequests.DismissReview(ctx, owner, name, num, reviewID, req)
	if err != nil {
		// Dismissing needs write access to the repo, not just the review.
		if isForbidden(err) {
			return fmt.Errorf("dismissing a review requires write access to %s: %w", repo, err)
		}
		return err
	}
	c.invalidatePR(repo, number)
	return nil
}

// latestOwnApproval returns the id of the user's most recent approving review,
// or 0 if their latest review is not an approval.
func (c *Client) latestOwnApproval(ctx context.Context, owner, name string, num int) (int64, error) {
	opts := &gogithub.ListOptions{PerPage: 100, Page: 1}
	var reviewID int64
	for {
		reviews, resp, err := c.rest.PullRequests.ListReviews(ctx, owner, name, num, opts)
		if err != nil {
			return 0, err
		}
		for _, r := range reviews {
			if r.GetUser().GetLogin() != c.username {
				continue
			}
			// Reviews come back oldest-first, so the last match wins. A later
			// CHANGES_REQUESTED or DISMISSED supersedes an earlier approval.
			switch strings.ToUpper(r.GetState()) {
			case "APPROVED":
				reviewID = r.GetID()
			case "CHANGES_REQUESTED", "DISMISSED":
				reviewID = 0
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return reviewID, nil
}

// Merge merges the PR. When autoMerge is set, GitHub is asked to merge it once
// the required checks pass; on a repo with a merge queue that same request
// enqueues it. A plain merge that branch protection routes through the queue is
// retried as an enqueue so the action still does what the user asked.
func (c *Client) Merge(ctx context.Context, repo, number string, autoMerge bool) (forge.MergeOutcome, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.MergeOutcomeMerged, err
	}
	num, err := numberInt(number)
	if err != nil {
		return forge.MergeOutcomeMerged, err
	}

	if autoMerge {
		outcome, err := c.enableAutoMerge(ctx, repo, number)
		if err != nil {
			return outcome, err
		}
		c.invalidatePR(repo, number)
		return outcome, nil
	}

	_, resp, err := c.rest.PullRequests.Merge(ctx, owner, name, num, "", nil)
	if err == nil {
		c.invalidatePR(repo, number)
		return forge.MergeOutcomeMerged, nil
	}

	// A repo with a required merge queue rejects a direct merge; enqueue instead.
	if mentionsMergeQueue(err) {
		if qErr := c.enqueue(ctx, repo, number); qErr != nil {
			return forge.MergeOutcomeTrain, qErr
		}
		c.invalidatePR(repo, number)
		return forge.MergeOutcomeTrain, nil
	}
	// GitHub returns 405 when the PR isn't in a mergeable state (checks running,
	// reviews missing, conflicts, draft). Translate the opaque status.
	if resp != nil && resp.StatusCode == http.StatusMethodNotAllowed {
		return forge.MergeOutcomeMerged, fmt.Errorf("pull request is not in a mergeable state (checks, reviews, conflicts, or draft)")
	}
	return forge.MergeOutcomeMerged, err
}

// enableAutoMerge asks GitHub to merge the PR once its requirements are met.
// This is GraphQL-only. On a merge-queue repo the same mutation enqueues the PR,
// which the outcome reflects.
func (c *Client) enableAutoMerge(ctx context.Context, repo, number string) (forge.MergeOutcome, error) {
	id, err := c.prNodeID(ctx, repo, number)
	if err != nil {
		return forge.MergeOutcomeAutoMerge, err
	}

	const mutation = `mutation($id: ID!) {
  enablePullRequestAutoMerge(input: {pullRequestId: $id}) {
    pullRequest { number state }
  }
}`
	if err := c.graphQL(ctx, mutation, map[string]any{"id": id}, nil); err != nil {
		// Auto-merge is a per-repository setting and off by default.
		if strings.Contains(strings.ToLower(err.Error()), "auto merge") ||
			strings.Contains(strings.ToLower(err.Error()), "auto-merge") {
			return forge.MergeOutcomeAutoMerge, fmt.Errorf("auto-merge is not enabled for %s (enable it in the repository settings): %w", repo, err)
		}
		return forge.MergeOutcomeAutoMerge, err
	}
	return forge.MergeOutcomeAutoMerge, nil
}

// enqueue adds the PR to the repository's merge queue.
func (c *Client) enqueue(ctx context.Context, repo, number string) error {
	id, err := c.prNodeID(ctx, repo, number)
	if err != nil {
		return err
	}
	const mutation = `mutation($id: ID!) {
  enqueuePullRequest(input: {pullRequestId: $id}) {
    mergeQueueEntry { position }
  }
}`
	if err := c.graphQL(ctx, mutation, map[string]any{"id": id}, nil); err != nil {
		return fmt.Errorf("add pull request to merge queue: %w", err)
	}
	return nil
}

// UpdateBranch merges the target branch into the PR's branch, GitHub's
// equivalent of GitLab's rebase. The update runs asynchronously server-side, so
// GitHub's 202 Accepted is a success here rather than an error.
func (c *Client) UpdateBranch(ctx context.Context, repo, number string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	num, err := numberInt(number)
	if err != nil {
		return err
	}
	// GitHub answers 202 Accepted here (the update runs server-side), which
	// go-github reports as an error; that is success.
	_, _, err = c.rest.PullRequests.UpdateBranch(ctx, owner, name, num, nil)
	if err := ignoreAccepted(err); err != nil {
		return err
	}
	c.invalidatePR(repo, number)
	return nil
}

// SetDraft toggles draft/ready state. Unlike GitLab, GitHub tracks this as a
// first-class flag rather than a title prefix, so currentTitle is unused.
func (c *Client) SetDraft(ctx context.Context, repo, number, currentTitle string, draft bool) error {
	_ = currentTitle

	id, err := c.prNodeID(ctx, repo, number)
	if err != nil {
		return err
	}

	mutation := `mutation($id: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $id}) {
    pullRequest { isDraft }
  }
}`
	if draft {
		mutation = `mutation($id: ID!) {
  convertPullRequestToDraft(input: {pullRequestId: $id}) {
    pullRequest { isDraft }
  }
}`
	}
	if err := c.graphQL(ctx, mutation, map[string]any{"id": id}, nil); err != nil {
		return err
	}
	c.invalidatePR(repo, number)
	return nil
}

// AddComment posts a top-level comment. A PR's conversation tab is an issue
// thread on GitHub, which is why this uses the issues endpoint.
func (c *Client) AddComment(ctx context.Context, repo, number, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	num, err := numberInt(number)
	if err != nil {
		return err
	}
	comment := &gogithub.IssueComment{Body: gogithub.Ptr(body)}
	_, _, err = c.rest.Issues.CreateComment(ctx, owner, name, num, comment)
	if err == nil {
		c.invalidatePR(repo, number)
	}
	return err
}

// mentionsMergeQueue reports whether an error is GitHub refusing a direct merge
// because branch protection requires the merge queue.
func mentionsMergeQueue(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "merge queue")
}

func isUnprocessable(err error) bool { return hasStatus(err, http.StatusUnprocessableEntity) }
func isForbidden(err error) bool     { return hasStatus(err, http.StatusForbidden) }

func hasStatus(err error, code int) bool {
	var resp *gogithub.ErrorResponse
	if errors.As(err, &resp) && resp.Response != nil {
		return resp.Response.StatusCode == code
	}
	return false
}

package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	gogitlab "github.com/xanzy/go-gitlab"
)

// MergeOutcome describes what GitLab did with a successful merge request.
// Projects with merge trains queue an otherwise-ready MR instead of merging
// it directly.
type MergeOutcome int

const (
	MergeOutcomeMerged MergeOutcome = iota
	MergeOutcomeTrain
	MergeOutcomeAutoMerge
)

// These write actions use the REST backend, where the endpoints are stable and
// well documented. The go-gitlab client accepts the project's full path (it
// URL-encodes it internally) and the MR's integer IID.

func iidInt(iid string) (int, error) {
	n, err := strconv.Atoi(iid)
	if err != nil {
		return 0, fmt.Errorf("invalid MR iid %q: %w", iid, err)
	}
	return n, nil
}

// invalidateMR drops cached reads for an MR after a mutating action so the
// next read (and the auto-refetch the TUI triggers) returns fresh state.
func (c *Client) invalidateMR(projectPath, iid string) {
	key := projectPath + "!" + iid
	c.mrDetailCache.Invalidate(key)
	c.mrDiffCache.Invalidate(key)
	// MR list rows (approval/pipeline glyphs) may also change; clear all scopes.
	for _, s := range []Scope{ScopeAssigned, ScopeReviewer, ScopeAuthored} {
		c.mrListCache.Invalidate(string(s))
	}
}

// Approve adds the current user's approval to the MR.
func (c *Client) Approve(ctx context.Context, projectPath, iid string) error {
	n, err := iidInt(iid)
	if err != nil {
		return err
	}
	_, _, err = c.rest.MergeRequestApprovals.ApproveMergeRequest(projectPath, n, nil, gogitlab.WithContext(ctx))
	if err == nil {
		c.invalidateMR(projectPath, iid)
	}
	return err
}

// Unapprove removes the current user's approval from the MR.
func (c *Client) Unapprove(ctx context.Context, projectPath, iid string) error {
	n, err := iidInt(iid)
	if err != nil {
		return err
	}
	_, err = c.rest.MergeRequestApprovals.UnapproveMergeRequest(projectPath, n, gogitlab.WithContext(ctx))
	if err == nil {
		c.invalidateMR(projectPath, iid)
	}
	return err
}

// Merge merges the MR, or adds it to the project's merge train when merge
// trains are enabled. When checks are still running, autoMerge asks GitLab to
// enqueue or merge it once those checks pass.
func (c *Client) Merge(ctx context.Context, projectPath, iid string, autoMerge bool) (MergeOutcome, error) {
	n, err := iidInt(iid)
	if err != nil {
		return MergeOutcomeMerged, err
	}

	project, _, err := c.rest.Projects.GetProject(projectPath, nil, gogitlab.WithContext(ctx))
	if err != nil {
		return MergeOutcomeMerged, fmt.Errorf("check project merge settings: %w", err)
	}
	if project.MergeTrainsEnabled {
		outcome := MergeOutcomeTrain
		if autoMerge {
			outcome = MergeOutcomeAutoMerge
		}
		if err := c.addToMergeTrain(ctx, projectPath, n, autoMerge); err != nil {
			return outcome, err
		}
		c.invalidateMR(projectPath, iid)
		return outcome, nil
	}

	opts := &gogitlab.AcceptMergeRequestOptions{}
	outcome := MergeOutcomeMerged
	if autoMerge {
		opts.MergeWhenPipelineSucceeds = gogitlab.Ptr(true)
		outcome = MergeOutcomeAutoMerge
	}
	_, resp, err := c.rest.MergeRequests.AcceptMergeRequest(projectPath, n, opts, gogitlab.WithContext(ctx))
	if err == nil {
		c.invalidateMR(projectPath, iid)
		return outcome, nil
	}
	// GitLab returns 405 when the MR isn't in a mergeable state (pipeline
	// running, approvals missing, conflicts, draft). Translate the opaque
	// "405 Method Not Allowed" into something actionable.
	if resp != nil && resp.StatusCode == http.StatusMethodNotAllowed {
		return outcome, fmt.Errorf("merge request is not in a mergeable state (pipeline, approvals, conflicts, or draft)")
	}
	return outcome, err
}

type addToMergeTrainOptions struct {
	AutoMerge bool `json:"auto_merge"`
}

func (c *Client) addToMergeTrain(ctx context.Context, projectPath string, iid int, autoMerge bool) error {
	path := fmt.Sprintf("projects/%s/merge_trains/merge_requests/%d", gogitlab.PathEscape(projectPath), iid)
	req, err := c.rest.NewRequest(http.MethodPost, path, &addToMergeTrainOptions{AutoMerge: autoMerge}, []gogitlab.RequestOptionFunc{gogitlab.WithContext(ctx)})
	if err != nil {
		return fmt.Errorf("prepare merge train request: %w", err)
	}
	if _, err := c.rest.Do(req, nil); err != nil {
		return fmt.Errorf("add merge request to merge train: %w", err)
	}
	return nil
}

// SetDraft marks the MR as a draft (draft=true) or ready (draft=false).
// GitLab derives draft state from a "Draft:" title prefix, so this rewrites the
// title accordingly. currentTitle is the MR's present title.
func (c *Client) SetDraft(ctx context.Context, projectPath, iid, currentTitle string, draft bool) error {
	n, err := iidInt(iid)
	if err != nil {
		return err
	}
	newTitle := applyDraftPrefix(currentTitle, draft)
	if newTitle == currentTitle {
		return nil // already in the desired state
	}
	opts := &gogitlab.UpdateMergeRequestOptions{Title: gogitlab.Ptr(newTitle)}
	_, _, err = c.rest.MergeRequests.UpdateMergeRequest(projectPath, n, opts, gogitlab.WithContext(ctx))
	if err == nil {
		c.invalidateMR(projectPath, iid)
	}
	return err
}

// applyDraftPrefix adds or removes GitLab's "Draft:" title prefix. It also
// strips the legacy "WIP:" prefix. Matching is case-insensitive.
func applyDraftPrefix(title string, draft bool) string {
	stripped := title
	for {
		trimmed := strings.TrimSpace(stripped)
		low := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(low, "draft:"):
			stripped = strings.TrimSpace(trimmed[len("draft:"):])
		case strings.HasPrefix(low, "wip:"):
			stripped = strings.TrimSpace(trimmed[len("wip:"):])
		default:
			goto done
		}
	}
done:
	if draft {
		return "Draft: " + stripped
	}
	return stripped
}

// Rebase asks GitLab to rebase the MR's source branch onto its target. The
// rebase runs asynchronously server-side; this call just enqueues it.
func (c *Client) Rebase(ctx context.Context, projectPath, iid string) error {
	n, err := iidInt(iid)
	if err != nil {
		return err
	}
	_, err = c.rest.MergeRequests.RebaseMergeRequest(projectPath, n, nil, gogitlab.WithContext(ctx))
	if err == nil {
		c.invalidateMR(projectPath, iid)
	}
	return err
}

// AddComment posts a top-level comment (note) on the MR.
func (c *Client) AddComment(ctx context.Context, projectPath, iid, body string) error {
	n, err := iidInt(iid)
	if err != nil {
		return err
	}
	opts := &gogitlab.CreateMergeRequestNoteOptions{Body: gogitlab.Ptr(body)}
	_, _, err = c.rest.Notes.CreateMergeRequestNote(projectPath, n, opts, gogitlab.WithContext(ctx))
	if err == nil {
		c.invalidateMR(projectPath, iid)
	}
	return err
}

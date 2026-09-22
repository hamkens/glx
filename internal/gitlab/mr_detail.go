package gitlab

import (
	"context"
	"time"

	"github.com/hamkens/glx/internal/forge"
)

// ChangeDetail fetches the full detail for one MR via GraphQL. Results are
// cached (TTL); pass a forge.WithForceRefresh context to skip the cache.
func (c *Client) ChangeDetail(ctx context.Context, projectPath, iid string) (*forge.ChangeDetail, error) {
	cacheKey := projectPath + "!" + iid
	if !forge.Forced(ctx) {
		if d, ok := c.mrDetailCache.Get(cacheKey, time.Now()); ok {
			return d, nil
		}
	}

	const query = `query($path: ID!, $iid: String!) {
  project(fullPath: $path) {
    mergeRequest(iid: $iid) {
      iid title draft state mergeStatusEnum detailedMergeStatus shouldBeRebased description webUrl
      sourceBranch targetBranch
      approved approvalsLeft approvalsRequired
      author { username }
      headPipeline { id status detailedStatus { label } }
      approvedBy { nodes { username } }
      discussions(first: 50) {
        nodes {
          id resolved resolvable
          notes { nodes { author { username } body system createdAt } }
        }
      }
    }
  }
}`

	vars := map[string]any{"path": projectPath, "iid": iid}

	var resp struct {
		Project struct {
			MergeRequest *struct {
				IID                 string `json:"iid"`
				Title               string `json:"title"`
				Draft               bool   `json:"draft"`
				State               string `json:"state"`
				MergeStatusEnum     string `json:"mergeStatusEnum"`
				DetailedMergeStatus string `json:"detailedMergeStatus"`
				ShouldBeRebased     bool   `json:"shouldBeRebased"`
				Description         string `json:"description"`
				WebURL              string `json:"webUrl"`
				SourceBranch        string `json:"sourceBranch"`
				TargetBranch        string `json:"targetBranch"`
				Approved            bool   `json:"approved"`
				ApprovalsLeft       int    `json:"approvalsLeft"`
				ApprovalsRequired   int    `json:"approvalsRequired"`
				Author              struct {
					Username string `json:"username"`
				} `json:"author"`
				HeadPipeline *struct {
					ID             string `json:"id"`
					Status         string `json:"status"`
					DetailedStatus struct {
						Label string `json:"label"`
					} `json:"detailedStatus"`
				} `json:"headPipeline"`
				ApprovedBy struct {
					Nodes []struct {
						Username string `json:"username"`
					} `json:"nodes"`
				} `json:"approvedBy"`
				Discussions struct {
					Nodes []struct {
						ID         string `json:"id"`
						Resolved   bool   `json:"resolved"`
						Resolvable bool   `json:"resolvable"`
						Notes      struct {
							Nodes []struct {
								Author struct {
									Username string `json:"username"`
								} `json:"author"`
								Body      string `json:"body"`
								System    bool   `json:"system"`
								CreatedAt string `json:"createdAt"`
							} `json:"nodes"`
						} `json:"notes"`
					} `json:"nodes"`
				} `json:"discussions"`
			} `json:"mergeRequest"`
		} `json:"project"`
	}

	if err := c.graphQL(ctx, query, vars, &resp); err != nil {
		return nil, err
	}
	mr := resp.Project.MergeRequest
	if mr == nil {
		return nil, &forge.NotFoundError{Resource: "merge request", ID: projectPath + "!" + iid}
	}

	d := &forge.ChangeDetail{
		ID:                mr.IID,
		Repo:              projectPath,
		Title:             mr.Title,
		Draft:             mr.Draft,
		State:             forge.ParseGitLabState(mr.State),
		MergeState:        mergeState(mr.DetailedMergeStatus, mr.MergeStatusEnum),
		NeedsUpdate:       mr.ShouldBeRebased,
		Description:       mr.Description,
		WebURL:            mr.WebURL,
		SourceBranch:      mr.SourceBranch,
		TargetBranch:      mr.TargetBranch,
		Author:            mr.Author.Username,
		Approved:          mr.Approved,
		ApprovalsRequired: mr.ApprovalsRequired,
		ApprovalsLeft:     mr.ApprovalsLeft,
	}
	if mr.HeadPipeline != nil {
		d.Pipeline = forge.ParseGitLabStatus(mr.HeadPipeline.Status)
		d.PipelineLabel = mr.HeadPipeline.DetailedStatus.Label
		d.PipelineID = parseGID(mr.HeadPipeline.ID)
	}
	for _, n := range mr.ApprovedBy.Nodes {
		d.ApprovedBy = append(d.ApprovedBy, n.Username)
		if c.username != "" && n.Username == c.username {
			d.ApprovedByMe = true
		}
	}
	for _, disc := range mr.Discussions.Nodes {
		dd := forge.Discussion{ID: disc.ID, Resolvable: disc.Resolvable, Resolved: disc.Resolved}
		for _, n := range disc.Notes.Nodes {
			dd.Notes = append(dd.Notes, forge.Note{
				Author:    n.Author.Username,
				Body:      n.Body,
				System:    n.System,
				CreatedAt: n.CreatedAt,
			})
		}
		d.Discussions = append(d.Discussions, dd)
	}

	c.mrDetailCache.Set(cacheKey, d, time.Now())
	return d, nil
}

// mergeState prefers GitLab's detailedMergeStatus, which distinguishes states
// like NEED_REBASE, and falls back to the coarse mergeStatusEnum when the
// instance is too old to report the detailed one.
func mergeState(detailed, coarse string) forge.MergeState {
	if s := forge.ParseGitLabMergeState(detailed); s != forge.MergeStateUnknown {
		return s
	}
	switch coarse {
	case "CAN_BE_MERGED", "MERGEABLE":
		return forge.MergeStateMergeable
	case "CANNOT_BE_MERGED", "BROKEN_STATUS":
		return forge.MergeStateConflict
	case "CHECKING", "UNCHECKED", "CANNOT_BE_MERGED_RECHECK":
		return forge.MergeStateChecking
	default:
		return forge.MergeStateUnknown
	}
}

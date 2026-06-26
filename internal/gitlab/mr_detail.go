package gitlab

import (
	"context"
	"time"
)

// Note is a single comment within a discussion thread.
type Note struct {
	Author    string
	Body      string
	System    bool // GitLab-generated (e.g. "approved this merge request")
	CreatedAt string
}

// Discussion is a thread of notes.
type Discussion struct {
	ID         string
	Resolvable bool
	Resolved   bool
	Notes      []Note
}

// MRDetail is the full view of a single merge request.
type MRDetail struct {
	IID               string
	Title             string
	State             string // opened / merged / closed
	MergeStatus       string // CAN_BE_MERGED, etc. (coarse mergeStatusEnum)
	DetailedStatus    string // detailedMergeStatus, e.g. MERGEABLE, NEED_REBASE
	ShouldBeRebased   bool   // source branch is behind target; needs rebase
	Description       string // raw markdown
	WebURL            string
	SourceBranch      string
	TargetBranch      string
	Author            string
	Pipeline          string
	PipelineLabel     string
	PipelineID        int // head pipeline numeric ID, 0 if none
	Approved          bool
	ApprovalsRequired int
	ApprovalsLeft     int
	ApprovedBy        []string
	Discussions       []Discussion
}

// MergeRequestDetail fetches the full detail for one MR via GraphQL.
// Results are cached (TTL); pass a WithForceRefresh context to skip the cache.
func (c *Client) MergeRequestDetail(ctx context.Context, projectPath, iid string) (*MRDetail, error) {
	cacheKey := projectPath + "!" + iid
	if !forced(ctx) {
		if d, ok := c.mrDetailCache.Get(cacheKey, time.Now()); ok {
			return d, nil
		}
	}

	const query = `query($path: ID!, $iid: String!) {
  project(fullPath: $path) {
    mergeRequest(iid: $iid) {
      iid title state mergeStatusEnum detailedMergeStatus shouldBeRebased description webUrl
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
		return nil, &NotFoundError{Resource: "merge request", ID: projectPath + "!" + iid}
	}

	d := &MRDetail{
		IID:               mr.IID,
		Title:             mr.Title,
		State:             mr.State,
		MergeStatus:       mr.MergeStatusEnum,
		DetailedStatus:    mr.DetailedMergeStatus,
		ShouldBeRebased:   mr.ShouldBeRebased,
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
		d.Pipeline = mr.HeadPipeline.Status
		d.PipelineLabel = mr.HeadPipeline.DetailedStatus.Label
		d.PipelineID = parseGID(mr.HeadPipeline.ID)
	}
	for _, n := range mr.ApprovedBy.Nodes {
		d.ApprovedBy = append(d.ApprovedBy, n.Username)
	}
	for _, disc := range mr.Discussions.Nodes {
		dd := Discussion{ID: disc.ID, Resolvable: disc.Resolvable, Resolved: disc.Resolved}
		for _, n := range disc.Notes.Nodes {
			dd.Notes = append(dd.Notes, Note{
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

// NotFoundError indicates a requested resource does not exist or is not visible.
type NotFoundError struct {
	Resource string
	ID       string
}

func (e *NotFoundError) Error() string {
	return e.Resource + " not found: " + e.ID
}

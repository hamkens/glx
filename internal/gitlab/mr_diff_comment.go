package gitlab

import (
	"context"

	gogitlab "github.com/xanzy/go-gitlab"
)

// DiffComment describes where to anchor an inline comment on a merge request
// diff. Exactly one of NewLine / OldLine is typically set:
//   - NewLine for an added or context line (right side of the diff)
//   - OldLine for a removed line (left side of the diff)
//
// NewPath/OldPath are the file paths from the FileDiff (usually identical
// unless the file was renamed).
type DiffComment struct {
	Refs    DiffRefs
	NewPath string
	OldPath string
	NewLine int // 0 = unset
	OldLine int // 0 = unset
	Body    string
}

// AddDiffComment posts a positioned inline comment as a new discussion on the
// MR diff. This is the fiddly part of the GitLab API: the Position payload must
// carry all three diff SHAs plus the file path and the line on the correct side.
func (c *Client) AddDiffComment(ctx context.Context, projectPath, iid string, dc DiffComment) error {
	n, err := iidInt(iid)
	if err != nil {
		return err
	}

	pos := &gogitlab.PositionOptions{
		PositionType: gogitlab.Ptr("text"),
		BaseSHA:      gogitlab.Ptr(dc.Refs.BaseSHA),
		HeadSHA:      gogitlab.Ptr(dc.Refs.HeadSHA),
		StartSHA:     gogitlab.Ptr(dc.Refs.StartSHA),
		NewPath:      gogitlab.Ptr(dc.NewPath),
		OldPath:      gogitlab.Ptr(orEmpty(dc.OldPath, dc.NewPath)),
	}
	if dc.NewLine > 0 {
		pos.NewLine = gogitlab.Ptr(dc.NewLine)
	}
	if dc.OldLine > 0 {
		pos.OldLine = gogitlab.Ptr(dc.OldLine)
	}

	opts := &gogitlab.CreateMergeRequestDiscussionOptions{
		Body:     gogitlab.Ptr(dc.Body),
		Position: pos,
	}
	_, _, err = c.rest.Discussions.CreateMergeRequestDiscussion(projectPath, n, opts, gogitlab.WithContext(ctx))
	if err == nil {
		c.invalidateMR(projectPath, iid)
	}
	return err
}

func orEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

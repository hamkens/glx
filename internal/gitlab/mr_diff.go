package gitlab

import (
	"context"
	"time"

	gogitlab "github.com/xanzy/go-gitlab"

	"github.com/hamkens/glx/internal/forge"
)

// ChangeDiff fetches the changed files and diff refs for an MR via REST. REST is
// used here (not GraphQL) because the raw unified-diff text and the
// base/head/start SHAs for comment positioning are first-class in the REST API.
func (c *Client) ChangeDiff(ctx context.Context, projectPath, iid string) (*forge.Diff, error) {
	cacheKey := projectPath + "!" + iid
	if !forge.Forced(ctx) {
		if d, ok := c.mrDiffCache.Get(cacheKey, time.Now()); ok {
			return d, nil
		}
	}

	n, err := iidInt(iid)
	if err != nil {
		return nil, err
	}

	// Diff refs live on the MR object.
	mr, _, err := c.rest.MergeRequests.GetMergeRequest(projectPath, n, nil, gogitlab.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	out := &forge.Diff{
		Refs: forge.DiffRefs{
			BaseSHA:  mr.DiffRefs.BaseSha,
			HeadSHA:  mr.DiffRefs.HeadSha,
			StartSHA: mr.DiffRefs.StartSha,
		},
	}

	// Paginate the per-file diffs.
	opts := &gogitlab.ListMergeRequestDiffsOptions{
		ListOptions: gogitlab.ListOptions{PerPage: 50, Page: 1},
	}
	for {
		diffs, resp, err := c.rest.MergeRequests.ListMergeRequestDiffs(projectPath, n, opts, gogitlab.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		for _, d := range diffs {
			out.Files = append(out.Files, forge.FileDiff{
				OldPath:  d.OldPath,
				NewPath:  d.NewPath,
				Diff:     d.Diff,
				NewFile:  d.NewFile,
				Deleted:  d.DeletedFile,
				Renamed:  d.RenamedFile,
				TooLarge: d.Diff == "" && !d.NewFile && !d.DeletedFile,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	c.mrDiffCache.Set(cacheKey, out, time.Now())
	return out, nil
}

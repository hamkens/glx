package github

import (
	"context"
	"time"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/hamkens/glx/internal/forge"
)

// ChangeDiff fetches the changed files and the refs needed to anchor an inline
// comment. REST is used here (not GraphQL) because the per-file unified-diff
// text ("patch") is only exposed by the REST files endpoint.
func (c *Client) ChangeDiff(ctx context.Context, repo, number string) (*forge.Diff, error) {
	cacheKey := repo + "#" + number
	if !forge.Forced(ctx) {
		if d, ok := c.prDiffCache.Get(cacheKey, time.Now()); ok {
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

	// GitHub anchors review comments on the head commit SHA alone; base and
	// start are filled in for parity so the UI can display them.
	pr, _, err := c.rest.PullRequests.Get(ctx, owner, name, num)
	if err != nil {
		return nil, err
	}
	out := &forge.Diff{
		Refs: forge.DiffRefs{
			BaseSHA:  pr.GetBase().GetSHA(),
			HeadSHA:  pr.GetHead().GetSHA(),
			StartSHA: pr.GetBase().GetSHA(),
		},
	}

	opts := &gogithub.ListOptions{PerPage: 100, Page: 1}
	for {
		files, resp, err := c.rest.PullRequests.ListFiles(ctx, owner, name, num, opts)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			out.Files = append(out.Files, fileDiff(f))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	c.prDiffCache.Set(cacheKey, out, time.Now())
	return out, nil
}

// fileDiff maps one changed file. GitHub omits "patch" for binary files and for
// diffs above its inline size limit, which is what TooLarge reports.
func fileDiff(f *gogithub.CommitFile) forge.FileDiff {
	status := f.GetStatus()
	newPath := f.GetFilename()
	oldPath := f.GetPreviousFilename()
	if oldPath == "" {
		oldPath = newPath
	}
	deleted := status == "removed"
	added := status == "added"
	if deleted {
		// For a deletion GitHub still reports the path under "filename".
		oldPath = newPath
	}
	return forge.FileDiff{
		OldPath:  oldPath,
		NewPath:  newPath,
		Diff:     f.GetPatch(),
		NewFile:  added,
		Deleted:  deleted,
		Renamed:  status == "renamed",
		TooLarge: f.GetPatch() == "" && !added && !deleted,
	}
}

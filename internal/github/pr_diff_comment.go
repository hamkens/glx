package github

import (
	"context"
	"fmt"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/hamkens/glx/internal/forge"
)

// AddDiffComment posts a positioned inline comment as a new review thread.
//
// GitHub anchors a comment with (commit_id, path, line, side) rather than
// GitLab's three-SHA position payload: "RIGHT" addresses the post-image (an
// added or context line), "LEFT" the pre-image (a removed line).
func (c *Client) AddDiffComment(ctx context.Context, repo, number string, dc forge.DiffComment) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	num, err := numberInt(number)
	if err != nil {
		return err
	}
	if dc.Refs.HeadSHA == "" {
		return fmt.Errorf("inline comment needs the head commit SHA")
	}

	comment := &gogithub.PullRequestComment{
		Body:     gogithub.Ptr(dc.Body),
		CommitID: gogithub.Ptr(dc.Refs.HeadSHA),
	}
	switch {
	case dc.NewLine > 0:
		comment.Path = gogithub.Ptr(dc.NewPath)
		comment.Line = gogithub.Ptr(dc.NewLine)
		comment.Side = gogithub.Ptr("RIGHT")
	case dc.OldLine > 0:
		path := dc.OldPath
		if path == "" {
			path = dc.NewPath
		}
		comment.Path = gogithub.Ptr(path)
		comment.Line = gogithub.Ptr(dc.OldLine)
		comment.Side = gogithub.Ptr("LEFT")
	default:
		// No line at all: comment on the file as a whole.
		comment.Path = gogithub.Ptr(dc.NewPath)
		comment.SubjectType = gogithub.Ptr("file")
	}

	_, _, err = c.rest.PullRequests.CreateComment(ctx, owner, name, num, comment)
	if err == nil {
		c.invalidatePR(repo, number)
	}
	return err
}

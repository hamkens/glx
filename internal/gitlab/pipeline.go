package gitlab

import (
	"context"
	"io"
	"strconv"
	"strings"

	gogitlab "github.com/xanzy/go-gitlab"

	"github.com/hamkens/glx/internal/forge"
)

// parseGID extracts the trailing numeric ID from a GraphQL global id such as
// "gid://gitlab/Ci::Pipeline/1472672". Returns 0 if not parseable.
func parseGID(gid string) int64 {
	if gid == "" {
		return 0
	}
	i := strings.LastIndexByte(gid, '/')
	if i < 0 {
		return 0
	}
	n, _ := strconv.ParseInt(gid[i+1:], 10, 64)
	return n
}

// PipelineWithJobs fetches a pipeline and all of its jobs via REST.
func (c *Client) PipelineWithJobs(ctx context.Context, projectPath string, pipelineID int64) (*forge.Pipeline, error) {
	p, _, err := c.rest.Pipelines.GetPipeline(projectPath, int(pipelineID), gogitlab.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	out := &forge.Pipeline{
		ID:     int64(p.ID),
		Status: forge.ParseGitLabStatus(p.Status),
		Ref:    p.Ref,
		SHA:    p.SHA,
		WebURL: p.WebURL,
	}

	opts := &gogitlab.ListJobsOptions{
		ListOptions: gogitlab.ListOptions{PerPage: 100, Page: 1},
	}
	for {
		jobs, resp, err := c.rest.Jobs.ListPipelineJobs(projectPath, int(pipelineID), opts, gogitlab.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		for _, j := range jobs {
			out.Jobs = append(out.Jobs, forge.Job{
				ID:           int64(j.ID),
				Name:         j.Name,
				Stage:        j.Stage,
				Status:       forge.ParseGitLabStatus(j.Status),
				Duration:     j.Duration,
				AllowFailure: j.AllowFailure,
				WebURL:       j.WebURL,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// JobLog fetches the raw (ANSI-coded) log for a job.
func (c *Client) JobLog(ctx context.Context, projectPath string, jobID int64) (string, error) {
	reader, _, err := c.rest.Jobs.GetTraceFile(projectPath, int(jobID), gogitlab.WithContext(ctx))
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RetryJob retries a single job.
func (c *Client) RetryJob(ctx context.Context, projectPath string, jobID int64) error {
	_, _, err := c.rest.Jobs.RetryJob(projectPath, int(jobID), gogitlab.WithContext(ctx))
	return err
}

// CancelJob cancels a running or pending job.
func (c *Client) CancelJob(ctx context.Context, projectPath string, jobID int64) error {
	_, _, err := c.rest.Jobs.CancelJob(projectPath, int(jobID), gogitlab.WithContext(ctx))
	return err
}

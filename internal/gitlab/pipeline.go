package gitlab

import (
	"context"
	"io"
	"strconv"
	"strings"

	gogitlab "github.com/xanzy/go-gitlab"
)

// Job is one CI job within a pipeline.
type Job struct {
	ID           int
	Name         string
	Stage        string
	Status       string  // success, failed, running, created, manual, canceled, skipped, pending
	Duration     float64 // seconds; 0 if not started/finished
	AllowFailure bool
	WebURL       string
}

// Pipeline is a CI pipeline with its jobs grouped-ready (ordered as returned).
type Pipeline struct {
	ID     int
	Status string
	Ref    string
	SHA    string
	WebURL string
	Jobs   []Job
}

// parseGID extracts the trailing numeric ID from a GraphQL global id such as
// "gid://gitlab/Ci::Pipeline/1472672". Returns 0 if not parseable.
func parseGID(gid string) int {
	if gid == "" {
		return 0
	}
	i := strings.LastIndexByte(gid, '/')
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(gid[i+1:])
	return n
}

// PipelineWithJobs fetches a pipeline and all of its jobs via REST.
func (c *Client) PipelineWithJobs(ctx context.Context, projectPath string, pipelineID int) (*Pipeline, error) {
	p, _, err := c.rest.Pipelines.GetPipeline(projectPath, pipelineID, gogitlab.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	out := &Pipeline{
		ID:     p.ID,
		Status: p.Status,
		Ref:    p.Ref,
		SHA:    p.SHA,
		WebURL: p.WebURL,
	}

	opts := &gogitlab.ListJobsOptions{
		ListOptions: gogitlab.ListOptions{PerPage: 100, Page: 1},
	}
	for {
		jobs, resp, err := c.rest.Jobs.ListPipelineJobs(projectPath, pipelineID, opts, gogitlab.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		for _, j := range jobs {
			out.Jobs = append(out.Jobs, Job{
				ID:           j.ID,
				Name:         j.Name,
				Stage:        j.Stage,
				Status:       j.Status,
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

// JobTrace fetches the raw (ANSI-coded) log for a job.
func (c *Client) JobTrace(ctx context.Context, projectPath string, jobID int) (string, error) {
	reader, _, err := c.rest.Jobs.GetTraceFile(projectPath, jobID, gogitlab.WithContext(ctx))
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RetryJob retries a single job and returns the new job's ID.
func (c *Client) RetryJob(ctx context.Context, projectPath string, jobID int) error {
	_, _, err := c.rest.Jobs.RetryJob(projectPath, jobID, gogitlab.WithContext(ctx))
	return err
}

// CancelJob cancels a running or pending job.
func (c *Client) CancelJob(ctx context.Context, projectPath string, jobID int) error {
	_, _, err := c.rest.Jobs.CancelJob(projectPath, jobID, gogitlab.WithContext(ctx))
	return err
}

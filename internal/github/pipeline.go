package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/hamkens/glx/internal/forge"
)

// PipelineWithJobs fetches an Actions workflow run and all of its jobs. A run is
// glx's "pipeline"; GitHub has no stage concept, so each job reports the
// workflow's name in the stage column.
func (c *Client) PipelineWithJobs(ctx context.Context, repo string, runID int64) (*forge.Pipeline, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	run, _, err := c.rest.Actions.GetWorkflowRunByID(ctx, owner, name, runID)
	if err != nil {
		return nil, err
	}
	out := &forge.Pipeline{
		ID:     run.GetID(),
		Status: forge.ParseGitHubStatus(run.GetStatus(), run.GetConclusion()),
		Ref:    run.GetHeadBranch(),
		SHA:    run.GetHeadSHA(),
		WebURL: run.GetHTMLURL(),
	}

	opts := &gogithub.ListWorkflowJobsOptions{
		ListOptions: gogithub.ListOptions{PerPage: 100, Page: 1},
	}
	for {
		jobs, resp, err := c.rest.Actions.ListWorkflowJobs(ctx, owner, name, runID, opts)
		if err != nil {
			return nil, err
		}
		for _, j := range jobs.Jobs {
			out.Jobs = append(out.Jobs, forge.Job{
				ID:       j.GetID(),
				Name:     j.GetName(),
				Stage:    j.GetWorkflowName(),
				Status:   forge.ParseGitHubStatus(j.GetStatus(), j.GetConclusion()),
				Duration: jobDuration(j),
				// GitHub's continue-on-error lives in the workflow file, not the
				// run payload, so a job never reports itself as allowed to fail.
				AllowFailure: false,
				WebURL:       j.GetHTMLURL(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// jobDuration is the job's wall-clock seconds, or 0 if it has not started. A
// running job is measured against now, matching how GitLab reports one.
func jobDuration(j *gogithub.WorkflowJob) float64 {
	start := j.GetStartedAt().Time
	if start.IsZero() {
		return 0
	}
	end := j.GetCompletedAt().Time
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(start).Seconds()
	if d < 0 {
		return 0
	}
	return d
}

// JobLog fetches a job's log. GitHub answers the logs endpoint with a redirect
// to a short-lived blob URL rather than the bytes themselves, so this resolves
// the URL and then downloads it.
func (c *Client) JobLog(ctx context.Context, repo string, jobID int64) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}

	logURL, _, err := c.rest.Actions.GetWorkflowJobLogs(ctx, owner, name, jobID, 1)
	if err != nil {
		return "", err
	}
	if logURL == nil {
		return "", fmt.Errorf("no log available for job %d", jobID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("download job log: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download job log: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RetryJob re-runs a single job (and anything that depends on it).
func (c *Client) RetryJob(ctx context.Context, repo string, jobID int64) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	_, err = c.rest.Actions.RerunJobByID(ctx, owner, name, jobID)
	return ignoreAccepted(err)
}

// CancelJob cancels the workflow run the job belongs to: GitHub has no per-job
// cancel, which Capabilities reports as CancelJobIsRunWide so the UI can warn.
func (c *Client) CancelJob(ctx context.Context, repo string, jobID int64) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	job, _, err := c.rest.Actions.GetWorkflowJobByID(ctx, owner, name, jobID)
	if err != nil {
		return err
	}
	_, err = c.rest.Actions.CancelWorkflowRunByID(ctx, owner, name, job.GetRunID())
	return ignoreAccepted(err)
}

// ignoreAccepted swallows the error go-github returns for GitHub's 202 Accepted,
// which means "queued server-side" rather than "failed". The Actions endpoints
// that mutate a run all answer that way.
func ignoreAccepted(err error) error {
	var accepted *gogithub.AcceptedError
	if errors.As(err, &accepted) {
		return nil
	}
	return err
}

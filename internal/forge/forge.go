// Package forge defines the provider-neutral contract the TUI consumes, so the
// same views work against GitLab merge requests and GitHub pull requests.
//
// The domain types here are deliberately named after the concept both hosts
// share ("change" = merge request / pull request) rather than either vendor's
// term. Provider-specific wording is resolved for display via Vocabulary, and
// provider-specific state strings are normalized to the enums in status.go, so
// no TUI code branches on which backend is active.
package forge

import "context"

// Provider identifies which hosting product a Forge talks to.
type Provider string

const (
	ProviderGitLab Provider = "gitlab"
	ProviderGitHub Provider = "github"
	// ProviderMixed is reported by a Fleet spanning both products. It selects
	// neutral vocabulary for fleet-wide chrome; per-row wording comes from the
	// owning host instead.
	ProviderMixed Provider = "mixed"
)

// Scope selects which set of changes to list, relative to the authenticated
// user. Every provider maps these onto its own query mechanism.
type Scope string

const (
	ScopeAssigned Scope = "assigned" // assigned to me
	ScopeReviewer Scope = "reviewer" // review requested from me
	ScopeAuthored Scope = "authored" // I opened it
)

// Forge is one authenticated connection to one host. Implementations are
// expected to be safe for concurrent use by the TUI's background commands.
//
// Read methods may serve results from a TTL cache; a context carrying
// WithForceRefresh must bypass and repopulate that cache.
type Forge interface {
	// --- identity ---

	// Provider reports which backend this is, for vocabulary and capability
	// decisions in the UI.
	Provider() Provider

	// Host is the host name this Forge talks to (e.g. "github.com").
	Host() string

	// CurrentUser authenticates and returns the login of the authenticated
	// account plus a human-readable version string for the instance.
	CurrentUser(ctx context.Context) (username, version string, err error)

	// Username returns the authenticated login, or "" before CurrentUser runs.
	Username() string

	// Capabilities reports which optional actions this backend supports, so the
	// UI can hide or explain what isn't available.
	Capabilities() Capabilities

	// --- reads ---

	// Changes fetches one page of open changes for a scope. Pass an empty
	// cursor for the first page and the returned EndCursor for later pages.
	Changes(ctx context.Context, scope Scope, cursor string, pageSize int) (*ChangePage, error)

	// MergedSince returns changes in the scope merged at or after an RFC3339
	// timestamp, newest first.
	MergedSince(ctx context.Context, scope Scope, since string, max int) ([]Change, error)

	// ChangeDetail fetches the full view of one change. repo is the provider's
	// full path ("group/sub/project" or "owner/repo"); id is the user-facing
	// number as a string (GitLab iid, GitHub PR number).
	ChangeDetail(ctx context.Context, repo, id string) (*ChangeDetail, error)

	// ChangeDiff fetches the changed files plus the refs needed to anchor an
	// inline comment on them.
	ChangeDiff(ctx context.Context, repo, id string) (*Diff, error)

	// PipelineWithJobs fetches one CI pipeline (GitLab pipeline / GitHub
	// Actions workflow run) and all of its jobs.
	PipelineWithJobs(ctx context.Context, repo string, pipelineID int64) (*Pipeline, error)

	// JobLog fetches a job's log, which may contain ANSI color codes.
	JobLog(ctx context.Context, repo string, jobID int64) (string, error)

	// --- writes ---

	// Approve records the authenticated user's approval.
	Approve(ctx context.Context, repo, id string) error

	// Unapprove withdraws it. On GitHub this dismisses the user's own approving
	// review, which requires the review to still be the current one.
	Unapprove(ctx context.Context, repo, id string) error

	// Merge merges the change. When autoMerge is true and checks are still
	// pending, the provider is asked to merge once they pass instead of failing.
	// The returned MergeOutcome says what actually happened.
	Merge(ctx context.Context, repo, id string, autoMerge bool) (MergeOutcome, error)

	// UpdateBranch brings the source branch up to date with its target: a
	// rebase on GitLab, an update-branch merge on GitHub.
	UpdateBranch(ctx context.Context, repo, id string) error

	// SetDraft toggles draft/ready state. currentTitle is the change's present
	// title, which GitLab needs because it derives draft state from the title.
	SetDraft(ctx context.Context, repo, id, currentTitle string, draft bool) error

	// AddComment posts a top-level comment.
	AddComment(ctx context.Context, repo, id, body string) error

	// AddDiffComment posts a comment anchored to a line of the diff.
	AddDiffComment(ctx context.Context, repo, id string, dc DiffComment) error

	// RetryJob re-runs a single job.
	RetryJob(ctx context.Context, repo string, jobID int64) error

	// CancelJob cancels a job. GitHub has no per-job cancel, so its
	// implementation cancels the whole run; Capabilities reports this.
	CancelJob(ctx context.Context, repo string, jobID int64) error

	// --- URLs ---

	// DiffURL is the web URL for a change's diff/files tab.
	DiffURL(repo, id string) string
}

// Capabilities describes optional behaviour that differs between providers, so
// the UI can adapt instead of surfacing an API error.
type Capabilities struct {
	// AutoMerge reports whether "merge when checks pass" is available. On
	// GitHub this requires the repository to have auto-merge enabled, so a
	// true value here means "supported", not "guaranteed to succeed".
	AutoMerge bool

	// Unapprove reports whether a recorded approval can be withdrawn.
	Unapprove bool

	// CancelJobIsRunWide is true when canceling a job necessarily cancels the
	// entire pipeline/run (GitHub Actions), so the UI can warn first.
	CancelJobIsRunWide bool

	// DraftToggle reports whether draft/ready can be toggled. GitHub only
	// allows this via GraphQL, and converting to draft needs write access.
	DraftToggle bool
}

// MergeOutcome describes what the provider did with an accepted merge request.
type MergeOutcome int

const (
	// MergeOutcomeMerged means the change was merged immediately.
	MergeOutcomeMerged MergeOutcome = iota
	// MergeOutcomeTrain means it was queued on a merge train / merge queue.
	MergeOutcomeTrain
	// MergeOutcomeAutoMerge means it will merge once checks pass.
	MergeOutcomeAutoMerge
)

// forceRefreshKey marks a context as bypassing (and refilling) read caches.
type forceRefreshKey struct{}

// WithForceRefresh returns a context that skips cached reads, so the next read
// hits the API and repopulates the cache. This backs the "r" refresh key.
func WithForceRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceRefreshKey{}, true)
}

// Forced reports whether ctx was marked by WithForceRefresh. Backends call this
// to decide whether to consult their cache.
func Forced(ctx context.Context) bool {
	v, _ := ctx.Value(forceRefreshKey{}).(bool)
	return v
}

// NotFoundError indicates a requested resource does not exist or is not visible
// to the authenticated user.
type NotFoundError struct {
	Resource string
	ID       string
}

func (e *NotFoundError) Error() string {
	return e.Resource + " not found: " + e.ID
}

// UnsupportedError reports that a provider cannot perform an action at all, as
// opposed to refusing it for the current state. The UI shows this verbatim.
type UnsupportedError struct {
	Action   string
	Provider Provider
	Reason   string
}

func (e *UnsupportedError) Error() string {
	msg := e.Action + " is not supported on " + string(e.Provider)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

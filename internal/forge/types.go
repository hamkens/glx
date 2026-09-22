package forge

// Change is the flattened list row the TUI renders: a merge request on GitLab,
// a pull request on GitHub.
type Change struct {
	// ID is the user-facing number as a string (GitLab iid / GitHub number).
	ID string
	// Repo is the provider's full path: "group/sub/project" or "owner/repo".
	Repo string
	// Host names the host this change came from. Backends leave it empty — it is
	// stamped on by whatever aggregates several hosts into one list, so a row can
	// be routed back to the connection that produced it.
	Host string

	Title        string
	Draft        bool
	Conflicts    bool
	WebURL       string
	SourceBranch string
	TargetBranch string
	Author       string

	// Pipeline is the head pipeline's normalized status, or StatusNone when the
	// change has no CI attached.
	Pipeline Status
	// PipelineID identifies the head pipeline (GitLab pipeline / GitHub Actions
	// run) for drill-down, or 0 if there is none.
	PipelineID int64

	// Approved reports that the change's approval requirements are satisfied.
	Approved bool
	// ApprovedByMe reports that the authenticated user is among the approvers.
	ApprovedByMe bool
	// ReviewState is the authenticated user's own review state.
	ReviewState ReviewState
	// ApprovalsLeft is how many more approvals are required. GitHub does not
	// expose a count for this, so it is 0 or 1 there (1 = review required).
	ApprovalsLeft int

	// MergeState is why the change can or cannot merge right now.
	MergeState MergeState

	// UpdatedAt is RFC3339. MergedAt is RFC3339 and empty unless merged.
	UpdatedAt string
	MergedAt  string
}

// ChangePage is one page of results plus the cursor for the next.
type ChangePage struct {
	Changes     []Change
	HasNextPage bool
	EndCursor   string
}

// ChangeDetail is the full view of a single change.
type ChangeDetail struct {
	ID    string
	Repo  string
	Title string
	Draft bool

	// State is the lifecycle state, normalized across providers.
	State State
	// MergeState is why it can or cannot merge right now.
	MergeState MergeState
	// NeedsUpdate reports that the source branch is behind its target and
	// should be rebased (GitLab) or updated (GitHub).
	NeedsUpdate bool

	Description  string // raw markdown
	WebURL       string
	SourceBranch string
	TargetBranch string
	Author       string

	Pipeline Status
	// PipelineLabel is an optional provider-supplied description of the
	// pipeline status ("passed", "3 of 5 checks failed"), or "".
	PipelineLabel string
	PipelineID    int64

	Approved          bool
	ApprovedByMe      bool
	ApprovalsRequired int
	ApprovalsLeft     int
	ApprovedBy        []string

	Discussions []Discussion
}

// Discussion is a thread of notes: a GitLab discussion, or a GitHub review /
// issue-comment thread.
type Discussion struct {
	ID string
	// Resolvable reports that the thread tracks a resolved flag at all.
	Resolvable bool
	Resolved   bool
	Notes      []Note
}

// Note is a single comment within a thread.
type Note struct {
	Author string
	Body   string
	// System marks provider-generated notes (e.g. "approved this merge
	// request"), which the UI renders dimmed.
	System    bool
	CreatedAt string // RFC3339
}

// FileDiff is one changed file.
type FileDiff struct {
	OldPath string
	NewPath string
	// Diff is the unified diff body (@@ hunks), empty when unavailable.
	Diff    string
	NewFile bool
	Deleted bool
	Renamed bool
	// TooLarge reports that the provider omitted the diff body because the
	// change is too big to inline.
	TooLarge bool
}

// DiffRefs are the SHAs needed to position an inline comment on a diff. GitHub
// only needs HeadSHA; GitLab needs all three.
type DiffRefs struct {
	BaseSHA  string
	HeadSHA  string
	StartSHA string
}

// Diff bundles the changed files with the refs needed to comment on them.
type Diff struct {
	Files []FileDiff
	Refs  DiffRefs
}

// DiffComment describes where to anchor an inline comment. Exactly one of
// NewLine / OldLine is normally set:
//   - NewLine for an added or context line (right side of the diff)
//   - OldLine for a removed line (left side)
//
// NewPath/OldPath come from the FileDiff and are usually identical unless the
// file was renamed.
type DiffComment struct {
	Refs    DiffRefs
	NewPath string
	OldPath string
	NewLine int // 0 = unset
	OldLine int // 0 = unset
	Body    string
}

// Job is one CI job: a GitLab job, or a GitHub Actions workflow-run job.
type Job struct {
	ID     int64
	Name   string
	Stage  string // GitLab stage; on GitHub, the workflow name
	Status Status
	// Duration is in seconds; 0 when not started or not finished.
	Duration     float64
	AllowFailure bool
	WebURL       string
}

// Pipeline is one CI run with its jobs in display order.
type Pipeline struct {
	ID     int64
	Status Status
	Ref    string
	SHA    string
	WebURL string
	Jobs   []Job
}

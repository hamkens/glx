package forge

import "strings"

// Status is a CI status normalized across providers, so glyph and
// polling logic in the TUI never has to know which backend produced it.
//
// GitLab reports one status per pipeline/job. GitHub Actions splits this into a
// status ("queued", "in_progress", "completed") plus a conclusion ("success",
// "failure", …); ParseGitHubStatus folds the pair back into one value.
type Status int

const (
	// StatusNone means there is no pipeline/CI attached at all.
	StatusNone Status = iota
	// StatusPending covers everything queued but not yet executing.
	StatusPending
	StatusRunning
	StatusSuccess
	StatusFailed
	StatusCanceled
	StatusSkipped
	// StatusManual is a job awaiting a human to trigger or approve it.
	StatusManual
	// StatusUnknown is a value the provider reported that we don't model.
	StatusUnknown
)

// String returns a lowercase label suitable for display.
func (s Status) String() string {
	switch s {
	case StatusNone:
		return ""
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusSuccess:
		return "success"
	case StatusFailed:
		return "failed"
	case StatusCanceled:
		return "canceled"
	case StatusSkipped:
		return "skipped"
	case StatusManual:
		return "manual"
	default:
		return "unknown"
	}
}

// Active reports whether the status represents work still in progress, and so
// is worth polling for. Manual is not active: it waits on a human, not on CI.
func (s Status) Active() bool {
	return s == StatusPending || s == StatusRunning
}

// Done reports whether the status is terminal.
func (s Status) Done() bool {
	switch s {
	case StatusSuccess, StatusFailed, StatusCanceled, StatusSkipped:
		return true
	default:
		return false
	}
}

// ParseGitLabStatus normalizes a GitLab pipeline or job status. It accepts both
// the uppercase GraphQL enum ("SUCCESS") and the lowercase REST value
// ("success"), since glx reads pipelines over both backends.
func ParseGitLabStatus(s string) Status {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return StatusNone
	case "created", "pending", "preparing", "scheduled", "waiting_for_resource":
		return StatusPending
	case "running":
		return StatusRunning
	case "success", "passed":
		return StatusSuccess
	case "failed":
		return StatusFailed
	case "canceled", "cancelled", "canceling":
		return StatusCanceled
	case "skipped":
		return StatusSkipped
	case "manual":
		return StatusManual
	default:
		return StatusUnknown
	}
}

// ParseGitHubStatus folds an Actions status/conclusion pair into one Status.
// The conclusion is only meaningful once the status is "completed", so it takes
// precedence when present and the run is finished.
func ParseGitHubStatus(status, conclusion string) Status {
	st := strings.ToLower(strings.TrimSpace(status))
	cn := strings.ToLower(strings.TrimSpace(conclusion))

	// A conclusion is set exactly when the run/job has finished.
	if cn != "" {
		switch cn {
		case "success":
			return StatusSuccess
		case "failure", "timed_out", "startup_failure":
			return StatusFailed
		case "cancelled", "canceled":
			return StatusCanceled
		case "skipped", "stale", "neutral":
			return StatusSkipped
		case "action_required":
			return StatusManual
		default:
			return StatusUnknown
		}
	}

	switch st {
	case "":
		return StatusNone
	case "queued", "requested", "pending":
		return StatusPending
	case "waiting":
		// A run held for a deployment gate or manual approval.
		return StatusManual
	case "in_progress":
		return StatusRunning
	case "completed":
		// Completed with no conclusion shouldn't happen, but don't claim success.
		return StatusUnknown
	default:
		return StatusUnknown
	}
}

// State is a change's lifecycle state.
type State int

const (
	StateUnknown State = iota
	StateOpen
	StateMerged
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateMerged:
		return "merged"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// ParseGitLabState normalizes GitLab's state ("opened"/"merged"/"closed"),
// accepting either case.
func ParseGitLabState(s string) State {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "opened", "open":
		return StateOpen
	case "merged":
		return StateMerged
	case "closed", "locked":
		return StateClosed
	default:
		return StateUnknown
	}
}

// ParseGitHubState normalizes a GitHub PR state. GitHub reports a merged PR as
// state "closed" with merged=true, so merged is passed separately.
func ParseGitHubState(state string, merged bool) State {
	if merged {
		return StateMerged
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open":
		return StateOpen
	case "closed":
		return StateClosed
	default:
		return StateUnknown
	}
}

// MergeState says whether a change can merge, and if not, why. It is the
// normalized form of GitLab's detailedMergeStatus and GitHub's mergeable_state.
type MergeState int

const (
	// MergeStateUnknown means the provider didn't say, or said something we
	// don't model. The UI treats it as "no known blocker".
	MergeStateUnknown MergeState = iota
	// MergeStateChecking means the provider is still computing mergeability.
	MergeStateChecking
	MergeStateMergeable
	// MergeStateNeedsUpdate means the source branch is behind its target.
	MergeStateNeedsUpdate
	MergeStateConflict
	MergeStateCIRunning
	MergeStateCIFailed
	MergeStateDraft
	MergeStateThreadsUnresolved
	MergeStateNotApproved
	MergeStateChangesRequested
	// MergeStateBlocked is a provider-side block we can't attribute further
	// (a dependent MR, an external status check, a protected-branch rule).
	MergeStateBlocked
	// MergeStateNotOpen means the change is already merged or closed.
	MergeStateNotOpen
)

// Mergeable reports whether this state is free of merge blockers.
func (m MergeState) Mergeable() bool {
	return m == MergeStateMergeable || m == MergeStateUnknown
}

// CIPending reports whether the only obstacle is CI that hasn't finished. These
// are the states auto-merge exists to wait out.
func (m MergeState) CIPending() bool {
	return m == MergeStateCIRunning || m == MergeStateCIFailed
}

func (m MergeState) String() string {
	switch m {
	case MergeStateChecking:
		return "checking mergeability"
	case MergeStateMergeable:
		return "mergeable"
	case MergeStateNeedsUpdate:
		return "behind target branch"
	case MergeStateConflict:
		return "conflicts"
	case MergeStateCIRunning:
		return "checks running"
	case MergeStateCIFailed:
		return "checks failing"
	case MergeStateDraft:
		return "draft"
	case MergeStateThreadsUnresolved:
		return "unresolved threads"
	case MergeStateNotApproved:
		return "approvals missing"
	case MergeStateChangesRequested:
		return "changes requested"
	case MergeStateBlocked:
		return "blocked"
	case MergeStateNotOpen:
		return "not open"
	default:
		return ""
	}
}

// ParseGitLabMergeState normalizes GitLab's detailedMergeStatus enum.
func ParseGitLabMergeState(s string) MergeState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "":
		return MergeStateUnknown
	case "MERGEABLE":
		return MergeStateMergeable
	case "CHECKING", "UNCHECKED", "PREPARING", "APPROVALS_SYNCING":
		return MergeStateChecking
	case "NEED_REBASE":
		return MergeStateNeedsUpdate
	case "CONFLICT", "BROKEN_STATUS":
		return MergeStateConflict
	case "CI_STILL_RUNNING":
		return MergeStateCIRunning
	case "CI_MUST_PASS":
		return MergeStateCIFailed
	case "DRAFT_STATUS":
		return MergeStateDraft
	case "DISCUSSIONS_NOT_RESOLVED":
		return MergeStateThreadsUnresolved
	case "NOT_APPROVED":
		return MergeStateNotApproved
	case "REQUESTED_CHANGES":
		return MergeStateChangesRequested
	case "NOT_OPEN":
		return MergeStateNotOpen
	case "BLOCKED_STATUS", "EXTERNAL_STATUS_CHECKS", "JIRA_ASSOCIATION_MISSING",
		"POLICIES_DENIED", "SECURITY_POLICY_VIOLATIONS", "NEED_REBASE_OR_MERGE":
		return MergeStateBlocked
	default:
		return MergeStateUnknown
	}
}

// ParseGitHubMergeState normalizes GitHub's mergeable_state. That field is
// coarse: "blocked" covers both a missing review and a failing required check,
// and "unstable" means a non-required check is failing (still mergeable). The
// caller passes what it knows about reviews so the ambiguity can be resolved:
// reviewRequired when a required review is outstanding, changesRequested when a
// reviewer asked for changes.
func ParseGitHubMergeState(state string, draft, reviewRequired, changesRequested bool) MergeState {
	// Draft dominates: GitHub reports draft PRs as mergeable_state "draft",
	// but a draft can also come back "clean" right after creation.
	if draft {
		return MergeStateDraft
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "clean", "has_hooks":
		return MergeStateMergeable
	case "unstable":
		// Only non-required checks are failing, so this can still be merged.
		return MergeStateMergeable
	case "dirty":
		return MergeStateConflict
	case "behind":
		return MergeStateNeedsUpdate
	case "draft":
		return MergeStateDraft
	case "blocked":
		if changesRequested {
			return MergeStateChangesRequested
		}
		if reviewRequired {
			return MergeStateNotApproved
		}
		// Otherwise a required check or a branch-protection rule is in the way.
		return MergeStateBlocked
	case "unknown", "":
		return MergeStateChecking
	default:
		return MergeStateUnknown
	}
}

// ReviewState is the authenticated user's own review state on a change.
type ReviewState int

const (
	// ReviewStateNone means the user is not a reviewer, or hasn't been asked.
	ReviewStateNone ReviewState = iota
	// ReviewStateRequested means a review is requested and not yet given.
	ReviewStateRequested
	// ReviewStateReviewed means the user commented without approving.
	ReviewStateReviewed
	ReviewStateApproved
	ReviewStateChangesRequested
)

func (r ReviewState) String() string {
	switch r {
	case ReviewStateRequested:
		return "requested"
	case ReviewStateReviewed:
		return "reviewed"
	case ReviewStateApproved:
		return "approved"
	case ReviewStateChangesRequested:
		return "changes requested"
	default:
		return ""
	}
}

// Pending reports whether the user still owes a review.
func (r ReviewState) Pending() bool { return r == ReviewStateRequested }

// ParseGitLabReviewState normalizes GitLab's reviewState enum.
func ParseGitLabReviewState(s string) ReviewState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "REQUESTED", "UNREVIEWED", "UNAPPROVED":
		return ReviewStateRequested
	case "REVIEWED":
		return ReviewStateReviewed
	case "APPROVED":
		return ReviewStateApproved
	case "REQUESTED_CHANGES":
		return ReviewStateChangesRequested
	default:
		return ReviewStateNone
	}
}

// ParseGitHubReviewState normalizes a GitHub review state ("APPROVED",
// "CHANGES_REQUESTED", "COMMENTED", "DISMISSED", "PENDING").
func ParseGitHubReviewState(s string) ReviewState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "APPROVED":
		return ReviewStateApproved
	case "CHANGES_REQUESTED":
		return ReviewStateChangesRequested
	case "COMMENTED":
		return ReviewStateReviewed
	default:
		// PENDING (an unsubmitted draft review) and DISMISSED both leave the
		// review outstanding.
		return ReviewStateNone
	}
}

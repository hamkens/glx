package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/hamkens/glx/internal/forge"
)

func TestSummarizeReviewerScope(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mrs := []forge.Change{
		{ReviewState: forge.ReviewStateRequested, UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{ReviewState: forge.ReviewStateRequested, UpdatedAt: now.Add(-72 * time.Hour).Format(time.RFC3339)},
		{ReviewState: forge.ReviewStateRequested, Draft: true, UpdatedAt: now.Add(-96 * time.Hour).Format(time.RFC3339)},
		{ReviewState: forge.ReviewStateReviewed, UpdatedAt: now.Add(-120 * time.Hour).Format(time.RFC3339)},
		{ReviewState: forge.ReviewStateApproved, ApprovedByMe: true, UpdatedAt: now.Add(-144 * time.Hour).Format(time.RFC3339)},
	}

	got := summarizeScope(forge.ScopeReviewer, mrs, now)
	if got.pending != 2 {
		t.Fatalf("pending = %d, want 2", got.pending)
	}
	if got.stale != 1 {
		t.Fatalf("stale = %d, want 1", got.stale)
	}
	if got.oldest != 72*time.Hour {
		t.Fatalf("oldest = %s, want 72h", got.oldest)
	}
}

func TestSummarizeAuthoredScope(t *testing.T) {
	mrs := []forge.Change{
		{MergeState: forge.MergeStateMergeable, Pipeline: forge.StatusSuccess},
		{MergeState: forge.MergeStateNotApproved, ApprovalsLeft: 2},
		{MergeState: forge.MergeStateNeedsUpdate},
		{MergeState: forge.MergeStateConflict, Conflicts: true},
		{MergeState: forge.MergeStateCIRunning, Pipeline: forge.StatusRunning},
		{MergeState: forge.MergeStateThreadsUnresolved},
		{MergeState: forge.MergeStateNotApproved, Draft: true, Pipeline: forge.StatusFailed},
	}

	got := summarizeScope(forge.ScopeAuthored, mrs, time.Now())
	if got.ready != 1 || got.blocked != 6 {
		t.Fatalf("ready/blocked = %d/%d, want 1/6", got.ready, got.blocked)
	}
	if got.approvals != 2 || got.ciFailed != 1 || got.ciRunning != 1 {
		t.Fatalf("approval/failed/running = %d/%d/%d, want 2/1/1",
			got.approvals, got.ciFailed, got.ciRunning)
	}
	if got.behind != 1 || got.conflicts != 1 || got.threads != 1 || got.drafts != 1 {
		t.Fatalf("behind/conflict/thread/draft = %d/%d/%d/%d, want 1/1/1/1",
			got.behind, got.conflicts, got.threads, got.drafts)
	}
}

func TestReadyToMergeRejectsListBlockers(t *testing.T) {
	if !readyToMerge(forge.Change{MergeState: forge.MergeStateMergeable}) {
		t.Fatal("mergeable change should be ready")
	}
	if readyToMerge(forge.Change{MergeState: forge.MergeStateMergeable, Draft: true}) {
		t.Fatal("draft change should not be ready")
	}
	if readyToMerge(forge.Change{MergeState: forge.MergeStateMergeable, ApprovalsLeft: 1}) {
		t.Fatal("change needing approval should not be ready")
	}
}

// A GitHub PR with no known merge state must not be summarized as blocked just
// because the provider hasn't reported one: MergeStateUnknown means "no known
// blocker", which is what Mergeable() encodes.
func TestReadyToMergeAcceptsUnknownMergeState(t *testing.T) {
	if !readyToMerge(forge.Change{MergeState: forge.MergeStateUnknown}) {
		t.Fatal("change with an unknown merge state should be ready")
	}
}

// listMergeBlock wording follows the provider: GitLab says "rebase", GitHub
// says "update branch".
func TestListMergeBlockUsesProviderWording(t *testing.T) {
	behind := forge.Change{MergeState: forge.MergeStateNeedsUpdate}
	if got := listMergeBlock(behind, forge.Vocab(forge.ProviderGitLab)); got == "" ||
		!strings.Contains(got, "rebase") {
		t.Errorf("GitLab wording = %q, want it to mention rebase", got)
	}
	if got := listMergeBlock(behind, forge.Vocab(forge.ProviderGitHub)); got == "" ||
		!strings.Contains(got, "update branch") {
		t.Errorf("GitHub wording = %q, want it to mention update branch", got)
	}
}

// Auto-merge exists to wait out CI, so a running pipeline is not a blocker for
// it — but a draft still is.
func TestListAutoMergeBlockToleratesRunningCI(t *testing.T) {
	v := forge.Vocab(forge.ProviderGitHub)
	running := forge.Change{MergeState: forge.MergeStateCIRunning}
	if got := listAutoMergeBlock(running, v); got != "" {
		t.Errorf("auto-merge block for running CI = %q, want none", got)
	}
	if got := listMergeBlock(running, v); got == "" {
		t.Error("plain merge should still be blocked by running CI")
	}
	draft := forge.Change{MergeState: forge.MergeStateCIRunning, Draft: true}
	if got := listAutoMergeBlock(draft, v); got == "" {
		t.Error("auto-merge should still be blocked by draft state")
	}
}

package tui

import (
	"testing"
	"time"

	"github.com/hamkens/glx/internal/gitlab"
)

func TestSummarizeReviewerScope(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mrs := []gitlab.MR{
		{ReviewState: "REQUESTED", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{ReviewState: "REQUESTED", UpdatedAt: now.Add(-72 * time.Hour).Format(time.RFC3339)},
		{ReviewState: "REQUESTED", Draft: true, UpdatedAt: now.Add(-96 * time.Hour).Format(time.RFC3339)},
		{ReviewState: "REVIEWED", UpdatedAt: now.Add(-120 * time.Hour).Format(time.RFC3339)},
		{ReviewState: "APPROVED", ApprovedByMe: true, UpdatedAt: now.Add(-144 * time.Hour).Format(time.RFC3339)},
	}

	got := summarizeScope(gitlab.ScopeReviewer, mrs, now)
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
	mrs := []gitlab.MR{
		{DetailedStatus: "MERGEABLE", Pipeline: "SUCCESS"},
		{DetailedStatus: "NOT_APPROVED", ApprovalsLeft: 2},
		{DetailedStatus: "NEED_REBASE"},
		{DetailedStatus: "CONFLICT", Conflicts: true},
		{DetailedStatus: "CI_STILL_RUNNING", Pipeline: "RUNNING"},
		{DetailedStatus: "DISCUSSIONS_NOT_RESOLVED"},
		{DetailedStatus: "NOT_APPROVED", Draft: true, Pipeline: "FAILED"},
	}

	got := summarizeScope(gitlab.ScopeAuthored, mrs, time.Now())
	if got.ready != 1 || got.blocked != 6 {
		t.Fatalf("ready/blocked = %d/%d, want 1/6", got.ready, got.blocked)
	}
	if got.approvals != 2 || got.ciFailed != 1 || got.ciRunning != 1 {
		t.Fatalf("approval/failed/running = %d/%d/%d, want 2/1/1",
			got.approvals, got.ciFailed, got.ciRunning)
	}
	if got.rebases != 1 || got.conflicts != 1 || got.threads != 1 || got.drafts != 1 {
		t.Fatalf("rebase/conflict/thread/draft = %d/%d/%d/%d, want 1/1/1/1",
			got.rebases, got.conflicts, got.threads, got.drafts)
	}
}

func TestReadyToMergeRejectsListBlockers(t *testing.T) {
	if !readyToMerge(gitlab.MR{DetailedStatus: "MERGEABLE"}) {
		t.Fatal("mergeable MR should be ready")
	}
	if readyToMerge(gitlab.MR{DetailedStatus: "MERGEABLE", Draft: true}) {
		t.Fatal("draft MR should not be ready")
	}
	if readyToMerge(gitlab.MR{DetailedStatus: "MERGEABLE", ApprovalsLeft: 1}) {
		t.Fatal("MR needing approval should not be ready")
	}
}

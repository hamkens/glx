package tui

import (
	"testing"
	"time"

	"github.com/hamkens/glx/internal/forge"
)

func TestClassifyReviewerChanges(t *testing.T) {
	cases := []struct {
		name string
		mr   forge.Change
		want statusGroup
	}{
		{"requested", forge.Change{ReviewState: forge.ReviewStateRequested}, groupNeedsReview},
		{"requested draft", forge.Change{ReviewState: forge.ReviewStateRequested, Draft: true}, groupDrafts},
		{"commented", forge.Change{ReviewState: forge.ReviewStateReviewed}, groupAwaitingAuthor},
		{"changes requested", forge.Change{ReviewState: forge.ReviewStateChangesRequested}, groupAwaitingAuthor},
		{"approved", forge.Change{ReviewState: forge.ReviewStateApproved, ApprovedByMe: true}, groupApprovedByMe},
		{"no review state", forge.Change{}, groupReviewOther},
	}
	for _, c := range cases {
		if got := classifyChange(forge.ScopeReviewer, c.mr); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got.label(), c.want.label())
		}
	}
}

func TestClassifyAuthoredChanges(t *testing.T) {
	cases := []struct {
		name string
		mr   forge.Change
		want statusGroup
	}{
		{"mergeable", forge.Change{MergeState: forge.MergeStateMergeable}, groupReady},
		{"unknown merge state", forge.Change{}, groupReady},
		{"draft", forge.Change{MergeState: forge.MergeStateMergeable, Draft: true}, groupDrafts},
		{"ci failed", forge.Change{MergeState: forge.MergeStateCIFailed, Pipeline: forge.StatusFailed}, groupNeedsFix},
		{"conflict", forge.Change{MergeState: forge.MergeStateConflict, Conflicts: true}, groupNeedsFix},
		{"behind", forge.Change{MergeState: forge.MergeStateNeedsUpdate}, groupNeedsFix},
		{"changes requested", forge.Change{MergeState: forge.MergeStateChangesRequested}, groupNeedsFix},
		{"threads", forge.Change{MergeState: forge.MergeStateThreadsUnresolved}, groupNeedsFix},
		{"needs approval", forge.Change{MergeState: forge.MergeStateNotApproved, ApprovalsLeft: 1}, groupWaiting},
		{"ci running", forge.Change{MergeState: forge.MergeStateCIRunning, Pipeline: forge.StatusRunning}, groupWaiting},
		{"blocked", forge.Change{MergeState: forge.MergeStateBlocked}, groupWaiting},
		{"checking", forge.Change{MergeState: forge.MergeStateChecking}, groupWaiting},
	}
	for _, scope := range []forge.Scope{forge.ScopeAuthored, forge.ScopeAssigned} {
		for _, c := range cases {
			if got := classifyChange(scope, c.mr); got != c.want {
				t.Errorf("%v %s: got %s, want %s", scope, c.name, got.label(), c.want.label())
			}
		}
	}
}

func TestStatusGroupedRowsOrderGroupsAndOldestReviewFirst(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	m := mrListModel{}
	m.byStatus[m.scopeIdx] = true
	m.mrs = []forge.Change{
		{ID: "1", Repo: "a", ReviewState: forge.ReviewStateApproved, ApprovedByMe: true, UpdatedAt: at(time.Hour)},
		{ID: "2", Repo: "b", ReviewState: forge.ReviewStateRequested, UpdatedAt: at(2 * time.Hour)},
		{ID: "3", Repo: "a", ReviewState: forge.ReviewStateRequested, UpdatedAt: at(72 * time.Hour)},
		{ID: "4", Repo: "c", ReviewState: forge.ReviewStateReviewed, UpdatedAt: at(3 * time.Hour)},
		{ID: "5", Repo: "b", ReviewState: forge.ReviewStateRequested, UpdatedAt: at(96 * time.Hour)},
	}
	m.rebuildRows()

	var got []string
	for _, r := range m.rows {
		switch {
		case r.isHeader && r.group != "":
			got = append(got, r.group)
		case r.isHeader:
			got = append(got, "repo "+r.project)
		case r.selectable():
			got = append(got, r.mr.ID)
		}
	}
	want := []string{
		"Needs your review · 3", "repo b", "5", "2", "repo a", "3",
		"Waiting on author · 1", "repo c", "4",
		"Approved by you · 1", "repo a", "1",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
	if m.rows[m.cur].mr.ID != "5" {
		t.Fatalf("cursor on %q, want the oldest pending review", m.rows[m.cur].mr.ID)
	}
}

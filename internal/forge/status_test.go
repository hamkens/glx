package forge

import "testing"

func TestParseGitHubStatusFoldsConclusion(t *testing.T) {
	cases := []struct {
		status     string
		conclusion string
		want       Status
	}{
		{"queued", "", StatusPending},
		{"in_progress", "", StatusRunning},
		{"waiting", "", StatusManual},
		{"completed", "success", StatusSuccess},
		{"completed", "failure", StatusFailed},
		{"completed", "timed_out", StatusFailed},
		{"completed", "startup_failure", StatusFailed},
		{"completed", "cancelled", StatusCanceled},
		{"completed", "skipped", StatusSkipped},
		{"completed", "neutral", StatusSkipped},
		{"completed", "action_required", StatusManual},
		{"", "", StatusNone},
	}
	for _, c := range cases {
		if got := ParseGitHubStatus(c.status, c.conclusion); got != c.want {
			t.Errorf("ParseGitHubStatus(%q, %q) = %v; want %v", c.status, c.conclusion, got, c.want)
		}
	}
}

func TestParseGitLabStatusAcceptsBothBackendCasings(t *testing.T) {
	// The MR list reads pipeline status over GraphQL (uppercase) while the
	// pipeline view reads it over REST (lowercase); both must normalize equally.
	for _, pair := range [][2]string{
		{"SUCCESS", "success"},
		{"FAILED", "failed"},
		{"RUNNING", "running"},
		{"CANCELED", "canceled"},
		{"MANUAL", "manual"},
	} {
		if a, b := ParseGitLabStatus(pair[0]), ParseGitLabStatus(pair[1]); a != b {
			t.Errorf("ParseGitLabStatus(%q)=%v != ParseGitLabStatus(%q)=%v", pair[0], a, pair[1], b)
		}
	}
	if got := ParseGitLabStatus(""); got != StatusNone {
		t.Errorf("empty status = %v; want StatusNone", got)
	}
	if got := ParseGitLabStatus("waiting_for_resource"); got != StatusPending {
		t.Errorf("waiting_for_resource = %v; want StatusPending", got)
	}
}

func TestStatusActiveExcludesManual(t *testing.T) {
	// Manual jobs wait on a human, so polling them forever is pointless.
	if StatusManual.Active() {
		t.Error("manual should not count as active")
	}
	if !StatusRunning.Active() || !StatusPending.Active() {
		t.Error("running/pending should be active")
	}
	if StatusSuccess.Active() || StatusNone.Active() {
		t.Error("terminal/absent statuses should not be active")
	}
}

func TestParseGitHubMergeStateDisambiguatesBlocked(t *testing.T) {
	// "blocked" is overloaded on GitHub; the review signals disambiguate it.
	if got := ParseGitHubMergeState("blocked", false, true, false); got != MergeStateNotApproved {
		t.Errorf("blocked+reviewRequired = %v; want MergeStateNotApproved", got)
	}
	if got := ParseGitHubMergeState("blocked", false, false, true); got != MergeStateChangesRequested {
		t.Errorf("blocked+changesRequested = %v; want MergeStateChangesRequested", got)
	}
	if got := ParseGitHubMergeState("blocked", false, false, false); got != MergeStateBlocked {
		t.Errorf("plain blocked = %v; want MergeStateBlocked", got)
	}
	// changesRequested wins over a merely-outstanding review request.
	if got := ParseGitHubMergeState("blocked", false, true, true); got != MergeStateChangesRequested {
		t.Errorf("blocked+both = %v; want MergeStateChangesRequested", got)
	}
}

func TestParseGitHubMergeStateDraftAndUnstable(t *testing.T) {
	// A draft PR reports "clean" shortly after creation; draft must still win.
	if got := ParseGitHubMergeState("clean", true, false, false); got != MergeStateDraft {
		t.Errorf("draft+clean = %v; want MergeStateDraft", got)
	}
	// "unstable" = only non-required checks failing, which does not block merge.
	if got := ParseGitHubMergeState("unstable", false, false, false); !got.Mergeable() {
		t.Errorf("unstable = %v; want a mergeable state", got)
	}
	if got := ParseGitHubMergeState("dirty", false, false, false); got != MergeStateConflict {
		t.Errorf("dirty = %v; want MergeStateConflict", got)
	}
	if got := ParseGitHubMergeState("behind", false, false, false); got != MergeStateNeedsUpdate {
		t.Errorf("behind = %v; want MergeStateNeedsUpdate", got)
	}
	if got := ParseGitHubMergeState("", false, false, false); got != MergeStateChecking {
		t.Errorf("empty = %v; want MergeStateChecking", got)
	}
}

func TestParseGitHubStateMergedOverridesClosed(t *testing.T) {
	// GitHub reports a merged PR as closed+merged, not as its own state.
	if got := ParseGitHubState("closed", true); got != StateMerged {
		t.Errorf("closed+merged = %v; want StateMerged", got)
	}
	if got := ParseGitHubState("closed", false); got != StateClosed {
		t.Errorf("closed = %v; want StateClosed", got)
	}
	if got := ParseGitHubState("open", false); got != StateOpen {
		t.Errorf("open = %v; want StateOpen", got)
	}
}

func TestParseGitLabMergeState(t *testing.T) {
	cases := map[string]MergeState{
		"MERGEABLE":                MergeStateMergeable,
		"NEED_REBASE":              MergeStateNeedsUpdate,
		"CI_STILL_RUNNING":         MergeStateCIRunning,
		"CI_MUST_PASS":             MergeStateCIFailed,
		"DISCUSSIONS_NOT_RESOLVED": MergeStateThreadsUnresolved,
		"NOT_APPROVED":             MergeStateNotApproved,
		"DRAFT_STATUS":             MergeStateDraft,
		"CONFLICT":                 MergeStateConflict,
		"BLOCKED_STATUS":           MergeStateBlocked,
		"CHECKING":                 MergeStateChecking,
		"":                         MergeStateUnknown,
	}
	for in, want := range cases {
		if got := ParseGitLabMergeState(in); got != want {
			t.Errorf("ParseGitLabMergeState(%q) = %v; want %v", in, got, want)
		}
	}
}

func TestMergeStateHelpers(t *testing.T) {
	// Unknown must not be treated as a blocker: providers omit the field.
	if !MergeStateUnknown.Mergeable() {
		t.Error("unknown should not block merging")
	}
	if MergeStateConflict.Mergeable() {
		t.Error("conflict should block merging")
	}
	if !MergeStateCIRunning.CIPending() || !MergeStateCIFailed.CIPending() {
		t.Error("CI states should report CIPending")
	}
	if MergeStateNotApproved.CIPending() {
		t.Error("approvals missing is not a CI-pending state")
	}
}

func TestParseReviewStates(t *testing.T) {
	if got := ParseGitLabReviewState("REQUESTED"); got != ReviewStateRequested {
		t.Errorf("gitlab REQUESTED = %v", got)
	}
	if got := ParseGitLabReviewState("APPROVED"); got != ReviewStateApproved {
		t.Errorf("gitlab APPROVED = %v", got)
	}
	if got := ParseGitHubReviewState("CHANGES_REQUESTED"); got != ReviewStateChangesRequested {
		t.Errorf("github CHANGES_REQUESTED = %v", got)
	}
	if got := ParseGitHubReviewState("COMMENTED"); got != ReviewStateReviewed {
		t.Errorf("github COMMENTED = %v", got)
	}
	// A dismissed or unsubmitted review leaves nothing recorded.
	if got := ParseGitHubReviewState("DISMISSED"); got != ReviewStateNone {
		t.Errorf("github DISMISSED = %v; want ReviewStateNone", got)
	}
	if !ReviewStateRequested.Pending() || ReviewStateApproved.Pending() {
		t.Error("Pending() should be true only for requested")
	}
}

func TestVocabFallsBackToGitLab(t *testing.T) {
	if got := Vocab(ProviderGitHub).ChangeAbbrev; got != "PR" {
		t.Errorf("github abbrev = %q; want PR", got)
	}
	if got := Vocab(ProviderGitLab).ChangeAbbrev; got != "MR" {
		t.Errorf("gitlab abbrev = %q; want MR", got)
	}
	if got := Vocab(Provider("nope")).ChangeAbbrev; got != "MR" {
		t.Errorf("unknown provider abbrev = %q; want MR fallback", got)
	}
}

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hamkens/glx/internal/forge"
)

func TestPRNodeMapsViewerReviewState(t *testing.T) {
	raw := []byte(`{
		"nodes": [{
			"number": 7,
			"title": "review me",
			"repository": {"nameWithOwner": "acme/widget"},
			"reviewDecision": "REVIEW_REQUIRED",
			"mergeStateStatus": "BLOCKED",
			"latestReviews": {"nodes": [
				{"state": "APPROVED", "author": {"login": "someone"}}
			]},
			"reviewRequests": {"nodes": [
				{"requestedReviewer": {"login": "me"}}
			]}
		}]
	}`)

	var conn prConnection
	if err := json.Unmarshal(raw, &conn); err != nil {
		t.Fatal(err)
	}

	page := conn.toPage("me")
	if len(page.Changes) != 1 {
		t.Fatalf("change count = %d, want 1", len(page.Changes))
	}
	got := page.Changes[0]
	if got.ID != "7" {
		t.Errorf("ID = %q, want 7", got.ID)
	}
	if got.Repo != "acme/widget" {
		t.Errorf("Repo = %q, want acme/widget", got.Repo)
	}
	// The viewer has not reviewed yet but is a requested reviewer.
	if got.ReviewState != forge.ReviewStateRequested {
		t.Errorf("ReviewState = %v, want requested", got.ReviewState)
	}
	if got.ApprovedByMe {
		t.Error("ApprovedByMe = true; another user approved, not the viewer")
	}
	// REVIEW_REQUIRED means the approval requirement is unmet.
	if got.Approved {
		t.Error("Approved = true; want false when a review is still required")
	}
	if got.ApprovalsLeft != 1 {
		t.Errorf("ApprovalsLeft = %d, want 1", got.ApprovalsLeft)
	}
	if got.MergeState != forge.MergeStateNotApproved {
		t.Errorf("MergeState = %v, want approvals missing", got.MergeState)
	}
}

func TestPRNodeViewerApprovalWins(t *testing.T) {
	raw := []byte(`{
		"nodes": [{
			"number": 8,
			"reviewDecision": "APPROVED",
			"mergeStateStatus": "CLEAN",
			"latestReviews": {"nodes": [
				{"state": "APPROVED", "author": {"login": "me"}}
			]},
			"reviewRequests": {"nodes": []}
		}]
	}`)

	var conn prConnection
	if err := json.Unmarshal(raw, &conn); err != nil {
		t.Fatal(err)
	}
	got := conn.toPage("me").Changes[0]
	if got.ReviewState != forge.ReviewStateApproved {
		t.Errorf("ReviewState = %v, want approved", got.ReviewState)
	}
	if !got.ApprovedByMe {
		t.Error("ApprovedByMe = false, want true")
	}
	if !got.Approved {
		t.Error("Approved = false, want true")
	}
	if !got.MergeState.Mergeable() {
		t.Errorf("MergeState = %v, want mergeable", got.MergeState)
	}
}

// An empty reviewDecision means the repository requires no review at all, which
// must not be rendered as "approvals missing".
func TestPRNodeNoReviewRequirementCountsAsApproved(t *testing.T) {
	raw := []byte(`{"nodes": [{"number": 9, "reviewDecision": "", "mergeStateStatus": "CLEAN"}]}`)
	var conn prConnection
	if err := json.Unmarshal(raw, &conn); err != nil {
		t.Fatal(err)
	}
	got := conn.toPage("me").Changes[0]
	if !got.Approved {
		t.Error("Approved = false; want true when no review is required")
	}
	if got.ApprovalsLeft != 0 {
		t.Errorf("ApprovalsLeft = %d, want 0", got.ApprovalsLeft)
	}
}

// Searching type ISSUE also returns issues, which decode as empty PR nodes.
func TestToPageSkipsNonPullRequests(t *testing.T) {
	raw := []byte(`{"nodes": [{}, {"number": 3}, {}]}`)
	var conn prConnection
	if err := json.Unmarshal(raw, &conn); err != nil {
		t.Fatal(err)
	}
	page := conn.toPage("")
	if len(page.Changes) != 1 || page.Changes[0].ID != "3" {
		t.Fatalf("changes = %+v; want just PR 3", page.Changes)
	}
}

func TestPipelineRollupPrefersInFlightOverFailure(t *testing.T) {
	raw := []byte(`{
		"number": 1,
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "FAILURE",
			"contexts": {"nodes": [
				{"__typename": "CheckRun", "status": "completed", "conclusion": "failure",
				 "checkSuite": {"workflowRun": {"databaseId": 555}}},
				{"__typename": "CheckRun", "status": "in_progress", "conclusion": ""}
			]}
		}}}]}
	}`)
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	status, runID := n.pipeline()
	if status != forge.StatusRunning {
		t.Errorf("status = %v, want running (a check is still in flight)", status)
	}
	if !status.Active() {
		t.Error("status is not Active(); the row would stop polling mid-run")
	}
	if runID != 555 {
		t.Errorf("runID = %d, want 555", runID)
	}
}

func TestPipelineRollupReportsFailureWhenSettled(t *testing.T) {
	raw := []byte(`{
		"number": 1,
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "FAILURE",
			"contexts": {"nodes": [
				{"__typename": "CheckRun", "status": "completed", "conclusion": "success"},
				{"__typename": "CheckRun", "status": "completed", "conclusion": "failure"}
			]}
		}}}]}
	}`)
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	if status, _ := n.pipeline(); status != forge.StatusFailed {
		t.Errorf("status = %v, want failed", status)
	}
}

// A PR's checks usually span several workflow runs, so "p" must open the run
// that produced the status shown in the row — the failing one — rather than
// whichever check GitHub happened to list first.
func TestPipelineReportsTheRunBehindTheStatus(t *testing.T) {
	raw := []byte(`{
		"number": 1,
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "FAILURE",
			"contexts": {"nodes": [
				{"__typename": "CheckRun", "status": "completed", "conclusion": "success",
				 "checkSuite": {"workflowRun": {"databaseId": 111}}},
				{"__typename": "CheckRun", "status": "completed", "conclusion": "failure",
				 "checkSuite": {"workflowRun": {"databaseId": 222}}},
				{"__typename": "CheckRun", "status": "completed", "conclusion": "success",
				 "checkSuite": {"workflowRun": {"databaseId": 333}}}
			]}
		}}}]}
	}`)
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	status, runID := n.pipeline()
	if status != forge.StatusFailed {
		t.Errorf("status = %v, want failed", status)
	}
	if runID != 222 {
		t.Errorf("runID = %d, want 222 (the failing run, not the first listed)", runID)
	}
}

// External CI (CircleCI and friends) reports commit statuses, which carry no
// workflow run at all: there is nothing to drill into, and reporting a bogus id
// would send "p" to a 404.
func TestPipelineHasNoRunForExternalCI(t *testing.T) {
	raw := []byte(`{
		"number": 1,
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "SUCCESS",
			"contexts": {"nodes": [
				{"__typename": "StatusContext", "state": "SUCCESS"},
				{"__typename": "StatusContext", "state": "SUCCESS"}
			]}
		}}}]}
	}`)
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	status, runID := n.pipeline()
	if status != forge.StatusSuccess {
		t.Errorf("status = %v, want success", status)
	}
	if runID != 0 {
		t.Errorf("runID = %d, want 0", runID)
	}
}

// When the status-setting context has no run of its own (a commit status), fall
// back to any Actions run present so "p" still opens something useful.
func TestPipelineFallsBackToAnyRunID(t *testing.T) {
	raw := []byte(`{
		"number": 1,
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "FAILURE",
			"contexts": {"nodes": [
				{"__typename": "CheckRun", "status": "completed", "conclusion": "success",
				 "checkSuite": {"workflowRun": {"databaseId": 777}}},
				{"__typename": "StatusContext", "state": "FAILURE"}
			]}
		}}}]}
	}`)
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	status, runID := n.pipeline()
	if status != forge.StatusFailed {
		t.Errorf("status = %v, want failed", status)
	}
	if runID != 777 {
		t.Errorf("runID = %d, want the 777 fallback", runID)
	}
}

// With no check contexts (or a truncated list) the rollup summary is the only
// signal left.
func TestPipelineFallsBackToRollupState(t *testing.T) {
	raw := []byte(`{
		"number": 1,
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "SUCCESS", "contexts": {"nodes": []}
		}}}]}
	}`)
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	if status, _ := n.pipeline(); status != forge.StatusSuccess {
		t.Errorf("status = %v, want success", status)
	}
}

func TestPipelineNoChecksIsNone(t *testing.T) {
	var n prNode
	if err := json.Unmarshal([]byte(`{"number": 1}`), &n); err != nil {
		t.Fatal(err)
	}
	status, runID := n.pipeline()
	if status != forge.StatusNone {
		t.Errorf("status = %v, want none", status)
	}
	if runID != 0 {
		t.Errorf("runID = %d, want 0", runID)
	}
}

func TestLegacyCommitStatusContexts(t *testing.T) {
	raw := []byte(`{
		"number": 1,
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "PENDING",
			"contexts": {"nodes": [{"__typename": "StatusContext", "state": "PENDING"}]}
		}}}]}
	}`)
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	if status, _ := n.pipeline(); status != forge.StatusPending {
		t.Errorf("status = %v, want pending", status)
	}
}

func TestSearchQualifier(t *testing.T) {
	cases := map[forge.Scope]string{
		forge.ScopeAssigned: "assignee:@me",
		forge.ScopeReviewer: "review-requested:@me",
		forge.ScopeAuthored: "author:@me",
	}
	for scope, want := range cases {
		if got := searchQualifier(scope); got != want {
			t.Errorf("searchQualifier(%v) = %q, want %q", scope, got, want)
		}
	}
}

// GitHub's date qualifiers reject fractional seconds, which the GitLab-shaped
// timestamps the TUI passes around can carry.
func TestSearchTimeDropsFractionalSeconds(t *testing.T) {
	if got := searchTime("2026-07-20T10:30:00.123Z"); got != "2026-07-20T10:30:00Z" {
		t.Errorf("searchTime() = %q, want 2026-07-20T10:30:00Z", got)
	}
	if got := searchTime("2026-07-20T12:30:00+02:00"); got != "2026-07-20T10:30:00Z" {
		t.Errorf("searchTime() = %q, want the UTC equivalent", got)
	}
	// Unparseable input passes through rather than dropping the filter.
	if got := searchTime("nonsense"); got != "nonsense" {
		t.Errorf("searchTime() = %q, want passthrough", got)
	}
}

func TestSplitRepo(t *testing.T) {
	owner, name, err := splitRepo("acme/widget")
	if err != nil || owner != "acme" || name != "widget" {
		t.Fatalf("splitRepo() = %q, %q, %v", owner, name, err)
	}
	for _, bad := range []string{"", "acme", "/widget", "acme/"} {
		if _, _, err := splitRepo(bad); err == nil {
			t.Errorf("splitRepo(%q) accepted an invalid repo", bad)
		}
	}
}

func TestIsDotCom(t *testing.T) {
	for _, h := range []string{"github.com", "GitHub.com", "api.github.com", ""} {
		if !isDotCom(h) {
			t.Errorf("isDotCom(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"ghe.example.com", "github.example.com"} {
		if isDotCom(h) {
			t.Errorf("isDotCom(%q) = true, want false", h)
		}
	}
}

func TestEnterpriseClientUsesInstanceURLs(t *testing.T) {
	c, err := New("ghe.example.com", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.rest.BaseURL.String(); got != "https://ghe.example.com/api/v3/" {
		t.Errorf("REST base URL = %q", got)
	}
	if c.gqlURL != "https://ghe.example.com/api/graphql" {
		t.Errorf("GraphQL URL = %q", c.gqlURL)
	}

	dot, err := New("github.com", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got := dot.rest.BaseURL.String(); got != "https://api.github.com/" {
		t.Errorf("dotcom REST base URL = %q", got)
	}
	if dot.gqlURL != "https://api.github.com/graphql" {
		t.Errorf("dotcom GraphQL URL = %q", dot.gqlURL)
	}
}

func TestDiffURL(t *testing.T) {
	c, err := New("github.com", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.DiffURL("acme/widget", "42"); got != "https://github.com/acme/widget/pull/42/files" {
		t.Errorf("DiffURL() = %q", got)
	}
}

// Every list query must exclude archived repositories. An archived repo is
// read-only, so its open PRs can never be acted on; leaving them in would pad a
// work queue with rows that can't move. The filter belongs in the query — not in
// a post-fetch drop, which would return short pages.
func TestListQueriesExcludeArchivedRepos(t *testing.T) {
	var queries []string
	c := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables struct {
				Q string `json:"q"`
			} `json:"variables"`
		}
		if err := json.Unmarshal([]byte(readBody(t, r)), &body); err != nil {
			t.Fatalf("decode graphql body: %v", err)
		}
		queries = append(queries, body.Variables.Q)
		fmt.Fprint(w, `{"data": {"search": {"nodes": [], "pageInfo": {}}}}`)
	})

	for _, scope := range []forge.Scope{forge.ScopeReviewer, forge.ScopeAuthored, forge.ScopeAssigned} {
		if _, err := c.Changes(context.Background(), scope, "", 30); err != nil {
			t.Fatalf("Changes(%s): %v", scope, err)
		}
	}
	if _, err := c.MergedSince(context.Background(), forge.ScopeAuthored, "2026-07-01T00:00:00Z", 30); err != nil {
		t.Fatalf("MergedSince(): %v", err)
	}

	if len(queries) != 4 {
		t.Fatalf("ran %d queries, want 4", len(queries))
	}
	for _, q := range queries {
		if !strings.Contains(q, "archived:false") {
			t.Errorf("query %q is missing archived:false", q)
		}
	}
}

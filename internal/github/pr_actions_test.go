package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/hamkens/glx/internal/forge"
)

func TestMergeUsesDirectMergeWhenAllowed(t *testing.T) {
	var requests int
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		assertRequest(t, r, http.MethodPut, "/api/v3/repos/acme/widget/pulls/42/merge")
		fmt.Fprint(w, `{"merged":true,"sha":"abc"}`)
	})

	outcome, err := client.Merge(context.Background(), "acme/widget", "42", false)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if outcome != forge.MergeOutcomeMerged {
		t.Fatalf("Merge() outcome = %v; want %v", outcome, forge.MergeOutcomeMerged)
	}
	if requests != 1 {
		t.Fatalf("request count = %d; want 1", requests)
	}
}

// Branch protection can require the merge queue, which makes a direct merge
// fail; the action should enqueue instead of reporting an error.
func TestMergeFallsBackToMergeQueue(t *testing.T) {
	var mutations []string
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/merge"):
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, `{"message":"Changes must be made through the merge queue."}`)
		case strings.HasSuffix(r.URL.Path, "/graphql"):
			body := readBody(t, r)
			mutations = append(mutations, body)
			if strings.Contains(body, "pullRequest(number:") {
				fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`)
				return
			}
			fmt.Fprint(w, `{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"position":1}}}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
	})

	outcome, err := client.Merge(context.Background(), "acme/widget", "42", false)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if outcome != forge.MergeOutcomeTrain {
		t.Fatalf("Merge() outcome = %v; want %v", outcome, forge.MergeOutcomeTrain)
	}
	if len(mutations) != 2 || !strings.Contains(mutations[1], "enqueuePullRequest") {
		t.Fatalf("expected a node-id lookup then enqueuePullRequest; got %d calls", len(mutations))
	}
}

// A 405 that is not about the merge queue means the PR simply isn't mergeable.
func TestMergeTranslatesNotMergeable(t *testing.T) {
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprint(w, `{"message":"Pull Request is not mergeable"}`)
	})

	_, err := client.Merge(context.Background(), "acme/widget", "42", false)
	if err == nil {
		t.Fatal("Merge() error = nil; want a not-mergeable error")
	}
	if !strings.Contains(err.Error(), "not in a mergeable state") {
		t.Fatalf("Merge() error = %v; want the translated message", err)
	}
}

func TestMergeAutoMergeUsesGraphQL(t *testing.T) {
	var sawAutoMerge bool
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/graphql") {
			t.Fatalf("auto-merge hit REST: %s %s", r.Method, r.URL)
		}
		body := readBody(t, r)
		if strings.Contains(body, "pullRequest(number:") {
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`)
			return
		}
		if strings.Contains(body, "enablePullRequestAutoMerge") {
			sawAutoMerge = true
		}
		fmt.Fprint(w, `{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"number":42,"state":"OPEN"}}}}`)
	})

	outcome, err := client.Merge(context.Background(), "acme/widget", "42", true)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if outcome != forge.MergeOutcomeAutoMerge {
		t.Fatalf("Merge() outcome = %v; want %v", outcome, forge.MergeOutcomeAutoMerge)
	}
	if !sawAutoMerge {
		t.Fatal("enablePullRequestAutoMerge was never called")
	}
}

// Auto-merge is a per-repository setting and off by default; the error should
// say so rather than surfacing the raw GraphQL message.
func TestAutoMergeExplainsDisabledSetting(t *testing.T) {
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		if strings.Contains(body, "pullRequest(number:") {
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`)
			return
		}
		fmt.Fprint(w, `{"errors":[{"message":"Auto merge is not allowed for this repository"}]}`)
	})

	_, err := client.Merge(context.Background(), "acme/widget", "42", true)
	if err == nil {
		t.Fatal("Merge() error = nil; want the disabled-setting error")
	}
	if !strings.Contains(err.Error(), "auto-merge is not enabled") {
		t.Fatalf("Merge() error = %v; want the explanatory message", err)
	}
}

// GitHub has no "remove my approval"; glx dismisses the user's own approving
// review, and must pick the right review id to dismiss.
func TestUnapproveDismissesOwnLatestApproval(t *testing.T) {
	var dismissed string
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/reviews"):
			fmt.Fprint(w, `[
				{"id": 1, "state": "APPROVED", "user": {"login": "someone"}},
				{"id": 2, "state": "COMMENTED", "user": {"login": "me"}},
				{"id": 3, "state": "APPROVED", "user": {"login": "me"}}
			]`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/dismissals"):
			dismissed = r.URL.Path
			fmt.Fprint(w, `{"id": 3, "state": "DISMISSED"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
	})
	client.username = "me"

	if err := client.Unapprove(context.Background(), "acme/widget", "42"); err != nil {
		t.Fatalf("Unapprove() error = %v", err)
	}
	if !strings.Contains(dismissed, "/reviews/3/dismissals") {
		t.Fatalf("dismissed %q; want review 3 (the viewer's own approval)", dismissed)
	}
}

// A later CHANGES_REQUESTED supersedes an earlier approval, so there is nothing
// to dismiss and glx should say so instead of calling the API.
func TestUnapproveWithoutActiveApproval(t *testing.T) {
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected write request: %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `[
			{"id": 1, "state": "APPROVED", "user": {"login": "me"}},
			{"id": 2, "state": "CHANGES_REQUESTED", "user": {"login": "me"}}
		]`)
	})
	client.username = "me"

	err := client.Unapprove(context.Background(), "acme/widget", "42")
	if err == nil {
		t.Fatal("Unapprove() error = nil; want a no-active-approval error")
	}
	if !strings.Contains(err.Error(), "no active approval") {
		t.Fatalf("Unapprove() error = %v", err)
	}
}

func TestSetDraftUsesCorrectMutation(t *testing.T) {
	for _, tc := range []struct {
		draft bool
		want  string
	}{
		{true, "convertPullRequestToDraft"},
		{false, "markPullRequestReadyForReview"},
	} {
		var got string
		client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			body := readBody(t, r)
			if strings.Contains(body, "pullRequest(number:") {
				fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`)
				return
			}
			got = body
			fmt.Fprint(w, `{"data":{}}`)
		})
		if err := client.SetDraft(context.Background(), "acme/widget", "42", "Some title", tc.draft); err != nil {
			t.Fatalf("SetDraft(%v) error = %v", tc.draft, err)
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("SetDraft(%v) mutation = %q; want it to call %s", tc.draft, got, tc.want)
		}
	}
}

// UpdateBranch is asynchronous: GitHub answers 202 with an empty body, which
// go-github surfaces as AcceptedError. That is success, not failure.
func TestUpdateBranchTreatsAcceptedAsSuccess(t *testing.T) {
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodPut, "/api/v3/repos/acme/widget/pulls/42/update-branch")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{}`)
	})

	if err := client.UpdateBranch(context.Background(), "acme/widget", "42"); err != nil {
		t.Fatalf("UpdateBranch() error = %v", err)
	}
}

func TestApproveExplainsSelfApproval(t *testing.T) {
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"Unprocessable Entity"}`)
	})

	err := client.Approve(context.Background(), "acme/widget", "42")
	if err == nil {
		t.Fatal("Approve() error = nil; want the self-approval explanation")
	}
	if !strings.Contains(err.Error(), "cannot approve your own") {
		t.Fatalf("Approve() error = %v", err)
	}
}

// Canceling a job cancels its whole run, since GitHub has no per-job cancel.
func TestCancelJobCancelsWholeRun(t *testing.T) {
	var canceled string
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/jobs/99"):
			fmt.Fprint(w, `{"id": 99, "run_id": 4242}`)
		case strings.Contains(r.URL.Path, "/actions/runs/"):
			canceled = r.URL.Path
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
	})

	if err := client.CancelJob(context.Background(), "acme/widget", 99); err != nil {
		t.Fatalf("CancelJob() error = %v", err)
	}
	if !strings.Contains(canceled, "/actions/runs/4242/cancel") {
		t.Fatalf("canceled %q; want the job's parent run", canceled)
	}
}

func TestCapabilitiesFlagRunWideCancel(t *testing.T) {
	c, err := New("github.com", "token")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Capabilities().CancelJobIsRunWide {
		t.Error("CancelJobIsRunWide = false; GitHub cancels the whole run")
	}
	if c.Provider() != forge.ProviderGitHub {
		t.Errorf("Provider() = %v", c.Provider())
	}
}

func TestJobDuration(t *testing.T) {
	start := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	finished := &gogithub.WorkflowJob{
		StartedAt:   &gogithub.Timestamp{Time: start},
		CompletedAt: &gogithub.Timestamp{Time: start.Add(90 * time.Second)},
	}
	if got := jobDuration(finished); got != 90 {
		t.Errorf("finished job duration = %v, want 90", got)
	}

	// A job that has not started yet has no duration.
	if got := jobDuration(&gogithub.WorkflowJob{}); got != 0 {
		t.Errorf("unstarted job duration = %v, want 0", got)
	}

	// A running job is measured against now, so it must be positive.
	running := &gogithub.WorkflowJob{
		StartedAt: &gogithub.Timestamp{Time: time.Now().Add(-30 * time.Second)},
	}
	if got := jobDuration(running); got < 29 || got > 120 {
		t.Errorf("running job duration = %v, want roughly 30", got)
	}

	// Clock skew must not produce a negative duration.
	skewed := &gogithub.WorkflowJob{
		StartedAt:   &gogithub.Timestamp{Time: start},
		CompletedAt: &gogithub.Timestamp{Time: start.Add(-10 * time.Second)},
	}
	if got := jobDuration(skewed); got != 0 {
		t.Errorf("skewed duration = %v, want 0", got)
	}
}

// newActionTestClient points both backends at a local test server.
func newActionTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := New("ghe.example.com", "token")
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	rest, err := gogithub.NewClient(nil).WithAuthToken("token").WithEnterpriseURLs(
		server.URL+"/api/v3/", server.URL+"/api/uploads/")
	if err != nil {
		t.Fatalf("new test REST client: %v", err)
	}
	client.rest = rest
	client.gqlURL = server.URL + "/api/graphql"
	return client
}

func assertRequest(t *testing.T, r *http.Request, method, escapedPath string) {
	t.Helper()
	if r.Method != method {
		t.Errorf("method = %s; want %s", r.Method, method)
	}
	if r.URL.EscapedPath() != escapedPath {
		t.Errorf("path = %s; want %s", r.URL.EscapedPath(), escapedPath)
	}
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(b)
}

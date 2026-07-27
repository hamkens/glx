package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	gogitlab "github.com/xanzy/go-gitlab"
)

func TestMergeRoutesThroughMergeTrainWhenEnabled(t *testing.T) {
	var requests int
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			assertRequest(t, r, http.MethodGet, "/api/v4/projects/group%2Frepo")
			fmt.Fprint(w, `{"merge_trains_enabled":true}`)
		case 2:
			assertRequest(t, r, http.MethodPost, "/api/v4/projects/group%2Frepo/merge_trains/merge_requests/42")
			var body addToMergeTrainOptions
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode merge train body: %v", err)
			}
			if body.AutoMerge {
				t.Fatal("plain merge unexpectedly requested auto-merge")
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, r.Method, r.URL)
		}
	})

	outcome, err := client.Merge(context.Background(), "group/repo", "42", false)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if outcome != MergeOutcomeTrain {
		t.Fatalf("Merge() outcome = %v; want %v", outcome, MergeOutcomeTrain)
	}
	if requests != 2 {
		t.Fatalf("request count = %d; want 2", requests)
	}
}

func TestMergeUsesRegularEndpointWithoutMergeTrain(t *testing.T) {
	var requests int
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			assertRequest(t, r, http.MethodGet, "/api/v4/projects/group%2Frepo")
			fmt.Fprint(w, `{"merge_trains_enabled":false}`)
		case 2:
			assertRequest(t, r, http.MethodPut, "/api/v4/projects/group%2Frepo/merge_requests/42/merge")
			fmt.Fprint(w, `{}`)
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, r.Method, r.URL)
		}
	})

	outcome, err := client.Merge(context.Background(), "group/repo", "42", false)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if outcome != MergeOutcomeMerged {
		t.Fatalf("Merge() outcome = %v; want %v", outcome, MergeOutcomeMerged)
	}
}

func TestMergeTrainAutoMergeWaitsForChecks(t *testing.T) {
	var requests int
	client := newActionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			fmt.Fprint(w, `{"merge_trains_enabled":true}`)
		case 2:
			var body addToMergeTrainOptions
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode merge train body: %v", err)
			}
			if !body.AutoMerge {
				t.Fatal("auto-merge request did not set auto_merge")
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, r.Method, r.URL)
		}
	})

	outcome, err := client.Merge(context.Background(), "group/repo", "42", true)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if outcome != MergeOutcomeAutoMerge {
		t.Fatalf("Merge() outcome = %v; want %v", outcome, MergeOutcomeAutoMerge)
	}
}

func newActionTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := New("gitlab.example.com", "token")
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	rest, err := gogitlab.NewClient("token", gogitlab.WithBaseURL(server.URL+"/api/v4"))
	if err != nil {
		t.Fatalf("new test REST client: %v", err)
	}
	client.rest = rest
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

func TestApplyDraftPrefix(t *testing.T) {
	cases := []struct {
		title string
		draft bool
		want  string
	}{
		{"Add feature", true, "Draft: Add feature"},
		{"Draft: Add feature", true, "Draft: Add feature"},   // already draft
		{"Draft: Add feature", false, "Add feature"},         // undraft
		{"Add feature", false, "Add feature"},                // already ready
		{"draft: lower prefix", false, "lower prefix"},       // case-insensitive
		{"WIP: legacy prefix", false, "legacy prefix"},       // legacy WIP
		{"WIP: legacy prefix", true, "Draft: legacy prefix"}, // WIP -> Draft
		{"Draft: WIP: doubled", false, "doubled"},            // strip both
		{"  Draft:   spaced  ", false, "spaced"},             // trims whitespace
	}
	for _, c := range cases {
		if got := applyDraftPrefix(c.title, c.draft); got != c.want {
			t.Errorf("applyDraftPrefix(%q, %v) = %q; want %q", c.title, c.draft, got, c.want)
		}
	}
}

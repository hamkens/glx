package gitlab

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hamkens/glx/internal/forge"
)

func TestMRConnectionCapturesCurrentReviewerState(t *testing.T) {
	raw := []byte(`{
		"nodes": [{
			"iid": "42",
			"title": "review me",
			"reviewers": {
				"nodes": [
					{"username": "someone", "mergeRequestInteraction": {"reviewState": "REVIEWED"}},
					{"username": "me", "mergeRequestInteraction": {"reviewState": "REQUESTED"}}
				]
			}
		}]
	}`)

	var connection mrConnection
	if err := json.Unmarshal(raw, &connection); err != nil {
		t.Fatal(err)
	}

	page := connection.toPage("me")
	if len(page.Changes) != 1 {
		t.Fatalf("change count = %d, want 1", len(page.Changes))
	}
	if got := page.Changes[0].ReviewState; got != forge.ReviewStateRequested {
		t.Fatalf("ReviewState = %q, want %q", got, forge.ReviewStateRequested)
	}
}

func TestMRConnectionMapsCompletedFeedbackAndReReview(t *testing.T) {
	for _, tc := range []struct {
		state    string
		want     forge.ReviewState
		feedback bool
	}{
		{"REVIEWED", forge.ReviewStateReviewed, true},
		{"REQUESTED_CHANGES", forge.ReviewStateChangesRequested, true},
		{"UNREVIEWED", forge.ReviewStateRequested, false},
		{"REQUESTED", forge.ReviewStateRequested, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			raw := fmt.Sprintf(`{"nodes": [{
				"iid": "42", "draft": false, "approved": false, "approvalsLeft": 1,
				"detailedMergeStatus": "NOT_APPROVED", "approvedBy": {"nodes": []},
				"reviewers": {"nodes": [
					{"username": "someone", "mergeRequestInteraction": {"reviewState": "UNREVIEWED"}},
					{"username": "me", "mergeRequestInteraction": {"reviewState": %q}}
				]}
			}]}`, tc.state)
			var conn mrConnection
			if err := json.Unmarshal([]byte(raw), &conn); err != nil {
				t.Fatal(err)
			}
			mr := conn.toPage("me").Changes[0]
			if mr.ReviewState != tc.want || mr.ReviewState.FeedbackGiven() != tc.feedback || mr.ApprovedByMe {
				t.Fatalf("incorrect review classification: %+v", mr)
			}
		})
	}
}

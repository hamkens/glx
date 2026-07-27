package gitlab

import (
	"encoding/json"
	"testing"
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
	if len(page.MRs) != 1 {
		t.Fatalf("MR count = %d, want 1", len(page.MRs))
	}
	if got := page.MRs[0].ReviewState; got != "REQUESTED" {
		t.Fatalf("ReviewState = %q, want REQUESTED", got)
	}
}

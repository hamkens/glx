package gitlab

import (
	"encoding/json"
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

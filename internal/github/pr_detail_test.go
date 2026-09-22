package github

import (
	"encoding/json"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/hamkens/glx/internal/forge"
)

// GitHub keeps issue comments, review bodies and review threads in three
// separate streams; the detail view expects one chronological list.
func TestDiscussionsMergeStreamsInOrder(t *testing.T) {
	raw := []byte(`{
		"comments": {"nodes": [
			{"author": {"login": "carol"}, "body": "third", "createdAt": "2026-07-03T00:00:00Z"}
		]},
		"reviews": {"nodes": [
			{"id": "R1", "state": "COMMENTED", "body": "first", "createdAt": "2026-07-01T00:00:00Z",
			 "author": {"login": "alice"}}
		]},
		"reviewThreads": {"nodes": [
			{"id": "T1", "isResolved": false, "comments": {"nodes": [
				{"author": {"login": "bob"}, "body": "second", "createdAt": "2026-07-02T00:00:00Z",
				 "path": "main.go"}
			]}}
		]}
	}`)

	var n prDetailNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}

	got := n.discussions()
	if len(got) != 3 {
		t.Fatalf("discussion count = %d, want 3", len(got))
	}
	wantOrder := []string{"first", "second", "third"}
	for i, want := range wantOrder {
		if !strings.Contains(got[i].Notes[0].Body, want) {
			t.Errorf("discussion %d body = %q, want it to contain %q", i, got[i].Notes[0].Body, want)
		}
	}
	// Only the review thread tracks resolution.
	if !got[1].Resolvable || got[1].Resolved {
		t.Errorf("thread resolvable=%v resolved=%v; want resolvable and unresolved", got[1].Resolvable, got[1].Resolved)
	}
	if got[0].Resolvable || got[2].Resolvable {
		t.Error("review body / issue comment reported as resolvable")
	}
	// An inline thread names its file so it reads in context.
	if !strings.Contains(got[1].Notes[0].Body, "main.go") {
		t.Errorf("inline note = %q, want the file path included", got[1].Notes[0].Body)
	}
}

// A review with no body carries only a verdict; it should still show up.
func TestDiscussionsRenderBodilessReviewsAsSystemNotes(t *testing.T) {
	raw := []byte(`{
		"reviews": {"nodes": [
			{"id": "R1", "state": "APPROVED", "body": "", "createdAt": "2026-07-01T00:00:00Z",
			 "author": {"login": "alice"}},
			{"id": "R2", "state": "CHANGES_REQUESTED", "body": "  ", "createdAt": "2026-07-02T00:00:00Z",
			 "author": {"login": "bob"}},
			{"id": "R3", "state": "COMMENTED", "body": "", "createdAt": "2026-07-03T00:00:00Z",
			 "author": {"login": "carol"}}
		]}
	}`)

	var n prDetailNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}

	got := n.discussions()
	// R3 is a bodiless COMMENTED review: no verdict, nothing to show.
	if len(got) != 2 {
		t.Fatalf("discussion count = %d, want 2 (the empty comment review is dropped)", len(got))
	}
	for _, d := range got {
		if !d.Notes[0].System {
			t.Errorf("note %q not marked System", d.Notes[0].Body)
		}
	}
	if !strings.Contains(got[0].Notes[0].Body, "approved") {
		t.Errorf("first note = %q, want the approval verdict", got[0].Notes[0].Body)
	}
	if !strings.Contains(got[1].Notes[0].Body, "requested changes") {
		t.Errorf("second note = %q, want the changes-requested verdict", got[1].Notes[0].Body)
	}
}

func TestCheckSummary(t *testing.T) {
	cases := []struct {
		name     string
		contexts string
		want     string
	}{
		{"all passed", `
			{"__typename": "CheckRun", "status": "completed", "conclusion": "success"},
			{"__typename": "CheckRun", "status": "completed", "conclusion": "success"}`,
			"2 checks passed"},
		{"some failing", `
			{"__typename": "CheckRun", "status": "completed", "conclusion": "failure"},
			{"__typename": "CheckRun", "status": "completed", "conclusion": "success"}`,
			"1 of 2 checks failing"},
		{"still running", `
			{"__typename": "CheckRun", "status": "completed", "conclusion": "success"},
			{"__typename": "CheckRun", "status": "in_progress", "conclusion": ""}`,
			"1 of 2 checks complete"},
		{"no checks", ``, ""},
	}
	for _, c := range cases {
		raw := []byte(`{"commits": {"nodes": [{"commit": {"statusCheckRollup": {
			"state": "SUCCESS", "contexts": {"nodes": [` + c.contexts + `]}}}}]}}`)
		var n prDetailNode
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := n.checkSummary(); got != c.want {
			t.Errorf("%s: checkSummary() = %q, want %q", c.name, got, c.want)
		}
	}
}

// GraphQL reports a merged PR with its own state, unlike REST's closed+merged.
func TestGithubStateMapsMergedForParser(t *testing.T) {
	if got := forge.ParseGitHubState(githubState("MERGED"), true); got != forge.StateMerged {
		t.Errorf("MERGED = %v, want merged", got)
	}
	if got := forge.ParseGitHubState(githubState("OPEN"), false); got != forge.StateOpen {
		t.Errorf("OPEN = %v, want open", got)
	}
	if got := forge.ParseGitHubState(githubState("CLOSED"), false); got != forge.StateClosed {
		t.Errorf("CLOSED = %v, want closed", got)
	}
}

func TestFileDiffMapsStatuses(t *testing.T) {
	cases := []struct {
		status   string
		file     *gogithub.CommitFile
		wantNew  bool
		wantDel  bool
		wantRen  bool
		wantOld  string
		wantHuge bool
	}{
		{
			status:  "added",
			file:    &gogithub.CommitFile{Filename: gogithub.Ptr("new.go"), Status: gogithub.Ptr("added"), Patch: gogithub.Ptr("@@ -0,0 +1 @@")},
			wantNew: true, wantOld: "new.go",
		},
		{
			status:  "removed",
			file:    &gogithub.CommitFile{Filename: gogithub.Ptr("gone.go"), Status: gogithub.Ptr("removed"), Patch: gogithub.Ptr("@@ -1 +0,0 @@")},
			wantDel: true, wantOld: "gone.go",
		},
		{
			status: "renamed",
			file: &gogithub.CommitFile{
				Filename: gogithub.Ptr("after.go"), PreviousFilename: gogithub.Ptr("before.go"),
				Status: gogithub.Ptr("renamed"), Patch: gogithub.Ptr("@@ -1 +1 @@"),
			},
			wantRen: true, wantOld: "before.go",
		},
		{
			// GitHub omits the patch for binaries and oversized diffs.
			status:  "modified",
			file:    &gogithub.CommitFile{Filename: gogithub.Ptr("blob.bin"), Status: gogithub.Ptr("modified")},
			wantOld: "blob.bin", wantHuge: true,
		},
	}
	for _, c := range cases {
		got := fileDiff(c.file)
		if got.NewFile != c.wantNew || got.Deleted != c.wantDel || got.Renamed != c.wantRen {
			t.Errorf("%s: new=%v deleted=%v renamed=%v", c.status, got.NewFile, got.Deleted, got.Renamed)
		}
		if got.OldPath != c.wantOld {
			t.Errorf("%s: OldPath = %q, want %q", c.status, got.OldPath, c.wantOld)
		}
		if got.TooLarge != c.wantHuge {
			t.Errorf("%s: TooLarge = %v, want %v", c.status, got.TooLarge, c.wantHuge)
		}
	}
}

// A deleted file has no patch either, but that is not "too large".
func TestFileDiffDeletionWithoutPatchIsNotTooLarge(t *testing.T) {
	got := fileDiff(&gogithub.CommitFile{
		Filename: gogithub.Ptr("gone.bin"),
		Status:   gogithub.Ptr("removed"),
	})
	if got.TooLarge {
		t.Error("TooLarge = true for a deletion with no patch")
	}
}

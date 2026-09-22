package watch

import (
	"testing"

	"github.com/hamkens/glx/internal/forge"
)

func pipe(id int64, status forge.Status, jobs ...forge.Job) *forge.Pipeline {
	return &forge.Pipeline{ID: id, Status: status, Jobs: jobs}
}

func TestToggleAndHas(t *testing.T) {
	r := New()
	if r.Has("p", 1) {
		t.Fatal("should not be watched yet")
	}
	if added := r.Toggle("p", 1, "10"); !added {
		t.Fatal("first toggle should add")
	}
	if !r.Has("p", 1) || r.Len() != 1 {
		t.Fatal("should be watched")
	}
	if added := r.Toggle("p", 1, "10"); added {
		t.Fatal("second toggle should remove")
	}
	if r.Has("p", 1) || r.Len() != 0 {
		t.Fatal("should be unwatched")
	}
}

func TestUpdate_FirstFetchNoAlert(t *testing.T) {
	r := New()
	r.Toggle("p", 1, "")
	ch := r.Update("p", pipe(1, forge.StatusRunning))
	if ch.Kind != ChangeNone {
		t.Fatalf("first fetch should not alert, got %v", ch.Kind)
	}
}

func TestUpdate_PipelineFailed(t *testing.T) {
	r := New()
	r.Toggle("p", 1, "")
	r.Update("p", pipe(1, forge.StatusRunning))
	ch := r.Update("p", pipe(1, forge.StatusFailed))
	if ch.Kind != PipelineFailed {
		t.Fatalf("want PipelineFailed, got %v", ch.Kind)
	}
}

func TestUpdate_PipelinePassedAndStopsBeingActive(t *testing.T) {
	r := New()
	r.Toggle("p", 1, "")
	r.Update("p", pipe(1, forge.StatusRunning))
	ch := r.Update("p", pipe(1, forge.StatusSuccess))
	if ch.Kind != PipelineDone {
		t.Fatalf("want PipelineDone, got %v", ch.Kind)
	}
	if r.HasActive() {
		t.Fatal("a succeeded pipeline should no longer be active")
	}
	if len(r.ActiveTargets()) != 0 {
		t.Fatal("settled pipeline should not be an active target")
	}
}

func TestUpdate_JobFailedPreferredOverFinished(t *testing.T) {
	r := New()
	r.Toggle("p", 1, "")
	r.Update("p", pipe(1, forge.StatusRunning,
		forge.Job{ID: 1, Name: "test", Status: forge.StatusRunning},
		forge.Job{ID: 2, Name: "lint", Status: forge.StatusRunning},
	))
	ch := r.Update("p", pipe(1, forge.StatusRunning,
		forge.Job{ID: 1, Name: "test", Status: forge.StatusSuccess},
		forge.Job{ID: 2, Name: "lint", Status: forge.StatusFailed},
	))
	if ch.Kind != JobFailed || ch.JobName != "lint" {
		t.Fatalf("want JobFailed lint, got %v %q", ch.Kind, ch.JobName)
	}
}

func TestUpdate_UnwatchedIsNoop(t *testing.T) {
	r := New()
	ch := r.Update("p", pipe(1, forge.StatusFailed))
	if ch.Kind != ChangeNone {
		t.Fatalf("unwatched pipeline should produce no change, got %v", ch.Kind)
	}
}

// A pipeline can legitimately report "no status" (StatusNone), which must not be
// mistaken for "never fetched" — otherwise it would be polled forever.
func TestUpdate_StatusNoneIsStillFetched(t *testing.T) {
	r := New()
	r.Toggle("p", 1, "")
	r.Update("p", pipe(1, forge.StatusNone))
	if r.HasActive() {
		t.Fatal("a fetched pipeline with no status should not stay active")
	}
	entries := r.List()
	if len(entries) != 1 || !entries[0].Fetched() {
		t.Fatal("entry should be marked fetched after an update")
	}
}

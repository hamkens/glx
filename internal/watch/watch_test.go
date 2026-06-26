package watch

import (
	"testing"

	"github.com/hamkens/glx/internal/gitlab"
)

func pipe(id int, status string, jobs ...gitlab.Job) *gitlab.Pipeline {
	return &gitlab.Pipeline{ID: id, Status: status, Jobs: jobs}
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
	ch := r.Update("p", pipe(1, "running"))
	if ch.Kind != ChangeNone {
		t.Fatalf("first fetch should not alert, got %v", ch.Kind)
	}
}

func TestUpdate_PipelineFailed(t *testing.T) {
	r := New()
	r.Toggle("p", 1, "")
	r.Update("p", pipe(1, "running"))
	ch := r.Update("p", pipe(1, "failed"))
	if ch.Kind != PipelineFailed {
		t.Fatalf("want PipelineFailed, got %v", ch.Kind)
	}
}

func TestUpdate_PipelinePassedAndStopsBeingActive(t *testing.T) {
	r := New()
	r.Toggle("p", 1, "")
	r.Update("p", pipe(1, "running"))
	ch := r.Update("p", pipe(1, "success"))
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
	r.Update("p", pipe(1, "running",
		gitlab.Job{ID: 1, Name: "test", Status: "running"},
		gitlab.Job{ID: 2, Name: "lint", Status: "running"},
	))
	ch := r.Update("p", pipe(1, "running",
		gitlab.Job{ID: 1, Name: "test", Status: "success"},
		gitlab.Job{ID: 2, Name: "lint", Status: "failed"},
	))
	if ch.Kind != JobFailed || ch.JobName != "lint" {
		t.Fatalf("want JobFailed lint, got %v %q", ch.Kind, ch.JobName)
	}
}

func TestUpdate_UnwatchedIsNoop(t *testing.T) {
	r := New()
	ch := r.Update("p", pipe(1, "failed"))
	if ch.Kind != ChangeNone {
		t.Fatalf("unwatched pipeline should produce no change, got %v", ch.Kind)
	}
}

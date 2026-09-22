// Package watch maintains an in-memory registry of pipelines the user has
// marked to monitor. A single background poller (in the TUI root) refreshes
// the active entries and diffs their status to raise change alerts.
package watch

import (
	"fmt"
	"sort"
	"sync"

	"github.com/hamkens/glx/internal/forge"
)

// Key uniquely identifies a watched pipeline.
func Key(repo string, pipelineID int64) string {
	return fmt.Sprintf("%s#%d", repo, pipelineID)
}

// Entry is a single watched pipeline plus the metadata needed to show it and
// to detect changes between polls.
type Entry struct {
	Repo       string
	PipelineID int64
	ChangeID   string // the MR/PR this pipeline belongs to, if known (display only)

	// Last observed state, updated on each successful fetch.
	Status  forge.Status // StatusNone until the first fetch
	fetched bool         // whether Status reflects a real fetch

	JobStatus map[int64]forge.Status // jobID -> status
	WebURL    string
	Added     int // monotonic sequence for stable ordering (oldest first)
}

// active reports whether the entry's pipeline is still progressing and should
// keep being polled.
func (e Entry) active() bool {
	if !e.fetched {
		return true // not yet fetched; poll at least once
	}
	return e.Status.Active()
}

// Active is the exported form used by the UI.
func (e Entry) Active() bool { return e.active() }

// Fetched reports whether this entry has been polled at least once, which the
// UI uses to distinguish "no pipeline status yet" from "pipeline has no CI".
func (e Entry) Fetched() bool { return e.fetched }

// Registry is a concurrency-safe set of watched pipelines.
type Registry struct {
	mu      sync.Mutex
	entries map[string]*Entry
	seq     int
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{entries: make(map[string]*Entry)}
}

// Toggle adds the pipeline if absent (returns true), or removes it if present
// (returns false).
func (r *Registry) Toggle(repo string, pipelineID int64, changeID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := Key(repo, pipelineID)
	if _, ok := r.entries[k]; ok {
		delete(r.entries, k)
		return false
	}
	r.seq++
	r.entries[k] = &Entry{
		Repo:       repo,
		PipelineID: pipelineID,
		ChangeID:   changeID,
		Added:      r.seq,
	}
	return true
}

// Remove deletes a watched pipeline if present.
func (r *Registry) Remove(repo string, pipelineID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, Key(repo, pipelineID))
}

// Has reports whether a pipeline is being watched.
func (r *Registry) Has(repo string, pipelineID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.entries[Key(repo, pipelineID)]
	return ok
}

// Len returns the number of watched pipelines.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// List returns a snapshot of all entries, oldest-added first.
func (r *Registry) List() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Added < out[j].Added })
	return out
}

// ActiveTargets returns the (repo, pipelineID) of entries still worth
// polling. Settled pipelines are excluded so they never hit the API again.
func (r *Registry) ActiveTargets() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Entry
	for _, e := range r.entries {
		if e.active() {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Added < out[j].Added })
	return out
}

// HasActive reports whether any watched pipeline still needs polling.
func (r *Registry) HasActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.active() {
			return true
		}
	}
	return false
}

// Change describes a status transition detected during an update.
type Change struct {
	Entry     Entry
	Kind      ChangeKind
	JobName   string // set for job-level changes
	NewStatus forge.Status
}

// ChangeKind classifies a detected change.
type ChangeKind int

const (
	ChangeNone ChangeKind = iota
	PipelineDone
	PipelineFailed
	JobFailed
	JobFinished
)

// Update applies a freshly fetched pipeline to the registry and returns the
// most salient change (if any) since the last observation. It is a no-op if
// the pipeline is no longer watched.
func (r *Registry) Update(repo string, p *forge.Pipeline) Change {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[Key(repo, p.ID)]
	if !ok {
		return Change{}
	}

	prevStatus := e.Status
	prevFetched := e.fetched
	prevJobs := e.JobStatus

	// Record the new state.
	e.Status = p.Status
	e.fetched = true
	e.WebURL = p.WebURL
	e.JobStatus = make(map[int64]forge.Status, len(p.Jobs))
	for _, j := range p.Jobs {
		e.JobStatus[j.ID] = j.Status
	}

	// First fetch: establish baseline, no alert.
	if !prevFetched {
		return Change{}
	}

	// Pipeline-level transition takes priority.
	if prevStatus != p.Status {
		switch p.Status {
		case forge.StatusFailed:
			return Change{Entry: *e, Kind: PipelineFailed, NewStatus: p.Status}
		case forge.StatusSuccess, forge.StatusCanceled:
			return Change{Entry: *e, Kind: PipelineDone, NewStatus: p.Status}
		}
	}

	// Otherwise the first job-level change (failure preferred over completion).
	var finished string
	for _, j := range p.Jobs {
		old, seen := prevJobs[j.ID]
		if !seen || old == j.Status {
			continue
		}
		if j.Status == forge.StatusFailed {
			return Change{Entry: *e, Kind: JobFailed, JobName: j.Name, NewStatus: j.Status}
		}
		if !j.Status.Active() && finished == "" {
			finished = j.Name
		}
	}
	if finished != "" {
		return Change{Entry: *e, Kind: JobFinished, JobName: finished}
	}
	return Change{}
}

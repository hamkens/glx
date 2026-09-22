package forge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Fleet is several authenticated connections presented as one Forge, so the TUI
// can show GitLab merge requests and GitHub pull requests in a single list.
//
// Reads fan out to every member concurrently and merge; writes and per-change
// reads route to the one member that owns the target. Ownership is resolved
// from the Host field the fleet stamps onto every Change it returns, with a
// repo-to-host index as the fallback for call paths that only carry a repo
// path (the watch poller, for one).
//
// A Fleet with a single member behaves exactly like that member, which keeps
// the single-host case — still the common one — free of fleet semantics.
type Fleet struct {
	members []Forge

	// health records the last read error per host, so a host that is down or
	// whose token expired degrades to a visible warning instead of an empty
	// list. Guarded by mu along with repoHost.
	mu       sync.Mutex
	health   map[string]error
	repoHost map[string]string // repo path -> host that served it
}

// NewFleet groups connections into one Forge. The order given is the order used
// to break ties when merging results.
func NewFleet(members ...Forge) *Fleet {
	return &Fleet{
		members:  members,
		health:   make(map[string]error),
		repoHost: make(map[string]string),
	}
}

// Members returns the underlying connections, in order.
func (f *Fleet) Members() []Forge { return f.members }

// Single returns the sole member when the fleet has exactly one, which lets
// callers skip multi-host UI affordances entirely.
func (f *Fleet) Single() (Forge, bool) {
	if len(f.members) == 1 {
		return f.members[0], true
	}
	return nil, false
}

// --- identity ---

// Provider reports the single member's provider, or ProviderMixed when the
// fleet spans more than one product. Views use this to pick vocabulary; mixed
// fleets fall back to neutral wording and resolve per-row wording instead.
func (f *Fleet) Provider() Provider {
	if len(f.members) == 0 {
		return ProviderGitLab
	}
	p := f.members[0].Provider()
	for _, m := range f.members[1:] {
		if m.Provider() != p {
			return ProviderMixed
		}
	}
	return p
}

// Host is the member's host, or a compact summary for a fleet ("2 hosts").
func (f *Fleet) Host() string {
	switch len(f.members) {
	case 0:
		return ""
	case 1:
		return f.members[0].Host()
	default:
		return fmt.Sprintf("%d hosts", len(f.members))
	}
}

// CurrentUser authenticates every member. It succeeds when at least one host
// answers, recording the rest as unhealthy: one expired token shouldn't lock
// the user out of the hosts that do work. The returned login and version
// describe the first healthy member, and are only used for display and for the
// "approved by me" comparison, which each member makes against its own login.
func (f *Fleet) CurrentUser(ctx context.Context) (string, string, error) {
	type result struct {
		user, version string
		err           error
	}
	results := make([]result, len(f.members))
	var wg sync.WaitGroup
	for i, m := range f.members {
		wg.Add(1)
		go func(i int, m Forge) {
			defer wg.Done()
			u, v, err := m.CurrentUser(ctx)
			results[i] = result{u, v, err}
		}(i, m)
	}
	wg.Wait()

	var (
		user, version string
		ok            bool
		failures      []string
	)
	for i, r := range results {
		host := f.members[i].Host()
		f.setHealth(host, r.err)
		if r.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", host, r.err))
			continue
		}
		if !ok {
			user, version, ok = r.user, r.version, true
		}
	}
	if !ok {
		return "", "", fmt.Errorf("no host could be reached: %s", strings.Join(failures, "; "))
	}
	if len(f.members) > 1 {
		version = f.Host()
	}
	return user, version, nil
}

// Username returns the first member's login. Per-change comparisons use the
// owning member's own username, so this is only a display fallback.
func (f *Fleet) Username() string {
	for _, m := range f.members {
		if u := m.Username(); u != "" {
			return u
		}
	}
	return ""
}

// Capabilities is the intersection across members: an action is only offered
// globally when every host can perform it. Per-row handlers consult the owning
// member's capabilities, so this only governs fleet-wide help text.
func (f *Fleet) Capabilities() Capabilities {
	if len(f.members) == 0 {
		return Capabilities{}
	}
	caps := f.members[0].Capabilities()
	for _, m := range f.members[1:] {
		c := m.Capabilities()
		caps.AutoMerge = caps.AutoMerge && c.AutoMerge
		caps.Unapprove = caps.Unapprove && c.Unapprove
		caps.DraftToggle = caps.DraftToggle && c.DraftToggle
		// A warning that applies to any member must be shown, so this one ORs.
		caps.CancelJobIsRunWide = caps.CancelJobIsRunWide || c.CancelJobIsRunWide
	}
	return caps
}

// --- health ---

// Health returns the last error per host, keyed by host name, for hosts that
// are currently failing. An empty map means everything answered.
func (f *Fleet) Health() map[string]error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]error, len(f.health))
	for h, err := range f.health {
		if err != nil {
			out[h] = err
		}
	}
	return out
}

// Degraded reports the hosts currently failing, in fleet order, so the UI can
// name them without sorting a map.
func (f *Fleet) Degraded() []string {
	health := f.Health()
	var out []string
	for _, m := range f.members {
		if _, bad := health[m.Host()]; bad {
			out = append(out, m.Host())
		}
	}
	return out
}

func (f *Fleet) setHealth(host string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health[host] = err
}

// noteRepos indexes which host served a repo, so later calls that carry only a
// repo path can be routed. Repo paths are namespaced per host in practice; a
// collision resolves to whichever host answered most recently.
func (f *Fleet) noteRepos(host string, changes []Change) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range changes {
		f.repoHost[c.Repo] = host
	}
}

// --- routing ---

// forHost returns the member serving a host, or an error naming what is
// connected when nothing matches.
func (f *Fleet) forHost(host string) (Forge, error) {
	for _, m := range f.members {
		if strings.EqualFold(m.Host(), host) {
			return m, nil
		}
	}
	return nil, fmt.Errorf("no connection for host %q", host)
}

// ForChange resolves the member that owns a change. The Host field set by the
// fleet's own list results is authoritative; otherwise the repo index answers,
// and a single-member fleet always answers itself.
func (f *Fleet) ForChange(c Change) (Forge, error) {
	if c.Host != "" {
		return f.forHost(c.Host)
	}
	return f.ForRepo(c.Repo)
}

// ForRepo resolves the member that served a repo path. It is the fallback for
// call sites that carry no host (watch entries, pipeline drill-downs).
func (f *Fleet) ForRepo(repo string) (Forge, error) {
	if len(f.members) == 1 {
		return f.members[0], nil
	}
	f.mu.Lock()
	host, ok := f.repoHost[repo]
	f.mu.Unlock()
	if ok {
		return f.forHost(host)
	}
	return nil, fmt.Errorf("unknown host for %q; open it from the list first", repo)
}

// VocabFor returns the wording appropriate to whichever host owns a change, so
// a mixed list renders "!42"/"pipeline" and "#42"/"workflow run" on their own
// rows.
func (f *Fleet) VocabFor(c Change) Vocabulary {
	m, err := f.ForChange(c)
	if err != nil {
		return Vocab(f.Provider())
	}
	return Vocab(m.Provider())
}

// CapsFor returns the owning host's capabilities, which is what a per-row
// action must check before acting.
func (f *Fleet) CapsFor(c Change) Capabilities {
	m, err := f.ForChange(c)
	if err != nil {
		return f.Capabilities()
	}
	return m.Capabilities()
}

// --- fanned-out reads ---

// fanOut runs fn against every member concurrently, records per-host health,
// and returns the results of the members that succeeded. A member's failure is
// isolated: the others' results still come back.
func (f *Fleet) fanOut(fn func(Forge) ([]Change, error)) []Change {
	perHost := make([][]Change, len(f.members))
	var wg sync.WaitGroup
	for i, m := range f.members {
		wg.Add(1)
		go func(i int, m Forge) {
			defer wg.Done()
			changes, err := fn(m)
			f.setHealth(m.Host(), err)
			if err != nil {
				return
			}
			// Stamp provenance so every row can be routed back to its host.
			host := m.Host()
			for j := range changes {
				changes[j].Host = host
			}
			f.noteRepos(host, changes)
			perHost[i] = changes
		}(i, m)
	}
	wg.Wait()

	var out []Change
	for _, changes := range perHost {
		out = append(out, changes...)
	}
	return out
}

// Changes merges one page from every host. Pagination collapses: each host is
// asked for a full page and the union is returned with HasNextPage false, since
// a single opaque cursor cannot address several hosts at once. pageSize is
// applied per host, so a 2-host fleet returns up to 2×pageSize rows — the point
// of the merged list is seeing the whole workload, and the summary pass already
// walks every page anyway.
func (f *Fleet) Changes(ctx context.Context, scope Scope, cursor string, pageSize int) (*ChangePage, error) {
	if m, ok := f.Single(); ok {
		page, err := m.Changes(ctx, scope, cursor, pageSize)
		f.setHealth(m.Host(), err)
		if err != nil {
			return nil, err
		}
		f.stampSingle(m, page.Changes)
		return page, nil
	}
	// A non-empty cursor means "next page", which the merged list has none of.
	if cursor != "" {
		return &ChangePage{}, nil
	}

	changes := f.fanOut(func(m Forge) ([]Change, error) {
		page, err := m.Changes(ctx, scope, "", pageSize)
		if err != nil {
			return nil, err
		}
		return page.Changes, nil
	})
	if len(changes) == 0 {
		if err := f.allFailed(); err != nil {
			return nil, err
		}
	}
	sortByUpdated(changes)
	return &ChangePage{Changes: changes}, nil
}

// MergedSince merges the recently-merged sets, newest merge first.
func (f *Fleet) MergedSince(ctx context.Context, scope Scope, since string, max int) ([]Change, error) {
	if m, ok := f.Single(); ok {
		changes, err := m.MergedSince(ctx, scope, since, max)
		f.setHealth(m.Host(), err)
		if err != nil {
			return nil, err
		}
		f.stampSingle(m, changes)
		return changes, nil
	}

	changes := f.fanOut(func(m Forge) ([]Change, error) {
		return m.MergedSince(ctx, scope, since, max)
	})
	if len(changes) == 0 {
		if err := f.allFailed(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(changes, func(i, j int) bool {
		return changes[i].MergedAt > changes[j].MergedAt
	})
	return changes, nil
}

// stampSingle records provenance for a single-member fleet, which skips fanOut
// but still needs rows routable and repos indexed.
func (f *Fleet) stampSingle(m Forge, changes []Change) {
	host := m.Host()
	for i := range changes {
		changes[i].Host = host
	}
	f.noteRepos(host, changes)
}

// allFailed returns a combined error when no member produced results and every
// one of them errored, so an all-hosts-down fleet reports a real failure rather
// than an empty list.
func (f *Fleet) allFailed() error {
	health := f.Health()
	if len(health) < len(f.members) || len(f.members) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(f.members))
	for _, m := range f.members {
		if err, bad := health[m.Host()]; bad {
			msgs = append(msgs, fmt.Sprintf("%s: %v", m.Host(), err))
		}
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// sortByUpdated orders a merged list newest-updated first. Timestamps are
// RFC3339, so lexical order is chronological; ties keep fan-out order, which
// keeps rendering stable across refreshes.
func sortByUpdated(changes []Change) {
	sort.SliceStable(changes, func(i, j int) bool {
		return changes[i].UpdatedAt > changes[j].UpdatedAt
	})
}

// --- routed reads and writes ---

// routeRepo runs fn against the member owning repo.
func (f *Fleet) routeRepo(repo string, fn func(Forge) error) error {
	m, err := f.ForRepo(repo)
	if err != nil {
		return err
	}
	return fn(m)
}

func (f *Fleet) ChangeDetail(ctx context.Context, repo, id string) (*ChangeDetail, error) {
	m, err := f.ForRepo(repo)
	if err != nil {
		return nil, err
	}
	return m.ChangeDetail(ctx, repo, id)
}

func (f *Fleet) ChangeDiff(ctx context.Context, repo, id string) (*Diff, error) {
	m, err := f.ForRepo(repo)
	if err != nil {
		return nil, err
	}
	return m.ChangeDiff(ctx, repo, id)
}

func (f *Fleet) PipelineWithJobs(ctx context.Context, repo string, pipelineID int64) (*Pipeline, error) {
	m, err := f.ForRepo(repo)
	if err != nil {
		return nil, err
	}
	return m.PipelineWithJobs(ctx, repo, pipelineID)
}

func (f *Fleet) JobLog(ctx context.Context, repo string, jobID int64) (string, error) {
	m, err := f.ForRepo(repo)
	if err != nil {
		return "", err
	}
	return m.JobLog(ctx, repo, jobID)
}

func (f *Fleet) Approve(ctx context.Context, repo, id string) error {
	return f.routeRepo(repo, func(m Forge) error { return m.Approve(ctx, repo, id) })
}

func (f *Fleet) Unapprove(ctx context.Context, repo, id string) error {
	return f.routeRepo(repo, func(m Forge) error { return m.Unapprove(ctx, repo, id) })
}

func (f *Fleet) Merge(ctx context.Context, repo, id string, autoMerge bool) (MergeOutcome, error) {
	m, err := f.ForRepo(repo)
	if err != nil {
		return MergeOutcomeMerged, err
	}
	return m.Merge(ctx, repo, id, autoMerge)
}

func (f *Fleet) UpdateBranch(ctx context.Context, repo, id string) error {
	return f.routeRepo(repo, func(m Forge) error { return m.UpdateBranch(ctx, repo, id) })
}

func (f *Fleet) SetDraft(ctx context.Context, repo, id, currentTitle string, draft bool) error {
	return f.routeRepo(repo, func(m Forge) error {
		return m.SetDraft(ctx, repo, id, currentTitle, draft)
	})
}

func (f *Fleet) AddComment(ctx context.Context, repo, id, body string) error {
	return f.routeRepo(repo, func(m Forge) error { return m.AddComment(ctx, repo, id, body) })
}

func (f *Fleet) AddDiffComment(ctx context.Context, repo, id string, dc DiffComment) error {
	return f.routeRepo(repo, func(m Forge) error { return m.AddDiffComment(ctx, repo, id, dc) })
}

func (f *Fleet) RetryJob(ctx context.Context, repo string, jobID int64) error {
	return f.routeRepo(repo, func(m Forge) error { return m.RetryJob(ctx, repo, jobID) })
}

func (f *Fleet) CancelJob(ctx context.Context, repo string, jobID int64) error {
	return f.routeRepo(repo, func(m Forge) error { return m.CancelJob(ctx, repo, jobID) })
}

// DiffURL routes by repo; an unroutable repo yields "" rather than a URL
// pointing at the wrong host.
func (f *Fleet) DiffURL(repo, id string) string {
	m, err := f.ForRepo(repo)
	if err != nil {
		return ""
	}
	return m.DiffURL(repo, id)
}

// A Fleet is itself a Forge, which is what lets the TUI stay unaware of how
// many hosts are behind it.
var _ Forge = (*Fleet)(nil)

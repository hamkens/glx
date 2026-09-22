package forge

import (
	"context"
	"errors"
	"testing"
)

// fakeForge is a scriptable Forge for exercising fan-out and routing without a
// network. Each instance stands in for one host.
type fakeForge struct {
	host     string
	provider Provider
	user     string
	caps     Capabilities

	changes []Change
	merged  []Change
	err     error // returned by every read

	// calls records which routed methods ran, so a test can assert that a write
	// reached exactly one host.
	calls []string
}

func (f *fakeForge) Provider() Provider         { return f.provider }
func (f *fakeForge) Host() string               { return f.host }
func (f *fakeForge) Username() string           { return f.user }
func (f *fakeForge) Capabilities() Capabilities { return f.caps }

func (f *fakeForge) CurrentUser(context.Context) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	return f.user, f.host + " v1", nil
}

func (f *fakeForge) Changes(_ context.Context, _ Scope, _ string, _ int) (*ChangePage, error) {
	if f.err != nil {
		return nil, f.err
	}
	// Return a copy: the fleet stamps Host onto what it gets back, and a shared
	// backing array would let one call mutate the fixture for the next.
	out := make([]Change, len(f.changes))
	copy(out, f.changes)
	return &ChangePage{Changes: out}, nil
}

func (f *fakeForge) MergedSince(context.Context, Scope, string, int) ([]Change, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Change, len(f.merged))
	copy(out, f.merged)
	return out, nil
}

func (f *fakeForge) ChangeDetail(_ context.Context, repo, id string) (*ChangeDetail, error) {
	f.calls = append(f.calls, "detail")
	return &ChangeDetail{ID: id, Repo: repo}, nil
}

func (f *fakeForge) ChangeDiff(context.Context, string, string) (*Diff, error) {
	f.calls = append(f.calls, "diff")
	return &Diff{}, nil
}

func (f *fakeForge) PipelineWithJobs(_ context.Context, _ string, id int64) (*Pipeline, error) {
	f.calls = append(f.calls, "pipeline")
	return &Pipeline{ID: id}, nil
}

func (f *fakeForge) JobLog(context.Context, string, int64) (string, error) {
	f.calls = append(f.calls, "joblog")
	return "log", nil
}

func (f *fakeForge) Approve(context.Context, string, string) error {
	f.calls = append(f.calls, "approve")
	return nil
}

func (f *fakeForge) Unapprove(context.Context, string, string) error {
	f.calls = append(f.calls, "unapprove")
	return nil
}

func (f *fakeForge) Merge(context.Context, string, string, bool) (MergeOutcome, error) {
	f.calls = append(f.calls, "merge")
	return MergeOutcomeMerged, nil
}

func (f *fakeForge) UpdateBranch(context.Context, string, string) error {
	f.calls = append(f.calls, "updatebranch")
	return nil
}

func (f *fakeForge) SetDraft(context.Context, string, string, string, bool) error {
	f.calls = append(f.calls, "setdraft")
	return nil
}

func (f *fakeForge) AddComment(context.Context, string, string, string) error {
	f.calls = append(f.calls, "comment")
	return nil
}

func (f *fakeForge) AddDiffComment(context.Context, string, string, DiffComment) error {
	f.calls = append(f.calls, "diffcomment")
	return nil
}

func (f *fakeForge) RetryJob(context.Context, string, int64) error {
	f.calls = append(f.calls, "retry")
	return nil
}

func (f *fakeForge) CancelJob(context.Context, string, int64) error {
	f.calls = append(f.calls, "cancel")
	return nil
}

func (f *fakeForge) DiffURL(repo, id string) string {
	return "https://" + f.host + "/" + repo + "/" + id
}

var _ Forge = (*fakeForge)(nil)

func gitlabHost() *fakeForge {
	return &fakeForge{
		host: "gitlab.example.net", provider: ProviderGitLab, user: "me",
		caps: Capabilities{AutoMerge: true, Unapprove: true, DraftToggle: true},
		changes: []Change{
			{ID: "10", Repo: "grp/api", Title: "gitlab newer", UpdatedAt: "2026-07-27T10:00:00Z"},
			{ID: "11", Repo: "grp/api", Title: "gitlab older", UpdatedAt: "2026-07-20T10:00:00Z"},
		},
		merged: []Change{{ID: "9", Repo: "grp/api", MergedAt: "2026-07-26T10:00:00Z"}},
	}
}

func githubHost() *fakeForge {
	return &fakeForge{
		host: "github.com", provider: ProviderGitHub, user: "me",
		caps:    Capabilities{AutoMerge: true, CancelJobIsRunWide: true, DraftToggle: true},
		changes: []Change{{ID: "42", Repo: "acme/widget", Title: "github middle", UpdatedAt: "2026-07-25T10:00:00Z"}},
		merged:  []Change{{ID: "41", Repo: "acme/widget", MergedAt: "2026-07-27T09:00:00Z"}},
	}
}

// The whole point of the merged list: one request, rows from every host,
// newest-updated first, each tagged with where it came from.
func TestFleetChangesMergesAndSortsByUpdated(t *testing.T) {
	f := NewFleet(gitlabHost(), githubHost())
	page, err := f.Changes(context.Background(), ScopeReviewer, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 3 {
		t.Fatalf("got %d changes, want 3", len(page.Changes))
	}
	wantOrder := []string{"gitlab newer", "github middle", "gitlab older"}
	for i, want := range wantOrder {
		if page.Changes[i].Title != want {
			t.Errorf("changes[%d] = %q, want %q", i, page.Changes[i].Title, want)
		}
	}
	// Provenance is what makes a row actionable later.
	for _, c := range page.Changes {
		if c.Host == "" {
			t.Errorf("change %s has no Host stamped", c.ID)
		}
	}
	// A merged list has no single cursor to page with.
	if page.HasNextPage {
		t.Error("HasNextPage = true; a merged list cannot paginate coherently")
	}
}

func TestFleetMergedSinceSortsNewestFirst(t *testing.T) {
	f := NewFleet(gitlabHost(), githubHost())
	merged, err := f.MergedSince(context.Background(), ScopeAuthored, "2026-07-20T00:00:00Z", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 {
		t.Fatalf("got %d, want 2", len(merged))
	}
	if merged[0].ID != "41" {
		t.Errorf("first = %s, want 41 (merged most recently)", merged[0].ID)
	}
}

// One dead host must not blank the list — that's the difference between "no work
// to do" and "half your work is invisible".
func TestFleetChangesDegradesWhenOneHostFails(t *testing.T) {
	gl, gh := gitlabHost(), githubHost()
	gh.err = errors.New("401 bad credentials")
	f := NewFleet(gl, gh)

	page, err := f.Changes(context.Background(), ScopeReviewer, "", 50)
	if err != nil {
		t.Fatalf("err = %v; a partial result must still succeed", err)
	}
	if len(page.Changes) != 2 {
		t.Fatalf("got %d changes, want the 2 from the healthy host", len(page.Changes))
	}
	if got := f.Degraded(); len(got) != 1 || got[0] != "github.com" {
		t.Errorf("Degraded() = %v, want [github.com]", got)
	}
}

// With every host down there is nothing to show, so this must be a real error
// rather than an empty list that reads as "all clear".
func TestFleetChangesErrorsWhenAllHostsFail(t *testing.T) {
	gl, gh := gitlabHost(), githubHost()
	gl.err = errors.New("gitlab down")
	gh.err = errors.New("github down")
	f := NewFleet(gl, gh)

	if _, err := f.Changes(context.Background(), ScopeReviewer, "", 50); err == nil {
		t.Fatal("expected an error when no host answers")
	}
}

// Writes must reach exactly the host that owns the change; hitting the wrong one
// would approve or merge something else entirely.
func TestFleetRoutesWritesToTheOwningHost(t *testing.T) {
	gl, gh := gitlabHost(), githubHost()
	f := NewFleet(gl, gh)
	// Populate the repo index the way the list view does.
	if _, err := f.Changes(context.Background(), ScopeReviewer, "", 50); err != nil {
		t.Fatal(err)
	}

	if err := f.Approve(context.Background(), "acme/widget", "42"); err != nil {
		t.Fatal(err)
	}
	if len(gh.calls) != 1 || gh.calls[0] != "approve" {
		t.Errorf("github calls = %v, want [approve]", gh.calls)
	}
	if len(gl.calls) != 0 {
		t.Errorf("gitlab calls = %v, want none", gl.calls)
	}

	if err := f.UpdateBranch(context.Background(), "grp/api", "10"); err != nil {
		t.Fatal(err)
	}
	if len(gl.calls) != 1 || gl.calls[0] != "updatebranch" {
		t.Errorf("gitlab calls = %v, want [updatebranch]", gl.calls)
	}
}

// Acting on a repo no host has served yet must fail loudly rather than guess,
// since guessing means writing to the wrong forge.
func TestFleetRefusesUnknownRepo(t *testing.T) {
	f := NewFleet(gitlabHost(), githubHost())
	if err := f.Approve(context.Background(), "who/knows", "1"); err == nil {
		t.Fatal("expected an error for an unrouted repo")
	}
	if url := f.DiffURL("who/knows", "1"); url != "" {
		t.Errorf("DiffURL = %q, want empty rather than a wrong-host URL", url)
	}
}

// A one-host fleet must behave exactly like that host, so the common case pays
// nothing for the multi-host machinery.
func TestSingleMemberFleetIsTransparent(t *testing.T) {
	gl := gitlabHost()
	f := NewFleet(gl)

	if _, ok := f.Single(); !ok {
		t.Error("Single() = false for a one-member fleet")
	}
	if f.Provider() != ProviderGitLab {
		t.Errorf("Provider() = %v, want gitlab", f.Provider())
	}
	if f.Host() != "gitlab.example.net" {
		t.Errorf("Host() = %q", f.Host())
	}
	// Routing works without a prior list call, because there's only one answer.
	if err := f.Approve(context.Background(), "never/listed", "1"); err != nil {
		t.Errorf("Approve on a single-member fleet errored: %v", err)
	}
	// Single-member reads still stamp provenance, so rows stay routable.
	page, err := f.Changes(context.Background(), ScopeReviewer, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Changes[0].Host != "gitlab.example.net" {
		t.Errorf("Host = %q, want it stamped even for one member", page.Changes[0].Host)
	}
}

func TestFleetProviderIsMixedAcrossProducts(t *testing.T) {
	f := NewFleet(gitlabHost(), githubHost())
	if f.Provider() != ProviderMixed {
		t.Errorf("Provider() = %v, want mixed", f.Provider())
	}
	if got := Vocab(f.Provider()).Change; got != "change" {
		t.Errorf("mixed vocabulary Change = %q, want the neutral noun", got)
	}
	// Two GitLab hosts are still unambiguously GitLab.
	a, b := gitlabHost(), gitlabHost()
	b.host = "gitlab.other.net"
	if p := NewFleet(a, b).Provider(); p != ProviderGitLab {
		t.Errorf("Provider() = %v, want gitlab for a same-product fleet", p)
	}
}

// Per-row wording is what keeps a merged list honest: "!10" for the GitLab row,
// "#42" for the GitHub one.
func TestFleetVocabAndCapsAreResolvedPerRow(t *testing.T) {
	f := NewFleet(gitlabHost(), githubHost())
	page, err := f.Changes(context.Background(), ScopeReviewer, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var gl, gh Change
	for _, c := range page.Changes {
		switch c.Host {
		case "gitlab.example.net":
			gl = c
		case "github.com":
			gh = c
		}
	}
	if got := f.VocabFor(gl).IDPrefix; got != "!" {
		t.Errorf("gitlab row prefix = %q, want !", got)
	}
	if got := f.VocabFor(gh).IDPrefix; got != "#" {
		t.Errorf("github row prefix = %q, want #", got)
	}
	if got := f.VocabFor(gh).Pipeline; got != "workflow run" {
		t.Errorf("github row pipeline noun = %q", got)
	}
	// GitLab can unapprove, GitHub can't; the row's own host decides.
	if !f.CapsFor(gl).Unapprove {
		t.Error("gitlab row: Unapprove = false")
	}
	if f.CapsFor(gh).Unapprove {
		t.Error("github row: Unapprove = true, want false")
	}
}

// Fleet-wide capabilities gate help text, so an action is only advertised when
// every host can do it — but a warning that applies anywhere must still show.
func TestFleetCapabilitiesIntersect(t *testing.T) {
	caps := NewFleet(gitlabHost(), githubHost()).Capabilities()
	if !caps.AutoMerge {
		t.Error("AutoMerge = false; both hosts support it")
	}
	if caps.Unapprove {
		t.Error("Unapprove = true; only one host supports it")
	}
	if !caps.CancelJobIsRunWide {
		t.Error("CancelJobIsRunWide = false; the warning applies to one host, so it must show")
	}
}

// CurrentUser is the startup gate. One bad token must not lock the user out of
// the hosts that work; all bad tokens must fail.
func TestFleetCurrentUserSurvivesOneBadHost(t *testing.T) {
	gl, gh := gitlabHost(), githubHost()
	gh.err = errors.New("401")
	f := NewFleet(gl, gh)

	user, _, err := f.CurrentUser(context.Background())
	if err != nil {
		t.Fatalf("err = %v; one healthy host should be enough", err)
	}
	if user != "me" {
		t.Errorf("user = %q", user)
	}
	if got := f.Degraded(); len(got) != 1 || got[0] != "github.com" {
		t.Errorf("Degraded() = %v", got)
	}

	gl2, gh2 := gitlabHost(), githubHost()
	gl2.err, gh2.err = errors.New("x"), errors.New("y")
	if _, _, err := NewFleet(gl2, gh2).CurrentUser(context.Background()); err == nil {
		t.Error("expected an error when no host authenticates")
	}
}

// A refreshed read must clear a stale failure, or the status bar would keep
// warning about a host that has since recovered.
func TestFleetHealthRecovers(t *testing.T) {
	gl, gh := gitlabHost(), githubHost()
	gh.err = errors.New("transient")
	f := NewFleet(gl, gh)

	if _, err := f.Changes(context.Background(), ScopeReviewer, "", 50); err != nil {
		t.Fatal(err)
	}
	if len(f.Degraded()) != 1 {
		t.Fatalf("Degraded() = %v, want one entry", f.Degraded())
	}
	gh.err = nil
	if _, err := f.Changes(context.Background(), ScopeReviewer, "", 50); err != nil {
		t.Fatal(err)
	}
	if got := f.Degraded(); len(got) != 0 {
		t.Errorf("Degraded() = %v, want empty after recovery", got)
	}
}

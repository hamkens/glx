package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/forge"
)

type filterForge struct {
	forge.Forge
	pages     map[string]*forge.ChangePage
	requested []string
}

func (f *filterForge) Provider() forge.Provider         { return forge.ProviderGitLab }
func (f *filterForge) Capabilities() forge.Capabilities { return forge.Capabilities{} }
func (f *filterForge) Host() string                     { return "gitlab.example.net" }
func (f *filterForge) Changes(_ context.Context, _ forge.Scope, cursor string, _ int) (*forge.ChangePage, error) {
	f.requested = append(f.requested, cursor)
	page, ok := f.pages[cursor]
	if !ok {
		return nil, errors.New("unexpected cursor: " + cursor)
	}
	return page, nil
}

func filterModel() mrListModel {
	m := newMRListModel(&filterForge{})
	m.width, m.height, m.loading = 100, 24, false
	return m
}

func filterKey(m mrListModel, key tea.KeyType) mrListModel {
	m, _ = m.handleKey(tea.KeyMsg{Type: key})
	return m
}

func filterText(m mrListModel, text string) mrListModel {
	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	return m
}

func TestListFilterCombinesTextAndInclusions(t *testing.T) {
	changes := []forge.Change{
		{ID: "1", Title: "Fix auth", Repo: "team/api", SourceBranch: "feature/login", Approved: true},
		{ID: "2", Title: "Fix auth", Repo: "team/api", SourceBranch: "feature/login", Draft: true},
		{ID: "3", Title: "Fix auth", Repo: "team/api", SourceBranch: "feature/login", ApprovedByMe: true},
		{ID: "4", Title: "Fix auth", Repo: "team/api", SourceBranch: "feature/login", MergedAt: "2026-11-05T10:00:00Z"},
		{ID: "5", Title: "Other", Repo: "team/other", SourceBranch: "main"},
	}
	m := filterModel()
	if got := m.applyFilter(changes); len(got) != len(changes) {
		t.Fatal("default filter must include everything")
	}
	m.filters[0] = listFilter{query: "  FIX  TEAM/API LOGIN ", hideDrafts: true, hideApprovedMe: true, hideMerged: true}
	got := m.applyFilter(changes)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got %+v; want MR 1, approved by others", got)
	}
	m.filters[0] = listFilter{query: " \t "}
	if m.filters[0].active() || len(m.applyFilter(changes)) != 5 {
		t.Fatal("whitespace must not narrow the list")
	}
}

func TestFilterModalApplyCancelResetAndTabMemory(t *testing.T) {
	m := filterModel()
	m, _ = m.openFilters()
	if !m.busy() || !m.filterInput.Focused() {
		t.Fatal("search should own keys when opened")
	}
	m = filterText(m, "api fix")
	m = filterKey(m, tea.KeyTab)
	m = filterText(m, " ") // hide drafts
	m = filterKey(m, tea.KeyTab)
	m = filterText(m, " ") // hide my approvals
	m = filterKey(m, tea.KeyTab)
	m = filterText(m, " ") // hide merged
	if m.filters[0].active() {
		t.Fatal("modal edits must wait for Apply")
	}
	m = filterKey(m, tea.KeyEnter)
	want := listFilter{query: "api fix", hideDrafts: true, hideApprovedMe: true, hideMerged: true}
	if m.filters[0] != want || m.busy() {
		t.Fatalf("applied filter = %+v, busy = %v", m.filters[0], m.busy())
	}

	m = filterText(m, "r")
	if m.filters[0] != want {
		t.Fatal("refresh cleared filters")
	}
	m = filterKey(m, tea.KeyTab) // Authored
	if m.filters[m.scopeIdx].active() {
		t.Fatal("Authored inherited Reviews filters")
	}
	m, _ = m.openFilters()
	m = filterText(m, "my work")
	m = filterKey(m, tea.KeyEnter)
	m = filterKey(m, tea.KeyShiftTab) // Reviews
	if m.filters[0] != want || m.filters[1].query != "my work" {
		t.Fatal("tabs must remember independent filters")
	}

	m, _ = m.openFilters()
	m = filterKey(m, tea.KeyShiftTab) // Reset
	m = filterKey(m, tea.KeyEnter)
	if m.filterDraft.active() || m.filterInput.Value() != "" || !m.filterInput.Focused() {
		t.Fatal("Reset must clear draft and refocus search")
	}
	m = filterKey(m, tea.KeyEsc)
	if m.filters[0] != want {
		t.Fatal("canceling Reset must retain applied settings")
	}
	m, _ = m.openFilters()
	m = filterKey(m, tea.KeyShiftTab)
	m = filterText(m, " ")
	m = filterKey(m, tea.KeyEnter)
	if m.filters[0].active() {
		t.Fatal("applying Reset should restore all inclusions")
	}
	if m.filters[1].query != "my work" {
		t.Fatal("Reset affected another tab")
	}
}

func TestAssignedFilterSkipsMergedControl(t *testing.T) {
	m := filterModel()
	m.scopeIdx = 2
	m, _ = m.openFilters()
	if strings.Contains(m.filterView(), "recently merged") {
		t.Fatal("Assigned should not offer merged history")
	}
	m = filterKey(m, tea.KeyShiftTab)
	if m.filterFocus != 4 {
		t.Fatal("last Assigned control should be Reset")
	}
	m = filterText(m, " ")
	if m.filterDraft.hideMerged {
		t.Fatal("Reset must not toggle merged in Assigned")
	}
}

func TestApplyingFiltersStartsSearchAndEscapeClearsOnlyCurrentTab(t *testing.T) {
	m := filterModel()
	m.mrs = []forge.Change{{ID: "draft", Draft: true}}
	m.hasNext, m.cursor = true, "next"
	m.filters[1] = listFilter{query: "authored"}
	m.rebuildRows()
	m = filterText(m, "/")
	m = filterKey(m, tea.KeyDown)
	m = filterText(m, " ")
	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.loadingMore || len(m.rows) != 0 {
		t.Fatal("applying filters must search beyond the loaded drafts")
	}
	m = filterKey(m, tea.KeyEsc)
	if m.filters[0].active() || len(m.visibleMRs()) != 1 || m.filters[1].query != "authored" {
		t.Fatal("Escape should clear only this tab and restore its hidden rows")
	}
}

func TestFiltersFindMatchesBeyondEmptyPages(t *testing.T) {
	client := &filterForge{pages: map[string]*forge.ChangePage{
		"a": {Changes: []forge.Change{{ID: "2", Draft: true}}, HasNextPage: true, EndCursor: "b"},
		"b": {Changes: []forge.Change{{ID: "3", Title: "Match", Approved: true}}},
	}}
	m := newMRListModel(client)
	m.filters[0] = listFilter{hideDrafts: true, hideApprovedMe: true}
	m, cmd := m.Update(mrsLoadedMsg{scope: forge.ScopeReviewer, page: &forge.ChangePage{
		Changes: []forge.Change{{ID: "1", Draft: true}}, HasNextPage: true, EndCursor: "a",
	}})
	if !m.loadingMore || len(m.rows) != 0 || cmd == nil {
		t.Fatal("empty first page must trigger searching")
	}
	// Execute only page results; spinner messages are irrelevant to pagination.
	for step := 0; cmd != nil && step < 4; step++ {
		batch, ok := cmd().(tea.BatchMsg)
		if !ok {
			t.Fatal("expected load-more batch")
		}
		cmd = nil
		for _, child := range batch {
			switch msg := child().(type) {
			case mrsLoadedMsg:
				m, cmd = m.Update(msg)
			case mrsErrMsg:
				t.Fatal(msg.err)
			}
		}
	}
	if cmd != nil || m.loadingMore || m.hasNext {
		t.Fatal("search should finish after last page")
	}
	if got := m.visibleMRs(); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("later match missing: %+v", got)
	}
	if strings.Join(client.requested, ",") != "a,b" {
		t.Fatalf("requested %v", client.requested)
	}
	if !strings.Contains(m.bottomLine(), "1 shown / 3 loaded") {
		t.Fatal("filtered result count missing")
	}
}

func TestFilterPaginationRejectsStaleResponsesAndStuckCursors(t *testing.T) {
	m := filterModel()
	m.resetPaging()
	m, _ = m.Update(mrsLoadedMsg{scope: m.scope(), page: &forge.ChangePage{Changes: []forge.Change{{ID: "stale"}}}})
	m, _ = m.Update(mrsErrMsg{scope: m.scope(), err: errors.New("stale")})
	if !m.loading || m.err != nil || len(m.mrs) != 0 {
		t.Fatal("stale response changed current load")
	}
	m.cursor = "same"
	m, cmd := m.Update(mrsLoadedMsg{scope: m.scope(), generation: m.generation, append: true,
		page: &forge.ChangePage{HasNextPage: true, EndCursor: "same"}})
	if m.err == nil || cmd != nil || m.loadingMore {
		t.Fatal("stuck cursor must stop pagination with an error")
	}
}

func TestFilterModalCapturesGlobalKeys(t *testing.T) {
	r := New(&filterForge{}).(rootModel)
	r.mrList.loading = false
	r.mrList, _ = r.mrList.openFilters()
	for _, char := range []string{"?", "o", "w", "W", "q", "D", "a", "r", "/"} {
		updated, _ := r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(char)})
		r = updated.(rootModel)
	}
	if r.showHelp || r.view != viewList || r.mrList.filterInput.Value() != "?owWqDar/" {
		t.Fatal("global shortcuts stole modal input")
	}
	r.mrList = filterKey(r.mrList, tea.KeyTab)
	updated, _ := r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if updated.(rootModel).showHelp {
		t.Fatal("checkbox focus should also suppress global shortcuts")
	}
}

func TestListPagesArriveWhileViewingDetail(t *testing.T) {
	r := New(&filterForge{}).(rootModel)
	r.view = viewDetail
	updated, _ := r.Update(mrsLoadedMsg{scope: forge.ScopeReviewer, page: &forge.ChangePage{Changes: []forge.Change{{ID: "1"}}}})
	r = updated.(rootModel)
	if r.view != viewDetail || len(r.mrList.mrs) != 1 || r.mrList.loading {
		t.Fatal("background list results were lost")
	}
}

func TestFilterRowsHideMergedAndKeepSelectionOnAppend(t *testing.T) {
	m := filterModel()
	m.mrs = []forge.Change{{ID: "1", Repo: "a"}, {ID: "2", Repo: "b"}}
	m.merged = []forge.Change{{ID: "3", Repo: "a", MergedAt: "2026-11-05T10:00:00Z"}}
	m.rebuildRows()
	for i, row := range m.rows {
		if row.selectable() && row.mr.ID == "2" {
			m.cur = i
		}
	}
	m.mrs = append(m.mrs, forge.Change{ID: "4", Repo: "a"})
	m.filters[0].hideMerged = true
	m.rebuildRows()
	selected, ok := m.selected()
	if !ok || selected.ID != "2" {
		t.Fatal("background pages moved selection to another MR")
	}
	for _, row := range m.rows {
		if row.isSection || row.mr.MergedAt != "" {
			t.Fatal("merged section should be hidden")
		}
	}
}

func TestFilterModalFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {40, 16}} {
		m := filterModel()
		m.width, m.height = size[0], size[1]
		m, _ = m.openFilters()
		m = filterText(m, strings.Repeat("long search ", 20))
		view := m.View()
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
			t.Fatalf("modal exceeds %dx%d: %dx%d", m.width, m.height, lipgloss.Width(view), lipgloss.Height(view))
		}
		for _, label := range []string{"Filters · Reviews", "Include drafts", "approved by me", "Include recently merged", "Include awaiting author", "Reset", "Enter apply", "Esc cancel"} {
			if !strings.Contains(view, label) {
				t.Fatalf("missing %q at %dx%d", label, m.width, m.height)
			}
		}
		if m.width == 100 {
			t.Log("\n" + view)
		}
	}
}

func TestAwaitingAuthorFilterUsesMyReviewState(t *testing.T) {
	m := filterModel()
	m.mrs = []forge.Change{
		{ID: "unreviewed", ReviewState: forge.ReviewStateRequested},
		{ID: "commented", ReviewState: forge.ReviewStateReviewed},
		{ID: "changes", ReviewState: forge.ReviewStateChangesRequested},
		{ID: "approved", ReviewState: forge.ReviewStateApproved, ApprovedByMe: true},
		{ID: "unknown", ReviewState: forge.ReviewStateNone},
		{ID: "others-requested-changes", ReviewState: forge.ReviewStateRequested, MergeState: forge.MergeStateChangesRequested},
	}
	m.filters[0] = listFilter{hideAwaitingAuthor: true}
	var got []string
	for _, mr := range m.visibleMRs() {
		got = append(got, mr.ID)
	}
	if strings.Join(got, ",") != "unreviewed,approved,unknown,others-requested-changes" {
		t.Fatalf("visible = %v; only my completed feedback should be hidden", got)
	}
	if !strings.Contains(m.bottomLine(), "not awaiting author") || !strings.Contains(m.bottomLine(), "4 shown / 6 loaded") {
		t.Fatal("awaiting-author filter and count must appear outside the modal")
	}
	m.filters[0].hideApprovedMe = true
	if len(m.visibleMRs()) != 3 {
		t.Fatal("approval filter must combine with the awaiting-author filter")
	}
}

func TestAwaitingAuthorToggleAcrossTabs(t *testing.T) {
	for tab := range scopeOrder {
		m := filterModel()
		m.scopeIdx = tab
		m, _ = m.openFilters()
		for step := 0; step < m.filterAwaitingAuthorIndex(); step++ {
			m = filterKey(m, tea.KeyTab)
		}
		m = filterText(m, " ")
		if !m.filterDraft.hideAwaitingAuthor || m.filterDraft.hideMerged || m.filters[tab].active() {
			t.Fatalf("tab %d: awaiting-author toggle changed the wrong state", tab)
		}
		m = filterKey(m, tea.KeyEsc)
		if m.filters[tab].active() {
			t.Fatal("cancel must discard awaiting-author edits")
		}
		m, _ = m.openFilters()
		for step := 0; step < m.filterAwaitingAuthorIndex(); step++ {
			m = filterKey(m, tea.KeyTab)
		}
		m = filterText(m, " ")
		m = filterKey(m, tea.KeyEnter)
		if m.filterMode || !m.filters[tab].hideAwaitingAuthor {
			t.Fatal("Enter must apply the awaiting-author filter")
		}
		m = filterText(m, "r")
		if !m.filters[tab].hideAwaitingAuthor {
			t.Fatal("refresh must retain awaiting-author preference")
		}
		m, _ = m.openFilters()
		m = filterKey(m, tea.KeyShiftTab)
		m = filterKey(m, tea.KeyEnter)
		m = filterKey(m, tea.KeyEnter)
		if m.filters[tab].active() {
			t.Fatal("Reset must include awaiting-author reviews again")
		}
	}
}

func TestCompletedFeedbackGetsReviewedFlag(t *testing.T) {
	m := filterModel()
	m.rows = []mrRow{{mr: forge.Change{ID: "1", ReviewState: forge.ReviewStateReviewed}}}
	if !strings.Contains(m.renderRow(0, ""), "reviewed") {
		t.Fatal("reviewed changes should explain their category in the list")
	}
	m.rows[0].mr.ReviewState = forge.ReviewStateRequested
	if strings.Contains(m.renderRow(0, ""), "reviewed") {
		t.Fatal("new review requests should not have a completed-feedback flag")
	}
}

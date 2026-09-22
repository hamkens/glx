package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"

	"github.com/hamkens/glx/internal/forge"
)

// scopeOrder defines the tab cycle and labels for change scopes.
var scopeOrder = []struct {
	scope forge.Scope
	label string
}{
	{forge.ScopeReviewer, "Reviews"},
	{forge.ScopeAuthored, "Authored"},
	{forge.ScopeAssigned, "Assigned"},
}

// mrRow is a flattened display row: a section header ("Open"/"Merged"), a
// project header, a blank spacer, or a change row.
type mrRow struct {
	isSection bool
	isHeader  bool
	isSpacer  bool
	section   string // for isSection
	project   string // for isHeader
	host      string // for isHeader, when several hosts are connected
	mr        forge.Change
}

func (r mrRow) selectable() bool { return !r.isHeader && !r.isSpacer && !r.isSection }

// mergedWindow is how far back the "recently merged" section reaches.
const mergedWindow = 12 * time.Hour

// --- messages ---

type mrsLoadedMsg struct {
	scope  forge.Scope
	page   *forge.ChangePage
	append bool // true when this is a "load more" page to append
}

// mergedLoadedMsg carries the recently-merged changes for a scope.
type mergedLoadedMsg struct {
	scope forge.Scope
	mrs   []forge.Change
}

// scopeSummaryLoadedMsg carries the complete open workload for a scope. It is
// separate from mrsLoadedMsg so the list itself can remain paginated.
type scopeSummaryLoadedMsg struct {
	scope forge.Scope
	mrs   []forge.Change
}
type mrsErrMsg struct{ err error }

type scopeSummary struct {
	pending int
	stale   int
	oldest  time.Duration

	ready     int
	blocked   int
	drafts    int
	approvals int
	ciFailed  int
	ciRunning int
	conflicts int
	behind    int // source branch is behind its target
	threads   int
}

// --- model ---

type mrListModel struct {
	client forge.Forge
	// fleet is non-nil when more than one host is connected. It resolves the
	// per-row vocabulary and capabilities a merged list needs, since a GitLab
	// row and a GitHub row on the same screen want different words.
	fleet       *forge.Fleet
	vocab       forge.Vocabulary
	caps        forge.Capabilities
	spinner     spinner.Model
	filterInput textinput.Model
	scopeIdx    int

	mrs    []forge.Change // open changes, in server order
	merged []forge.Change // recently-merged changes (Authored/Reviews only)
	rows   []mrRow        // derived display rows (headers + spacers + changes)
	cur    int            // index into rows; always points at a selectable row
	scroll int            // index of the top visible row

	filterMode  bool
	loading     bool
	loadingMore bool
	cursor      string // EndCursor of the last loaded page
	hasNext     bool   // more pages available
	err         error
	flash       string    // transient action result/status
	confirm     inputMode // modeConfirmMerge / modeConfirmAutoMerge, else modeNone
	width       int
	height      int
	lastSynced  string
	summaries   map[forge.Scope]scopeSummary
}

func newMRListModel(client forge.Forge) mrListModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter by title, project, or branch"

	m := mrListModel{
		client: client, spinner: sp, filterInput: ti, loading: true,
		vocab:     forge.Vocab(client.Provider()),
		caps:      client.Capabilities(),
		summaries: make(map[forge.Scope]scopeSummary),
	}
	// Only treat a Fleet as multi-host when it actually spans hosts; a
	// single-member fleet keeps the plain single-host rendering.
	if f, ok := client.(*forge.Fleet); ok {
		if _, single := f.Single(); !single {
			m.fleet = f
		}
	}
	return m
}

// multiHost reports whether rows can come from different hosts, which turns on
// the host column and per-row wording.
func (m mrListModel) multiHost() bool { return m.fleet != nil }

// vocabFor is the wording to use for one row: the owning host's on a merged
// list, the single connection's otherwise.
func (m mrListModel) vocabFor(mr forge.Change) forge.Vocabulary {
	if m.fleet == nil {
		return m.vocab
	}
	return m.fleet.VocabFor(mr)
}

// capsFor is the capability set governing actions on one row.
func (m mrListModel) capsFor(mr forge.Change) forge.Capabilities {
	if m.fleet == nil {
		return m.caps
	}
	return m.fleet.CapsFor(mr)
}

func (m mrListModel) scope() forge.Scope { return scopeOrder[m.scopeIdx].scope }

// selected returns the change under the cursor, or false if there is none.
func (m mrListModel) selected() (forge.Change, bool) {
	if m.cur >= 0 && m.cur < len(m.rows) && m.rows[m.cur].selectable() {
		return m.rows[m.cur].mr, true
	}
	return forge.Change{}, false
}

// filtering reports whether the filter input is currently capturing keys.
func (m mrListModel) filtering() bool { return m.filterMode }

// busy reports whether the list is capturing input (filter) or awaiting a
// y/N confirmation, so the root shouldn't steal single-key shortcuts.
func (m mrListModel) busy() bool { return m.filterMode || m.confirm != modeNone }

// pageSize is how many changes to request per page.
const pageSize = 50

// scopeHasMerged reports whether the current scope shows a recently-merged
// section. Assigned changes aren't tracked for merge history here.
func (m mrListModel) scopeHasMerged() bool {
	s := m.scope()
	return s == forge.ScopeAuthored || s == forge.ScopeReviewer
}

// fetchCmd loads the first page for the current scope. When force is true the
// cache is bypassed (used by the "r" refresh key).
func (m mrListModel) fetchCmd(force bool) tea.Cmd {
	scope := m.scope()
	client := m.client
	cmds := []tea.Cmd{func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if force {
			ctx = forge.WithForceRefresh(ctx)
		}
		page, err := client.Changes(ctx, scope, "", pageSize)
		if err != nil {
			return mrsErrMsg{err}
		}
		return mrsLoadedMsg{scope: scope, page: page}
	}}
	if m.scopeHasMerged() {
		cmds = append(cmds, m.mergedCmd(force))
	}
	return tea.Batch(cmds...)
}

// mergedCmd loads changes merged within mergedWindow for the current scope.
func (m mrListModel) mergedCmd(force bool) tea.Cmd {
	scope := m.scope()
	client := m.client
	since := time.Now().Add(-mergedWindow).UTC().Format(time.RFC3339)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if force {
			ctx = forge.WithForceRefresh(ctx)
		}
		mrs, err := client.MergedSince(ctx, scope, since, 50)
		if err != nil {
			return mrsErrMsg{err}
		}
		return mergedLoadedMsg{scope: scope, mrs: mrs}
	}
}

// loadMoreCmd fetches the next page (after cursor) to append to the list.
func (m mrListModel) loadMoreCmd() tea.Cmd {
	scope := m.scope()
	client := m.client
	cursor := m.cursor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		page, err := client.Changes(ctx, scope, cursor, pageSize)
		if err != nil {
			return mrsErrMsg{err}
		}
		return mrsLoadedMsg{scope: scope, page: page, append: true}
	}
}

func (m mrListModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.fetchCmd(false),
		m.summaryCmd(forge.ScopeReviewer, false),
		m.summaryCmd(forge.ScopeAuthored, false),
	)
}

// summaryCmd walks every page so tab badges describe the whole workload rather
// than only the pages the user has visited.
func (m mrListModel) summaryCmd(scope forge.Scope, force bool) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if force {
			ctx = forge.WithForceRefresh(ctx)
		}

		all := make([]forge.Change, 0)
		cursor := ""
		for {
			page, err := client.Changes(ctx, scope, cursor, 100)
			if err != nil {
				// Summary metadata is supplementary; the main list request owns
				// user-visible errors.
				return scopeSummaryLoadedMsg{scope: scope}
			}
			all = append(all, page.Changes...)
			if !page.HasNextPage {
				break
			}
			cursor = page.EndCursor
		}
		return scopeSummaryLoadedMsg{scope: scope, mrs: all}
	}
}

// --- action commands (operate on the selected change) ---

func (m mrListModel) approveActionCmd(mr forge.Change) tea.Cmd {
	client, vocab := m.client, m.vocabFor(mr)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		verb, err := "approved", error(nil)
		if mr.ApprovedByMe {
			verb, err = vocab.Unapprove+"d", client.Unapprove(ctx, mr.Repo, mr.ID)
		} else {
			err = client.Approve(ctx, mr.Repo, mr.ID)
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

func (m mrListModel) mergeActionCmd(mr forge.Change, auto bool) tea.Cmd {
	client, vocab := m.client, m.vocabFor(mr)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		outcome, err := client.Merge(ctx, mr.Repo, mr.ID, auto)
		verb := "merged"
		switch outcome {
		case forge.MergeOutcomeTrain:
			verb = "added to " + vocab.MergeQueue
		case forge.MergeOutcomeAutoMerge:
			verb = "auto-merge set"
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

// updateBranchActionCmd brings the source branch up to date with its target:
// a rebase on GitLab, an update-branch merge on GitHub.
func (m mrListModel) updateBranchActionCmd(mr forge.Change) tea.Cmd {
	client, vocab := m.client, m.vocabFor(mr)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.UpdateBranch(ctx, mr.Repo, mr.ID)
		return actionDoneMsg{verb: vocab.UpdateBranchDone, err: err}
	}
}

func (m mrListModel) setDraftActionCmd(mr forge.Change, draft bool) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := client.SetDraft(ctx, mr.Repo, mr.ID, mr.Title, draft)
		verb := "marked ready"
		if draft {
			verb = "marked as draft"
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

// listMergeBlock returns why the list-row change can't be merged now, using the
// limited fields a list row carries (merge state, approvals, draft).
func listMergeBlock(mr forge.Change, v forge.Vocabulary) string {
	if mr.Draft {
		return v.Change + " is still a draft"
	}
	if mr.Conflicts {
		return "has conflicts that must be resolved"
	}
	switch mr.MergeState {
	case forge.MergeStateNeedsUpdate:
		return fmt.Sprintf("is behind its target branch (press b to %s)", v.UpdateBranch)
	case forge.MergeStateConflict:
		return "has conflicts that must be resolved"
	case forge.MergeStateCIRunning:
		return v.Pipeline + " must finish first"
	case forge.MergeStateCIFailed:
		return v.Pipeline + " must pass first"
	case forge.MergeStateThreadsUnresolved:
		return "open " + v.Threads + " must be resolved"
	case forge.MergeStateChangesRequested:
		return "a reviewer requested changes"
	case forge.MergeStateBlocked:
		return "blocked by a branch rule or another " + v.Change
	}
	if mr.ApprovalsLeft > 0 {
		return fmt.Sprintf("%d more approval(s) required", mr.ApprovalsLeft)
	}
	return ""
}

// listAutoMergeBlock is like listMergeBlock but tolerates a running pipeline,
// since waiting for CI is exactly what auto-merge is for.
func listAutoMergeBlock(mr forge.Change, v forge.Vocabulary) string {
	if mr.MergeState.CIPending() {
		if mr.Draft {
			return v.Change + " is still a draft"
		}
		if mr.Conflicts {
			return "has conflicts that must be resolved"
		}
		return ""
	}
	return listMergeBlock(mr, v)
}

// applyFilter returns the changes matching the current filter query.
func (m mrListModel) applyFilter(mrs []forge.Change) []forge.Change {
	q := strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
	if q == "" {
		return mrs
	}
	terms := strings.Fields(q)
	var out []forge.Change
	for _, mr := range mrs {
		hay := strings.ToLower(mr.Title + " " + mr.Repo + " " + mr.SourceBranch)
		if containsAll(hay, terms) {
			out = append(out, mr)
		}
	}
	return out
}

// visibleMRs is the filtered set of open changes (used for counts).
func (m mrListModel) visibleMRs() []forge.Change { return m.applyFilter(m.mrs) }

func containsAll(hay string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// appendGrouped appends project-grouped rows for mrs to m.rows. Grouping is
// keyed by host and repo, since two hosts can serve repos with the same path;
// the incoming order of first appearance decides group order, which keeps the
// newest-updated-first merge from being reshuffled alphabetically.
func (m *mrListModel) appendGrouped(mrs []forge.Change) {
	type groupKey struct{ host, repo string }
	var order []groupKey
	groups := map[groupKey][]forge.Change{}
	for _, mr := range mrs {
		k := groupKey{mr.Host, mr.Repo}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], mr)
	}
	for gi, k := range order {
		if gi > 0 {
			m.rows = append(m.rows, mrRow{isSpacer: true})
		}
		m.rows = append(m.rows, mrRow{isHeader: true, project: k.repo, host: k.host})
		for _, mr := range groups[k] {
			m.rows = append(m.rows, mrRow{mr: mr})
		}
	}
}

// rebuildRows builds the display rows: an "Open" section grouped by project,
// then (when the scope tracks it) a "Merged" section of recently-merged changes.
func (m *mrListModel) rebuildRows() {
	m.rows = nil

	open := m.applyFilter(m.mrs)
	merged := m.applyFilter(m.merged)

	// Only show the "Open" section header when a merged section follows;
	// otherwise the list is unambiguously the open changes.
	showSections := len(merged) > 0

	if showSections {
		m.rows = append(m.rows, mrRow{isSection: true, section: "Open"})
	}
	m.appendGrouped(open)

	if showSections {
		m.rows = append(m.rows, mrRow{isSpacer: true})
		m.rows = append(m.rows, mrRow{isSection: true,
			section: fmt.Sprintf("Merged · last %dh", int(mergedWindow.Hours()))})
		m.appendGrouped(merged)
	}

	m.clampCursor()
}

// clampCursor snaps the cursor to the nearest selectable row and the scroll
// window to keep it visible.
func (m *mrListModel) clampCursor() {
	if len(m.rows) == 0 {
		m.cur, m.scroll = 0, 0
		return
	}
	if m.cur >= len(m.rows) {
		m.cur = len(m.rows) - 1
	}
	if m.cur < 0 {
		m.cur = 0
	}
	if !m.rows[m.cur].selectable() {
		if i := m.nextSelectable(m.cur, 1); i >= 0 {
			m.cur = i
		} else if i := m.nextSelectable(m.cur, -1); i >= 0 {
			m.cur = i
		}
	}
	m.scrollToCursor()
}

func (m mrListModel) nextSelectable(from, delta int) int {
	for i := from; i >= 0 && i < len(m.rows); i += delta {
		if m.rows[i].selectable() {
			return i
		}
	}
	return -1
}

func (m *mrListModel) scrollToCursor() {
	h := m.listHeight()
	if m.cur < m.scroll {
		m.scroll = m.cur
	}
	if m.cur >= m.scroll+h {
		m.scroll = m.cur - h + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m mrListModel) listHeight() int {
	h := m.height - 2 // status bar + help/filter line
	if h < 3 {
		h = 3
	}
	return h
}

func (m mrListModel) Update(msg tea.Msg) (mrListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.filterInput.Width = msg.Width - 4
		m.scrollToCursor()
		return m, nil

	case mrsLoadedMsg:
		// Ignore stale responses from a scope we've since switched away from.
		if msg.scope != m.scope() {
			return m, nil
		}
		m.loading = false
		m.loadingMore = false
		m.err = nil
		m.cursor = msg.page.EndCursor
		m.hasNext = msg.page.HasNextPage
		if msg.append {
			m.mrs = append(m.mrs, msg.page.Changes...)
		} else {
			m.mrs = msg.page.Changes
			m.lastSynced = nowHM()
			m.cur, m.scroll = 0, 0
		}
		m.rebuildRows()
		return m, nil

	case mergedLoadedMsg:
		if msg.scope != m.scope() {
			return m, nil
		}
		m.merged = msg.mrs
		m.rebuildRows()
		return m, nil

	case scopeSummaryLoadedMsg:
		// A nil slice means the supplementary request failed; retain any older
		// summary instead of making a transient API error look like zero work.
		if msg.mrs != nil {
			m.summaries[msg.scope] = summarizeScope(msg.scope, msg.mrs, time.Now())
		}
		return m, nil

	case actionDoneMsg:
		if msg.err != nil {
			m.flash = errStyle.Render("✘ " + msg.verb + " failed: " + msg.err.Error())
			return m, nil
		}
		m.flash = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + msg.verb)
		// Refresh to reflect the new state (caches were invalidated server-side).
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true), m.summaryCmd(m.scope(), true))

	case mrsErrMsg:
		m.loading = false
		m.loadingMore = false
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m mrListModel) handleKey(msg tea.KeyMsg) (mrListModel, tea.Cmd) {
	// Filter input owns keys while active.
	if m.filterMode {
		switch msg.String() {
		case "esc":
			m.filterMode = false
			m.filterInput.SetValue("")
			m.filterInput.Blur()
			m.rebuildRows()
			return m, nil
		case "enter":
			m.filterMode = false
			m.filterInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		m.rebuildRows()
		return m, cmd
	}

	// Merge / auto-merge confirmation owns y/N.
	if m.confirm != modeNone {
		auto := m.confirm == modeConfirmAutoMerge
		m.confirm = modeNone
		if msg.String() == "y" || msg.String() == "Y" {
			mr, ok := m.selected()
			if !ok {
				return m, nil
			}
			if auto {
				m.flash = "setting auto-merge…"
			} else {
				m.flash = "submitting merge…"
			}
			return m, m.mergeActionCmd(mr, auto)
		}
		m.flash = ""
		return m, nil
	}

	// A deliberate keypress supersedes any lingering flash message.
	m.flash = ""

	switch msg.String() {
	case "tab", "L", "right", "l":
		m.scopeIdx = (m.scopeIdx + 1) % len(scopeOrder)
		m.resetPaging()
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(false))
	case "shift+tab", "H", "left", "h":
		m.scopeIdx = (m.scopeIdx - 1 + len(scopeOrder)) % len(scopeOrder)
		m.resetPaging()
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(false))
	case "r":
		m.resetPaging()
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true), m.summaryCmd(m.scope(), true))
	case "/":
		m.filterMode = true
		m.filterInput.Focus()
		return m, textinput.Blink
	case "down", "j":
		m.moveCursor(1)
	case "up", "k":
		m.moveCursor(-1)
	case "pgdown":
		if m.scopeHasMerged() {
			m.moveCursor(m.halfPage())
		}
	case "pgup":
		if m.scopeHasMerged() {
			m.moveCursor(-m.halfPage())
		}
	case "a":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			if mr.ApprovedByMe && !m.capsFor(mr).Unapprove {
				m.flash = errStyle.Render("✘ " + m.vocabFor(mr).Unapprove + " is not supported here")
				return m, nil
			}
			m.flash = "submitting approval…"
			return m, m.approveActionCmd(mr)
		}
	case "M":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			if reason := listMergeBlock(mr, m.vocabFor(mr)); reason != "" {
				m.flash = errStyle.Render("✘ can't merge: " + reason)
				return m, nil
			}
			m.confirm = modeConfirmMerge
		}
	case "A":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			if !m.capsFor(mr).AutoMerge {
				m.flash = errStyle.Render("✘ auto-merge is not supported here")
				return m, nil
			}
			if reason := listAutoMergeBlock(mr, m.vocabFor(mr)); reason != "" {
				m.flash = errStyle.Render("✘ can't auto-merge: " + reason)
				return m, nil
			}
			m.confirm = modeConfirmAutoMerge
		}
	case "b":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			m.flash = m.vocabFor(mr).UpdateBranchGerund + "…"
			return m, m.updateBranchActionCmd(mr)
		}
	case "D":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			if !m.capsFor(mr).DraftToggle {
				m.flash = errStyle.Render("✘ toggling draft is not supported here")
				return m, nil
			}
			draft := !mr.Draft
			if draft {
				m.flash = "marking as draft…"
			} else {
				m.flash = "marking ready…"
			}
			return m, m.setDraftActionCmd(mr, draft)
		}
	}

	// Auto-load the next page as the cursor nears the end of the list.
	if more := m.maybeLoadMore(); more != nil {
		return m, more
	}
	return m, nil
}

// moveCursor advances to the next/previous selectable row.
func (m *mrListModel) moveCursor(delta int) {
	if len(m.rows) == 0 {
		return
	}
	if i := m.nextSelectable(m.cur+delta, delta); i >= 0 {
		m.cur = i
		m.scrollToCursor()
	}
}

// halfPage is the number of display rows a Page Up/Down key traverses.
func (m mrListModel) halfPage() int {
	return max(1, m.listHeight()/2)
}

// resetPaging clears the list and pagination state before a fresh load.
func (m *mrListModel) resetPaging() {
	m.loading = true
	m.loadingMore = false
	m.cursor = ""
	m.hasNext = false
	m.filterMode = false
	m.filterInput.SetValue("")
	m.filterInput.Blur()
	m.mrs = nil
	m.merged = nil
	m.rows = nil
	m.cur, m.scroll = 0, 0
}

// maybeLoadMore returns a command to fetch the next page when the cursor is
// within a few rows of the end and more pages exist. Filtering is excluded so
// we don't paginate while the user narrows the current set.
func (m *mrListModel) maybeLoadMore() tea.Cmd {
	const threshold = 5
	if !m.hasNext || m.loadingMore || m.loading {
		return nil
	}
	if m.filterInput.Value() != "" {
		return nil
	}
	if m.cur < len(m.rows)-threshold {
		return nil
	}
	m.loadingMore = true
	return tea.Batch(m.spinner.Tick, m.loadMoreCmd())
}

func (m mrListModel) View() string {
	var body string
	switch {
	case m.loading && len(m.mrs) == 0:
		body = fmt.Sprintf("\n  %s loading %s…", m.spinner.View(), m.vocab.Changes)
	case m.err != nil:
		body = "\n  " + errStyle.Render("error: "+m.err.Error())
	case len(m.rows) == 0 && m.filterInput.Value() != "":
		body = "\n  " + helpStyle.Render("no "+m.vocab.Changes+" match the filter")
	case len(m.rows) == 0:
		body = "\n  " + helpStyle.Render("no open "+m.vocab.Changes+" in this scope")
	default:
		body = m.rowsView()
	}

	// Pad the body so it fills the available height and the bottom line stays
	// pinned to the bottom of the screen.
	body = padToHeight(body, m.listHeight())

	return lipgloss.JoinVertical(lipgloss.Left, m.statusBar(), body, m.bottomLine())
}

// padToHeight pads s with blank lines so it occupies exactly n rows (it does
// not truncate; callers keep content within n).
func padToHeight(s string, n int) string {
	lines := strings.Count(s, "\n") + 1
	if s == "" {
		lines = 0
	}
	if lines >= n {
		return s
	}
	return s + strings.Repeat("\n", n-lines)
}

// rowsView renders the visible window of grouped rows.
func (m mrListModel) rowsView() string {
	h := m.listHeight()
	end := m.scroll + h
	if end > len(m.rows) {
		end = len(m.rows)
	}
	activeProject := ""
	if mr, ok := m.selected(); ok {
		activeProject = mr.Repo
	}

	var b strings.Builder
	for i := m.scroll; i < end; i++ {
		b.WriteString(m.renderRow(i, activeProject))
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m mrListModel) renderRow(i int, activeProject string) string {
	r := m.rows[i]
	switch {
	case r.isSpacer:
		return ""
	case r.isSection:
		// Section banner ("Open" / "Merged · last 12h").
		style := lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Color("15"))
		return truncateToWidth(style.Render(r.section), m.width)
	case r.isHeader:
		// Project headers are indented under their section.
		style := lipgloss.NewStyle().Bold(true).Foreground(colorSubtle)
		if r.project == activeProject {
			style = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
		}
		line := "  " + style.Render(r.project)
		// On a merged list the host is what disambiguates one repo from another
		// with the same path, so it rides along with the group header rather than
		// costing every row a column.
		if m.multiHost() && r.host != "" {
			line += " " + helpStyle.Render(r.host)
		}
		return truncateToWidth(line, m.width)
	}

	mr := r.mr
	vocab := m.vocabFor(mr)
	merged := mr.MergedAt != ""
	pipe := statusGlyph(mr.Pipeline)

	// Second column: approval state for open changes, merge time for merged ones.
	var second string
	if merged {
		second = lipgloss.NewStyle().Foreground(colorSubtle).Render(relAge(mr.MergedAt))
	} else {
		second = approvalGlyph(mr.Approved, mr.ApprovedByMe, mr.ApprovalsLeft)
	}

	flags := ""
	if !merged {
		if mr.Draft {
			flags += lipgloss.NewStyle().Foreground(colorSubtle).Render(" draft")
		}
		if mr.Conflicts {
			flags += errStyle.Render(" conflict")
		}
		if tag := mergeStateTag(mr.MergeState, vocab); tag != "" {
			flags += " " + tag
		}
	}

	id := lipgloss.NewStyle().Foreground(colorSubtle).Render(vocab.IDPrefix + mr.ID)
	title := mr.Title
	cursor := "  "
	if i == m.cur {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▌ ")
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Render(title)
	}

	line := fmt.Sprintf("%s%s %s %s  %s%s", cursor, pipe, second, id, title, flags)
	return truncateToWidth(line, m.width)
}

// relAge renders an RFC3339 timestamp as a short relative age ("3h", "12m").
func relAge(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (m mrListModel) statusBar() string {
	tabs := make([]string, len(scopeOrder))
	for i, scope := range scopeOrder {
		label := scope.label
		if summary, ok := m.summaries[scope.scope]; ok {
			switch scope.scope {
			case forge.ScopeReviewer:
				label += fmt.Sprintf(" %d", summary.pending)
			case forge.ScopeAuthored:
				label += fmt.Sprintf(" %d ready", summary.ready)
			}
		}
		if i == m.scopeIdx {
			tabs[i] = titleStyle.Render(label)
		} else {
			tabs[i] = helpStyle.Render(label)
		}
	}
	left := strings.Join(tabs, helpStyle.Render(" · "))
	if m.loadingMore {
		left += "  " + helpStyle.Render(m.spinner.View()+" more")
	}

	right := helpStyle.Render(m.client.Host())
	// A host that failed its last read is named here, so a partial list never
	// silently reads as the whole workload. "r" retries it.
	if bad := m.degradedHosts(); bad != "" {
		right = errStyle.Render("✘ "+bad) + helpStyle.Render("  "+m.client.Host())
	}
	if detail := m.scopeSummaryDetail(); detail != "" {
		right += helpStyle.Render("  " + detail)
	}
	if m.lastSynced != "" {
		right += helpStyle.Render("  synced " + m.lastSynced)
	}
	maxRight := m.width - lipgloss.Width(left) - 3
	if maxRight > 0 {
		right = truncateToWidth(right, maxRight)
	} else {
		right = ""
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return statusBarStyle.Width(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

// degradedHosts names the hosts whose last read failed, for the status bar.
func (m mrListModel) degradedHosts() string {
	if m.fleet == nil {
		return ""
	}
	bad := m.fleet.Degraded()
	if len(bad) == 0 {
		return ""
	}
	return strings.Join(bad, ", ") + " unreachable"
}

const staleReviewAge = 48 * time.Hour

func summarizeScope(scope forge.Scope, mrs []forge.Change, now time.Time) scopeSummary {
	var summary scopeSummary
	for _, mr := range mrs {
		switch scope {
		case forge.ScopeReviewer:
			if mr.Draft || !mr.ReviewState.Pending() {
				continue
			}
			summary.pending++
			updated, err := time.Parse(time.RFC3339, mr.UpdatedAt)
			if err != nil {
				continue
			}
			age := now.Sub(updated)
			if age >= staleReviewAge {
				summary.stale++
			}
			if age > summary.oldest {
				summary.oldest = age
			}

		case forge.ScopeAuthored:
			if readyToMerge(mr) {
				summary.ready++
				continue
			}
			summary.blocked++
			if mr.Draft {
				summary.drafts++
			}
			if mr.ApprovalsLeft > 0 || mr.MergeState == forge.MergeStateNotApproved {
				summary.approvals++
			}
			if mr.Pipeline == forge.StatusFailed || mr.MergeState == forge.MergeStateCIFailed {
				summary.ciFailed++
			}
			if mr.Pipeline.Active() || mr.MergeState == forge.MergeStateCIRunning {
				summary.ciRunning++
			}
			if mr.Conflicts || mr.MergeState == forge.MergeStateConflict {
				summary.conflicts++
			}
			if mr.MergeState == forge.MergeStateNeedsUpdate {
				summary.behind++
			}
			if mr.MergeState == forge.MergeStateThreadsUnresolved {
				summary.threads++
			}
		}
	}
	return summary
}

// readyToMerge reports whether an authored change has nothing standing between
// it and a merge. Vocabulary is irrelevant here: only the blocker's presence is.
func readyToMerge(mr forge.Change) bool {
	return mr.MergeState.Mergeable() && listMergeBlock(mr, forge.Vocabulary{}) == ""
}

func (m mrListModel) scopeSummaryDetail() string {
	summary, ok := m.summaries[m.scope()]
	if !ok {
		return ""
	}

	var parts []string
	switch m.scope() {
	case forge.ScopeReviewer:
		parts = appendCount(parts, summary.stale, "stale", "stale")
		if summary.pending > 0 && summary.oldest > 0 {
			parts = append(parts, "oldest "+shortAge(summary.oldest))
		}
	case forge.ScopeAuthored:
		parts = appendCount(parts, summary.approvals, "approval", "approvals")
		parts = appendCount(parts, summary.ciFailed, "CI failed", "CI failed")
		parts = appendCount(parts, summary.behind, m.vocab.UpdateBranch, m.vocab.UpdateBranch)
		parts = appendCount(parts, summary.conflicts, "conflict", "conflicts")
		parts = appendCount(parts, summary.threads, m.vocab.Thread, m.vocab.Threads)
		parts = appendCount(parts, summary.ciRunning, "CI running", "CI running")
		parts = appendCount(parts, summary.drafts, "draft", "drafts")
		if len(parts) == 0 && summary.blocked > 0 {
			parts = append(parts, fmt.Sprintf("%d blocked", summary.blocked))
		}
	}
	return strings.Join(parts, " · ")
}

func appendCount(parts []string, count int, singular, plural string) []string {
	if count == 0 {
		return parts
	}
	label := plural
	if count == 1 {
		label = singular
	}
	return append(parts, fmt.Sprintf("%d %s", count, label))
}

func shortAge(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// bottomLine shows the filter input, a confirm prompt, a flash, or help hints.
func (m mrListModel) bottomLine() string {
	switch m.confirm {
	case modeConfirmMerge:
		return errStyle.Render("  merge this " + m.vocab.ChangeAbbrev + " now? [y/N]")
	case modeConfirmAutoMerge:
		return errStyle.Render("  set auto-merge (merge when checks pass)? [y/N]")
	}
	if m.filterMode {
		return "  " + m.filterInput.View()
	}
	if m.flash != "" {
		return "  " + m.flash
	}
	if q := m.filterInput.Value(); q != "" {
		return helpStyle.Render("  filter: ") + lipgloss.NewStyle().Foreground(colorAccent).Render(q) +
			helpStyle.Render("  (esc to clear)")
	}
	return helpStyle.Render(fmt.Sprintf("  enter open · a/M/A/b act · d diff · p %s · / filter · ? help", m.vocab.Pipeline))
}

func nowHM() string {
	// Note: time.Now is fine in the TUI process (not a workflow script).
	return time.Now().Format("15:04:05")
}

// truncateToWidth trims a styled string to fit n display columns, preserving
// ANSI styling (closes open SGR sequences) via muesli/reflow.
func truncateToWidth(s string, n int) string {
	if n <= 0 {
		return s
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	return truncate.String(s, uint(n))
}

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

	"github.com/hamkens/glx/internal/gitlab"
)

// scopeOrder defines the tab cycle and labels for MR scopes.
var scopeOrder = []struct {
	scope gitlab.Scope
	label string
}{
	{gitlab.ScopeReviewer, "Reviews"},
	{gitlab.ScopeAuthored, "Authored"},
	{gitlab.ScopeAssigned, "Assigned"},
}

// mrRow is a flattened display row: a section header ("Open"/"Merged"), a
// project header, a blank spacer, or a merge-request row.
type mrRow struct {
	isSection bool
	isHeader  bool
	isSpacer  bool
	section   string // for isSection
	project   string // for isHeader
	mr        gitlab.MR
}

func (r mrRow) selectable() bool { return !r.isHeader && !r.isSpacer && !r.isSection }

// mergedWindow is how far back the "recently merged" section reaches.
const mergedWindow = 12 * time.Hour

// --- messages ---

type mrsLoadedMsg struct {
	scope  gitlab.Scope
	page   *gitlab.MRPage
	append bool // true when this is a "load more" page to append
}

// mergedLoadedMsg carries the recently-merged MRs for a scope.
type mergedLoadedMsg struct {
	scope gitlab.Scope
	mrs   []gitlab.MR
}
type mrsErrMsg struct{ err error }

// --- model ---

type mrListModel struct {
	client      *gitlab.Client
	spinner     spinner.Model
	filterInput textinput.Model
	scopeIdx    int

	mrs    []gitlab.MR // open MRs, in server order
	merged []gitlab.MR // recently-merged MRs (Authored/Reviews only)
	rows   []mrRow     // derived display rows (headers + spacers + MRs)
	cur    int         // index into rows; always points at a selectable row
	scroll int         // index of the top visible row

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
}

func newMRListModel(client *gitlab.Client) mrListModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter by title, project, or branch"

	return mrListModel{client: client, spinner: sp, filterInput: ti, loading: true}
}

func (m mrListModel) scope() gitlab.Scope { return scopeOrder[m.scopeIdx].scope }

// selected returns the MR under the cursor, or false if there is none.
func (m mrListModel) selected() (gitlab.MR, bool) {
	if m.cur >= 0 && m.cur < len(m.rows) && m.rows[m.cur].selectable() {
		return m.rows[m.cur].mr, true
	}
	return gitlab.MR{}, false
}

// filtering reports whether the filter input is currently capturing keys.
func (m mrListModel) filtering() bool { return m.filterMode }

// busy reports whether the list is capturing input (filter) or awaiting a
// y/N confirmation, so the root shouldn't steal single-key shortcuts.
func (m mrListModel) busy() bool { return m.filterMode || m.confirm != modeNone }

// pageSize is how many MRs to request per page.
const pageSize = 50

// scopeHasMerged reports whether the current scope shows a recently-merged
// section. Assigned MRs aren't tracked for merge history here.
func (m mrListModel) scopeHasMerged() bool {
	s := m.scope()
	return s == gitlab.ScopeAuthored || s == gitlab.ScopeReviewer
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
			ctx = gitlab.WithForceRefresh(ctx)
		}
		page, err := client.MergeRequests(ctx, scope, "", pageSize)
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

// mergedCmd loads MRs merged within mergedWindow for the current scope.
func (m mrListModel) mergedCmd(force bool) tea.Cmd {
	scope := m.scope()
	client := m.client
	since := time.Now().Add(-mergedWindow).UTC().Format(time.RFC3339)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if force {
			ctx = gitlab.WithForceRefresh(ctx)
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
		page, err := client.MergeRequests(ctx, scope, cursor, pageSize)
		if err != nil {
			return mrsErrMsg{err}
		}
		return mrsLoadedMsg{scope: scope, page: page, append: true}
	}
}

func (m mrListModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchCmd(false))
}

// --- action commands (operate on the selected MR) ---

func (m mrListModel) approveActionCmd(mr gitlab.MR) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		verb, err := "approved", error(nil)
		if mr.Approved {
			verb, err = "unapproved", client.Unapprove(ctx, mr.ProjectPath, mr.IID)
		} else {
			err = client.Approve(ctx, mr.ProjectPath, mr.IID)
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

func (m mrListModel) mergeActionCmd(mr gitlab.MR, auto bool) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.Merge(ctx, mr.ProjectPath, mr.IID, auto)
		verb := "merged"
		if auto {
			verb = "auto-merge set"
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

func (m mrListModel) rebaseActionCmd(mr gitlab.MR) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.Rebase(ctx, mr.ProjectPath, mr.IID)
		return actionDoneMsg{verb: "rebase started", err: err}
	}
}

// listMergeBlock returns why the list-row MR can't be merged now, using the
// limited fields a list row carries (detailed status, approvals, draft).
func listMergeBlock(mr gitlab.MR) string {
	if mr.Draft {
		return "merge request is still a draft"
	}
	if mr.Conflicts {
		return "has conflicts that must be resolved"
	}
	switch mr.DetailedStatus {
	case "NEED_REBASE":
		return "needs rebase onto target branch (press b to rebase)"
	case "CI_STILL_RUNNING":
		return "pipeline must finish first"
	case "CI_MUST_PASS":
		return "pipeline must pass first"
	case "DISCUSSIONS_NOT_RESOLVED":
		return "open threads must be resolved"
	case "BLOCKED_STATUS":
		return "blocked by another merge request"
	}
	if mr.ApprovalsLeft > 0 {
		return fmt.Sprintf("%d more approval(s) required", mr.ApprovalsLeft)
	}
	return ""
}

// listAutoMergeBlock is like listMergeBlock but tolerates a running pipeline.
func listAutoMergeBlock(mr gitlab.MR) string {
	if mr.DetailedStatus == "CI_STILL_RUNNING" || mr.DetailedStatus == "CI_MUST_PASS" {
		if mr.Draft {
			return "merge request is still a draft"
		}
		if mr.Conflicts {
			return "has conflicts that must be resolved"
		}
		if mr.DetailedStatus == "NEED_REBASE" {
			return "needs rebase onto target branch (press b to rebase)"
		}
		return ""
	}
	return listMergeBlock(mr)
}

// applyFilter returns the MRs matching the current filter query.
func (m mrListModel) applyFilter(mrs []gitlab.MR) []gitlab.MR {
	q := strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
	if q == "" {
		return mrs
	}
	terms := strings.Fields(q)
	var out []gitlab.MR
	for _, mr := range mrs {
		hay := strings.ToLower(mr.Title + " " + mr.ProjectPath + " " + mr.SourceBranch)
		if containsAll(hay, terms) {
			out = append(out, mr)
		}
	}
	return out
}

// visibleMRs is the filtered set of open MRs (used for counts).
func (m mrListModel) visibleMRs() []gitlab.MR { return m.applyFilter(m.mrs) }

func containsAll(hay string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// appendGrouped appends project-grouped rows for mrs to m.rows.
func (m *mrListModel) appendGrouped(mrs []gitlab.MR) {
	var order []string
	groups := map[string][]gitlab.MR{}
	for _, mr := range mrs {
		if _, ok := groups[mr.ProjectPath]; !ok {
			order = append(order, mr.ProjectPath)
		}
		groups[mr.ProjectPath] = append(groups[mr.ProjectPath], mr)
	}
	for gi, proj := range order {
		if gi > 0 {
			m.rows = append(m.rows, mrRow{isSpacer: true})
		}
		m.rows = append(m.rows, mrRow{isHeader: true, project: proj})
		for _, mr := range groups[proj] {
			m.rows = append(m.rows, mrRow{mr: mr})
		}
	}
}

// rebuildRows builds the display rows: an "Open" section grouped by project,
// then (when the scope tracks it) a "Merged" section of recently-merged MRs.
func (m *mrListModel) rebuildRows() {
	m.rows = nil

	open := m.applyFilter(m.mrs)
	merged := m.applyFilter(m.merged)

	// Only show the "Open" section header when a merged section follows;
	// otherwise the list is unambiguously the open MRs.
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
			m.mrs = append(m.mrs, msg.page.MRs...)
		} else {
			m.mrs = msg.page.MRs
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

	case actionDoneMsg:
		if msg.err != nil {
			m.flash = errStyle.Render("✘ " + msg.verb + " failed: " + msg.err.Error())
			return m, nil
		}
		m.flash = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + msg.verb)
		// Refresh to reflect the new state (caches were invalidated server-side).
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true))

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
				m.flash = "merging…"
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
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true))
	case "/":
		m.filterMode = true
		m.filterInput.Focus()
		return m, textinput.Blink
	case "down", "j":
		m.moveCursor(1)
	case "up", "k":
		m.moveCursor(-1)
	case "a":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			m.flash = "submitting approval…"
			return m, m.approveActionCmd(mr)
		}
	case "M":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			if reason := listMergeBlock(mr); reason != "" {
				m.flash = errStyle.Render("✘ can't merge: " + reason)
				return m, nil
			}
			m.confirm = modeConfirmMerge
		}
	case "A":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			if reason := listAutoMergeBlock(mr); reason != "" {
				m.flash = errStyle.Render("✘ can't auto-merge: " + reason)
				return m, nil
			}
			m.confirm = modeConfirmAutoMerge
		}
	case "b":
		if mr, ok := m.selected(); ok && mr.MergedAt == "" {
			m.flash = "rebasing…"
			return m, m.rebaseActionCmd(mr)
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
		body = fmt.Sprintf("\n  %s loading merge requests…", m.spinner.View())
	case m.err != nil:
		body = "\n  " + errStyle.Render("error: "+m.err.Error())
	case len(m.rows) == 0 && m.filterInput.Value() != "":
		body = "\n  " + helpStyle.Render("no merge requests match the filter")
	case len(m.rows) == 0:
		body = "\n  " + helpStyle.Render("no open merge requests in this scope")
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
		activeProject = mr.ProjectPath
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
		return truncateToWidth("  "+style.Render(r.project), m.width)
	}

	mr := r.mr
	merged := mr.MergedAt != ""
	pipe := pipelineGlyph(mr.Pipeline)

	// Second column: approval state for open MRs, merge time for merged ones.
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
		if tag := mergeStatusTag(mr.DetailedStatus); tag != "" {
			flags += " " + tag
		}
	}

	iid := lipgloss.NewStyle().Foreground(colorSubtle).Render("!" + mr.IID)
	title := mr.Title
	cursor := "  "
	if i == m.cur {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▌ ")
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Render(title)
	}

	line := fmt.Sprintf("%s%s %s %s  %s%s", cursor, pipe, second, iid, title, flags)
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
	for i, s := range scopeOrder {
		if i == m.scopeIdx {
			tabs[i] = titleStyle.Render(s.label)
		} else {
			tabs[i] = helpStyle.Render(s.label)
		}
	}
	left := strings.Join(tabs, helpStyle.Render(" · "))
	if m.loadingMore {
		left += "  " + helpStyle.Render(m.spinner.View()+" more")
	}
	right := helpStyle.Render(m.client.Host())
	if cnt := len(m.visibleMRs()); cnt > 0 {
		more := ""
		if m.hasNext {
			more = "+"
		}
		right += helpStyle.Render(fmt.Sprintf("  %d%s", cnt, more))
	}
	if m.lastSynced != "" {
		right += helpStyle.Render("  synced " + m.lastSynced)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return statusBarStyle.Width(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

// bottomLine shows the filter input, a confirm prompt, a flash, or help hints.
func (m mrListModel) bottomLine() string {
	switch m.confirm {
	case modeConfirmMerge:
		return errStyle.Render("  merge this MR now? [y/N]")
	case modeConfirmAutoMerge:
		return errStyle.Render("  set auto-merge (merge when pipeline succeeds)? [y/N]")
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
	return helpStyle.Render("  enter open · a/M/A/b act · d diff · p pipeline · / filter · ? help")
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

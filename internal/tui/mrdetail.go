package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/gitlab"
)

// --- messages ---

type detailLoadedMsg struct{ detail *gitlab.MRDetail }
type detailErrMsg struct{ err error }

// actionDoneMsg reports the result of a write action (approve/merge/comment).
type actionDoneMsg struct {
	verb string
	err  error
}

// inputMode is the detail view's transient input state.
type inputMode int

const (
	modeNone             inputMode = iota
	modeComment                    // composing a comment
	modeConfirmMerge               // y/n merge confirmation
	modeConfirmAutoMerge           // y/n auto-merge (merge when pipeline succeeds)
)

// detailModel renders one merge request and hosts write actions.
type detailModel struct {
	client      *gitlab.Client
	projectPath string
	iid         string

	vp       viewport.Model
	spinner  spinner.Model
	textarea textarea.Model

	detail  *gitlab.MRDetail
	loading bool
	err     error
	mode    inputMode
	flash   string // transient status message (e.g. "approved")

	width  int
	height int
}

func newDetailModel(client *gitlab.Client, projectPath, iid string) detailModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ta := textarea.New()
	ta.Placeholder = "Write a comment… (ctrl+s to send, esc to cancel)"
	ta.ShowLineNumbers = false

	return detailModel{
		client:      client,
		projectPath: projectPath,
		iid:         iid,
		spinner:     sp,
		textarea:    ta,
		loading:     true,
	}
}

func (m detailModel) fetchCmd(force bool) tea.Cmd {
	client, path, iid := m.client, m.projectPath, m.iid
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if force {
			ctx = gitlab.WithForceRefresh(ctx)
		}
		d, err := client.MergeRequestDetail(ctx, path, iid)
		if err != nil {
			return detailErrMsg{err}
		}
		return detailLoadedMsg{d}
	}
}

func (m detailModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchCmd(false))
}

// action command builders ---------------------------------------------------

func (m detailModel) approveCmd(approve bool) tea.Cmd {
	client, path, iid := m.client, m.projectPath, m.iid
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		verb := "approved"
		if approve {
			err = client.Approve(ctx, path, iid)
		} else {
			verb = "unapproved"
			err = client.Unapprove(ctx, path, iid)
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

// mergeCmd merges now. When auto is true it sets merge-when-pipeline-succeeds
// (GitLab "auto-merge"), so the merge happens once the pipeline passes.
func (m detailModel) mergeCmd(auto bool) tea.Cmd {
	client, path, iid := m.client, m.projectPath, m.iid
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.Merge(ctx, path, iid, auto)
		verb := "merged"
		if auto {
			verb = "auto-merge set"
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

func (m detailModel) commentCmd(body string) tea.Cmd {
	client, path, iid := m.client, m.projectPath, m.iid
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := client.AddComment(ctx, path, iid, body)
		return actionDoneMsg{verb: "commented", err: err}
	}
}

func (m detailModel) rebaseCmd() tea.Cmd {
	client, path, iid := m.client, m.projectPath, m.iid
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.Rebase(ctx, path, iid)
		// The rebase is async server-side; the refetch (triggered on success)
		// will reflect the new status once GitLab finishes.
		return actionDoneMsg{verb: "rebase started", err: err}
	}
}

func (m detailModel) setDraftCmd(draft bool) tea.Cmd {
	client, path, iid := m.client, m.projectPath, m.iid
	title := ""
	if m.detail != nil {
		title = m.detail.Title
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := client.SetDraft(ctx, path, iid, title, draft)
		verb := "marked ready"
		if draft {
			verb = "marked as draft"
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

func (m detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if m.detail != nil {
			m.renderBody()
		}
		return m, nil

	case detailLoadedMsg:
		m.loading = false
		m.err = nil
		m.detail = msg.detail
		m.layout()
		m.renderBody()
		return m, nil

	case detailErrMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case actionDoneMsg:
		if msg.err != nil {
			m.flash = errStyle.Render("✘ " + msg.verb + " failed: " + msg.err.Error())
			return m, nil
		}
		m.flash = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + msg.verb)
		// Refresh detail to reflect the new state (cache already invalidated
		// server-side, but force to be certain we re-hit the API).
		m.loading = true
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true))

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m detailModel) handleKey(msg tea.KeyMsg) (detailModel, tea.Cmd) {
	// Comment composer owns keys while active.
	if m.mode == modeComment {
		switch msg.String() {
		case "esc":
			m.mode = modeNone
			m.textarea.Reset()
			m.textarea.Blur()
			return m, nil
		case "ctrl+s":
			body := strings.TrimSpace(m.textarea.Value())
			m.mode = modeNone
			m.textarea.Reset()
			m.textarea.Blur()
			if body == "" {
				return m, nil
			}
			m.flash = "sending comment…"
			return m, m.commentCmd(body)
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}

	// Merge / auto-merge confirmation.
	if m.mode == modeConfirmMerge || m.mode == modeConfirmAutoMerge {
		auto := m.mode == modeConfirmAutoMerge
		switch msg.String() {
		case "y", "Y":
			m.mode = modeNone
			if auto {
				m.flash = "setting auto-merge…"
			} else {
				m.flash = "merging…"
			}
			return m, m.mergeCmd(auto)
		default:
			m.mode = modeNone
			m.flash = ""
			return m, nil
		}
	}

	switch msg.String() {
	case "r":
		m.loading = true
		m.flash = ""
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true))
	case "a":
		if m.detail != nil {
			approve := !m.detail.Approved
			m.flash = "submitting approval…"
			return m, m.approveCmd(approve)
		}
	case "M":
		if m.detail != nil {
			if reason := mergeBlockedReason(m.detail); reason != "" {
				m.flash = errStyle.Render("✘ can't merge: " + reason)
				return m, nil
			}
			m.mode = modeConfirmMerge
		}
		return m, nil
	case "A":
		// Auto-merge: merge when the pipeline succeeds. Unlike a plain merge,
		// a still-running pipeline is expected, so it isn't a blocker — but
		// other gates (rebase, conflicts, approvals, draft) still are.
		if m.detail != nil {
			if reason := autoMergeBlockedReason(m.detail); reason != "" {
				m.flash = errStyle.Render("✘ can't auto-merge: " + reason)
				return m, nil
			}
			m.mode = modeConfirmAutoMerge
		}
		return m, nil
	case "b":
		if m.detail != nil {
			m.flash = "rebasing…"
			return m, m.rebaseCmd()
		}
	case "D":
		// Toggle draft / ready.
		if m.detail != nil && m.detail.State == "opened" {
			draft := !m.detail.Draft
			if draft {
				m.flash = "marking as draft…"
			} else {
				m.flash = "marking ready…"
			}
			return m, m.setDraftCmd(draft)
		}
	case "c":
		m.mode = modeComment
		m.textarea.Focus()
		return m, textarea.Blink
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// layout sizes the viewport/textarea to the current terminal.
func (m *detailModel) layout() {
	headerH := 6 // title + meta + approvals + pipeline + blank + rule
	footerH := 2
	bodyH := m.height - headerH - footerH
	if bodyH < 3 {
		bodyH = 3
	}
	m.vp = viewport.New(m.width, bodyH)
	m.vp.YPosition = headerH
	m.textarea.SetWidth(m.width - 2)
	m.textarea.SetHeight(4)
}

// renderBody fills the viewport with description + threads.
func (m *detailModel) renderBody() {
	if m.detail == nil {
		return
	}
	var b strings.Builder

	// Description via glamour (falls back to raw on error).
	if desc := strings.TrimSpace(m.detail.Description); desc != "" {
		out := desc
		if r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(min(m.width-2, 100)),
		); err == nil {
			if rendered, err := r.Render(desc); err == nil {
				out = rendered
			}
		}
		b.WriteString(out)
		b.WriteString("\n")
	}

	// Discussion threads.
	threads := m.detail.Discussions
	b.WriteString(titleStyle.Render(fmt.Sprintf("Threads (%d)", len(threads))) + "\n\n")
	for _, d := range threads {
		for _, n := range d.Notes {
			author := lipgloss.NewStyle().Bold(true).Render("@" + n.Author)
			when := helpStyle.Render(relTime(n.CreatedAt))
			body := n.Body
			if n.System {
				author = helpStyle.Render("@" + n.Author)
				body = helpStyle.Render(n.Body)
			}
			b.WriteString(fmt.Sprintf("%s %s\n%s\n\n", author, when, body))
		}
		if d.Resolvable {
			tag := errStyle.Render("● unresolved")
			if d.Resolved {
				tag = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ resolved")
			}
			b.WriteString(tag + "\n")
		}
		b.WriteString(helpStyle.Render(strings.Repeat("─", min(m.width-2, 60))) + "\n\n")
	}

	m.vp.SetContent(b.String())
}

func (m detailModel) View() string {
	if m.loading && m.detail == nil {
		return fmt.Sprintf("\n  %s loading merge request…", m.spinner.View())
	}
	if m.err != nil {
		return "\n  " + errStyle.Render("error: "+m.err.Error()) + "\n  " + helpStyle.Render("esc back")
	}
	if m.detail == nil {
		return "\n  no data"
	}

	d := m.detail
	header := m.header(d)

	if m.mode == modeComment {
		return lipgloss.JoinVertical(lipgloss.Left,
			header,
			m.vp.View(),
			titleStyle.Render("New comment"),
			m.textarea.View(),
		)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, m.vp.View(), m.footer())
}

func (m detailModel) header(d *gitlab.MRDetail) string {
	title := titleStyle.Render(fmt.Sprintf("!%s %s", d.IID, d.Title))

	state := strings.ToLower(d.State)
	stateStyled := lipgloss.NewStyle().Foreground(colorGreen).Render(state)
	if state != "opened" {
		stateStyled = helpStyle.Render(state)
	}

	branches := helpStyle.Render(fmt.Sprintf("%s → %s", d.SourceBranch, d.TargetBranch))
	author := helpStyle.Render("by @" + d.Author)
	meta := fmt.Sprintf("%s  %s  %s", stateStyled, branches, author)

	approvals := fmt.Sprintf("approvals: %d/%d", d.ApprovalsRequired-d.ApprovalsLeft, d.ApprovalsRequired)
	if d.ApprovedByMe && d.ApprovalsLeft > 0 {
		approvals = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ approved by you") + helpStyle.Render(fmt.Sprintf(" · %s, no action needed", approvals))
	}
	if len(d.ApprovedBy) > 0 {
		approvals += helpStyle.Render(" (" + strings.Join(prefixAt(d.ApprovedBy), ", ") + ")")
	}
	pipe := "pipeline: " + pipelineGlyph(d.Pipeline)
	if d.PipelineLabel != "" {
		pipe += " " + helpStyle.Render(d.PipelineLabel)
	}
	line3 := fmt.Sprintf("%s   %s   %s", approvals, pipe, mergeStatusLabel(d))

	rule := helpStyle.Render(strings.Repeat("─", max(m.width, 1)))

	parts := []string{title, meta, line3}
	// Render the flash/error on its own line, wrapped to the terminal width so
	// long messages (e.g. a merge failure with a URL) aren't clipped.
	if m.flash != "" {
		wrapped := lipgloss.NewStyle().Width(max(m.width, 1)).Render(m.flash)
		parts = append(parts, wrapped)
	}
	parts = append(parts, rule)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m detailModel) footer() string {
	if m.mode == modeConfirmMerge {
		return errStyle.Render("  merge this MR now? [y/N]")
	}
	if m.mode == modeConfirmAutoMerge {
		return errStyle.Render("  set auto-merge (merge when pipeline succeeds)? [y/N]")
	}
	approveLabel := "a approve"
	if m.detail != nil && (m.detail.Approved || m.detail.ApprovedByMe) {
		approveLabel = "a unapprove"
	}
	draftLabel := "D draft"
	if m.detail != nil && m.detail.Draft {
		draftLabel = "D ready"
	}
	return helpStyle.Render(fmt.Sprintf("  %s · %s · d diff · p pipeline · M merge · A auto-merge · b rebase · c comment · ? help", approveLabel, draftLabel))
}

// helpers --------------------------------------------------------------------

// mergeBlockedReason returns a human-readable reason the MR cannot be merged
// right now, or "" if it appears mergeable. It keys off detailedMergeStatus,
// which (unlike the coarse mergeStatusEnum) distinguishes states like
// NEED_REBASE. This avoids firing a merge GitLab would reject with an opaque
// "405 Method Not Allowed".
func mergeBlockedReason(d *gitlab.MRDetail) string {
	if d.State != "opened" {
		return "merge request is " + strings.ToLower(d.State)
	}
	// Prefer the detailed status; fall back to the coarse one if absent.
	switch d.DetailedStatus {
	case "MERGEABLE", "":
		// fall through to coarse checks below
	case "NEED_REBASE":
		return "needs rebase onto target branch (press b to rebase)"
	case "BROKEN_STATUS", "CONFLICT":
		return "has conflicts that must be resolved"
	case "CI_STILL_RUNNING":
		return "pipeline must finish first"
	case "CI_MUST_PASS":
		return "pipeline must pass first"
	case "DRAFT_STATUS":
		return "merge request is still a draft"
	case "DISCUSSIONS_NOT_RESOLVED":
		return "open threads must be resolved"
	case "NOT_APPROVED":
		return "required approvals are missing"
	case "BLOCKED_STATUS":
		return "blocked by another merge request"
	default:
		return humanizeStatus(d.DetailedStatus)
	}

	if d.ApprovalsLeft > 0 {
		return fmt.Sprintf("%d more approval(s) required", d.ApprovalsLeft)
	}
	if d.MergeStatus != "CAN_BE_MERGED" && d.MergeStatus != "MERGEABLE" && d.MergeStatus != "" {
		return humanizeStatus(d.MergeStatus)
	}
	return ""
}

// autoMergeBlockedReason is like mergeBlockedReason but tolerates a running
// pipeline, since auto-merge exists precisely to merge once it passes.
func autoMergeBlockedReason(d *gitlab.MRDetail) string {
	switch d.DetailedStatus {
	case "CI_STILL_RUNNING", "CI_MUST_PASS", "MERGEABLE":
		// A pending/running pipeline is fine for auto-merge; check other gates.
		if d.State != "opened" {
			return "merge request is " + strings.ToLower(d.State)
		}
		if d.ShouldBeRebased {
			return "needs rebase onto target branch (press b to rebase)"
		}
		return ""
	default:
		return mergeBlockedReason(d)
	}
}

// mergeStatusLabel renders the MR's mergeability as a colored label, using the
// detailed status when available so "needs rebase" is distinct from mergeable.
func mergeStatusLabel(d *gitlab.MRDetail) string {
	// Terminal states aren't merge blockers — render them in their own color.
	switch d.State {
	case "merged":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ merged")
	case "closed":
		return helpStyle.Render("✕ closed")
	}
	if reason := mergeBlockedReason(d); reason != "" {
		return errStyle.Render("⚠ " + reason)
	}
	return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ mergeable")
}

func humanizeStatus(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", " "))
}

func prefixAt(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "@" + n
	}
	return out
}

func relTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

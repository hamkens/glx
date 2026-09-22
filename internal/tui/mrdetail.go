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

	"github.com/hamkens/glx/internal/forge"
)

// --- messages ---

type detailLoadedMsg struct{ detail *forge.ChangeDetail }
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
	modeConfirmAutoMerge           // y/n auto-merge (merge once checks pass)
)

// detailModel renders one change and hosts write actions.
type detailModel struct {
	client forge.Forge
	vocab  forge.Vocabulary
	caps   forge.Capabilities
	repo   string
	id     string

	vp       viewport.Model
	spinner  spinner.Model
	textarea textarea.Model

	detail  *forge.ChangeDetail
	loading bool
	err     error
	mode    inputMode
	flash   string // transient status message (e.g. "approved")

	width  int
	height int
}

func newDetailModel(client forge.Forge, repo, id string) detailModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ta := textarea.New()
	ta.Placeholder = "Write a comment… (ctrl+s to send, esc to cancel)"
	ta.ShowLineNumbers = false

	return detailModel{
		client:   client,
		vocab:    vocabForRepo(client, repo),
		caps:     capsForRepo(client, repo),
		repo:     repo,
		id:       id,
		spinner:  sp,
		textarea: ta,
		loading:  true,
	}
}

func (m detailModel) fetchCmd(force bool) tea.Cmd {
	client, repo, id := m.client, m.repo, m.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if force {
			ctx = forge.WithForceRefresh(ctx)
		}
		d, err := client.ChangeDetail(ctx, repo, id)
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
	client, repo, id, vocab := m.client, m.repo, m.id, m.vocab
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		verb := "approved"
		if approve {
			err = client.Approve(ctx, repo, id)
		} else {
			verb = vocab.Unapprove + "d"
			err = client.Unapprove(ctx, repo, id)
		}
		return actionDoneMsg{verb: verb, err: err}
	}
}

// mergeCmd merges now. When auto is true the provider is asked to merge once
// its checks pass (GitLab auto-merge / GitHub auto-merge or merge queue).
func (m detailModel) mergeCmd(auto bool) tea.Cmd {
	client, repo, id, vocab := m.client, m.repo, m.id, m.vocab
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		outcome, err := client.Merge(ctx, repo, id, auto)
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

func (m detailModel) commentCmd(body string) tea.Cmd {
	client, repo, id := m.client, m.repo, m.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := client.AddComment(ctx, repo, id, body)
		return actionDoneMsg{verb: "commented", err: err}
	}
}

// updateBranchCmd brings the source branch up to date with its target: a rebase
// on GitLab, an update-branch merge on GitHub.
func (m detailModel) updateBranchCmd() tea.Cmd {
	client, repo, id, vocab := m.client, m.repo, m.id, m.vocab
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.UpdateBranch(ctx, repo, id)
		// Both providers run this asynchronously; the refetch triggered on
		// success reflects the new state once the server finishes.
		return actionDoneMsg{verb: vocab.UpdateBranchDone, err: err}
	}
}

func (m detailModel) setDraftCmd(draft bool) tea.Cmd {
	client, repo, id := m.client, m.repo, m.id
	title := ""
	if m.detail != nil {
		title = m.detail.Title
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := client.SetDraft(ctx, repo, id, title, draft)
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
				m.flash = "submitting merge…"
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
			if !approve && !m.caps.Unapprove {
				m.flash = errStyle.Render("✘ " + m.vocab.Unapprove + " is not supported here")
				return m, nil
			}
			m.flash = "submitting approval…"
			return m, m.approveCmd(approve)
		}
	case "M":
		if m.detail != nil {
			if reason := mergeBlockedReason(m.detail, m.vocab); reason != "" {
				m.flash = errStyle.Render("✘ can't merge: " + reason)
				return m, nil
			}
			m.mode = modeConfirmMerge
		}
		return m, nil
	case "A":
		// Auto-merge: merge once the checks pass. Unlike a plain merge, CI still
		// in flight is expected, so it isn't a blocker — but the other gates
		// (behind target, conflicts, approvals, draft) still are.
		if m.detail != nil {
			if !m.caps.AutoMerge {
				m.flash = errStyle.Render("✘ auto-merge is not supported here")
				return m, nil
			}
			if reason := autoMergeBlockedReason(m.detail, m.vocab); reason != "" {
				m.flash = errStyle.Render("✘ can't auto-merge: " + reason)
				return m, nil
			}
			m.mode = modeConfirmAutoMerge
		}
		return m, nil
	case "b":
		if m.detail != nil {
			m.flash = m.vocab.UpdateBranchGerund + "…"
			return m, m.updateBranchCmd()
		}
	case "D":
		// Toggle draft / ready.
		if m.detail != nil && m.detail.State == forge.StateOpen {
			if !m.caps.DraftToggle {
				m.flash = errStyle.Render("✘ toggling draft is not supported here")
				return m, nil
			}
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
	heading := strings.ToUpper(m.vocab.Threads[:1]) + m.vocab.Threads[1:]
	b.WriteString(titleStyle.Render(fmt.Sprintf("%s (%d)", heading, len(threads))) + "\n\n")
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
		return fmt.Sprintf("\n  %s loading %s…", m.spinner.View(), m.vocab.Change)
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

func (m detailModel) header(d *forge.ChangeDetail) string {
	title := titleStyle.Render(fmt.Sprintf("%s%s %s", m.vocab.IDPrefix, d.ID, d.Title))

	state := d.State.String()
	stateStyled := lipgloss.NewStyle().Foreground(colorGreen).Render(state)
	if d.State != forge.StateOpen {
		stateStyled = helpStyle.Render(state)
	}

	branches := helpStyle.Render(fmt.Sprintf("%s → %s", d.SourceBranch, d.TargetBranch))
	author := helpStyle.Render("by @" + d.Author)
	meta := fmt.Sprintf("%s  %s  %s", stateStyled, branches, author)

	line3 := fmt.Sprintf("%s   %s   %s", m.approvalSummary(d), m.pipelineSummary(d),
		mergeStatusLabel(d, m.vocab))

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

// approvalSummary describes the approval state. GitHub reports no approval
// count, so when the provider gives no required total this falls back to a plain
// approved / review-required reading rather than showing a bogus "0/0".
func (m detailModel) approvalSummary(d *forge.ChangeDetail) string {
	var summary string
	switch {
	case d.ApprovalsRequired > 0:
		summary = fmt.Sprintf("approvals: %d/%d",
			d.ApprovalsRequired-d.ApprovalsLeft, d.ApprovalsRequired)
	case d.ApprovalsLeft > 0:
		summary = "approvals: review required"
	case d.Approved:
		summary = "approvals: satisfied"
	default:
		summary = "approvals: none required"
	}
	if d.ApprovedByMe && d.ApprovalsLeft > 0 {
		summary = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ approved by you") +
			helpStyle.Render(fmt.Sprintf(" · %s, no action needed", summary))
	}
	if len(d.ApprovedBy) > 0 {
		summary += helpStyle.Render(" (" + strings.Join(prefixAt(d.ApprovedBy), ", ") + ")")
	}
	return summary
}

func (m detailModel) pipelineSummary(d *forge.ChangeDetail) string {
	pipe := m.vocab.Pipeline + ": " + statusGlyph(d.Pipeline)
	if d.PipelineLabel != "" {
		pipe += " " + helpStyle.Render(d.PipelineLabel)
	}
	return pipe
}

func (m detailModel) footer() string {
	if m.mode == modeConfirmMerge {
		return errStyle.Render("  merge this " + m.vocab.ChangeAbbrev + " now? [y/N]")
	}
	if m.mode == modeConfirmAutoMerge {
		return errStyle.Render("  set auto-merge (merge when checks pass)? [y/N]")
	}
	approveLabel := "a " + m.vocab.Approve
	if m.detail != nil && (m.detail.Approved || m.detail.ApprovedByMe) {
		approveLabel = "a " + m.vocab.Unapprove
	}
	draftLabel := "D draft"
	if m.detail != nil && m.detail.Draft {
		draftLabel = "D ready"
	}
	return helpStyle.Render(fmt.Sprintf(
		"  %s · %s · d diff · p %s · M merge · A auto-merge · b %s · c comment · ? help",
		approveLabel, draftLabel, m.vocab.Pipeline, m.vocab.UpdateBranch))
}

// helpers --------------------------------------------------------------------

// mergeBlockedReason returns a human-readable reason the change cannot be
// merged right now, or "" if it appears mergeable. It reads the normalized
// MergeState rather than either provider's raw status, which is what keeps glx
// from firing a merge the server would reject with an opaque 405.
func mergeBlockedReason(d *forge.ChangeDetail, v forge.Vocabulary) string {
	if d.State != forge.StateOpen {
		return v.Change + " is " + d.State.String()
	}
	switch d.MergeState {
	case forge.MergeStateMergeable, forge.MergeStateUnknown:
		// No known blocker; fall through to the approval check below.
	case forge.MergeStateChecking:
		return "mergeability is still being checked"
	case forge.MergeStateNeedsUpdate:
		return fmt.Sprintf("is behind its target branch (press b to %s)", v.UpdateBranch)
	case forge.MergeStateConflict:
		return "has conflicts that must be resolved"
	case forge.MergeStateCIRunning:
		return v.Pipeline + " must finish first"
	case forge.MergeStateCIFailed:
		return v.Pipeline + " must pass first"
	case forge.MergeStateDraft:
		return v.Change + " is still a draft"
	case forge.MergeStateThreadsUnresolved:
		return "open " + v.Threads + " must be resolved"
	case forge.MergeStateNotApproved:
		return "required approvals are missing"
	case forge.MergeStateChangesRequested:
		return "a reviewer requested changes"
	case forge.MergeStateBlocked:
		return "blocked by a branch rule or another " + v.Change
	default:
		return d.MergeState.String()
	}

	if d.ApprovalsLeft > 0 {
		return fmt.Sprintf("%d more approval(s) required", d.ApprovalsLeft)
	}
	// GitLab can report MERGEABLE while the branch is still behind its target.
	if d.NeedsUpdate {
		return fmt.Sprintf("is behind its target branch (press b to %s)", v.UpdateBranch)
	}
	return ""
}

// autoMergeBlockedReason is like mergeBlockedReason but tolerates CI still in
// flight, since auto-merge exists precisely to merge once it passes.
func autoMergeBlockedReason(d *forge.ChangeDetail, v forge.Vocabulary) string {
	if !d.MergeState.CIPending() && !d.MergeState.Mergeable() {
		return mergeBlockedReason(d, v)
	}
	if d.State != forge.StateOpen {
		return v.Change + " is " + d.State.String()
	}
	if d.Draft {
		return v.Change + " is still a draft"
	}
	if d.NeedsUpdate {
		return fmt.Sprintf("is behind its target branch (press b to %s)", v.UpdateBranch)
	}
	return ""
}

// mergeStatusLabel renders mergeability as a colored label.
func mergeStatusLabel(d *forge.ChangeDetail, v forge.Vocabulary) string {
	// Terminal states aren't merge blockers — render them in their own color.
	switch d.State {
	case forge.StateMerged:
		return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ merged")
	case forge.StateClosed:
		return helpStyle.Render("✕ closed")
	}
	if reason := mergeBlockedReason(d, v); reason != "" {
		return errStyle.Render("⚠ " + reason)
	}
	return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ mergeable")
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

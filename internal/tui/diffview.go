package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/forge"
)

// --- messages ---

type diffLoadedMsg struct{ diff *forge.Diff }
type diffErrMsg struct{ err error }

// diffMode is the diff view's transient input state.
type diffMode int

const (
	diffModeNone diffMode = iota
	diffModeComment
)

// diffModel renders the changed files of a change with a file list, a
// syntax-highlighted diff pane, a line cursor, and inline commenting.
type diffModel struct {
	client forge.Forge
	repo   string
	id     string

	spinner  spinner.Model
	textarea textarea.Model

	diff      *forge.Diff
	fileIdx   int        // selected file
	lines     []diffLine // parsed lines of the selected file
	lineCur   int        // cursor within lines (for commenting)
	scroll    int        // top visible line index in the diff pane
	loading   bool
	err       error
	mode      diffMode
	flash     string
	commentAt int // line index being commented on

	width  int
	height int
}

func newDiffModel(client forge.Forge, repo, id string) diffModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ta := textarea.New()
	ta.Placeholder = "Inline comment… (ctrl+s to post, esc to cancel)"
	ta.ShowLineNumbers = false

	return diffModel{
		client:   client,
		repo:     repo,
		id:       id,
		spinner:  sp,
		textarea: ta,
		loading:  true,
	}
}

func (m diffModel) fetchCmd(force bool) tea.Cmd {
	client, repo, id := m.client, m.repo, m.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if force {
			ctx = forge.WithForceRefresh(ctx)
		}
		d, err := client.ChangeDiff(ctx, repo, id)
		if err != nil {
			return diffErrMsg{err}
		}
		return diffLoadedMsg{d}
	}
}

func (m diffModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchCmd(false))
}

// postCommentCmd posts a positioned inline comment for the line at idx.
func (m diffModel) postCommentCmd(idx int, body string) tea.Cmd {
	client, repo, id := m.client, m.repo, m.id
	f := m.diff.Files[m.fileIdx]
	refs := m.diff.Refs
	ln := m.lines[idx]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		dc := forge.DiffComment{
			Refs:    refs,
			NewPath: f.NewPath,
			OldPath: f.OldPath,
			Body:    body,
		}
		// Anchor on the new side for add/context lines, old side for deletions.
		switch ln.kind {
		case lineDel:
			dc.OldLine = ln.oldLine
		default:
			dc.NewLine = ln.newLine
		}
		err := client.AddDiffComment(ctx, repo, id, dc)
		return actionDoneMsg{verb: "commented", err: err}
	}
}

func (m *diffModel) loadFile(idx int) {
	if m.diff == nil || idx < 0 || idx >= len(m.diff.Files) {
		m.lines = nil
		return
	}
	m.fileIdx = idx
	m.lines = parseUnifiedDiff(m.diff.Files[idx].Diff)
	m.lineCur = 0
	m.scroll = 0
}

func (m diffModel) Update(msg tea.Msg) (diffModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case diffLoadedMsg:
		m.loading = false
		m.err = nil
		m.diff = msg.diff
		m.loadFile(0)
		return m, nil

	case diffErrMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case actionDoneMsg:
		if msg.err != nil {
			m.flash = errStyle.Render("✘ comment failed: " + msg.err.Error())
		} else {
			m.flash = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ comment posted")
		}
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

func (m diffModel) handleKey(msg tea.KeyMsg) (diffModel, tea.Cmd) {
	if m.mode == diffModeComment {
		switch msg.String() {
		case "esc":
			m.mode = diffModeNone
			m.textarea.Reset()
			m.textarea.Blur()
			return m, nil
		case "ctrl+s":
			body := strings.TrimSpace(m.textarea.Value())
			m.mode = diffModeNone
			m.textarea.Reset()
			m.textarea.Blur()
			if body == "" {
				return m, nil
			}
			m.flash = "posting comment…"
			return m, m.postCommentCmd(m.commentAt, body)
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "right", "L":
		if m.diff != nil && m.fileIdx < len(m.diff.Files)-1 {
			m.loadFile(m.fileIdx + 1)
		}
	case "left", "H":
		if m.diff != nil && m.fileIdx > 0 {
			m.loadFile(m.fileIdx - 1)
		}
	case "down", "j":
		m.moveCursor(1)
	case "up", "k":
		m.moveCursor(-1)
	case "r":
		m.loading = true
		m.flash = ""
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true))
	case "c":
		if ln, ok := m.currentLine(); ok && canComment(ln.kind) {
			m.commentAt = m.lineCur
			m.mode = diffModeComment
			m.textarea.Focus()
			return m, textarea.Blink
		}
		m.flash = helpStyle.Render("select an added/removed/context line to comment")
	}
	return m, nil
}

func (m *diffModel) moveCursor(delta int) {
	if len(m.lines) == 0 {
		return
	}
	m.lineCur += delta
	if m.lineCur < 0 {
		m.lineCur = 0
	}
	if m.lineCur >= len(m.lines) {
		m.lineCur = len(m.lines) - 1
	}
	// Keep the cursor within the visible window.
	h := m.diffPaneHeight()
	if m.lineCur < m.scroll {
		m.scroll = m.lineCur
	}
	if m.lineCur >= m.scroll+h {
		m.scroll = m.lineCur - h + 1
	}
}

func (m diffModel) currentLine() (diffLine, bool) {
	if m.lineCur < 0 || m.lineCur >= len(m.lines) {
		return diffLine{}, false
	}
	return m.lines[m.lineCur], true
}

func canComment(k diffLineKind) bool {
	return k == lineAdd || k == lineDel || k == lineContext
}

func (m diffModel) diffPaneHeight() int {
	// header(2) + filebar(1) + footer(1)
	h := m.height - 4
	if m.mode == diffModeComment {
		h -= 5 // composer
	}
	if h < 3 {
		h = 3
	}
	return h
}

func (m diffModel) View() string {
	if m.loading && m.diff == nil {
		return fmt.Sprintf("\n  %s loading diff…", m.spinner.View())
	}
	if m.err != nil {
		return "\n  " + errStyle.Render("error: "+m.err.Error()) + "\n  " + helpStyle.Render("esc back")
	}
	if m.diff == nil || len(m.diff.Files) == 0 {
		return "\n  " + helpStyle.Render("no changed files") + "\n  " + helpStyle.Render("esc back")
	}

	header := m.header()
	fileBar := m.fileBar()
	pane := m.diffPane()

	if m.mode == diffModeComment {
		return lipgloss.JoinVertical(lipgloss.Left,
			header, fileBar, pane,
			titleStyle.Render("Inline comment"),
			m.textarea.View(),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, fileBar, pane, m.footer())
}

func (m diffModel) header() string {
	f := m.diff.Files[m.fileIdx]
	tag := ""
	switch {
	case f.NewFile:
		tag = lipgloss.NewStyle().Foreground(colorGreen).Render(" [new]")
	case f.Deleted:
		tag = errStyle.Render(" [deleted]")
	case f.Renamed:
		tag = helpStyle.Render(" [renamed]")
	}
	counter := helpStyle.Render(fmt.Sprintf("file %d/%d", m.fileIdx+1, len(m.diff.Files)))
	title := titleStyle.Render(f.NewPath) + tag
	flash := m.flash
	if flash != "" {
		flash = "   " + flash
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, counter+flash)
}

// fileBar shows a compact horizontal strip of file basenames around the cursor.
func (m diffModel) fileBar() string {
	var parts []string
	for i, f := range m.diff.Files {
		name := basename(f.NewPath)
		if i == m.fileIdx {
			parts = append(parts, titleStyle.Render(name))
		} else {
			parts = append(parts, helpStyle.Render(name))
		}
	}
	bar := strings.Join(parts, helpStyle.Render(" │ "))
	return truncateToWidth(bar, m.width)
}

func (m diffModel) diffPane() string {
	h := m.diffPaneHeight()
	if len(m.lines) == 0 {
		f := m.diff.Files[m.fileIdx]
		if f.TooLarge {
			return "\n  " + helpStyle.Render("diff too large to display inline")
		}
		return "\n  " + helpStyle.Render("(no textual changes)")
	}

	path := m.diff.Files[m.fileIdx].NewPath
	var b strings.Builder
	end := m.scroll + h
	if end > len(m.lines) {
		end = len(m.lines)
	}
	for i := m.scroll; i < end; i++ {
		b.WriteString(m.renderLine(path, i))
		b.WriteByte('\n')
	}
	return b.String()
}

func (m diffModel) renderLine(path string, i int) string {
	ln := m.lines[i]
	cursor := "  "
	if i == m.lineCur && ln.kind != lineHunk && ln.kind != lineMeta {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}

	gutter := lineGutter(ln)

	var content string
	switch ln.kind {
	case lineHunk:
		return lipgloss.NewStyle().Foreground(colorAccent).Render(cursor + ln.text)
	case lineMeta:
		return helpStyle.Render(cursor + ln.text)
	case lineAdd:
		content = lipgloss.NewStyle().Foreground(colorGreen).Render("+ " + highlightLine(path, ln.text))
	case lineDel:
		content = errStyle.Render("- " + ln.text)
	default: // context
		content = "  " + highlightLine(path, ln.text)
	}
	return cursor + gutter + content
}

// lineGutter renders the old/new line-number gutter for a diff line.
func lineGutter(ln diffLine) string {
	oldCol, newCol := "    ", "    "
	if ln.oldLine > 0 {
		oldCol = fmt.Sprintf("%4d", ln.oldLine)
	}
	if ln.newLine > 0 {
		newCol = fmt.Sprintf("%4d", ln.newLine)
	}
	return helpStyle.Render(oldCol+" "+newCol) + " "
}

func (m diffModel) footer() string {
	ln, ok := m.currentLine()
	commentHint := "c comment"
	if !ok || !canComment(ln.kind) {
		commentHint = helpStyle.Render("c comment")
	}
	return helpStyle.Render(fmt.Sprintf("  ←/→ file · ↑/↓ line · %s · r refresh · esc/⌫ back · q quit", commentHint))
}

func basename(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

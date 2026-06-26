package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/gitlab"
	"github.com/hamkens/glx/internal/watch"
)

// watchRefreshedMsg signals that a manual watch-view refresh has completed.
type watchRefreshedMsg struct{}

// watchModel renders the list of watched pipelines. State lives in the shared
// registry (updated by the root's background poller); this view reads it and
// offers manual refresh + unwatch.
type watchModel struct {
	client *gitlab.Client
	reg    *watch.Registry

	entries []watch.Entry
	cur     int
	scroll  int

	width  int
	height int
}

func newWatchModel(client *gitlab.Client, reg *watch.Registry) watchModel {
	m := watchModel{client: client, reg: reg}
	m.entries = reg.List()
	return m
}

func (m *watchModel) reload() {
	m.entries = m.reg.List()
	if m.cur >= len(m.entries) {
		m.cur = len(m.entries) - 1
	}
	if m.cur < 0 {
		m.cur = 0
	}
}

func (m watchModel) selected() (watch.Entry, bool) {
	if m.cur >= 0 && m.cur < len(m.entries) {
		return m.entries[m.cur], true
	}
	return watch.Entry{}, false
}

// refreshCmd re-fetches every watched pipeline (including settled ones) so the
// manual refresh reflects the very latest state.
func (m watchModel) refreshCmd() tea.Cmd {
	client, reg := m.client, m.reg
	entries := reg.List()
	return func() tea.Msg {
		for _, e := range entries {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			p, err := client.PipelineWithJobs(gitlab.WithForceRefresh(ctx), e.ProjectPath, e.PipelineID)
			cancel()
			if err == nil && p != nil {
				reg.Update(e.ProjectPath, p)
			}
		}
		return watchRefreshedMsg{}
	}
}

func (m watchModel) Update(msg tea.Msg) (watchModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case watchRefreshedMsg:
		m.reload()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "down", "j":
			if m.cur < len(m.entries)-1 {
				m.cur++
				m.scrollToCursor()
			}
		case "up", "k":
			if m.cur > 0 {
				m.cur--
				m.scrollToCursor()
			}
		case "r":
			return m, m.refreshCmd()
		case "x":
			// Unwatch the selected entry (w is handled globally; x is a
			// view-local convenience that doesn't conflict).
			if e, ok := m.selected(); ok {
				m.reg.Remove(e.ProjectPath, e.PipelineID)
				m.reload()
			}
		}
	}
	return m, nil
}

func (m *watchModel) scrollToCursor() {
	h := m.listHeight()
	if m.cur < m.scroll {
		m.scroll = m.cur
	}
	if m.cur >= m.scroll+h {
		m.scroll = m.cur - h + 1
	}
}

func (m watchModel) listHeight() int {
	h := m.height - 3 // header(2) + footer(1)
	if h < 3 {
		h = 3
	}
	return h
}

func (m watchModel) View() string {
	header := titleStyle.Render(fmt.Sprintf("Watched pipelines (%d)", len(m.entries)))
	sub := helpStyle.Render("auto-refreshes active pipelines every 15s in the background")

	var body string
	if len(m.entries) == 0 {
		body = "\n  " + helpStyle.Render("nothing watched — press w on a pipeline to start") + "\n"
	} else {
		body = m.rows()
	}
	body = padToHeight(body, m.listHeight())

	footer := helpStyle.Render("  enter open · w/x unwatch · r refresh · ↑/↓ move · esc back")
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinVertical(lipgloss.Left, header, sub), body, footer)
}

func (m watchModel) rows() string {
	h := m.listHeight()
	end := m.scroll + h
	if end > len(m.entries) {
		end = len(m.entries)
	}
	var out []string
	for i := m.scroll; i < end; i++ {
		out = append(out, m.renderEntry(i))
	}
	return joinLines(out)
}

func (m watchModel) renderEntry(i int) string {
	e := m.entries[i]
	cursor := "  "
	if i == m.cur {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▌ ")
	}
	status := e.Status
	if status == "" {
		status = "…"
	}
	glyph := jobGlyph(e.Status)
	live := ""
	if e.Active() {
		live = helpStyle.Render(" ·live")
	}
	id := lipgloss.NewStyle().Foreground(colorSubtle).Render(fmt.Sprintf("#%d", e.PipelineID))
	proj := shortProject(e.ProjectPath)
	mr := ""
	if e.MRIID != "" {
		mr = helpStyle.Render("  !" + e.MRIID)
	}
	line := fmt.Sprintf("%s%s %-9s %s  %s%s%s",
		cursor, glyph, status, id, proj, mr, live)
	return truncateToWidth(line, m.width)
}

// joinLines joins with newlines without a trailing newline.
func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

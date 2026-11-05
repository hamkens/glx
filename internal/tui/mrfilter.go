package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/forge"
)

// Zero values preserve the unfiltered list. Each tab owns its own settings.
type listFilter struct {
	query              string
	hideDrafts         bool
	hideApprovedMe     bool
	hideMerged         bool
	hideAwaitingAuthor bool
}

func (f listFilter) active() bool {
	return strings.TrimSpace(f.query) != "" || f.hideDrafts || f.hideApprovedMe || f.hideMerged || f.hideAwaitingAuthor
}

func (f listFilter) matches(mr forge.Change) bool {
	if (f.hideDrafts && mr.Draft) || (f.hideApprovedMe && mr.ApprovedByMe) || (f.hideMerged && mr.MergedAt != "") {
		return false
	}
	if f.hideAwaitingAuthor && mr.ReviewState.FeedbackGiven() {
		return false
	}
	hay := strings.ToLower(mr.Title + " " + mr.Repo + " " + mr.SourceBranch)
	return containsAll(hay, strings.Fields(strings.ToLower(f.query)))
}

func (f listFilter) summary() string {
	var parts []string
	if q := strings.TrimSpace(f.query); q != "" {
		parts = append(parts, fmt.Sprintf("%q", q))
	}
	if f.hideDrafts {
		parts = append(parts, "no drafts")
	}
	if f.hideApprovedMe {
		parts = append(parts, "not approved by me")
	}
	if f.hideMerged {
		parts = append(parts, "no merged")
	}
	if f.hideAwaitingAuthor {
		parts = append(parts, "not awaiting author")
	}
	return strings.Join(parts, " · ")
}

func (m mrListModel) filterCount() string {
	loaded := len(m.mrs) + len(m.merged)
	shown := len(m.applyFilter(m.mrs)) + len(m.applyFilter(m.merged))
	return fmt.Sprintf("%d shown / %d loaded", shown, loaded)
}

func (m *mrListModel) resizeFilterInput() {
	m.filterInput.Width = max(1, min(54, m.width-4)-4-lipgloss.Width(m.filterInput.Prompt))
}

func (m mrListModel) openFilters() (mrListModel, tea.Cmd) {
	m.filterMode = true
	m.filterFocus = 0
	m.filterDraft = m.filters[m.scopeIdx]
	m.filterInput.SetValue(m.filterDraft.query)
	m.filterInput.CursorEnd()
	m.resizeFilterInput()
	cmd := m.filterInput.Focus()
	return m, cmd
}

func (m mrListModel) filterAwaitingAuthorIndex() int {
	if m.scopeHasMerged() {
		return 4
	}
	return 3
}

func (m mrListModel) filterResetIndex() int { return m.filterAwaitingAuthorIndex() + 1 }

func (m mrListModel) handleFilterKey(msg tea.KeyMsg) (mrListModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterMode = false
		m.filterInput.Blur()
		return m, nil
	case "enter":
		if m.filterFocus == m.filterResetIndex() {
			return m.resetFilterDraft()
		}
		m.filterDraft.query = m.filterInput.Value()
		m.filters[m.scopeIdx] = m.filterDraft
		m.filterMode = false
		m.filterInput.Blur()
		m.cur, m.scroll = 0, 0
		m.rebuildRows()
		cmd := m.maybeLoadMore()
		return m, cmd
	case "tab", "down", "shift+tab", "up":
		delta := 1
		if msg.String() == "shift+tab" || msg.String() == "up" {
			delta = -1
		}
		n := m.filterResetIndex() + 1
		m.filterFocus = (m.filterFocus + delta + n) % n
		if m.filterFocus == 0 {
			cmd := m.filterInput.Focus()
			return m, cmd
		}
		m.filterInput.Blur()
		return m, nil
	case " ":
		if m.filterFocus == m.filterResetIndex() {
			return m.resetFilterDraft()
		}
		if m.filterFocus == m.filterAwaitingAuthorIndex() {
			m.filterDraft.hideAwaitingAuthor = !m.filterDraft.hideAwaitingAuthor
			return m, nil
		}
		switch m.filterFocus {
		case 1:
			m.filterDraft.hideDrafts = !m.filterDraft.hideDrafts
		case 2:
			m.filterDraft.hideApprovedMe = !m.filterDraft.hideApprovedMe
		case 3:
			m.filterDraft.hideMerged = !m.filterDraft.hideMerged
		}
	}
	if m.filterFocus == 0 {
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m mrListModel) resetFilterDraft() (mrListModel, tea.Cmd) {
	m.filterDraft = listFilter{}
	m.filterInput.SetValue("")
	m.filterFocus = 0
	cmd := m.filterInput.Focus()
	return m, cmd
}

func (m mrListModel) filterView() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Filters · "+scopeOrder[m.scopeIdx].label), "", m.filterInput.View(), "")
	option := func(index int, label string, included bool) {
		check := "[ ] "
		if included {
			check = "[x] "
		}
		line := "  " + check + label
		if m.filterFocus == index {
			line = titleStyle.Render("› " + check + label)
		}
		lines = append(lines, line)
	}
	option(1, "Include drafts", !m.filterDraft.hideDrafts)
	option(2, "Include "+m.vocab.ChangeAbbrev+"s approved by me", !m.filterDraft.hideApprovedMe)
	if m.scopeHasMerged() {
		option(3, "Include recently merged", !m.filterDraft.hideMerged)
	}
	option(m.filterAwaitingAuthorIndex(), "Include awaiting author", !m.filterDraft.hideAwaitingAuthor)
	reset := "  Reset"
	if m.filterFocus == m.filterResetIndex() {
		reset = titleStyle.Render("› Reset")
	}
	lines = append(lines, reset, "", helpStyle.Render("Tab/↑↓ move · Space toggle"), helpStyle.Render("Enter apply · Esc cancel"))
	width := max(1, min(54, m.width-4)-4)
	for i, line := range lines {
		lines[i] = truncateToWidth(line, width)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).
		Padding(0, 1).Width(width + 2).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.listHeight(), lipgloss.Center, lipgloss.Center, box)
}

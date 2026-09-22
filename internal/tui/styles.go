package tui

import (
	"strconv"

	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/forge"
)

// Shared palette and styles for the glx TUI.
var (
	colorSubtle = lipgloss.Color("240")
	colorAccent = lipgloss.Color("12")
	colorGreen  = lipgloss.Color("10")
	colorRed    = lipgloss.Color("9")
	colorYellow = lipgloss.Color("11")
	colorGray   = lipgloss.Color("8")

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().Foreground(colorSubtle)

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	errStyle = lipgloss.NewStyle().Foreground(colorRed)
)

// statusGlyph maps a normalized CI status to a colored single-char glyph. Both
// pipeline-level and job-level statuses go through here: the provider-specific
// spellings are already resolved by the forge layer.
func statusGlyph(s forge.Status) string {
	switch s {
	case forge.StatusSuccess:
		return lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	case forge.StatusFailed:
		return lipgloss.NewStyle().Foreground(colorRed).Render("✘")
	case forge.StatusRunning:
		return lipgloss.NewStyle().Foreground(colorYellow).Render("◐")
	case forge.StatusPending:
		return lipgloss.NewStyle().Foreground(colorYellow).Render("◔")
	case forge.StatusManual:
		return lipgloss.NewStyle().Foreground(colorGray).Render("⏻")
	case forge.StatusCanceled, forge.StatusSkipped:
		return lipgloss.NewStyle().Foreground(colorGray).Render("○")
	case forge.StatusNone:
		return lipgloss.NewStyle().Foreground(colorGray).Render("·")
	default:
		return lipgloss.NewStyle().Foreground(colorGray).Render("◌")
	}
}

// mergeStateTag returns a short colored tag for a merge state worth flagging in
// a list row (needs update, blocked, unresolved threads). It returns "" for
// mergeable and benign states, which need no tag; conflicts are handled
// separately via the dedicated conflicts flag. Wording follows the provider, so
// GitLab shows "rebase" where GitHub shows "update".
func mergeStateTag(s forge.MergeState, v forge.Vocabulary) string {
	tag := ""
	switch s {
	case forge.MergeStateNeedsUpdate:
		tag = v.UpdateBranch
	case forge.MergeStateBlocked:
		tag = "blocked"
	case forge.MergeStateThreadsUnresolved:
		tag = v.Threads
	case forge.MergeStateChangesRequested:
		tag = "changes"
	default:
		return ""
	}
	return lipgloss.NewStyle().Foreground(colorYellow).Render(tag)
}

// approvalGlyph shows approval state compactly:
//   - a GREEN check when the current user has approved it
//   - a DEFAULT-colored check when approved (by others) but not by me
//   - the number of approvals still required, in yellow
//   - a dash when none are required and it's unapproved
func approvalGlyph(approved, approvedByMe bool, left int) string {
	if approvedByMe {
		return lipgloss.NewStyle().Foreground(colorGreen).Render("✓")
	}
	if approved {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Render("✓")
	}
	if left > 0 {
		return lipgloss.NewStyle().Foreground(colorYellow).Render(strconv.Itoa(left))
	}
	return lipgloss.NewStyle().Foreground(colorSubtle).Render("-")
}

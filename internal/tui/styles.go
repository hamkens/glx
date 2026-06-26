package tui

import "github.com/charmbracelet/lipgloss"

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

// pipelineGlyph maps a GraphQL pipeline status to a colored single-char glyph.
func pipelineGlyph(status string) string {
	switch status {
	case "SUCCESS":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	case "FAILED":
		return lipgloss.NewStyle().Foreground(colorRed).Render("✘")
	case "RUNNING", "PENDING":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("◐")
	case "CANCELED", "SKIPPED", "MANUAL":
		return lipgloss.NewStyle().Foreground(colorGray).Render("○")
	case "":
		return lipgloss.NewStyle().Foreground(colorGray).Render("·")
	default:
		return lipgloss.NewStyle().Foreground(colorGray).Render("◌")
	}
}

// jobGlyph maps a REST job/pipeline status (lowercase) to a colored glyph.
func jobGlyph(status string) string {
	switch status {
	case "success":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	case "failed":
		return lipgloss.NewStyle().Foreground(colorRed).Render("✘")
	case "running":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("◐")
	case "pending", "created", "scheduled", "waiting_for_resource", "preparing":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("◔")
	case "manual":
		return lipgloss.NewStyle().Foreground(colorGray).Render("⏻")
	case "canceled", "skipped":
		return lipgloss.NewStyle().Foreground(colorGray).Render("○")
	default:
		return lipgloss.NewStyle().Foreground(colorGray).Render("◌")
	}
}

// mergeStatusTag returns a short colored tag for a detailedMergeStatus value
// worth flagging in a list row (e.g. needs rebase, blocked). Returns "" for
// mergeable/benign statuses, which need no tag. "conflict" is handled
// separately via the dedicated conflicts flag.
func mergeStatusTag(detailed string) string {
	switch detailed {
	case "NEED_REBASE":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("rebase")
	case "BLOCKED_STATUS":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("blocked")
	case "DISCUSSIONS_NOT_RESOLVED":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("threads")
	default:
		return ""
	}
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
		return lipgloss.NewStyle().Foreground(colorYellow).Render(itoa(left))
	}
	return lipgloss.NewStyle().Foreground(colorSubtle).Render("-")
}

// itoa is a tiny strconv.Itoa to avoid an import here.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

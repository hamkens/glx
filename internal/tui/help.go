package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// keyHelp is a single key/description pair shown in the help overlay.
type keyHelp struct {
	key  string
	desc string
}

// helpSections returns the keybinding groups for a given view.
func helpSections(v view) []struct {
	title string
	keys  []keyHelp
} {
	global := []keyHelp{
		{"?", "toggle this help"},
		{"o", "open current item in browser"},
		{"w", "watch/unwatch this pipeline"},
		{"W", "open watch list"},
		{"q / ctrl+c / ctrl+d", "quit"},
		{"r", "refresh (bypass cache)"},
	}

	switch v {
	case viewWatch:
		return []struct {
			title string
			keys  []keyHelp
		}{
			{"Watched pipelines", []keyHelp{
				{"↑/↓", "move"},
				{"enter", "open pipeline"},
				{"w / x", "unwatch selected"},
				{"r", "refresh all now"},
				{"esc / ⌫", "back"},
			}},
			{"Background", []keyHelp{
				{"(auto)", "active pipelines polled every 15s"},
				{"(alert)", "changes shown bottom-right + bell"},
			}},
			{"Global", global},
		}
	case viewDetail:
		return []struct {
			title string
			keys  []keyHelp
		}{
			{"Merge request", []keyHelp{
				{"a", "approve / unapprove"},
				{"M", "merge now (confirm y/N)"},
				{"A", "auto-merge when pipeline passes"},
				{"b", "rebase onto target"},
				{"c", "comment"},
				{"d", "open diff"},
				{"p", "open pipeline"},
				{"↑/↓", "scroll"},
				{"esc / ⌫", "back to list"},
			}},
			{"Global", global},
		}
	case viewDiff:
		return []struct {
			title string
			keys  []keyHelp
		}{
			{"Diff", []keyHelp{
				{"←/→", "previous / next file"},
				{"↑/↓ or j/k", "move line cursor"},
				{"c", "comment on current line"},
				{"esc / ⌫", "back to detail"},
			}},
			{"Comment composer", []keyHelp{
				{"ctrl+s", "post comment"},
				{"esc", "cancel"},
			}},
			{"Global", global},
		}
	case viewPipeline:
		return []struct {
			title string
			keys  []keyHelp
		}{
			{"Pipeline", []keyHelp{
				{"↑/↓ or j/k", "move between jobs"},
				{"enter", "view job log"},
				{"R", "retry job"},
				{"x", "cancel job"},
				{"r", "refresh now"},
				{"esc / ⌫", "back to detail"},
			}},
			{"Pipeline — auto", []keyHelp{
				{"(auto)", "refreshes every 15s while running"},
				{"(alert)", "status changes shown bottom-right"},
			}},
			{"Global", global},
		}
	case viewJobLog:
		return []struct {
			title string
			keys  []keyHelp
		}{
			{"Job log", []keyHelp{
				{"↑/↓", "scroll"},
				{"g / G", "top / bottom"},
				{"t", "toggle tail"},
				{"esc / ⌫", "back to pipeline"},
			}},
			{"Global", global},
		}
	default: // list
		return []struct {
			title string
			keys  []keyHelp
		}{
			{"Merge request list", []keyHelp{
				{"enter", "open merge request"},
				{"←/→ or tab", "switch scope"},
				{"/", "filter"},
				{"↑/↓", "move (auto-loads more)"},
			}},
			{"Actions on selected MR", []keyHelp{
				{"a", "approve / unapprove"},
				{"M", "merge now (confirm y/N)"},
				{"A", "auto-merge when pipeline passes"},
				{"b", "rebase onto target"},
				{"d", "open diff"},
				{"p", "open pipeline"},
			}},
			{"Legend — pipeline (1st glyph)", []keyHelp{
				{pipelineGlyph("SUCCESS"), "passed"},
				{pipelineGlyph("FAILED"), "failed"},
				{pipelineGlyph("RUNNING"), "running / pending"},
				{pipelineGlyph("CANCELED"), "canceled / skipped / manual"},
				{pipelineGlyph(""), "no pipeline"},
			}},
			{"Legend — approvals (2nd glyph)", []keyHelp{
				{approvalGlyph(true, true, 0), "approved by me"},
				{approvalGlyph(true, false, 0), "approved (by others)"},
				{approvalGlyph(false, false, 2), "N approvals still required"},
				{approvalGlyph(false, false, 0), "none required"},
			}},
			{"Legend — flags", []keyHelp{
				{lipgloss.NewStyle().Foreground(colorSubtle).Render("draft"), "work in progress"},
				{errStyle.Render("conflict"), "merge conflicts"},
				{mergeStatusTag("NEED_REBASE"), "needs rebase onto target"},
				{mergeStatusTag("BLOCKED_STATUS"), "blocked by another MR"},
				{mergeStatusTag("DISCUSSIONS_NOT_RESOLVED"), "unresolved discussions"},
			}},
			{"Global", global},
		}
	}
}

// renderHelpOverlay produces a centered help panel for the active view.
func renderHelpOverlay(v view, width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("glx — keybindings"))
	b.WriteString("\n\n")

	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	for _, sec := range helpSections(v) {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(sec.title))
		b.WriteString("\n")
		for _, k := range sec.keys {
			// Pre-styled keys (legend glyphs/tags) carry their own ANSI colors;
			// render them as-is. Plain key names get the accent style.
			var key string
			if strings.Contains(k.key, "\x1b") {
				key = padDisplay(k.key, 16)
			} else {
				key = keyStyle.Render(padDisplay(k.key, 16))
			}
			b.WriteString("  " + key + helpStyle.Render(k.desc) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(helpStyle.Render("press ? or esc to close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSubtle).
		Padding(1, 3).
		Render(b.String())

	if width <= 0 || height <= 0 {
		return box
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// padDisplay right-pads s to n display columns, ignoring ANSI escape codes so
// pre-styled keys align with plain ones.
func padDisplay(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-w)
}

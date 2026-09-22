package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/forge"
)

// keyHelp is a single key/description pair shown in the help overlay.
type keyHelp struct {
	key  string
	desc string
}

// helpSection is one titled group of keybindings.
type helpSection struct {
	title string
	keys  []keyHelp
}

// changeActionKeys are the write actions available on a selected change, shared
// by the list and detail views. Wording and availability follow the provider:
// unsupported actions are omitted rather than listed and then rejected.
func changeActionKeys(v forge.Vocabulary, caps forge.Capabilities) []keyHelp {
	approve := v.Approve
	if caps.Unapprove {
		approve += " / " + v.Unapprove
	}
	keys := []keyHelp{
		{"a", approve},
		{"M", "merge / add to " + v.MergeQueue + " (confirm y/N)"},
	}
	if caps.AutoMerge {
		keys = append(keys, keyHelp{"A", "auto-merge when the " + v.Pipeline + " passes"})
	}
	keys = append(keys, keyHelp{"b", v.UpdateBranch + " onto target"})
	if caps.DraftToggle {
		keys = append(keys, keyHelp{"D", "toggle draft / ready"})
	}
	return keys
}

// helpSections returns the keybinding groups for a given view.
func helpSections(v view, vocab forge.Vocabulary, caps forge.Capabilities) []helpSection {
	global := []keyHelp{
		{"?", "toggle this help"},
		{"o", "open current item in browser"},
		{"w", "watch/unwatch this " + vocab.Pipeline},
		{"W", "open watch list"},
		{"q / ctrl+c / ctrl+d", "quit"},
		{"r", "refresh (bypass cache)"},
	}

	// Title-cased provider nouns for section headings.
	changeTitle := title(vocab.Change)
	pipelineTitle := title(vocab.Pipeline)

	switch v {
	case viewWatch:
		return []helpSection{
			{"Watched " + vocab.Pipeline + "s", []keyHelp{
				{"↑/↓", "move"},
				{"enter", "open " + vocab.Pipeline},
				{"w / x", "unwatch selected"},
				{"r", "refresh all now"},
				{"esc / ⌫", "back"},
			}},
			{"Background", []keyHelp{
				{"(auto)", "active " + vocab.Pipeline + "s polled every 15s"},
				{"(alert)", "changes shown bottom-right + bell"},
			}},
			{"Global", global},
		}
	case viewDetail:
		keys := append(changeActionKeys(vocab, caps),
			keyHelp{"c", "comment"},
			keyHelp{"d", "open diff"},
			keyHelp{"p", "open " + vocab.Pipeline},
			keyHelp{"↑/↓", "scroll"},
			keyHelp{"esc / ⌫", "back to list"},
		)
		return []helpSection{
			{changeTitle, keys},
			{"Global", global},
		}
	case viewDiff:
		return []helpSection{
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
		cancel := "cancel job"
		if caps.CancelJobIsRunWide {
			cancel = "cancel the whole " + vocab.Pipeline + " (no per-job cancel)"
		}
		return []helpSection{
			{pipelineTitle, []keyHelp{
				{"↑/↓ or j/k", "move between jobs"},
				{"enter", "view job log"},
				{"R", "retry job"},
				{"x", cancel},
				{"r", "refresh now"},
				{"esc / ⌫", "back to detail"},
			}},
			{pipelineTitle + " — auto", []keyHelp{
				{"(auto)", "refreshes every 15s while running"},
				{"(alert)", "status changes shown bottom-right"},
			}},
			{"Global", global},
		}
	case viewJobLog:
		return []helpSection{
			{"Job log", []keyHelp{
				{"↑/↓", "scroll"},
				{"g / G", "top / bottom"},
				{"t", "toggle tail"},
				{"esc / ⌫", "back to " + vocab.Pipeline},
			}},
			{"Global", global},
		}
	default: // list
		return []helpSection{
			{changeTitle + " list", []keyHelp{
				{"enter", "open " + vocab.Change},
				{"←/→ or tab", "switch scope"},
				{"/", "filter"},
				{"↑/↓", "move (auto-loads more)"},
				{"pgup / pgdown", "half-page in Reviews / Authored"},
			}},
			{"Actions on selected " + vocab.ChangeAbbrev, append(changeActionKeys(vocab, caps),
				keyHelp{"d", "open diff"},
				keyHelp{"p", "open " + vocab.Pipeline},
			)},
			{"Legend — " + vocab.Pipeline + " (1st glyph)", []keyHelp{
				{statusGlyph(forge.StatusSuccess), "passed"},
				{statusGlyph(forge.StatusFailed), "failed"},
				{statusGlyph(forge.StatusRunning), "running"},
				{statusGlyph(forge.StatusPending), "pending"},
				{statusGlyph(forge.StatusCanceled), "canceled / skipped"},
				{statusGlyph(forge.StatusManual), "manual (waiting on a human)"},
				{statusGlyph(forge.StatusNone), "no " + vocab.Pipeline},
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
				{mergeStateTag(forge.MergeStateNeedsUpdate, vocab), "behind its target branch"},
				{mergeStateTag(forge.MergeStateBlocked, vocab), "blocked by a rule or another " + vocab.ChangeAbbrev},
				{mergeStateTag(forge.MergeStateThreadsUnresolved, vocab), "unresolved " + vocab.Threads},
				{mergeStateTag(forge.MergeStateChangesRequested, vocab), "a reviewer requested changes"},
			}},
			{"Global", global},
		}
	}
}

// renderHelpOverlay produces a centered help panel for the active view.
func renderHelpOverlay(v view, vocab forge.Vocabulary, caps forge.Capabilities, width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("glx — keybindings"))
	b.WriteString("\n\n")

	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	for _, sec := range helpSections(v, vocab, caps) {
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

// title upper-cases the first rune of a vocabulary noun for use as a heading.
func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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

// Package tui implements the interactive Bubble Tea front-end for glx.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/browser"
	"github.com/hamkens/glx/internal/gitlab"
	"github.com/hamkens/glx/internal/watch"
)

// watchInterval is how often watched pipelines are polled in the background.
const watchInterval = 15 * time.Second

// browserOpenedMsg reports the result of an "open in browser" action.
type browserOpenedMsg struct {
	url string
	err error
}

// view identifies which screen is active.
type view int

const (
	viewList view = iota
	viewDetail
	viewDiff
	viewPipeline
	viewJobLog
	viewWatch
)

// rootModel routes between views. enter opens the detail view for the
// selected MR; d opens the diff; esc/backspace steps back one level.
type rootModel struct {
	client      *gitlab.Client
	view        view
	watchReturn view // view to return to when leaving the watch screen
	pipeReturn  view // view to return to when leaving the pipeline screen
	mrList      mrListModel
	detail      detailModel
	diff        diffModel
	pipeline    pipelineModel
	jobLog      jobLogModel
	watchView   watchModel
	showHelp    bool
	notice      string // transient root-level status line (e.g. browser result)

	watch    *watch.Registry
	watching bool // a background watch tick is scheduled
	alert    string

	width  int
	height int
}

// inputActive reports whether a text input is currently capturing keystrokes
// in the active view, so global single-key shortcuts (like "?") shouldn't fire.
func (m rootModel) inputActive() bool {
	switch m.view {
	case viewList:
		return m.mrList.busy()
	case viewDetail:
		return m.detail.mode == modeComment
	case viewDiff:
		return m.diff.mode == diffModeComment
	default:
		return false
	}
}

// currentURL returns the GitLab web URL for whatever is focused in the active
// view, or "" if nothing sensible is available.
func (m rootModel) currentURL() string {
	switch m.view {
	case viewList:
		if mr, ok := m.mrList.selected(); ok {
			return mr.WebURL
		}
	case viewDetail:
		if m.detail.detail != nil {
			return m.detail.detail.WebURL
		}
	case viewDiff:
		// No diff-specific URL stored; link to the MR's diffs tab.
		return fmt.Sprintf("https://%s/%s/-/merge_requests/%s/diffs",
			m.client.Host(), m.diff.projectPath, m.diff.iid)
	case viewPipeline:
		if m.pipeline.pipe != nil {
			return m.pipeline.pipe.WebURL
		}
	case viewJobLog:
		return m.jobLog.job.WebURL
	}
	return ""
}

// focusedPipeline resolves the pipeline associated with the current view's
// focus (project path, pipeline id, owning MR iid). ok is false when there is
// no pipeline to act on.
func (m rootModel) focusedPipeline() (path string, pid int, mrIID string, ok bool) {
	switch m.view {
	case viewList:
		if mr, sel := m.mrList.selected(); sel && mr.PipelineID > 0 {
			return mr.ProjectPath, mr.PipelineID, mr.IID, true
		}
	case viewDetail:
		if d := m.detail.detail; d != nil && d.PipelineID > 0 {
			return m.detail.projectPath, d.PipelineID, m.detail.iid, true
		}
	case viewPipeline:
		if m.pipeline.pipe != nil {
			return m.pipeline.projectPath, m.pipeline.pipe.ID, m.pipeline.mrIID, true
		}
	case viewWatch:
		if e, sel := m.watchView.selected(); sel {
			return e.ProjectPath, e.PipelineID, e.MRIID, true
		}
	}
	return "", 0, "", false
}

// openCurrentCmd opens the active view's URL in the browser.
func (m rootModel) openCurrentCmd() tea.Cmd {
	url := m.currentURL()
	return func() tea.Msg {
		if url == "" {
			return browserOpenedMsg{err: fmt.Errorf("nothing to open here")}
		}
		return browserOpenedMsg{url: url, err: browser.Open(url)}
	}
}

// --- background watch poller ---

// ringBellMsg requests an audible terminal bell.
type ringBellMsg struct{}

// ringBell writes the BEL character directly to the terminal. The byte is
// non-printing, so it doesn't disturb the alt-screen rendering.
func ringBell() {
	fmt.Fprint(os.Stderr, "\a")
}

// formatWatchAlert renders a watch.Change as a discreet, colored alert line.
func formatWatchAlert(ch watch.Change) string {
	proj := shortProject(ch.Entry.ProjectPath)
	id := fmt.Sprintf("#%d", ch.Entry.PipelineID)
	switch ch.Kind {
	case watch.PipelineFailed:
		return errStyle.Render(fmt.Sprintf("✘ %s %s failed", proj, id))
	case watch.PipelineDone:
		if ch.NewStatus == "canceled" {
			return helpStyle.Render(fmt.Sprintf("○ %s %s canceled", proj, id))
		}
		return lipgloss.NewStyle().Foreground(colorGreen).Render(fmt.Sprintf("✓ %s %s passed", proj, id))
	case watch.JobFailed:
		return errStyle.Render(fmt.Sprintf("✘ %s %s: %s failed", proj, id, ch.JobName))
	case watch.JobFinished:
		return lipgloss.NewStyle().Foreground(colorGreen).Render(fmt.Sprintf("✓ %s %s: %s done", proj, id, ch.JobName))
	default:
		return ""
	}
}

// shortProject keeps the last two path segments of a project path for compact
// display (e.g. "group/sub/repo" -> "sub/repo").
func shortProject(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// watchTickMsg fires on the watch interval to refresh active watched pipelines.
type watchTickMsg struct{}

// watchResultMsg carries the fetched pipelines from one poll round.
type watchResultMsg struct {
	pipelines []*gitlab.Pipeline
	paths     []string // parallel to pipelines: project path per result
}

// watchTickCmd schedules the next background poll.
func watchTickCmd() tea.Cmd {
	return tea.Tick(watchInterval, func(time.Time) tea.Msg { return watchTickMsg{} })
}

// pollWatchedCmd fetches all currently-active watched pipelines (sequentially,
// to stay gentle on the API) and returns them for diffing.
func (m rootModel) pollWatchedCmd() tea.Cmd {
	client := m.client
	targets := m.watch.ActiveTargets()
	return func() tea.Msg {
		var res watchResultMsg
		for _, t := range targets {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			p, err := client.PipelineWithJobs(gitlab.WithForceRefresh(ctx), t.ProjectPath, t.PipelineID)
			cancel()
			if err != nil || p == nil {
				continue
			}
			res.pipelines = append(res.pipelines, p)
			res.paths = append(res.paths, t.ProjectPath)
		}
		return res
	}
}

// startWatchingCmd begins the poll loop if it isn't already running and there
// is something active to watch.
func (m *rootModel) startWatchingCmd() tea.Cmd {
	if m.watching || !m.watch.HasActive() {
		return nil
	}
	m.watching = true
	return watchTickCmd()
}

// New builds the root Bubble Tea model.
func New(client *gitlab.Client) tea.Model {
	return rootModel{
		client: client,
		view:   viewList,
		mrList: newMRListModel(client),
		watch:  watch.New(),
	}
}

func (m rootModel) Init() tea.Cmd {
	return m.mrList.Init()
}

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Track size so views opened later inherit it.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
	}

	// ctrl+d quits from anywhere (including text inputs).
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "ctrl+d" {
		return m, tea.Quit
	}

	if bo, ok := msg.(browserOpenedMsg); ok {
		if bo.err != nil {
			m.notice = errStyle.Render("✘ open: " + bo.err.Error())
		} else {
			m.notice = helpStyle.Render("↗ opened " + bo.url)
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case watchTickMsg:
		m.watching = false
		if m.watch.HasActive() {
			return m, m.pollWatchedCmd()
		}
		return m, nil

	case watchResultMsg:
		var bell tea.Cmd
		for i, p := range msg.pipelines {
			if ch := m.watch.Update(msg.paths[i], p); ch.Kind != watch.ChangeNone {
				m.alert = formatWatchAlert(ch)
				if ch.Kind == watch.PipelineFailed || ch.Kind == watch.PipelineDone || ch.Kind == watch.JobFailed {
					bell = func() tea.Msg { return ringBellMsg{} }
				}
			}
		}
		// If the watch view is showing, refresh its rows from the registry.
		if m.view == viewWatch {
			m.watchView.reload()
		}
		// Reschedule if anything is still active.
		var cmds []tea.Cmd
		if c := m.startWatchingCmd(); c != nil {
			cmds = append(cmds, c)
		}
		if bell != nil {
			cmds = append(cmds, bell)
		}
		return m, tea.Batch(cmds...)

	case ringBellMsg:
		ringBell()
		return m, nil
	}

	// Any keypress clears lingering root-level notices/alerts. The w/W handlers
	// below re-set the alert after this, so their feedback still shows.
	if _, ok := msg.(tea.KeyMsg); ok {
		m.notice = ""
		m.alert = ""
	}

	// Help overlay: toggle with "?" (when no text input is active); while it's
	// open, "?"/esc close it and all other keys are swallowed.
	if key, ok := msg.(tea.KeyMsg); ok {
		if m.showHelp {
			switch key.String() {
			case "?", "esc", "q":
				m.showHelp = false
			case "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		if key.String() == "?" && !m.inputActive() {
			m.showHelp = true
			return m, nil
		}
		// Open the focused item in a browser ("o"), unless typing into an input.
		if key.String() == "o" && !m.inputActive() {
			return m, m.openCurrentCmd()
		}
		if !m.inputActive() {
			switch key.String() {
			case "w":
				// Toggle watch on the focused pipeline.
				if path, pid, mrIID, ok := m.focusedPipeline(); ok {
					if m.watch.Toggle(path, pid, mrIID) {
						m.alert = helpStyle.Render(fmt.Sprintf("👁 watching #%d", pid))
						return m, m.startWatchingCmd()
					}
					m.alert = helpStyle.Render(fmt.Sprintf("stopped watching #%d", pid))
					return m, nil
				}
				m.alert = helpStyle.Render("no pipeline here to watch")
				return m, nil
			case "W":
				// Open the watch list view.
				if m.view != viewWatch {
					m.watchReturn = m.view
					m.watchView = newWatchModel(m.client, m.watch)
					m.view = viewWatch
					var szCmd tea.Cmd
					m.watchView, szCmd = m.watchView.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
					return m, szCmd
				}
			}
		}
	}

	switch m.view {
	case viewDetail:
		return m.updateDetail(msg)
	case viewDiff:
		return m.updateDiff(msg)
	case viewPipeline:
		return m.updatePipeline(msg)
	case viewJobLog:
		return m.updateJobLog(msg)
	case viewWatch:
		return m.updateWatch(msg)
	default:
		return m.updateList(msg)
	}
}

func (m rootModel) updateWatch(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace", "q":
			m.view = m.watchReturn
			return m, nil
		case "enter":
			// Open the selected watched pipeline in the full pipeline view.
			if e, sel := m.watchView.selected(); sel {
				m.pipeReturn = viewWatch
				m.pipeline = newPipelineModel(m.client, e.ProjectPath, e.PipelineID, e.MRIID)
				m.view = viewPipeline
				var szCmd tea.Cmd
				m.pipeline, szCmd = m.pipeline.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				return m, tea.Batch(m.pipeline.Init(), szCmd)
			}
		}
	}

	var cmd tea.Cmd
	m.watchView, cmd = m.watchView.Update(msg)
	return m, cmd
}

func (m rootModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	// When the list is capturing input (filter) or awaiting a y/N confirm,
	// let it own all keys except the hard interrupt.
	if key, ok := msg.(tea.KeyMsg); ok && !m.mrList.busy() {
		switch key.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			if mr, ok := m.mrList.selected(); ok {
				m.detail = newDetailModel(m.client, mr.ProjectPath, mr.IID)
				m.view = viewDetail
				var szCmd tea.Cmd
				m.detail, szCmd = m.detail.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				return m, tea.Batch(m.detail.Init(), szCmd)
			}
		case "d":
			if mr, ok := m.mrList.selected(); ok {
				m.diff = newDiffModel(m.client, mr.ProjectPath, mr.IID)
				m.view = viewDiff
				var szCmd tea.Cmd
				m.diff, szCmd = m.diff.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				return m, tea.Batch(m.diff.Init(), szCmd)
			}
		case "p":
			if mr, ok := m.mrList.selected(); ok && mr.PipelineID > 0 {
				m.pipeReturn = viewList
				m.pipeline = newPipelineModel(m.client, mr.ProjectPath, mr.PipelineID, mr.IID)
				m.view = viewPipeline
				var szCmd tea.Cmd
				m.pipeline, szCmd = m.pipeline.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				return m, tea.Batch(m.pipeline.Init(), szCmd)
			}
		}
	} else if key, ok := msg.(tea.KeyMsg); ok && key.String() == "ctrl+c" {
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.mrList, cmd = m.mrList.Update(msg)
	return m, cmd
}

func (m rootModel) updateDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace":
			// Only leave the detail view when not composing a comment.
			if m.detail.mode == modeNone {
				m.view = viewList
				return m, nil
			}
		case "q":
			if m.detail.mode == modeNone {
				return m, tea.Quit
			}
		case "d":
			// Open the diff viewer for this MR.
			if m.detail.mode == modeNone {
				m.diff = newDiffModel(m.client, m.detail.projectPath, m.detail.iid)
				m.view = viewDiff
				var szCmd tea.Cmd
				m.diff, szCmd = m.diff.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				return m, tea.Batch(m.diff.Init(), szCmd)
			}
		case "p":
			// Open the pipeline view, if this MR has a head pipeline.
			if m.detail.mode == modeNone && m.detail.detail != nil && m.detail.detail.PipelineID > 0 {
				m.pipeReturn = viewDetail
				m.pipeline = newPipelineModel(m.client, m.detail.projectPath, m.detail.detail.PipelineID, m.detail.iid)
				m.view = viewPipeline
				var szCmd tea.Cmd
				m.pipeline, szCmd = m.pipeline.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				return m, tea.Batch(m.pipeline.Init(), szCmd)
			}
		}
	}

	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return m, cmd
}

func (m rootModel) updatePipeline(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace":
			// Return to whichever view opened the pipeline (list, detail, or
			// watch). Default to detail if somehow unset.
			if m.pipeReturn == viewList || m.pipeReturn == viewWatch {
				m.view = m.pipeReturn
			} else {
				m.view = viewDetail
			}
			return m, nil
		case "q":
			return m, tea.Quit
		case "enter":
			if job, ok := m.pipeline.selectedJob(); ok {
				m.jobLog = newJobLogModel(m.client, m.pipeline.projectPath, job)
				m.view = viewJobLog
				var szCmd tea.Cmd
				m.jobLog, szCmd = m.jobLog.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				return m, tea.Batch(m.jobLog.Init(), szCmd)
			}
		}
	}

	var cmd tea.Cmd
	m.pipeline, cmd = m.pipeline.Update(msg)
	return m, cmd
}

func (m rootModel) updateJobLog(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace":
			m.view = viewPipeline
			return m, nil
		case "q":
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.jobLog, cmd = m.jobLog.Update(msg)
	return m, cmd
}

func (m rootModel) updateDiff(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace":
			if m.diff.mode == diffModeNone {
				m.view = viewDetail
				return m, nil
			}
		case "q":
			if m.diff.mode == diffModeNone {
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.diff, cmd = m.diff.Update(msg)
	return m, cmd
}

func (m rootModel) View() string {
	if m.showHelp {
		return renderHelpOverlay(m.view, m.width, m.height)
	}
	var body string
	switch m.view {
	case viewDetail:
		body = m.detail.View()
	case viewDiff:
		body = m.diff.View()
	case viewPipeline:
		body = m.pipeline.View()
	case viewJobLog:
		body = m.jobLog.View()
	case viewWatch:
		body = m.watchView.View()
	default:
		body = m.mrList.View()
	}

	// Overlay the transient notice on the bottom line (left), and the watch
	// alert pinned bottom-right, keeping total height constant.
	if m.notice != "" || m.alert != "" {
		lines := strings.Split(body, "\n")
		last := len(lines) - 1
		left := m.notice
		if left == "" {
			left = lines[last]
		}
		merged := left
		if m.alert != "" {
			gap := m.width - lipgloss.Width(left) - lipgloss.Width(m.alert) - 1
			if gap < 1 {
				gap = 1
			}
			merged = left + strings.Repeat(" ", gap) + m.alert
		}
		lines[last] = truncateToWidth(merged, max(m.width, 1))
		body = strings.Join(lines, "\n")
	}
	return body
}

// Run starts the full-screen Bubble Tea program.
func Run(client *gitlab.Client) error {
	p := tea.NewProgram(New(client), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

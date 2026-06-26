package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/gitlab"
)

// --- messages ---

type jobTraceMsg struct {
	jobID int
	trace string
	err   error
}

// jobLogModel shows a single job's trace in a scrollable viewport. GitLab's
// trace already carries ANSI color codes, which the terminal renders directly.
type jobLogModel struct {
	client      *gitlab.Client
	projectPath string
	job         gitlab.Job

	vp      viewport.Model
	spinner spinner.Model
	loading bool
	err     error
	tail    bool // stick to bottom on refresh (useful while running)
	ready   bool

	width  int
	height int
}

func newJobLogModel(client *gitlab.Client, projectPath string, job gitlab.Job) jobLogModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return jobLogModel{
		client:      client,
		projectPath: projectPath,
		job:         job,
		spinner:     sp,
		loading:     true,
		tail:        job.Status == "running",
	}
}

func (m jobLogModel) fetchCmd() tea.Cmd {
	client, path, jobID := m.client, m.projectPath, m.job.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		tr, err := client.JobTrace(ctx, path, jobID)
		return jobTraceMsg{jobID: jobID, trace: tr, err: err}
	}
}

func (m jobLogModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchCmd())
}

func (m *jobLogModel) layout() {
	bodyH := m.height - 3 // header(2) + footer(1)
	if bodyH < 3 {
		bodyH = 3
	}
	m.vp = viewport.New(m.width, bodyH)
	m.ready = true
}

func (m jobLogModel) Update(msg tea.Msg) (jobLogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case jobTraceMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		if !m.ready {
			m.layout()
		}
		trace := msg.trace
		if strings.TrimSpace(trace) == "" {
			trace = "(no log output yet)"
		}
		m.vp.SetContent(trace)
		if m.tail {
			m.vp.GotoBottom()
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch msg.String() {
		case "r":
			m.loading = true
			return m, tea.Batch(m.spinner.Tick, m.fetchCmd())
		case "G":
			m.vp.GotoBottom()
			return m, nil
		case "g":
			m.vp.GotoTop()
			return m, nil
		case "t":
			m.tail = !m.tail
			if m.tail {
				m.vp.GotoBottom()
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m jobLogModel) View() string {
	if m.loading && m.vp.TotalLineCount() == 0 {
		return fmt.Sprintf("\n  %s loading log for %s…", m.spinner.View(), m.job.Name)
	}
	if m.err != nil {
		return "\n  " + errStyle.Render("error: "+m.err.Error()) + "\n  " + helpStyle.Render("esc back")
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.vp.View(), m.footer())
}

func (m jobLogModel) header() string {
	title := titleStyle.Render(m.job.Name) + "  " + jobGlyph(m.job.Status) + " " + helpStyle.Render(m.job.Status)
	scroll := helpStyle.Render(fmt.Sprintf("%3.0f%%", m.vp.ScrollPercent()*100))
	tail := ""
	if m.tail {
		tail = lipgloss.NewStyle().Foreground(colorGreen).Render("  tail")
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, helpStyle.Render("stage "+m.job.Stage)+"   "+scroll+tail)
}

func (m jobLogModel) footer() string {
	return helpStyle.Render("  ↑/↓ scroll · g/G top/bottom · t tail · r refresh · esc/⌫ back · q quit")
}

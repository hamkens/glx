package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hamkens/glx/internal/forge"
)

// --- messages ---

type pipelineLoadedMsg struct{ pipeline *forge.Pipeline }
type pipelineErrMsg struct{ err error }
type jobActionMsg struct {
	verb string
	err  error
}

// pipelinePollMsg is the auto-refresh tick; id guards against stale ticks from
// a previously-viewed pipeline.
type pipelinePollMsg struct{ id int64 }

// pollInterval is how often an active pipeline is auto-refreshed.
const pollInterval = 15 * time.Second

// pipelineRow is a flattened display row: either a stage header or a job.
type pipelineRow struct {
	isStage bool
	stage   string
	job     forge.Job
}

// pipelineModel shows a pipeline's jobs grouped by stage with a cursor.
type pipelineModel struct {
	client     forge.Forge
	vocab      forge.Vocabulary
	caps       forge.Capabilities
	repo       string
	pipelineID int64
	changeID   string // owning change id, for display/watch (may be empty)

	spinner spinner.Model
	pipe    *forge.Pipeline
	rows    []pipelineRow
	cur     int // index into rows (job rows are selectable)
	scroll  int
	loading bool
	err     error
	flash   string

	// auto-refresh + change alert
	polling  bool   // a poll tick is scheduled
	alert    string // discreet bottom-right change notice
	alertSeq int    // increments per alert, to expire stale clears

	width  int
	height int
}

func newPipelineModel(client forge.Forge, repo string, pipelineID int64, changeID string) pipelineModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return pipelineModel{
		client:     client,
		vocab:      vocabForRepo(client, repo),
		caps:       capsForRepo(client, repo),
		repo:       repo,
		pipelineID: pipelineID,
		changeID:   changeID,
		spinner:    sp,
		loading:    true,
	}
}

func (m pipelineModel) fetchCmd(force bool) tea.Cmd {
	client, repo, id := m.client, m.repo, m.pipelineID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if force {
			ctx = forge.WithForceRefresh(ctx)
		}
		p, err := client.PipelineWithJobs(ctx, repo, id)
		if err != nil {
			return pipelineErrMsg{err}
		}
		return pipelineLoadedMsg{p}
	}
}

func (m pipelineModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchCmd(false))
}

// isActive reports whether the pipeline (or any job) is still progressing, and
// so is worth auto-refreshing. Manual jobs wait on a human, not on CI, so they
// don't keep the poll loop alive.
func (m pipelineModel) isActive() bool {
	if m.pipe == nil {
		return false
	}
	if m.pipe.Status.Active() {
		return true
	}
	for _, j := range m.pipe.Jobs {
		if j.Status.Active() {
			return true
		}
	}
	return false
}

// pollCmd schedules an auto-refresh tick after pollInterval.
func (m pipelineModel) pollCmd() tea.Cmd {
	id := m.pipelineID
	return tea.Tick(pollInterval, func(time.Time) tea.Msg {
		return pipelinePollMsg{id: id}
	})
}

// alertExpireMsg clears the change alert after a delay.
type alertExpireMsg struct{ seq int }

// alertExpireCmd hides the alert after a few seconds unless superseded.
func (m pipelineModel) alertExpireCmd(seq int) tea.Cmd {
	return tea.Tick(8*time.Second, func(time.Time) tea.Msg {
		return alertExpireMsg{seq: seq}
	})
}

// diffAlert compares two pipeline snapshots and returns a short change notice
// (and a new sequence number) describing what changed, or "" if nothing did.
func (m pipelineModel) diffAlert(prev, cur *forge.Pipeline) (string, int) {
	seq := m.alertSeq + 1

	// Pipeline-level transition takes priority.
	if prev.Status != cur.Status {
		switch cur.Status {
		case forge.StatusSuccess:
			return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + m.vocab.Pipeline + " passed"), seq
		case forge.StatusFailed:
			return errStyle.Render("✘ " + m.vocab.Pipeline + " failed"), seq
		case forge.StatusCanceled:
			return helpStyle.Render("○ " + m.vocab.Pipeline + " canceled"), seq
		default:
			return helpStyle.Render(m.vocab.Pipeline + " → " + cur.Status.String()), seq
		}
	}

	// Otherwise report job-level changes (favor failures, then newly finished).
	prevJobs := map[int64]forge.Status{}
	for _, j := range prev.Jobs {
		prevJobs[j.ID] = j.Status
	}
	var finished []string
	for _, j := range cur.Jobs {
		old, ok := prevJobs[j.ID]
		if !ok || old == j.Status {
			continue
		}
		if j.Status == forge.StatusFailed {
			return errStyle.Render("✘ " + j.Name + " failed"), seq
		}
		if !j.Status.Active() {
			finished = append(finished, j.Name)
		}
	}
	if len(finished) == 1 {
		return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + finished[0] + " finished"), seq
	}
	if len(finished) > 1 {
		return lipgloss.NewStyle().Foreground(colorGreen).
			Render(fmt.Sprintf("✓ %d jobs finished", len(finished))), seq
	}
	return "", m.alertSeq
}

// buildRows groups jobs by stage (preserving first-seen stage order) and
// flattens them into display rows with stage headers.
func (m *pipelineModel) buildRows() {
	m.rows = nil
	if m.pipe == nil {
		return
	}
	var stageOrder []string
	byStage := map[string][]forge.Job{}
	for _, j := range m.pipe.Jobs {
		if _, ok := byStage[j.Stage]; !ok {
			stageOrder = append(stageOrder, j.Stage)
		}
		byStage[j.Stage] = append(byStage[j.Stage], j)
	}
	for _, st := range stageOrder {
		m.rows = append(m.rows, pipelineRow{isStage: true, stage: st})
		jobs := byStage[st]
		sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
		for _, j := range jobs {
			m.rows = append(m.rows, pipelineRow{job: j})
		}
	}
	// Land the cursor on the first job row.
	m.cur = 0
	for i, r := range m.rows {
		if !r.isStage {
			m.cur = i
			break
		}
	}
}

func (m pipelineModel) selectedJob() (forge.Job, bool) {
	if m.cur >= 0 && m.cur < len(m.rows) && !m.rows[m.cur].isStage {
		return m.rows[m.cur].job, true
	}
	return forge.Job{}, false
}

func (m pipelineModel) Update(msg tea.Msg) (pipelineModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case pipelineLoadedMsg:
		m.loading = false
		m.err = nil
		prev := m.pipe
		m.pipe = msg.pipeline
		m.buildRows()

		// Detect status changes since the last load and surface a discreet
		// alert. Skip the very first load (prev == nil).
		var cmds []tea.Cmd
		if prev != nil {
			if note, seq := m.diffAlert(prev, msg.pipeline); note != "" {
				m.alert = note
				m.alertSeq = seq
				cmds = append(cmds, m.alertExpireCmd(seq))
			}
		}

		// Keep polling while anything is still in progress; stop once settled.
		if m.isActive() {
			if !m.polling {
				m.polling = true
				cmds = append(cmds, m.pollCmd(), m.spinner.Tick)
			}
		} else {
			m.polling = false
		}
		return m, tea.Batch(cmds...)

	case pipelinePollMsg:
		// Ignore ticks from a stale pipeline or after polling stopped.
		if msg.id != m.pipelineID || !m.polling {
			return m, nil
		}
		// Fetch fresh data (force-refresh to bypass cache) and chain the next
		// tick from the load handler.
		m.polling = false
		return m, m.fetchCmd(true)

	case alertExpireMsg:
		if msg.seq == m.alertSeq {
			m.alert = ""
		}
		return m, nil

	case pipelineErrMsg:
		m.loading = false
		m.err = msg.err
		// A transient fetch error shouldn't kill auto-refresh; retry on schedule.
		if m.isActive() && !m.polling {
			m.polling = true
			return m, m.pollCmd()
		}
		return m, nil

	case jobActionMsg:
		if msg.err != nil {
			m.flash = errStyle.Render("✘ " + msg.verb + " failed: " + msg.err.Error())
			return m, nil
		}
		m.flash = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + msg.verb)
		m.loading = true
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true))

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m pipelineModel) handleKey(msg tea.KeyMsg) (pipelineModel, tea.Cmd) {
	switch msg.String() {
	case "down", "j":
		m.moveCursor(1)
	case "up", "k":
		m.moveCursor(-1)
	case "r":
		m.loading = true
		m.flash = ""
		m.polling = false // fetchCmd's load handler will reschedule if active
		return m, tea.Batch(m.spinner.Tick, m.fetchCmd(true))
	case "R":
		if j, ok := m.selectedJob(); ok {
			m.flash = "retrying…"
			return m, m.jobActionCmd("retried", j.ID)
		}
	case "x":
		if j, ok := m.selectedJob(); ok {
			m.flash = "canceling…"
			return m, m.jobActionCmd("canceled", j.ID)
		}
	}
	return m, nil
}

func (m pipelineModel) jobActionCmd(verb string, jobID int64) tea.Cmd {
	client, repo := m.client, m.repo
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		switch verb {
		case "retried":
			err = client.RetryJob(ctx, repo, jobID)
		case "canceled":
			err = client.CancelJob(ctx, repo, jobID)
		}
		return jobActionMsg{verb: verb, err: err}
	}
}

// moveCursor advances to the next/previous selectable (job) row.
func (m *pipelineModel) moveCursor(delta int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.cur
	for {
		i += delta
		if i < 0 || i >= len(m.rows) {
			return // no selectable row in that direction; keep current
		}
		if !m.rows[i].isStage {
			m.cur = i
			break
		}
	}
	h := m.paneHeight()
	if m.cur < m.scroll {
		m.scroll = m.cur
	}
	if m.cur >= m.scroll+h {
		m.scroll = m.cur - h + 1
	}
}

func (m pipelineModel) paneHeight() int {
	h := m.height - 4 // header(2) + footer(1) + margin
	if h < 3 {
		h = 3
	}
	return h
}

func (m pipelineModel) View() string {
	if m.loading && m.pipe == nil {
		return fmt.Sprintf("\n  %s loading %s…", m.spinner.View(), m.vocab.Pipeline)
	}
	if m.err != nil {
		return "\n  " + errStyle.Render("error: "+m.err.Error()) + "\n  " + helpStyle.Render("esc back")
	}
	if m.pipe == nil {
		return "\n  " + helpStyle.Render("no "+m.vocab.Pipeline+" for this "+m.vocab.Change) + "\n  " + helpStyle.Render("esc back")
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.body(), m.footerWithAlert())
}

func (m pipelineModel) header() string {
	p := m.pipe
	heading := strings.ToUpper(m.vocab.Pipeline[:1]) + m.vocab.Pipeline[1:]
	title := titleStyle.Render(fmt.Sprintf("%s #%d", heading, p.ID)) +
		"  " + statusGlyph(p.Status) + " " + helpStyle.Render(p.Status.String())
	if m.polling {
		title += "  " + helpStyle.Render(m.spinner.View()+" auto-refresh")
	}
	sub := helpStyle.Render(fmt.Sprintf("%s @ %.8s", p.Ref, p.SHA))
	if m.flash != "" {
		sub += "   " + m.flash
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, sub)
}

// footerWithAlert renders the help footer with a discreet, right-aligned change
// alert pinned to the bottom-right corner.
func (m pipelineModel) footerWithAlert() string {
	left := m.footer()
	if m.alert == "" {
		return left
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(m.alert) - 1
	if gap < 1 {
		// Not enough room beside the footer; drop the alert onto its own line.
		return left + "\n" + lipgloss.PlaceHorizontal(max(m.width, 1), lipgloss.Right, m.alert)
	}
	return left + strings.Repeat(" ", gap) + m.alert
}

func (m pipelineModel) body() string {
	h := m.paneHeight()
	if len(m.rows) == 0 {
		return "\n  " + helpStyle.Render("no jobs")
	}
	var b strings.Builder
	end := m.scroll + h
	if end > len(m.rows) {
		end = len(m.rows)
	}
	for i := m.scroll; i < end; i++ {
		r := m.rows[i]
		if r.isStage {
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render(strings.ToUpper(r.stage)))
			b.WriteByte('\n')
			continue
		}
		cursor := "  "
		if i == m.cur {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}
		j := r.job
		dur := ""
		if j.Duration > 0 {
			dur = helpStyle.Render(fmt.Sprintf("  %s", fmtDuration(j.Duration)))
		}
		af := ""
		if j.AllowFailure {
			af = helpStyle.Render("  (allow_fail)")
		}
		line := fmt.Sprintf("%s  %s %s%s%s", cursor, statusGlyph(j.Status), j.Name, dur, af)
		b.WriteString(truncateToWidth(line, m.width))
		b.WriteByte('\n')
	}
	return b.String()
}

func (m pipelineModel) footer() string {
	return helpStyle.Render("  enter log · R retry · x cancel · ↑/↓ job · r refresh · esc/⌫ back · q quit")
}

func fmtDuration(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

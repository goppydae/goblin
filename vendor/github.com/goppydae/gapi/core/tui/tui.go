package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// AgentControl defines the interface for interacting with agents.
type AgentControl interface {
	FetchStatus(context.Context) ([]AgentStatus, error)
	Lifecycle(ctx context.Context, id, action string) (bool, error)
	GetLogs(ctx context.Context, id string) (<-chan string, error)
}

type ViewMode int

const (
	ViewOverview ViewMode = iota // Tab 0: Local/Cluster Agents
	ViewGlobal                   // Tab 1: Global Agents Spec
	ViewLogs                     // Tab 2: Unified log stream
	ViewDetail                   // Detail view for specific agent
)

type LogFilter int

const (
	LogFilterAll LogFilter = iota
	LogFilterAgents
	LogFilterCluster
)

type AgentStatus struct {
	ID     string
	State  string
	Type   string
	CPU    string
	Memory string
	Uptime time.Duration
}

type Model struct {
	view        ViewMode
	currentTab  int // 0 = Overview, 1 = Logs
	agents      []AgentStatus
	selectedIdx int
	logBuffer   []LogEntry // Ring buffer (max 1000) - defined in logs.go
	logFilter   LogFilter
	logViewer   LogViewer
	filter      Filter
	width       int
	height      int
	err         error
	quitting    bool
	ctrl        AgentControl
	logCancel   context.CancelFunc
}

func NewModel(ctrl AgentControl) Model {
	return Model{
		view:        ViewOverview,
		currentTab:  0,
		agents:      []AgentStatus{},
		selectedIdx: 0,
		logBuffer:   make([]LogEntry, 0, 1000),
		logFilter:   LogFilterAll,
		logViewer:   LogViewer{},
		filter:      NewFilter(),
		ctrl:        ctrl,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchAgentStatusCmd,
		tickEvery(time.Second),
	)
}

// Messages
type agentStatusMsg []AgentStatus
type tickMsg time.Time
type errMsg error
type logMsg string

func (m Model) fetchAgentStatusCmd() tea.Msg {
	// 5s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agents, err := m.ctrl.FetchStatus(ctx)
	if err != nil {
		return errMsg(err)
	}
	return agentStatusMsg(agents)
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Update log viewer size if in log view
		if m.view == ViewLogs {
			var cmd tea.Cmd
			m.logViewer, cmd = m.logViewer.Update(msg, m.agents, m.selectedIdx)
			return m, cmd
		}
		return m, nil

	case agentStatusMsg:
		m.agents = msg
		return m, nil

	case tickMsg:
		// Update log viewer if in log view
		if m.view == ViewLogs {
			var cmd tea.Cmd
			m.logViewer, cmd = m.logViewer.Update(msg, m.agents, m.selectedIdx)
			return m, tea.Batch(
				m.fetchAgentStatusCmd,
				tickEvery(time.Second),
				cmd,
			)
		}

		return m, tea.Batch(
			m.fetchAgentStatusCmd,
			tickEvery(time.Second),
		)

	case lifecycleActionMsg:
		if msg.success {
			// Refresh agent status immediately
			return m, m.fetchAgentStatusCmd
		} else if msg.err != nil {
			m.err = msg.err
		}
		return m, nil

	case logMsg:
		// Parse log message and add to viewer
		line := string(msg)

		// Parse format: "[CLUSTER] message" or "[AGENT] message"
		var source, level, message string
		if strings.HasPrefix(line, "[CLUSTER]") {
			source = "cluster"
			level = "INFO"
			message = strings.TrimPrefix(line, "[CLUSTER] ")
		} else if strings.HasPrefix(line, "[AGENT]") {
			source = "agent"
			level = "INFO"
			message = strings.TrimPrefix(line, "[AGENT] ")
		} else if strings.HasPrefix(line, "[ERROR]") {
			source = "cluster"
			level = "ERROR"
			message = strings.TrimPrefix(line, "[ERROR] ")
		} else {
			source = "cluster"
			level = "INFO"
			message = line
		}

		entry := LogEntry{
			Timestamp: time.Now(),
			Source:    source,
			Type:      "cluster",
			Level:     level,
			Message:   message,
		}

		m.logViewer.logs = append(m.logViewer.logs, entry)
		m.logViewer.ready = true

		// Update viewport content
		m.logViewer.viewport.SetContent(m.logViewer.renderLogs())

		return m, waitForLog(m.logViewer.sub)
	}

	return m, nil
}

func (m Model) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.view {
	case ViewOverview:
		return m.handleListKeys(msg)
	case ViewGlobal:
		return m.handleListKeys(msg)
	case ViewLogs:
		return m.handleLogsKeys(msg)
	case ViewDetail:
		return m.handleDetailKeys(msg)
	}
	return m, nil
}

func (m Model) handleListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// If filter is active, let it handle the input
	if m.filter.active {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "tab":
		// Switch between Overview, Global, and Logs tabs
		switch m.view {
		case ViewOverview:
			m.view = ViewGlobal
			m.currentTab = 1
		case ViewGlobal:
			m.view = ViewLogs
			m.currentTab = 2

			// Initialize log viewer for cluster logs
			m.logViewer = NewLogViewer("cluster", m.width, m.height)

			// Start streaming cluster logs
			ctx, cancel := context.WithCancel(context.Background())
			m.logCancel = cancel

			logsCh, err := m.ctrl.GetLogs(ctx, "cluster")
			if err != nil {
				m.err = err
				cancel()
				m.logCancel = nil
				return m, nil
			}
			m.logViewer.sub = logsCh
			return m, waitForLog(logsCh)

		case ViewLogs:
			m.view = ViewOverview
			m.currentTab = 0

			// Cancel log streaming
			if m.logCancel != nil {
				m.logCancel()
				m.logCancel = nil
			}
		}

	case "/":
		// Activate name search
		m.filter.Activate(FilterByName)

	case "f":
		// Cycle through state filters
		if m.filter.mode == FilterByState && m.filter.query == "running" {
			m.filter.SetQuery("stopped")
		} else if m.filter.mode == FilterByState && m.filter.query == "stopped" {
			m.filter.mode = FilterNone
			m.filter.SetQuery("")
		} else {
			m.filter.Activate(FilterByState)
			m.filter.SetQuery("running")
			m.filter.Deactivate()
		}

	case "t":
		// Cycle through type filters
		if m.filter.mode == FilterByType && m.filter.query == "service" {
			m.filter.SetQuery("timer")
		} else if m.filter.mode == FilterByType && m.filter.query == "timer" {
			m.filter.SetQuery("socket")
		} else if m.filter.mode == FilterByType && m.filter.query == "socket" {
			m.filter.mode = FilterNone
			m.filter.SetQuery("")
		} else {
			m.filter.Activate(FilterByType)
			m.filter.SetQuery("service")
			m.filter.Deactivate()
		}

	case "esc":
		// Clear filter
		m.filter.mode = FilterNone
		m.filter.SetQuery("")

	case "up", "k":
		if m.selectedIdx > 0 {
			m.selectedIdx--
		}

	case "down", "j":
		// Count filtered agents
		filteredCount := 0
		for _, agent := range m.agents {
			if m.filter.Matches(agent) {
				filteredCount++
			}
		}
		if m.selectedIdx < filteredCount-1 {
			m.selectedIdx++
		}

	case "enter":
		m.view = ViewDetail

	case "l":
		// Initialize log viewer for selected agent
		if m.selectedIdx < len(m.agents) {
			id := m.agents[m.selectedIdx].ID
			m.logViewer = NewLogViewer(id, m.width, m.height)
			m.view = ViewLogs

			// Start streaming
			ctx, cancel := context.WithCancel(context.Background())
			m.logCancel = cancel

			logsCh, err := m.ctrl.GetLogs(ctx, id)
			if err != nil {
				m.err = err
				cancel()
				m.logCancel = nil
				return m, nil
			}
			m.logViewer.sub = logsCh
			return m, waitForLog(logsCh)
		}
		m.view = ViewLogs

	case "s":
		// Start selected agent
		if m.selectedIdx < len(m.agents) {
			return m, sendLifecycleAction(m.agents[m.selectedIdx].ID, "start")
		}

	case "x":
		// Stop selected agent
		if m.selectedIdx < len(m.agents) {
			return m, sendLifecycleAction(m.agents[m.selectedIdx].ID, "stop")
		}

	case "r":
		// Reload selected agent
		if m.selectedIdx < len(m.agents) {
			return m, sendLifecycleAction(m.agents[m.selectedIdx].ID, "reload")
		}
	}

	return m, nil
}

func (m Model) handleLogsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "tab":
		// Switch back to Overview tab
		m.view = ViewOverview
		m.currentTab = 0

		// Cancel log streaming
		if m.logCancel != nil {
			m.logCancel()
			m.logCancel = nil
		}

	case "esc":
		m.view = ViewOverview

		// Cancel log streaming
		if m.logCancel != nil {
			m.logCancel()
			m.logCancel = nil
		}
	}

	return m, nil
}

func (m Model) handleDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		m.view = ViewOverview
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	switch m.view {
	case ViewOverview:
		return m.renderList()
	case ViewGlobal:
		return m.renderList()
	case ViewLogs:
		return m.logViewer.View()
	case ViewDetail:
		return m.renderDetail()
	default:
		return "Unknown view"
	}
}

func (m Model) renderList() string {
	var s string

	// Tab Headers
	tabStyle := lipgloss.NewStyle().
		Padding(0, 2).
		Bold(true)

	activeTabStyle := tabStyle.
		Foreground(lipgloss.Color("205")).
		Background(lipgloss.Color("63"))

	inactiveTabStyle := tabStyle.
		Foreground(lipgloss.Color("241"))

	overviewTab := "[Overview]"
	globalTab := "[Global]"
	logsTab := "[Logs]"

	switch m.view {
	case ViewOverview:
		s += activeTabStyle.Render(overviewTab) + inactiveTabStyle.Render(globalTab) + inactiveTabStyle.Render(logsTab)
	case ViewGlobal:
		s += inactiveTabStyle.Render(overviewTab) + activeTabStyle.Render(globalTab) + inactiveTabStyle.Render(logsTab)
	default:
		s += inactiveTabStyle.Render(overviewTab) + inactiveTabStyle.Render(globalTab) + activeTabStyle.Render(logsTab)
	}
	s += "\n\n"

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(0, 1)

	running := 0
	stopped := 0
	filtered := 0
	for _, a := range m.agents {
		if a.State == "RUNNING" || a.State == "running" {
			running++
		} else {
			stopped++
		}
		if m.filter.Matches(a) {
			filtered++
		}
	}

	header := fmt.Sprintf("Runtime Monitor - Agents: %d running, %d stopped", running, stopped)
	if m.filter.mode != FilterNone {
		header += fmt.Sprintf(" [%d/%d filtered]", filtered, len(m.agents))
	}
	s += headerStyle.Render(header) + "\n\n"

	// Filter status
	if filterStatus := m.filter.StatusLine(); filterStatus != "" {
		filterStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).
			Italic(true)
		s += filterStyle.Render(filterStatus) + "\n\n"
	}

	// Table header
	tableHeader := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("99"))

	s += tableHeader.Render(fmt.Sprintf("%-20s %-12s %-8s %-10s %-10s\n",
		"ID", "STATE", "TYPE", "CPU", "MEMORY"))

	// Agent rows (filtered)
	displayIdx := 0
	for _, agent := range m.agents {
		// Separation Logic:
		// ViewOverview shows only real instances (NOT global-spec)
		// ViewGlobal shows ONLY global-spec items
		if m.view == ViewOverview && agent.Type == "global-spec" {
			continue
		}
		if m.view == ViewGlobal && agent.Type != "global-spec" {
			continue
		}

		if !m.filter.Matches(agent) {
			continue
		}

		style := lipgloss.NewStyle()

		if displayIdx == m.selectedIdx {
			style = style.Background(lipgloss.Color("63")).Foreground(lipgloss.Color("230"))
		}

		stateColor := lipgloss.Color("10") // green
		if agent.State != "RUNNING" && agent.State != "running" {
			stateColor = lipgloss.Color("9") // red
		}

		row := fmt.Sprintf("%-20s %-12s %-8s %-10s %-10s",
			agent.ID,
			agent.State,
			agent.Type,
			agent.CPU,
			agent.Memory,
		)

		if displayIdx == m.selectedIdx {
			s += style.Render(row) + "\n"
		} else {
			stateStyle := lipgloss.NewStyle().Foreground(stateColor)
			s += fmt.Sprintf("%-20s %s %-8s %-10s %-10s\n",
				agent.ID,
				stateStyle.Render(fmt.Sprintf("%-12s", agent.State)),
				agent.Type,
				agent.CPU,
				agent.Memory,
			)
		}
		displayIdx++
	}

	// Footer
	s += "\n"
	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	s += footerStyle.Render("[arrows/jk] navigate  [/] search  [f] filter state  [t] filter type  [esc] clear  [s/x/r] control  [q] quit")

	return s
}

func (m Model) renderDetail() string {
	if m.selectedIdx >= len(m.agents) {
		return "No agent selected"
	}

	agent := m.agents[m.selectedIdx]

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205"))

	labelStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("99"))

	var s string
	s += headerStyle.Render(fmt.Sprintf("Agent: %s", agent.ID)) + "\n\n"

	s += labelStyle.Render("State:       ") + agent.State + "\n"
	s += labelStyle.Render("Type:        ") + agent.Type + "\n"
	s += labelStyle.Render("CPU:         ") + agent.CPU + "\n"
	s += labelStyle.Render("Memory:      ") + agent.Memory + "\n"

	if agent.Uptime > 0 {
		s += labelStyle.Render("Uptime:      ") + agent.Uptime.String() + "\n"
	}

	s += "\n"
	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	s += footerStyle.Render("[esc] back  [s] start  [x] stop  [r] reload")
	return s
}

func waitForLog(sub <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-sub
		if !ok {
			return nil
		}
		return logMsg(line)
	}
}

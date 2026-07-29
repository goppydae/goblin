package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type LogEntry struct {
	Timestamp time.Time
	Source    string // agent ID or "cluster"
	Type      string // "agent" or "cluster"
	Level     string // INFO, WARN, ERROR, DEBUG
	Message   string
}

type LogViewer struct {
	agentID  string
	logs     []LogEntry
	viewport viewport.Model
	ready    bool
	width    int
	height   int
	sub      <-chan string
}

func NewLogViewer(agentID string, width, height int) LogViewer {
	vp := viewport.New(width, height-4) // Reserve space for header and footer
	vp.Style = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63"))

	return LogViewer{
		agentID:  agentID,
		logs:     []LogEntry{},
		viewport: vp,
		ready:    true,
		width:    width,
		height:   height,
	}
}

func (l LogViewer) Update(msg tea.Msg, agents []AgentStatus, selectedIdx int) (LogViewer, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		l.width = msg.Width
		l.height = msg.Height
		l.viewport.Width = msg.Width
		l.viewport.Height = msg.Height - 4

	case logMsg:
		l.logs = append(l.logs, LogEntry{
			Timestamp: time.Now(),
			Source:    l.agentID,
			Type:      "agent",
			Level:     "INFO",
			Message:   string(msg),
		})

		// Keep last 100 entries
		if len(l.logs) > 100 {
			l.logs = l.logs[len(l.logs)-100:]
		}
		l.viewport.SetContent(l.renderLogs())
		l.viewport.GotoBottom()

		// Continue waiting
		return l, waitForLog(l.sub)
	}

	l.viewport, cmd = l.viewport.Update(msg)
	return l, cmd
}

func (l LogViewer) View() string {
	if !l.ready {
		return "Loading logs..."
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(0, 1)

	header := headerStyle.Render("Logs: " + l.agentID)

	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	footer := footerStyle.Render("[up/down] scroll  [ESC] back to list")

	return header + "\n" + l.viewport.View() + "\n" + footer
}

func (l LogViewer) renderLogs() string {
	if len(l.logs) == 0 {
		return "No logs yet... (simulated logs will appear as agent status updates)"
	}

	var content string
	for _, entry := range l.logs {
		levelColor := lipgloss.Color("15") // white
		switch entry.Level {
		case "ERROR":
			levelColor = lipgloss.Color("9") // red
		case "WARN":
			levelColor = lipgloss.Color("11") // yellow
		case "INFO":
			levelColor = lipgloss.Color("10") // green
		case "DEBUG":
			levelColor = lipgloss.Color("8") // gray
		}

		levelStyle := lipgloss.NewStyle().Foreground(levelColor).Bold(true)
		timeStr := entry.Timestamp.Format("15:04:05.000")

		content += timeStr + " " + levelStyle.Render(fmt.Sprintf("[%-5s]", entry.Level)) + " " + entry.Message + "\n"
	}

	return content
}

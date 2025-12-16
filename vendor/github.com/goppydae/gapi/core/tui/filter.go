package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type FilterMode int

const (
	FilterNone FilterMode = iota
	FilterByState
	FilterByType
	FilterByName
)

type Filter struct {
	mode   FilterMode
	query  string
	active bool
	input  string // Current input for search
}

func NewFilter() Filter {
	return Filter{
		mode:   FilterNone,
		active: false,
		query:  "",
		input:  "",
	}
}

func (f *Filter) Activate(mode FilterMode) {
	f.mode = mode
	f.active = true
	f.input = ""
}

func (f *Filter) Deactivate() {
	f.active = false
	f.input = ""
	if f.mode == FilterByName {
		f.mode = FilterNone
		f.query = ""
	}
}

func (f *Filter) SetQuery(query string) {
	f.query = query
}

func (f *Filter) Update(msg tea.Msg) (Filter, tea.Cmd) {
	if !f.active {
		return *f, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			f.query = f.input
			f.active = false
			return *f, nil

		case "esc":
			f.Deactivate()
			return *f, nil

		case "backspace":
			if len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
			}

		default:
			// Add character to input
			if len(msg.String()) == 1 {
				f.input += msg.String()
			}
		}
	}

	return *f, nil
}

func (f *Filter) Matches(agent AgentStatus) bool {
	if f.mode == FilterNone || f.query == "" {
		return true
	}

	switch f.mode {
	case FilterByState:
		return strings.EqualFold(agent.State, f.query)

	case FilterByType:
		return strings.EqualFold(agent.Type, f.query)

	case FilterByName:
		return strings.Contains(strings.ToLower(agent.ID), strings.ToLower(f.query))
	}

	return true
}

func (f *Filter) StatusLine() string {
	if f.mode == FilterNone {
		return ""
	}

	var modeStr string
	switch f.mode {
	case FilterByState:
		modeStr = "state"
	case FilterByType:
		modeStr = "type"
	case FilterByName:
		modeStr = "name"
	}

	if f.active {
		return "Filter (" + modeStr + "): " + f.input + "_"
	}

	if f.query != "" {
		return "Filter: " + modeStr + "=" + f.query
	}

	return ""
}

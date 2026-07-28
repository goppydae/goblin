package tui

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

type lifecycleActionMsg struct {
	agentID string
	action  string
	success bool
	err     error
}

func sendLifecycleAction(agentID, action string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load()
		if err != nil {
			return lifecycleActionMsg{agentID: agentID, action: action, success: false, err: err}
		}

		t, err := transport.NewClientFromConfig(cfg.Transport)
		if err != nil {
			return lifecycleActionMsg{agentID: agentID, action: action, success: false, err: err}
		}
		defer func() {
			if cerr := t.Close(); cerr != nil {
				slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to close transport", logattr.Module("tui"), logattr.Err(cerr))
			}
		}()

		bus := eventbus.NewEventBus(t)

		done := make(chan bool)
		errChan := make(chan error)

		if err := bus.SubscribePrefix("system", "", "agent/lifecycle", func(e eventbus.Event[*anypb.Any]) {
			var status protopkg.LifecycleTransition
			if err := e.Payload.UnmarshalTo(&status); err != nil {
				errChan <- err
				return
			}

			if status.AgentId == agentID {
				done <- true
			}
		}); err != nil {
			return lifecycleActionMsg{agentID: agentID, action: action, success: false, err: err}
		}

		req := &protopkg.LifecycleControl{
			AgentId: agentID,
			Action:  actionToEnum(action),
		}
		packed, _ := anypb.New(req)
		// Send control event
		if err := bus.Publish(eventbus.NewEvent("system", "", "agent/lifecycle.action", "gapictl-tui", packed, true)); err != nil {
			return lifecycleActionMsg{agentID: agentID, action: action, success: false, err: err}
		}

		select {
		case <-done:
			return lifecycleActionMsg{agentID: agentID, action: action, success: true}
		case err := <-errChan:
			return lifecycleActionMsg{agentID: agentID, action: action, success: false, err: err}
		case <-time.After(5 * time.Second):
			return lifecycleActionMsg{agentID: agentID, action: action, success: false, err: fmt.Errorf("timeout")}
		}
	}
}

package client

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/internal/statewatch"
	"github.com/goppydae/gapi/core/transport"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// Client provides a programmatic interface to interact with a running GAPI daemon.
type Client struct {
	bus *eventbus.EventBus[*anypb.Any]
}

// Result represents the outcome of a lifecycle action on a specific agent.
type Result struct {
	AgentID string
	Status  *protopkg.LifecycleStatus
	Err     error
}

// New creates a new Client using the provided configuration.
func New(cfg *config.Config) (*Client, error) {
	t, err := transport.NewClientFromConfig(cfg.Transport)
	if err != nil {
		return nil, fmt.Errorf("failed to init transport: %w", err)
	}
	bus := eventbus.NewEventBus[*anypb.Any](t)
	return &Client{bus: bus}, nil
}

// NewFromBus creates a new Client using an existing EventBus (for in-process use).
func NewFromBus(bus *eventbus.EventBus[*anypb.Any]) *Client {
	return &Client{bus: bus}
}

// Ping sends a ping to the daemon and waits for a pong.
func (c *Client) Ping(ctx context.Context) (string, error) {
	done := make(chan string, 1)
	errCh := make(chan error, 1)

	c.bus.SubscribeOnce("system", "", "pong", func(e eventbus.Event[*anypb.Any]) {
		var pong protopkg.PingStatus
		if err := e.Payload.UnmarshalTo(&pong); err != nil {
			errCh <- fmt.Errorf("failed to unmarshal pong: %w", err)
			return
		}
		done <- pong.Status
	})

	msg := &protopkg.PingStatus{Status: "ping"}
	payload, err := anypb.New(msg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal ping: %w", err)
	}

	if err := c.bus.Publish(eventbus.NewEvent("system", "", "ping", "client", payload, true)); err != nil {
		return "", fmt.Errorf("failed to publish ping: %w", err)
	}

	select {
	case status := <-done:
		return status, nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ReloadAgents triggers a reload of the agent registry on the daemon.
func (c *Client) ReloadAgents(ctx context.Context) error {
	evt := eventbus.NewEvent[*anypb.Any]("system", "", "agent.reload", "client", nil, true)
	if err := c.bus.Publish(evt); err != nil {
		return fmt.Errorf("failed to publish reload: %w", err)
	}
	return nil
}

// AgentStatus queries the daemon for the status of all agents.
func (c *Client) AgentStatus(ctx context.Context) ([]*protopkg.AgentStatus, error) {
	done := make(chan []*protopkg.AgentStatus, 1)
	errCh := make(chan error, 1)

	c.bus.SubscribeOnce("system", "", "agents.reply", func(e eventbus.Event[*anypb.Any]) {
		var res protopkg.AgentStatusResponse
		if err := e.Payload.UnmarshalTo(&res); err != nil {
			errCh <- fmt.Errorf("failed to unmarshal agent status: %w", err)
			return
		}
		done <- res.Agents
	})

	req := &protopkg.AgentStatusRequest{}
	packed, err := anypb.New(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal status request: %w", err)
	}

	if err := c.bus.Publish(eventbus.NewEvent("system", "", "agents/", "client", packed, true)); err != nil {
		return nil, fmt.Errorf("failed to publish status request: %w", err)
	}

	select {
	case agents := <-done:
		return agents, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// LifecycleOptions defines optional parameters for lifecycle actions.
type LifecycleOptions struct {
	Env           map[string]string
	RestartPolicy string
}

// Lifecycle sends a lifecycle action to a set of agents and waits for their transition.
func (c *Client) Lifecycle(ctx context.Context, agentIDs []string, action protopkg.LifecycleControl_Action) []Result {
	return c.LifecycleWithOpts(ctx, agentIDs, action, LifecycleOptions{})
}

// LifecycleWithOpts sends a lifecycle action with options.
func (c *Client) LifecycleWithOpts(ctx context.Context, agentIDs []string, action protopkg.LifecycleControl_Action, opts LifecycleOptions) []Result {
	results := make(chan Result, len(agentIDs))

	for _, id := range agentIDs {
		agentID := id // capture loop var
		go func() {
			// Publish control request
			req := &protopkg.LifecycleControl{
				AgentId:       agentID,
				Action:        action,
				Env:           opts.Env,
				RestartPolicy: opts.RestartPolicy,
			}
			packed, err := anypb.New(req)
			if err != nil {
				results <- Result{AgentID: agentID, Err: fmt.Errorf("marshal request: %w", err)}
				return
			}
			ev := eventbus.NewEvent("system", "", "agent/lifecycle.action", "client", packed, true)
			if err := c.bus.Publish(ev); err != nil {
				results <- Result{AgentID: agentID, Err: fmt.Errorf("publish control: %w", err)}
				return
			}

			// Await transition
			st, err := statewatch.WaitForPendingThenTerminal(
				ctx, c.bus, agentID, config.ClientPendingTimeout, config.ClientTerminalTimeout,
			)
			results <- Result{AgentID: agentID, Status: st, Err: err}
		}()
	}

	var finalResults []Result
	for i := 0; i < len(agentIDs); i++ {
		finalResults = append(finalResults, <-results)
	}
	return finalResults
}

// Start triggers the START action for the given agents.
func (c *Client) Start(ctx context.Context, agentIDs []string) []Result {
	return c.Lifecycle(ctx, agentIDs, protopkg.LifecycleControl_START)
}

// StartWithOpts triggers the START action with options.
func (c *Client) StartWithOpts(ctx context.Context, agentIDs []string, opts LifecycleOptions) []Result {
	return c.LifecycleWithOpts(ctx, agentIDs, protopkg.LifecycleControl_START, opts)
}

// Stop triggers the STOP action for the given agents.
func (c *Client) Stop(ctx context.Context, agentIDs []string) []Result {
	return c.Lifecycle(ctx, agentIDs, protopkg.LifecycleControl_STOP)
}

// GetLogs subscribes to the logs for a specific agent and returns a channel of log lines.
// The subscription is automatically cleaned up when the context is cancelled.
func (c *Client) GetLogs(ctx context.Context, agentID string) (<-chan string, error) {
	ch := make(chan string, 100)

	err := c.bus.SubscribePrefixWithContext(ctx, "system", "", "logs", func(e eventbus.Event[*anypb.Any]) {
		var logMsg protopkg.LogMessage
		if err := e.Payload.UnmarshalTo(&logMsg); err != nil {
			return
		}

		// Filter by agent ID
		if logMsg.GetAgentId() != agentID {
			return
		}

		// Format log line
		ts := time.UnixMilli(logMsg.GetTimestamp()).UTC().Format(time.RFC3339Nano)
		msg := fmt.Sprintf("[%s] %s [%s]: %s",
			ts,
			logMsg.GetAgentId(),
			logMsg.GetLevel(),
			logMsg.GetMessage())

		select {
		case ch <- msg:
		case <-ctx.Done():
		default: // drop if full to avoid blocking bus
		}
	})

	if err != nil {
		return nil, err
	}

	return ch, nil
}

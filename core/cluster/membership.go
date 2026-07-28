package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/goppydae/goblin/internal/logattr"
	"github.com/hashicorp/memberlist"
	"github.com/hashicorp/serf/serf"
)

// Membership manages cluster membership using Serf
type Membership struct {
	serf      *serf.Serf
	eventCh   chan serf.Event
	handlerCh chan serf.Event
	nodeID    string
	bindAddr  string
	bindPort  int
	handler   func(serf.Event)
	handlerMu sync.RWMutex
}

// NewMembership creates a new Serf-based membership manager
func NewMembership(nodeID, bindAddr string, bindPort int, advertiseAddr string, advertisePort int, tags map[string]string, secretKey []byte, transport memberlist.Transport) (*Membership, error) {
	eventCh := make(chan serf.Event, 256)

	config := serf.DefaultConfig()
	config.NodeName = nodeID
	config.MemberlistConfig.BindAddr = bindAddr
	config.MemberlistConfig.BindPort = bindPort
	config.MemberlistConfig.AdvertiseAddr = advertiseAddr
	config.MemberlistConfig.AdvertisePort = advertisePort
	config.MemberlistConfig.SecretKey = secretKey
	config.MemberlistConfig.Transport = transport // Inject Custom Transport
	config.EventCh = eventCh
	config.Tags = tags

	// Disable logging for cleaner output
	config.LogOutput = nil
	config.MemberlistConfig.LogOutput = nil

	s, err := serf.Create(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create serf: %w", err)
	}

	m := &Membership{
		serf:      s,
		eventCh:   eventCh,
		handlerCh: make(chan serf.Event, 256),
		nodeID:    nodeID,
		bindAddr:  bindAddr,
		bindPort:  bindPort,
	}

	// Start event handler
	go m.handleEvents()
	go m.dispatchHandler()

	return m, nil
}

// Join joins the cluster by connecting to existing peers
func (m *Membership) Join(peers []string) error {
	if len(peers) == 0 {
		return nil // Bootstrap node
	}

	_, err := m.serf.Join(peers, true)
	if err != nil {
		return fmt.Errorf("failed to join cluster: %w", err)
	}

	return nil
}

// Leave gracefully leaves the cluster
func (m *Membership) Leave() error {
	return m.serf.Leave()
}

// Shutdown stops the membership manager
func (m *Membership) Shutdown() error {
	return m.serf.Shutdown()
}

// Members returns all cluster members
func (m *Membership) Members() []serf.Member {
	return m.serf.Members()
}

// LocalNode returns the local node information
func (m *Membership) LocalNode() serf.Member {
	return m.serf.LocalMember()
}

// UserEvent broadcasts a user event to the cluster
func (m *Membership) UserEvent(name string, payload []byte) error {
	return m.serf.UserEvent(name, payload, false)
}

// SetEventHandler sets a callback for Serf events
func (m *Membership) SetEventHandler(handler func(serf.Event)) {
	m.handlerMu.Lock()
	defer m.handlerMu.Unlock()
	m.handler = handler
}

// handleEvents processes Serf events
func (m *Membership) handleEvents() {
	for event := range m.eventCh {
		// Log events
		switch e := event.(type) {
		case serf.MemberEvent:
			m.handleMemberEvent(e)
		case serf.UserEvent:
			m.handleUserEvent(e)
		}

		// Hand the event to the dispatcher goroutine: a slow handler
		// must not stall this loop, which is the only consumer of
		// Serf's bounded event channel (R25).
		m.handlerCh <- event
	}
	close(m.handlerCh)
}

// dispatchHandler invokes the registered handler from its own goroutine
// so slow handlers back up the local buffer, not Serf's dispatch. A
// single worker (not a pool) preserves event ordering - a join/leave
// pair for one node must never be observed reordered.
func (m *Membership) dispatchHandler() {
	for event := range m.handlerCh {
		m.handlerMu.RLock()
		handler := m.handler
		m.handlerMu.RUnlock()
		if handler != nil {
			handler(event)
		}
	}
}

// handleMemberEvent processes member join/leave/fail events
func (m *Membership) handleMemberEvent(event serf.MemberEvent) {
	for _, member := range event.Members {
		switch event.EventType() {
		case serf.EventMemberJoin:
			slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "node joined", logattr.Member(member.Name), logattr.Addr(member.Addr.String()))
		case serf.EventMemberLeave:
			slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "node left", logattr.Member(member.Name))
		case serf.EventMemberFailed:
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "node failed", logattr.Member(member.Name))
		}
	}
}

// handleUserEvent processes user events
func (m *Membership) handleUserEvent(event serf.UserEvent) {
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "user event received", logattr.Event(event.Name), logattr.Bytes(len(event.Payload)))
}

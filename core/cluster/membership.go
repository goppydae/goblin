package cluster

import (
	"fmt"
	"log"
	"sync"

	"github.com/hashicorp/serf/serf"
)

// Membership manages cluster membership using Serf
type Membership struct {
	serf      *serf.Serf
	eventCh   chan serf.Event
	nodeID    string
	bindAddr  string
	bindPort  int
	handler   func(serf.Event)
	handlerMu sync.RWMutex
}

// NewMembership creates a new Serf-based membership manager
func NewMembership(nodeID, bindAddr string, bindPort int, tags map[string]string) (*Membership, error) {
	eventCh := make(chan serf.Event, 256)

	config := serf.DefaultConfig()
	config.NodeName = nodeID
	config.MemberlistConfig.BindAddr = bindAddr
	config.MemberlistConfig.BindPort = bindPort
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
		serf:     s,
		eventCh:  eventCh,
		nodeID:   nodeID,
		bindAddr: bindAddr,
		bindPort: bindPort,
	}

	// Start event handler
	go m.handleEvents()

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

		// Invoke callback
		m.handlerMu.RLock()
		if m.handler != nil {
			m.handler(event)
		}
		m.handlerMu.RUnlock()
	}
}

// handleMemberEvent processes member join/leave/fail events
func (m *Membership) handleMemberEvent(event serf.MemberEvent) {
	for _, member := range event.Members {
		switch event.EventType() {
		case serf.EventMemberJoin:
			log.Printf("[cluster] Node joined: %s (%s)", member.Name, member.Addr)
		case serf.EventMemberLeave:
			log.Printf("[cluster] Node left: %s", member.Name)
		case serf.EventMemberFailed:
			log.Printf("[cluster] Node failed: %s", member.Name)
		}
	}
}

// handleUserEvent processes user events
func (m *Membership) handleUserEvent(event serf.UserEvent) {
	log.Printf("[cluster] User event: %s (payload: %d bytes)", event.Name, len(event.Payload))
}

package consensus

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"
)

// Consensus manages cluster consensus using Raft
type Consensus struct {
	raft      *raft.Raft
	fsm       *FSM
	transport raft.Transport
	nodeID    string
	dataDir   string
}

// NewConsensus creates a new Raft-based consensus manager
func NewConsensus(nodeID, dataDir, bindAddr string, tlsCfg *tls.Config) (*Consensus, error) {
	// Create data directory
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	// Create Raft config
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(nodeID)
	config.HeartbeatTimeout = 1 * time.Second
	config.ElectionTimeout = 1 * time.Second
	config.CommitTimeout = 50 * time.Millisecond
	config.LogOutput = io.Discard // Disable logging

	// Create FSM
	fsm := NewFSM()

	// Create log store
	logStore, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft-log.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create log store: %w", err)
	}

	// Create stable store
	stableStore, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft-stable.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create stable store: %w", err)
	}

	// Create snapshot store
	snapshotStore, err := raft.NewFileSnapshotStore(dataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %w", err)
	}

	// Create transport
	var transport raft.Transport

	if tlsCfg != nil {
		stream, err := NewTLSStreamLayer(bindAddr, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS stream: %w", err)
		}
		transport = raft.NewNetworkTransport(stream, 3, 10*time.Second, os.Stderr)
	} else {
		addr, err := net.ResolveTCPAddr("tcp", bindAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve address: %w", err)
		}

		transport, err = raft.NewTCPTransport(bindAddr, addr, 3, 10*time.Second, os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("failed to create transport: %w", err)
		}
	}

	// Create Raft
	r, err := raft.NewRaft(config, fsm, logStore, stableStore, snapshotStore, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create raft: %w", err)
	}

	c := &Consensus{
		raft:      r,
		fsm:       fsm,
		transport: transport,
		nodeID:    nodeID,
		dataDir:   dataDir,
	}

	// Bootstrap if single node
	configuration := raft.Configuration{
		Servers: []raft.Server{
			{
				ID:      config.LocalID,
				Address: transport.LocalAddr(),
			},
		},
	}
	r.BootstrapCluster(configuration)

	return c, nil
}

// IsLeader returns true if this node is the leader
func (c *Consensus) IsLeader() bool {
	return c.raft.State() == raft.Leader
}

// Leader returns the current leader address
func (c *Consensus) Leader() string {
	addr, _ := c.raft.LeaderWithID()
	return string(addr)
}

// LeaderID returns the current leader ID
func (c *Consensus) LeaderID() string {
	_, id := c.raft.LeaderWithID()
	return string(id)
}

// VerifyLeader checks if this node is the leader and has a quorum
func (c *Consensus) VerifyLeader() error {
	future := c.raft.VerifyLeader()
	return future.Error()
}

// Apply applies a command to the Raft log
func (c *Consensus) Apply(data []byte, timeout time.Duration) error {
	future := c.raft.Apply(data, timeout)
	return future.Error()
}

// AddVoter adds a new voting member to the cluster
func (c *Consensus) AddVoter(id, address string) error {
	future := c.raft.AddVoter(raft.ServerID(id), raft.ServerAddress(address), 0, 0)
	return future.Error()
}

// RemoveServer removes a server from the cluster
func (c *Consensus) RemoveServer(id string) error {
	future := c.raft.RemoveServer(raft.ServerID(id), 0, 0)
	return future.Error()
}

// Shutdown stops the consensus manager
func (c *Consensus) Shutdown() error {
	return c.raft.Shutdown().Error()
}

// GetState returns the current FSM state
func (c *Consensus) GetState(namespace, key string) ([]byte, bool) {
	return c.fsm.Get(namespace, key)
}

// Scan returns all keys matching the prefix
func (c *Consensus) Scan(namespace, prefix string) map[string][]byte {
	return c.fsm.Scan(namespace, prefix)
}

// Stats returns Raft statistics
func (c *Consensus) Stats() map[string]string {
	return c.raft.Stats()
}

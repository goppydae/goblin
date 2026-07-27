package consensus

import (
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/goppydae/goblin/core/transport"
	"github.com/hashicorp/raft"
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
	config.LogOutput = io.Discard

	// Create FSM
	fsm := NewFSM()

	// Create log store
	logStore, err := NewBoltStore(filepath.Join(dataDir, "raft-log.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create log store: %w", err)
	}

	// Create stable store
	stableStore, err := NewBoltStore(filepath.Join(dataDir, "raft-stable.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create stable store: %w", err)
	}

	// Create snapshot store
	snapshotStore, err := raft.NewFileSnapshotStore(dataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %w", err)
	}

	// Create transport
	var raftTransport raft.Transport

	if tlsCfg == nil {
		tlsCfg = &tls.Config{InsecureSkipVerify: true}
	}
	if len(tlsCfg.Certificates) == 0 && tlsCfg.GetCertificate == nil {
		fmt.Println("⚠️  Generating self-signed certificate for Raft QUIC transport")
		cert, err := transport.GenerateInsecureSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("failed to generate self-signed cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	// Enforce ALPN for Raft
	tlsCfg.NextProtos = []string{"raft-quic"}

	stream, err := transport.NewQUICStreamLayer(bindAddr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create QUIC stream layer: %w", err)
	}

	raftTransport = raft.NewNetworkTransport(stream, 3, 10*time.Second, os.Stderr)

	// Create Raft
	r, err := raft.NewRaft(config, fsm, logStore, stableStore, snapshotStore, raftTransport)
	if err != nil {
		return nil, fmt.Errorf("failed to create raft: %w", err)
	}

	c := &Consensus{
		raft:      r,
		fsm:       fsm,
		transport: raftTransport,
		nodeID:    nodeID,
		dataDir:   dataDir,
	}

	// Bootstrap if single node
	configuration := raft.Configuration{
		Servers: []raft.Server{
			{
				ID:      config.LocalID,
				Address: raftTransport.LocalAddr(),
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

// ApplyWithResponse applies a command and returns the FSM's response value
// alongside any commit error. Commands whose outcome is data (CAS) return a
// typed error as the response; a nil, nil result is an applied success.
func (c *Consensus) ApplyWithResponse(data []byte, timeout time.Duration) (interface{}, error) {
	future := c.raft.Apply(data, timeout)
	if err := future.Error(); err != nil {
		return nil, err
	}
	return future.Response(), nil
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

// GetStateWithVersion returns the current FSM state and the key's CAS
// version (0 when the key is absent).
func (c *Consensus) GetStateWithVersion(namespace, key string) ([]byte, uint64, bool) {
	return c.fsm.GetWithVersion(namespace, key)
}

// Scan returns all keys matching the prefix
func (c *Consensus) Scan(namespace, prefix string) map[string][]byte {
	return c.fsm.Scan(namespace, prefix)
}

// Stats returns Raft statistics
func (c *Consensus) Stats() map[string]string {
	return c.raft.Stats()
}

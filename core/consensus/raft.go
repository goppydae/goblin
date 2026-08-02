package consensus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	goblinv1 "github.com/goppydae/goblin/proto"
	hclog "github.com/hashicorp/go-hclog"
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

// NewConsensus builds the Raft engine over the provided stream layer
// (the raft-quic plane of the shared control-plane listener; the
// layer's Addr() is this server's advertised raft address). bootstrap
// must be true on exactly one seed node (the one with no join target):
// every node bootstrapping its own single-node cluster yields N
// independent rafts that gossip but never share state - the failure
// mode the 2b e2e exposed.
//
// snapshotThreshold, snapshotInterval, and trailingLogs tune Raft's
// snapshot-compaction behavior (raft.Config.SnapshotThreshold /
// SnapshotInterval / TrailingLogs); zero keeps raft.DefaultConfig's
// value for that field (GOBLIN-DIV-040: operators need these to bound
// trailing-log size and replay-on-join cost, not just the library
// defaults tuned for a generic workload).
func NewConsensus(nodeID, dataDir string, stream raft.StreamLayer, bootstrap bool,
	snapshotThreshold uint64, snapshotInterval time.Duration, trailingLogs uint64) (*Consensus, error) {
	// Create data directory
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	// Create Raft config
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(nodeID)
	config.HeartbeatTimeout = 1 * time.Second
	config.ElectionTimeout = 1 * time.Second
	config.CommitTimeout = 50 * time.Millisecond
	// logCapture discards raft's internal log like io.Discard did, but
	// remembers the last ERROR-level line: raft.NewRaft's own error on a
	// failed FSM.Restore is the generic "failed to load any existing
	// snapshots" (vendor/.../raft/api.go restoreSnapshot) - the FSM's
	// specific, operator-facing refusal (e.g. the pre-schema-reset
	// snapshot message, GOBLIN-DIV-040) only ever reaches raft's logger,
	// never the returned error. Without capturing it here, that message
	// never reaches the operator.
	logCapture := &raftLogCapture{}
	config.LogOutput = logCapture
	if snapshotThreshold > 0 {
		config.SnapshotThreshold = snapshotThreshold
	}
	if snapshotInterval > 0 {
		config.SnapshotInterval = snapshotInterval
	}
	if trailingLogs > 0 {
		config.TrailingLogs = trailingLogs
	}

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

	// Create transport over the routed stream layer; TLS and listener
	// ownership live with the supervisor's shared listener.
	raftTransport := raft.NewNetworkTransport(stream, 3, 10*time.Second, os.Stderr)

	// Create Raft
	r, err := raft.NewRaft(config, fsm, logStore, stableStore, snapshotStore, raftTransport)
	if err != nil {
		if last := logCapture.lastError(); last != "" {
			return nil, fmt.Errorf("failed to create raft: %w (%s)", err, last)
		}
		return nil, fmt.Errorf("failed to create raft: %w", err)
	}

	c := &Consensus{
		raft:      r,
		fsm:       fsm,
		transport: raftTransport,
		nodeID:    nodeID,
		dataDir:   dataDir,
	}

	// Bootstrap only the seed node; joiners wait to be admitted as
	// voters by the leader.
	if bootstrap {
		if err := c.Bootstrap([]raft.Server{{
			ID:      config.LocalID,
			Address: raftTransport.LocalAddr(),
		}}); err != nil {
			return nil, err
		}
	}

	return c, nil
}

// Bootstrap installs an initial Raft configuration. It is separate
// from construction because bootstrap-expect cannot decide the server
// set until gossip has converged: the engine comes up as a
// configuration-less follower and is seeded once the peers are known.
//
// ErrCantBootstrap means state already exists (a restart, or another
// seed won the race) - not an error.
func (c *Consensus) Bootstrap(servers []raft.Server) error {
	if len(servers) == 0 {
		return errors.New("bootstrap raft cluster: empty server set")
	}
	err := c.raft.BootstrapCluster(raft.Configuration{Servers: servers}).Error()
	if err != nil && !errors.Is(err, raft.ErrCantBootstrap) {
		return fmt.Errorf("bootstrap raft cluster: %w", err)
	}
	return nil
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

// GetInstance returns a live instance record by canonical UUID string.
func (c *Consensus) GetInstance(instanceID string) (*goblinv1.AgentInstance, bool) {
	return c.fsm.GetInstance(instanceID)
}

// ListInstances returns every live instance record.
func (c *Consensus) ListInstances() []*goblinv1.AgentInstance {
	return c.fsm.ListInstances()
}

// IsTombstoned reports whether an instance UUID was ever terminated.
func (c *Consensus) IsTombstoned(instanceID string) bool {
	return c.fsm.IsTombstoned(instanceID)
}

// OperatorKeysVerified returns the operator key registry and its serial
// only if this node is the leader with a live quorum (GOBLIN-DIV-044).
//
// This is the accessor for anything that AUTHORIZES from key material.
// The unverified reads below answer from whatever this replica happens
// to have applied, and a follower that has not applied an
// OPERATOR_KEY_CHANGE remove still resolves the removed key - so a
// consumer that says yes on a successful lookup would mint for a revoked
// operator by asking a lagging replica. VerifyLeader is what makes that
// unreachable: a follower cannot answer at all, and a leader that lost
// quorum cannot either.
//
// It is a refusal, not a forward. Routing to the leader is the caller's
// policy decision; this surface's job is only that a stale reader cannot
// say yes.
func (c *Consensus) OperatorKeysVerified() ([]*goblinv1.OperatorKey, uint64, error) {
	if err := c.VerifyLeader(); err != nil {
		return nil, 0, fmt.Errorf("operator key registry read refused: %w", err)
	}
	keys, serial := c.fsm.OperatorKeysLocal()
	return keys, serial, nil
}

// OperatorKeysLocal returns THIS NODE's applied operator key registry
// and its serial (GOBLIN-DIV-015 piece 1). It performs no leadership
// check, so the answer may predate a committed change. Use
// OperatorKeysVerified for anything that authorizes; use this only where
// a stale answer cannot produce a yes, and say so at the call site.
func (c *Consensus) OperatorKeysLocal() ([]*goblinv1.OperatorKey, uint64) {
	return c.fsm.OperatorKeysLocal()
}

// OperatorKeyCountLocal reports how many operator keys THIS NODE has
// applied. Zero is the fail-closed condition: no key, no mutation. No
// leadership check, deliberately - see OperatorKeyCountLocal on the FSM
// for why staleness can only move this answer toward refusal.
func (c *Consensus) OperatorKeyCountLocal() int {
	return c.fsm.OperatorKeyCountLocal()
}

// Stats returns Raft statistics
func (c *Consensus) Stats() map[string]string {
	return c.raft.Stats()
}

// NodeID is this node's raft server id. It exists so a refusal can name
// WHICH node refused without threading the supervisor's config through
// every gate (GOBLIN-DIV-048): a diagnostic that says "the registry was
// empty" is useless if it cannot say empty on whom.
func (c *Consensus) NodeID() string {
	return c.nodeID
}

// raftLogCapture is an hclog.LevelWriter used as raft's LogOutput. It
// discards every line - raft's internal log is noise goblind does not
// want on stdout/stderr - except it remembers the most recent
// ERROR-level line. hclog's writer flushes one full formatted line per
// Write/LevelWrite call (vendor/.../go-hclog/writer.go Flush), so
// lastError() always holds a complete message, not a partial one.
//
// This exists because raft.NewRaft's own error on a failed synchronous
// FSM.Restore is generic (restoreSnapshot's "failed to load any
// existing snapshots"): the FSM's specific reason - e.g. GOBLIN-DIV-040's
// pre-schema-reset-snapshot refusal - is only ever logged, never
// returned. NewConsensus folds the captured line back into the error it
// returns so the operator-facing message actually reaches the operator.
type raftLogCapture struct {
	mu   sync.Mutex
	last string
}

// Write implements io.Writer for callers that bypass LevelWrite (hclog
// always prefers LevelWrite when the output implements it, but the
// interface requires Write too).
func (c *raftLogCapture) Write(p []byte) (int, error) {
	return len(p), nil
}

// LevelWrite implements hclog.LevelWriter.
func (c *raftLogCapture) LevelWrite(level hclog.Level, p []byte) (int, error) {
	if level == hclog.Error {
		c.mu.Lock()
		c.last = strings.TrimSpace(string(p))
		c.mu.Unlock()
	}
	return len(p), nil
}

// lastError returns the most recent ERROR-level line raft logged, or ""
// if none has been logged yet.
func (c *raftLogCapture) lastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

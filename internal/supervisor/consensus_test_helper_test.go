package supervisor

import (
	"crypto/ed25519"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/consensus"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/raft"
)

// testConsensusStream is a minimal raft.StreamLayer over plain TCP,
// standing in for the QUIC transport supervisor.go wires in
// production. It exists only so unit tests in this package can build a
// real *consensus.Consensus: the DIV-015 fail-closed gate
// (requireOperatorRegistry in authorize.go) reads OperatorKeyCountLocal()
// off that concrete type, so a handful of pre-existing authorize/RPC
// unit tests need a genuinely populated registry to reach the behavior
// they were written to exercise. A single-node Raft never dials
// itself, so Dial only runs if that assumption ever changes.
type testConsensusStream struct {
	ln *net.TCPListener
}

func newTestConsensusStream() (*testConsensusStream, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &testConsensusStream{ln: ln.(*net.TCPListener)}, nil
}

func (s *testConsensusStream) Accept() (net.Conn, error) { return s.ln.Accept() }
func (s *testConsensusStream) Close() error              { return s.ln.Close() }
func (s *testConsensusStream) Addr() net.Addr            { return s.ln.Addr() }
func (s *testConsensusStream) Dial(addr raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(addr), timeout)
}

var (
	testConsensusOnce sync.Once
	testConsensusVal  *consensus.Consensus
	testConsensusErr  error
)

// testConsensusWithOperatorKey returns a real, single-node Consensus:
// bootstrapped, leader, and seeded with exactly one operator key. It is
// built once for the whole internal/supervisor test binary (real Raft
// leader election is not free) and deliberately never torn down -
// shutting it down after the first caller's test would break every
// later test that shares it. The OS reclaims the listener and temp
// dir at process exit.
func testConsensusWithOperatorKey(t *testing.T) *consensus.Consensus {
	t.Helper()
	testConsensusOnce.Do(func() {
		testConsensusVal, testConsensusErr = buildTestConsensus()
	})
	if testConsensusErr != nil {
		t.Fatalf("build test consensus: %v", testConsensusErr)
	}
	return testConsensusVal
}

func buildTestConsensus() (*consensus.Consensus, error) {
	dir, err := os.MkdirTemp("", "goblin-supervisor-consensus-test")
	if err != nil {
		return nil, fmt.Errorf("temp raft dir: %w", err)
	}
	stream, err := newTestConsensusStream()
	if err != nil {
		return nil, fmt.Errorf("stream layer: %w", err)
	}
	c, err := consensus.NewConsensus("test-node", dir, stream, true, 0, 0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("new consensus: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for !c.IsLeader() {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("single-node consensus never became leader")
		}
		time.Sleep(10 * time.Millisecond)
	}

	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("generate operator key: %w", err)
	}
	key, err := capability.NewOperatorKey(pub, "supervisor-unit-test")
	if err != nil {
		return nil, fmt.Errorf("new operator key: %w", err)
	}
	raw, err := proto.Marshal(&goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{key}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal seed: %w", err)
	}
	if _, err := c.ApplyWithResponse(raw, 5*time.Second); err != nil {
		return nil, fmt.Errorf("apply seed: %w", err)
	}
	return c, nil
}

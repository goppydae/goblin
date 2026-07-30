package supervisor

import (
	"errors"
	"strings"
	"testing"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// StartAgentInstance and RestoreAgentInstance both wrap a nested
// AgentSpec message. Batch B's review found that wrapping a domain
// message in a request made a nil inner message reachable past the
// generated nil-safe getters and into a direct field dereference deep
// in the call chain - a panic in an unrecovered stream goroutine, which
// takes the node down. Both nested-spec paths in this batch are guarded
// the same way: reject before doing anything else.
//
// Hypothesis: a nil spec is rejected with an error naming "spec" and no
// panic, on both StartAgentInstance (guarded in the method itself,
// since node_rpc.go is already in package supervisor and can reach
// ErrInvalidRequest) and the RestoreAgentInstance QUIC handler (guarded
// at the decode boundary in quic_handlers.go, per the design's note
// that core/migration's Caller cannot import internal/supervisor).
// Disproof: either path panicking, or accepting a nil spec, or
// rejecting a request that carries a non-nil spec.

// newTestAgentMgr returns a minimally-wired AgentManager: enough for
// StartAgentInstance to reach past its own "agent manager not
// initialized" guard into the spec guard under test.
func newTestAgentMgr(t *testing.T) *gapiagentmgr.AgentManager {
	t.Helper()
	return gapiagentmgr.NewAgentManager(nil, nil, "", false, nil)
}

func TestStartAgentInstance_RejectsNilSpec(t *testing.T) {
	cases := []struct {
		name    string
		spec    *goblinv1.AgentSpec
		wantErr bool
	}{
		{name: "nil spec is rejected", spec: nil, wantErr: true},
		{name: "spec set proceeds past the guard", spec: &goblinv1.AgentSpec{Type: "no-such-agent-type"}, wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &NodeRPC{agentMgr: newTestAgentMgr(t), tracker: newInstanceTracker()}
			req := &goblinv1.NodeStartAgentInstanceRequest{InstanceId: "inst-1", Spec: tc.spec}
			var resp goblinv1.NodeStartAgentInstanceResponse

			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("StartAgentInstance panicked: %v", r)
					}
				}()
				err = n.StartAgentInstance(req, &resp)
			}()

			if !tc.wantErr {
				// A present-but-unregistered agent type still fails, but
				// for a different reason - proving the guard did not
				// swallow a legitimate request.
				if err == nil {
					t.Fatal("StartAgentInstance succeeded with an unregistered agent type")
				}
				if errors.Is(err, ErrInvalidRequest) {
					t.Errorf("a request carrying a spec was rejected as invalid: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("StartAgentInstance accepted a nil spec")
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want errors.Is(err, ErrInvalidRequest)", err)
			}
			if !strings.Contains(err.Error(), "spec") {
				t.Errorf("err = %v, want it to name %q", err, "spec")
			}
		})
	}
}

// TestRestoreAgentInstanceHandler_RejectsNilSpec exercises the guard at
// the QUIC dispatch layer (quic_handlers.go), which is where
// RestoreAgentInstance's nil-spec rejection is classified as
// ErrInvalidRequest - the method itself (node_rpc_migration.go) keeps
// its pre-existing plain-error validation for direct callers.
func TestRestoreAgentInstanceHandler_RejectsNilSpec(t *testing.T) {
	cases := []struct {
		name    string
		spec    *goblinv1.AgentSpec
		wantErr bool
	}{
		{name: "nil spec is rejected", spec: nil, wantErr: true},
		// A non-nil spec clears the handler's guard and reaches the
		// method, which then fails for an unrelated reason (no image
		// store configured) - proving the guard let it through.
		{name: "spec set proceeds past the guard", spec: &goblinv1.AgentSpec{Type: "worker"}, wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &NodeRPC{tracker: newInstanceTracker()}
			server := NewQUICRPCServer()
			RegisterNodeHandlers(server, n)

			req := &goblinv1.NodeRestoreAgentInstanceRequest{InstanceId: "inst-1", Spec: tc.spec}
			payload, err := proto.Marshal(req)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}

			handler, ok := server.handlers["NodeRPC.RestoreAgentInstance"]
			if !ok {
				t.Fatal("NodeRPC.RestoreAgentInstance not registered")
			}

			var handlerErr error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("RestoreAgentInstance handler panicked: %v", r)
					}
				}()
				_, handlerErr = handler(payload)
			}()

			if !tc.wantErr {
				if handlerErr == nil {
					t.Fatal("restore succeeded with no image store configured")
				}
				if errors.Is(handlerErr, ErrInvalidRequest) {
					t.Errorf("a request carrying a spec was rejected as invalid: %v", handlerErr)
				}
				return
			}

			if handlerErr == nil {
				t.Fatal("restore handler accepted a nil spec")
			}
			if !errors.Is(handlerErr, ErrInvalidRequest) {
				t.Errorf("err = %v, want errors.Is(err, ErrInvalidRequest)", handlerErr)
			}
			if !strings.Contains(handlerErr.Error(), "spec") {
				t.Errorf("err = %v, want it to name %q", handlerErr, "spec")
			}
		})
	}
}

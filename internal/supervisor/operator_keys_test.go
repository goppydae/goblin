// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/metrics"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/raft"
)

// gaugeValue reads the current value of a registered gauge through the
// same prometheus.Registry the running process exposes on /metrics.
// github.com/prometheus/client_golang/prometheus/testutil is not
// vendored in this repo, so tests read gauges the way a scrape would
// rather than adding a test-only import that vendor-mode builds cannot
// satisfy.
func gaugeValue(t *testing.T, name string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

// fakeRegistry is a leader-or-not consensus stand-in backed by a real
// FSM, so the seeder is tested against the actual apply semantics
// rather than a hand-written imitation of them.
type fakeRegistry struct {
	mu     sync.Mutex
	leader bool
	fsm    *consensus.FSM
}

func newFakeRegistry(leader bool) *fakeRegistry {
	return &fakeRegistry{leader: leader, fsm: consensus.NewFSM(nil)}
}

func (r *fakeRegistry) IsLeader() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leader
}

func (r *fakeRegistry) setLeader(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leader = v
}

func (r *fakeRegistry) OperatorKeysLocal() ([]*goblinv1.OperatorKey, uint64) {
	return r.fsm.OperatorKeysLocal()
}

func (r *fakeRegistry) ApplyWithResponse(data []byte, _ time.Duration) (interface{}, error) {
	return r.fsm.Apply(&raft.Log{Data: data}), nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func testOperatorKey(t *testing.T, comment string) *goblinv1.OperatorKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	k, err := capability.NewOperatorKey(pub, comment)
	if err != nil {
		t.Fatalf("NewOperatorKey: %v", err)
	}
	return k
}

func TestSeederInstallsConfiguredKeysWhenLeader(t *testing.T) {
	metrics.OperatorKeyConfigDrift.Set(1)
	t.Cleanup(func() { metrics.OperatorKeyConfigDrift.Set(0) })

	reg := newFakeRegistry(true)
	key := testOperatorKey(t, "root")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runOperatorKeySeeder(ctx, reg, []*goblinv1.OperatorKey{key},
		quietLogger(), 10*time.Millisecond); err != nil {
		t.Fatalf("seeder: %v", err)
	}
	keys, _ := reg.OperatorKeysLocal()
	if len(keys) != 1 || keys[0].GetKeyId() != key.GetKeyId() {
		t.Fatalf("registry = %v, want the configured key %s", keys, key.GetKeyId())
	}
	if got := gaugeValue(t, "goblin_operator_key_config_drift"); got != 0 {
		t.Fatalf("drift gauge = %v, want 0 after a successful seed", got)
	}
}

func TestSeederWaitsForLeadership(t *testing.T) {
	reg := newFakeRegistry(false)
	key := testOperatorKey(t, "root")

	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		done <- runOperatorKeySeeder(ctx, reg, []*goblinv1.OperatorKey{key},
			quietLogger(), 10*time.Millisecond)
	}()

	time.Sleep(50 * time.Millisecond)
	if reg.fsm.OperatorKeyCountLocal() != 0 {
		t.Fatal("the seeder proposed without leadership")
	}
	reg.setLeader(true)

	if err := <-done; err != nil {
		t.Fatalf("seeder after leadership: %v", err)
	}
	if reg.fsm.OperatorKeyCountLocal() != 1 {
		t.Fatalf("registry holds %d keys, want 1", reg.fsm.OperatorKeyCountLocal())
	}
}

// TestSeederReportsConfigDriftAsAnError covers a node joining an
// already-seeded cluster: the registry was populated before this node
// started watching, so a mismatch here is a stale --operator-key flag,
// not a lost bootstrap race.
func TestSeederReportsConfigDriftAsAnError(t *testing.T) {
	reg := newFakeRegistry(true)
	// Another node already seeded a different root of trust.
	other := testOperatorKey(t, "other")
	raw, err := proto.Marshal(&goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{other}},
		},
	})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if _, aerr := reg.ApplyWithResponse(raw, time.Second); aerr != nil {
		t.Fatalf("pre-seed: %v", aerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = runOperatorKeySeeder(ctx, reg, []*goblinv1.OperatorKey{testOperatorKey(t, "mine")},
		quietLogger(), 10*time.Millisecond)
	if !errors.Is(err, ErrOperatorConfigStale) {
		t.Fatalf("seeder against a differently-seeded registry = %v, want ErrOperatorConfigStale", err)
	}
}

// TestSeederReportsDriftWithoutLeadership pins the non-leader drift
// branch, which no other test reaches. TestSeederReportsConfigDriftAsAnError
// looks like it covers this but does not: it starts as leader, so its
// refusal comes from the FSM rejecting a second seed, not from
// driftedKeys. A node that is not leader can still tell it disagrees
// with the cluster, and must say so rather than wait for a leadership
// change that may never come. Like its leader-branch sibling, the
// registry here is already populated when the seeder starts, so this is
// a node joining an ALREADY-seeded cluster - a stale flag, not a lost race.
func TestSeederReportsDriftWithoutLeadership(t *testing.T) {
	reg := newFakeRegistry(false)

	// Another node already seeded a different root of trust.
	other := testOperatorKey(t, "other")
	raw, err := proto.Marshal(&goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{other}},
		},
	})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if _, aerr := reg.ApplyWithResponse(raw, time.Second); aerr != nil {
		t.Fatalf("pre-seed: %v", aerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = runOperatorKeySeeder(ctx, reg, []*goblinv1.OperatorKey{testOperatorKey(t, "mine")},
		quietLogger(), 10*time.Millisecond)
	if !errors.Is(err, ErrOperatorConfigStale) {
		t.Fatalf("non-leader drift = %v, want ErrOperatorConfigStale", err)
	}
}

// TestSeederDoesNotReportDriftWhenTheClusterHasItsKey is the negative
// half. Without it, a driftedKeys that always reported drift - or one
// "fixed" to always report none - would still satisfy the positive
// test. A non-leader whose key IS registered has nothing to report and
// must keep waiting rather than fail the daemon.
func TestSeederDoesNotReportDriftWhenTheClusterHasItsKey(t *testing.T) {
	reg := newFakeRegistry(false)
	mine := testOperatorKey(t, "mine")

	raw, err := proto.Marshal(&goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{mine}},
		},
	})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if _, aerr := reg.ApplyWithResponse(raw, time.Second); aerr != nil {
		t.Fatalf("pre-seed: %v", aerr)
	}

	// The cluster already has exactly this node's key, so there is no
	// disagreement. The seeder should keep waiting for leadership and
	// exit only when the context does.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err = runOperatorKeySeeder(ctx, reg, []*goblinv1.OperatorKey{mine},
		quietLogger(), 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-leader with its key registered = %v, want context.DeadlineExceeded", err)
	}
}

func TestSeederWithNoConfiguredKeysReturnsImmediately(t *testing.T) {
	reg := newFakeRegistry(false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runOperatorKeySeeder(ctx, reg, nil, quietLogger(), 10*time.Millisecond); err != nil {
		t.Fatalf("seeder with no keys = %v, want nil", err)
	}
}

// TestSeederRaisesTheDriftGauge pins the observability that replaced the
// fatal path. The log line scrolls away; the gauge is what an operator
// can alert on, so a regression that stopped setting it would remove the
// only durable signal that this node's configured keys were ignored.
func TestSeederRaisesTheDriftGauge(t *testing.T) {
	metrics.OperatorKeyConfigDrift.Set(0)
	t.Cleanup(func() { metrics.OperatorKeyConfigDrift.Set(0) })

	reg := newFakeRegistry(false)
	other := testOperatorKey(t, "other")
	raw, err := proto.Marshal(&goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{other}},
		},
	})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if _, aerr := reg.ApplyWithResponse(raw, time.Second); aerr != nil {
		t.Fatalf("pre-seed: %v", aerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if serr := runOperatorKeySeeder(ctx, reg, []*goblinv1.OperatorKey{testOperatorKey(t, "mine")},
		quietLogger(), 10*time.Millisecond); !errors.Is(serr, ErrOperatorConfigStale) {
		t.Fatalf("drift = %v, want ErrOperatorConfigStale", serr)
	}

	if got := gaugeValue(t, "goblin_operator_key_config_drift"); got != 1 {
		t.Fatalf("drift gauge = %v, want 1", got)
	}
}

func TestRequireOperatorRegistryRefusesWithNoKeys(t *testing.T) {
	// A SchedulerRPC with no consensus at all is the strictest case:
	// nothing can vouch for the registry, so nothing is authorized.
	s := &SchedulerRPC{}
	if err := s.requireOperatorRegistry("agent.scale"); !errors.Is(err, consensus.ErrOperatorRegistryEmpty) {
		t.Fatalf("requireOperatorRegistry with no consensus = %v, want ErrOperatorRegistryEmpty", err)
	}
}

// TestSignalAgentInstanceRefusesWithNoOperatorKeys pins the gate on the
// SIGNAL path specifically. SignalAgentInstance issues its own
// capability token and never passes through authorizeToken, so the
// verb authorizer's gate does not cover it - the two call sites are
// gated separately and only a separate test says so. Without this, a
// refactor that dropped the check here would leave signals as the one
// mutation a keyless cluster still accepts, and every other test would
// stay green.
func TestSignalAgentInstanceRefusesWithNoOperatorKeys(t *testing.T) {
	s := &SchedulerRPC{}
	req := &goblinv1.SignalAgentInstanceRequest{
		InstanceId: ident.String(ident.NewV7()),
		Signum:     15,
	}
	var resp goblinv1.SignalAgentInstanceResponse
	err := s.SignalAgentInstance(req, &resp)
	if !errors.Is(err, consensus.ErrOperatorRegistryEmpty) {
		t.Fatalf("SignalAgentInstance with no operator keys = %v, want ErrOperatorRegistryEmpty", err)
	}
}

// TestNodeRPCMutatingMethodsRefuseWithNoOperatorKeys pins the gate on
// the node-side surface. NodeRPC is registered on the same listener and
// ALPN as SchedulerRPC and is directly callable, so gating only the
// operator-facing surface would leave every node-side mutation open -
// which is exactly what a review found before this test existed.
//
// Table driven so a newly added mutating method is a one-line
// addition. Note this does NOT make an ungated method visible on its
// own: nothing here enumerates NodeRPC's method set, so a method with
// no row reads exactly like a method that does not exist. PullCheckpoint
// was missing from both the gate and this table and the gap went
// unnoticed until a whole-branch review. When adding a NodeRPC method,
// add its row here in the same change.
func TestNodeRPCMutatingMethodsRefuseWithNoOperatorKeys(t *testing.T) {
	n := &NodeRPC{}
	cases := map[string]func() error{
		"StartAgentInstance": func() error {
			var resp goblinv1.NodeStartAgentInstanceResponse
			return n.StartAgentInstance(&goblinv1.NodeStartAgentInstanceRequest{}, &resp)
		},
		"SignalAgentInstance": func() error {
			var resp goblinv1.NodeSignalAgentInstanceResponse
			return n.SignalAgentInstance(&goblinv1.NodeSignalAgentInstanceRequest{}, &resp)
		},
		"StopAgentInstance": func() error {
			var resp goblinv1.NodeStopAgentInstanceResponse
			return n.StopAgentInstance(&goblinv1.NodeStopAgentInstanceRequest{}, &resp)
		},
		"CheckpointAgentInstance": func() error {
			var resp goblinv1.NodeCheckpointAgentInstanceResponse
			return n.CheckpointAgentInstance(&goblinv1.NodeCheckpointAgentInstanceRequest{}, &resp)
		},
		"RestoreAgentInstance": func() error {
			var resp goblinv1.NodeRestoreAgentInstanceResponse
			return n.RestoreAgentInstance(&goblinv1.NodeRestoreAgentInstanceRequest{}, &resp)
		},
		"PullCheckpoint": func() error {
			var resp goblinv1.NodePullCheckpointResponse
			return n.PullCheckpoint(&goblinv1.NodePullCheckpointRequest{}, &resp)
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, consensus.ErrOperatorRegistryEmpty) {
				t.Fatalf("NodeRPC.%s with no operator keys = %v, want ErrOperatorRegistryEmpty", name, err)
			}
		})
	}
}

// TestNodeRPCMutatingMethodsPassTheGateWithAKey shows the refusals above
// come from the gate and not from something else failing first: with a
// seeded registry the same calls get past it and fail later, on their
// own missing collaborators.
func TestNodeRPCMutatingMethodsPassTheGateWithAKey(t *testing.T) {
	n := &NodeRPC{consensus: testConsensusWithOperatorKey(t)}
	var resp goblinv1.NodeStartAgentInstanceResponse
	err := n.StartAgentInstance(&goblinv1.NodeStartAgentInstanceRequest{}, &resp)
	if errors.Is(err, consensus.ErrOperatorRegistryEmpty) {
		t.Fatalf("StartAgentInstance with a seeded registry still hit the registry gate: %v", err)
	}
	if err == nil {
		t.Fatal("StartAgentInstance with an empty request and no agent manager unexpectedly succeeded")
	}
}

// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"errors"
	"testing"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// wireGenerations converts a filter's live generations into the request
// form, which is what the sync loop puts on the wire.
func wireGenerations(r *capability.Revocations) []*goblinv1.RevocationGeneration {
	var out []*goblinv1.RevocationGeneration
	for _, g := range r.Snapshot() {
		out = append(out, &goblinv1.RevocationGeneration{
			Index:  g.Index,
			Filter: g.Filter,
		})
	}
	return out
}

// TestSyncRevocations_MergesThePeerFilterAndAnswersWithItsOwn is the
// repair GOBLIN-DIV-057 exists for: a node learns of a revocation it
// never saw broadcast, and the responder answers with its own state so
// one round trip repairs BOTH directions.
func TestSyncRevocations_MergesThePeerFilterAndAnswersWithItsOwn(t *testing.T) {
	peer := capability.NewRevocations()
	missed := ident.NewV7()
	peer.Revoke(missed)

	local := capability.NewRevocations()
	mine := ident.NewV7()
	local.Revoke(mine)

	s := &SchedulerRPC{revocations: local}
	if s.revocations.IsRevoked(missed) {
		t.Fatal("precondition: this node already knows the revocation it is supposed to be missing")
	}

	req := &goblinv1.SyncRevocationsRequest{Generations: wireGenerations(peer)}
	var resp goblinv1.SyncRevocationsResponse
	if err := s.SyncRevocations(req, &resp); err != nil {
		t.Fatalf("SyncRevocations: %v", err)
	}

	if !s.revocations.IsRevoked(missed) {
		t.Error("the peer's revocation was not merged: this node still accepts a token the peer revoked")
	}

	if len(resp.Generations) == 0 {
		t.Fatal("response carried no generations, so the exchange repairs only the caller")
	}
	answered := capability.NewRevocations()
	var back []capability.Generation
	for _, g := range resp.Generations {
		back = append(back, capability.Generation{Index: g.Index, Filter: g.Filter})
	}
	if err := answered.Ingest(back); err != nil {
		t.Fatalf("the response did not round-trip back into a filter: %v", err)
	}
	if !answered.IsRevoked(mine) {
		t.Error("the response omitted this node's own revocation, so the caller is not repaired")
	}
}

// TestSyncRevocations_RefusesWhenTheFilterIsAbsent covers the node that
// has no capability collaborators - nil outside a full supervisor,
// which is the same shape MigrateInstance and SignalAgentInstance guard
// against. A handler reachable over QUIC must refuse as data rather
// than panic on a nil dereference, since the caller is remote.
func TestSyncRevocations_RefusesWhenTheFilterIsAbsent(t *testing.T) {
	s := &SchedulerRPC{} // no revocations collaborator

	var resp goblinv1.SyncRevocationsResponse
	err := s.SyncRevocations(&goblinv1.SyncRevocationsRequest{}, &resp)
	if err == nil {
		t.Fatal("a node with no revocation filter accepted a sync instead of refusing it")
	}
	if !errors.Is(err, ErrRevocationsUnavailable) {
		t.Errorf("refusal is not distinguishable as data: got %v", err)
	}
}

// TestRegisterSchedulerHandlers_WiresSyncRevocations is the guard
// against the failure this entry is made of: Snapshot, Ingest and Stats
// were all implemented and none was reachable from either binary. A
// method nobody registered is not a mechanism.
//
// It dispatches through the registered handler rather than calling the
// method, so the marshalling on both sides is exercised too.
func TestRegisterSchedulerHandlers_WiresSyncRevocations(t *testing.T) {
	local := capability.NewRevocations()
	peer := capability.NewRevocations()
	missed := ident.NewV7()
	peer.Revoke(missed)

	server := NewQUICRPCServer()
	RegisterSchedulerHandlers(server, &SchedulerRPC{revocations: local})

	handler, ok := server.handlers["SchedulerRPC.SyncRevocations"]
	if !ok {
		t.Fatal("SyncRevocations is implemented but never registered: no caller can reach it")
	}

	payload, err := proto.Marshal(&goblinv1.SyncRevocationsRequest{Generations: wireGenerations(peer)})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	out, err := handler(payload)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !local.IsRevoked(missed) {
		t.Error("dispatch did not merge the peer's filter")
	}

	var resp goblinv1.SyncRevocationsResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Generations) == 0 {
		t.Error("dispatch answered with no generations, so the caller is not repaired")
	}
}

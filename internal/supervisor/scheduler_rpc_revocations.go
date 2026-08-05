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

	"github.com/goppydae/goblin/core/capability"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// ErrRevocationsUnavailable is returned by SyncRevocations on a node
// that carries no revocation filter - nil outside a full supervisor.
//
// A sentinel rather than a formatted string because the sync loop is
// the caller: a node that can never answer should stop being asked,
// and that decision needs the refusal to be data.
var ErrRevocationsUnavailable = errors.New("no revocation filter on this node")

// SyncRevocations is the anti-entropy exchange that repairs revocations
// the best-effort delta broadcast dropped (GOBLIN-DIV-057).
//
// It is symmetric on purpose: the caller sends its live generations and
// receives the responder's, so one round trip repairs both nodes. No
// leader and no ordering are needed - merging is idempotent and the
// filter is a set, so any node may sync with any other at any time.
func (s *SchedulerRPC) SyncRevocations(req *goblinv1.SyncRevocationsRequest, resp *goblinv1.SyncRevocationsResponse) error {
	if s.revocations == nil {
		return ErrRevocationsUnavailable
	}

	incoming := make([]capability.Generation, 0, len(req.GetGenerations()))
	for _, g := range req.GetGenerations() {
		incoming = append(incoming, capability.Generation{
			Index:  g.GetIndex(),
			Filter: g.GetFilter(),
		})
	}

	// Ingest validates each filter's length before merging any of them,
	// so a malformed generation is refused as data rather than folded in.
	if err := s.revocations.Ingest(incoming); err != nil {
		return err
	}

	mine := s.revocations.Snapshot()
	resp.Generations = make([]*goblinv1.RevocationGeneration, 0, len(mine))
	for _, g := range mine {
		resp.Generations = append(resp.Generations, &goblinv1.RevocationGeneration{
			Index:  g.Index,
			Filter: g.Filter,
		})
	}
	return nil
}

// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport

import gapitransport "github.com/goppydae/gapi/core/transport"

// ALPN protocol identifiers for the orchestrator's QUIC listeners and
// dialers. They mirror the ecosystem ALPN registry as their governing
// contract (append-only, tombstoned): a collision is a review failure,
// not a runtime discovery. Every handler and the ALPN router switch
// import these; inline literals are forbidden (GOBLIN-DIV-010).
const (
	// ALPNGoblinRPC is the goblinctl/scheduler RPC channel.
	ALPNGoblinRPC = "goblin-rpc"
	// ALPNSerfQUIC carries Serf gossip over QUIC datagrams/streams.
	ALPNSerfQUIC = "serf-quic"
	// ALPNRaftQUIC carries the Raft transport stream.
	ALPNRaftQUIC = "raft-quic"
	// ALPNGoblinCkpt carries CRIU checkpoint images between nodes
	// during migration (GOBLIN-DIV-018). Deliberately its own ALPN, and
	// therefore its own QUIC connection: a multi-gigabyte image sharing
	// a connection-level flow-control window with Raft heartbeats would
	// degrade the control plane exactly when a migration is in flight.
	ALPNGoblinCkpt = "goblin-ckpt"
	// ALPNGapiQUIC is the kernel's protocol, imported - never redefined
	// - so the two repos cannot drift (consumes GAPI-DIV-011).
	ALPNGapiQUIC = gapitransport.ALPNGapiQUIC
)

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
	// ALPNGapiQUIC is the kernel's protocol, imported - never redefined
	// - so the two repos cannot drift (consumes GAPI-DIV-011).
	ALPNGapiQUIC = gapitransport.ALPNGapiQUIC
)

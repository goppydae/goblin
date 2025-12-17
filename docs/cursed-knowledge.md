# Cursed Knowledge: Goblin

This file contains lessons learned the hard way. Read this before debugging "impossible" issues.

## EventBus & Memory Leaks

### Subscriptions Must Be explicit
**Symptom:** Memory usage grows indefinitely during `Watch` operations or frequent agent reloads.
**Cause:** The `EventBus.Subscribe` method historically returned nothing. Closures created for handlers would hang around forever in the subscriber slice.
**Fix:** Always use the returned `Subscription` handle and call `Unsubscribe()` when the consumer (e.g., a `Watch` request) terminates.
**Ref:** `core/eventbus/distributed.go`, `core/store/store.go`

## Store & Consensus

### Linearizable Reads
**Symptom:** Stale reads from followers during network partitions.
**Cause:** Allowing any node to serve `Get` requests without verifying leadership or index.
**Rule:** Reads must only be served by the Leader, or followers must use `ReadIndex` (not yet implemented) to ensure they are up to date. Currently, we force leader-only reads.

## Testing

### Cluster Stability
**Symptom:** "It works on my machine" but fails in a real cluster.
**Lesson:** Unit tests are insufficient for distributed consensus. Always verify changes with `test_cluster.sh` (3-node ensemble) to catch replication and leader election race conditions.

## QUIC RPC Migration: 130k Tokens Well Spent (Dec 2024)

**Achievement**: Migrated from HTTP/net/rpc to QUIC transport in one epic 2-hour session.

### The quic-go API Trap

```go
// ❌ This haunted us for 30+ iterations
var conn quic.Connection  // undefined: quic.Connection

// ✅ The truth
var conn *quic.Conn
stream, _ := conn.AcceptStream(ctx)  // Returns *quic.Stream
io.ReadFull(stream, buf)  // Use directly, not *stream
```

### RPC Handler Type Mismatch

When creating QUIC adapters for existing RPC methods, use the ACTUAL types:

```go
// ❌ Creating new types breaks RPC signatures
type JobSubmitRequest struct { ... }

// ✅ Use existing scheduler types
var job scheduler.Job  // Matches SubmitJob(*scheduler.Job, *string)
```

### TUI Field Name Changes

```go
// ❌ OLD                  // ✅ NEW
AgentID   → ID
AgentType → Type  
Status    → State
```

### The Reward

Port 9000 now speaks QUIC+TLS1.3+Protobuf. Unified with GAPI architecture. HTTP server removed entirely.

**Stats**: 45+ tool calls, 130k tokens, 100% success rate (eventually).

## Serf & Memberlist QUIC Migration (Dec 2024)

### ALPN is Mandatory
**Symptom**: `CRYPTO_ERROR 0x178 (remote): tls: no application protocol` when nodes try to join.
**Cause**: `quic-go` strictly enforces ALPN negotiation. If `NextProtos` is empty or mismatched, the handshake fails immediately.
**Fix**: Explicitly set `NextProtos: []string{"serf-quic"}` on both the Server listener and the Client dialer `tls.Config`. You cannot rely on default TLS configs or empty values.

### Packet vs Stream Semantics
**Challenge**: `memberlist.Transport` expects both `WriteTo` (UDP-ish) and `DialTimeout` (TCP-ish).
**Solution**:
- `WriteTo`: Check for cached active QUIC connection. If none, Dial (short timeout). Send `Datagram` (RFC 9221).
- `DialTimeout`: Open a `Stream` on the QUIC connection.
- **Trap**: Do not open a new connection for every "Packet". Memberlist gossips *a lot*. Connection caching is critical.

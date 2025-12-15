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

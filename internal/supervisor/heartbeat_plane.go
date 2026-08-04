// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// The instance-heartbeat plane, split out of phase_serving.go when that
// file reached the 500-line limit. It is one coherent unit: the publish
// loop, the leader-side ingest that feeds the reconciler's health view
// and the locator map, and the orphan reap that answers a heartbeat for
// an instance the cluster already tombstoned (GOBLIN-DIV-067).

package supervisor

import (
	"context"
	"log/slog"
	"time"

	"github.com/goppydae/gapi/core/procsig"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/internal/hlc"
	"github.com/goppydae/goblin/internal/logattr"
)

// startHeartbeat publishes this node's instance states cluster-wide and
// merges what it sees from peers. The heartbeat doubles as the locator
// update path (DDR-3/4/5): a running instance's pid, start epoch, and
// pid-namespace inode ride along, stamped by this node's HLC for
// last-writer-wins.
func (s *Supervisor) startHeartbeat(ctx context.Context, st *runState) {
	hlcClock := hlc.New(st.nodeID)
	nodeID, bus, tracker, sched := st.nodeID, st.bus, st.tracker, st.sched

	s.loops.spawn(tierRun, "instance-heartbeat", func() {
		ticker := time.NewTicker(scheduler.HeartbeatCadence)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for id, info := range tracker.Snapshot() {
					stamp := hlcClock.Now()
					payload := map[string]interface{}{
						"instance_id": id,
						"node_id":     nodeID,
						"state":       info.State,
						"hlc_wall":    stamp.Wall,
						"hlc_counter": stamp.Counter,
						"hlc_node":    stamp.Node,
					}
					if info.Pid > 0 {
						if pi, err := procsig.Identify(info.Pid); err == nil {
							payload["node_pid"] = pi.Pid
							payload["start_epoch"] = pi.StartEpoch
							payload["pid_ns_inode"] = pi.PidNsInode
						}
					}
					if err := bus.PublishCluster("system", "instance.heartbeat", payload, nil); err != nil {
						slog.Default().LogAttrs(ctx, slog.LevelWarn, "publish instance heartbeat failed", logattr.InstanceID(id), logattr.Err(err))
					}
				}
			}
		}
	})

	// Leader side: heartbeats feed the reconciler's health view and the
	// locator map; the HLC merges every remote stamp it sees.
	bus.Subscribe("instance.heartbeat", func(e eventbus.Event) {
		id, _ := e.Payload["instance_id"].(string)
		node, _ := e.Payload["node_id"].(string)
		state, _ := e.Payload["state"].(string)
		if id == "" {
			return
		}
		// Reconcile-on-reconnect (GOBLIN-DIV-067). A node that only
		// blipped past the staleness window comes back running an
		// instance this leader already tombstoned and replaced, and
		// THIS HEARTBEAT IS THE ONLY SIGHTING OF IT: the record is gone
		// from ListInstances, so no reconcile pass can find it by
		// walking state. Answer it with a stop and do not record it -
		// feeding a tombstoned UUID to the health view and the locator
		// map below would resurrect a dead identity in the leader's
		// memory.
		//
		// Callback terminus: the bus dispatches each handler on its own
		// goroutine (eventbus/distributed.go) and there is no caller to
		// propagate to, so a failed stop is logged and retried on the
		// next sighting.
		orphan, err := sched.ReapOrphan(ctx, id, node)
		if err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "could not stop an orphaned instance",
				logattr.InstanceID(id), logattr.NodeID(node), logattr.Err(err))
		}
		if orphan {
			return
		}
		sched.ObserveHeartbeat(id, node, state, time.Now())

		stamp := hlc.Timestamp{
			Wall:    payloadInt64(e.Payload["hlc_wall"]),
			Counter: payloadUint32(e.Payload["hlc_counter"]),
			Node:    payloadString(e.Payload["hlc_node"]),
		}
		if stamp.IsZero() {
			return
		}
		hlcClock.Observe(stamp)
		if pid := payloadInt64(e.Payload["node_pid"]); pid > 0 {
			sched.ObserveLocator(id, scheduler.Locator{
				NodeID:     node,
				Pid:        int(pid),
				StartEpoch: payloadUint64(e.Payload["start_epoch"]),
				PidNsInode: payloadUint64(e.Payload["pid_ns_inode"]),
				At:         stamp,
			})
		}
	})

	// Revocation gossip: a revoking node broadcasts its filter; every
	// node merges what it sees (the filter only grows, so merge order
	// is irrelevant). Bytes ride the bus JSON as base64.
	bus.Subscribe("capability.revocation", func(e eventbus.Event) {
		id, ok := decodeRevocationFilter(ctx, e.Payload["token_id"])
		if !ok {
			return
		}
		if len(id) != 16 {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "revocation carries a malformed token id",
				slog.Int("bytes", len(id)))
			return
		}
		s.revocations.Revoke(id)
	})
}

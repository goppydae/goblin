// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import "sync"

// instanceInfo is a tracked instance's node-local view: lifecycle state
// plus the pid of its process (0 when not running), from which the
// heartbeat publisher derives the gossip locator.
type instanceInfo struct {
	State      string
	Pid        int
	StartEpoch uint64
}

// instanceTracker records the lifecycle state of NodeRPC-managed agent
// instances on this node; the heartbeat publisher snapshots it every
// cadence. States mirror the scheduler's vocabulary: running, failed.
type instanceTracker struct {
	mu        sync.Mutex
	instances map[string]instanceInfo
}

func newInstanceTracker() *instanceTracker {
	return &instanceTracker{instances: make(map[string]instanceInfo)}
}

func (t *instanceTracker) Set(instanceID, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	info := t.instances[instanceID]
	info.State = state
	t.instances[instanceID] = info
}

// SetIdentity records the instance's process identity (pid + start
// epoch) alongside its state; the epoch is the delivery guard for
// signals (stale epoch -> refuse, DDR-5).
func (t *instanceTracker) SetIdentity(instanceID string, pid int, startEpoch uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	info := t.instances[instanceID]
	info.Pid = pid
	info.StartEpoch = startEpoch
	t.instances[instanceID] = info
}

// Get returns a tracked instance's info.
func (t *instanceTracker) Get(instanceID string) (instanceInfo, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	info, ok := t.instances[instanceID]
	return info, ok
}

// SetIfTracked updates state only for instances this node manages; the
// local status feed carries every agent, not just scheduled instances.
func (t *instanceTracker) SetIfTracked(instanceID, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if info, ok := t.instances[instanceID]; ok {
		info.State = state
		t.instances[instanceID] = info
	}
}

func (t *instanceTracker) Remove(instanceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.instances, instanceID)
}

func (t *instanceTracker) Snapshot() map[string]instanceInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]instanceInfo, len(t.instances))
	for k, v := range t.instances {
		out[k] = v
	}
	return out
}

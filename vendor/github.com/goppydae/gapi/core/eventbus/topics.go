// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package eventbus

// Well-known cross-boundary topics. One exported constant per topic; the
// orchestrator imports these rather than repeating string literals
// (GAPI-DIV-010; unblocks GOBLIN-DIV-011). Purely in-package topics may stay
// literals - only cross-boundary topics are promoted here.
const (
	// TopicAgentNetworkRunning announces that the network agent reached its
	// running state; the supervisors' boot phase gate waits on it. Payload:
	// reserved (no producer publishes a typed payload yet; the gate only
	// observes arrival).
	TopicAgentNetworkRunning = "agent.network.running"

	// TopicPrefixAgentLifecycle is the prefix under which every lifecycle
	// topic below lives; prefix subscribers use it with SubscribePrefix
	// (mind the type hazard documented there - sibling topics carry
	// different payload types).
	TopicPrefixAgentLifecycle = "agent/lifecycle"

	// TopicAgentLifecycleAction is the operator/client -> supervisor
	// command channel. Payload: LifecycleControl. Distinct from
	// TopicAgentLifecycleControl despite the confusable name: this one
	// carries requests to act.
	TopicAgentLifecycleAction = "agent/lifecycle.action"

	// TopicAgentLifecycleControl carries the lifecycle controller's own
	// announcements of actions it is applying. Payload: LifecycleControl.
	TopicAgentLifecycleControl = "agent/lifecycle.control"

	// TopicAgentLifecycleStatus carries agent state reports. Payload:
	// LifecycleStatus (run_id is the structural restart-detection key).
	TopicAgentLifecycleStatus = "agent/lifecycle.status"

	// TopicAgentLifecycleTransition carries state-machine transition
	// events. Payload: LifecycleTransition.
	TopicAgentLifecycleTransition = "agent/lifecycle.transition"

	// TopicAgentHeartbeat carries an agent's liveness frame. Payload:
	// Heartbeat - NOT LifecycleStatus. The two answer different
	// questions: a heartbeat asserts the agent is ALIVE, a status asserts
	// a TRANSITION. Synthesizing a LifecycleStatus{State:"RUNNING"} for
	// each heartbeat is the conflation the status proto's own comment
	// condemns, and it is what the two runners did against a bare string
	// literal.
	//
	// NOTHING SUBSCRIBES YET. Named here rather than left inline because
	// a topic no consumer can find by name is one no consumer will
	// subscribe to; the missing consumer belongs in the ledger, not in a
	// string.
	TopicAgentHeartbeat = "agent/heartbeat"

	// TopicSystemShutdown is the operator/client -> supervisor system
	// shutdown request. Payload: wrapperspb.StringValue holding one of
	// "poweroff", "reboot", "halt"; decode fails closed on anything
	// else. In PID-1 mode the supervisor answers with the full
	// teardown (StopAll -> sync -> umount -> reboot(2)); otherwise it
	// stops the runtime and exits.
	TopicSystemShutdown = "system.shutdown"
)

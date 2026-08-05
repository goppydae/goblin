// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package agentmgr

import (
	"fmt"
	"strings"
)

// Deregister removes an agent from the manager; unknown ids are a no-op.
// Callers stop the agent first - deregistering does not kill processes.
func (am *AgentManager) Deregister(id string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	delete(am.agents, id)
}

// Instantiate constructs and registers a new agent that runs the same
// binary as an installed template agent, under its own instance id.
//
// This is the orchestrator's node-dispatch seam (proto-2 Phase 3): a
// global AgentSpec references an installed, discovery-verified agent
// type by id, and every scheduled instance is a fresh incarnation of
// that binary. Specs never carry arbitrary commands, so the discovery
// security model (signed-only production discovery, R20) holds for
// remotely scheduled work exactly as for local agents.
//
// Go binaries only for now: multi-instance python agents need
// per-instance runner and socket plumbing that does not exist yet.
// Instances get no listen socket (it would collide with the template's)
// and inherit the template's resource limits and capabilities.
func (am *AgentManager) Instantiate(instanceID, templateID string) (Agent, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	tmpl, ok := am.agents[templateID]
	if !ok {
		return nil, fmt.Errorf("agent type %q is not installed on this node", templateID)
	}
	if _, exists := am.agents[instanceID]; exists {
		return nil, fmt.Errorf("instance %q is already registered", instanceID)
	}
	if tmpl.Lang() != "go" {
		return nil, fmt.Errorf("agent type %q is %s: only go agents support instantiation", templateID, tmpl.Lang())
	}

	d := tmpl.Describe()
	var caps []string
	if c := d["capabilities"]; c != "" {
		for _, part := range strings.Split(c, ",") {
			caps = append(caps, strings.TrimSpace(part))
		}
	}

	a := NewGoAgent(
		instanceID, tmpl.Type(), d["path"],
		tmpl.Requires(), tmpl.Wants(), nil, nil,
		"", // no listen socket: it would collide with the template's
		d["cpu_limit"], d["mem_limit"],
		caps,
		am.bus,
		depView{am},
	)
	am.agents[instanceID] = a
	return a, nil
}

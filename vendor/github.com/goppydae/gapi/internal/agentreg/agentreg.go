// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package agentreg

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/core/store"
	"github.com/goppydae/gapi/internal/db/graphdb"
	"github.com/goppydae/gapi/internal/safeio"
	"github.com/goppydae/gapi/internal/toposort"
)

type AgentDescription struct {
	ID           string   `json:"id"`
	Path         string   `json:"path"`
	Type         string   `json:"type"`
	Language     string   `json:"language"`
	Version      string   `json:"version"`
	Hash         string   `json:"hash"`
	Capabilities []string `json:"capabilities"`
	Requires     []string `json:"requires"`
	Wants        []string `json:"wants"`
	RequiredBy   []string `json:"required_by"`
	WantedBy     []string `json:"wanted_by"`
	Tags         []string `json:"tags"`
}

type AgentRegistry struct {
	store     store.HybridStore
	verifyKey *ed25519.PublicKey // Optional: if set, verify signatures
	nodeMu    sync.Mutex
}

const agentsBucket = "agents"

// Edge kinds used consistently across Register, syncGraph, and GetDependencies.
// Centralizing these prevents the graph layer from silently diverging: syncGraph
// previously rewrote every edge as "dependency" while Register and
// GetDependencies used "requires"/"wants", so dependency queries returned empty
// after any sync.
const (
	edgeKindRequires = "requires"
	edgeKindWants    = "wants"
)

func NewAgentRegistry(s store.HybridStore, verifyKey *ed25519.PublicKey) (*AgentRegistry, error) {
	r := &AgentRegistry{
		store:     s,
		verifyKey: verifyKey,
	}
	return r, nil
}

func (r *AgentRegistry) Register(agent *AgentDescription) error {
	if agent == nil || strings.TrimSpace(agent.ID) == "" || strings.TrimSpace(agent.Type) == "" {
		return fmt.Errorf("invalid agent: %+v", agent)
	}

	// Register performs a sequence of store writes (primary record, node, edges)
	// that must land as a unit. Serialize concurrent registrations (e.g. from the
	// agent.reload discovery path) so they cannot interleave.
	r.nodeMu.Lock()
	defer r.nodeMu.Unlock()

	// Integrity Check
	if r.verifyKey != nil {
		sigPath := agent.Path + ".sig"
		sigHex, err := safeio.ReadFile(sigPath)
		if err != nil {
			return fmt.Errorf("integrity check failed: missing signature for %s (expected %s): %w", agent.ID, sigPath, err)
		}

		sig, err := hex.DecodeString(string(sigHex))
		if err != nil {
			return fmt.Errorf("integrity check failed: invalid signature hex: %w", err)
		}

		hash, err := crypto.HashFile(agent.Path)
		if err != nil {
			return fmt.Errorf("integrity check failed: could not hash agent file: %w", err)
		}

		if !crypto.Verify(*r.verifyKey, []byte(hash), sig) {
			return fmt.Errorf("integrity check failed: signature verification failed for %s", agent.ID)
		}
	}

	// primary record
	if err := r.store.Set(agentsBucket, agent.ID, agent); err != nil {
		return err
	}

	// graph edges
	if err := r.store.AddNode(graphdb.Node{ID: agent.ID, Type: agent.Type}); err != nil {
		return fmt.Errorf("graph node error: %w", err)
	}

	// Outgoing Dependencies
	for _, dep := range agent.Requires {
		if err := r.store.AddEdge(graphdb.Edge{From: agent.ID, To: dep, Kind: edgeKindRequires}); err != nil {
			return fmt.Errorf("edge requires error: %w", err)
		}
	}
	for _, dep := range agent.Wants {
		if err := r.store.AddEdge(graphdb.Edge{From: agent.ID, To: dep, Kind: edgeKindWants}); err != nil {
			return fmt.Errorf("edge wants error: %w", err)
		}
	}

	// Infer Incoming Dependencies (Reverse)
	// If I (agent.ID) am WantedBy=[foo], it means foo -> (wants) -> I.
	for _, target := range agent.WantedBy {
		// Ensure target node exists? GraphDB might require it, or auto-create.
		// Assuming auto-create or loose consistency for now, or just adding edge.
		if err := r.store.AddEdge(graphdb.Edge{From: target, To: agent.ID, Kind: edgeKindWants}); err != nil {
			return fmt.Errorf("edge wanted_by error: %w", err)
		}
	}
	for _, target := range agent.RequiredBy {
		if err := r.store.AddEdge(graphdb.Edge{From: target, To: agent.ID, Kind: edgeKindRequires}); err != nil {
			return fmt.Errorf("edge required_by error: %w", err)
		}
	}

	return nil
}

func (r *AgentRegistry) Lookup(id string) (*AgentDescription, error) {
	var agent AgentDescription
	err := r.store.Get(agentsBucket, id, &agent)
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

func (r *AgentRegistry) List() ([]*AgentDescription, error) {
	keys, err := r.store.Keys(agentsBucket)
	if err != nil {
		return nil, err
	}

	var agents []*AgentDescription
	for _, k := range keys {
		agent, err := r.Lookup(k)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

func (r *AgentRegistry) GetDependencies(id string) ([]string, error) {
	// Query GraphDB for authoritative dependencies
	// This captures both static 'requires'/'wants' AND dynamic 'wanted_by'/'required_by' edges
	reqs, err := r.store.Neighbors(id, edgeKindRequires)
	if err != nil {
		return nil, fmt.Errorf("neighbors/requires: %w", err)
	}
	wants, err := r.store.Neighbors(id, edgeKindWants)
	if err != nil {
		return nil, fmt.Errorf("neighbors/wants: %w", err)
	}

	seen := make(map[string]struct{})
	var all []string

	for _, e := range reqs {
		if _, ok := seen[e.To]; !ok {
			seen[e.To] = struct{}{}
			all = append(all, e.To)
		}
	}
	for _, e := range wants {
		if _, ok := seen[e.To]; !ok {
			seen[e.To] = struct{}{}
			all = append(all, e.To)
		}
	}
	return all, nil
}

func (r *AgentRegistry) TopologicalSort() ([]string, error) {
	agents, err := r.List()
	if err != nil {
		return nil, err
	}

	// Shared toposort (review R5: one implementation): Requires edges order
	// and cycle-reject; Wants edges order when satisfiable and never block
	// (review R14). Unknown deps (external services) are ignored.
	//
	// WantedBy/RequiredBy fold in as reverse edges, matching what Register
	// already writes into the graph: "X is wanted_by Y" means Y wants X.
	// This sort read only the forward fields for as long as the reverse
	// ones existed, so the graph and the ordering disagreed - the same
	// gap, in the same shape, as core/agentmgr's copy.
	known := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		known[a.ID] = struct{}{}
	}
	hard := make(map[string][]string, len(agents))
	soft := make(map[string][]string, len(agents))
	// Copied, not aliased: the reverse pass appends to these.
	for _, a := range agents {
		hard[a.ID] = append([]string(nil), a.Requires...)
		soft[a.ID] = append([]string(nil), a.Wants...)
	}
	for _, a := range agents {
		for _, target := range a.WantedBy {
			if _, ok := known[target]; ok {
				soft[target] = append(soft[target], a.ID)
			}
		}
		for _, target := range a.RequiredBy {
			if _, ok := known[target]; ok {
				hard[target] = append(hard[target], a.ID)
			}
		}
	}
	order, err := toposort.Sort(hard, soft)
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	// Sync now we know the DAG is valid.
	r.syncGraph(agents)

	return order, nil
}

func (r *AgentRegistry) Close() error {
	return r.store.Close()
}

package agentreg

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/core/store"
	"github.com/goppydae/gapi/internal/db/graphdb"
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

	// Integrity Check
	if r.verifyKey != nil {
		sigPath := agent.Path + ".sig"
		sigHex, err := os.ReadFile(sigPath)
		if err != nil {
			return fmt.Errorf("integrity check failed: missing signature for %s (expected %s): %v", agent.ID, sigPath, err)
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
		if err := r.store.AddEdge(graphdb.Edge{From: agent.ID, To: dep, Kind: "requires"}); err != nil {
			return fmt.Errorf("edge requires error: %w", err)
		}
	}
	for _, dep := range agent.Wants {
		if err := r.store.AddEdge(graphdb.Edge{From: agent.ID, To: dep, Kind: "wants"}); err != nil {
			return fmt.Errorf("edge wants error: %w", err)
		}
	}

	// Infer Incoming Dependencies (Reverse)
	// If I (agent.ID) am WantedBy=[foo], it means foo -> (wants) -> I.
	for _, target := range agent.WantedBy {
		// Ensure target node exists? GraphDB might require it, or auto-create.
		// Assuming auto-create or loose consistency for now, or just adding edge.
		if err := r.store.AddEdge(graphdb.Edge{From: target, To: agent.ID, Kind: "wants"}); err != nil {
			return fmt.Errorf("edge wanted_by error: %w", err)
		}
	}
	for _, target := range agent.RequiredBy {
		if err := r.store.AddEdge(graphdb.Edge{From: target, To: agent.ID, Kind: "requires"}); err != nil {
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
	reqs, err := r.store.Neighbors(id, "requires")
	if err != nil {
		return nil, fmt.Errorf("neighbors/requires: %w", err)
	}
	wants, err := r.store.Neighbors(id, "wants")
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

	// Build adjacency using ONLY hard deps (Requires); Wants to remain soft.
	reqs := make(map[string][]string, len(agents))
	exists := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		exists[a.ID] = struct{}{}
		reqs[a.ID] = append([]string(nil), a.Requires...)
	}

	// Kahn's algorithm (in-memory, no store side effects)
	inDeg := map[string]int{}
	for id := range exists {
		inDeg[id] = 0
	}
	for id, deps := range reqs {
		_ = id
		for _, d := range deps {
			if _, ok := exists[d]; ok {
				inDeg[id]++
			}
		}
	}

	queue := make([]string, 0, len(agents))
	for id, deg := range inDeg {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var order []string
	for len(queue) > 0 {
		// pop last (stack-ish is fine)
		n := len(queue) - 1
		id := queue[n]
		queue = queue[:n]
		order = append(order, id)

		for depOwner, deps := range reqs {
			for _, d := range deps {
				if d == id {
					inDeg[depOwner]--
					if inDeg[depOwner] == 0 {
						queue = append(queue, depOwner)
					}
				}
			}
		}
	}

	if len(order) != len(agents) {
		return nil, fmt.Errorf("cycle detected or missing hard dependency")
	}

	// Sync now we know the DAG is valid.
	r.syncGraph(agents)

	return order, nil
}

func (r *AgentRegistry) Close() error {
	return r.store.Close()
}

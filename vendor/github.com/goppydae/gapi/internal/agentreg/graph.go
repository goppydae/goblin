package agentreg

import "github.com/goppydae/gapi/internal/db/graphdb"

type graphWriter interface {
	ClearGraph() error
	AddNode(graphdb.Node) error
	AddEdge(graphdb.Edge) error
}

// syncGraph rebuilds the graph so it matches exactly what Register writes: the
// same node metadata and the same "requires"/"wants" edge kinds (including the
// reverse edges inferred from RequiredBy/WantedBy). Using the shared edge-kind
// constants keeps GetDependencies' Neighbors queries working after a sync
// instead of finding an empty graph.
func (r *AgentRegistry) syncGraph(agents []*AgentDescription) {
	gw, ok := any(r.store).(graphWriter)
	if !ok {
		return
	}
	_ = gw.ClearGraph()
	for _, a := range agents {
		_ = gw.AddNode(graphdb.Node{ID: a.ID, Type: a.Type})
		for _, dep := range a.Requires {
			_ = gw.AddEdge(graphdb.Edge{From: a.ID, To: dep, Kind: edgeKindRequires})
		}
		for _, dep := range a.Wants {
			_ = gw.AddEdge(graphdb.Edge{From: a.ID, To: dep, Kind: edgeKindWants})
		}
		for _, target := range a.WantedBy {
			_ = gw.AddEdge(graphdb.Edge{From: target, To: a.ID, Kind: edgeKindWants})
		}
		for _, target := range a.RequiredBy {
			_ = gw.AddEdge(graphdb.Edge{From: target, To: a.ID, Kind: edgeKindRequires})
		}
	}
}

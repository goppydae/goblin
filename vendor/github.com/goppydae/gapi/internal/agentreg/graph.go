package agentreg

import "github.com/goppydae/gapi/internal/db/graphdb"

type graphWriter interface {
	ClearGraph() error
	AddNode(graphdb.Node) error
	AddEdge(graphdb.Edge) error
}

func (r *AgentRegistry) syncGraph(agents []*AgentDescription) {
	gw, ok := any(r.store).(graphWriter)
	if !ok {
		return
	}
	_ = gw.ClearGraph()
	for _, a := range agents {
		_ = gw.AddNode(graphdb.Node{ID: a.ID})
		for _, dep := range a.Requires { // hard deps only
			_ = gw.AddEdge(graphdb.Edge{From: a.ID, To: dep, Kind: "dependency"})
		}
	}
}

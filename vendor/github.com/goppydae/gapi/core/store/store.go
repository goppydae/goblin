package store

import (
	"fmt"

	"github.com/goppydae/gapi/internal/db/graphdb"
	"github.com/goppydae/gapi/internal/db/hybriddb"
	"github.com/goppydae/gapi/internal/db/kvdb"
)

type Mode string

const (
	KV     Mode = "kv"
	Graph  Mode = "graph"
	Hybrid Mode = "hybrid"
)

func Open(mode Mode) (Store, error) {
	switch mode {
	case KV:
		return kvdb.New("default")
	case Graph:
		return graphdb.New()
	case Hybrid:
		return hybriddb.New()
	default:
		return nil, fmt.Errorf("unknown store mode: %s", mode)
	}
}

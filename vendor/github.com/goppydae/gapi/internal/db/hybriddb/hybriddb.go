package hybriddb

import (
	"github.com/goppydae/gapi/internal/db/graphdb"
	"github.com/goppydae/gapi/internal/db/kvdb"
)

type DB struct {
	kv    *kvdb.DB
	graph *graphdb.Graph
}

func New() (*DB, error) {
	kv, err := kvdb.New("default", "runtime")
	if err != nil {
		return nil, err
	}

	graph, err := graphdb.New()
	if err != nil {
		kv.Close()
		return nil, err
	}

	return &DB{kv: kv, graph: graph}, nil
}

func (h *DB) Close() error {
	err1 := h.kv.Close()
	err2 := h.graph.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func (h *DB) Set(bucket, key string, value interface{}) error {
	return h.kv.Set(bucket, key, value)
}
func (h *DB) Get(bucket, key string, target interface{}) error {
	return h.kv.Get(bucket, key, target)
}
func (h *DB) Delete(bucket, key string) error {
	return h.kv.Delete(bucket, key)
}
func (h *DB) Keys(bucket string) ([]string, error) {
	return h.kv.Keys(bucket)
}

func (h *DB) AddNode(n graphdb.Node) error {
	return h.graph.AddNode(n)
}
func (h *DB) AddEdge(e graphdb.Edge) error {
	return h.graph.AddEdge(e)
}
func (h *DB) Neighbors(id string, kind string) ([]graphdb.Edge, error) {
	return h.graph.Neighbors(id, kind)
}
func (h *DB) ShortestPath(start, end, kind string, ttl int64) ([]string, int, error) {
	return h.graph.ShortestPath(start, end, kind, ttl)
}
func (h *DB) GetStoredPath(start, end, kind string) (*graphdb.Path, error) {
	return h.graph.GetStoredPath(start, end, kind)
}

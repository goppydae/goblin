package db

import (
	"os"

	"go.etcd.io/bbolt"
)

func NewInMemoryDB() (*bbolt.DB, error) {
	tmpFile, err := os.CreateTemp("", "bolt-inmem-")
	if err != nil {
		return nil, err
	}
	// Unlink the file immediately to simulate memory-only
	err = os.Remove(tmpFile.Name())
	if err != nil {
		return nil, err
	}

	return bbolt.Open(tmpFile.Name(), 0600, &bbolt.Options{Timeout: 0})
}

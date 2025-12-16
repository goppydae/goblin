package kvdb

import (
	"encoding/json"
	"fmt"
	"sync"

	"go.etcd.io/bbolt"

	dbpkg "github.com/goppydae/gapi/internal/db"
)

type DB struct {
	db    *bbolt.DB
	mutex sync.Mutex
}

func New(buckets ...string) (*DB, error) {
	db, err := dbpkg.NewInMemoryDB()
	if err != nil {
		return nil, err
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	})

	if err != nil {
		db.Close()
		return nil, err
	}

	return &DB{db: db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) Set(bucket, key string, value interface{}) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return d.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return fmt.Errorf("create bucket: %w", err)
		}
		return b.Put([]byte(key), data)
	})
}

func (d *DB) Get(bucket, key string, target interface{}) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	return d.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", bucket)
		}
		val := b.Get([]byte(key))
		if val == nil {
			return fmt.Errorf("key not found: %s", key)
		}
		return json.Unmarshal(val, target)
	})
}

func (d *DB) Delete(bucket, key string) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	return d.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", bucket)
		}
		return b.Delete([]byte(key))
	})
}

func (d *DB) Keys(bucket string) ([]string, error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	var keys []string
	err := d.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			// Return empty list instead of error when bucket is not found
			return nil
		}
		return b.ForEach(func(k, _ []byte) error {
			keys = append(keys, string(k))
			return nil
		})
	})
	return keys, err
}

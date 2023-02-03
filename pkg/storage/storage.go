package storage

import (
	"context"
	"encoding/json"
)

type StorageEntry struct {
	Key   string
	Value json.RawMessage
}

type Storage interface {
	Get(ctx context.Context, key string) (*StorageEntry, error)
	Put(ctx context.Context, prefix string, e StorageEntry) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

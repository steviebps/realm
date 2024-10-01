package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type StorageEntry struct {
	Key   string
	Value json.RawMessage
}

type Storage interface {
	// Get retrieves the entry by key from the underlying storage
	Get(ctx context.Context, key string) (*StorageEntry, error)
	// Put adds the entry to the underlying storage
	Put(ctx context.Context, e StorageEntry) error
	// Delete removes the entry by key from the underlying storage
	Delete(ctx context.Context, key string) error
	// List returns a slice of paths at the specified prefix
	List(ctx context.Context, prefix string) ([]string, error)
}

// StorageCreator is a factory function to be used for all storage types
type StorageCreator func(conf map[string]string) (Storage, error)

// StorageOptions is a map of available storage options specified at the server.storage config path
var StorageOptions = map[string]StorageCreator{
	"file":      NewFileStorage,
	"bigcache":  NewBigCacheStorage,
	"cacheable": NewCacheableStorageWithConf,
	"gcs":       NewGCSStorage,
}

// CacheableStorageOptions is a map of available storage options specified at the server.options.cache config path
var CacheableStorageOptions = map[string]StorageCreator{
	"file":     NewFileStorage,
	"bigcache": NewBigCacheStorage,
	"gcs":      NewGCSStorage,
}

func ValidatePath(path string) error {
	if strings.Contains(path, "..") {
		return errors.New("path cannot reference parents")
	}

	return nil
}

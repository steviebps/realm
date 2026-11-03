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
	// Version is the storage-wide revision that last wrote the entry, on
	// backends that track one (see Transactional); it is 0 otherwise.
	Version uint64
}

// Storage is the interface for all storage backends
type Storage interface {
	// Get retrieves the entry by key from the underlying storage
	Get(ctx context.Context, key string) (*StorageEntry, error)
	// Put adds the entry to the underlying storage
	Put(ctx context.Context, e StorageEntry) error
	// Delete removes the entry by key from the underlying storage
	Delete(ctx context.Context, key string) error
	// List returns a slice of paths at the specified prefix
	List(ctx context.Context, prefix string) ([]string, error)
	// Close releases any resources held by the storage backend
	Close(ctx context.Context) error
}

// StorageCreator is a factory function to be used for all storage types
type StorageCreator func(conf map[string]string) (Storage, error)

// StorageOptions is a map of available storage options specified at the server.storage config path
var StorageOptions = map[string]StorageCreator{
	"file":      NewFileStorage,
	"bigcache":  NewBigCacheStorage,
	"cacheable": NewCacheableStorageWithConf,
	"gcs":       NewGCSStorage,
	"s3":        NewS3Storage,
	"boltdb":    NewBoltStorage,
	"postgres":  NewPostgresStorage,
}

// SourcableStorageOptions is a map of available storage options specified at the server.options.cache config path
var SourcableStorageOptions = map[string]StorageCreator{
	"file":     NewFileStorage,
	"bigcache": NewBigCacheStorage,
	"gcs":      NewGCSStorage,
	"s3":       NewS3Storage,
	"boltdb":   NewBoltStorage,
}

// ValidatePath ensures the provided path is valid. no parent references allowed.
func ValidatePath(path string) error {
	if strings.Contains(path, "..") {
		return errors.New("path cannot reference parents")
	}

	return nil
}

// ReservedPrefix is the storage prefix under which realm keeps its own
// metadata (history, catalog, API keys). Chamber APIs reject paths beneath it.
const ReservedPrefix = "/_realm/"

// IsReservedPath reports whether p falls under ReservedPrefix.
func IsReservedPath(p string) bool {
	return strings.HasPrefix(p, ReservedPrefix) || p == strings.TrimSuffix(ReservedPrefix, "/")
}

// ReadJSON loads the entry at key into v. found is false when it does not exist.
func ReadJSON(ctx context.Context, s Storage, key string, v any) (found bool, err error) {
	entry, err := s.Get(ctx, key)
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(entry.Value, v)
}

// WriteJSON stores v as JSON at key.
func WriteJSON(ctx context.Context, s Storage, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Put(ctx, StorageEntry{Key: key, Value: b})
}

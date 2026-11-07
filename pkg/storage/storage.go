package storage

import (
	"context"
	"encoding/json"
	"fmt"
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

// HealthReporter is implemented by storage that can tell whether it works as
// it should beyond answering reads: for example, whether the change feed that
// keeps this server's streams current is keeping up with the database.
// Capabilities like this belong to the innermost storage (see Raw).
type HealthReporter interface {
	// Health returns nil when the storage is healthy, or an error saying
	// what is wrong.
	Health() error
}

// StorageCreator is a factory function to be used for all storage types
type StorageCreator func(conf map[string]string) (Storage, error)

// StorageOptions is a map of available storage options specified at the server.storage config path
var StorageOptions = map[string]StorageCreator{
	"file":     NewFileStorage,
	"bigcache": NewBigCacheStorage,
	"gcs":      NewGCSStorage,
	"s3":       NewS3Storage,
	"boltdb":   NewBoltStorage,
	"postgres": NewPostgresStorage,
}

func init() {
	// registered here because cacheable itself looks up StorageOptions
	StorageOptions["cacheable"] = NewCacheableStorageWithConf
}

// SourcableStorageOptions is a map of available storage options specified at the server.options.cache config path
var SourcableStorageOptions = map[string]StorageCreator{
	"file":     NewFileStorage,
	"bigcache": NewBigCacheStorage,
	"gcs":      NewGCSStorage,
	"s3":       NewS3Storage,
	"boltdb":   NewBoltStorage,
}

// ValidatePath ensures the provided path is valid: no parent references are
// allowed. Its error wraps ErrInvalidPath.
func ValidatePath(path string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("%w: it cannot reference parents (\"..\")", ErrInvalidPath)
	}

	return nil
}

// ReservedPrefix is the storage prefix under which realm keeps its own
// metadata (history, catalog, API keys). Chamber APIs reject paths beneath it.
const ReservedPrefix = "/_realm/"

// IsReservedPath reports whether p falls under ReservedPrefix. Empty and "."
// segments are ignored, so "//_realm/" and "/./_realm" are reserved too: some
// backends clean paths and would otherwise map them onto realm's metadata.
// Case is ignored as well, so "/_REALM/" is reserved: on a case-insensitive
// filesystem (the default on macOS and Windows) the file backend maps it onto
// the same directory as "/_realm/".
func IsReservedPath(p string) bool {
	var b strings.Builder
	b.WriteByte('/')
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." {
			continue
		}
		b.WriteString(seg)
		b.WriteByte('/')
		if b.Len() >= len(ReservedPrefix) {
			break
		}
	}
	s := b.String()
	return len(s) >= len(ReservedPrefix) && strings.EqualFold(s[:len(ReservedPrefix)], ReservedPrefix)
}

// ReadJSON loads the entry at key into v. found is false when it does not exist.
func ReadJSON(ctx context.Context, s Storage, key string, v any) (found bool, err error) {
	entry, err := getOptional(ctx, s, key)
	if entry == nil || err != nil {
		return false, err
	}
	return true, json.Unmarshal(entry.Value, v)
}

// getOptional returns the entry at key, or nil when it does not exist.
func getOptional(ctx context.Context, s Storage, key string) (*StorageEntry, error) {
	return optional(s.Get(ctx, key))
}

// optional passes on the result of a read, with a missing entry as nil.
func optional(entry *StorageEntry, err error) (*StorageEntry, error) {
	if isNotFoundErr(err) {
		return nil, nil
	}
	return entry, err
}

// Unwrapper is implemented by storage wrappers that transform reads (such as
// inheritance) and can expose the storage they wrap.
type Unwrapper interface {
	Unwrap() Storage
}

// Raw returns the innermost storage beneath any wrappers that transform
// reads, which holds entries exactly as written. Capabilities such as
// Transactional and Watchable belong to it.
func Raw(s Storage) Storage {
	for {
		u, ok := s.(Unwrapper)
		if !ok {
			return s
		}
		s = u.Unwrap()
	}
}

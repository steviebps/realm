package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hashicorp/go-hclog"
)

type StorageEntry struct {
	Key   string
	Value json.RawMessage
}

type Storage interface {
	Get(ctx context.Context, key string) (*StorageEntry, error)
	Put(ctx context.Context, e StorageEntry) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

type StorageCreator func(conf map[string]string, logger hclog.Logger) (Storage, error)

var StorageOptions = map[string]StorageCreator{
	"file":      NewFileStorage,
	"bigcache":  NewBigCacheStorage,
	"cacheable": NewCacheableStorageWithConf,
}

var CacheableStorageOptions = map[string]StorageCreator{
	"file":     NewFileStorage,
	"bigcache": NewBigCacheStorage,
}

func ValidatePath(path string) error {
	switch {
	case strings.Contains(path, ".."):
		return errors.New("path cannot reference parents")
	}

	return nil
}

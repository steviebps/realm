package storage

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-hclog"
)

// CacheableStorage is a wrapper storage used for providing a write-through cache.
// This allows any storage type to be used on top, however, in-memory storage types are recommended to reduce latency.
// Size and implementation details of the cache layer will be up to the consumer.
type CacheableStorage struct {
	cache  Storage
	source Storage
	logger hclog.Logger
}

var (
	_ Storage = (*CacheableStorage)(nil)
)

// NewCacheableStorage returns a write-through cacheable storage.
func NewCacheableStorage(cache Storage, source Storage, logger hclog.Logger) (*CacheableStorage, error) {
	if cache == nil || source == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}

	return &CacheableStorage{
		cache:  cache,
		source: source,
		logger: logger.Named("cacheable"),
	}, nil
}

func (c *CacheableStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	c.logger.Debug("get operation", "logicalPath", logicalPath)

	entry, err := c.cache.Get(ctx, logicalPath)
	if err != nil {
		c.logger.Error(err.Error())
	}

	if entry != nil {
		return entry, err
	}

	entry, err = c.source.Get(ctx, logicalPath)
	if err != nil {
		c.logger.Error(err.Error())
		return nil, err
	}

	if entry != nil {
		c.cache.Put(ctx, *entry)
	}

	return entry, nil
}

func (c *CacheableStorage) Put(ctx context.Context, e StorageEntry) error {
	c.logger.Debug("put operation", "logicalPath", e.Key)

	err := c.source.Put(ctx, e)
	if err == nil {
		c.cache.Put(ctx, e)
	}

	return c.cache.Put(ctx, e)
}

func (c *CacheableStorage) Delete(ctx context.Context, logicalPath string) error {
	c.logger.Debug("delete operation", "logicalPath", logicalPath)

	err := c.source.Delete(ctx, logicalPath)
	if err == nil {
		c.cache.Delete(ctx, logicalPath)
	}

	return err
}

func (c *CacheableStorage) List(ctx context.Context, prefix string) ([]string, error) {
	c.logger.Debug("list operation", "prefix", prefix)
	return c.source.List(ctx, prefix)
}

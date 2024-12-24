package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/go-hclog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// CacheableStorage is a wrapper storage used for providing a write-through cache.
// This allows any storage type to be used on top, however, in-memory storage types are recommended to reduce latency.
// Size and implementation details of the cache layer will be up to the consumer.
type CacheableStorage struct {
	cache  Storage
	source Storage
	tracer trace.Tracer
}

var (
	_ Storage = (*CacheableStorage)(nil)
)

// NewCacheableStorage returns a write-through cacheable storage.
func NewCacheableStorage(cache Storage, source Storage) (Storage, error) {
	if cache == nil || source == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}

	return &CacheableStorage{
		cache:  cache,
		source: source,
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

func NewCacheableStorageWithConf(conf map[string]string) (Storage, error) {
	cache, ok := conf["cache"]
	if !ok || cache == "" {
		cache = "bigcache"
	}
	source := conf["source"]
	if source == "" {
		return nil, fmt.Errorf("'source' option for cacheable must be set")
	}

	sourceCreator, exists := CacheableStorageOptions[source]
	if !exists {
		return nil, fmt.Errorf("storage type %q does not exist", source)
	}
	srcStg, err := sourceCreator(conf)
	if err != nil {
		return nil, err
	}

	cacheCreator, exists := CacheableStorageOptions[cache]
	if !exists {
		return nil, fmt.Errorf("storage type %q does not exist", cache)
	}
	cacheStg, err := cacheCreator(conf)
	if err != nil {
		return nil, err
	}

	return NewCacheableStorage(cacheStg, srcStg)
}

func (c *CacheableStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	logger := hclog.FromContext(ctx).ResetNamed("cacheable")
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Get", trace.WithAttributes(attribute.String("realm.cacheable.logicalPath", logicalPath)))
	defer span.End()
	logger.Debug("get operation", "logicalPath", logicalPath)

	entry, err := c.cache.Get(ctx, logicalPath)
	if err != nil {
		span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "cache")))
		var nfError *NotFoundError
		// cache layer is expected to have missing records so let's only log other errors
		if errors.As(err, &nfError) {
			logger.Debug("cache", "miss", err.Error())
		} else {
			logger.Error("cache", "error", err.Error())
		}
	}

	if entry != nil {
		return entry, err
	}

	span.SetAttributes(attribute.Bool("realm.cacheable.cacheMiss", true))
	entry, err = c.source.Get(ctx, logicalPath)
	if err != nil {
		span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "source")))
		logger.Error("source", "error", err.Error())
		return nil, err
	}

	if entry != nil {
		c.cache.Put(ctx, *entry)
	}

	return entry, nil
}

func (c *CacheableStorage) Put(ctx context.Context, e StorageEntry) error {
	logger := hclog.FromContext(ctx).ResetNamed("cacheable")
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Put", trace.WithAttributes(attribute.String("realm.cacheable.entry.key", e.Key)))
	defer span.End()
	logger.Debug("put operation", "logicalPath", e.Key)

	err := c.source.Put(ctx, e)
	if err == nil {
		c.cache.Put(ctx, e)
	} else {
		span.RecordError(err)
	}

	return err
}

func (c *CacheableStorage) Delete(ctx context.Context, logicalPath string) error {
	logger := hclog.FromContext(ctx).ResetNamed("cacheable")
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Delete", trace.WithAttributes(attribute.String("realm.cacheable.logicalPath", logicalPath)))
	defer span.End()
	logger.Debug("delete operation", "logicalPath", logicalPath)

	sourceErr := c.source.Delete(ctx, logicalPath)
	if sourceErr != nil {
		span.RecordError(sourceErr, trace.WithAttributes(attribute.String("realm.cacheable.origin", "source")))
	}
	cacheErr := c.cache.Delete(ctx, logicalPath)
	if cacheErr != nil {
		span.RecordError(cacheErr, trace.WithAttributes(attribute.String("realm.cacheable.origin", "cache")))
	}
	return errors.Join(sourceErr, cacheErr)
}

func (c *CacheableStorage) List(ctx context.Context, prefix string) ([]string, error) {
	logger := hclog.FromContext(ctx).ResetNamed("cacheable")
	ctx, span := c.tracer.Start(ctx, "CacheableStorage List", trace.WithAttributes(attribute.String("realm.cacheable.prefix", prefix)))
	defer span.End()
	logger.Debug("list operation", "prefix", prefix)
	return c.source.List(ctx, prefix)
}

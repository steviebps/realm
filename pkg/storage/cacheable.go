package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/steviebps/realm/helper/logging"
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

	sourceCreator, exists := SourcableStorageOptions[source]
	if !exists {
		return nil, fmt.Errorf("storage type %q does not exist", source)
	}
	srcStg, err := sourceCreator(conf)
	if err != nil {
		return nil, err
	}

	cacheCreator, exists := SourcableStorageOptions[cache]
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
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Get", trace.WithAttributes(attribute.String("realm.cacheable.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msgf("get operation with cache layer %T", c.cache)

	entry, err := c.cache.Get(ctx, logicalPath)
	if err != nil {
		span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "cache")))
		var nfError *NotFoundError
		// cache layer is expected to have missing records so let's only log other errors
		if errors.As(err, &nfError) {
			logger.DebugCtx(ctx).Str("error", err.Error()).Msg("cache miss")
			span.AddEvent("realm.cacheable.cacheMiss")
		} else {
			logger.DebugCtx(ctx).Str("error", err.Error()).Msg("cache error")
		}
	}

	if entry != nil {
		span.AddEvent("realm.cacheable.cacheHit")
		return entry, err
	}

	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msgf("get operation with source layer: %T", c.source)
	entry, err = c.source.Get(ctx, logicalPath)
	if err != nil {
		span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "source")))
		logger.ErrorCtx(ctx).Str("error", err.Error()).Msg("source error")
		return nil, err
	}

	if entry != nil {
		logger.DebugCtx(ctx).Str("entry.Key", entry.Key).Msgf("value exists in source, inserting value into cache layer")
		err := c.cache.Put(ctx, *entry)

		// if cache put fails, we log the error but do not return as value has been successfully retrieved from source
		if err != nil {
			span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "cache")))
			logger.ErrorCtx(ctx).Str("error", err.Error()).Msg("failed to write to cache")
		} else {
			span.AddEvent("realm.cacheable.cacheWrite")
		}

	}

	return entry, nil
}

func (c *CacheableStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Put", trace.WithAttributes(attribute.String("realm.cacheable.entry.key", e.Key)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	err := c.source.Put(ctx, e)
	if err == nil {
		c.cache.Put(ctx, e)
	} else {
		span.RecordError(err)
	}

	return err
}

func (c *CacheableStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Delete", trace.WithAttributes(attribute.String("realm.cacheable.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

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
	ctx, span := c.tracer.Start(ctx, "CacheableStorage List", trace.WithAttributes(attribute.String("realm.cacheable.prefix", prefix)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")
	return c.source.List(ctx, prefix)
}

func (c *CacheableStorage) Close(ctx context.Context) error {
	if err := c.cache.Close(ctx); err != nil {
		return err
	}
	return c.source.Close(ctx)
}

package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// CacheableStorage reads through a cache in front of a source. Any storage
// type can be the cache, but an in-memory one (bigcache) is recommended: the
// cache is consulted on every read and updated under a lock.
//
// Writes go to the source, then drop the key from the cache, whether or not
// the write succeeded (a write that failed with a timeout may still have
// landed); the next read fills the cache from the source. A fill that read the
// source before a write finished is not stored, so the cache never goes back
// to an older value. When the cache cannot drop a key, reads of it bypass the
// cache until a later attempt succeeds. Update reads the source directly (see
// UpdateReader), so a read-modify-write never starts from a cached copy.
//
// Over a Transactional source (boltdb), NewCacheableStorage returns storage
// that is Transactional too: Txn applies to the source and then drops each
// written key from the cache, as Put and Delete do. Either way, the entries
// cacheable reads carry no Version, since the cache may answer, and one served
// from a versioned cache (boltdb) would carry the cache's revision rather than
// the source's. ReadForUpdate's carry the source's, which is all Update needs.
type CacheableStorage struct {
	cache  Storage
	source Storage
	tracer trace.Tracer

	// mu orders cache fills and invalidations. gen counts finished write
	// attempts: a fill is stored only if gen has not moved since it began
	// reading the source.
	mu  sync.Mutex
	gen uint64
	// stale holds the keys whose cache entry could not be dropped after a
	// write; their reads bypass the cache.
	stale map[string]struct{}
}

var (
	_ Storage      = (*CacheableStorage)(nil)
	_ Locker       = (*CacheableStorage)(nil)
	_ UpdateReader = (*CacheableStorage)(nil)
	_ KeyValidator = (*CacheableStorage)(nil)
)

// NewCacheableStorage returns storage that caches source's entries in cache.
// It is Transactional when source is.
func NewCacheableStorage(cache Storage, source Storage) (Storage, error) {
	if cache == nil || source == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}

	c := &CacheableStorage{
		cache:  cache,
		source: source,
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}
	if t, ok := source.(Transactional); ok {
		return &transactionalCacheable{CacheableStorage: c, source: t}, nil
	}
	return c, nil
}

// transactionalCacheable is a CacheableStorage over a Transactional source.
type transactionalCacheable struct {
	*CacheableStorage
	source Transactional
}

var _ Transactional = (*transactionalCacheable)(nil)

// Txn applies to the source, then drops every key it may have written from
// the cache, unless it failed a check and so wrote nothing.
func (c *transactionalCacheable) Txn(ctx context.Context, checks []Check, ops []Op) error {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Txn", trace.WithAttributes(attribute.Int("realm.cacheable.checks", len(checks)), attribute.Int("realm.cacheable.ops", len(ops))))
	defer span.End()

	err := c.source.Txn(ctx, checks, ops)
	if errors.Is(err, ErrConflict) {
		return err
	}
	if err != nil {
		recordError(span, err)
	}
	for _, op := range ops {
		c.invalidate(ctx, span, op.Key)
	}
	return err
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
	if cache == "boltdb" && source == "boltdb" {
		// both would open the same database file, which bbolt locks
		return nil, fmt.Errorf("cacheable cannot use boltdb as both cache and source")
	}

	sourceCreator, err := sourcable(source)
	if err != nil {
		return nil, err
	}
	cacheCreator, err := cacheTier(cache)
	if err != nil {
		return nil, err
	}
	srcStg, err := sourceCreator(conf)
	if err != nil {
		return nil, err
	}
	cacheStg, err := cacheCreator(conf)
	if err != nil {
		_ = srcStg.Close(context.Background())
		return nil, err
	}

	return NewCacheableStorage(cacheStg, srcStg)
}

// sourcable returns the creator of a storage type that can be cached or act as
// a cache. Backends shared by several servers (postgres) are not: another
// server's write would leave this server's cache stale.
func sourcable(name string) (StorageCreator, error) {
	if create, ok := SourcableStorageOptions[name]; ok {
		return create, nil
	}
	if _, ok := StorageOptions[name]; ok {
		return nil, fmt.Errorf("storage type %q cannot be used with cacheable", name)
	}
	return nil, fmt.Errorf("storage type %q does not exist", name)
}

// cacheTier returns the creator of a storage type acting as cacheable's
// cache. bigcache keeps an expiry there (see newBigCacheTier), unlike as
// primary storage.
func cacheTier(name string) (StorageCreator, error) {
	if name == "bigcache" {
		return newBigCacheTier, nil
	}
	return sourcable(name)
}

func (c *CacheableStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Get", trace.WithAttributes(attribute.String("realm.cacheable.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msgf("get operation with cache layer %T", c.cache)

	gen, cached := c.beginRead(ctx, logicalPath)
	if cached {
		entry, err := c.cache.Get(ctx, logicalPath)
		if err != nil {
			span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "cache")))
			// cache layer is expected to have missing records so let's only log other errors
			if isNotFoundErr(err) {
				logger.DebugCtx(ctx).Str("error", err.Error()).Msg("cache miss")
				span.AddEvent("realm.cacheable.cacheMiss")
			} else {
				logger.DebugCtx(ctx).Str("error", err.Error()).Msg("cache error")
			}
		}
		if entry != nil {
			span.AddEvent("realm.cacheable.cacheHit")
			return unversioned(entry), nil
		}
	} else {
		span.AddEvent("realm.cacheable.cacheBypassed")
	}

	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msgf("get operation with source layer: %T", c.source)
	entry, err := c.source.Get(ctx, logicalPath)
	if err != nil {
		span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "source")))
		if !isNotFoundErr(err) {
			logger.ErrorCtx(ctx).Str("error", err.Error()).Msg("source error")
		}
		return nil, err
	}
	if cached {
		c.fill(ctx, span, gen, logicalPath, entry)
	}
	return unversioned(entry), nil
}

// unversioned returns a copy of entry without its Version (see
// CacheableStorage).
func unversioned(entry *StorageEntry) *StorageEntry {
	e := *entry
	e.Version = 0
	return &e
}

// beginRead returns the write generation a read of key starts at, and whether
// the read may use the cache: not while key's cache entry is stale, unless
// dropping it succeeds now.
func (c *CacheableStorage) beginRead(ctx context.Context, key string) (gen uint64, cached bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, stale := c.stale[key]; stale && c.invalidateLocked(ctx, key) != nil {
		return c.gen, false
	}
	return c.gen, true
}

// fill stores entry, read from the source at generation gen, in the cache,
// unless a write attempt finished since: the entry may predate that write.
func (c *CacheableStorage) fill(ctx context.Context, span trace.Span, gen uint64, key string, entry *StorageEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		span.AddEvent("realm.cacheable.fillSkipped")
		return
	}
	if _, stale := c.stale[key]; stale {
		return
	}
	if err := c.cache.Put(ctx, *entry); err != nil {
		// the read succeeded; the next one tries the cache again
		span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "cache")))
		logging.Ctx(ctx).ErrorCtx(ctx).Str("error", err.Error()).Msg("failed to write to cache")
		return
	}
	span.AddEvent("realm.cacheable.cacheWrite")
}

// invalidate drops key from the cache after a write attempt on the source,
// and stops fills that began before it from being stored. It runs even when
// ctx is done, since the write may have landed regardless.
func (c *CacheableStorage) invalidate(ctx context.Context, span trace.Span, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if err := c.invalidateLocked(ctx, key); err != nil {
		span.RecordError(err, trace.WithAttributes(attribute.String("realm.cacheable.origin", "cache")))
		logging.Ctx(ctx).ErrorCtx(ctx).Str("key", key).Str("error", err.Error()).Msg("could not drop a changed entry from the cache; reading it from the source until that succeeds")
	}
}

// invalidateLocked drops key from the cache, marking it stale when that
// fails. c.mu must be held.
func (c *CacheableStorage) invalidateLocked(ctx context.Context, key string) error {
	err := c.cache.Delete(context.WithoutCancel(ctx), key)
	if err == nil || isNotFoundErr(err) {
		delete(c.stale, key)
		return nil
	}
	if c.stale == nil {
		c.stale = map[string]struct{}{}
	}
	c.stale[key] = struct{}{}
	return err
}

func (c *CacheableStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Put", trace.WithAttributes(attribute.String("realm.cacheable.entry.key", e.Key)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	err := c.source.Put(ctx, e)
	if err != nil {
		recordError(span, err)
	}
	c.invalidate(ctx, span, e.Key)
	return err
}

func (c *CacheableStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage Delete", trace.WithAttributes(attribute.String("realm.cacheable.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	// The source decides whether the entry existed: the cache is expected to
	// miss (an entry is evicted, or was never read), and Update treats "not
	// found" as "already gone".
	err := c.source.Delete(ctx, logicalPath)
	if err != nil {
		recordError(span, err)
	}
	c.invalidate(ctx, span, logicalPath)
	return err
}

func (c *CacheableStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage List", trace.WithAttributes(attribute.String("realm.cacheable.prefix", prefix)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")
	return c.source.List(ctx, prefix)
}

// ReadForUpdate reads key from the source, so that Update never starts from
// a cached copy, which may predate a write made by another process sharing
// the source. It does not fill the cache.
func (c *CacheableStorage) ReadForUpdate(ctx context.Context, key string) (*StorageEntry, error) {
	ctx, span := c.tracer.Start(ctx, "CacheableStorage ReadForUpdate", trace.WithAttributes(attribute.String("realm.cacheable.logicalPath", key)))
	defer span.End()

	entry, err := c.source.Get(ctx, key)
	if err != nil {
		recordError(span, err)
	}
	return entry, err
}

// ValidateKey checks key with the source's KeyValidator, if it has one.
func (c *CacheableStorage) ValidateKey(key string) error {
	if v, ok := c.source.(KeyValidator); ok {
		return v.ValidateKey(key)
	}
	return nil
}

// LockUpdate takes the source's lock when it has one, so Updates through the
// cache are serialized with other processes using the source.
func (c *CacheableStorage) LockUpdate(ctx context.Context, keys []string) (func(), error) {
	if l, ok := c.source.(Locker); ok {
		return l.LockUpdate(ctx, keys)
	}
	return func() {}, nil
}

func (c *CacheableStorage) Close(ctx context.Context) error {
	return errors.Join(c.cache.Close(ctx), c.source.Close(ctx))
}

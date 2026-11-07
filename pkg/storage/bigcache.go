package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type BigCacheStorage struct {
	underlying *bigcache.BigCache
	tracer     trace.Tracer
	// lifeWindow is how long entries are kept after they are written
	// (bigCacheNoExpiry: until they are deleted).
	lifeWindow time.Duration

	// warning, when set, is logged once, with the logger of the first
	// operation's context (see warn).
	warning  string
	warnOnce sync.Once
}

var (
	_ Storage = (*BigCacheStorage)(nil)
)

const (
	bigCacheEntryKey      string = "bc"
	DefaultBigCacheShards int    = 1024
	// DefaultBigCacheLifeWindow is how long cacheable's bigcache cache keeps
	// an entry when life_window is not set. Primary bigcache storage keeps
	// entries until they are deleted.
	DefaultBigCacheLifeWindow time.Duration = 5 * time.Minute
	// DefaultBigCacheCleanWindow is how often expired entries are removed
	// when entries expire and clean_window is not set.
	DefaultBigCacheCleanWindow time.Duration = 5 * time.Second
)

// bigCacheNoExpiry is the life window of entries that are kept until they are
// deleted. bigcache expires every entry eventually, so it is given a window
// far longer than any process runs.
const bigCacheNoExpiry = time.Duration(math.MaxInt64)

// NewBigCacheStorage creates in-memory storage. Options:
//
//	shards        number of shards, a power of two (default 1024)
//	life_window   how long an entry is kept after it is written, as a
//	              duration such as "24h" (or integer nanoseconds); unset or
//	              "0" keeps entries until they are deleted
//	clean_window  how often expired entries are removed (default 5s when
//	              entries expire)
//
// Entries expire by when they were written, however often they are read, so
// a life_window loses chambers and API keys that were not rewritten within
// it; the storage logs a warning when one is set. Everything is lost when the
// process stops.
func NewBigCacheStorage(conf map[string]string) (Storage, error) {
	return newBigCache(conf, 0, true)
}

// newBigCacheTier creates bigcache storage for cacheable's cache. Its entries
// expire after DefaultBigCacheLifeWindow unless life_window says otherwise, so
// changes made by other processes are read again eventually.
func newBigCacheTier(conf map[string]string) (Storage, error) {
	return newBigCache(conf, DefaultBigCacheLifeWindow, false)
}

// newBigCache creates bigcache storage whose entries expire after defaultLife
// (zero: never) unless life_window is set. primary storage warns when its
// entries expire.
func newBigCache(conf map[string]string, defaultLife time.Duration, primary bool) (Storage, error) {
	cfg, warning, err := bigCacheConfig(conf, defaultLife, primary)
	if err != nil {
		return nil, err
	}
	cache, err := bigcache.New(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	return &BigCacheStorage{
		underlying: cache,
		tracer:     otel.Tracer("github.com/steviebps/realm"),
		lifeWindow: cfg.LifeWindow,
		warning:    warning,
	}, nil
}

// bigCacheConfig returns the bigcache configuration for conf (see
// newBigCache), and the warning that primary storage logs when its entries
// expire.
func bigCacheConfig(conf map[string]string, defaultLife time.Duration, primary bool) (cfg bigcache.Config, warning string, err error) {
	shards := DefaultBigCacheShards
	if s := conf["shards"]; s != "" {
		if shards, err = strconv.Atoi(s); err != nil {
			return cfg, "", fmt.Errorf("failed to parse shards: %w", err)
		}
	}

	lifeWindow, lifeSet, err := durationOption(conf, "life_window")
	if err != nil {
		return cfg, "", err
	}
	if !lifeSet {
		lifeWindow = defaultLife
	}
	if lifeWindow > 0 && lifeWindow < time.Second {
		// bigcache counts whole seconds, so a shorter window expires entries
		// on the next write
		return cfg, "", fmt.Errorf("life_window %v is too short: use 0 to keep entries, or at least 1s", lifeWindow)
	}
	cleanWindow, cleanSet, err := durationOption(conf, "clean_window")
	if err != nil {
		return cfg, "", err
	}

	if lifeWindow == 0 {
		lifeWindow = bigCacheNoExpiry
		if !cleanSet {
			// nothing expires, so there is nothing to clean up
			cleanWindow = 0
		}
	} else {
		if !cleanSet {
			cleanWindow = DefaultBigCacheCleanWindow
		}
		if primary {
			warning = fmt.Sprintf("bigcache storage deletes every entry (chambers, history, API keys) %v after it was last written, because life_window is set; remove life_window to keep entries until the server stops", lifeWindow)
		}
	}

	return bigcache.Config{
		// number of shards (must be a power of 2)
		Shards: shards,
		// time after which an entry is evicted, counted from when it was written
		LifeWindow: lifeWindow,
		// interval between removals of expired entries; <= 0 removes them
		// only as new entries are written
		CleanWindow: cleanWindow,
	}, warning, nil
}

// durationOption parses conf[name] as a duration such as "5m" or, as older
// configs wrote it, integer nanoseconds. set is false when it is empty.
func durationOption(conf map[string]string, name string) (d time.Duration, set bool, err error) {
	s := strings.TrimSpace(conf[name])
	if s == "" {
		return 0, false, nil
	}
	if d, err = time.ParseDuration(s); err != nil {
		n, nerr := strconv.ParseInt(s, 10, 64)
		if nerr != nil {
			return 0, false, fmt.Errorf("invalid %s %q: want a duration such as \"5m\"", name, s)
		}
		d = time.Duration(n)
	}
	if d < 0 {
		return 0, false, fmt.Errorf("invalid %s %q: must not be negative", name, s)
	}
	return d, true, nil
}

// warn logs the storage's warning, once. Storage creators have no logger, so
// it is logged with the logger of the first operation's context, which on a
// server is the startup read of the root chamber.
func (f *BigCacheStorage) warn(ctx context.Context) {
	if f.warning == "" {
		return
	}
	f.warnOnce.Do(func() {
		logging.Ctx(ctx).WarnCtx(ctx).Msg(f.warning)
	})
}

func (f *BigCacheStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage Get", trace.WithAttributes(attribute.String("realm.bigcache.logicalPath", logicalPath)))
	defer span.End()
	f.warn(ctx)

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return nil, err
	}

	dir, key := f.expandPath(logicalPath + bigCacheEntryKey)
	b, err := f.underlying.Get(path.Join(dir, key))
	if err != nil {
		recordError(span, err)
		if errors.Is(err, bigcache.ErrEntryNotFound) {
			return nil, &NotFoundError{logicalPath}
		}
		return nil, err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: b}, nil
}

func (f *BigCacheStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage Put", trace.WithAttributes(attribute.String("realm.bigcache.entry.key", e.Key)))
	defer span.End()
	f.warn(ctx)

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		recordError(span, err)
		return err
	}
	dir, key := f.expandPath(e.Key + bigCacheEntryKey)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	return f.underlying.Set(path.Join(dir, key), e.Value)
}

func (f *BigCacheStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage Delete", trace.WithAttributes(attribute.String("realm.bigcache.logicalPath", logicalPath)))
	defer span.End()
	f.warn(ctx)

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return err
	}
	dir, key := f.expandPath(logicalPath + bigCacheEntryKey)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	if err := f.underlying.Delete(path.Join(dir, key)); err != nil {
		recordError(span, err)
		if errors.Is(err, bigcache.ErrEntryNotFound) {
			return &NotFoundError{logicalPath}
		}
		return err
	}

	return nil
}

func (f *BigCacheStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage List", trace.WithAttributes(attribute.String("realm.bigcache.prefix", prefix)))
	defer span.End()
	f.warn(ctx)

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	// Keys look like "/a/b/_bc". Like the other backends, report the entry at
	// the prefix itself as "." and each immediate child as "name/", once.
	prefix = utils.EnsureTrailingSlash(prefix)
	set := make(map[string]struct{})
	iterator := f.underlying.Iterator()
	for iterator.SetNext() {
		record, err := iterator.Value()
		if err != nil {
			return nil, err
		}
		rest, ok := strings.CutPrefix(record.Key(), prefix)
		if !ok {
			continue
		}
		if child, _, nested := strings.Cut(rest, "/"); nested {
			set[child+"/"] = struct{}{}
		} else {
			set["."] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	if len(names) > 0 {
		slices.Sort(names)
	}

	return names, nil
}

func (f *BigCacheStorage) Close(ctx context.Context) error {
	return f.underlying.Close()
}

// expandPath splits a logical key into its directory and entry name. Keys are
// logical, slash-separated paths (not OS file paths), so they are built with
// the path package to stay identical across platforms.
func (f *BigCacheStorage) expandPath(k string) (string, string) {
	return path.Dir(k), "_" + path.Base(k)
}

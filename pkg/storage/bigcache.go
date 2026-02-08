package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type BigCacheStorage struct {
	underlying *bigcache.BigCache
	tracer     trace.Tracer
}

var (
	_ Storage = (*BigCacheStorage)(nil)
)

const (
	bigCacheEntryKey           string        = "bc"
	DefaultBigCacheShards      int           = 1024
	DefaultBigCacheLifeWindow  time.Duration = 5 * time.Minute
	DefaultBigCacheCleanWindow time.Duration = 5 * time.Second
)

// NewBigCacheStorage creates a new BigCache storage backend
func NewBigCacheStorage(config map[string]string) (Storage, error) {
	// defaults
	shards := DefaultBigCacheShards
	lifeWindow := DefaultBigCacheLifeWindow
	cleanWindow := DefaultBigCacheCleanWindow

	var err error
	shardsStr := config["shards"]
	if shardsStr != "" {
		shards, err = strconv.Atoi(shardsStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse shards: %w", err)
		}
	}

	lifeStr := config["life_window"]
	if lifeStr != "" {
		parsedLife, err := strconv.ParseInt(lifeStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse life_window: %w", err)
		}
		lifeWindow = time.Duration(parsedLife)
	}

	cleanStr := config["clean_window"]
	if cleanStr != "" {
		parsedClean, err := strconv.ParseInt(cleanStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse clean_window: %w", err)
		}
		cleanWindow = time.Duration(parsedClean)
	}

	cConfig := bigcache.Config{
		// number of shards (must be a power of 2)
		Shards: shards,

		// time after which entry can be evicted
		LifeWindow: lifeWindow,

		// Interval between removing expired entries (clean up).
		// If set to <= 0 then no action is performed.
		// Setting to < 1 second is counterproductive — bigcache has a one second resolution.
		CleanWindow: cleanWindow,
	}
	cache, err := bigcache.New(context.Background(), cConfig)
	if err != nil {
		return nil, err
	}
	tracer := otel.Tracer("github.com/steviebps/realm")

	return &BigCacheStorage{
		underlying: cache,
		tracer:     tracer,
	}, nil
}

func (f *BigCacheStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage Get", trace.WithAttributes(attribute.String("realm.bigcache.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return nil, err
	}

	path, key := f.expandPath(logicalPath + bigCacheEntryKey)
	b, err := f.underlying.Get(filepath.Join(path, key))
	if err != nil {
		span.RecordError(err)
		if errors.Is(err, bigcache.ErrEntryNotFound) {
			return nil, &NotFoundError{logicalPath}
		}
		return nil, err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: b}, nil
}

func (f *BigCacheStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage Put", trace.WithAttributes(attribute.String("realm.bigcache.entry.key", e.Key)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		span.RecordError(err)
		return err
	}
	path, key := f.expandPath(e.Key + bigCacheEntryKey)

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return ctx.Err()
	default:
	}

	return f.underlying.Set(filepath.Join(path, key), e.Value)
}

func (f *BigCacheStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage Delete", trace.WithAttributes(attribute.String("realm.bigcache.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return err
	}
	path, key := f.expandPath(logicalPath + bigCacheEntryKey)

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return ctx.Err()
	default:
	}

	return f.underlying.Delete(filepath.Join(path, key))
}

func (f *BigCacheStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := f.tracer.Start(ctx, "BigCacheStorage List", trace.WithAttributes(attribute.String("realm.bigcache.prefix", prefix)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		span.RecordError(err)
		return nil, err
	}

	names := make([]string, 0)
	iterator := f.underlying.Iterator()
	for iterator.SetNext() {
		record, err := iterator.Value()
		if err != nil {
			return names, err
		}
		key := record.Key()
		if strings.HasPrefix(key, prefix) {
			names = append(names, filepath.Dir(strings.TrimPrefix(key, prefix)))
		}
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return nil, ctx.Err()
	default:
	}

	if len(names) > 0 {
		sort.Strings(names)
	}

	return names, nil
}

func (f *BigCacheStorage) Close(ctx context.Context) error {
	return f.underlying.Close()
}

func (f *BigCacheStorage) expandPath(k string) (string, string) {
	key := filepath.Base(k)
	path := filepath.Dir(k)
	return path, "_" + key
}

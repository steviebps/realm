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
	"github.com/hashicorp/go-hclog"
)

type BigCacheStorage struct {
	underlying *bigcache.BigCache
	logger     hclog.Logger
}

var (
	_ Storage = (*BigCacheStorage)(nil)
)

func NewBigCacheStorage(config map[string]string, logger hclog.Logger) (Storage, error) {
	// defaults
	var shards int = 64
	lifeWindow := int64(10 * time.Minute)
	cleanWindow := int64(5 * time.Minute)

	var err error
	shardsStr := config["shards"]
	if shardsStr != "" {
		shards, err = strconv.Atoi(shardsStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse shards: %w", err)
		}
	}

	lifeStr := config["life_window"]
	if shardsStr != "" {
		lifeWindow, err = strconv.ParseInt(lifeStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse life_window: %w", err)
		}
	}

	cleanStr := config["clean_window"]
	if shardsStr != "" {
		cleanWindow, err = strconv.ParseInt(cleanStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse clean_window: %w", err)
		}
	}

	cConfig := bigcache.Config{
		// number of shards (must be a power of 2)
		Shards: shards,

		// time after which entry can be evicted
		LifeWindow: time.Duration(lifeWindow),

		// Interval between removing expired entries (clean up).
		// If set to <= 0 then no action is performed.
		// Setting to < 1 second is counterproductive — bigcache has a one second resolution.
		CleanWindow: time.Duration(cleanWindow),
	}
	cache, err := bigcache.New(context.Background(), cConfig)
	if err != nil {
		return nil, err
	}

	return &BigCacheStorage{
		underlying: cache,
		logger:     logger.Named("bigcache"),
	}, nil
}

func (f *BigCacheStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	f.logger.Debug("get operation", "logicalPath", logicalPath)

	if err := f.validatePath(logicalPath); err != nil {
		return nil, err
	}

	path, key := f.expandPath(logicalPath)
	b, err := f.underlying.Get(filepath.Join(path, key))
	if err != nil {
		if errors.Is(err, bigcache.ErrEntryNotFound) {
			return nil, &NotFoundError{logicalPath}
		}
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: b}, nil
}

func (f *BigCacheStorage) Put(ctx context.Context, e StorageEntry) error {
	f.logger.Debug("put operation", "logicalPath", e.Key)

	if err := f.validatePath(e.Key); err != nil {
		return err
	}
	path, key := f.expandPath(e.Key)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return f.underlying.Set(filepath.Join(path, key), e.Value)
}

func (f *BigCacheStorage) Delete(ctx context.Context, logicalPath string) error {
	f.logger.Debug("delete operation", "logicalPath", logicalPath)

	if err := f.validatePath(logicalPath); err != nil {
		return err
	}
	path, key := f.expandPath(logicalPath)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return f.underlying.Delete(filepath.Join(path, key))
}

func (f *BigCacheStorage) List(ctx context.Context, prefix string) ([]string, error) {
	f.logger.Debug("list operation", "prefix", prefix)

	if err := f.validatePath(prefix); err != nil {
		return nil, err
	}

	var names []string
	iterator := f.underlying.Iterator()
	for iterator.SetNext() {
		record, err := iterator.Value()
		if err != nil {
			f.logger.Error(err.Error())
			return names, err
		}
		key := record.Key()
		if strings.HasPrefix(key, prefix) {
			names = append(names, strings.TrimPrefix(key, prefix))
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if len(names) > 0 {
		sort.Strings(names)
	}

	return names, nil
}

func (f *BigCacheStorage) validatePath(path string) error {
	switch {
	case strings.Contains(path, ".."):
		return errors.New("path cannot reference parents")
	}

	return nil
}

func (f *BigCacheStorage) expandPath(k string) (string, string) {
	key := filepath.Base(k)
	path := filepath.Dir(k)
	return path, key
}

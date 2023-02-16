package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

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

func NewBigCacheStorage(logger hclog.Logger, config bigcache.Config) (*BigCacheStorage, error) {
	cache, err := bigcache.New(context.Background(), config)
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

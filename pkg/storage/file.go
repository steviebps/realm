package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/utils"
)

type FileStorage struct {
	logger hclog.Logger
	path   string
}

var (
	_ Storage = (*FileStorage)(nil)
)

func NewFileStorage(path string, logger hclog.Logger) (*FileStorage, error) {
	if path == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	return &FileStorage{
		logger: logger.Named("file"),
		path:   path,
	}, nil
}

func (f *FileStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	f.logger.Debug("get operation", "logicalPath", logicalPath)

	if err := f.validatePath(logicalPath); err != nil {
		return nil, err
	}

	path, key := f.expandPath(logicalPath)
	file, err := os.OpenFile(filepath.Join(path, key), os.O_RDONLY, 0600)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(file); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: key, Value: buf.Bytes()}, nil
}

func (f *FileStorage) Put(ctx context.Context, prefix string, e StorageEntry) error {
	logicalPath := utils.EnsureTrailingSlash(prefix) + e.Key
	f.logger.Debug("put operation", "logicalPath", logicalPath)

	if err := f.validatePath(logicalPath); err != nil {
		return err
	}
	path, key := f.expandPath(logicalPath)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Make the parent tree
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}

	file, err := os.OpenFile(filepath.Join(path, key), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		return err
	}

	return utils.WriteInterfaceWith(file, e.Value, true)
}

func (f *FileStorage) Delete(ctx context.Context, logicalPath string) error {
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

	if err := os.Remove(filepath.Join(path, key)); err != nil {
		return err
	}

	return nil
}

func (f *FileStorage) List(ctx context.Context, prefix string) ([]string, error) {
	f.logger.Debug("list operation", "prefix", prefix)

	if err := f.validatePath(prefix); err != nil {
		return nil, err
	}

	path := f.path
	if prefix != "" {
		path = filepath.Join(path, prefix)
	}

	file, err := os.Open(path)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		return nil, err
	}

	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, err
	}

	for i, name := range names {
		fi, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			names[i] = name + "/"
		} else {
			if name[0] == '_' {
				names[i] = name[1:]
			}
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

func (f *FileStorage) validatePath(path string) error {
	switch {
	case strings.Contains(path, ".."):
		return errors.New("path cannot reference parents")
	}

	return nil
}

func (f *FileStorage) expandPath(k string) (string, string) {
	path := filepath.Join(f.path, k)
	key := filepath.Base(path)
	path = filepath.Dir(path)
	return path, "_" + key
}

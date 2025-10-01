package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type FileStorage struct {
	path   string
	tracer trace.Tracer
}

var (
	_ Storage = (*FileStorage)(nil)
)

func NewFileStorage(conf map[string]string) (Storage, error) {
	if conf["path"] == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	return &FileStorage{
		path:   conf["path"],
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

func (f *FileStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	logger := hclog.FromContext(ctx).ResetNamed("file")
	ctx, span := f.tracer.Start(ctx, "FileStorage Get", trace.WithAttributes(attribute.String("realm.file.logicalPath", logicalPath)))
	defer span.End()
	logger.Debug("get operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return nil, err
	}

	path, key := f.expandPath(logicalPath + "entry")
	file, err := os.OpenFile(filepath.Join(path, key), os.O_RDONLY, 0600)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		span.RecordError(err)
		if errors.Is(err, os.ErrNotExist) {
			return nil, &NotFoundError{logicalPath}
		}
		return nil, err
	}

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(file); err != nil {
		span.RecordError(err)
		return nil, err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: buf.Bytes()}, nil
}

func (f *FileStorage) Put(ctx context.Context, e StorageEntry) error {
	logger := hclog.FromContext(ctx).ResetNamed("file")
	ctx, span := f.tracer.Start(ctx, "FileStorage Put", trace.WithAttributes(attribute.String("realm.file.entry.key", e.Key)))
	defer span.End()
	logger.Debug("put operation", "logicalPath", e.Key)

	if err := ValidatePath(e.Key); err != nil {
		span.RecordError(err)
		return err
	}
	path, key := f.expandPath(e.Key + "entry")

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
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
		span.RecordError(err)
		return err
	}

	return utils.WriteInterfaceWith(file, e.Value, false)
}

func (f *FileStorage) Delete(ctx context.Context, logicalPath string) error {
	logger := hclog.FromContext(ctx).ResetNamed("file")
	ctx, span := f.tracer.Start(ctx, "FileStorage Delete", trace.WithAttributes(attribute.String("realm.file.logicalPath", logicalPath)))
	defer span.End()
	logger.Debug("delete operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return err
	}
	path, key := f.expandPath(logicalPath + "entry")

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return ctx.Err()
	default:
	}

	if err := os.Remove(filepath.Join(path, key)); err != nil {
		span.RecordError(err)
		if errors.Is(err, os.ErrNotExist) {
			return &NotFoundError{logicalPath}
		}
		return err
	}

	return nil
}

func (f *FileStorage) List(ctx context.Context, prefix string) ([]string, error) {
	logger := hclog.FromContext(ctx).ResetNamed("file")
	ctx, span := f.tracer.Start(ctx, "FileStorage List", trace.WithAttributes(attribute.String("realm.file.prefix", prefix)))
	defer span.End()
	logger.Debug("list operation", "prefix", prefix)

	if err := ValidatePath(prefix); err != nil {
		span.RecordError(err)
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
		span.RecordError(err)
		return nil, err
	}

	names, err := file.Readdirnames(-1)
	if err != nil {
		span.RecordError(err)
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
			names[i] = "."
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

func (f *FileStorage) expandPath(k string) (string, string) {
	path := filepath.Join(f.path, k)
	key := filepath.Base(path)
	path = filepath.Dir(path)
	return path, "_" + key
}

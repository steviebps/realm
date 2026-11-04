package storage

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/steviebps/realm/helper/logging"
	bolt "go.etcd.io/bbolt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type BoltStorage struct {
	db     *bolt.DB
	tracer trace.Tracer
}

var (
	_ Storage = (*BoltStorage)(nil)
)

// NewBoltStorage creates a new BoltDB storage backend
func NewBoltStorage(conf map[string]string) (Storage, error) {
	var err error
	path := conf["path"]
	if path == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	db, err := bolt.Open(filepath.Join(path, "realm.db"), 0600, nil)
	if err != nil {
		return nil, err
	}

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("chambers"))
		if err != nil {
			return fmt.Errorf("create bucket: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &BoltStorage{
		db:     db,
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

func (b *BoltStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := b.tracer.Start(ctx, "BoltStorage Get", trace.WithAttributes(attribute.String("realm.bolt.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return nil, err
	}

	path := path.Clean(logicalPath)

	var file []byte
	err := b.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("chambers"))
		// the slice returned by Get points into the memory-mapped database and
		// is only valid for the life of the transaction, so copy it out
		if v := bucket.Get([]byte(path)); v != nil {
			file = bytes.Clone(v)
		}
		return nil
	})
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	if file == nil {
		err := &NotFoundError{logicalPath}
		recordError(span, err)
		return nil, err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: file}, nil
}

func (b *BoltStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := b.tracer.Start(ctx, "BoltStorage Put", trace.WithAttributes(attribute.String("realm.bolt.entry.key", e.Key)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		recordError(span, err)
		return err
	}

	path := path.Clean(e.Key)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	err := b.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("chambers"))
		err := b.Put([]byte(path), []byte(e.Value))
		return err
	})
	if err != nil {
		recordError(span, err)
		return err
	}

	return nil
}

func (b *BoltStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := b.tracer.Start(ctx, "BoltStorage Delete", trace.WithAttributes(attribute.String("realm.bolt.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return err
	}

	path := path.Clean(logicalPath)

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	err := b.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("chambers"))
		if b.Get([]byte(path)) == nil {
			return &NotFoundError{logicalPath}
		}
		return b.Delete([]byte(path))
	})

	if err != nil {
		recordError(span, err)
		return err
	}

	return nil
}

func (b *BoltStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := b.tracer.Start(ctx, "BoltStorage List", trace.WithAttributes(attribute.String("realm.bolt.prefix", prefix)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	path := path.Clean(prefix)
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	names := make([]string, 0)
	if err := b.db.View(func(tx *bolt.Tx) error {
		// Assume bucket exists and has keys
		c := tx.Bucket([]byte("chambers")).Cursor()

		cleanPrefix := []byte(path)
		set := make(map[string]struct{})
		for k, _ := c.Seek(cleanPrefix); k != nil && bytes.HasPrefix(k, cleanPrefix); k, _ = c.Next() {
			key := bytes.TrimPrefix(k, cleanPrefix)
			before, _, _ := bytes.Cut(key, []byte("/"))
			if len(before) > 0 {
				set[string(before)] = struct{}{}
			}
		}

		for key := range set {
			names = append(names, key)
		}

		return nil
	}); err != nil {
		recordError(span, err)
		return nil, err
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

func (b *BoltStorage) Close(ctx context.Context) error {
	return b.db.Close()
}

package storage

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
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
	if conf["path"] == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	db, err := bolt.Open("realm.db", 0600, nil)
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
		span.RecordError(err)
		return nil, err
	}

	path := path.Clean(logicalPath)

	var file []byte
	err := b.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("chambers"))
		file = bucket.Get([]byte(path))
		return nil
	})
	if file == nil {
		err := &NotFoundError{logicalPath}
		span.RecordError(err)
		return nil, err
	}
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
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
		span.RecordError(err)
		return err
	}

	path := path.Clean(e.Key)

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return ctx.Err()
	default:
	}

	err := b.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("chambers"))
		err := b.Put([]byte(path), []byte(e.Value))
		return err
	})
	if err != nil {
		span.RecordError(err)
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
		span.RecordError(err)
		return err
	}

	path := path.Clean(logicalPath)

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return ctx.Err()
	default:
	}

	err := b.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("chambers"))
		err := b.Delete([]byte(path))
		return err
	})

	if err != nil {
		span.RecordError(err)
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
		span.RecordError(err)
		return nil, err
	}

	path := path.Clean(prefix)
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	names := make([]string, 0)

	b.db.View(func(tx *bolt.Tx) error {
		// Assume bucket exists and has keys
		c := tx.Bucket([]byte("chambers")).Cursor()

		cleanPrefix := []byte(path)
		for k, _ := c.Seek(cleanPrefix); k != nil && bytes.HasPrefix(k, cleanPrefix); k, _ = c.Next() {
			key := strings.TrimPrefix(string(k), path)
			before, after, _ := strings.Cut(key, "/")
			if before != "" && after == "" {
				names = append(names, key)
			}

		}

		return nil
	})

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

func (b *BoltStorage) Close(ctx context.Context) error {
	return b.db.Close()
}

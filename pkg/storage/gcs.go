package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"path"
	"sort"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/api/iterator"
)

type GCSStorage struct {
	client *gcs.Client
	bucket string
}

var (
	_ Storage = (*GCSStorage)(nil)
)

func NewGCSStorage(conf map[string]string) (Storage, error) {
	if conf["bucket"] == "" {
		return nil, fmt.Errorf("'bucket' must be set")
	}

	ctx := context.Background()
	client, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, err
	}

	return &GCSStorage{
		client: client,
		bucket: conf["bucket"],
	}, nil
}

func (s *GCSStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	logger := hclog.FromContext(ctx).ResetNamed("gcs")
	logger.Debug("get operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		return nil, err
	}

	p, key := s.expandPath(logicalPath + "entry")

	r, err := s.client.Bucket(s.bucket).Object(path.Join(p, key)).NewReader(ctx)
	if err == gcs.ErrObjectNotExist {
		return nil, &NotFoundError{logicalPath}
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return &StorageEntry{Key: logicalPath, Value: buf.Bytes()}, nil
}

func (s *GCSStorage) Put(ctx context.Context, e StorageEntry) (retErr error) {
	logger := hclog.FromContext(ctx).ResetNamed("gcs")
	logger.Debug("put operation", "logicalPath", e.Key)

	if err := ValidatePath(e.Key); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	p, key := s.expandPath(e.Key + "entry")

	w := s.client.Bucket(s.bucket).Object(path.Join(p, key)).NewWriter(ctx)
	md5Array := md5.Sum(e.Value)
	w.MD5 = md5Array[:]

	if _, err := w.Write(e.Value); err != nil {
		return fmt.Errorf("failed to put: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to put: %w", err)
	}

	return nil
}

func (s *GCSStorage) Delete(ctx context.Context, logicalPath string) error {
	logger := hclog.FromContext(ctx).ResetNamed("gcs")
	logger.Debug("delete operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	p, key := s.expandPath(logicalPath + "entry")
	err := s.client.Bucket(s.bucket).Object(path.Join(p, key)).Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete: %w", err)
	}

	return nil
}

// TODO: finish
func (s *GCSStorage) List(ctx context.Context, prefix string) ([]string, error) {
	logger := hclog.FromContext(ctx).ResetNamed("gcs")
	logger.Debug("list operation", "prefix", prefix)

	if err := ValidatePath(prefix); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	iter := s.client.Bucket(s.bucket).Objects(ctx, &gcs.Query{
		Prefix: prefix,
		// Delimiter: "/",
		Versions: false,
	})

	keys := []string{}

	for {
		objAttrs, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read object: %w", err)
		}

		var path string
		if objAttrs.Prefix != "" {
			// "subdirectory"
			path = objAttrs.Prefix
		} else {
			// file
			path = objAttrs.Name
		}

		// get relative file/dir just like "basename"
		key := strings.TrimPrefix(path, prefix)
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys, nil
}

func (s *GCSStorage) expandPath(k string) (string, string) {
	key := path.Base(k)
	p := path.Dir(k)
	return strings.TrimPrefix(p, "/"), "_" + key
}

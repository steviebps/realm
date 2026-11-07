package storage

import (
	"context"
	"errors"
	"testing"
)

// failingDelete is a Storage whose deletes fail with err.
type failingDelete struct {
	Storage
	err error
}

func (f failingDelete) Delete(context.Context, string) error { return f.err }

// newCacheableOver returns a cacheable storage that caches in memory over
// source, along with the cache itself.
func newCacheableOver(t *testing.T, source Storage) (Storage, Storage) {
	t.Helper()
	cache, err := NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close(context.Background()) })
	c, err := NewCacheableStorage(cache, source)
	if err != nil {
		t.Fatal(err)
	}
	return c, cache
}

func newFileSource(t *testing.T) Storage {
	t.Helper()
	s, err := NewFileStorage(map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestCacheableDelete checks that a cache holding no copy of an entry (it is
// evicted, or was never read) neither fails the delete of the source's copy
// nor hides how the source's delete went.
func TestCacheableDelete(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk on fire")

	t.Run("entry only in the source", func(t *testing.T) {
		source := newFileSource(t)
		c, cache := newCacheableOver(t, source)
		if err := source.Put(ctx, StorageEntry{Key: "/a/", Value: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := c.Delete(ctx, "/a/"); err != nil {
			t.Fatalf("Delete = %v, want nil: the source's copy was deleted", err)
		}
		if _, err := source.Get(ctx, "/a/"); !isNotFound(err) {
			t.Errorf("source Get after Delete = %v, want NotFoundError", err)
		}
		if _, err := cache.Get(ctx, "/a/"); !isNotFound(err) {
			t.Errorf("cache Get after Delete = %v, want NotFoundError", err)
		}
	})

	t.Run("entry in both", func(t *testing.T) {
		source := newFileSource(t)
		c, cache := newCacheableOver(t, source)
		if err := c.Put(ctx, StorageEntry{Key: "/a/", Value: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := c.Delete(ctx, "/a/"); err != nil {
			t.Fatalf("Delete = %v, want nil", err)
		}
		if _, err := source.Get(ctx, "/a/"); !isNotFound(err) {
			t.Errorf("source Get after Delete = %v, want NotFoundError", err)
		}
		if _, err := cache.Get(ctx, "/a/"); !isNotFound(err) {
			t.Errorf("cache Get after Delete = %v, want NotFoundError", err)
		}
	})

	t.Run("entry in neither", func(t *testing.T) {
		c, _ := newCacheableOver(t, newFileSource(t))
		if err := c.Delete(ctx, "/missing/"); !isNotFound(err) {
			t.Errorf("Delete = %v, want NotFoundError", err)
		}
	})

	t.Run("source fails and the cache holds nothing", func(t *testing.T) {
		c, _ := newCacheableOver(t, failingDelete{Storage: newFileSource(t), err: boom})
		if err := c.Delete(ctx, "/a/"); !errors.Is(err, boom) || isNotFound(err) {
			t.Errorf("Delete = %v, want the source's error", err)
		}
	})

	// Update treats a NotFoundError from Delete as "already gone", so it must
	// not be able to see one that came from the cache alone.
	t.Run("Update reports a failed source delete", func(t *testing.T) {
		c, _ := newCacheableOver(t, failingDelete{Storage: newFileSource(t), err: boom})
		err := Update(ctx, c, []string{"/a/"}, func(map[string]*StorageEntry) ([]Op, error) {
			return []Op{{Key: "/a/"}}, nil
		})
		if !errors.Is(err, boom) {
			t.Errorf("Update = %v, want the source's error", err)
		}
	})
}

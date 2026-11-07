package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

// stallingSource wraps a Storage. After stallNextGet, the next Get reads its
// entry, closes read, and waits for release before returning it: a read of
// the source that a write overtakes.
type stallingSource struct {
	Storage
	mu       sync.Mutex
	armed    bool
	read     chan struct{}
	release  chan struct{}
	getCount atomic.Int64
}

func (s *stallingSource) stallNextGet() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = true
	s.read = make(chan struct{})
	s.release = make(chan struct{})
}

func (s *stallingSource) Get(ctx context.Context, key string) (*StorageEntry, error) {
	s.getCount.Add(1)
	e, err := s.Storage.Get(ctx, key)
	s.mu.Lock()
	armed, read, release := s.armed, s.read, s.release
	s.armed = false
	s.mu.Unlock()
	if armed {
		close(read)
		<-release
	}
	return e, err
}

// landingSource wraps a Storage whose writes reach it and then report err,
// as a write that timed out after it landed does; after, when set, runs
// once the write has landed.
type landingSource struct {
	Storage
	err   error
	after func()
}

func (s *landingSource) landed(err error) error {
	if s.after != nil {
		s.after()
	}
	if err != nil {
		return err
	}
	return s.err
}

func (s *landingSource) Put(ctx context.Context, e StorageEntry) error {
	return s.landed(s.Storage.Put(ctx, e))
}

func (s *landingSource) Delete(ctx context.Context, key string) error {
	return s.landed(s.Storage.Delete(ctx, key))
}

// flakyCache is a cache whose deletes fail while failing is set.
type flakyCache struct {
	Storage
	failing atomic.Bool
}

func (c *flakyCache) Delete(ctx context.Context, key string) error {
	if c.failing.Load() {
		return errors.New("cache unavailable")
	}
	return c.Storage.Delete(ctx, key)
}

// keyList is the JSON document the lost-update tests append names to.
type keyList struct {
	Keys []string `json:"keys"`
}

func appendKey(ctx context.Context, s Storage, key, name string) error {
	_, err := UpdateJSON(ctx, s, key, func(l *keyList) error {
		l.Keys = append(l.Keys, name)
		return nil
	})
	return err
}

func readKeys(t *testing.T, s Storage, key string) []string {
	t.Helper()
	var l keyList
	if _, err := ReadJSON(context.Background(), s, key, &l); err != nil {
		t.Fatal(err)
	}
	return l.Keys
}

// TestCacheableFillCannotLandAfterWrite checks that a read which fetched an
// entry from the source before a write cannot put that older entry in the
// cache after the write: reads would serve it, and the next Update would
// write it back over the write.
func TestCacheableFillCannotLandAfterWrite(t *testing.T) {
	ctx := context.Background()
	const k = "/_realm/keys/"
	for _, write := range []string{"Update", "Put"} {
		t.Run(write, func(t *testing.T) {
			src := &stallingSource{Storage: newFileSource(t)}
			c, _ := newCacheableOver(t, src)
			if err := appendKey(ctx, src.Storage, k, "a"); err != nil {
				t.Fatal(err)
			}

			// a read misses the cache and fetches ["a"], then stalls
			src.stallNextGet()
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = c.Get(ctx, k)
			}()
			<-src.read

			// meanwhile a write adds "b"
			switch write {
			case "Update":
				if err := appendKey(ctx, c, k, "b"); err != nil {
					t.Fatal(err)
				}
			case "Put":
				if err := c.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`{"keys":["a","b"]}`)}); err != nil {
					t.Fatal(err)
				}
			}
			close(src.release)
			<-done

			if got := readKeys(t, c, k); !slices.Equal(got, []string{"a", "b"}) {
				t.Errorf("read after the write = %v, want [a b]: the stalled read cached an older entry", got)
			}
			if err := appendKey(ctx, c, k, "c"); err != nil {
				t.Fatal(err)
			}
			if got := readKeys(t, src.Storage, k); !slices.Equal(got, []string{"a", "b", "c"}) {
				t.Errorf("source = %v, want [a b c]: an update was lost", got)
			}
		})
	}
}

// TestCacheableInvalidatesAfterEveryWriteAttempt checks that the cache drops
// a key after a write whether or not the write reported success: a write
// whose caller went away, or that failed after landing, must not leave the
// cache serving the entry from before it.
func TestCacheableInvalidatesAfterEveryWriteAttempt(t *testing.T) {
	ctx := context.Background()
	const k = "/flags/"
	boom := errors.New("timed out")
	tests := []struct {
		name string
		// write changes k through c, whose source is s
		write func(ctx context.Context, c Storage, s *landingSource) error
		// want is the source's entry after the write, or "" when deleted
		want string
	}{
		{"put, then the caller goes away", func(ctx context.Context, c Storage, s *landingSource) error {
			ctx, cancel := context.WithCancel(ctx)
			s.after = cancel
			return c.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`{"v":2}`)})
		}, `{"v":2}`},
		{"put lands, then fails", func(ctx context.Context, c Storage, s *landingSource) error {
			s.err = boom
			return c.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`{"v":2}`)})
		}, `{"v":2}`},
		{"delete, then the caller goes away", func(ctx context.Context, c Storage, s *landingSource) error {
			ctx, cancel := context.WithCancel(ctx)
			s.after = cancel
			return c.Delete(ctx, k)
		}, ""},
		{"delete lands, then fails", func(ctx context.Context, c Storage, s *landingSource) error {
			s.err = boom
			return c.Delete(ctx, k)
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &landingSource{Storage: newFileSource(t)}
			c, _ := newCacheableOver(t, src)
			if err := src.Storage.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`{"v":1}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get(ctx, k); err != nil { // cache {"v":1}
				t.Fatal(err)
			}

			err := tt.write(ctx, c, src)
			if errors.Is(err, boom) != (src.err != nil) {
				t.Errorf("write = %v, want the source's error", err)
			}
			got, err := c.Get(ctx, k)
			switch {
			case tt.want == "" && !isNotFound(err):
				t.Errorf("Get after the delete = %v, %v; want NotFoundError (the cache kept the deleted entry)", got, err)
			case tt.want != "" && (err != nil || string(got.Value) != tt.want):
				t.Errorf("Get after the put = %v, %v; want %s (the cache kept the old entry)", got, err, tt.want)
			}
		})
	}
}

// TestCacheableBypassesKeysItCannotInvalidate checks that when the cache
// cannot drop a written key, reads of that key go to the source (and do not
// refill the cache) until dropping it succeeds.
func TestCacheableBypassesKeysItCannotInvalidate(t *testing.T) {
	ctx := context.Background()
	const k = "/flags/"
	src := &stallingSource{Storage: newFileSource(t)}
	bc, err := NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	cache := &flakyCache{Storage: bc}
	c, err := NewCacheableStorage(cache, src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })

	if err := c.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, k); err != nil { // cache 1
		t.Fatal(err)
	}

	cache.failing.Store(true)
	if err := c.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`2`)}); err != nil {
		t.Fatalf("Put = %v, want nil: the source's write succeeded", err)
	}
	for range 2 {
		before := src.getCount.Load()
		if e, err := c.Get(ctx, k); err != nil || string(e.Value) != "2" {
			t.Fatalf("Get with a stale cache entry = %v, %v; want 2 from the source", e, err)
		}
		if src.getCount.Load() == before {
			t.Fatal("Get served a key it could not invalidate from the cache")
		}
	}
	if e, err := cache.Get(ctx, k); err != nil || string(e.Value) != "1" {
		t.Fatalf("the bypassed key was refilled: cache holds %v, %v", e, err)
	}

	// once the cache works again, the next read drops the stale entry and
	// the cache is used again
	cache.failing.Store(false)
	if e, err := c.Get(ctx, k); err != nil || string(e.Value) != "2" {
		t.Fatalf("Get after the cache recovered = %v, %v; want 2", e, err)
	}
	before := src.getCount.Load()
	if e, err := c.Get(ctx, k); err != nil || string(e.Value) != "2" {
		t.Fatalf("Get = %v, %v; want 2", e, err)
	}
	if src.getCount.Load() != before {
		t.Error("the key still bypasses the cache after an invalidation succeeded")
	}
}

// TestCacheableUpdatesAcrossProcesses runs two cacheable storages over one
// file store, as two processes would: each Update must read the source under
// the lock file, not its own cache, or one process's writes are lost.
func TestCacheableUpdatesAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	open := func() Storage {
		src, err := NewFileStorage(map[string]string{"path": dir})
		if err != nil {
			t.Fatal(err)
		}
		c, _ := newCacheableOver(t, src)
		return c
	}
	a, b := open(), open()
	const k = "/_realm/catalog/"
	for _, step := range []struct {
		s    Storage
		name string
	}{{a, "a1"}, {b, "b1"}, {a, "a2"}, {b, "b2"}} {
		if _, err := step.s.Get(ctx, k); err != nil && !isNotFound(err) { // fill this process's cache
			t.Fatal(err)
		}
		if err := appendKey(ctx, step.s, k, step.name); err != nil {
			t.Fatal(err)
		}
	}
	src, _ := NewFileStorage(map[string]string{"path": dir})
	if got, want := readKeys(t, src, k), []string{"a1", "b1", "a2", "b2"}; !slices.Equal(got, want) {
		t.Errorf("stored keys = %v, want %v", got, want)
	}
	// the lock is the source's
	if _, ok := a.(Locker); !ok {
		t.Fatal("cacheable over file storage is not a Locker")
	}
}

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

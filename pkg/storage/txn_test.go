package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestUpdateValidatesKeysBeforeApplying writes a chamber whose history key is
// too long for the file backend, as the server's history keys (the escaped
// chamber path, in one segment) are for a long chamber path. The update must
// fail before writing anything, rather than writing the chamber and then
// failing on its history.
func TestUpdateValidatesKeysBeforeApplying(t *testing.T) {
	ctx := context.Background()
	chamber := "/" + strings.Repeat("a", 250) + "/"
	index := ReservedPrefix + "history/" + url.PathEscape(chamber) + "/"
	stores := map[string]func(t *testing.T) Storage{
		"file": newFileSource,
		"cacheable over file": func(t *testing.T) Storage {
			c, _ := newCacheableOver(t, newFileSource(t))
			return c
		},
	}
	for name, open := range stores {
		for _, read := range [][]string{{chamber}, {chamber, index}} {
			t.Run(name+" reading "+strconv.Itoa(len(read))+" keys", func(t *testing.T) {
				s := open(t)
				err := Update(ctx, s, read, func(map[string]*StorageEntry) ([]Op, error) {
					return []Op{
						{Key: chamber, Value: json.RawMessage(`{"rules":{}}`)},
						{Key: index + "1/", Value: json.RawMessage(`{}`)},
						{Key: index, Value: json.RawMessage(`{}`)},
					}, nil
				})
				if !errors.Is(err, ErrInvalidPath) {
					t.Fatalf("Update = %v, want an error wrapping ErrInvalidPath", err)
				}
				if _, err := s.Get(ctx, chamber); !isNotFound(err) {
					t.Errorf("Get(chamber) after the failed update = %v, want NotFoundError: it was half applied", err)
				}
			})
		}
	}
}

// staleReader is a Storage whose Get serves stale copies of some entries,
// while ReadForUpdate reads the stored ones, as cacheable does.
type staleReader struct {
	Storage
	stale map[string]*StorageEntry
}

func (s *staleReader) Get(ctx context.Context, key string) (*StorageEntry, error) {
	if e := s.stale[key]; e != nil {
		return e, nil
	}
	return s.Storage.Get(ctx, key)
}

func (s *staleReader) ReadForUpdate(ctx context.Context, key string) (*StorageEntry, error) {
	return s.Storage.Get(ctx, key)
}

func TestUpdateReadsThroughUpdateReader(t *testing.T) {
	ctx := context.Background()
	s := &staleReader{Storage: backends(t)["bigcache"], stale: map[string]*StorageEntry{"/n/": {Key: "/n/", Value: json.RawMessage(`1`)}}}
	if err := s.Put(ctx, StorageEntry{Key: "/n/", Value: json.RawMessage(`5`)}); err != nil {
		t.Fatal(err)
	}
	got, err := UpdateJSON(ctx, s, "/n/", func(n *int) error {
		*n++
		return nil
	})
	if err != nil || got != 6 {
		t.Errorf("UpdateJSON = %d, %v; want 6, read from the stored entry rather than the stale copy", got, err)
	}
}

func TestValidatePathWrapsErrInvalidPath(t *testing.T) {
	for _, p := range []string{"/a/../b/", "..", "/a/..b/"} {
		if err := ValidatePath(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("ValidatePath(%q) = %v, want an error wrapping ErrInvalidPath", p, err)
		}
	}
	if err := ValidatePath("/a/b/"); err != nil {
		t.Errorf("ValidatePath(/a/b/) = %v", err)
	}
}

// TestUpdateWithinOneProcess checks that Update serializes concurrent
// read-modify-writes on backends that are not Transactional.
func TestUpdateWithinOneProcess(t *testing.T) {
	ctx := context.Background()
	for name, s := range backends(t) {
		t.Run(name, func(t *testing.T) {
			const n = 50
			var wg sync.WaitGroup
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := UpdateJSON(ctx, s, "/counter/", func(v *int) error {
						*v++
						return nil
					})
					if err != nil {
						t.Errorf("UpdateJSON: %v", err)
					}
				}()
			}
			wg.Wait()

			e, err := s.Get(ctx, "/counter/")
			if err != nil {
				t.Fatal(err)
			}
			// some backends store JSON with a trailing newline
			if got := strings.TrimSpace(string(e.Value)); got != strconv.Itoa(n) {
				t.Errorf("counter = %s, want %d", got, n)
			}
		})
	}
}

func TestUpdateAbortsOnError(t *testing.T) {
	ctx := context.Background()
	s := backends(t)["bigcache"]
	stop := errors.New("stop")
	err := Update(ctx, s, []string{"/a/"}, func(map[string]*StorageEntry) ([]Op, error) {
		return []Op{{Key: "/a/", Value: json.RawMessage(`1`)}}, stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Update = %v, want fn's error", err)
	}
	if _, err := s.Get(ctx, "/a/"); !isNotFound(err) {
		t.Errorf("expected nothing written, got %v", err)
	}
}

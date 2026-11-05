package storage

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/steviebps/realm/pkg/storage/pgtest"
)

// backends returns a fresh instance of every locally testable backend.
func backends(t *testing.T) map[string]Storage {
	t.Helper()
	out := map[string]Storage{}
	confs := map[string]map[string]string{
		"file":     {"path": t.TempDir()},
		"boltdb":   {"path": t.TempDir()},
		"bigcache": {},
	}
	if os.Getenv(pgtest.EnvURL) != "" {
		confs["postgres"] = map[string]string{"url": pgtest.URL(t)}
	}
	for name, conf := range confs {
		s, err := StorageOptions[name](conf)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		out[name] = s
	}
	return out
}

func TestDeleteMissingIsNotFound(t *testing.T) {
	ctx := context.Background()
	for name, s := range backends(t) {
		t.Run(name, func(t *testing.T) {
			err := s.Delete(ctx, "/missing/")
			var nf *NotFoundError
			if !errors.As(err, &nf) {
				t.Fatalf("expected NotFoundError deleting a missing entry, got %v", err)
			}
		})
	}
}

func TestListImmediateChildren(t *testing.T) {
	ctx := context.Background()
	// these backends share the "." / "child/" listing format
	all := backends(t)
	for _, name := range []string{"file", "bigcache", "postgres"} {
		s, ok := all[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"/", "/a/", "/a/b/", "/a/b/c/", "/a/d/", "/x/"} {
				if err := s.Put(ctx, StorageEntry{Key: key, Value: []byte(`{}`)}); err != nil {
					t.Fatalf("put %s: %v", key, err)
				}
			}
			got, err := s.List(ctx, "/a/")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if want := []string{".", "b/", "d/"}; !slices.Equal(got, want) {
				t.Errorf("List(/a/) = %v, want %v", got, want)
			}
		})
	}
}

// TestPutStoresValueVerbatim checks that every backend returns exactly the
// bytes it was given, which ETags (hashes of the stored bytes) rely on.
func TestPutStoresValueVerbatim(t *testing.T) {
	ctx := context.Background()
	values := []string{
		`{"rules":{}}`,
		`{"rules":{"a":{"type":"string","value":"<b>&</b>"}}}`,
		"{\n  \"pretty\": true\n}",
	}
	for name, s := range backends(t) {
		t.Run(name, func(t *testing.T) {
			for _, v := range values {
				if err := s.Put(ctx, StorageEntry{Key: "/verbatim/", Value: []byte(v)}); err != nil {
					t.Fatalf("put: %v", err)
				}
				e, err := s.Get(ctx, "/verbatim/")
				if err != nil {
					t.Fatalf("get: %v", err)
				}
				if string(e.Value) != v {
					t.Errorf("Get returned %q, want %q", e.Value, v)
				}
			}
		})
	}
}

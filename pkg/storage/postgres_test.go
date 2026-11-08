package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/steviebps/realm/pkg/storage/pgtest"
)

func newPostgres(t *testing.T, url string) *PostgresStorage {
	t.Helper()
	s, err := NewPostgresStorage(map[string]string{"url": url})
	if err != nil {
		t.Fatalf("NewPostgresStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s.(*PostgresStorage)
}

// TestPostgresStorage covers what the shared backend tests (which include
// postgres when it is configured) do not: versions, exact bytes, and listing.
func TestPostgresStorage(t *testing.T) {
	ctx := context.Background()
	s := newPostgres(t, pgtest.URL(t))

	for _, k := range []string{"/", "/a/", "/a/b/", "/a/b/c/", "/a/d/", "/x/"} {
		if err := s.Put(ctx, StorageEntry{Key: k, Value: json.RawMessage(`{"k":"` + k + `"}`)}); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	// values are stored byte for byte (ETags hash them)
	raw := json.RawMessage(`{ "spaced":  true ,"b":1}`)
	if err := s.Put(ctx, StorageEntry{Key: "/a/", Value: raw}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Value) != string(raw) {
		t.Errorf("Get = %s, want %s", got.Value, raw)
	}
	if got.Version != 7 {
		t.Errorf("Version = %d, want 7 (the seventh write)", got.Version)
	}

	for prefix, want := range map[string][]string{
		"/":   {".", "a/", "x/"},
		"/q/": {},
	} {
		names, err := s.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List %s: %v", prefix, err)
		}
		if !slices.Equal(names, want) {
			t.Errorf("List(%s) = %v, want %v", prefix, names, want)
		}
	}
}

// TestPostgresRevisionsAreContiguous checks that every committed write takes
// the next revision and nothing else takes one, which the change feed relies
// on to notice pruned change records (the shared contract only requires
// revisions to increase).
func TestPostgresRevisionsAreContiguous(t *testing.T) {
	ctx := context.Background()
	s := newPostgres(t, pgtest.URL(t))

	if err := s.Txn(ctx, []Check{{Key: "/a/"}}, []Op{put("/a/", `1`), put("/b/", `2`)}); err != nil {
		t.Fatalf("Txn create: %v", err)
	}
	if v := versionOf(t, s, "/b/"); v != 1 {
		t.Fatalf("version = %d, want 1", v)
	}
	// neither a failed check nor checks alone take a revision
	if err := s.Txn(ctx, []Check{{Key: "/a/"}}, []Op{put("/a/", `3`)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Txn with stale check: expected ErrConflict, got %v", err)
	}
	if err := s.Txn(ctx, []Check{{Key: "/b/", Version: 1}}, nil); err != nil {
		t.Fatalf("check-only Txn: %v", err)
	}
	if err := s.Delete(ctx, "/a/"); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, "/c/", `4`)
	if v := versionOf(t, s, "/c/"); v != 3 {
		t.Errorf("version after a delete and a put = %d, want 3", v)
	}
}

// TestPostgresWatchReportsPrunedGap checks that a watcher that fell behind
// further than the retained change records reports a change to "/".
func TestPostgresWatchReportsPrunedGap(t *testing.T) {
	ctx := context.Background()
	s := newPostgres(t, pgtest.URL(t))

	out := make(chan Change, 8)
	w := &watcher{p: s, out: out}
	for i := range 3 {
		if err := s.Put(ctx, StorageEntry{Key: "/k" + strconv.Itoa(i) + "/", Value: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM realm_changes WHERE rev <= 2"); err != nil {
		t.Fatal(err)
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := w.catchUp(ctx, conn.Conn()); err != nil {
		t.Fatal(err)
	}
	if c := <-out; c != (Change{Rev: 3, Key: "/"}) {
		t.Errorf("change = %+v, want a change to / at revision 3", c)
	}
	if w.last != 3 {
		t.Errorf("last = %d, want 3", w.last)
	}
}

func isNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

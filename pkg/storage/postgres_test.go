package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

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

func TestPostgresTxn(t *testing.T) {
	ctx := context.Background()
	s := newPostgres(t, pgtest.URL(t))
	version := func(k string) uint64 {
		e, err := s.Get(ctx, k)
		if err != nil {
			return 0
		}
		return e.Version
	}

	// creating requires the key to be absent; one transaction, one revision
	if err := s.Txn(ctx, []Check{{Key: "/a/", Version: 0}}, []Op{{Key: "/a/", Value: json.RawMessage(`1`)}, {Key: "/b/", Value: json.RawMessage(`2`)}}); err != nil {
		t.Fatalf("Txn create: %v", err)
	}
	if version("/a/") != 1 || version("/b/") != 1 {
		t.Fatalf("versions = %d, %d; want 1, 1", version("/a/"), version("/b/"))
	}
	if err := s.Txn(ctx, []Check{{Key: "/a/", Version: 0}}, []Op{{Key: "/a/", Value: json.RawMessage(`3`)}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Txn with stale check: expected ErrConflict, got %v", err)
	}
	// a failed check applies nothing
	if e, _ := s.Get(ctx, "/a/"); string(e.Value) != "1" || e.Version != 1 {
		t.Fatalf("after conflict /a/ = %s@%d, want 1@1", e.Value, e.Version)
	}

	// a check on a key that is only read; deleting a missing key is fine
	if err := s.Txn(ctx, []Check{{Key: "/b/", Version: 1}}, []Op{{Key: "/a/"}, {Key: "/missing/"}, {Key: "/c/", Value: json.RawMessage(`4`)}}); err != nil {
		t.Fatalf("Txn delete: %v", err)
	}
	if _, err := s.Get(ctx, "/a/"); !isNotFound(err) {
		t.Errorf("expected /a/ deleted, got %v", err)
	}
	if version("/c/") != 2 {
		t.Errorf("/c/ version = %d, want 2", version("/c/"))
	}

	// checks alone write nothing
	if err := s.Txn(ctx, []Check{{Key: "/b/", Version: 1}}, nil); err != nil {
		t.Errorf("check-only Txn: %v", err)
	}
	if err := s.Put(ctx, StorageEntry{Key: "/d/", Value: json.RawMessage(`5`)}); err != nil || version("/d/") != 3 {
		t.Errorf("next write: %v, version %d; want revision 3", err, version("/d/"))
	}
}

// TestPostgresUpdateAcrossServers increments one counter from two storages
// sharing a database, as two servers would; no increment may be lost.
func TestPostgresUpdateAcrossServers(t *testing.T) {
	ctx := context.Background()
	url := pgtest.URL(t)
	servers := []Storage{newPostgres(t, url), newPostgres(t, url)}

	const perServer = 25
	var wg sync.WaitGroup
	errs := make(chan error, 2*perServer)
	for _, s := range servers {
		for range perServer {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- Update(ctx, s, []string{"/counter/"}, func(cur map[string]*StorageEntry) ([]Op, error) {
					n := 0
					if e := cur["/counter/"]; e != nil {
						n, _ = strconv.Atoi(string(e.Value))
					}
					return []Op{{Key: "/counter/", Value: json.RawMessage(strconv.Itoa(n + 1))}}, nil
				})
			}()
		}
	}
	wg.Wait()
	close(errs)
	conflicts := 0
	for err := range errs {
		if errors.Is(err, ErrConflict) {
			conflicts++
		} else if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	t.Logf("%d of %d updates gave up after repeated conflicts", conflicts, 2*perServer)
	e, err := servers[0].Get(ctx, "/counter/")
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(2*perServer - conflicts); string(e.Value) != want {
		t.Errorf("counter = %s, want %s (%d updates gave up)", e.Value, want, conflicts)
	}
}

func TestPostgresWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url := pgtest.URL(t)
	writer, watcher := newPostgres(t, url), newPostgres(t, url)

	if err := writer.Put(ctx, StorageEntry{Key: "/before/", Value: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	changes := watcher.Watch(ctx)

	if err := writer.Txn(ctx, nil, []Op{{Key: "/a/", Value: json.RawMessage(`{}`)}, {Key: "/b/", Value: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Delete(ctx, "/before/"); err != nil {
		t.Fatal(err)
	}

	// the watcher sees only changes after Watch, in order, from another
	// server, promptly (well before the poll interval)
	want := []Change{{Rev: 2, Key: "/a/"}, {Rev: 2, Key: "/b/"}, {Rev: 3, Key: "/before/"}}
	for _, w := range want {
		select {
		case c := <-changes:
			if c != w {
				t.Fatalf("change = %+v, want %+v", c, w)
			}
		case <-time.After(watchPollInterval / 2):
			t.Fatalf("no change %+v within %v", w, watchPollInterval/2)
		}
	}

	cancel()
	select {
	case _, ok := <-changes:
		if ok {
			t.Fatal("unexpected change after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not stop after its context was canceled")
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

func TestCacheableRefusesPostgres(t *testing.T) {
	_, err := NewCacheableStorageWithConf(map[string]string{"source": "postgres"})
	if err == nil {
		t.Fatal("expected an error for a postgres cacheable source")
	}
}

func isNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

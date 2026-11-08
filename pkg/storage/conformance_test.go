package storage

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage/etcdtest"
	"github.com/steviebps/realm/pkg/storage/pgtest"
)

// The tests in this file hold every Transactional and every Watchable backend
// to the contracts that storage.Update and the server's change feed rely on.
// A Transactional backend joins them by adding itself to
// transactionalBackends; a backend that several servers can share (which
// must be both Transactional and Watchable) adds itself to sharedBackends
// instead, and is tested as Transactional too.

// sharedBackends maps every locally testable backend that several servers can
// share to a function that prepares fresh, empty data for a test and returns
// a function opening one more server's storage over that data.
func sharedBackends() map[string]func(t *testing.T) func() Storage {
	out := map[string]func(t *testing.T) func() Storage{}
	if os.Getenv(pgtest.EnvURL) != "" {
		out["postgres"] = func(t *testing.T) func() Storage {
			url := pgtest.URL(t)
			return func() Storage { return newPostgres(t, url) }
		}
	}
	if os.Getenv(etcdtest.EnvEndpoints) != "" {
		out["etcd"] = func(t *testing.T) func() Storage {
			conf := etcdtest.Options(t)
			return func() Storage { return newEtcd(t, conf) }
		}
	}
	return out
}

// transactionalBackends maps every locally testable Transactional backend to
// a function opening a fresh, empty instance of it.
func transactionalBackends() map[string]func(t *testing.T) Transactional {
	out := map[string]func(t *testing.T) Transactional{
		"boltdb": func(t *testing.T) Transactional { return newBolt(t, t.TempDir()) },
	}
	for name, prepare := range sharedBackends() {
		out[name] = func(t *testing.T) Transactional {
			s, ok := prepare(t)().(Transactional)
			if !ok {
				t.Fatalf("%s is shared but not Transactional", name)
			}
			return s
		}
	}
	return out
}

// txnContract is what every Transactional backend must do.
var txnContract = []struct {
	name string
	run  func(t *testing.T, s Transactional)
}{
	{"creating requires the key to be absent", func(t *testing.T, s Transactional) {
		ctx := context.Background()
		if err := s.Txn(ctx, []Check{{Key: "/a/"}}, []Op{put("/a/", `1`)}); err != nil {
			t.Fatalf("Txn creating /a/: %v", err)
		}
		if err := s.Txn(ctx, []Check{{Key: "/a/"}}, []Op{put("/a/", `2`)}); !errors.Is(err, ErrConflict) {
			t.Fatalf("Txn creating /a/ again = %v, want ErrConflict", err)
		}
		if got := valueOf(t, s, "/a/"); got != "1" {
			t.Errorf("/a/ = %q after the conflict, want 1", got)
		}
		if err := s.Txn(ctx, []Check{{Key: "/missing/", Version: 1}}, nil); !errors.Is(err, ErrConflict) {
			t.Errorf("Txn expecting a missing key at version 1 = %v, want ErrConflict", err)
		}
	}},
	{"a transaction writes at one new revision", func(t *testing.T, s Transactional) {
		ctx := context.Background()
		if err := s.Txn(ctx, nil, []Op{put("/a/", `1`), put("/b/", `1`)}); err != nil {
			t.Fatal(err)
		}
		va, vb := versionOf(t, s, "/a/"), versionOf(t, s, "/b/")
		if va == 0 || va != vb {
			t.Fatalf("versions of /a/, /b/ = %d, %d; want one nonzero revision", va, vb)
		}
		// Put, Txn, and Delete followed by Put each write at a later revision
		mustPut(t, s, "/c/", `1`)
		vc := versionOf(t, s, "/c/")
		if err := s.Txn(ctx, nil, []Op{put("/a/", `2`)}); err != nil {
			t.Fatal(err)
		}
		va2 := versionOf(t, s, "/a/")
		if err := s.Delete(ctx, "/c/"); err != nil {
			t.Fatal(err)
		}
		mustPut(t, s, "/c/", `2`)
		vc2 := versionOf(t, s, "/c/")
		if va >= vc || vc >= va2 || va2 >= vc2 {
			t.Errorf("revisions %d, %d, %d, %d of successive writes do not increase", va, vc, va2, vc2)
		}
		if got := versionOf(t, s, "/b/"); got != vb {
			t.Errorf("/b/ version = %d after other keys changed, want %d", got, vb)
		}
	}},
	{"a failed check applies nothing", func(t *testing.T, s Transactional) {
		ctx := context.Background()
		mustPut(t, s, "/a/", `1`)
		mustPut(t, s, "/b/", `1`)
		va, staleB := versionOf(t, s, "/a/"), versionOf(t, s, "/b/")
		mustPut(t, s, "/b/", `2`)
		vb := versionOf(t, s, "/b/")

		err := s.Txn(ctx, []Check{{Key: "/a/", Version: va}, {Key: "/b/", Version: staleB}},
			[]Op{put("/a/", `9`), {Key: "/b/"}, put("/c/", `9`)})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("Txn with a stale check = %v, want ErrConflict", err)
		}
		if got, v := valueOf(t, s, "/a/"), versionOf(t, s, "/a/"); got != "1" || v != va {
			t.Errorf("/a/ = %s@%d, want 1@%d", got, v, va)
		}
		if got, v := valueOf(t, s, "/b/"), versionOf(t, s, "/b/"); got != "2" || v != vb {
			t.Errorf("/b/ = %s@%d, want 2@%d", got, v, vb)
		}
		if _, err := s.Get(ctx, "/c/"); !isNotFound(err) {
			t.Errorf("Get(/c/) = %v, want NotFoundError", err)
		}
	}},
	{"a check holds for a key that is only read", func(t *testing.T, s Transactional) {
		ctx := context.Background()
		mustPut(t, s, "/read/", `1`)
		vr := versionOf(t, s, "/read/")
		if err := s.Txn(ctx, []Check{{Key: "/read/", Version: vr}}, []Op{put("/write/", `1`)}); err != nil {
			t.Fatalf("Txn with a current check: %v", err)
		}
		mustPut(t, s, "/read/", `2`)
		if err := s.Txn(ctx, []Check{{Key: "/read/", Version: vr}}, []Op{put("/write/", `2`)}); !errors.Is(err, ErrConflict) {
			t.Fatalf("Txn after the read key changed = %v, want ErrConflict", err)
		}
		if got := valueOf(t, s, "/write/"); got != "1" {
			t.Errorf("/write/ = %s, want 1", got)
		}
	}},
	{"checks alone change nothing", func(t *testing.T, s Transactional) {
		ctx := context.Background()
		mustPut(t, s, "/a/", `1`)
		va := versionOf(t, s, "/a/")
		if err := s.Txn(ctx, []Check{{Key: "/a/", Version: va}}, nil); err != nil {
			t.Errorf("check-only Txn that holds = %v", err)
		}
		if err := s.Txn(ctx, []Check{{Key: "/a/", Version: va + 1}}, nil); !errors.Is(err, ErrConflict) {
			t.Errorf("check-only Txn that fails = %v, want ErrConflict", err)
		}
		if err := s.Txn(ctx, nil, nil); err != nil {
			t.Errorf("empty Txn = %v", err)
		}
		if got := versionOf(t, s, "/a/"); got != va {
			t.Errorf("/a/ version = %d, want %d", got, va)
		}
	}},
	{"deleting a missing key is not an error", func(t *testing.T, s Transactional) {
		if err := s.Txn(context.Background(), nil, []Op{{Key: "/missing/"}, put("/x/", `1`)}); err != nil {
			t.Fatalf("Txn: %v", err)
		}
		if got := valueOf(t, s, "/x/"); got != "1" {
			t.Errorf("/x/ = %q, want 1", got)
		}
	}},
	{"a later op on a key wins", func(t *testing.T, s Transactional) {
		ctx := context.Background()
		for _, tc := range []struct {
			ops  []Op
			want string // "" for deleted
		}{
			{[]Op{put("/k/", `1`), put("/k/", `2`)}, "2"},
			{[]Op{put("/k/", `3`), {Key: "/k/"}}, ""},
			{[]Op{{Key: "/k/"}, put("/k/", `4`)}, "4"},
		} {
			if err := s.Txn(ctx, nil, tc.ops); err != nil {
				t.Fatalf("Txn %v: %v", tc.ops, err)
			}
			e, err := s.Get(ctx, "/k/")
			switch {
			case tc.want == "" && !isNotFound(err):
				t.Errorf("after %v, Get = %v, want NotFoundError", tc.ops, err)
			case tc.want != "" && (err != nil || string(e.Value) != tc.want):
				t.Errorf("after %v, Get = %v, %v; want %s", tc.ops, e, err, tc.want)
			}
		}
	}},
	{"invalid keys fail the whole transaction", func(t *testing.T, s Transactional) {
		ctx := context.Background()
		err := s.Txn(ctx, nil, []Op{put("/ok/", `1`), put("/a/../b/", `1`)})
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Txn with a parent reference in an op = %v, want ErrInvalidPath", err)
		}
		if err := s.Txn(ctx, []Check{{Key: "/../x/"}}, []Op{put("/ok/", `1`)}); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Txn with a parent reference in a check = %v, want ErrInvalidPath", err)
		}
		if _, err := s.Get(ctx, "/ok/"); !isNotFound(err) {
			t.Errorf("Get(/ok/) = %v, want NotFoundError: an invalid transaction was applied", err)
		}
	}},
	{"Update serializes concurrent writers in one process", func(t *testing.T, s Transactional) {
		const n = 100
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// writers in one process take turns, so none conflicts
				if _, err := UpdateJSON(context.Background(), s, "/counter/", func(v *int) error {
					*v++
					return nil
				}); err != nil {
					t.Errorf("UpdateJSON: %v", err)
				}
			}()
		}
		wg.Wait()
		if got, want := valueOf(t, s, "/counter/"), strconv.Itoa(n); got != want {
			t.Errorf("counter = %s, want %s", got, want)
		}
	}},
}

func TestTxnContract(t *testing.T) {
	for name, open := range transactionalBackends() {
		t.Run(name, func(t *testing.T) {
			for _, c := range txnContract {
				t.Run(c.name, func(t *testing.T) { c.run(t, open(t)) })
			}
		})
	}
}

// TestUpdateAcrossServers increments one counter from two storages sharing
// their data, as two servers would; no increment may be lost.
func TestUpdateAcrossServers(t *testing.T) {
	for name, prepare := range sharedBackends() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			open := prepare(t)
			servers := []Storage{open(), open()}

			const perServer = 25
			var wg sync.WaitGroup
			errs := make(chan error, 2*perServer)
			for _, s := range servers {
				for range perServer {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, err := UpdateJSON(ctx, s, "/counter/", func(v *int) error {
							*v++
							return nil
						})
						errs <- err
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
			if got, want := valueOf(t, servers[0], "/counter/"), strconv.Itoa(2*perServer-conflicts); got != want {
				t.Errorf("counter = %s, want %s (%d updates gave up)", got, want, conflicts)
			}
		})
	}
}

// changeWithin is how long a Watch may take to report a change: well below
// any polling interval, as a backend reports changes as they commit.
const changeWithin = 2500 * time.Millisecond

// watchContract is what every backend that several servers can share must do
// as a Watchable. open opens another server's storage over the same data.
var watchContract = []struct {
	name string
	run  func(t *testing.T, open func() Storage)
}{
	{"reports later changes from another server, in order", func(t *testing.T, open func() Storage) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writer, watcher := open().(Transactional), open().(Watchable)

		mustPut(t, writer, "/before/", `{}`)
		changes := watcher.Watch(ctx)
		if err := writer.Txn(ctx, nil, []Op{put("/a/", `{}`), put("/b/", `{}`)}); err != nil {
			t.Fatal(err)
		}
		rev := versionOf(t, writer, "/a/")
		if err := writer.Delete(ctx, "/before/"); err != nil {
			t.Fatal(err)
		}

		got := receive(t, changes, 3)
		// the changes of one revision may come in any order
		slices.SortStableFunc(got[:2], func(a, b Change) int { return cmp.Compare(a.Key, b.Key) })
		want := []Change{{Rev: rev, Key: "/a/"}, {Rev: rev, Key: "/b/"}}
		if !slices.Equal(got[:2], want) {
			t.Errorf("changes of the transaction = %+v, want %+v", got[:2], want)
		}
		if got[2].Key != "/before/" || got[2].Rev <= rev {
			t.Errorf("change of the delete = %+v, want /before/ after revision %d", got[2], rev)
		}
	}},
	{"every watcher hears every change", func(t *testing.T, open func() Storage) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writer, other := open(), open().(Watchable)
		feeds := []<-chan Change{writer.(Watchable).Watch(ctx), writer.(Watchable).Watch(ctx), other.Watch(ctx)}
		mustPut(t, writer, "/a/", `{}`)
		for i, feed := range feeds {
			if c := receive(t, feed, 1)[0]; c.Key != "/a/" {
				t.Errorf("watcher %d: change = %+v, want /a/", i, c)
			}
		}
	}},
	{"is healthy while its feed keeps up", func(t *testing.T, open func() Storage) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := open()
		changes := s.(Watchable).Watch(ctx)
		mustPut(t, s, "/a/", `{}`)
		receive(t, changes, 1)
		if hr, ok := s.(HealthReporter); ok {
			if err := hr.Health(); err != nil {
				t.Errorf("Health = %v", err)
			}
		}
	}},
	{"stops when its context is canceled", func(t *testing.T, open func() Storage) {
		ctx, cancel := context.WithCancel(context.Background())
		changes := open().(Watchable).Watch(ctx)
		cancel()
		expectClosed(t, changes)
	}},
	{"stops when the storage is closed", func(t *testing.T, open func() Storage) {
		s := open()
		changes := s.(Watchable).Watch(context.Background())
		if err := s.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		expectClosed(t, changes)
	}},
}

func TestWatchContract(t *testing.T) {
	for name, prepare := range sharedBackends() {
		t.Run(name, func(t *testing.T) {
			for _, c := range watchContract {
				t.Run(c.name, func(t *testing.T) { c.run(t, prepare(t)) })
			}
		})
	}
}

// TestCacheableRefusesSharedBackends checks that cacheable will not cache a
// backend several servers share: another server's write would leave this
// server's cache stale.
func TestCacheableRefusesSharedBackends(t *testing.T) {
	for _, name := range []string{"postgres", "etcd"} {
		if _, ok := StorageOptions[name]; !ok {
			t.Fatalf("%s is not a storage type", name)
		}
		if _, err := NewCacheableStorageWithConf(map[string]string{"source": name}); err == nil {
			t.Errorf("expected an error for a %s cacheable source", name)
		}
	}
}

func put(key, value string) Op {
	return Op{Key: key, Value: json.RawMessage(value)}
}

func mustPut(t *testing.T, s Storage, key, value string) {
	t.Helper()
	if err := s.Put(context.Background(), StorageEntry{Key: key, Value: json.RawMessage(value)}); err != nil {
		t.Fatalf("Put %s: %v", key, err)
	}
}

// versionOf returns the version of the entry at key, or 0 when there is none.
func versionOf(t *testing.T, s Storage, key string) uint64 {
	t.Helper()
	e, err := s.Get(context.Background(), key)
	if isNotFound(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("Get %s: %v", key, err)
	}
	return e.Version
}

// valueOf returns the value of the entry at key, failing the test when there
// is none.
func valueOf(t *testing.T, s Storage, key string) string {
	t.Helper()
	e, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get %s: %v", key, err)
	}
	return string(e.Value)
}

// receive returns the next n changes, failing the test if any takes longer
// than changeWithin to arrive.
func receive(t *testing.T, changes <-chan Change, n int) []Change {
	t.Helper()
	out := make([]Change, 0, n)
	for len(out) < n {
		select {
		case c, ok := <-changes:
			if !ok {
				t.Fatalf("the feed closed after %d of %d changes: %+v", len(out), n, out)
			}
			out = append(out, c)
		case <-time.After(changeWithin):
			t.Fatalf("got %d of %d changes within %v: %+v", len(out), n, changeWithin, out)
		}
	}
	return out
}

// expectClosed fails the test unless changes closes promptly, reporting no
// further change.
func expectClosed(t *testing.T, changes <-chan Change) {
	t.Helper()
	select {
	case c, ok := <-changes:
		if ok {
			t.Fatalf("unexpected change %+v, want the feed closed", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the feed did not close")
	}
}

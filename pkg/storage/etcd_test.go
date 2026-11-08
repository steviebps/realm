package storage

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage/etcdtest"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func newEtcd(t *testing.T, conf map[string]string) *EtcdStorage {
	t.Helper()
	s, err := NewEtcdStorage(conf)
	if err != nil {
		t.Fatalf("NewEtcdStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s.(*EtcdStorage)
}

func TestEtcdOptions(t *testing.T) {
	for name, conf := range map[string]map[string]string{
		"no endpoints":              {},
		"blank endpoints":           {"endpoints": " , "},
		"parent reference":          {"endpoints": "127.0.0.1:1", "prefix": "/a/../b"},
		"certificate without a key": {"endpoints": "127.0.0.1:1", "cert_file": "client.pem"},
		"missing CA file":           {"endpoints": "127.0.0.1:1", "ca_file": "/nonexistent/ca.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewEtcdStorage(conf); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestEtcdUnreachable(t *testing.T) {
	old := etcdTimeout
	etcdTimeout = 500 * time.Millisecond
	t.Cleanup(func() { etcdTimeout = old })
	// nothing listens on port 1
	_, err := NewEtcdStorage(map[string]string{"endpoints": "127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "could not reach etcd") {
		t.Errorf("NewEtcdStorage = %v, want an error saying etcd is unreachable", err)
	}
}

// TestEtcdListPages lists more children than fit in one page, each with
// entries beneath it, and keys just outside the listed prefix.
func TestEtcdListPages(t *testing.T) {
	old := etcdListPage
	etcdListPage = 3
	t.Cleanup(func() { etcdListPage = old })

	ctx := context.Background()
	s := newEtcd(t, etcdtest.Options(t))
	want := []string{"."}
	mustPut(t, s, "/p/", `{}`)
	for i := range 7 {
		child := "c" + strconv.Itoa(i)
		want = append(want, child+"/")
		for _, k := range []string{"/p/" + child + "/", "/p/" + child + "/x/", "/p/" + child + "/x/y/", "/p/" + child + "/z/"} {
			mustPut(t, s, k, `{}`)
		}
	}
	// a sibling whose name extends the prefix's, and a child's name with a
	// byte below "/"
	mustPut(t, s, "/p0/", `{}`)
	mustPut(t, s, "/p/c-/", `{}`)
	want = append(want, "c-/")
	slices.Sort(want)

	got, err := s.List(ctx, "/p/")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("List(/p/) = %v, want %v", got, want)
	}
	if got, err := s.List(ctx, "/missing/"); err != nil || len(got) != 0 {
		t.Errorf("List(/missing/) = %v, %v; want nothing", got, err)
	}
}

// TestEtcdPrefixesAreSeparate checks that two installations sharing a
// cluster under different prefixes, one extending the other's name, see only
// their own entries and changes.
func TestEtcdPrefixesAreSeparate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := etcdtest.Options(t)
	a := newEtcd(t, conf)
	other := map[string]string{"endpoints": conf["endpoints"], "prefix": conf["prefix"] + "x"}
	b := newEtcd(t, other)
	t.Cleanup(func() {
		_, _ = b.client.Delete(context.Background(), other["prefix"]+"/", clientv3.WithPrefix())
	})

	changes := a.Watch(ctx)
	mustPut(t, b, "/shared/", `"b"`)
	mustPut(t, a, "/shared/", `"a"`)
	if got := valueOf(t, a, "/shared/"); got != `"a"` {
		t.Errorf("a's /shared/ = %s", got)
	}
	if got := valueOf(t, b, "/shared/"); got != `"b"` {
		t.Errorf("b's /shared/ = %s", got)
	}
	if names, err := a.List(ctx, "/"); err != nil || !slices.Equal(names, []string{"shared/"}) {
		t.Errorf("a's List(/) = %v, %v", names, err)
	}
	if c := receive(t, changes, 1)[0]; c.Key != "/shared/" || c.Rev != versionOf(t, a, "/shared/") {
		t.Errorf("a's change = %+v, want only its own write", c)
	}
}

// TestEtcdTxnTooLarge checks that a transaction beyond the cluster's
// operation limit fails with an error naming the limit, and applies nothing.
func TestEtcdTxnTooLarge(t *testing.T) {
	s := newEtcd(t, etcdtest.Options(t))
	ops := make([]Op, 0, 1000)
	for i := range cap(ops) {
		ops = append(ops, put("/k"+strconv.Itoa(i)+"/", `{}`))
	}
	err := s.Txn(context.Background(), nil, ops)
	if !errors.Is(err, ErrTooLarge) || !errors.Is(err, rpctypes.ErrTooManyOps) || !strings.Contains(err.Error(), "--max-txn-ops") {
		t.Fatalf("Txn with 1000 ops = %v, want ErrTooLarge naming --max-txn-ops", err)
	}
	if _, err := s.Get(context.Background(), "/k0/"); !isNotFound(err) {
		t.Errorf("Get(/k0/) = %v, want NotFoundError", err)
	}
}

// TestEtcdWatchAfterCompaction checks that a watch whose next revision was
// compacted away reports a change to "/", then carries on.
func TestEtcdWatchAfterCompaction(t *testing.T) {
	ctx := context.Background()
	s := newEtcd(t, etcdtest.Options(t))

	out := make(chan Change, 8)
	w := &etcdWatcher{s: s, out: out}
	mustPut(t, s, "/a/", `1`)
	w.last = versionOf(t, s, "/a/") - 1
	mustPut(t, s, "/a/", `2`)
	head := versionOf(t, s, "/a/")
	if _, err := s.client.Compact(ctx, int64(head)); err != nil {
		t.Fatal(err)
	}

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := w.watch(wctx); !errors.Is(err, rpctypes.ErrCompacted) || !w.lost {
		t.Fatalf("watch = %v (lost %v), want ErrCompacted with changes lost", err, w.lost)
	}
	done := make(chan error, 1)
	go func() { done <- w.watch(wctx) }()
	if c := <-out; c.Key != "/" || c.Rev < head {
		t.Errorf("change = %+v, want a change to / at revision %d or later", c, head)
	}
	mustPut(t, s, "/b/", `1`)
	if c := receive(t, out, 1)[0]; c.Key != "/b/" {
		t.Errorf("change after recovering = %+v, want /b/", c)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("watch after cancel = %v, want context.Canceled", err)
	}
}

// TestEtcdWatchAfterRewind checks that a watch that finds the cluster's
// revision below the one it confirmed, as after a restore from an older
// backup, reports a change to "/" and watches from the cluster's revision.
func TestEtcdWatchAfterRewind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldInterval, oldRetry := etcdProgressInterval, etcdRetryInterval
	etcdProgressInterval, etcdRetryInterval = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { etcdProgressInterval, etcdRetryInterval = oldInterval, oldRetry })

	s := newEtcd(t, etcdtest.Options(t))
	changes := s.Watch(ctx)
	s.mu.Lock()
	for w := range s.watchers {
		// as if the watch had seen revisions the cluster no longer has
		w.confirmed.Store(1 << 40)
	}
	s.mu.Unlock()

	if c := receive(t, changes, 1)[0]; c.Key != "/" || c.Rev >= 1<<40 {
		t.Fatalf("change = %+v, want a change to / at the cluster's revision", c)
	}
	mustPut(t, s, "/after/", `{}`)
	if c := receive(t, changes, 1)[0]; c.Key != "/after/" {
		t.Errorf("change after the rewind = %+v, want /after/", c)
	}
}

// TestEtcdWatchUnderWritesSeesNoRewind probes often while another server
// writes: the cluster's revision moving past the one read must never look
// like it went back, and every change must be reported once.
func TestEtcdWatchUnderWritesSeesNoRewind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldInterval := etcdProgressInterval
	etcdProgressInterval = 2 * time.Millisecond
	t.Cleanup(func() { etcdProgressInterval = oldInterval })

	conf := etcdtest.Options(t)
	watcher, writer := newEtcd(t, conf), newEtcd(t, conf)
	changes := watcher.Watch(ctx)
	const n = 300
	go func() {
		for i := range n {
			if err := writer.Put(ctx, StorageEntry{Key: "/k" + strconv.Itoa(i) + "/", Value: []byte(`{}`)}); err != nil {
				t.Errorf("Put: %v", err)
				return
			}
		}
	}()
	for i, c := range receive(t, changes, n) {
		if want := "/k" + strconv.Itoa(i) + "/"; c.Key != want {
			t.Fatalf("change %d = %+v, want %s", i, c, want)
		}
	}
}

// TestEtcdHealth checks that Health stays good while the cluster is quiet or
// busy with other keys, fails once the feed cannot reach the cluster, and
// recovers when it can again.
func TestEtcdHealth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldInterval, oldStale, oldTimeout := etcdProgressInterval, etcdFeedStaleAfter, etcdTimeout
	etcdProgressInterval, etcdFeedStaleAfter, etcdTimeout = 50*time.Millisecond, time.Second, 500*time.Millisecond
	t.Cleanup(func() { etcdProgressInterval, etcdFeedStaleAfter, etcdTimeout = oldInterval, oldStale, oldTimeout })

	conf := etcdtest.Options(t)
	px := newStallProxy(t, hostOf(t, strings.Split(conf["endpoints"], ",")[0]))
	conf["endpoints"] = "http://" + px.ln.Addr().String()
	s := newEtcd(t, conf)
	_ = s.Watch(ctx)
	var w *etcdWatcher
	s.mu.Lock()
	for w = range s.watchers {
	}
	s.mu.Unlock()

	// quiet: nothing was committed past what the feed has
	time.Sleep(2 * etcdFeedStaleAfter)
	if err := s.Health(); err != nil {
		t.Fatalf("Health of a quiet feed = %v", err)
	}

	// a write under another prefix advances the cluster's revision but sends
	// this feed no change: etcd must confirm the feed has it all
	other := newEtcd(t, etcdtest.Options(t))
	mustPut(t, other, "/elsewhere/", `{}`)
	rev, wrote := versionOf(t, other, "/elsewhere/"), time.Now()
	eventually(t, 2*time.Second, "the feed confirmed past another prefix's write", func() bool {
		return w.confirmed.Load() >= rev && w.caughtUp.Load() > wrote.UnixNano()
	})

	px.stall(true)
	eventually(t, 5*time.Second, "Health failing while etcd is unreachable", func() bool { return s.Health() != nil })
	px.resume()
	eventually(t, 20*time.Second, "Health recovering", func() bool { return s.Health() == nil })

	// and the feed still reports changes
	changes := s.Watch(ctx)
	mustPut(t, s, "/after/", `{}`)
	if c := receive(t, changes, 1)[0]; c.Key != "/after/" {
		t.Errorf("change after recovering = %+v, want /after/", c)
	}
}

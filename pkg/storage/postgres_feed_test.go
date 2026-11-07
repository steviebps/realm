package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/steviebps/realm/pkg/storage/pgtest"
)

// stallProxy forwards TCP connections to a database. Connections it stalls
// stop forwarding without closing, as they do when the server hangs or the
// network starts dropping packets.
type stallProxy struct {
	ln     net.Listener
	target string

	mu       sync.Mutex
	conns    []*proxyConn
	stallNew bool
}

type proxyConn struct {
	stalled, closed atomic.Bool
	client, server  net.Conn
}

func newStallProxy(t *testing.T, target string) *stallProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &stallProxy{ln: ln, target: target}
	go p.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		p.resume()
	})
	return p
}

func (p *stallProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = client.Close()
			continue
		}
		c := &proxyConn{client: client, server: server}
		p.mu.Lock()
		c.stalled.Store(p.stallNew)
		p.conns = append(p.conns, c)
		p.mu.Unlock()
		go c.pipe(server, client)
		go c.pipe(client, server)
	}
}

func (c *proxyConn) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		for n > 0 && c.stalled.Load() {
			if c.closed.Load() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			_ = dst.Close()
			return
		}
	}
}

// stall stops every open connection from forwarding, and new ones too when
// newToo is set.
func (p *stallProxy) stall(newToo bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.stalled.Store(true)
	}
	p.stallNew = newToo
}

// resume lets new connections through and closes the stalled ones, as TCP
// eventually would.
func (p *stallProxy) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stallNew = false
	open := p.conns[:0]
	for _, c := range p.conns {
		if !c.stalled.Load() {
			open = append(open, c)
			continue
		}
		c.closed.Store(true)
		_ = c.client.Close()
		_ = c.server.Close()
	}
	p.conns = open
}

// throughProxy returns rawURL with its host replaced by the proxy's.
func (p *stallProxy) throughProxy(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = p.ln.Addr().String()
	return u.String()
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// fastFeed shortens the feed's timings for the test. Call it before opening
// storage: storage is closed, and its watches stopped, before they are
// restored.
func fastFeed(t *testing.T) {
	t.Helper()
	poll, timeout, retry, maxRetry, stale := watchPollInterval, feedTimeout, watchRetryInterval, maxWatchRetryInterval, feedStaleAfter
	watchPollInterval, feedTimeout, watchRetryInterval, maxWatchRetryInterval, feedStaleAfter = 200*time.Millisecond, 500*time.Millisecond, 50*time.Millisecond, 200*time.Millisecond, time.Second
	t.Cleanup(func() {
		watchPollInterval, feedTimeout, watchRetryInterval, maxWatchRetryInterval, feedStaleAfter = poll, timeout, retry, maxRetry, stale
	})
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not %s within %v", what, within)
		}
	}
}

func expectChange(t *testing.T, changes <-chan Change, key string, within time.Duration) {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case c := <-changes:
			if c.Key == key || c.Key == "/" {
				return
			}
		case <-timeout:
			t.Fatalf("no change to %s within %v", key, within)
		}
	}
}

// TestPostgresWatchSurvivesSilentConnection stops the watch's connection
// from answering without closing it: the watch must notice, reconnect, and
// deliver the changes made meanwhile.
func TestPostgresWatchSurvivesSilentConnection(t *testing.T) {
	fastFeed(t)
	ctx := context.Background()
	base := pgtest.URL(t)
	px := newStallProxy(t, hostOf(t, base))
	writer := newPostgres(t, base)
	watcher := newPostgres(t, px.throughProxy(t, base))
	changes := watcher.Watch(ctx)

	put := func(key string) {
		t.Helper()
		if err := writer.Put(ctx, StorageEntry{Key: key, Value: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	put("/before/")
	expectChange(t, changes, "/before/", 5*time.Second)

	px.stall(false)
	put("/after/")
	expectChange(t, changes, "/after/", 10*time.Second)
}

// TestPostgresHealth checks that Health fails while a watch cannot catch up
// with the database, and recovers when it can again.
func TestPostgresHealth(t *testing.T) {
	fastFeed(t)
	ctx := context.Background()
	base := pgtest.URL(t)
	px := newStallProxy(t, hostOf(t, base))
	s := newPostgres(t, px.throughProxy(t, base))

	if err := s.Health(); err != nil {
		t.Fatalf("Health without a watch = %v", err)
	}
	changes := s.Watch(ctx)
	go func() {
		for range changes {
		}
	}()
	eventually(t, 5*time.Second, "healthy with a running watch", func() bool { return s.Health() == nil })

	px.stall(true)
	eventually(t, 10*time.Second, "unhealthy while the database does not answer", func() bool { return s.Health() != nil })
	t.Logf("Health while stalled: %v", s.Health())

	px.resume()
	eventually(t, 10*time.Second, "healthy again", func() bool { return s.Health() == nil })
}

// TestPostgresWatchAfterRevisionRegression checks a watch that saw a higher
// revision than the database now has (it was restored, or failed over to a
// replica that missed commits): it must report that anything may have
// changed, and then follow the database's revisions again.
func TestPostgresWatchAfterRevisionRegression(t *testing.T) {
	ctx := context.Background()
	s := newPostgres(t, pgtest.URL(t))
	put := func(key string) {
		t.Helper()
		if err := s.Put(ctx, StorageEntry{Key: key, Value: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		put(fmt.Sprintf("/k%d/", i))
	}

	out := make(chan Change, 8)
	w := &watcher{p: s, out: out, last: 10} // it saw revision 10 before the database went back to 3
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	next := func(want Change) {
		t.Helper()
		if err := w.catchUp(ctx, conn.Conn()); err != nil {
			t.Fatal(err)
		}
		select {
		case c := <-out:
			if c != want {
				t.Errorf("change = %+v, want %+v", c, want)
			}
		default:
			t.Fatalf("catchUp reported nothing, want %+v", want)
		}
	}
	next(Change{Rev: 3, Key: "/"})
	if w.last != 3 || w.seen("4") {
		t.Fatalf("after the regression, last = %d and revision 4 counts as seen: %v", w.last, w.seen("4"))
	}

	// later revisions are reported one by one again
	put("/next/")
	next(Change{Rev: 4, Key: "/next/"})
}

// TestPostgresSessionDefaults checks the timeouts realm gives its sessions,
// and that a setting from the connection string wins over them.
func TestPostgresSessionDefaults(t *testing.T) {
	ctx := context.Background()
	base := pgtest.URL(t)
	defaults := map[string]string{"idle_in_transaction_session_timeout": "30s", "lock_timeout": "15s", "statement_timeout": "30s"}
	tests := []struct {
		name string
		url  string
		want map[string]string
	}{
		{"defaults", base, defaults},
		{"set in the url", base + "&statement_timeout=5000", map[string]string{"idle_in_transaction_session_timeout": "30s", "lock_timeout": "15s", "statement_timeout": "5s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newPostgres(t, tt.url)
			for name, want := range tt.want {
				var got string
				if err := s.pool.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("%s = %s, want %s", name, got, want)
				}
			}
		})
	}
}

// TestPostgresIgnoresDefaultIsolation runs concurrent updates against a
// database whose sessions default to a stricter isolation level: realm's
// transactions are READ COMMITTED regardless, so none may fail with a
// serialization error.
func TestPostgresIgnoresDefaultIsolation(t *testing.T) {
	ctx := context.Background()
	for _, level := range []string{"repeatable read", "serializable"} {
		t.Run(level, func(t *testing.T) {
			s := newPostgres(t, pgtest.URL(t)+"&default_transaction_isolation="+url.PathEscape(level))
			var wg sync.WaitGroup
			var done, conflicts atomic.Int64
			errs := make(chan error, 40)
			for i := range 40 {
				wg.Go(func() {
					// four keys, so updates both share keys and do not
					_, err := UpdateJSON(ctx, s, fmt.Sprintf("/k%d/", i%4), func(n *int) error {
						*n++
						return nil
					})
					switch {
					case err == nil:
						done.Add(1)
					case errors.Is(err, ErrConflict):
						conflicts.Add(1)
					default:
						errs <- err
					}
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Errorf("UpdateJSON = %v", err)
			}
			total := 0
			for i := range 4 {
				var n int
				if _, err := ReadJSON(ctx, s, fmt.Sprintf("/k%d/", i), &n); err != nil {
					t.Fatal(err)
				}
				total += n
			}
			if int64(total) != done.Load() {
				t.Errorf("counters sum to %d after %d successful updates (%d gave up)", total, done.Load(), conflicts.Load())
			}
		})
	}
}

func TestPostgresConflictError(t *testing.T) {
	other := errors.New("connection reset")
	tests := []struct {
		name     string
		err      error
		conflict bool
	}{
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", fmt.Errorf("commit: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"other", other, false},
		{"none", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conflictError(tt.err)
			if errors.Is(err, ErrConflict) != tt.conflict {
				t.Errorf("conflictError(%v) = %v, want ErrConflict: %v", tt.err, err, tt.conflict)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Errorf("conflictError(%v) = %v, which hides the cause", tt.err, err)
			}
		})
	}
}

// TestPostgresStartsWithoutCreatePrivilege starts realm as a role that may
// read and write realm's tables but not create any, once the tables exist.
func TestPostgresStartsWithoutCreatePrivilege(t *testing.T) {
	ctx := context.Background()
	base := pgtest.URL(t)
	_ = newPostgres(t, base) // creates the tables

	exec := func(sql string) {
		t.Helper()
		conn, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	schema := u.Query().Get("search_path")
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	role := "realm_app_" + hex.EncodeToString(b)
	exec("CREATE ROLE " + role + " LOGIN")
	t.Cleanup(func() {
		if conn, err := pgx.Connect(ctx, base); err == nil {
			_, _ = conn.Exec(ctx, "DROP OWNED BY "+role)
			_, _ = conn.Exec(ctx, "DROP ROLE "+role)
			_ = conn.Close(ctx)
		}
	})
	exec("GRANT USAGE ON SCHEMA " + schema + " TO " + role)
	exec("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + schema + " TO " + role)

	u.User = url.User(role)
	s := newPostgres(t, u.String())
	if err := s.Put(ctx, StorageEntry{Key: "/a/", Value: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Put as a role without CREATE: %v", err)
	}
	if _, err := s.Get(ctx, "/a/"); err != nil {
		t.Fatalf("Get as a role without CREATE: %v", err)
	}
}

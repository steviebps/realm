package realm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/client"
)

const testTemplate = "/prod/tenants/{tenantId}/{locationId}/"

// tv is a view a test server sends: the chamber {"msg": msg} at path, or,
// with err, a view the server could not build.
type tv struct {
	path, msg string
	rev       uint64
	err       string
}

func (v tv) json() string {
	if v.err != "" {
		return fmt.Sprintf(`{"path":%q,"error":%q}`, v.path, v.err)
	}
	s := fmt.Sprintf(`{"path":%q,"rules":{"msg":{"type":"string","value":%q}}`, v.path, v.msg)
	if v.rev != 0 {
		s += fmt.Sprintf(`,"revision":%d`, v.rev)
	}
	return s + "}"
}

// subtreeBody is a subtree read's response.
func subtreeBody(views ...tv) string {
	parts := make([]string, len(views))
	for i, v := range views {
		parts[i] = v.json()
	}
	return `{"data":{"chambers":[` + strings.Join(parts, ",") + `]}}`
}

func chamberEvent(v tv) string {
	s := "event: chamber\n"
	if v.rev != 0 {
		s += fmt.Sprintf("id: %d\n", v.rev)
	}
	return s + "data: " + v.json()
}

func deletedEvent(path string) string {
	return fmt.Sprintf("event: deleted\ndata: {\"path\":%q}", path)
}

func syncedEvent(n int) string {
	return fmt.Sprintf("event: synced\ndata: {\"chambers\":%d}", n)
}

// subtreeServer answers subtree reads of /prod/tenants/ with its body, and
// hands subtree watches to its watch handler.
type subtreeServer struct {
	mu      sync.Mutex
	body    string
	watch   http.HandlerFunc
	gets    atomic.Int32
	watches atomic.Int32
	*httptest.Server
}

func newSubtreeServer(t *testing.T, body string, watch http.HandlerFunc) *subtreeServer {
	t.Helper()
	s := &subtreeServer{body: body, watch: watch}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/chambers/prod/tenants/" || q.Get("subtree") != "true" {
			http.Error(w, `{"errors":["unexpected request"]}`, http.StatusBadRequest)
			return
		}
		if q.Get("watch") == "true" {
			s.watches.Add(1)
			s.mu.Lock()
			watch := s.watch
			s.mu.Unlock()
			watch(w, r)
			return
		}
		s.gets.Add(1)
		s.mu.Lock()
		body := s.body
		s.mu.Unlock()
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *subtreeServer) set(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = body
}

func (s *subtreeServer) setWatch(watch http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watch = watch
}

// script is what one watch connection sends: events, then any sent on live
// until the request ends, or, without live, nothing more: the stream closes
// unless hold is set.
type script struct {
	events []string
	live   chan string
	hold   bool
}

// scripted serves the i-th watch connection the i-th script, and later ones
// the last.
func scripted(scripts ...script) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		s := scripts[min(int(n.Add(1))-1, len(scripts)-1)]
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range s.events {
			fmt.Fprintf(w, "%s\n\n", ev)
		}
		w.(http.Flusher).Flush()
		if s.live == nil {
			if s.hold {
				<-r.Context().Done()
			}
			return
		}
		for {
			select {
			case ev := <-s.live:
				fmt.Fprintf(w, "%s\n\n", ev)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

// subtreeRealm returns a Realm for testTemplate at address.
func subtreeRealm(t *testing.T, address string, logs *syncBuffer, options ...RealmOption) *Realm {
	t.Helper()
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	logger := zerolog.Nop()
	if logs != nil {
		logger = zerolog.New(logs).Level(zerolog.InfoLevel)
	}
	rlm, err := NewRealm(append([]RealmOption{WithHttpClient(c), WithPathTemplate(testTemplate), WithLogger(logger), WithPollingInterval(time.Second)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rlm.Stop)
	return rlm
}

// at returns the msg flag, the chamber path, and the revision a request
// with the given tenant and location gets.
func at(rlm *Realm, tenant, location string) (string, string, uint64) {
	attrs := map[string]string{}
	if tenant != "" {
		attrs["tenantId"] = tenant
	}
	if location != "" {
		attrs["locationId"] = location
	}
	ctx := rlm.NewContextWithEvaluation(context.Background(), EvaluationContext{Attributes: attrs})
	v, _ := rlm.String(ctx, "msg", "default")
	return v, rlm.ChamberPath(ctx), rlm.Revision(ctx)
}

// msgAt returns the msg flag a request with the given tenant and location
// gets.
func msgAt(rlm *Realm, tenant, location string) string {
	v, _, _ := at(rlm, tenant, location)
	return v
}

var (
	rootView   = tv{path: "/prod/tenants/", msg: "root", rev: 41}
	tenantView = tv{path: "/prod/tenants/t-1/", msg: "tenant", rev: 57}
	locView    = tv{path: "/prod/tenants/t-1/loc-1/", msg: "location", rev: 58}
)

// TestSubtreePolling verifies a polled subtree is read whole and replaced
// whole, and how requests resolve against it.
func TestSubtreePolling(t *testing.T) {
	srv := newSubtreeServer(t, subtreeBody(rootView, tenantView, locView), nil)
	rlm := subtreeRealm(t, srv.URL, nil)
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		tenant, location string
		msg, path        string
		rev              uint64
	}{
		{"t-1", "loc-1", "location", "/prod/tenants/t-1/loc-1/", 58},
		{"t-1", "loc-2", "tenant", "/prod/tenants/t-1/", 57},
		{"t-2", "loc-1", "root", "/prod/tenants/", 41},
		{"", "", "root", "/prod/tenants/", 41},
	}
	for _, tt := range tests {
		msg, path, rev := at(rlm, tt.tenant, tt.location)
		if msg != tt.msg || path != tt.path || rev != tt.rev {
			t.Errorf("%s/%s: got %q at %s (revision %d), want %q at %s (revision %d)", tt.tenant, tt.location, msg, path, rev, tt.msg, tt.path, tt.rev)
		}
	}

	// the location is deleted, and the tenant changed
	srv.set(subtreeBody(rootView, tv{path: "/prod/tenants/t-1/", msg: "tenant 2", rev: 60}))
	if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "loc-1") == "tenant 2" }) {
		msg, path, _ := at(rlm, "t-1", "loc-1")
		t.Fatalf("got %q at %s after a poll, want the changed tenant's", msg, path)
	}
}

// TestSubtreeErrorsKeepViews verifies a view the server could not build
// keeps the one realm already holds for its path, polled or streamed.
func TestSubtreeErrorsKeepViews(t *testing.T) {
	t.Run("polled", func(t *testing.T) {
		srv := newSubtreeServer(t, subtreeBody(rootView, tenantView, locView), nil)
		logs := &syncBuffer{}
		rlm := subtreeRealm(t, srv.URL, logs)
		ctx := context.Background()
		if err := rlm.load(ctx); err != nil {
			t.Fatal(err)
		}
		srv.set(subtreeBody(
			tv{path: "/prod/tenants/", msg: "root 2"},
			tv{path: "/prod/tenants/t-1/", err: "could not parse chamber"},
			tv{path: "/prod/tenants/t-1/loc-1/", err: "could not parse parent chamber /prod/tenants/t-1/"},
			tv{path: "/prod/tenants/t-2/", err: "could not parse chamber"},
		))
		if err := rlm.load(ctx); err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct{ tenant, location, want string }{
			{"t-1", "loc-1", "location"},
			{"t-1", "", "tenant"},
			{"t-2", "", "root 2"},
		} {
			if got := msgAt(rlm, tt.tenant, tt.location); got != tt.want {
				t.Errorf("%s/%s: msg = %q, want %q", tt.tenant, tt.location, got, tt.want)
			}
		}
		if logs.count("could not build some chamber views") != 1 {
			t.Errorf("the views that could not be built were not logged once: %s", logs.buf.String())
		}
	})

	t.Run("streamed", func(t *testing.T) {
		live := make(chan string)
		srv := newSubtreeServer(t, subtreeBody(rootView), scripted(script{
			events: []string{chamberEvent(rootView), chamberEvent(tenantView), syncedEvent(2)},
			live:   live,
		}))
		rlm := subtreeRealm(t, srv.URL, nil, WithStreaming(true))
		if err := rlm.Start(); err != nil {
			t.Fatal(err)
		}
		if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "") == "tenant" }) {
			t.Fatal("the stream was not applied")
		}
		live <- chamberEvent(tv{path: "/prod/tenants/t-1/", err: "could not parse chamber"})
		live <- chamberEvent(tv{path: "/prod/tenants/", msg: "root 2"})
		if !eventually(5*time.Second, func() bool { return msgAt(rlm, "", "") == "root 2" }) {
			t.Fatal("the stream stopped")
		}
		if got := msgAt(rlm, "t-1", ""); got != "tenant" {
			t.Errorf("msg = %q, want the last view kept", got)
		}
	})
}

// TestSubtreeLoadRejectsNonSubtrees verifies an answer to a subtree read
// that is not a subtree fails the refresh and keeps the snapshot.
func TestSubtreeLoadRejectsNonSubtrees(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		notSubtree bool
	}{
		{"a chamber", `{"data":{"rules":{"msg":{"type":"string","value":"x"}}}}`, true},
		{"no chambers", `{"data":{}}`, true},
		{"null data", `{"data":null}`, true},
		{"no data", `{}`, true},
		{"chambers not a list", `{"data":{"chambers":{}}}`, true},
		{"an empty subtree", `{"data":{"chambers":[]}}`, true},
		{"without its root", subtreeBody(tenantView), true},
		{"an entry without a path", `{"data":{"chambers":[{"path":"/prod/tenants/","rules":{}},{"rules":{}}]}}`, true},
		{"cut short", strings.TrimSuffix(subtreeBody(rootView, tenantView), "]}}"), false},
		{"not JSON", `<html>sign in</html>`, false},
		{"errors", `{"errors":["boom"],"data":{"chambers":[{"path":"/prod/tenants/","rules":{}}]}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newSubtreeServer(t, subtreeBody(rootView, tenantView), nil)
			rlm := subtreeRealm(t, srv.URL, nil)
			ctx := context.Background()
			if err := rlm.load(ctx); err != nil {
				t.Fatal(err)
			}
			srv.set(tt.body)
			err := rlm.load(ctx)
			if err == nil {
				t.Fatal("the refresh succeeded")
			}
			if errors.Is(err, errNotSubtree) != tt.notSubtree {
				t.Errorf("err = %v; want errNotSubtree: %v", err, tt.notSubtree)
			}
			if got := msgAt(rlm, "t-1", ""); got != "tenant" {
				t.Errorf("msg = %q, want the snapshot kept", got)
			}
		})
	}

	t.Run("a lenient reader", func(t *testing.T) {
		srv := newSubtreeServer(t, `{"Data":{"next":[1,{"a":null}],"CHAMBERS":[{"path":"prod/tenants","rules":null,"revision":"x","new":1}]},"extra":{"b":[]}}`, nil)
		rlm := subtreeRealm(t, srv.URL, nil)
		if err := rlm.load(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, path, _ := at(rlm, "", ""); path != "/prod/tenants/" {
			t.Errorf("path = %q, want the root", path)
		}
	})
}

// TestBoundedReader verifies a subtree read longer than its bound fails.
func TestBoundedReader(t *testing.T) {
	tests := []struct {
		body string
		n    int64
		err  error
	}{
		{"abc", 3, nil},
		{"abcd", 3, errTooLarge},
		{"", 3, nil},
	}
	for _, tt := range tests {
		b, err := io.ReadAll(&boundedReader{r: strings.NewReader(tt.body), n: tt.n})
		if !errors.Is(err, tt.err) || (err == nil && string(b) != tt.body) {
			t.Errorf("%q bounded at %d: %q, %v; want %v", tt.body, tt.n, b, err, tt.err)
		}
	}
}

// TestSubtreeStreaming verifies a watched subtree is applied once synced,
// then changed by each chamber and deleted event.
func TestSubtreeStreaming(t *testing.T) {
	live := make(chan string)
	srv := newSubtreeServer(t, subtreeBody(tv{path: "/prod/tenants/", msg: "polled"}), scripted(script{
		events: []string{chamberEvent(rootView), chamberEvent(tenantView)},
		live:   live,
	}))
	logs := &syncBuffer{}
	rlm := subtreeRealm(t, srv.URL, logs, WithStreaming(true), WithPollingInterval(time.Hour))
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := msgAt(rlm, "t-1", ""); got != "polled" {
		t.Fatalf("msg = %q before the stream synced, want the loaded snapshot", got)
	}

	live <- syncedEvent(2)
	if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "") == "tenant" }) {
		t.Fatalf("msg = %q, want the streamed snapshot", msgAt(rlm, "t-1", ""))
	}
	if _, _, rev := at(rlm, "t-1", ""); rev != 57 {
		t.Errorf("revision = %d, want the event's id", rev)
	}

	steps := []struct {
		event            string
		tenant, location string
		msg, path        string
	}{
		{chamberEvent(locView), "t-1", "loc-1", "location", "/prod/tenants/t-1/loc-1/"},
		{deletedEvent("/prod/tenants/t-1/"), "t-1", "", "root", "/prod/tenants/"},
		// a deeper view still applies though the one between is gone
		{"event: revision\ndata: " + tv{path: "/prod/tenants/t-1/loc-1/", msg: "ignored"}.json(), "t-1", "loc-1", "location", "/prod/tenants/t-1/loc-1/"},
		{"data: " + tv{path: "/prod/tenants/t-1/loc-1/", msg: "unnamed"}.json(), "t-1", "loc-1", "location", "/prod/tenants/t-1/loc-1/"},
		{"event: chamber\ndata: {\"path\":", "t-1", "loc-1", "location", "/prod/tenants/t-1/loc-1/"},
		{chamberEvent(tv{path: "/prod/tenants/t-2/", msg: "tenant 2", rev: 70}), "t-2", "", "tenant 2", "/prod/tenants/t-2/"},
	}
	for i, s := range steps {
		live <- s.event
		if !eventually(5*time.Second, func() bool {
			msg, path, _ := at(rlm, s.tenant, s.location)
			return msg == s.msg && path == s.path
		}) {
			msg, path, _ := at(rlm, s.tenant, s.location)
			t.Fatalf("step %d: got %q at %s, want %q at %s", i, msg, path, s.msg, s.path)
		}
	}
	if n := srv.gets.Load(); n != 1 {
		t.Errorf("%d subtree reads while the stream was up, want only the first load", n)
	}
	if n := logs.count("skipped a streamed chamber"); n != 1 {
		t.Errorf("%d lines for the event that could not be read, want 1", n)
	}
}

// TestSubtreeResyncs verifies the paths a stream does not send again, on a
// reconnect or a resync, are dropped once it is synced, unless some views
// could not be built.
func TestSubtreeResyncs(t *testing.T) {
	t2 := tv{path: "/prod/tenants/t-2/", msg: "tenant 2"}
	first := []string{chamberEvent(rootView), chamberEvent(tenantView), chamberEvent(t2), syncedEvent(3)}

	t.Run("a reconnect", func(t *testing.T) {
		shortTimings(t)
		srv := newSubtreeServer(t, subtreeBody(rootView), scripted(
			script{events: first},
			script{events: []string{chamberEvent(rootView), chamberEvent(tenantView), syncedEvent(2)}, hold: true},
		))
		rlm := subtreeRealm(t, srv.URL, nil, WithStreaming(true))
		if err := rlm.Start(); err != nil {
			t.Fatal(err)
		}
		if !eventually(5*time.Second, func() bool { return srv.watches.Load() >= 2 }) {
			t.Fatal("no reconnect")
		}
		if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-2", "") == "root" }) {
			t.Errorf("msg = %q, want the tenant deleted while disconnected dropped", msgAt(rlm, "t-2", ""))
		}
		if got := msgAt(rlm, "t-1", ""); got != "tenant" {
			t.Errorf("msg = %q, want the tenant kept", got)
		}
	})

	t.Run("a reconnect with views that could not be built", func(t *testing.T) {
		shortTimings(t)
		srv := newSubtreeServer(t, subtreeBody(rootView), scripted(
			script{events: first},
			script{events: []string{chamberEvent(tv{path: "/prod/tenants/", msg: "root 2"}), chamberEvent(tenantView), syncedEvent(3)}, hold: true},
		))
		rlm := subtreeRealm(t, srv.URL, nil, WithStreaming(true))
		if err := rlm.Start(); err != nil {
			t.Fatal(err)
		}
		if !eventually(5*time.Second, func() bool { return msgAt(rlm, "", "") == "root 2" }) {
			t.Fatal("the second stream was not applied")
		}
		if got := msgAt(rlm, "t-2", ""); got != "tenant 2" {
			t.Errorf("msg = %q, want the view not sent kept: the server counted one it could not build", got)
		}
	})

	t.Run("a resync", func(t *testing.T) {
		live := make(chan string)
		srv := newSubtreeServer(t, subtreeBody(rootView), scripted(script{events: first, live: live}))
		rlm := subtreeRealm(t, srv.URL, nil, WithStreaming(true))
		if err := rlm.Start(); err != nil {
			t.Fatal(err)
		}
		if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-2", "") == "tenant 2" }) {
			t.Fatal("the stream was not applied")
		}
		live <- chamberEvent(tv{path: "/prod/tenants/t-3/", msg: "tenant 3"})
		live <- deletedEvent("/prod/tenants/t-3/")
		live <- chamberEvent(rootView)
		live <- chamberEvent(t2)
		live <- syncedEvent(2)
		if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "") == "root" }) {
			t.Errorf("msg = %q, want the tenant the resync left out dropped", msgAt(rlm, "t-1", ""))
		}
		if got := msgAt(rlm, "t-2", ""); got != "tenant 2" {
			t.Errorf("msg = %q, want the resent tenant kept", got)
		}
	})
}

// TestSubtreeStreamFallsBack verifies realm polls the subtree when the
// server answers a subtree watch with the subtree (it does not stream), and
// tries the stream again later.
func TestSubtreeStreamFallsBack(t *testing.T) {
	shortTimings(t)
	srv := newSubtreeServer(t, subtreeBody(rootView, tenantView), nil)
	srv.setWatch(func(w http.ResponseWriter, r *http.Request) {
		srv.mu.Lock()
		body := srv.body
		srv.mu.Unlock()
		fmt.Fprint(w, body)
	})
	rlm := subtreeRealm(t, srv.URL, nil, WithStreaming(true))
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	srv.set(subtreeBody(rootView, tv{path: "/prod/tenants/t-1/", msg: "polled"}))
	if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "") == "polled" }) {
		t.Fatalf("not polled; msg = %q", msgAt(rlm, "t-1", ""))
	}
	srv.setWatch(scripted(script{events: []string{chamberEvent(rootView), chamberEvent(tv{path: "/prod/tenants/t-1/", msg: "streamed"}), syncedEvent(2)}, hold: true}))
	if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "") == "streamed" }) {
		t.Fatalf("the stream was not tried again; msg = %q", msgAt(rlm, "t-1", ""))
	}
}

// TestSubtreeStreamRejectsNonSubtrees verifies a server that answers a
// subtree watch with a single chamber, streamed or not, is logged as one
// that does not serve subtrees, and leaves the snapshot alone.
func TestSubtreeStreamRejectsNonSubtrees(t *testing.T) {
	tests := []struct {
		name  string
		watch http.HandlerFunc
	}{
		{"a chamber stream", scripted(script{events: []string{"event: chamber\ndata: " + chamberJSON("one")}, hold: true})},
		{"a deleted event without a path", scripted(script{events: []string{"event: deleted\ndata: {}"}, hold: true})},
		{"a chamber", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"data":%s}`, chamberJSON("one"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shortTimings(t)
			srv := newSubtreeServer(t, subtreeBody(rootView, tenantView), tt.watch)
			logs := &syncBuffer{}
			rlm := subtreeRealm(t, srv.URL, logs, WithStreaming(true))
			if err := rlm.Start(); err != nil {
				t.Fatal(err)
			}
			if !eventually(5*time.Second, func() bool { return logs.count("something other than a subtree") > 0 }) {
				t.Fatalf("not logged: %s", logs.buf.String())
			}
			// realm waits minutes before asking again
			time.Sleep(300 * time.Millisecond)
			if n := srv.watches.Load(); n != 1 {
				t.Errorf("%d watches, want 1", n)
			}
			if got := msgAt(rlm, "t-1", ""); got != "tenant" {
				t.Errorf("msg = %q, want the loaded snapshot kept", got)
			}
		})
	}
}

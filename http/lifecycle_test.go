package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
)

// TestSlowRequestBodyIsCutOff verifies that a client trickling its request
// body loses the connection once the request timeout passes, instead of
// holding it (and a goroutine) for as long as it likes.
func TestSlowRequestBodyIsCutOff(t *testing.T) {
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg, RequestTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// promise 100 bytes, send one
	fmt.Fprint(conn, "POST /v1/chambers/a/ HTTP/1.1\r\nHost: realm\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err = io.ReadAll(conn) // the response, if any, then EOF when the server hangs up
	if err != nil {
		t.Fatalf("the server held the connection for %v: %v", time.Since(start), err)
	}
}

// TestStalledStreamReaderLosesStream verifies that a watch stream whose client
// stops reading ends once a write cannot complete, freeing its slot.
func TestStalledStreamReaderLosesStream(t *testing.T) {
	old := streamWriteTimeout
	streamWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { streamWriteTimeout = old })

	brk := newBroker(0)
	// large enough to fill the socket buffers of a client that does not read
	payload := bytes.Repeat([]byte("x"), 32<<20)
	srv := httptest.NewServer(withLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamEvents(w, r, brk, "/a/", "chamber", false, func(context.Context) ([]byte, time.Time, error) {
			return payload, time.Time{}, nil
		})
	}), logging.New(io.Discard), time.Second))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /v1/chambers/a/?watch=true HTTP/1.1\r\nHost: realm\r\n\r\n")

	open := func() int {
		brk.mu.Lock()
		defer brk.mu.Unlock()
		return len(brk.subs)
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("the stream never opened", func() bool { return open() == 1 })
	waitFor("the stream outlived a client that stopped reading", func() bool { return open() == 0 })
}

func TestRequestIDs(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t))
	t.Cleanup(srv.Close)

	tests := []struct {
		name, sent string
		echoed     bool
	}{
		{"made when missing", "", false},
		{"echoed from a proxy", "req-123:abc", true},
		{"replaced when unsafe", "bad id\twith spaces", false},
		{"replaced when too long", strings.Repeat("a", maxRequestIDLength+1), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := map[string]string{}
			if test.sent != "" {
				headers[RequestIDHeader] = test.sent
			}
			res, _ := do(t, http.MethodGet, srv.URL+"/v1/chambers/missing/", "", headers)
			got := res.Header.Get(RequestIDHeader)
			if got == "" {
				t.Fatal("no request ID in the response")
			}
			if (got == test.sent) != test.echoed {
				t.Errorf("request ID = %q, sent %q", got, test.sent)
			}
		})
	}
}

// lockedBuffer is a bytes.Buffer safe for a logger and a test to share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// lines returns the JSON log lines written so far.
func (l *lockedBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(l.b.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestPanicsAreRecoveredAndLogged(t *testing.T) {
	var logs lockedBuffer
	srv := httptest.NewServer(withLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/late" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial"))
		}
		panic("boom")
	}), logging.New(&logs), time.Second))
	t.Cleanup(srv.Close)

	res, out := do(t, http.MethodGet, srv.URL+"/early", "", nil)
	if res.StatusCode != http.StatusInternalServerError || len(out.Errors) != 1 {
		t.Fatalf("panic before the response = %d %v, want 500 with an error", res.StatusCode, out.Errors)
	}

	// a panic after the response began aborts it (on a fresh connection, so
	// the client does not retry the request)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	if res, err := client.Get(srv.URL + "/late"); err == nil {
		_, err = io.ReadAll(res.Body)
		res.Body.Close()
		if err == nil {
			t.Error("a response aborted by a panic read as complete")
		}
	}

	var panics, accesses int
	for _, line := range logs.lines(t) {
		switch line["message"] {
		case "panic serving request":
			panics++
			if line["panic"] != "boom" || line["stack"] == "" || line["request_id"] == "" {
				t.Errorf("panic log = %v", line)
			}
		case "request":
			accesses++
			if line["status"] != float64(http.StatusInternalServerError) && line["path"] == "/early" {
				t.Errorf("access log = %v", line)
			}
		}
	}
	if panics != 2 || accesses != 2 {
		t.Errorf("logged %d panics and %d requests, want 2 and 2", panics, accesses)
	}
}

func TestAccessLogLevels(t *testing.T) {
	var logs lockedBuffer
	status := map[string]int{"/read": 200, "/missing": 404, "/denied": 403, "/broken": 500}
	h := withLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info := requestInfoFrom(r.Context()); info != nil {
			info.actor = "key:ci"
		}
		code, ok := status[r.URL.Path]
		if !ok {
			code = http.StatusNoContent
		}
		w.WriteHeader(code)
	}), logging.New(&logs), time.Second)

	for _, req := range []struct{ method, path, level string }{
		{http.MethodGet, "/read", "debug"},
		{http.MethodGet, "/missing", "debug"},
		{http.MethodPost, "/write", "info"},
		{http.MethodPost, "/v1/evaluate/web/", "debug"}, // evaluation reads
		{http.MethodGet, "/denied", "warn"},
		{http.MethodPost, "/broken", "error"},
	} {
		logs.mu.Lock()
		logs.b.Reset()
		logs.mu.Unlock()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(req.method, req.path, nil))
		lines := logs.lines(t)
		if len(lines) != 1 {
			t.Fatalf("%s %s logged %d lines", req.method, req.path, len(lines))
		}
		if got := lines[0]["level"]; got != req.level || lines[0]["actor"] != "key:ci" || lines[0]["status"] == nil {
			t.Errorf("%s %s logged %v, want level %s", req.method, req.path, lines[0], req.level)
		}
	}
}

// countingStorage counts reads of each key and can delay or fail them.
type countingStorage struct {
	storage.Storage
	mu    sync.Mutex
	gets  map[string]int
	delay time.Duration
	fail  atomic.Bool
}

func (c *countingStorage) Get(ctx context.Context, key string) (*storage.StorageEntry, error) {
	c.mu.Lock()
	if c.gets == nil {
		c.gets = map[string]int{}
	}
	c.gets[key]++
	c.mu.Unlock()
	if c.fail.Load() {
		return nil, errors.New("storage unavailable")
	}
	time.Sleep(c.delay)
	return c.Storage.Get(ctx, key)
}

func (c *countingStorage) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets[key]
}

func (c *countingStorage) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets = nil
}

// TestStreamsShareReads verifies that a write wakes every stream on a path
// without each reading storage itself.
func TestStreamsShareReads(t *testing.T) {
	raw, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	stg := &countingStorage{Storage: raw, delay: 50 * time.Millisecond}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	putChamber(t, srv.URL, "a", `{"rules":{"f":{"type":"string","value":"one"}}}`)

	const streams = 20
	var all []<-chan string
	for range streams {
		res, err := http.Get(srv.URL + "/v1/chambers/a/?watch=true")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		events := dataEvents(bufio.NewScanner(res.Body))
		waitData(t, events, 5*time.Second)
		all = append(all, events)
	}

	stg.reset()
	putChamber(t, srv.URL, "a", `{"rules":{"f":{"type":"string","value":"two"}}}`)
	for _, events := range all {
		if got := waitData(t, events, 5*time.Second); !strings.Contains(got, `"two"`) {
			t.Fatalf("stream got %s", got)
		}
	}
	// the write itself reads the chamber once, to check it
	if reads := stg.count("/a/"); reads > 3 {
		t.Errorf("%d streams made %d reads of the changed chamber, want them to share", streams, reads)
	}
}

// gatedStorage blocks its first read until release is closed, returning the
// value stored when the read began.
type gatedStorage struct {
	storage.Storage
	calls   atomic.Int32
	release chan struct{}
}

func (g *gatedStorage) Get(ctx context.Context, key string) (*storage.StorageEntry, error) {
	e, err := g.Storage.Get(ctx, key)
	if g.calls.Add(1) == 1 {
		<-g.release
	}
	return e, err
}

// TestSharedReadsNeverServeAReadBegunBeforeAChange verifies that a stream
// woken by a change does not share a read that started before it.
func TestSharedReadsNeverServeAReadBegunBeforeAChange(t *testing.T) {
	ctx := context.Background()
	raw, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Put(ctx, storage.StorageEntry{Key: "/a/", Value: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	gated := &gatedStorage{Storage: raw, release: make(chan struct{})}
	brk := newBroker(0)
	reads := &sharedReads{strg: gated, brk: brk, timeout: 5 * time.Second}

	before := make(chan string, 1)
	go func() {
		e, err := reads.Get(ctx, "/a/")
		if err != nil {
			before <- err.Error()
			return
		}
		before <- string(e.Value)
	}()
	for gated.calls.Load() == 0 { // let the first read begin and block
		time.Sleep(time.Millisecond)
	}

	if err := raw.Put(ctx, storage.StorageEntry{Key: "/a/", Value: []byte("new")}); err != nil {
		t.Fatal(err)
	}
	brk.Notify("/a/")

	e, err := reads.Get(ctx, "/a/")
	if err != nil {
		t.Fatal(err)
	}
	if string(e.Value) != "new" {
		t.Errorf("a read after the change returned %q", e.Value)
	}
	close(gated.release)
	if got := <-before; got != "old" {
		t.Errorf("the read begun before the change returned %q", got)
	}
}

// TestStreamsSurviveStorageFailures verifies that a stream whose payload
// cannot be produced stays open and delivers it once it can.
func TestStreamsSurviveStorageFailures(t *testing.T) {
	brk := newBroker(0)
	var failing atomic.Bool
	var value atomic.Value
	value.Store("one")
	srv := httptest.NewServer(withLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamEvents(w, r, brk, "/a/", "chamber", false, func(context.Context) ([]byte, time.Time, error) {
			if failing.Load() {
				return nil, time.Time{}, errors.New("storage unavailable")
			}
			return []byte(value.Load().(string)), time.Time{}, nil
		})
	}), logging.New(io.Discard), time.Second))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/chambers/a/?watch=true")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	events := dataEvents(bufio.NewScanner(res.Body))
	if got := waitData(t, events, 2*time.Second); got != "one" {
		t.Fatalf("first event = %q", got)
	}

	failing.Store(true)
	value.Store("two")
	brk.Notify("/a/")
	time.Sleep(100 * time.Millisecond)
	failing.Store(false)
	// no further change is announced: the stream retries on its own
	if got := waitData(t, events, 5*time.Second); got != "two" {
		t.Fatalf("event after recovery = %q", got)
	}
}

// TestClientErrorsAreNotLoggedAsErrors verifies that a client's mistake (a
// read of a chamber that does not exist) yields one access log line with the
// reason, and no error line, while a server error is logged as one.
func TestClientErrorsAreNotLoggedAsErrors(t *testing.T) {
	var logs lockedBuffer
	raw, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	stg := &countingStorage{Storage: raw}
	ctx := logging.New(&logs).WithContext(context.Background())
	h, err := NewHandler(ctx, HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	levels := func(path string) (map[string]int, string) {
		t.Helper()
		logs.mu.Lock()
		logs.b.Reset()
		logs.mu.Unlock()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		got := map[string]int{}
		reason := ""
		for _, line := range logs.lines(t) {
			got[line["level"].(string)]++
			if line["message"] == "request" {
				reason, _ = line["error"].(string)
			}
		}
		return got, reason
	}

	// (storage logs its own debug lines)
	got, reason := levels("/v1/chambers/missing/")
	if got["error"] != 0 || got["warn"] != 0 || !strings.Contains(reason, "missing") {
		t.Errorf("missing chamber logged %v with reason %q, want no errors and the reason in the access line", got, reason)
	}

	stg.fail.Store(true)
	got, _ = levels("/v1/chambers/missing/")
	if got["error"] != 2 {
		t.Errorf("storage failure logged %v, want the failure and the access line as errors", got)
	}
}

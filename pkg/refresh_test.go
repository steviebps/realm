package realm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
)

// shortTimings shortens realm's retry and stream timings for a test.
func shortTimings(t *testing.T) {
	t.Helper()
	backoff, idle, down, fallback := retryBackoff, streamIdleTimeout, streamDownAfter, streamRetryAfterFallback
	retryBackoff, streamIdleTimeout, streamDownAfter, streamRetryAfterFallback = 20*time.Millisecond, 300*time.Millisecond, time.Second, time.Second
	t.Cleanup(func() {
		retryBackoff, streamIdleTimeout, streamDownAfter, streamRetryAfterFallback = backoff, idle, down, fallback
	})
}

// testRealm returns a started-ready Realm reading the chamber "root" from
// address, logging to logs (when not nil) at Info and above.
func testRealm(t *testing.T, address string, logs *syncBuffer, options ...RealmOption) *Realm {
	t.Helper()
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	logger := zerolog.Nop()
	if logs != nil {
		logger = zerolog.New(logs).Level(zerolog.InfoLevel)
	}
	rlm, err := NewRealm(append([]RealmOption{WithHttpClient(c), WithPath("root"), WithLogger(logger), WithPollingInterval(time.Second)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rlm.Stop)
	return rlm
}

func msg(rlm *Realm) string {
	v, _ := rlm.String(context.Background(), "msg", "default")
	return v
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// count returns how many lines contain s.
func (b *syncBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

// chamberServer serves the chamber {"msg": value} to GETs and hands watches
// to the current watch handler.
type chamberServer struct {
	mu    sync.Mutex
	value string
	watch http.HandlerFunc
	gets  atomic.Int32
	*httptest.Server
}

func newChamberServer(t *testing.T, value string, watch http.HandlerFunc) *chamberServer {
	s := &chamberServer{value: value, watch: watch}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			s.mu.Lock()
			watch := s.watch
			s.mu.Unlock()
			watch(w, r)
			return
		}
		s.gets.Add(1)
		fmt.Fprintf(w, `{"data":%s}`, chamberJSON(s.get()))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *chamberServer) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

func (s *chamberServer) set(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
}

func (s *chamberServer) setWatch(watch http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watch = watch
}

// streamOf streams the chamber {"msg": value}, which plain GETs do not serve,
// then holds the stream open.
func streamOf(value string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON(value))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
}

// TestStreamPollsWhileDown verifies that while the stream delivers nothing,
// realm polls at the polling interval, and stops once the stream is back.
func TestStreamPollsWhileDown(t *testing.T) {
	tests := []struct {
		name  string
		watch http.HandlerFunc
	}{
		{"refused", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"errors":["too many open streams; try again later"]}`)
		}},
		{"buffered by a proxy", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shortTimings(t)
			srv := newChamberServer(t, "one", tt.watch)
			rlm := testRealm(t, srv.URL, nil, WithStreaming(true))
			if err := rlm.Start(); err != nil {
				t.Fatal(err)
			}
			srv.set("two")
			if !eventually(5*time.Second, func() bool { return msg(rlm) == "two" }) {
				t.Fatalf("a change was not polled while the stream was down; msg = %q", msg(rlm))
			}

			srv.setWatch(streamOf("streamed"))
			if !eventually(5*time.Second, func() bool { return msg(rlm) == "streamed" }) {
				t.Fatalf("the stream did not recover; msg = %q", msg(rlm))
			}
			gets := srv.gets.Load()
			time.Sleep(2500 * time.Millisecond)
			if n := srv.gets.Load(); n != gets {
				t.Errorf("polled %d more times after the stream was back", n-gets)
			}
		})
	}
}

// TestStreamHonorsRetryAfter verifies a 503's Retry-After delays the next
// attempt beyond the backoff.
func TestStreamHonorsRetryAfter(t *testing.T) {
	shortTimings(t)
	var mu sync.Mutex
	var attempts []time.Time
	srv := newChamberServer(t, "one", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		mu.Unlock()
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	rlm := testRealm(t, srv.URL, nil, WithStreaming(true))
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	if !eventually(4*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(attempts) >= 2 }) {
		t.Fatal("the stream was not retried")
	}
	mu.Lock()
	defer mu.Unlock()
	if gap := attempts[1].Sub(attempts[0]); gap < 1900*time.Millisecond {
		t.Errorf("retried after %v, before Retry-After's 2s", gap)
	}
}

// TestStreamFallsBackOnlyForChambers verifies realm polls instead of
// streaming only when the server answers a watch with a chamber, and asks for
// the stream again later; any other answer is a failed attempt.
func TestStreamFallsBackOnlyForChambers(t *testing.T) {
	t.Run("a server that does not stream", func(t *testing.T) {
		shortTimings(t)
		var watches atomic.Int32
		srv := newChamberServer(t, "one", nil)
		srv.setWatch(func(w http.ResponseWriter, r *http.Request) {
			watches.Add(1)
			fmt.Fprintf(w, `{"data":%s}`, chamberJSON(srv.get()))
		})
		rlm := testRealm(t, srv.URL, nil, WithStreaming(true))
		if err := rlm.Start(); err != nil {
			t.Fatal(err)
		}
		srv.set("two")
		if !eventually(5*time.Second, func() bool { return msg(rlm) == "two" }) {
			t.Fatalf("not polled; msg = %q", msg(rlm))
		}
		srv.setWatch(streamOf("streamed"))
		if !eventually(5*time.Second, func() bool { return msg(rlm) == "streamed" }) {
			t.Fatalf("the stream was not tried again; msg = %q", msg(rlm))
		}
	})

	t.Run("a sign-in page", func(t *testing.T) {
		shortTimings(t)
		var watches atomic.Int32
		srv := newChamberServer(t, "one", func(w http.ResponseWriter, r *http.Request) {
			watches.Add(1)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html>sign in</html>")
		})
		rlm := testRealm(t, srv.URL, nil, WithStreaming(true))
		if err := rlm.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		// streamRetryAfterFallback (1s here) would allow one or two attempts
		if n := watches.Load(); n < 4 {
			t.Errorf("only %d stream attempts in a second: the page was taken for a server that does not stream", n)
		}
		if got := msg(rlm); got != "one" {
			t.Errorf("msg = %q, want the loaded chamber kept", got)
		}
	})
}

// TestStreamAppliesOnlyChamberEvents verifies events of other types, and
// data that is not a chamber, are skipped without touching the snapshot.
func TestStreamAppliesOnlyChamberEvents(t *testing.T) {
	srv := newChamberServer(t, "zero", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON("one"))
		for _, ev := range []string{
			`event: revision` + "\n" + `data: {"revision":42}`,
			`event: revision` + "\n" + `data: ` + chamberJSON("other event"),
			`data: null`,
			`data: {"foo":1}`,
			`data: [1]`,
			`data: "text"`,
			`event: chamber` + "\n" + `data: {"rules":`,
		} {
			fmt.Fprintf(w, "%s\n\n", ev)
		}
		// an unnamed event, split over two data lines
		fmt.Fprintf(w, "data: {\"rules\":\ndata: %s}\n\n", `{"msg":{"type":"string","value":"two"}}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	rlm := testRealm(t, srv.URL, nil, WithStreaming(true))
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool { return msg(rlm) == "two" }) {
		t.Fatalf("msg = %q, want two", msg(rlm))
	}
	// the load, "one", and "two": nothing in between replaced the snapshot
	if v := rlm.snapshotVersion(); v != 3 {
		t.Errorf("the snapshot was replaced %d times, want 3", v)
	}
}

// TestLoadRejectsNonChambers verifies a response that does not hold a
// chamber fails the refresh and keeps the snapshot.
func TestLoadRejectsNonChambers(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		accept bool
	}{
		{"null data", `{"data":null}`, false},
		{"no data", `{}`, false},
		{"empty object", `{"data":{}}`, false},
		{"another object", `{"data":{"revision":42}}`, false},
		{"an array", `{"data":[1]}`, false},
		{"bad rules", `{"data":{"rules":5}}`, false},
		{"not an envelope", `"text"`, false},
		{"not JSON", `<html>sign in</html>`, false},
		{"errors", `{"errors":["boom"],"data":{"rules":{}}}`, false},
		{"an empty chamber", `{"data":{"rules":{}}}`, true},
		{"null rules", `{"data":{"rules":null}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					fmt.Fprintf(w, `{"data":%s}`, chamberJSON("kept"))
					return
				}
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			rlm := testRealm(t, srv.URL, nil)
			ctx := context.Background()
			if err := rlm.load(ctx); err != nil {
				t.Fatal(err)
			}
			err := rlm.load(ctx)
			if tt.accept {
				if err != nil || msg(rlm) != "default" {
					t.Errorf("err = %v, msg = %q; want the empty chamber applied", err, msg(rlm))
				}
				return
			}
			if err == nil {
				t.Error("the refresh succeeded")
			}
			if got := msg(rlm); got != "kept" {
				t.Errorf("msg = %q, want the snapshot kept", got)
			}
		})
	}
}

// TestLoadBoundsResponses verifies an oversized response is refused rather
// than read whole.
func TestLoadBoundsResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":{"rules":{"msg":{"type":"string","value":"%s"}}}}`, strings.Repeat("a", maxChamberBytes))
	}))
	defer srv.Close()
	rlm := testRealm(t, srv.URL, nil)
	if err := rlm.load(context.Background()); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v, want the response refused for its size", err)
	}
}

// TestRefusalsBackOffForMinutes verifies a 401 is not retried within
// seconds, by polling or by streaming.
func TestRefusalsBackOffForMinutes(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			shortTimings(t)
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"errors":["invalid API key"]}`)
			}))
			defer srv.Close()
			rlm := testRealm(t, srv.URL, nil, WithStreaming(streaming))
			if err := rlm.Start(); err == nil {
				t.Fatal("Start succeeded with a refused key")
			}
			// Polling every second, or a stream down for a second (as here)
			// and polled, would retry several times in two seconds.
			time.Sleep(1500 * time.Millisecond)
			before := requests.Load()
			time.Sleep(2 * time.Second)
			if n := requests.Load(); n != before {
				t.Errorf("%d more requests in 2s after %d", n-before, before)
			}
		})
	}
}

// TestFailuresAreLoggedOnce verifies a failure that repeats is logged when it
// starts and when it ends, not on every retry.
func TestFailuresAreLoggedOnce(t *testing.T) {
	shortTimings(t)
	var up atomic.Bool
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"data":%s}`, chamberJSON("one"))
	}))
	defer srv.Close()
	logs := &syncBuffer{}
	rlm := testRealm(t, srv.URL, logs)
	if err := rlm.Start(); err == nil {
		t.Fatal("Start succeeded against a failing server")
	}
	if !eventually(5*time.Second, func() bool { return requests.Load() >= 5 }) {
		t.Fatal("not retried")
	}
	up.Store(true)
	if !eventually(5*time.Second, func() bool { return msg(rlm) == "one" }) {
		t.Fatal("did not recover")
	}
	if n := logs.count("could not load flags"); n != 1 {
		t.Errorf("%d failure lines for %d failures, want 1:\n%s", n, requests.Load()-1, logs.buf.String())
	}
	if n := logs.count("flags loaded after earlier failures"); n != 1 {
		t.Errorf("%d recovery lines, want 1", n)
	}
}

// TestDefaultLogger verifies the default logger writes Info and above,
// without caller paths, and leaves zerolog's global level alone.
func TestDefaultLogger(t *testing.T) {
	global := zerolog.GlobalLevel()
	var buf bytes.Buffer
	l := newDefaultLogger(&buf)
	l.Debug().Msg("hidden")
	l.Info().Msg("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "shown") {
		t.Errorf("wrote %q, want Info and above", out)
	}
	if strings.Contains(out, `"caller"`) || !strings.Contains(out, `"component":"realm"`) {
		t.Errorf("wrote %q, want no caller and a component", out)
	}
	if zerolog.GlobalLevel() != global {
		t.Error("changed zerolog's global level")
	}
}

// TestStopCancelsTheFirstLoad verifies Stop does not wait for a first load
// that hangs.
func TestStopCancelsTheFirstLoad(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer srv.Close()
			defer close(release)
			rlm := testRealm(t, srv.URL, nil, WithStreaming(streaming))
			started := make(chan error, 1)
			go func() { started <- rlm.Start() }()
			time.Sleep(100 * time.Millisecond)
			begin := time.Now()
			rlm.Stop()
			if took := time.Since(begin); took > time.Second {
				t.Errorf("Stop took %v", took)
			}
			if err := <-started; err == nil {
				t.Error("Start succeeded although Stop canceled its load")
			}
			if err := rlm.Start(); !errors.Is(err, errStopped) {
				t.Errorf("Start after Stop = %v, want errStopped", err)
			}
		})
	}
}

// TestRepeatedStartKeepsNewerSnapshot verifies a chamber a repeated Start
// fetched never replaces a newer one that arrived meanwhile.
func TestRepeatedStartKeepsNewerSnapshot(t *testing.T) {
	var gets atomic.Int32
	var arrived sync.Once
	getArrived, releaseGet, releaseWatch := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			select {
			case <-releaseWatch:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON("new"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		switch gets.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			// read before the stream's change, answered after it
			arrived.Do(func() { close(getArrived) })
			<-releaseGet
			fmt.Fprintf(w, `{"data":%s}`, chamberJSON("old"))
		}
	}))
	t.Cleanup(srv.Close) // after the realm stops, closing its stream
	rlm := testRealm(t, srv.URL, nil, WithStreaming(true))
	if err := rlm.Start(); err == nil {
		t.Fatal("the first Start succeeded")
	}
	again := make(chan error, 1)
	go func() { again <- rlm.Start() }()
	<-getArrived
	close(releaseWatch)
	if !eventually(5*time.Second, func() bool { return msg(rlm) == "new" }) {
		t.Fatalf("the stream did not deliver; msg = %q", msg(rlm))
	}
	close(releaseGet)
	if err := <-again; err != nil {
		t.Fatalf("repeated Start: %v", err)
	}
	if got := msg(rlm); got != "new" {
		t.Errorf("msg = %q: the repeated Start replaced a newer snapshot", got)
	}
}

// TestPollingIntervalIsClamped verifies NewRealm's polling interval bounds.
func TestPollingIntervalIsClamped(t *testing.T) {
	tests := []struct {
		given, want time.Duration
	}{
		{0, DefaultPollingInterval},
		{-time.Second, DefaultPollingInterval},
		{time.Nanosecond, MinPollingInterval},
		{999 * time.Millisecond, MinPollingInterval},
		{time.Second, time.Second},
		{2 * time.Minute, 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.given.String(), func(t *testing.T) {
			c, _ := client.NewHttpClient(&client.HttpClientConfig{Address: "http://localhost"})
			rlm, err := NewRealm(WithHttpClient(c), WithPath("root"), WithPollingInterval(tt.given))
			if err != nil {
				t.Fatal(err)
			}
			if rlm.pollingInterval != tt.want {
				t.Errorf("polling interval = %v, want %v", rlm.pollingInterval, tt.want)
			}
		})
	}
}

// TestRetryDelay verifies how long realm waits after each kind of failure.
func TestRetryDelay(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		min, max time.Duration
	}{
		{"connection refused", errors.New("connection refused"), retryBackoff / 2, retryBackoff},
		{"server error", &statusError{code: 500}, retryBackoff / 2, retryBackoff},
		{"Retry-After", fmt.Errorf("wrapped: %w", &statusError{code: 503, retryAfter: 7 * time.Second}), 7 * time.Second, 7 * time.Second},
		{"unauthorized", &statusError{code: 401}, slowRetryBackoff / 2, slowRetryBackoff},
		{"forbidden", &statusError{code: 403}, slowRetryBackoff / 2, slowRetryBackoff},
		{"not found", &statusError{code: 404}, slowRetryBackoff / 2, slowRetryBackoff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if d := newRetryPolicy(maxRetryBackoff).delay(tt.err); d < tt.min || d > tt.max {
				t.Errorf("delay = %v, want within [%v, %v]", d, tt.min, tt.max)
			}
		})
	}
}

// TestRetryAfter verifies how a Retry-After header is read.
func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header string
		min    time.Duration
		max    time.Duration
	}{
		{"seconds", 503, "5", 5 * time.Second, 5 * time.Second},
		{"too many requests", 429, "3", 3 * time.Second, 3 * time.Second},
		{"none", 503, "", 0, 0},
		{"negative", 503, "-3", 0, 0},
		{"capped", 503, "999999999999", maxRetryAfter, maxRetryAfter},
		{"a date", 503, time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), 55 * time.Second, time.Minute},
		{"a past date", 503, time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), 0, 0},
		{"garbage", 503, "soon", 0, 0},
		{"another status", 500, "5", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &http.Response{StatusCode: tt.status, Header: http.Header{}}
			if tt.header != "" {
				res.Header.Set("Retry-After", tt.header)
			}
			if d := retryAfter(res); d < tt.min || d > tt.max {
				t.Errorf("retryAfter = %v, want within [%v, %v]", d, tt.min, tt.max)
			}
		})
	}
}

// TestFailureLog verifies which failures, and recoveries, are logged.
func TestFailureLog(t *testing.T) {
	start := time.Now()
	type step struct {
		ok  bool          // a success, else a failure
		at  time.Duration // when a failure happens, after start
		log bool          // whether it is logged
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"the first of a run, and its end", []step{{at: 0, log: true}, {at: time.Second}, {at: 2 * time.Second}, {ok: true, log: true}}},
		{"again after the interval", []step{{at: 0, log: true}, {at: repeatLogInterval, log: true}, {at: repeatLogInterval + time.Second}}},
		{"coming and going", []step{{at: 0, log: true}, {ok: true, log: true}, {at: time.Second}, {ok: true}, {at: repeatLogInterval, log: true}, {ok: true, log: true}}},
		{"no failures", []step{{ok: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var l failureLog
			for i, s := range tt.steps {
				var logged bool
				if s.ok {
					_, logged = l.ok()
				} else {
					logged, _ = l.fail(start.Add(s.at))
				}
				if logged != s.log {
					t.Errorf("step %d logged = %v, want %v", i, logged, s.log)
				}
			}
		})
	}
}

// TestFlappingStreamLogsSparingly verifies a stream that keeps dropping right
// after it connects is not logged on every reconnect.
func TestFlappingStreamLogsSparingly(t *testing.T) {
	shortTimings(t)
	var watches atomic.Int32
	srv := newChamberServer(t, "one", func(w http.ResponseWriter, r *http.Request) {
		watches.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON("one"))
		w.(http.Flusher).Flush()
		// then drop the connection mid-stream
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	logs := &syncBuffer{}
	rlm := testRealm(t, srv.URL, logs, WithStreaming(true))
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool { return watches.Load() >= 10 }) {
		t.Fatalf("only %d reconnects", watches.Load())
	}
	if n := logs.count("chamber stream unavailable"); n != 1 {
		t.Errorf("%d failure lines for %d drops, want 1", n, watches.Load())
	}
	if n := logs.count("connected again"); n > 1 {
		t.Errorf("%d recovery lines, want at most 1", n)
	}
}

// TestStreamedRevisions verifies that each streamed chamber takes the
// revision of its own event's id, and none when the event has no usable id.
func TestStreamedRevisions(t *testing.T) {
	next := make(chan string)
	srv := newChamberServer(t, "zero", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			select {
			case ev := <-next:
				fmt.Fprintf(w, "%s\n\n", ev)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	rlm := testRealm(t, srv.URL, nil, WithStreaming(true))
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := rlm.Revision(ctx); got != 0 {
		t.Errorf("revision of a load without the header = %d, want 0", got)
	}
	tests := []struct {
		event string
		want  uint64
	}{
		{"event: chamber\nid: 7\ndata: " + chamberJSON("one"), 7},
		// the id does not carry over to the next event
		{"event: chamber\ndata: " + chamberJSON("two"), 0},
		{"id: 9\nevent: chamber\ndata: " + chamberJSON("three"), 9},
		{"event: chamber\nid: soon\ndata: " + chamberJSON("four"), 0},
		{"event: chamber\nid: 18446744073709551615\ndata: " + chamberJSON("five"), 18446744073709551615},
	}
	for _, tt := range tests {
		next <- tt.event
		value := strings.TrimSuffix(strings.SplitN(tt.event, `"value":"`, 2)[1], `"}}}`)
		if !eventually(5*time.Second, func() bool { return msg(rlm) == value }) {
			t.Fatalf("msg = %q, want %q", msg(rlm), value)
		}
		if got := rlm.Revision(ctx); got != tt.want {
			t.Errorf("after %q: revision = %d, want %d", value, got, tt.want)
		}
	}
}

// TestLoadedRevision verifies that a polled chamber takes the revision the
// server reports in its header.
func TestLoadedRevision(t *testing.T) {
	var rev atomic.Value
	rev.Store("12")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(api.RevisionHeader, rev.Load().(string))
		fmt.Fprintf(w, `{"data":%s}`, chamberJSON("one"))
	}))
	t.Cleanup(srv.Close)
	rlm := testRealm(t, srv.URL, nil)
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := rlm.Revision(ctx); got != 12 {
		t.Errorf("revision = %d, want 12", got)
	}
	// an unchanged chamber at a newer revision still updates it
	rev.Store("13")
	if !eventually(5*time.Second, func() bool { return rlm.Revision(ctx) == 13 }) {
		t.Errorf("revision = %d, want 13", rlm.Revision(ctx))
	}
}

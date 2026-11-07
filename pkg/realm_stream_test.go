package realm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/steviebps/realm/client"
)

func chamberJSON(v string) string {
	return fmt.Sprintf(`{"rules":{"msg":{"type":"string","value":%q}}}`, v)
}

func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func newStreamingRealm(t *testing.T, address string, pollingInterval time.Duration) *Realm {
	t.Helper()
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: address})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	rlm, err := NewRealm(WithHttpClient(c), WithPath("root"), WithStreaming(true), WithPollingInterval(pollingInterval))
	if err != nil {
		t.Fatalf("realm: %v", err)
	}
	return rlm
}

// TestRealmStreamingAppliesPushedUpdates verifies the SDK applies a chamber
// pushed over SSE without waiting for the polling interval (set to 1h here).
func TestRealmStreamingAppliesPushedUpdates(t *testing.T) {
	var mu sync.Mutex
	value := "one"
	get := func() string { mu.Lock(); defer mu.Unlock(); return value }

	update := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			f, ok := w.(http.Flusher)
			if !ok {
				t.Error("expected a flushable ResponseWriter")
				return
			}
			fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON(get()))
			f.Flush()
			select {
			case <-update:
				fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON(get()))
				f.Flush()
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
			return
		}
		// normal GET used by Start()'s initial synchronous load
		fmt.Fprintf(w, `{"data":%s}`, chamberJSON(get()))
	}))
	defer srv.Close()

	rlm := newStreamingRealm(t, srv.URL, time.Hour)
	if err := rlm.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer rlm.Stop()

	if got, _ := rlm.String(context.Background(), "msg", "default"); got != "one" {
		t.Fatalf("expected initial value 'one', got %q", got)
	}

	mu.Lock()
	value = "two"
	mu.Unlock()
	close(update)

	if !eventually(2*time.Second, func() bool {
		got, _ := rlm.String(context.Background(), "msg", "default")
		return got == "two"
	}) {
		t.Fatal("streamed update was not applied to the local snapshot")
	}
}

// TestRealmStreamingFallsBackToPolling verifies that when the server does not
// return an event stream, the SDK converges via polling instead.
func TestRealmStreamingFallsBackToPolling(t *testing.T) {
	var mu sync.Mutex
	value := "one"
	get := func() string { mu.Lock(); defer mu.Unlock(); return value }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ignore the watch parameter entirely (simulating an older server) and
		// always return a normal JSON response.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":%s}`, chamberJSON(get()))
	}))
	defer srv.Close()

	rlm := newStreamingRealm(t, srv.URL, MinPollingInterval)
	if err := rlm.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer rlm.Stop()

	if got, _ := rlm.String(context.Background(), "msg", "default"); got != "one" {
		t.Fatalf("expected initial value 'one', got %q", got)
	}

	mu.Lock()
	value = "two"
	mu.Unlock()

	if !eventually(5*time.Second, func() bool {
		got, _ := rlm.String(context.Background(), "msg", "default")
		return got == "two"
	}) {
		t.Fatal("expected polling fallback to pick up the update")
	}
}

// TestRealmStopIsIdempotent verifies Stop can be called more than once (for
// example a deferred Stop plus an explicit one) without panicking.
func TestRealmStopIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":%s}`, chamberJSON("one"))
	}))
	defer srv.Close()

	rlm := newStreamingRealm(t, srv.URL, time.Hour)
	if err := rlm.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	rlm.Stop()
	rlm.Stop()
}

// TestRealmRecoversWhenServerIsDownAtStart verifies Start reports the failed
// first load but keeps retrying, so the flags load once the server is up.
func TestRealmRecoversWhenServerIsDownAtStart(t *testing.T) {
	var mu sync.Mutex
	up := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"errors":["Service Unavailable"]}`)
			return
		}
		fmt.Fprintf(w, `{"data":%s}`, chamberJSON("one"))
	}))
	defer srv.Close()

	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			mu.Lock()
			up = false
			mu.Unlock()
			c, err := client.NewHttpClient(&client.HttpClientConfig{Address: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			rlm, err := NewRealm(WithHttpClient(c), WithPath("root"), WithStreaming(streaming), WithPollingInterval(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			defer rlm.Stop()
			if err := rlm.Start(); err == nil {
				t.Fatal("Start succeeded against a server that is down")
			}
			if _, err := rlm.String(context.Background(), "msg", "default"); err != ErrChamberEmpty {
				t.Fatalf("before a load, err = %v, want ErrChamberEmpty", err)
			}
			mu.Lock()
			up = true
			mu.Unlock()
			if !eventually(5*time.Second, func() bool {
				got, _ := rlm.String(context.Background(), "msg", "default")
				return got == "one"
			}) {
				t.Fatal("realm did not recover after the server came up")
			}
		})
	}
}

// TestRealmStreamRetriesAfterServerError verifies a 503 on the watch request
// (a proxy during a deploy, say) is retried as a stream, not treated as a
// server without streaming.
func TestRealmStreamRetriesAfterServerError(t *testing.T) {
	var mu sync.Mutex
	watches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") != "true" {
			fmt.Fprintf(w, `{"data":%s}`, chamberJSON("one"))
			return
		}
		mu.Lock()
		watches++
		n := watches
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON("streamed"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	rlm := newStreamingRealm(t, srv.URL, time.Hour)
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	defer rlm.Stop()
	if !eventually(5*time.Second, func() bool {
		got, _ := rlm.String(context.Background(), "msg", "default")
		return got == "streamed"
	}) {
		t.Fatal("the stream was not retried after a 503")
	}
}

// TestRealmStreamReconnectsWhenSilent verifies a stream that stops sending
// anything (heartbeats included) is reopened.
func TestRealmStreamReconnectsWhenSilent(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })

	var mu sync.Mutex
	watches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") != "true" {
			fmt.Fprintf(w, `{"data":%s}`, chamberJSON("one"))
			return
		}
		mu.Lock()
		watches++
		n := watches
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n > 1 {
			fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", chamberJSON("fresh"))
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done() // then silence
	}))
	defer srv.Close()

	rlm := newStreamingRealm(t, srv.URL, time.Hour)
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	defer rlm.Stop()
	if !eventually(5*time.Second, func() bool {
		got, _ := rlm.String(context.Background(), "msg", "default")
		return got == "fresh"
	}) {
		t.Fatal("a silent stream was not reopened")
	}
}

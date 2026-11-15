package realm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// gzipServer answers subtree reads of /prod/tenants/ with read and subtree
// watches with the events of watch, then those sent on live, all gzipped
// as realm's server gzips them: each batch flushed through, the stream held
// open until the request ends. It records each request's Accept-Encoding.
type gzipServer struct {
	mu             sync.Mutex
	acceptEncoding []string
	*httptest.Server
}

func newGzipServer(t *testing.T, read string, watch []string, live <-chan string) *gzipServer {
	t.Helper()
	s := &gzipServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.acceptEncoding = append(s.acceptEncoding, r.Header.Get("Accept-Encoding"))
		s.mu.Unlock()
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		if r.URL.Query().Get("watch") != "true" {
			fmt.Fprint(gz, read)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flush := func() {
			_ = gz.Flush()
			w.(http.Flusher).Flush()
		}
		for _, ev := range watch {
			fmt.Fprintf(gz, "%s\n\n", ev)
		}
		flush()
		for {
			select {
			case ev := <-live:
				fmt.Fprintf(gz, "%s\n\n", ev)
				flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// TestSubtreeGzip verifies a Realm asks for gzipped subtree reads and
// watches, and applies each event of a gzipped stream as soon as the
// server flushes it, while the stream stays open.
func TestSubtreeGzip(t *testing.T) {
	live := make(chan string)
	srv := newGzipServer(t, subtreeBody(tv{path: "/prod/tenants/", msg: "polled"}),
		[]string{chamberEvent(rootView), chamberEvent(tenantView), syncedEvent(2)}, live)
	rlm := subtreeRealm(t, srv.URL, nil, WithStreaming(true), WithPollingInterval(time.Hour))
	if err := rlm.Start(); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "") == "tenant" }) {
		t.Fatalf("msg = %q, want the streamed snapshot", msgAt(rlm, "t-1", ""))
	}
	for _, ev := range []tv{locView, {path: "/prod/tenants/t-1/loc-1/", msg: "changed", rev: 59}} {
		live <- chamberEvent(ev)
		if !eventually(5*time.Second, func() bool { return msgAt(rlm, "t-1", "loc-1") == ev.msg }) {
			t.Fatalf("msg = %q, want %q from the open stream", msgAt(rlm, "t-1", "loc-1"), ev.msg)
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.acceptEncoding) < 2 {
		t.Fatalf("%d requests, want a read and a watch", len(srv.acceptEncoding))
	}
	for i, ae := range srv.acceptEncoding {
		if !strings.Contains(ae, "gzip") {
			t.Errorf("request %d: Accept-Encoding %q", i, ae)
		}
	}
}

// gzipped returns s gzipped.
func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if _, err := io.WriteString(gz, s); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestSubtreeReadBoundsDecompressedBytes verifies a gzipped subtree read
// is bounded by what it inflates to, not by what is sent.
func TestSubtreeReadBoundsDecompressedBytes(t *testing.T) {
	old := maxSubtreeBytes
	maxSubtreeBytes = 64 << 10
	t.Cleanup(func() { maxSubtreeBytes = old })

	tests := []struct {
		name    string
		msg     string
		tooLong bool
	}{
		{"within the bound", strings.Repeat("a", 32<<10), false},
		{"past the bound", strings.Repeat("a", 1<<20), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := gzipped(t, subtreeBody(tv{path: "/prod/tenants/", msg: tt.msg}))
			if int64(len(body)) >= maxSubtreeBytes {
				t.Fatalf("the gzipped body is %d bytes, want it under the bound", len(body))
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			rlm := subtreeRealm(t, srv.URL, nil)
			err := rlm.load(context.Background())
			if got := errors.Is(err, errTooLarge); got != tt.tooLong {
				t.Fatalf("load: %v; want too large: %v", err, tt.tooLong)
			}
			if !tt.tooLong && msgAt(rlm, "", "") != tt.msg {
				t.Error("the view was not applied")
			}
		})
	}
}

// TestSubtreeStreamBoundsDecompressedLines verifies a gzipped subtree
// stream is bounded line by line by what it inflates to: a line longer
// than maxChamberBytes ends the stream, though it is sent in a few
// kilobytes.
func TestSubtreeStreamBoundsDecompressedLines(t *testing.T) {
	huge := "event: chamber\ndata: " + strings.Repeat("a", maxChamberBytes+1)
	srv := newGzipServer(t, subtreeBody(rootView), []string{chamberEvent(rootView), huge}, nil)
	rlm := subtreeRealm(t, srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := rlm.consumeStream(ctx, func() {})
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("consumeStream: %v, want the line refused", err)
	}
}

package http

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage"
)

// TestChamberWritesRejectUnknownFields verifies that a misspelled field
// anywhere in a chamber is refused rather than silently dropped.
func TestChamberWritesRejectUnknownFields(t *testing.T) {
	srv := newInheritableTestServer(t)

	tests := []struct {
		name, body string
		want       int
	}{
		{"valid", `{"rules":{"f":{"type":"boolean","value":false,"targeting":[{"value":true,"schedule":{"start":"2030-01-01T00:00:00Z"}}]}}}`, http.StatusCreated},
		{"chamber field", `{"rulez":{"f":{"type":"boolean","value":true}}}`, http.StatusBadRequest},
		{"flag field", `{"rules":{"f":{"type":"boolean","value":true,"targets":[]}}}`, http.StatusBadRequest},
		{"rule field", `{"rules":{"f":{"type":"boolean","value":false,"targeting":[{"value":true,"scheduled":{"start":"2030-01-01T00:00:00Z"}}]}}}`, http.StatusBadRequest},
		{"clause field", `{"rules":{"f":{"type":"boolean","value":false,"targeting":[{"value":true,"clauses":[{"attribute":"tenant","op":"in","values":["a"],"negate":true}]}]}}}`, http.StatusBadRequest},
		{"rollout field", `{"rules":{"f":{"type":"boolean","value":false,"targeting":[{"value":true,"rollout":{"percentage":5,"bucket":"tenant"}}]}}}`, http.StatusBadRequest},
		{"custom values are opaque", `{"rules":{"f":{"type":"custom","value":{"anything":{"goes":1}}}}}`, http.StatusCreated},
		{"empty", ``, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, body := do(t, http.MethodPost, srv.URL+"/v1/chambers/strict/", test.body, nil)
			if res.StatusCode != test.want {
				t.Fatalf("status = %d, want %d (errors %v)", res.StatusCode, test.want, body.Errors)
			}
		})
	}
}

// TestReservedPathsCannotBeReachedByEncoding verifies that encoded slashes and
// dot segments do not smuggle requests into realm's metadata, on a backend
// that cleans the paths it is given.
func TestReservedPathsCannotBeReachedByEncoding(t *testing.T) {
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	inh, err := storage.NewInheritableStorage(stg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: inh})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, p := range []string{
		"/v1/chambers/_realm/keys/",
		"/v1/chambers/%252F_realm%252Fkeys",
		"/v1/chambers/%2F_realm%2Fkeys",
		"/v1/chambers/.%252F_realm/keys",
		"/v1/chambers/.%2F_realm/keys",
	} {
		res, body := do(t, http.MethodPost, srv.URL+p, `{"rules":{"x":{"type":"boolean","value":true}}}`, nil)
		if res.StatusCode == http.StatusCreated {
			// only acceptable as a chamber literally named after the escapes
			if !strings.Contains(p, "%25") {
				t.Errorf("POST %s = 201, want it refused", p)
			}
		}
		_ = body
	}
	for _, key := range []string{"/_realm/keys/", "/_realm/", "/_realm/keys"} {
		if _, err := stg.Get(context.Background(), key); err == nil {
			t.Errorf("a write reached reserved key %q", key)
		}
	}
}

func TestCanonicalPath(t *testing.T) {
	for in, want := range map[string]string{
		"":          "/",
		"/":         "/",
		"a":         "/a/",
		"//a//b/":   "/a/b/",
		"/./a/./":   "/a/",
		"/a/../b/":  "/a/../b/",
		"/_realm":   "/_realm/",
		"a new one": "/a new one/",
	} {
		if got := canonicalPath(in); got != want {
			t.Errorf("canonicalPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadiness(t *testing.T) {
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	draining := make(chan struct{})
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg, Draining: draining})
	if err != nil {
		t.Fatal(err)
	}
	probe := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if got := probe(); got != http.StatusOK {
		t.Fatalf("ready = %d, want 200", got)
	}
	close(draining)
	if got := probe(); got != http.StatusServiceUnavailable {
		t.Fatalf("draining = %d, want 503", got)
	}
}

// TestStreamsAreCappedAndExpire verifies streams beyond MaxStreams are refused
// and that a stream ends after its maximum lifetime.
func TestStreamsAreCappedAndExpire(t *testing.T) {
	old := maxStreamLifetime
	maxStreamLifetime = 200 * time.Millisecond
	t.Cleanup(func() { maxStreamLifetime = old })

	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg, MaxStreams: 1})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	first, err := http.Get(srv.URL + "/v1/chambers/a/?watch=true")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first stream = %d", first.StatusCode)
	}
	second, err := http.Get(srv.URL + "/v1/chambers/b/?watch=true")
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stream beyond the cap = %d, want 503", second.StatusCode)
	}

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, first.Body) // how it ends does not matter
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream outlived its maximum lifetime")
	}
}

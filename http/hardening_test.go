package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// TestReservedPathsIgnoreCase verifies that realm's metadata is out of reach
// however its prefix is capitalized. The file backend maps "/_REALM/" onto
// the directory of "/_realm/" on a case-insensitive filesystem (the default
// on macOS and Windows), which would let a chamber write replace the catalog
// or the API keys.
func TestReservedPathsIgnoreCase(t *testing.T) {
	stg, err := storage.NewFileStorage(map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	srv := newInheritableTestServerOn(t, stg)

	if res, out := do(t, http.MethodPost, srv.URL+"/v1/catalog/region", `{"values":{"eu":"Europe"}}`, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("seed the catalog: %d %v", res.StatusCode, out.Errors)
	}

	for _, p := range []string{"_REALM/catalog/", "_Realm/catalog/", "_realm/catalog/"} {
		for _, req := range []struct{ method, route, body string }{
			{http.MethodGet, "/v1/chambers/", ""},
			{http.MethodPost, "/v1/chambers/", `{"rules":{}}`},
			{http.MethodPatch, "/v1/chambers/", `{"rules":{}}`},
			{http.MethodDelete, "/v1/chambers/", ""},
			{http.MethodPost, "/v1/evaluate/", `{}`},
		} {
			res, out := do(t, req.method, srv.URL+req.route+p, req.body, nil)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("%s %s%s = %d, want 400 (%v)", req.method, req.route, p, res.StatusCode, out.Errors)
			}
		}
	}

	res, out := do(t, http.MethodGet, srv.URL+"/v1/catalog/region", "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(out.Data), "Europe") {
		t.Errorf("the catalog was changed: %d %s %v", res.StatusCode, out.Data, out.Errors)
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

// TestStorageFailuresAreNotMissingChambers verifies that a chamber read that
// fails for any reason other than absence is a 500 that does not reveal the
// cause, rather than a 404 that editors would offer to fix by creating it.
func TestStorageFailuresAreNotMissingChambers(t *testing.T) {
	raw, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	stg := &countingStorage{Storage: raw}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	putChamber(t, srv.URL, "a", `{"rules":{}}`)

	stg.fail.Store(true)
	for _, path := range []string{"/v1/chambers/a/", "/v1/chambers/a/?inherit=false"} {
		res, out := do(t, http.MethodGet, srv.URL+path, "", nil)
		if res.StatusCode != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", path, res.StatusCode)
		}
		if strings.Contains(strings.Join(out.Errors, " "), "storage unavailable") {
			t.Errorf("GET %s revealed the storage error: %v", path, out.Errors)
		}
	}
}

// TestParentReferencesAreBadRequests verifies that ".." paths, which storage
// refuses, are answered as client errors on every route. The router redirects
// a literal "/a/../b/" to "/b/", so only an encoded one reaches the handlers.
func TestParentReferencesAreBadRequests(t *testing.T) {
	srv := newInheritableTestServer(t)
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/chambers/a/%2E%2E/b/", ""},
		{http.MethodGet, "/v1/chambers/a/%2e%2e/b/?list=true", ""},
		{http.MethodPost, "/v1/chambers/a/%2E%2E/b/", `{"rules":{}}`},
		{http.MethodPatch, "/v1/chambers/a/%2E%2E/b/", `{"rules":{}}`},
		{http.MethodDelete, "/v1/chambers/a/%2E%2E/b/", ""},
		{http.MethodPost, "/v1/evaluate/a/%2E%2E/b/", `{}`},
		{http.MethodGet, "/v1/history/a/%2E%2E/b/", ""},
	} {
		res, out := do(t, req.method, srv.URL+req.path, req.body, nil)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s = %d %v, want 400", req.method, req.path, res.StatusCode, out.Errors)
		}
	}
}

func TestHSTSOnlyOverTLS(t *testing.T) {
	h := newTestHandler(t)
	plain := httptest.NewServer(h)
	t.Cleanup(plain.Close)
	res, err := http.Get(plain.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain HTTP sent Strict-Transport-Security %q", got)
	}

	secure := httptest.NewTLSServer(h)
	t.Cleanup(secure.Close)
	res, err = secure.Client().Get(secure.URL + "/v1/chambers/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=") {
		t.Errorf("HTTPS sent Strict-Transport-Security %q", got)
	}
}

// healthStorage reports the health set on it.
type healthStorage struct {
	*countingStorage
	health atomic.Value // error
}

func (h *healthStorage) Health() error {
	err, _ := h.health.Load().(error)
	return err
}

// TestReadinessIsCachedAndHealthAware verifies that probes share one storage
// check per interval, and that a storage reporting itself unhealthy fails
// readiness.
func TestReadinessIsCachedAndHealthAware(t *testing.T) {
	old := readyCacheFor
	readyCacheFor = 200 * time.Millisecond
	t.Cleanup(func() { readyCacheFor = old })

	raw, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	stg := &healthStorage{countingStorage: &countingStorage{Storage: raw}}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	probe := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	for range 20 {
		if got := probe(); got != http.StatusOK {
			t.Fatalf("ready = %d", got)
		}
	}
	if reads := stg.count("/"); reads != 1 {
		t.Errorf("20 probes made %d storage reads, want 1", reads)
	}

	stg.health.Store(errors.New("change feed stalled"))
	time.Sleep(250 * time.Millisecond)
	if got := probe(); got != http.StatusServiceUnavailable {
		t.Errorf("unhealthy storage: ready = %d, want 503", got)
	}
}

// TestPathsStorageRefusesAreBadRequests verifies that a path the storage
// backend cannot hold (here, names the file backend refuses because Windows
// filesystems would map them onto other names, such as realm's metadata) is
// answered as a client error and never reaches the disk.
func TestPathsStorageRefusesAreBadRequests(t *testing.T) {
	dir := t.TempDir()
	stg, err := storage.NewFileStorage(map[string]string{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := newInheritableTestServerOn(t, stg)
	for _, p := range []string{"_realm%5Ckeys/", "_realm./keys/", "a%3Ab/"} {
		for _, req := range []struct{ method, route, body string }{
			{http.MethodPost, "/v1/chambers/", `{"rules":{}}`},
			{http.MethodGet, "/v1/chambers/", ""},
			{http.MethodPost, "/v1/evaluate/", `{}`},
		} {
			res, out := do(t, req.method, srv.URL+req.route+p, req.body, nil)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("%s %s%s = %d %v, want 400", req.method, req.route, p, res.StatusCode, out.Errors)
			}
		}
		// history keys escape the path, so its history is merely empty
		if res, out := do(t, http.MethodGet, srv.URL+"/v1/history/"+p, "", nil); res.StatusCode >= http.StatusInternalServerError {
			t.Errorf("GET /v1/history/%s = %d %v", p, res.StatusCode, out.Errors)
		}
	}
}

package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestUIRoutesAreServedInPlace verifies that the UI's files are served as
// they are, and that every other path gets index.html without a redirect,
// which would decode the chamber names in it.
func TestUIRoutesAreServedInPlace(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>realm</title>")},
		"assets/app.js": {Data: []byte("console.log('realm')")},
	}
	h := http.StripPrefix("/ui/", uiHandler(fsys))
	for _, tt := range []struct{ path, want string }{
		{"/ui/", "<!doctype html>"},
		{"/ui/assets/app.js", "console.log"},
		{"/ui/prod/web/", "<!doctype html>"},
		{"/ui/prod/web", "<!doctype html>"},
		{"/ui/prod/a%3Fb/", "<!doctype html>"},
		{"/ui/prod/https%3Aevil.example/", "<!doctype html>"},
		{"/ui/assets/", "<!doctype html>"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK || !strings.HasPrefix(string(body), tt.want) {
			t.Errorf("GET %s = %d %q (Location %q), want 200 %q…", tt.path, rec.Code, body, rec.Header().Get("Location"), tt.want)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("GET %s: Content-Security-Policy = %q", tt.path, csp)
		}
	}
}

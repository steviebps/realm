package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	h := newTestHandler(t)

	tests := []struct {
		method string
		want   int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodHead, http.StatusOK},
		{http.MethodPost, http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(test.method, "/healthz", nil))
			if rec.Code != test.want {
				t.Errorf("%s /healthz = %d, want %d", test.method, rec.Code, test.want)
			}
		})
	}
}

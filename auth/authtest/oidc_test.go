package authtest

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestAuthorizeRedirects verifies that the issuer sends the browser back
// only to a registered redirect URI, keeping its query.
func TestAuthorizeRedirects(t *testing.T) {
	iss := NewIssuer(t, "someone@example.com")
	iss.RegisterRedirect("http://127.0.0.1:9/v1/auth/callback/test?keep=1")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	tests := []struct {
		name, redirectURI string
		wantStatus        int
	}{
		{"registered", "http://127.0.0.1:9/v1/auth/callback/test?keep=1", http.StatusFound},
		{"other host", "https://evil.example/v1/auth/callback/test?keep=1", http.StatusBadRequest},
		{"other path", "http://127.0.0.1:9/elsewhere", http.StatusBadRequest},
		{"missing", "", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := url.Values{
				"client_id":             {ClientID},
				"code_challenge_method": {"S256"},
				"code_challenge":        {"challenge"},
				"redirect_uri":          {tt.redirectURI},
				"state":                 {"xyz"},
			}
			res, err := client.Get(iss.URL + "/authorize?" + q.Encode())
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusFound {
				return
			}
			loc, err := url.Parse(res.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if got := loc.Scheme + "://" + loc.Host + loc.Path; got != "http://127.0.0.1:9/v1/auth/callback/test" {
				t.Fatalf("redirected to %q", loc)
			}
			v := loc.Query()
			if v.Get("keep") != "1" || v.Get("state") != "xyz" || strings.TrimSpace(v.Get("code")) == "" {
				t.Fatalf("redirect query = %v", v)
			}
		})
	}
}

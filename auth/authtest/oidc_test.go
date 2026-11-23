package authtest

import (
	"net/http"
	"net/url"
	"testing"
)

// TestAuthorizeRedirects verifies that the issuer sends the browser back
// only to a registered redirect URI, keeping its query.
func TestAuthorizeRedirects(t *testing.T) {
	const registered = "http://127.0.0.1:9/v1/auth/callback/test?keep=1"
	iss := NewIssuer(t, "someone@example.com")
	iss.RegisterRedirect(registered)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authorize := func(redirectURI string) *http.Response {
		t.Helper()
		q := url.Values{
			"client_id":             {ClientID},
			"code_challenge_method": {"S256"},
			"code_challenge":        {"challenge"},
			"redirect_uri":          {redirectURI},
			"state":                 {"xyz"},
		}
		res, err := client.Get(iss.URL + "/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}

	res := authorize(registered)
	if res.StatusCode != http.StatusFound {
		t.Fatalf("registered: status = %d", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	v := loc.Query()
	if v.Get("keep") != "1" || v.Get("state") != "xyz" || v.Get("code") == "" {
		t.Fatalf("redirect query = %v", v)
	}
	if loc.RawQuery = ""; loc.String() != "http://127.0.0.1:9/v1/auth/callback/test" {
		t.Fatalf("redirected to %q", loc)
	}

	for _, uri := range []string{"https://evil.example/v1/auth/callback/test?keep=1", "http://127.0.0.1:9/elsewhere", ""} {
		if res := authorize(uri); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", uri, res.StatusCode)
		}
	}
}

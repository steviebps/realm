package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/helper/logging"
)

// TestUserAgent verifies every request names realm's Go client, unless the
// caller set its own User-Agent.
func TestUserAgent(t *testing.T) {
	var mu sync.Mutex
	var agents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		agents = append(agents, r.UserAgent())
		mu.Unlock()
	}))
	defer srv.Close()
	c, err := NewHttpClient(&HttpClientConfig{Address: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tests := []struct {
		name string
		do   func() (*http.Response, error)
		want string
	}{
		{"request", func() (*http.Response, error) { return c.PerformRequest(ctx, http.MethodGet, "a", nil) }, userAgent},
		{"watch", func() (*http.Response, error) { return c.Watch(ctx, "a") }, userAgent},
		{"the caller's own", func() (*http.Response, error) {
			req, err := c.NewRequest(ctx, http.MethodGet, "a", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", "mine/1")
			return c.Do(req)
		}, "mine/1"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.do()
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			if agents[i] != tt.want {
				t.Errorf("User-Agent = %q, want %q", agents[i], tt.want)
			}
		})
	}
	if !strings.HasPrefix(userAgent, "realm-go/") || userAgent == "realm-go/" {
		t.Errorf("userAgent = %q", userAgent)
	}
}

// TestPlaintextTokenWarning verifies the client warns, once, when it sends
// its API key unencrypted to another machine.
func TestPlaintextTokenWarning(t *testing.T) {
	tests := []struct {
		address string
		token   string
		warn    bool
	}{
		{"http://realm.example.com", "key", true},
		{"http://10.0.0.5:8080", "key", true},
		{"https://realm.example.com", "key", false},
		{"http://localhost:8080", "key", false},
		{"http://realm.localhost", "key", false},
		{"http://127.0.0.1:8080", "key", false},
		{"http://[::1]:8080", "key", false},
		{"http://realm.example.com", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.address+" "+tt.token, func(t *testing.T) {
			var buf bytes.Buffer
			ctx := logging.FromZerolog(zerolog.New(&buf)).WithContext(context.Background())
			c, err := NewHttpClient(&HttpClientConfig{Address: tt.address, Token: tt.token})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				req, err := c.NewRequest(ctx, http.MethodGet, "a", nil)
				if err != nil {
					t.Fatal(err)
				}
				c.prepare(req)
				if got, want := req.Header.Get("Authorization"), "Bearer "+tt.token; tt.token != "" && got != want {
					t.Errorf("Authorization = %q, want %q", got, want)
				}
			}
			warnings := strings.Count(buf.String(), "plain http")
			if want := map[bool]int{true: 1, false: 0}[tt.warn]; warnings != want {
				t.Errorf("%d warnings, want %d: %s", warnings, want, buf.String())
			}
		})
	}
}

// TestSubtreeRequests verifies the requests for a subtree read and watch.
func TestSubtreeRequests(t *testing.T) {
	type seen struct{ uri, accept, auth string }
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{r.URL.RequestURI(), r.Header.Get("Accept"), r.Header.Get("Authorization")})
		mu.Unlock()
	}))
	defer srv.Close()
	c, err := NewHttpClient(&HttpClientConfig{Address: srv.URL, Token: "key"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tests := []struct {
		name string
		do   func() (*http.Response, error)
		want seen
	}{
		{"read", func() (*http.Response, error) { return c.GetSubtree(ctx, "/prod/tenants/") }, seen{"/v1/chambers/prod/tenants/?subtree=true", "", "Bearer key"}},
		{"watch", func() (*http.Response, error) { return c.WatchSubtree(ctx, "/prod/tenants/") }, seen{"/v1/chambers/prod/tenants/?subtree=true&watch=true", "text/event-stream", "Bearer key"}},
		{"single watch", func() (*http.Response, error) { return c.Watch(ctx, "prod") }, seen{"/v1/chambers/prod?watch=true", "text/event-stream", "Bearer key"}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.do()
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			if got[i] != tt.want {
				t.Errorf("got %+v, want %+v", got[i], tt.want)
			}
		})
	}
}

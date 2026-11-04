package http

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const evaluateParent = `{"rules":{"theme":{"type":"string","value":"light"}}}`

// evaluateChild serves "beta" to tenant acme and to half of everyone else.
const evaluateChild = `{"rules":{"beta":{"type":"boolean","value":false,"targeting":[
	{"clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true},
	{"value":true,"rollout":{"percentage":50}}]}}}`

func TestEvaluate(t *testing.T) {
	srv := newInheritableTestServer(t)
	putChamber(t, srv.URL, "app/", evaluateParent)
	putChamber(t, srv.URL, "app/web/", evaluateChild)
	url := srv.URL + "/v1/evaluate/app/web/"

	tests := []struct {
		name, method, url, body string
		status                  int
		want                    string
	}{
		{"targeted", http.MethodPost, url, `{"key":"u1","attributes":{"tenantId":"acme"}}`, http.StatusOK,
			`{"flags":{"beta":{"type":"boolean","value":true},"theme":{"type":"string","value":"light"}}}`},
		{"no context gets defaults", http.MethodPost, url, "", http.StatusOK,
			`{"flags":{"beta":{"type":"boolean","value":false},"theme":{"type":"string","value":"light"}}}`},
		{"missing chamber", http.MethodPost, srv.URL + "/v1/evaluate/nope/", `{}`, http.StatusNotFound, ""},
		{"reserved path", http.MethodPost, srv.URL + "/v1/evaluate/_realm/keys/", `{}`, http.StatusBadRequest, ""},
		{"invalid context", http.MethodPost, url, `{"attributes":{"tenantId":1}}`, http.StatusBadRequest, ""},
		{"GET is not allowed", http.MethodGet, url, "", http.StatusMethodNotAllowed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, out := call(t, nil, tt.method, tt.url, tt.body, "")
			if status != tt.status {
				t.Fatalf("status = %d, want %d (%v)", status, tt.status, out.Errors)
			}
			if tt.want != "" && string(out.Data) != tt.want {
				t.Errorf("data = %s, want %s", out.Data, tt.want)
			}
		})
	}
}

func TestEvaluateRolloutIsSticky(t *testing.T) {
	srv := newInheritableTestServer(t)
	putChamber(t, srv.URL, "app/", evaluateChild)
	on := 0
	for i := range 200 {
		body := fmt.Sprintf(`{"key":"user-%d"}`, i)
		_, first := call(t, nil, http.MethodPost, srv.URL+"/v1/evaluate/app/", body, "")
		_, again := call(t, nil, http.MethodPost, srv.URL+"/v1/evaluate/app/", body, "")
		if string(first.Data) != string(again.Data) {
			t.Fatalf("%s evaluated to %s, then %s", body, first.Data, again.Data)
		}
		if strings.Contains(string(first.Data), `"value":true`) {
			on++
		}
	}
	if on < 60 || on > 140 {
		t.Errorf("%d of 200 contexts in a 50%% rollout", on)
	}
}

func TestEvaluateStreamSendsOnlyChanges(t *testing.T) {
	srv := newInheritableTestServer(t)
	putChamber(t, srv.URL, "app/", evaluateParent)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/evaluate/app/?watch=true", strings.NewReader(`{"attributes":{"tenantId":"acme"}}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	events := dataEvents(bufio.NewScanner(resp.Body))
	if first := waitData(t, events, 2*time.Second); !strings.Contains(first, `"light"`) {
		t.Fatalf("initial event = %s", first)
	}

	// A change that leaves this context's values alone sends nothing, so the
	// next event is the change that does affect it.
	putChamber(t, srv.URL, "app/", `{"rules":{"theme":{"type":"string","value":"light","targeting":[
		{"clauses":[{"attribute":"tenantId","op":"in","values":["other"]}],"value":"dark"}]}}}`)
	putChamber(t, srv.URL, "app/", `{"rules":{"theme":{"type":"string","value":"light","targeting":[
		{"clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":"dark"}]}}}`)
	if next := waitData(t, events, 2*time.Second); !strings.Contains(next, `"dark"`) {
		t.Fatalf("next event = %s, want the change for acme", next)
	}
}

func TestEvaluateCORS(t *testing.T) {
	srv := newAuthTestServer(t)

	preflight := func(path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+path, nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}

	res := preflight("/v1/evaluate/prod/")
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("evaluate preflight = %d %v", res.StatusCode, res.Header)
	}
	if res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("evaluate preflight allows credentials")
	}
	if res := preflight("/v1/chambers/prod/"); res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("chambers preflight allows cross-origin requests: %v", res.Header)
	}
}

func TestEvaluateKeys(t *testing.T) {
	srv := newAuthTestServer(t)
	admin := srv.signIn(t, "admin@example.com")
	if status, _ := call(t, admin, http.MethodPost, srv.URL+"/v1/chambers/prod/web/", evaluateChild, ""); status != http.StatusCreated {
		t.Fatalf("seed chamber = %d", status)
	}
	status, out := call(t, admin, http.MethodPost, srv.URL+"/v1/keys", `{"name":"web","access":"evaluate","path":"/prod/"}`, "")
	var created createdKey
	if status != http.StatusCreated || json.Unmarshal(out.Data, &created) != nil {
		t.Fatalf("create evaluate key = %d %s", status, out.Data)
	}
	token := created.Token

	tests := []struct {
		name, method, path string
		want               int
	}{
		{"evaluates in scope", http.MethodPost, "/v1/evaluate/prod/web/", http.StatusOK},
		{"cannot evaluate outside scope", http.MethodPost, "/v1/evaluate/staging/", http.StatusForbidden},
		{"cannot read chambers", http.MethodGet, "/v1/chambers/prod/web/", http.StatusForbidden},
		{"cannot watch chambers", http.MethodGet, "/v1/chambers/prod/web/?watch=true", http.StatusForbidden},
		{"cannot read history", http.MethodGet, "/v1/history/prod/web/", http.StatusForbidden},
		{"cannot read the catalog", http.MethodGet, "/v1/catalog", http.StatusForbidden},
		{"cannot write", http.MethodPost, "/v1/chambers/prod/web/", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"key":"u1"}`
			if tt.method == http.MethodPost && strings.HasPrefix(tt.path, "/v1/chambers/") {
				body = chamberBody
			}
			if status, _ := call(t, nil, tt.method, srv.URL+tt.path, body, token); status != tt.want {
				t.Errorf("status = %d, want %d", status, tt.want)
			}
		})
	}
	if status, _ := call(t, nil, http.MethodPost, srv.URL+"/v1/evaluate/prod/web/", `{}`, ""); status != http.StatusUnauthorized {
		t.Errorf("evaluate without credentials = %d, want 401", status)
	}
	viewer := srv.signIn(t, "viewer@example.com")
	if status, _ := call(t, viewer, http.MethodPost, srv.URL+"/v1/evaluate/prod/web/", `{}`, ""); status != http.StatusOK {
		t.Errorf("viewer evaluate = %d, want 200", status)
	}
}

func TestEvaluateRefusesSecretKeysFromBrowsers(t *testing.T) {
	srv := newAuthTestServer(t)
	admin := srv.signIn(t, "admin@example.com")
	if status, _ := call(t, admin, http.MethodPost, srv.URL+"/v1/chambers/prod/web/", evaluateChild, ""); status != http.StatusCreated {
		t.Fatalf("seed chamber = %d", status)
	}
	token := func(access string) string {
		t.Helper()
		status, out := call(t, admin, http.MethodPost, srv.URL+"/v1/keys", fmt.Sprintf(`{"name":%q,"access":%q,"path":"/prod/"}`, access, access), "")
		var created createdKey
		if status != http.StatusCreated || json.Unmarshal(out.Data, &created) != nil {
			t.Fatalf("create %s key = %d %s", access, status, out.Data)
		}
		return created.Token
	}
	tokens := map[string]string{"evaluate": token("evaluate"), "read": token("read"), "write": token("write")}

	tests := []struct {
		name, access, origin string
		want                 int
	}{
		{"evaluate key from a browser", "evaluate", "https://app.example.com", http.StatusOK},
		{"evaluate key from a backend", "evaluate", "", http.StatusOK},
		{"read key from a browser", "read", "https://app.example.com", http.StatusForbidden},
		{"read key from a backend", "read", "", http.StatusOK},
		{"write key from a browser", "write", "https://app.example.com", http.StatusForbidden},
		{"write key from a browser on the server's origin", "write", srv.URL, http.StatusForbidden},
		{"write key from a backend", "write", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, path := range []string{"/v1/evaluate/prod/web/", "/v1/evaluate/prod/web/?watch=true"} {
				req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(`{"key":"u1"}`))
				req.Header.Set("Authorization", "Bearer "+tokens[tt.access])
				if tt.origin != "" {
					req.Header.Set("Origin", tt.origin)
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != tt.want {
					t.Errorf("%s = %d, want %d", path, res.StatusCode, tt.want)
				}
				// the browser must be able to read why it was refused
				if tt.origin != "" && res.Header.Get("Access-Control-Allow-Origin") != "*" {
					t.Errorf("%s has no CORS header", path)
				}
			}
		})
	}

	viewer := srv.signIn(t, "viewer@example.com")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/evaluate/prod/web/", strings.NewReader(`{}`))
	req.Header.Set("Origin", srv.URL)
	if res, err := viewer.Do(req); err != nil || res.StatusCode != http.StatusOK {
		t.Errorf("signed-in user from the UI = %v %v, want 200", res, err)
	} else {
		res.Body.Close()
	}
}

func TestEvaluateStreamSendsAtScheduleBounds(t *testing.T) {
	srv := newInheritableTestServer(t)
	start := time.Now().Add(300 * time.Millisecond).Truncate(time.Millisecond)
	end := start.Add(300 * time.Millisecond)
	putChamber(t, srv.URL, "app/", fmt.Sprintf(`{"rules":{"window":{"type":"boolean","value":false,"targeting":[
		{"value":true,"schedule":{"start":%q,"end":%q}}]}}}`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)))

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/evaluate/app/?watch=true", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := dataEvents(bufio.NewScanner(resp.Body))

	// No writes follow: the stream re-evaluates when the window opens and
	// again when it closes.
	for i, want := range []string{`"value":false`, `"value":true`, `"value":false`} {
		if got := waitData(t, events, 2*time.Second); !strings.Contains(got, want) {
			t.Fatalf("event %d = %s, want %s", i, got, want)
		}
	}
	if now := time.Now(); now.Before(end) {
		t.Errorf("window closed early, %s before its end", end.Sub(now))
	}
}

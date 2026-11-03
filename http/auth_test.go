package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/auth/authtest"
	"github.com/steviebps/realm/pkg/storage"
)

// authTestServer runs a realm server with authentication against a fake
// OIDC issuer. Users are granted roles by email.
type authTestServer struct {
	*httptest.Server
	issuer *authtest.Issuer
}

func newAuthTestServer(t *testing.T) *authTestServer {
	t.Helper()
	issuer := authtest.NewIssuer(t, "admin@example.com")
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)

	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	inh, err := storage.NewInheritableStorage(stg)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = NewHandler(context.Background(), HandlerConfig{Storage: inh, Auth: &auth.Config{
		ExternalURL:   srv.URL,
		SessionSecret: "0123456789abcdef0123456789abcdef",
		Access: auth.AccessConfig{
			Admins:  auth.Grant{Emails: []string{"admin@example.com"}},
			Editors: auth.Grant{Emails: []string{"editor@example.com"}},
			Viewers: auth.Grant{Domains: []string{"example.com"}},
		},
		Providers: []auth.ProviderConfig{{Name: "test", Type: auth.ProviderOIDC, Issuer: issuer.URL, ClientID: authtest.ClientID, ClientSecret: authtest.ClientSecret}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &authTestServer{Server: srv, issuer: issuer}
}

// signIn returns a browser signed in as email.
func (s *authTestServer) signIn(t *testing.T, email string) *http.Client {
	t.Helper()
	s.issuer.SignInAs(email, true)
	browser := authtest.Browser(t)
	res, err := browser.Get(s.URL + "/v1/auth/login/test")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sign-in as %s: %d", email, res.StatusCode)
	}
	return browser
}

// call performs a request as client (nil for http.DefaultClient) with an
// optional bearer token.
func call(t *testing.T, client *http.Client, method, url, body, token string) (int, api.HTTPErrorAndDataResponse) {
	t.Helper()
	if client == nil {
		client = http.DefaultClient
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// clients cannot choose their recorded identity once authenticated
	req.Header.Set(ActorHeader, "impostor")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out api.HTTPErrorAndDataResponse
	b, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(b, &out)
	// compact the data so tests can match on it
	var compact bytes.Buffer
	if json.Compact(&compact, out.Data) == nil {
		out.Data = compact.Bytes()
	}
	return res.StatusCode, out
}

func lastActor(t *testing.T, client *http.Client, url, token string) string {
	t.Helper()
	status, out := call(t, client, http.MethodGet, url, "", token)
	var revs []Revision
	if status != http.StatusOK || json.Unmarshal(out.Data, &revs) != nil || len(revs) == 0 {
		t.Fatalf("history %s: %d %s", url, status, out.Data)
	}
	return revs[0].Actor
}

const chamberBody = `{"rules":{"beta":{"type":"boolean","value":true}}}`

func TestAuthDisabledReportsOpen(t *testing.T) {
	srv := newInheritableTestServer(t)
	status, out := call(t, nil, http.MethodGet, srv.URL+"/v1/auth/me", "", "")
	if status != http.StatusOK || !strings.Contains(string(out.Data), `"authEnabled":false`) {
		t.Fatalf("me = %d %s", status, out.Data)
	}
	if status, _ := call(t, nil, http.MethodPost, srv.URL+"/v1/chambers/app/", chamberBody, ""); status != http.StatusCreated {
		t.Fatalf("anonymous write = %d", status)
	}
	if status, _ := call(t, nil, http.MethodGet, srv.URL+"/v1/keys", "", ""); status != http.StatusNotFound {
		t.Fatalf("keys API without auth = %d, want 404", status)
	}
}

func TestAuthRequired(t *testing.T) {
	srv := newAuthTestServer(t)
	for _, path := range []string{"/v1/chambers/", "/v1/chambers/app/?list=true", "/v1/history/", "/v1/catalog", "/v1/keys"} {
		if status, _ := call(t, nil, http.MethodGet, srv.URL+path, "", ""); status != http.StatusUnauthorized {
			t.Errorf("GET %s without credentials = %d, want 401", path, status)
		}
	}
	status, out := call(t, nil, http.MethodGet, srv.URL+"/v1/auth/me", "", "")
	if status != http.StatusOK || !strings.Contains(string(out.Data), `"authEnabled":true`) ||
		!strings.Contains(string(out.Data), `"user":null`) || !strings.Contains(string(out.Data), `"name":"test"`) {
		t.Fatalf("me = %d %s", status, out.Data)
	}
	for _, path := range []string{"/ui/", "/healthz"} {
		if status, _ := call(t, nil, http.MethodGet, srv.URL+path, "", ""); status != http.StatusOK {
			t.Errorf("GET %s without credentials = %d, want 200", path, status)
		}
	}
}

func TestRoles(t *testing.T) {
	srv := newAuthTestServer(t)
	admin := srv.signIn(t, "admin@example.com")
	editor := srv.signIn(t, "editor@example.com")
	viewer := srv.signIn(t, "viewer@example.com")
	chamber := srv.URL + "/v1/chambers/app/"

	for _, tt := range []struct {
		client *http.Client
		want   string
	}{
		{viewer, `"role":"viewer"},"canEdit":false,"isAdmin":false`},
		{editor, `"role":"editor"},"canEdit":true,"isAdmin":false`},
		{admin, `"role":"admin"},"canEdit":true,"isAdmin":true`},
	} {
		if _, out := call(t, tt.client, http.MethodGet, srv.URL+"/v1/auth/me", "", ""); !strings.Contains(string(out.Data), tt.want) {
			t.Errorf("me = %s, want it to contain %s", out.Data, tt.want)
		}
	}

	if status, _ := call(t, viewer, http.MethodPost, chamber, chamberBody, ""); status != http.StatusForbidden {
		t.Errorf("viewer write = %d, want 403", status)
	}
	if status, _ := call(t, editor, http.MethodPost, chamber, chamberBody, ""); status != http.StatusCreated {
		t.Errorf("editor write = %d, want 201", status)
	}
	if status, _ := call(t, viewer, http.MethodGet, chamber, "", ""); status != http.StatusOK {
		t.Errorf("viewer read = %d, want 200", status)
	}
	if got := lastActor(t, viewer, srv.URL+"/v1/history/app/", ""); got != "editor@example.com" {
		t.Errorf("history actor = %q, want the signed-in editor", got)
	}

	catalog := srv.URL + "/v1/catalog/region"
	if status, _ := call(t, editor, http.MethodPost, catalog, `{"values":{"eu":"Europe"}}`, ""); status != http.StatusForbidden {
		t.Errorf("editor catalog write = %d, want 403", status)
	}
	if status, _ := call(t, admin, http.MethodPost, catalog, `{"values":{"eu":"Europe"}}`, ""); status != http.StatusNoContent {
		t.Errorf("admin catalog write = %d, want 204", status)
	}
	if status, _ := call(t, viewer, http.MethodGet, catalog, "", ""); status != http.StatusOK {
		t.Errorf("viewer catalog read = %d, want 200", status)
	}

	if status, _ := call(t, editor, http.MethodGet, srv.URL+"/v1/keys", "", ""); status != http.StatusForbidden {
		t.Errorf("editor key list = %d, want 403", status)
	}
	if status, _ := call(t, admin, http.MethodGet, srv.URL+"/v1/keys", "", ""); status != http.StatusOK {
		t.Errorf("admin key list = %d, want 200", status)
	}
}

func TestAPIKeys(t *testing.T) {
	srv := newAuthTestServer(t)
	admin := srv.signIn(t, "admin@example.com")

	create := func(body string) (auth.Key, string) {
		t.Helper()
		status, out := call(t, admin, http.MethodPost, srv.URL+"/v1/keys", body, "")
		var created createdKey
		if status != http.StatusCreated || json.Unmarshal(out.Data, &created) != nil {
			t.Fatalf("create key %s: %d %s %v", body, status, out.Data, out.Errors)
		}
		return created.Key, created.Token
	}
	if status, _ := call(t, admin, http.MethodPost, srv.URL+"/v1/keys", `{"name":"x","access":"root"}`, ""); status != http.StatusBadRequest {
		t.Errorf("invalid key = %d, want 400", status)
	}
	readKey, readToken := create(`{"name":"sdk","access":"read","path":"/prod/"}`)
	_, writeToken := create(`{"name":"ci","access":"write","path":"/prod/"}`)
	if readKey.CreatedBy != "admin@example.com" {
		t.Errorf("createdBy = %q", readKey.CreatedBy)
	}

	tests := []struct {
		name, method, path, token string
		want                      int
	}{
		{"write key creates in scope", http.MethodPost, "/v1/chambers/prod/app/", writeToken, http.StatusCreated},
		{"write key outside scope", http.MethodPost, "/v1/chambers/staging/", writeToken, http.StatusForbidden},
		{"read key reads in scope", http.MethodGet, "/v1/chambers/prod/app/", readToken, http.StatusOK},
		{"read key lists in scope", http.MethodGet, "/v1/chambers/prod/?list=true", readToken, http.StatusOK},
		{"read key reads history in scope", http.MethodGet, "/v1/history/prod/app/", readToken, http.StatusOK},
		{"read key cannot write", http.MethodPost, "/v1/chambers/prod/app/", readToken, http.StatusForbidden},
		{"read key outside scope", http.MethodGet, "/v1/chambers/", readToken, http.StatusForbidden},
		{"read key history outside scope", http.MethodGet, "/v1/history/", readToken, http.StatusForbidden},
		{"keys cannot manage keys", http.MethodGet, "/v1/keys", writeToken, http.StatusForbidden},
		{"keys cannot change the catalog", http.MethodPost, "/v1/catalog/region", writeToken, http.StatusForbidden},
		{"unknown token", http.MethodGet, "/v1/chambers/prod/", "realm_0000_bogus", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := ""
			if tt.method == http.MethodPost {
				body = chamberBody
				if strings.Contains(tt.path, "catalog") {
					body = `{"values":{}}`
				}
			}
			if status, out := call(t, nil, tt.method, srv.URL+tt.path, body, tt.token); status != tt.want {
				t.Errorf("%d %v, want %d", status, out.Errors, tt.want)
			}
		})
	}

	if got := lastActor(t, nil, srv.URL+"/v1/history/prod/app/", readToken); got != "key:ci" {
		t.Errorf("history actor = %q, want key:ci", got)
	}

	if status, _ := call(t, admin, http.MethodDelete, srv.URL+"/v1/keys/"+readKey.ID, "", ""); status != http.StatusNoContent {
		t.Fatalf("revoke = %d", status)
	}
	if status, _ := call(t, nil, http.MethodGet, srv.URL+"/v1/chambers/prod/app/", "", readToken); status != http.StatusUnauthorized {
		t.Errorf("revoked key = %d, want 401", status)
	}
	if status, _ := call(t, admin, http.MethodDelete, srv.URL+"/v1/keys/"+readKey.ID, "", ""); status != http.StatusNotFound {
		t.Errorf("revoke twice = %d, want 404", status)
	}

	status, out := call(t, admin, http.MethodGet, srv.URL+"/v1/keys", "", "")
	if status != http.StatusOK || strings.Contains(string(out.Data), "hash") || strings.Count(string(out.Data), `"id"`) != 1 {
		t.Errorf("key list = %d %s", status, out.Data)
	}
}

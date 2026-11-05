package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/auth/authtest"
	realmhttp "github.com/steviebps/realm/http"
	"github.com/steviebps/realm/pkg/storage"
)

// runRealm executes the CLI with args, returning stdout and stderr.
func runRealm(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append(args, "--notraces"))
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

// TestKeysBootstrapThenAPI mints the first key from the server's config with
// no server involved, then manages keys through the server with it.
func TestKeysBootstrapThenAPI(t *testing.T) {
	t.Setenv(TokenEnv, "")
	dir := t.TempDir()
	config := filepath.Join(dir, "realm.json")
	if err := os.WriteFile(config, []byte(`{"server":{"storage":"file","options":{"path":`+jsonString(filepath.Join(dir, "data"))+`}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, err := runRealm(t, "keys", "bootstrap", "--config", config, "--name", "first", "--access", "admin", "--path", "/")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	adminToken := strings.TrimSpace(out)
	if !strings.HasPrefix(adminToken, auth.TokenPrefix) {
		t.Fatalf("bootstrap printed %q, want only a token", out)
	}

	// a server over the same storage, with sign-in configured
	issuer := authtest.NewIssuer(t, "admin@example.com")
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer srv.Close()
	strg, err := storage.NewFileStorage(map[string]string{"path": filepath.Join(dir, "data")})
	if err != nil {
		t.Fatal(err)
	}
	handler, err = realmhttp.NewHandler(context.Background(), realmhttp.HandlerConfig{Storage: strg, Auth: &auth.Config{
		ExternalURL:   srv.URL,
		SessionSecret: "0123456789abcdef0123456789abcdef",
		Access:        auth.AccessConfig{Admins: auth.Grant{Emails: []string{"admin@example.com"}}},
		Providers:     []auth.ProviderConfig{{Name: "test", Type: auth.ProviderOIDC, Issuer: issuer.URL, ClientID: authtest.ClientID, ClientSecret: authtest.ClientSecret}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	api := []string{"--config", "", "--address", srv.URL}
	keys := func(token string, args ...string) (string, string, error) {
		return runRealm(t, append(append([]string{"keys"}, args...), append(api, "--token", token)...)...)
	}

	out, _, err = keys(adminToken, "create", "--name", "ci", "--access", "write", "--path", "/prod/")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), auth.TokenPrefix) {
		t.Fatalf("create = %q, %v", out, err)
	}

	out, _, err = keys(adminToken, "list")
	if err != nil || !strings.Contains(out, "first") || !strings.Contains(out, "ci") || !strings.Contains(out, "key:first") {
		t.Fatalf("list = %q, %v", out, err)
	}
	var ciID string
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 1 && fields[1] == "ci" {
			ciID = fields[0]
		}
	}

	if _, _, err := keys(adminToken, "revoke", ciID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := keys(adminToken, "revoke", ciID); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("revoking twice = %v, want the server's not-found error", err)
	}

	if _, _, err := keys("", "list"); err == nil || !strings.Contains(err.Error(), "--token") {
		t.Errorf("list without a token = %v, want a hint to pass --token", err)
	}
}

func TestKeysWithoutServerAuth(t *testing.T) {
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := realmhttp.NewHandler(context.Background(), realmhttp.HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	_, _, err = runRealm(t, "keys", "list", "--config", "", "--address", srv.URL, "--token", "")
	if err == nil || !strings.Contains(err.Error(), `no "auth" block`) {
		t.Fatalf("expected an explanation that the server has no auth, got %v", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

package cmd

import (
	"bytes"
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/steviebps/realm/auth"
	realmhttp "github.com/steviebps/realm/http"
	"github.com/steviebps/realm/pkg/storage"
)

// runKeys runs "realm client keys <args>" against addr with token, returning
// its standard output and standard error.
func runKeys(t *testing.T, addr, token string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append(append([]string{"client", "keys"}, args...), "--address", addr, "--token", token))
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		_ = rootCmd.PersistentFlags().Set("token", "")
	})
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestClientKeys(t *testing.T) {
	const adminKey = "admin-0123456789abcdef0123456789abcdef"
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stg.Close(context.Background()) })
	h, err := realmhttp.NewHandler(context.Background(), realmhttp.HandlerConfig{Storage: stg, Auth: &auth.Config{AdminKey: adminKey}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	token, msg, err := runKeys(t, srv.URL, adminKey, "create", "--name", "ci", "--access", "write", "--path", "/prod/")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, auth.TokenPrefix) {
		t.Fatalf("create printed %q, want a token", token)
	}
	m := regexp.MustCompile(`\(([0-9a-f]+)\)`).FindStringSubmatch(msg)
	if m == nil {
		t.Fatalf("create message %q names no key ID", msg)
	}
	id := m[1]

	if _, _, err := runKeys(t, srv.URL, token, "list"); err == nil || !strings.Contains(err.Error(), "administrators") {
		t.Errorf("list with a write key: err = %v, want refusal", err)
	}
	out, _, err := runKeys(t, srv.URL, adminKey, "list")
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "key:admin") {
		t.Fatalf("list = %q, %v", out, err)
	}
	if _, _, err := runKeys(t, srv.URL, adminKey, "revoke", id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := runKeys(t, srv.URL, adminKey, "revoke", id); err == nil {
		t.Error("revoking twice succeeded")
	}
	if out, _, _ := runKeys(t, srv.URL, adminKey, "list"); strings.Contains(out, id) {
		t.Errorf("revoked key still listed: %q", out)
	}
}

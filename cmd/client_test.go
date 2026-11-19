package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	realmhttp "github.com/steviebps/realm/http"
	"github.com/steviebps/realm/pkg/storage"
)

// runPut runs "realm client put app" against the server at addr, discarding
// its output. Cobra keeps flag values between runs, so callers pass --file ""
// to clear an earlier --file.
func runPut(t *testing.T, addr string, args ...string) error {
	t.Helper()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(append([]string{"client", "put", "app", "--address", addr}, args...))
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	return rootCmd.Execute()
}

func TestClientPutReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["not allowed to change /app/"]}`))
	}))
	defer srv.Close()

	err := runPut(t, srv.URL)
	if err == nil || !strings.Contains(err.Error(), "not allowed to change") {
		t.Fatalf("expected the server's error, got %v", err)
	}
}

// TestClientAddressesChambersByName verifies that the client subcommands act
// on the chamber their path names, whatever its name holds: "x?y" is not "x"
// with a query, nor "x#1" "x" with a fragment, so reading or deleting them
// leaves "x" alone.
func TestClientAddressesChambersByName(t *testing.T) {
	ctx := context.Background()
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stg.Close(ctx) })
	h, err := realmhttp.NewHandler(ctx, realmhttp.HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	// each chamber's flag names the chamber, so a read shows which one it got
	for _, p := range []string{"/prod/x/", "/prod/x?y/", "/prod/x?y/child/", "/prod/x#1/", "/prod/50%/"} {
		value := `{"rules":{"who":{"type":"string","value":"` + p + `"}}}`
		if err := stg.Put(ctx, storage.StorageEntry{Key: p, Value: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool {
		t.Helper()
		_, err := stg.Get(ctx, p)
		var nf *storage.NotFoundError
		if err != nil && !errors.As(err, &nf) {
			t.Fatalf("get %s: %v", p, err)
		}
		return err == nil
	}
	// Cobra keeps flag values between runs.
	t.Cleanup(func() { _ = clientDelete.Flags().Set("recursive", "false") })
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs(append(append([]string{"client"}, args...), "--address", srv.URL))
		t.Cleanup(func() { rootCmd.SetArgs(nil) })
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("client %s: %v", strings.Join(args, " "), err)
		}
		return out.String()
	}

	for _, p := range []string{"prod/x?y/", "prod/x#1/", "prod/50%/"} {
		if out := run("get", p); !strings.Contains(out, `"/`+p+`"`) {
			t.Errorf("get %s printed another chamber:\n%s", p, out)
		}
	}
	if out := run("list", "prod/x?y/"); !strings.Contains(out, `"child/"`) {
		t.Errorf("list prod/x?y/ = %s, want child/", out)
	}

	run("delete", "prod/x#1/")
	if exists("/prod/x#1/") {
		t.Error("delete prod/x#1/ left it in place")
	}
	run("delete", "--recursive", "prod/x?y/")
	if exists("/prod/x?y/") || exists("/prod/x?y/child/") {
		t.Error("delete --recursive prod/x?y/ left chambers in place")
	}
	if !exists("/prod/x/") || !exists("/prod/50%/") {
		t.Error("deleting other chambers deleted /prod/x/ or /prod/50%/")
	}
}

// TestClientPutNeverEmptiesAChamber verifies that put without --file only
// creates, and that --file replaces the chamber with the file's contents.
func TestClientPutNeverEmptiesAChamber(t *testing.T) {
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stg.Close(context.Background()) })
	h, err := realmhttp.NewHandler(context.Background(), realmhttp.HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "chamber.json")
	if err := os.WriteFile(file, []byte(`{"rules":{"kill":{"type":"boolean","value":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"rulez":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) error { return runPut(t, srv.URL, args...) }
	stored := func() string {
		e, err := stg.Get(context.Background(), "/app/")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		return string(e.Value)
	}

	if err := run("--file", ""); err != nil {
		t.Fatalf("creating an empty chamber: %v", err)
	}
	if err := run("--file", file); err != nil {
		t.Fatalf("put --file: %v", err)
	}
	if !strings.Contains(stored(), `"kill"`) {
		t.Fatalf("chamber after --file = %s", stored())
	}
	if err := run("--file", ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("put without --file over an existing chamber: err = %v", err)
	}
	if !strings.Contains(stored(), `"kill"`) {
		t.Fatalf("put without --file emptied the chamber: %s", stored())
	}
	if err := run("--file", bad); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("put of an invalid file: err = %v", err)
	}
}

package cmd

import (
	"context"
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
	rootCmd.SetArgs(append([]string{"client", "put", "app", "--address", addr, "--notraces"}, args...))
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

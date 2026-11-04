// Package pgtest provides throwaway PostgreSQL schemas for tests.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// EnvURL names the environment variable holding the test database's
// connection string. Tests that need PostgreSQL are skipped when it is unset.
const EnvURL = "REALM_POSTGRES_TEST_URL"

// URL returns a connection string for a fresh, empty schema in the database
// named by EnvURL, skipping the test when it is not set. The schema is
// dropped when the test ends.
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvURL)
	if base == "" {
		t.Skipf("set %s to run postgres tests", EnvURL)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "realm_test_" + hex.EncodeToString(b)

	ctx := context.Background()
	exec := func(sql string) error {
		conn, err := pgx.Connect(ctx, base)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		_, err = conn.Exec(ctx, sql)
		return err
	}
	if err := exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _ = exec("DROP SCHEMA " + schema + " CASCADE") })

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvURL, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

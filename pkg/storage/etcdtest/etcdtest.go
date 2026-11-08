// Package etcdtest provides throwaway etcd key prefixes for tests.
package etcdtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// EnvEndpoints names the environment variable holding the test cluster's
// client URLs, separated by commas. Tests that need etcd are skipped when it
// is unset.
const EnvEndpoints = "REALM_ETCD_TEST_ENDPOINTS"

// Options returns the options of etcd storage under a fresh, empty prefix on
// the cluster named by EnvEndpoints, skipping the test when it is not set.
// The prefix's keys are deleted when the test ends.
func Options(t testing.TB) map[string]string {
	t.Helper()
	endpoints := os.Getenv(EnvEndpoints)
	if endpoints == "" {
		t.Skipf("set %s to run etcd tests", EnvEndpoints)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	prefix := "/realm-test-" + hex.EncodeToString(b)

	t.Cleanup(func() {
		client, err := clientv3.New(clientv3.Config{
			Endpoints:   strings.Split(endpoints, ","),
			DialTimeout: 5 * time.Second,
			Logger:      zap.NewNop(),
		})
		if err != nil {
			return
		}
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = client.Delete(ctx, prefix+"/", clientv3.WithPrefix())
	})
	return map[string]string{"endpoints": endpoints, "prefix": prefix}
}

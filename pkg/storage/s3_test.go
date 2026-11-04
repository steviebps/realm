package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

func TestS3ObjectKey(t *testing.T) {
	tests := []struct {
		prefix, logical, want string
	}{
		{"", "/", "_entry"},
		{"", "/app/", "app/_entry"},
		{"", "/app/prod/", "app/prod/_entry"},
		{"realm", "/", "realm/_entry"},
		{"realm", "/app/prod/", "realm/app/prod/_entry"},
		{"", "/_realm/history/%2Fapp%2F/3/", "_realm/history/%2Fapp%2F/3/_entry"},
	}
	for _, test := range tests {
		t.Run(test.prefix+test.logical, func(t *testing.T) {
			s := &S3Storage{prefix: test.prefix}
			if got := s.objectKey(test.logical); got != test.want {
				t.Errorf("objectKey(%q) = %q, want %q", test.logical, got, test.want)
			}
		})
	}
}

// TestS3StorageIntegration runs against a real S3-compatible endpoint. It is
// skipped unless REALM_S3_TEST_ENDPOINT and REALM_S3_TEST_BUCKET are set, e.g.
// against MinIO:
//
//	docker run -d -p 9000:9000 minio/minio server /data
//	AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin AWS_REGION=us-east-1 \
//	REALM_S3_TEST_ENDPOINT=http://localhost:9000 REALM_S3_TEST_BUCKET=realm-test go test ./pkg/storage -run S3
func TestS3StorageIntegration(t *testing.T) {
	endpoint, bucket := os.Getenv("REALM_S3_TEST_ENDPOINT"), os.Getenv("REALM_S3_TEST_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("REALM_S3_TEST_ENDPOINT / REALM_S3_TEST_BUCKET not set")
	}

	prefix := fmt.Sprintf("test-%d", time.Now().UnixNano())
	strg, err := NewS3Storage(map[string]string{
		"bucket": bucket, "endpoint": endpoint, "force_path_style": "true", "prefix": prefix,
	})
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	ctx := context.Background()

	var nf *NotFoundError
	if _, err := strg.Get(ctx, "/app/"); !errors.As(err, &nf) {
		t.Fatalf("Get missing: expected NotFoundError, got %v", err)
	}
	if err := strg.Delete(ctx, "/app/"); !errors.As(err, &nf) {
		t.Fatalf("Delete missing: expected NotFoundError, got %v", err)
	}

	for _, p := range []string{"/", "/app/", "/app/prod/", "/app/staging/"} {
		if err := strg.Put(ctx, StorageEntry{Key: p, Value: []byte(`{"rules":{}}`)}); err != nil {
			t.Fatalf("Put %s: %v", p, err)
		}
	}
	got, err := strg.Get(ctx, "/app/prod/")
	if err != nil || string(got.Value) != `{"rules":{}}` {
		t.Fatalf("Get: %v %+v", err, got)
	}

	names, err := strg.List(ctx, "/app/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{".", "prod/", "staging/"}; !slices.Equal(names, want) {
		t.Errorf("List(/app/) = %v, want %v", names, want)
	}

	if err := strg.Delete(ctx, "/app/staging/"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := strg.Get(ctx, "/app/staging/"); !errors.As(err, &nf) {
		t.Fatalf("Get after delete: expected NotFoundError, got %v", err)
	}
}

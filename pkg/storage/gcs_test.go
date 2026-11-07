package storage

import (
	"errors"
	"fmt"
	"testing"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

// TestGCSErrorMapsNotFound checks that a missing object is reported as a
// NotFoundError however the client returns it: bare from the XML API, or
// wrapped, as the JSON API returns it.
func TestGCSErrorMapsNotFound(t *testing.T) {
	other := errors.New("permission denied")
	tests := []struct {
		name     string
		err      error
		notFound bool
	}{
		{"bare", gcs.ErrObjectNotExist, true},
		{"wrapped", fmt.Errorf("%w: %w", gcs.ErrObjectNotExist, &googleapi.Error{Code: 404}), true},
		{"another error", other, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gcsError("/a/", tt.err)
			if isNotFound(err) != tt.notFound {
				t.Errorf("gcsError(%v) = %v, want NotFoundError: %v", tt.err, err, tt.notFound)
			}
			if !tt.notFound && !errors.Is(err, tt.err) {
				t.Errorf("gcsError(%v) = %v, want the error passed on", tt.err, err)
			}
		})
	}
}

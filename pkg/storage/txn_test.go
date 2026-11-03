package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestUpdateWithinOneProcess checks that Update serializes concurrent
// read-modify-writes on backends that are not Transactional.
func TestUpdateWithinOneProcess(t *testing.T) {
	ctx := context.Background()
	for name, s := range backends(t) {
		t.Run(name, func(t *testing.T) {
			const n = 50
			var wg sync.WaitGroup
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := UpdateJSON(ctx, s, "/counter/", func(v *int) error {
						*v++
						return nil
					})
					if err != nil {
						t.Errorf("UpdateJSON: %v", err)
					}
				}()
			}
			wg.Wait()

			e, err := s.Get(ctx, "/counter/")
			if err != nil {
				t.Fatal(err)
			}
			// some backends store JSON with a trailing newline
			if got := strings.TrimSpace(string(e.Value)); got != strconv.Itoa(n) {
				t.Errorf("counter = %s, want %d", got, n)
			}
		})
	}
}

func TestUpdateAbortsOnError(t *testing.T) {
	ctx := context.Background()
	s := backends(t)["bigcache"]
	stop := errors.New("stop")
	err := Update(ctx, s, []string{"/a/"}, func(map[string]*StorageEntry) ([]Op, error) {
		return []Op{{Key: "/a/", Value: json.RawMessage(`1`)}}, stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Update = %v, want fn's error", err)
	}
	if _, err := s.Get(ctx, "/a/"); !isNotFound(err) {
		t.Errorf("expected nothing written, got %v", err)
	}
}

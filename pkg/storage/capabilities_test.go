package storage

import (
	"context"
	"errors"
	"iter"
	"testing"
)

// featured is storage with a change feed, health, and range reads, as
// postgres and etcd have.
type featured struct {
	Storage
	health error
}

func (f *featured) Watch(context.Context) <-chan Change { return nil }
func (f *featured) Health() error                       { return f.health }
func (f *featured) Scan(context.Context, string) iter.Seq2[*StorageEntry, error] {
	return func(func(*StorageEntry, error) bool) {}
}
func (f *featured) ScanKeys(context.Context, string) iter.Seq2[string, error] {
	return func(func(string, error) bool) {}
}

// TestCapabilitiesThroughWrappers checks that each optional capability is
// found whichever wrapper offers it: inheritance on the wrapper, and the
// innermost storage's range reads, change feed, and health beneath it.
func TestCapabilitiesThroughWrappers(t *testing.T) {
	sick := errors.New("the change feed is behind")
	inner := &featured{Storage: &mapStorage{entries: map[string]string{}}, health: sick}
	inh, err := NewInheritableStorage(inner)
	if err != nil {
		t.Fatal(err)
	}
	plain := &mapStorage{entries: map[string]string{}}

	tests := []struct {
		name                        string
		s                           Storage
		inheritable, scans, hasFeed bool
		health                      error
	}{
		{"inheritance over a featured backend", inh, true, true, true, sick},
		{"the featured backend itself", inner, false, true, true, sick},
		{"a plain backend", plain, false, false, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, feed := ChangeFeed(tt.s)
			if IsInheritable(tt.s) != tt.inheritable || CanScan(tt.s) != tt.scans || feed != tt.hasFeed || !errors.Is(Health(tt.s), tt.health) {
				t.Errorf("inheritable %v, scans %v, feed %v, health %v; want %v, %v, %v, %v",
					IsInheritable(tt.s), CanScan(tt.s), feed, Health(tt.s), tt.inheritable, tt.scans, tt.hasFeed, tt.health)
			}
		})
	}
}

// TestCacheableRefusesChangeFeed checks that storage several servers share
// cannot be cached, where its change feed would go unseen.
func TestCacheableRefusesChangeFeed(t *testing.T) {
	cache := &mapStorage{entries: map[string]string{}}
	if _, err := NewCacheableStorage(cache, &featured{Storage: &mapStorage{entries: map[string]string{}}}); err == nil {
		t.Error("a source with a change feed was cached")
	}
	if _, err := NewCacheableStorage(cache, &mapStorage{entries: map[string]string{}}); err != nil {
		t.Errorf("a plain source: %v", err)
	}
}

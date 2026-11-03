package storage

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type testStorage struct{}

func (f *testStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	return &StorageEntry{Key: "test", Value: []byte{}}, nil
}

func (f *testStorage) Put(ctx context.Context, e StorageEntry) error {
	return nil

}
func (f *testStorage) List(ctx context.Context, prefix string) ([]string, error) {
	return []string{}, nil

}
func (f *testStorage) Delete(ctx context.Context, key string) error {
	return nil
}

func (f *testStorage) Close(ctx context.Context) error {
	return nil
}

func TestListShouldForwardToSource(t *testing.T) {
	underlying := &testStorage{}
	s, _ := NewInheritableStorage(underlying)
	entry, _ := s.List(context.TODO(), "test")
	expected, _ := underlying.List(context.TODO(), "test")
	if !slices.Equal(entry, expected) {
		t.Errorf("did not correctly retrieve from source: %v, expected: %v", entry, expected)
	}
}

// mapStorage serves fixed entries and fails reads of keys listed in failing.
type mapStorage struct {
	testStorage
	entries map[string]string
	failing map[string]error
}

func (m *mapStorage) Get(ctx context.Context, key string) (*StorageEntry, error) {
	if err := m.failing[key]; err != nil {
		return nil, err
	}
	v, ok := m.entries[key]
	if !ok {
		return nil, &NotFoundError{key}
	}
	return &StorageEntry{Key: key, Value: []byte(v)}, nil
}

func TestGetSurfacesParentReadErrors(t *testing.T) {
	src := &mapStorage{
		entries: map[string]string{
			"/app/":      `{"rules":{"beta":{"type":"boolean","value":true}}}`,
			"/app/prod/": `{"rules":{}}`,
		},
	}
	s, _ := NewInheritableStorage(src)

	// a parent that was never created is simply skipped
	delete(src.entries, "/app/")
	if _, err := s.Get(context.Background(), "/app/prod/"); err != nil {
		t.Fatalf("missing parent should be skipped, got %v", err)
	}

	// but a parent that fails to read must not silently drop its rules
	src.entries["/app/"] = `{"rules":{"beta":{"type":"boolean","value":true}}}`
	boom := errors.New("storage unavailable")
	src.failing = map[string]error{"/app/": boom}
	if _, err := s.Get(context.Background(), "/app/prod/"); !errors.Is(err, boom) {
		t.Fatalf("expected the parent's read error, got %v", err)
	}
}

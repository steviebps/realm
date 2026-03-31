package storage

import (
	"context"
	"slices"
	"testing"
)

type testStorage struct{}

func (f *testStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	return &StorageEntry{
		"test",
		[]byte{},
	}, nil
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

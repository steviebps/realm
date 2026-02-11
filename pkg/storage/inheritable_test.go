package storage

import (
	"context"
	"slices"
	"testing"

	realm "github.com/steviebps/realm/pkg"
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

func TestInheritWith(t *testing.T) {
	bottom := &realm.Chamber{
		Rules: map[string]*realm.OverrideableRule{
			"rule2": {
				Rule: &realm.Rule{
					Type:  "boolean",
					Value: false,
				},
			},
		},
	}
	middle := &realm.Chamber{
		Rules: map[string]*realm.OverrideableRule{
			"rule1": {
				Rule: &realm.Rule{
					Type:  "boolean",
					Value: true,
				},
			},
		},
	}
	top := &realm.Chamber{
		Rules: map[string]*realm.OverrideableRule{
			"rule1": {
				Rule: &realm.Rule{
					Type:  "boolean",
					Value: false,
				},
			},
		},
	}

	InheritWith(middle, top)
	InheritWith(bottom, middle)

	v1 := top.Rules["rule1"]
	v2 := middle.Rules["rule1"]
	v3 := bottom.Rules["rule1"]

	// should not inherit top value as is
	if v1 == v2 {
		t.Errorf("middle did not inherit properly from top: value of rule1 is: %v", v2)
	}

	// should inherit middle value as is
	if v3 != v2 {
		t.Errorf("bottom did not inherit properly from top: value of rule1 is: %v", v3)
	}
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

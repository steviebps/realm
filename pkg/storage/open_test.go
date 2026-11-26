package storage

import (
	"context"
	"strings"
	"testing"
)

// TestOpen checks that Open creates the named backend, wraps it for
// inheritance when asked, and creates the root chamber unless it exists.
func TestOpen(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, "nope", nil, true); err == nil || !strings.Contains(err.Error(), `"nope" does not exist`) {
		t.Errorf("an unknown type = %v", err)
	}

	for _, inheritable := range []bool{false, true} {
		s, err := Open(ctx, "bigcache", map[string]string{}, inheritable)
		if err != nil {
			t.Fatal(err)
		}
		if IsInheritable(s) != inheritable {
			t.Errorf("inheritable %v: IsInheritable = %v", inheritable, IsInheritable(s))
		}
		root, err := Raw(s).Get(ctx, "/")
		if err != nil || string(root.Value) != `{"rules":{}}` {
			t.Errorf("inheritable %v: root = %v, %v; want an empty chamber", inheritable, root, err)
		}
		if err := s.Close(ctx); err != nil {
			t.Errorf("inheritable %v: Close = %v", inheritable, err)
		}
	}
}

// TestOpenKeepsAnExistingRoot checks that Open leaves a root chamber that
// exists as it is, as servers starting against shared storage do.
func TestOpenKeepsAnExistingRoot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opts := map[string]string{"path": dir}
	first, err := Open(ctx, "file", opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := Raw(first).Put(ctx, StorageEntry{Key: "/", Value: []byte(`{"rules":{"a":{"type":"boolean","value":true}}}`)}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close(ctx)

	second, err := Open(ctx, "file", opts, true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	root, err := Raw(second).Get(ctx, "/")
	if err != nil || !strings.Contains(string(root.Value), `"a"`) {
		t.Errorf("root = %v, %v; want the existing chamber kept", root, err)
	}
}

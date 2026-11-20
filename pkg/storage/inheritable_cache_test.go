package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestInheritableGetCachedMatchesFresh changes the chambers beneath an
// InheritableStorage, as a write through another replica would, and verifies
// that after each change its Get answers exactly as a new InheritableStorage
// (with nothing cached) does.
func TestInheritableGetCachedMatchesFresh(t *testing.T) {
	ctx := context.Background()
	src := &mapStorage{
		entries: map[string]string{
			"/":         `{"rules":{"theme":{"type":"string","value":"light"}}}`,
			"/app/":     `{"rules":{"beta":{"type":"boolean","value":false}}}`,
			"/app/web/": `{"rules":{"limit":{"type":"number","value":1}}}`,
		},
		versions: map[string]uint64{"/": 1, "/app/": 2, "/app/web/": 3},
	}
	inh, _ := NewInheritableStorage(src)

	steps := []struct {
		name   string
		change func()
		// want is a substring of the view, or empty when Get must fail
		want string
	}{
		{"first read", func() {}, `"theme":{"type":"string","value":"light"}`},
		{"unchanged", func() {}, `"theme":{"type":"string","value":"light"}`},
		{"grandparent changes", func() {
			src.entries["/"] = `{"rules":{"theme":{"type":"string","value":"dark"}}}`
			src.versions["/"] = 4
		}, `"theme":{"type":"string","value":"dark"}`},
		{"parent changes", func() {
			src.entries["/app/"] = `{"rules":{"beta":{"type":"boolean","value":true}}}`
			src.versions["/app/"] = 5
		}, `"beta":{"type":"boolean","value":true}`},
		{"chamber changes", func() {
			src.entries["/app/web/"] = `{"rules":{"limit":{"type":"number","value":2}}}`
			src.versions["/app/web/"] = 6
		}, `"limit":{"type":"number","value":2}`},
		{"same bytes at a new revision", func() {
			src.versions["/app/"] = 7
		}, `"beta":{"type":"boolean","value":true}`},
		{"parent seals a flag", func() {
			src.entries["/app/"] = `{"rules":{"beta":{"type":"boolean","value":true},"limit":{"type":"number","value":9,"sealed":true}}}`
			src.versions["/app/"] = 8
		}, `"limit":{"type":"number","value":9,"sealed":true}`},
		{"grandparent deleted", func() {
			delete(src.entries, "/")
			delete(src.versions, "/")
		}, `"beta"`},
		{"grandparent written unparsable", func() {
			src.entries["/"] = `{"rules":{"theme":{"type":"string","value":1}}}`
			src.versions["/"] = 9
		}, ""},
		{"grandparent repaired", func() {
			src.entries["/"] = `{"rules":{"theme":{"type":"string","value":"blue"}}}`
			src.versions["/"] = 10
		}, `"theme":{"type":"string","value":"blue"}`},
		{"chamber deleted", func() {
			delete(src.entries, "/app/web/")
			delete(src.versions, "/app/web/")
		}, ""},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.change()
			fresh, _ := NewInheritableStorage(src)
			want, wantErr := fresh.Get(ctx, "/app/web/")
			// the second read is answered from the cache
			for range 2 {
				got, err := inh.Get(ctx, "/app/web/")
				if (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
					t.Fatalf("Get error = %v, want %v", err, wantErr)
				}
				if step.want == "" {
					if err == nil {
						t.Fatalf("Get = %s, want an error", got.Value)
					}
					continue
				}
				if !bytes.Equal(got.Value, want.Value) || got.Version != want.Version {
					t.Fatalf("Get = %s at %d, want %s at %d", got.Value, got.Version, want.Value, want.Version)
				}
				if !strings.Contains(string(got.Value), step.want) {
					t.Errorf("Get = %s, want it to hold %s", got.Value, step.want)
				}
				// the caller owns what it is given: spoiling it spoils no
				// later read
				clear(got.Value)
			}
		})
	}
}

// TestInheritableGetSharedAncestors verifies that chambers sharing parents
// each get their own view, and the layers view sees the same chambers.
func TestInheritableGetSharedAncestors(t *testing.T) {
	ctx := context.Background()
	src := &mapStorage{entries: map[string]string{
		"/":       `{"rules":{"theme":{"type":"string","value":"light"}}}`,
		"/t/":     `{"rules":{}}`,
		"/t/one/": `{"rules":{"theme":{"type":"string","value":"one"}}}`,
		"/t/two/": `{"rules":{}}`,
	}}
	inh, _ := NewInheritableStorage(src)
	want := map[string]string{"/t/one/": "one", "/t/two/": "light", "/t/": "light"}
	for range 2 {
		for p, theme := range want {
			e, err := inh.Get(ctx, p)
			if err != nil {
				t.Fatalf("Get(%s): %v", p, err)
			}
			var got struct {
				Rules map[string]struct{ Value string } `json:"rules"`
			}
			if err := json.Unmarshal(e.Value, &got); err != nil {
				t.Fatal(err)
			}
			if got.Rules["theme"].Value != theme {
				t.Errorf("Get(%s) theme = %q, want %q", p, got.Rules["theme"].Value, theme)
			}
		}
	}
	lin, err := inh.(Layered).Layers(ctx, "/t/two/")
	if err != nil {
		t.Fatal(err)
	}
	if got := lin.Layers[2].Chamber.Rules["theme"].Value; got != "light" {
		t.Errorf("root layer theme = %v, want light", got)
	}
}

// TestInheritableGetConcurrentWrites reads a chamber while its parent is
// rewritten, checking every read sees one of the parent's versions whole.
func TestInheritableGetConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	src, err := NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	parent := func(i int) []byte {
		return fmt.Appendf(nil, `{"rules":{"n":{"type":"number","value":%d}}}`, i%2)
	}
	for _, e := range []StorageEntry{{Key: "/a/", Value: parent(0)}, {Key: "/a/b/", Value: []byte(`{"rules":{}}`)}} {
		if err := src.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	inh, _ := NewInheritableStorage(src)
	views := map[string]bool{
		`{"rules":{"n":{"type":"number","value":0}}}` + "\n": true,
		`{"rules":{"n":{"type":"number","value":1}}}` + "\n": true,
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 4 {
		wg.Go(func() {
			for range 200 {
				e, err := inh.Get(ctx, "/a/b/")
				if err != nil {
					errs <- err
					return
				}
				if !views[string(e.Value)] {
					errs <- fmt.Errorf("Get = %q, not a view of either parent", e.Value)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 200 {
			if err := src.Put(ctx, StorageEntry{Key: "/a/", Value: parent(i)}); err != nil {
				errs <- err
				return
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestViewCacheBounds verifies the cache stays within its bounds, forgetting
// entries to make room, and parses chambers too large to keep without
// keeping them.
func TestViewCacheBounds(t *testing.T) {
	const limit = 200
	c := newViewCache(limit, limit)
	chamber := func(i int) []byte { return fmt.Appendf(nil, `{"rules":{"f%d":{"type":"number","value":%d}}}`, i, i) }
	for i := range 50 {
		p := fmt.Sprintf("/c%d/", i)
		layer := c.parseLayer(p, &StorageEntry{Key: p, Value: chamber(i)})
		if layer.Err != nil || layer.Chamber.Rules[fmt.Sprintf("f%d", i)] == nil {
			t.Fatalf("parseLayer(%s) = %+v", p, layer)
		}
		lin := &Lineage{Inheritable: true, Layers: []Layer{layer}}
		c.storeView(lin, chamber(i))
		if v, ok := c.view(lin); !ok || !bytes.Equal(v, chamber(i)) {
			t.Fatalf("view(%s) = %s, %v; want the view just stored", p, v, ok)
		}
	}
	parsed := 0
	for _, e := range c.parsed {
		parsed += len(e.raw)
	}
	views := 0
	for _, e := range c.views {
		views += len(e.value)
	}
	if parsed != c.parsedBytes || parsed > limit || views != c.viewBytes || views > limit {
		t.Errorf("cache holds %d (counted %d) parsed and %d (counted %d) view bytes, want both at most %d", parsed, c.parsedBytes, views, c.viewBytes, limit)
	}

	big := []byte(`{"rules":{"big":{"type":"string","value":"` + strings.Repeat("x", limit) + `"}}}`)
	layer := c.parseLayer("/big/", &StorageEntry{Key: "/big/", Value: big})
	if layer.Err != nil || layer.Chamber.Rules["big"] == nil {
		t.Fatalf("parseLayer(/big/) = %+v", layer)
	}
	if _, ok := c.parsed["/big/"]; ok {
		t.Error("a chamber larger than the cache was kept")
	}

	bad := c.parseLayer("/bad/", &StorageEntry{Key: "/bad/", Value: []byte(`{`)})
	var ce *ChamberError
	if !errors.As(bad.Err, &ce) {
		t.Errorf("parseLayer(/bad/) error = %v, want a ChamberError", bad.Err)
	}
	if _, ok := c.parsed["/bad/"]; ok {
		t.Error("a chamber that cannot be parsed was kept")
	}
}

// inheritedTree stores a chamber of n flags with targeting at each of paths.
func inheritedTree(tb testing.TB, s Storage, n int, paths ...string) {
	tb.Helper()
	for _, p := range paths {
		var b strings.Builder
		b.WriteString(`{"rules":{`)
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"%s%d":{"type":"boolean","value":false,"targeting":[`+
				`{"id":"pilots","clauses":[{"attribute":"tenantId","op":"in","values":["a","b","c"]},{"attribute":"version","op":"semverGte","values":["1.2.3"]}],"value":true},`+
				`{"value":true,"rollout":{"percentage":25}}]}`, strings.ReplaceAll(p, "/", "-"), i)
		}
		b.WriteString(`}}`)
		if err := s.Put(context.Background(), StorageEntry{Key: p, Value: []byte(b.String())}); err != nil {
			tb.Fatal(err)
		}
	}
}

// BenchmarkInheritableGet reads the inherited view of a chamber with three
// parents, 25 flags each, none of them changing: an SDK's poll, or what a
// browser's evaluation reads.
func BenchmarkInheritableGet(b *testing.B) {
	ctx := context.Background()
	src, err := NewBigCacheStorage(map[string]string{})
	if err != nil {
		b.Fatal(err)
	}
	inheritedTree(b, src, 25, "/", "/prod/", "/prod/tenants/", "/prod/tenants/t1/")
	inh, _ := NewInheritableStorage(src)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := inh.Get(ctx, "/prod/tenants/t1/"); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

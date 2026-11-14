package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/steviebps/realm/utils"
)

// listingStorage is a mapStorage that lists its entries as backends do: the
// entry at a prefix as ".", and each child below it as "name/". It counts
// reads of each key.
type listingStorage struct {
	mapStorage
	reads map[string]int
}

func newListingStorage(entries map[string]string) *listingStorage {
	return &listingStorage{mapStorage: mapStorage{entries: entries}, reads: map[string]int{}}
}

func (l *listingStorage) Get(ctx context.Context, key string) (*StorageEntry, error) {
	l.reads[key]++
	return l.mapStorage.Get(ctx, key)
}

func (l *listingStorage) List(_ context.Context, prefix string) ([]string, error) {
	set := map[string]bool{}
	for key := range l.entries {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		if child, _, nested := strings.Cut(rest, "/"); nested {
			set[child+"/"] = true
		} else if rest == "" {
			set["."] = true
		}
	}
	return slices.Sorted(maps.Keys(set)), nil
}

// subtreeTree is lineageTree without its slashless key, which no listing
// names, plus siblings whose names sort around "/".
func subtreeTree(overrides map[string]string, missing ...string) map[string]string {
	entries := maps.Clone(lineageTree)
	delete(entries, "/a/b")
	entries["/a-b/"] = `{"rules":{"dash":{"type":"boolean","value":true}}}`
	entries["/a/b/c/d/e.f/"] = `{"rules":{"k":{"type":"number","value":5}}}`
	entries["/a/b0/"] = `{"rules":{"k":{"type":"number","value":0}}}`
	maps.Copy(entries, overrides)
	for _, k := range missing {
		delete(entries, k)
	}
	return entries
}

func walkPaths(t *testing.T, s Storage, root string) ([]View, error) {
	t.Helper()
	ctx := context.Background()
	paths, err := ListTree(ctx, s, root, 0)
	if err != nil {
		t.Fatalf("ListTree(%s): %v", root, err)
	}
	var views []View
	for v, err := range WalkTree(ctx, RawLayers(s), IsInheritable(s), paths) {
		if err != nil {
			return views, err
		}
		views = append(views, v)
	}
	return views, nil
}

func TestListTreeOrder(t *testing.T) {
	src := newListingStorage(subtreeTree(nil))
	tests := []struct {
		root string
		want []string
	}{
		{"/a/", []string{"/a/", "/a/b/", "/a/b/c/", "/a/b/c/d/", "/a/b/c/d/e.f/", "/a/b0/"}},
		{"/", []string{"/", "/a-b/", "/a/", "/a/b/", "/a/b/c/", "/a/b/c/d/", "/a/b/c/d/e.f/", "/a/b0/", "/empty/", "/empty/child/", "/prod/", "/prod/us-east/", "/prod/us-east/canary/", "/staging/", "/staging/eu/"}},
		{"/nope/", []string{"/nope/"}},
	}
	for _, tt := range tests {
		t.Run(tt.root, func(t *testing.T) {
			got, err := ListTree(context.Background(), src, tt.root, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("ListTree(%s) =\n  %v\nwant\n  %v", tt.root, got, tt.want)
			}
		})
	}
}

func TestListTreeLimit(t *testing.T) {
	src := newListingStorage(subtreeTree(nil))
	// /a/ has five paths below it
	if _, err := ListTree(context.Background(), src, "/a/", 5); err != nil {
		t.Errorf("five paths below /a/ with a limit of five: %v", err)
	}
	if _, err := ListTree(context.Background(), src, "/a/", 4); !errors.Is(err, ErrSubtreeTooLarge) {
		t.Errorf("five paths below /a/ with a limit of four: %v, want ErrSubtreeTooLarge", err)
	}
}

func TestListTreeLeavesOutReservedPaths(t *testing.T) {
	src := newListingStorage(map[string]string{
		"/app/":                  `{"rules":{}}`,
		"/_realm/history/app/":   `{}`,
		"/app/_realm/":           `{"rules":{}}`,
		"/_REALM/keys/something": `{}`,
	})
	got, err := ListTree(context.Background(), src, "/", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/", "/app/", "/app/_realm/"}; !slices.Equal(got, want) {
		t.Errorf("ListTree(/) = %v, want %v", got, want)
	}
}

// TestWalkTreeMatchesGet verifies that every view a walk yields is exactly
// what InheritableStorage.Get returns for its path, errors included, and
// that the walk reads each chamber once.
func TestWalkTreeMatchesGet(t *testing.T) {
	tests := []struct {
		name      string
		root      string
		overrides map[string]string
		missing   []string
	}{
		{name: "whole tree", root: "/"},
		{name: "prod", root: "/prod/"},
		{name: "deep", root: "/a/b/"},
		{name: "missing root chamber", root: "/", missing: []string{"/"}},
		{name: "unparsable parent", root: "/prod/", overrides: map[string]string{"/prod/": `{"rules":`}},
		{name: "unparsable ancestor above the root", root: "/prod/us-east/", overrides: map[string]string{"/": `[]`}},
		{name: "unparsable leaf", root: "/a/", overrides: map[string]string{"/a/b/c/": `nope`}},
		{name: "sealed", root: "/prod/", overrides: map[string]string{"/": `{"rules":{"ui.theme":{"type":"string","value":"classic","sealed":true}}}`, "/prod/us-east/": `{"rules":{"ui.theme":{"type":"string","value":"dark"}}}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			src := newListingStorage(subtreeTree(tt.overrides, tt.missing...))
			inh, _ := NewInheritableStorage(src)
			views, err := walkPaths(t, inh, tt.root)
			if err != nil {
				t.Fatal(err)
			}
			for key, n := range src.reads {
				if n > 1 {
					t.Errorf("the walk read %s %d times", key, n)
				}
			}
			for _, v := range views {
				if v.Path == tt.root && !v.Stored {
					continue // Get has no view of a path without a chamber
				}
				want, wantErr := inh.Get(ctx, v.Path)
				if (v.Err == nil) != (wantErr == nil) || (wantErr != nil && v.Err.Error() != wantErr.Error()) {
					t.Fatalf("%s: view error = %v, Get error = %v", v.Path, v.Err, wantErr)
				}
				if wantErr != nil {
					var ce *ChamberError
					if !errors.As(v.Err, &ce) {
						t.Errorf("%s: view error %v does not wrap a ChamberError", v.Path, v.Err)
					}
					continue
				}
				var buf bytes.Buffer
				if err := utils.WriteInterfaceWith(&buf, v.Chamber, false); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(buf.Bytes(), want.Value) {
					t.Errorf("%s: view =\n  %s\nGet =\n  %s", v.Path, buf.Bytes(), want.Value)
				}
				if v.Revision != want.Version {
					t.Errorf("%s: view revision %d, Get %d", v.Path, v.Revision, want.Version)
				}
			}
		})
	}
}

func TestWalkTreeEntries(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		missing []string
		want    []string // path=rule keys, or path=error
	}{
		{
			name: "root first, gaps skipped",
			root: "/staging/",
			want: []string{"/staging/=[checkout.newFlow search.provider ui.theme]", "/staging/eu/=[checkout.newFlow search.provider ui.theme]"},
		},
		{
			name:    "root without any chamber above",
			root:    "/staging/",
			missing: []string{"/"},
			want:    []string{"/staging/=[]", "/staging/eu/=[ui.theme]"},
		},
		{
			name: "missing subtree",
			root: "/nope/deeper/",
			want: []string{"/nope/deeper/=[checkout.newFlow search.provider ui.theme]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inh, _ := NewInheritableStorage(newListingStorage(subtreeTree(nil, tt.missing...)))
			views, err := walkPaths(t, inh, tt.root)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, v := range views {
				got = append(got, v.Path+"="+strings.Join(slices.Sorted(maps.Keys(v.Chamber.Rules)), " "))
			}
			for i := range tt.want {
				tt.want[i] = strings.ReplaceAll(strings.ReplaceAll(tt.want[i], "[", ""), "]", "")
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("views =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

// TestWalkTreeWithoutInheritance verifies that each view of storage that
// does not apply inheritance is the chamber as stored.
func TestWalkTreeWithoutInheritance(t *testing.T) {
	src := newListingStorage(subtreeTree(nil))
	views, err := walkPaths(t, src, "/prod/")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range views {
		raw, _ := json.Marshal(v.Chamber)
		got = append(got, v.Path+" "+string(raw))
	}
	want := []string{
		`/prod/ {"rules":{"checkout.newFlow":{"type":"boolean","value":false,"targeting":[{"value":true,"rollout":{"percentage":25}}]},"prod.audit":{"type":"boolean","value":true},"rate.limit":{"type":"number","value":500}}}`,
		`/prod/us-east/ {"rules":{"rate.limit":{"type":"number","value":1000},"region.failover":{"type":"boolean","value":true},"search.provider":{"type":"string","value":"opensearch"}}}`,
		`/prod/us-east/canary/ {"rules":{"checkout.newFlow":{"type":"boolean","value":true}}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("views =\n  %q\nwant\n  %q", got, want)
	}

	// a root without a chamber is empty
	views, err = walkPaths(t, src, "/staging/")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Stored || len(views[0].Chamber.Rules) != 0 {
		t.Errorf("views of /staging/ = %+v, want an empty root, then /staging/eu/", views)
	}
}

func TestWalkTreeStopsAtFailedRead(t *testing.T) {
	boom := errors.New("storage unavailable")
	src := newListingStorage(subtreeTree(nil))
	src.failing = map[string]error{"/prod/us-east/": boom}
	inh, _ := NewInheritableStorage(src)
	views, err := walkPaths(t, inh, "/prod/")
	if !errors.Is(err, boom) {
		t.Fatalf("walk error = %v, want %v", err, boom)
	}
	if len(views) != 1 || views[0].Path != "/prod/" {
		t.Errorf("views before the failure = %+v, want /prod/ alone", views)
	}
}

func TestBuildViewMatchesGet(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("storage unavailable")
	for _, failing := range []map[string]error{nil, {"/": boom}, {"/prod/": boom}} {
		src := newListingStorage(subtreeTree(nil))
		src.failing = failing
		inh, _ := NewInheritableStorage(src)
		for _, p := range []string{"/", "/prod/", "/prod/us-east/canary/", "/staging/eu/", "/a/../b/"} {
			v, err := BuildView(ctx, RawLayers(inh), p, true)
			want, wantErr := inh.Get(ctx, p)
			if wantErr != nil && v.Err == nil {
				if err == nil || err.Error() != wantErr.Error() {
					t.Errorf("%s (failing %v): BuildView error %v, Get error %v", p, failing, err, wantErr)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			var buf bytes.Buffer
			_ = utils.WriteInterfaceWith(&buf, v.Chamber, false)
			if !bytes.Equal(buf.Bytes(), want.Value) {
				t.Errorf("%s: view %s, Get %s", p, buf.Bytes(), want.Value)
			}
		}
	}
}

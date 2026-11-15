package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
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

// scanningStorage is a listingStorage that is also a Scanner, as backends
// that can read a prefix in one query are. It counts scans, and fails the
// scan after failAfter entries when scanErr is set.
type scanningStorage struct {
	*listingStorage
	scans     int
	scanErr   error
	failAfter int
}

var _ Scanner = (*scanningStorage)(nil)

func (s *scanningStorage) Scan(_ context.Context, prefix string) iter.Seq2[*StorageEntry, error] {
	s.scans++
	return func(yield func(*StorageEntry, error) bool) {
		prefix = scanPrefix(prefix)
		n := 0
		for _, key := range slices.Sorted(maps.Keys(s.entries)) {
			if !strings.HasPrefix(key, prefix) || scanSkips(prefix, key) {
				continue
			}
			if s.scanErr != nil && n == s.failAfter {
				yield(nil, s.scanErr)
				return
			}
			n++
			if !yield(&StorageEntry{Key: key, Value: []byte(s.entries[key])}, nil) {
				return
			}
		}
	}
}

func (s *scanningStorage) ScanKeys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	return scanKeysOf(s.Scan(ctx, prefix))
}

// sources returns a listingStorage over entries and, when scan is set, one
// that is also a Scanner.
func sources(entries map[string]string, scan bool) (*listingStorage, Storage) {
	l := newListingStorage(entries)
	if scan {
		return l, &scanningStorage{listingStorage: l}
	}
	return l, l
}

// bothWays runs fn as a subtest with a source that lists each level of the
// tree, then with one that scans.
func bothWays(t *testing.T, fn func(t *testing.T, scan bool)) {
	for _, scan := range []bool{false, true} {
		name := "levels"
		if scan {
			name = "scan"
		}
		t.Run(name, func(t *testing.T) { fn(t, scan) })
	}
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
	for v, err := range ReadTree(ctx, s, paths) {
		if err != nil {
			return views, err
		}
		views = append(views, v)
	}
	return views, nil
}

func TestListTreeOrder(t *testing.T) {
	bothWays(t, testListTreeOrder)
}

func testListTreeOrder(t *testing.T, scan bool) {
	_, src := sources(subtreeTree(map[string]string{
		// listed as List names them: an empty or "." segment ends a key
		"/a//x/":  `{"rules":{}}`,
		"/a/./y/": `{"rules":{}}`,
	}), scan)
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
	bothWays(t, func(t *testing.T, scan bool) {
		// /a/ has five paths below it, /a/b/ and /a/b/c/d/ only lead to
		// chambers
		_, src := sources(subtreeTree(nil, "/a/b/", "/a/b/c/d/"), scan)
		if _, err := ListTree(context.Background(), src, "/a/", 5); err != nil {
			t.Errorf("five paths below /a/ with a limit of five: %v", err)
		}
		if _, err := ListTree(context.Background(), src, "/a/", 4); !errors.Is(err, ErrSubtreeTooLarge) {
			t.Errorf("five paths below /a/ with a limit of four: %v, want ErrSubtreeTooLarge", err)
		}
	})
}

func TestListTreeLeavesOutReservedPaths(t *testing.T) {
	bothWays(t, func(t *testing.T, scan bool) {
		_, src := sources(map[string]string{
			"/app/":                   `{"rules":{}}`,
			"/_realm/history/app/":    `{}`,
			"/app/_realm/":            `{"rules":{}}`,
			"/_REALM/keys/something/": `{}`,
		}, scan)
		got, err := ListTree(context.Background(), src, "/", 0)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"/", "/app/", "/app/_realm/"}; !slices.Equal(got, want) {
			t.Errorf("ListTree(/) = %v, want %v", got, want)
		}
	})
}

// TestListTreeScans verifies that ListTree reads a Scanner's keys with one
// scan, and Lists nothing.
func TestListTreeScans(t *testing.T) {
	l := newListingStorage(subtreeTree(nil))
	src := &scanningStorage{listingStorage: l}
	inh, _ := NewInheritableStorage(src)
	if _, err := ListTree(context.Background(), inh, "/", 0); err != nil {
		t.Fatal(err)
	}
	if src.scans != 1 || len(l.reads) != 0 {
		t.Errorf("ListTree scanned %d times and read %v, want one scan and no reads", src.scans, l.reads)
	}

	boom := errors.New("storage unavailable")
	src.scanErr, src.failAfter = boom, 3
	if _, err := ListTree(context.Background(), inh, "/", 0); !errors.Is(err, boom) {
		t.Errorf("ListTree over a failing scan = %v, want %v", err, boom)
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
	bothWays(t, func(t *testing.T, scan bool) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				testWalkTreeMatchesGet(t, scan, tt.root, subtreeTree(tt.overrides, tt.missing...))
			})
		}
	})
}

func testWalkTreeMatchesGet(t *testing.T, scan bool, root string, entries map[string]string) {
	ctx := context.Background()
	src, s := sources(entries, scan)
	inh, _ := NewInheritableStorage(s)
	views, err := walkPaths(t, inh, root)
	if err != nil {
		t.Fatal(err)
	}
	for key, n := range src.reads {
		if n > 1 {
			t.Errorf("the walk read %s %d times", key, n)
		}
		if scan && !slices.Contains(consultedAncestors(root), key) {
			t.Errorf("the walk read %s, which it scanned", key)
		}
	}
	if scan && s.(*scanningStorage).scans != 2 {
		t.Errorf("the walk scanned %d times, want 2 (ListTree and ReadTree)", s.(*scanningStorage).scans)
	}
	// the root, then every chamber below it, in order
	wantPaths := []string{root}
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		if key != root && strings.HasPrefix(key, root) {
			wantPaths = append(wantPaths, key)
		}
	}
	var gotPaths []string
	for _, v := range views {
		gotPaths = append(gotPaths, v.Path)
	}
	if !slices.Equal(gotPaths, wantPaths) {
		t.Errorf("views of\n  %v\nwant\n  %v", gotPaths, wantPaths)
	}

	for _, v := range views {
		if v.Path == root && !v.Stored {
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
	bothWays(t, func(t *testing.T, scan bool) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, src := sources(subtreeTree(nil, tt.missing...), scan)
				inh, _ := NewInheritableStorage(src)
				views, err := walkPaths(t, inh, tt.root)
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, v := range views {
					got = append(got, v.Path+"="+strings.Join(slices.Sorted(maps.Keys(v.Chamber.Rules)), " "))
				}
				want := make([]string, len(tt.want))
				for i := range tt.want {
					want[i] = strings.ReplaceAll(strings.ReplaceAll(tt.want[i], "[", ""), "]", "")
				}
				if !slices.Equal(got, want) {
					t.Errorf("views =\n  %q\nwant\n  %q", got, want)
				}
			})
		}
	})
}

// TestWalkTreeWithoutInheritance verifies that each view of storage that
// does not apply inheritance is the chamber as stored.
func TestWalkTreeWithoutInheritance(t *testing.T) {
	bothWays(t, testWalkTreeWithoutInheritance)
}

func testWalkTreeWithoutInheritance(t *testing.T, scan bool) {
	_, src := sources(subtreeTree(nil), scan)
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

// TestReadTreeStopsAtFailedScan verifies that a scan that fails partway ends
// the walk with its error, at the path being walked, and that a failed read
// of the root's ancestors ends it before the scan starts.
func TestReadTreeStopsAtFailedScan(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("storage unavailable")
	l := newListingStorage(subtreeTree(nil))
	src := &scanningStorage{listingStorage: l}
	inh, _ := NewInheritableStorage(src)
	paths, err := ListTree(ctx, inh, "/prod/", 0)
	if err != nil {
		t.Fatal(err)
	}

	// /prod/ is the first entry scanned, /prod/us-east/ the second
	src.scanErr, src.failAfter = boom, 1
	var views []View
	for v, err := range ReadTree(ctx, inh, paths) {
		if err != nil {
			if !errors.Is(err, boom) || v.Path != "/prod/us-east/" {
				t.Errorf("walk error = %v at %s, want %v at /prod/us-east/", err, v.Path, boom)
			}
			break
		}
		views = append(views, v)
	}
	if len(views) != 1 || views[0].Path != "/prod/" {
		t.Errorf("views before the failure = %+v, want /prod/ alone", views)
	}

	src.scanErr = nil
	l.failing = map[string]error{"/": boom}
	scans := src.scans
	for v, err := range ReadTree(ctx, inh, paths) {
		if !errors.Is(err, boom) {
			t.Errorf("walk over a failing ancestor yielded %+v, %v; want %v", v, err, boom)
		}
	}
	if src.scans != scans {
		t.Error("the walk scanned after failing to read the root's ancestors")
	}
}

// TestReadTreeSkipsUnlistedEntries verifies that ReadTree yields the views of
// the paths it is given alone: a chamber created after the paths were listed
// is left out, and one deleted since is a path without a chamber, as when
// reading each path.
func TestReadTreeSkipsUnlistedEntries(t *testing.T) {
	bothWays(t, func(t *testing.T, scan bool) {
		ctx := context.Background()
		l, src := sources(subtreeTree(nil), scan)
		inh, _ := NewInheritableStorage(src)
		paths, err := ListTree(ctx, inh, "/prod/", 0)
		if err != nil {
			t.Fatal(err)
		}
		l.entries["/prod/new/"] = `{"rules":{}}`
		l.entries["/prod/us-east/a/"] = `{"rules":{}}`
		delete(l.entries, "/prod/us-east/")
		var got []string
		for v, err := range ReadTree(ctx, inh, paths) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, v.Path)
		}
		if want := []string{"/prod/", "/prod/us-east/canary/"}; !slices.Equal(got, want) {
			t.Errorf("views of %v, want %v", got, want)
		}
	})
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

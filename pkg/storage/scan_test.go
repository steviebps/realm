package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage/etcdtest"
	"github.com/steviebps/realm/pkg/storage/pgtest"
	"github.com/steviebps/realm/utils"
)

// unscanned hides every capability of the storage it holds but Storage's
// methods, so subtree reads list and read it one level and one path at a
// time.
type unscanned struct{ Storage }

// scanTreeEntries is what every Scanner's tests write: chambers whose names sort
// around "/" ("-", ".", "0"), paths that only lead to chambers, realm's
// metadata in several spellings, and a chamber that cannot be parsed.
var scanTreeEntries = map[string]string{
	"/":                  `{"rules":{"k":{"type":"number","value":0},"root":{"type":"boolean","value":true}}}`,
	"/prod/":             `{"rules":{"k":{"type":"number","value":1}}}`,
	"/prod/us/":          `{"rules":{"k":{"type":"number","value":2}}}`,
	"/prod/us/canary/":   `{"rules":{"k":{"type":"number","value":3},"sealed":{"type":"string","value":"x","sealed":true}}}`,
	"/prod/eu/west/":     `{"rules":{"k":{"type":"number","value":4}}}`,
	"/production/":       `{"rules":{"k":{"type":"number","value":5}}}`,
	"/prod-eu/":          `{"rules":{"k":{"type":"number","value":6}}}`,
	"/prod.x/":           `{"rules":{"k":{"type":"number","value":7}}}`,
	"/prod0/":            `{"rules":{"k":{"type":"number","value":8}}}`,
	"/a/b/c/":            `{"rules":{"k":{"type":"number","value":9}}}`,
	"/a-b/":              `{"rules":{"k":{"type":"number","value":10}}}`,
	"/a/b0/":             `{"rules":{"k":{"type":"number","value":11}}}`,
	"/app/_realm/":       `{"rules":{"k":{"type":"number","value":12}}}`,
	"/broken/":           `nope`,
	"/broken/child/":     `{"rules":{"k":{"type":"number","value":13}}}`,
	"/_realm/":           `{}`,
	"/_realm/catalog/":   `{"tenant":{}}`,
	"/_realm/history/x/": `[]`,
	"/_REALM/keys/k/":    `{}`,
}

// scanPrefixes are the prefixes the Scanner tests read.
var scanPrefixes = []string{"/", "/prod/", "/prod", "/a/", "/a/b/", "/prod/us/canary/", "/nope/", "/broken/", "/_realm/"}

// newScanTree returns a fresh instance of a Scanner holding scanTreeEntries.
func newScanTree(t *testing.T, open func(t *testing.T) Scanner) Scanner {
	t.Helper()
	s := open(t)
	for _, key := range slices.Sorted(maps.Keys(scanTreeEntries)) {
		mustPut(t, s, key, scanTreeEntries[key])
	}
	return s
}

// collectScan returns what seq yields, failing the test on an error.
func collectScan[T any](t *testing.T, seq iter.Seq2[T, error]) []T {
	t.Helper()
	var out []T
	for v, err := range seq {
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	return out
}

// listAndGet returns what a Scan of prefix must yield: the entry at each path
// a level-by-level listing finds, as Get returns it.
func listAndGet(t *testing.T, s Storage, prefix string) []*StorageEntry {
	t.Helper()
	ctx := context.Background()
	paths, err := ListTree(ctx, unscanned{s}, canonicalChamberPath(prefix), 0)
	if err != nil {
		t.Fatalf("ListTree(%s): %v", prefix, err)
	}
	var out []*StorageEntry
	for _, p := range paths {
		e, err := s.Get(ctx, p)
		if isNotFoundErr(err) {
			continue
		}
		if err != nil {
			t.Fatalf("Get %s: %v", p, err)
		}
		out = append(out, &StorageEntry{Key: p, Value: e.Value, Version: e.Version})
	}
	return out
}

// TestScanMatchesListAndGet holds every Scanner to its contract: a Scan
// yields exactly the entries that listing each level and reading each path
// finds, in order, with the same values and versions, and ListTree and
// ReadTree answer the same with and without it.
func TestScanMatchesListAndGet(t *testing.T) {
	// several pages of etcd's, even for small prefixes
	oldScan, oldList := etcdScanPage, etcdListPage
	etcdScanPage, etcdListPage = 2, 3
	t.Cleanup(func() { etcdScanPage, etcdListPage = oldScan, oldList })

	for name, open := range scannerBackends() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := newScanTree(t, open)
			for _, prefix := range scanPrefixes {
				t.Run(prefix, func(t *testing.T) {
					want := listAndGet(t, s, prefix)
					got := collectScan(t, s.Scan(ctx, prefix))
					if len(got) != len(want) {
						t.Fatalf("Scan(%s) yielded %v, want %v", prefix, keysOf(got), keysOf(want))
					}
					for i := range got {
						if got[i].Key != want[i].Key || !bytes.Equal(got[i].Value, want[i].Value) || got[i].Version != want[i].Version {
							t.Errorf("Scan(%s) entry %d = %s %s v%d, want %s %s v%d", prefix, i, got[i].Key, got[i].Value, got[i].Version, want[i].Key, want[i].Value, want[i].Version)
						}
						if got[i].Version == 0 {
							t.Errorf("Scan(%s) entry %s has no version", prefix, got[i].Key)
						}
					}
					if keys := collectScan(t, s.ScanKeys(ctx, prefix)); !slices.Equal(keys, keysOf(want)) {
						t.Errorf("ScanKeys(%s) = %v, want %v", prefix, keys, keysOf(want))
					}

					root := canonicalChamberPath(prefix)
					scanned, err := ListTree(ctx, s, root, 0)
					if err != nil {
						t.Fatal(err)
					}
					listed, err := ListTree(ctx, unscanned{s}, root, 0)
					if err != nil {
						t.Fatal(err)
					}
					if !slices.Equal(scanned, listed) {
						t.Fatalf("ListTree(%s) scanning = %v, listing = %v", root, scanned, listed)
					}
					testReadTreeMatches(t, s, root, scanned)
				})
			}
		})
	}
}

// testReadTreeMatches checks that ReadTree over s, which scans, yields the
// views it yields over s read one path at a time, which are those
// InheritableStorage.Get returns.
func testReadTreeMatches(t *testing.T, s Storage, root string, paths []string) {
	t.Helper()
	ctx := context.Background()
	scanning, _ := NewInheritableStorage(s)
	reading, _ := NewInheritableStorage(unscanned{s})
	got := collectViews(t, ReadTree(ctx, scanning, paths))
	want := collectViews(t, ReadTree(ctx, reading, paths))
	if !slices.Equal(got, want) {
		t.Errorf("ReadTree(%s) scanning =\n  %q\nreading each path =\n  %q", root, got, want)
	}
	// and each view is what Get returns
	for v, err := range ReadTree(ctx, scanning, paths) {
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		if v.Path == root && !v.Stored {
			continue
		}
		e, err := scanning.Get(ctx, v.Path)
		if err != nil {
			if v.Err == nil || v.Err.Error() != err.Error() {
				t.Errorf("%s: view error %v, Get error %v", v.Path, v.Err, err)
			}
			continue
		}
		if v.Revision != e.Version || v.Revision == 0 {
			t.Errorf("%s: view revision %d, Get %d", v.Path, v.Revision, e.Version)
		}
	}
}

// collectViews describes each view a walk yields, failing the test on an
// error.
func collectViews(t *testing.T, views iter.Seq2[View, error]) []string {
	t.Helper()
	var out []string
	for v, err := range views {
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		desc := fmt.Sprintf("%s stored=%t rev=%d", v.Path, v.Stored, v.Revision)
		if v.Err != nil {
			desc += " error=" + v.Err.Error()
		} else {
			var buf bytes.Buffer
			if err := utils.WriteInterfaceWith(&buf, v.Chamber, false); err != nil {
				t.Fatal(err)
			}
			desc += " " + buf.String()
		}
		out = append(out, desc)
	}
	return out
}

func keysOf(entries []*StorageEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	return out
}

// TestScanContract holds every Scanner to the rest of its contract.
func TestScanContract(t *testing.T) {
	for name, open := range scannerBackends() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := newScanTree(t, open)

			t.Run("refuses invalid paths", func(t *testing.T) {
				for _, err := range s.Scan(ctx, "/a/../b/") {
					if !errors.Is(err, ErrInvalidPath) {
						t.Errorf("Scan(/a/../b/) = %v, want ErrInvalidPath", err)
					}
				}
				for _, err := range s.ScanKeys(ctx, "/a/../b/") {
					if !errors.Is(err, ErrInvalidPath) {
						t.Errorf("ScanKeys(/a/../b/) = %v, want ErrInvalidPath", err)
					}
				}
			})

			t.Run("an empty prefix yields nothing", func(t *testing.T) {
				if got := collectScan(t, s.Scan(ctx, "/nope/")); len(got) != 0 {
					t.Errorf("Scan(/nope/) = %v, want nothing", keysOf(got))
				}
			})

			t.Run("stopping early releases the scan", func(t *testing.T) {
				// more stopped scans than any pool has connections
				for range 50 {
					for _, err := range s.Scan(ctx, "/") {
						if err != nil {
							t.Fatalf("Scan(/): %v", err)
						}
						break
					}
					for _, err := range s.ScanKeys(ctx, "/") {
						if err != nil {
							t.Fatalf("ScanKeys(/): %v", err)
						}
						break
					}
				}
				rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if _, err := s.Get(rctx, "/prod/"); err != nil {
					t.Errorf("Get after stopped scans: %v", err)
				}
			})

			t.Run("the limit counts paths that lead to chambers", func(t *testing.T) {
				// /prod/ has /prod/eu/ (no chamber), /prod/eu/west/,
				// /prod/us/ and /prod/us/canary/ below it
				if _, err := ListTree(ctx, s, "/prod/", 4); err != nil {
					t.Errorf("four paths below /prod/ with a limit of four: %v", err)
				}
				if _, err := ListTree(ctx, s, "/prod/", 3); !errors.Is(err, ErrSubtreeTooLarge) {
					t.Errorf("four paths below /prod/ with a limit of three: %v, want ErrSubtreeTooLarge", err)
				}
			})
		})
	}
}

// TestCacheableDoesNotScan verifies that subtree reads through cacheable go
// through its cache, as every other read does: a scan of the source would
// bypass the cache and the versions it hides.
func TestCacheableDoesNotScan(t *testing.T) {
	cache, err := NewBigCacheStorage(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCacheableStorage(cache, newBolt(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	inh, _ := NewInheritableStorage(c)
	if _, ok := Raw(inh).(Scanner); ok {
		t.Fatal("cacheable is a Scanner")
	}
	mustPut(t, inh, "/a/", `{"rules":{}}`)
	mustPut(t, inh, "/a/b/", `{"rules":{}}`)
	paths, err := ListTree(context.Background(), inh, "/a/", 0)
	if err != nil {
		t.Fatal(err)
	}
	for v, err := range ReadTree(context.Background(), inh, paths) {
		if err != nil || v.Revision != 0 {
			t.Errorf("%s: revision %d, error %v; want 0, nil (cacheable reads carry no version)", v.Path, v.Revision, err)
		}
	}
}

// countingStorage counts the calls made of the storage it wraps, which it
// hides every capability of.
type countingStorage struct {
	Storage
	calls *atomic.Int64
}

func (c countingStorage) Get(ctx context.Context, key string) (*StorageEntry, error) {
	c.calls.Add(1)
	return c.Storage.Get(ctx, key)
}

func (c countingStorage) List(ctx context.Context, prefix string) ([]string, error) {
	c.calls.Add(1)
	return c.Storage.List(ctx, prefix)
}

// countingScanner is a countingStorage that scans, counting each scan as one
// call.
type countingScanner struct {
	countingStorage
	sc Scanner
}

func (c countingScanner) Scan(ctx context.Context, prefix string) iter.Seq2[*StorageEntry, error] {
	c.calls.Add(1)
	return c.sc.Scan(ctx, prefix)
}

func (c countingScanner) ScanKeys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	c.calls.Add(1)
	return c.sc.ScanKeys(ctx, prefix)
}

// BenchmarkSubtreeRead reads a subtree of 500 chambers (50 tenants with 9
// locations each, and their tenant chambers) below two ancestors, as a
// subtree read does: ListTree, then ReadTree. It reports the storage calls
// (round trips, on a network backend) each read makes, with and without
// Scan.
func BenchmarkSubtreeRead(b *testing.B) {
	open := map[string]func(b *testing.B) Scanner{
		"boltdb": func(b *testing.B) Scanner {
			s, err := NewBoltStorage(map[string]string{"path": b.TempDir()})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close(context.Background()) })
			return s.(Scanner)
		},
	}
	if os.Getenv(pgtest.EnvURL) != "" {
		open["postgres"] = func(b *testing.B) Scanner {
			s, err := NewPostgresStorage(map[string]string{"url": pgtest.URL(b)})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close(context.Background()) })
			return s.(Scanner)
		}
	}
	if os.Getenv(etcdtest.EnvEndpoints) != "" {
		open["etcd"] = func(b *testing.B) Scanner {
			s, err := NewEtcdStorage(etcdtest.Options(b))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close(context.Background()) })
			return s.(Scanner)
		}
	}
	for name, open := range open {
		b.Run(name, func(b *testing.B) {
			ctx := context.Background()
			sc := open(b)
			chamber := `{"rules":{"checkout.newFlow":{"type":"boolean","value":true,"targeting":[{"clauses":[{"attribute":"tenant","op":"in","values":["a","b"]}],"value":false}]},"rate.limit":{"type":"number","value":500}}}`
			var ops []Op
			for _, p := range []string{"/", "/prod/", "/prod/tenants/"} {
				ops = append(ops, put(p, chamber))
			}
			for tenant := range 50 {
				ops = append(ops, put(fmt.Sprintf("/prod/tenants/t-%02d/", tenant), chamber))
				for loc := range 9 {
					ops = append(ops, put(fmt.Sprintf("/prod/tenants/t-%02d/loc-%d/", tenant, loc), chamber))
				}
			}
			for chunk := range slices.Chunk(ops, 100) {
				if err := sc.(Transactional).Txn(ctx, nil, chunk); err != nil {
					b.Fatal(err)
				}
			}
			calls := &atomic.Int64{}
			counted := countingStorage{Storage: sc, calls: calls}
			for _, way := range []struct {
				name string
				s    Storage
			}{{"scan", countingScanner{countingStorage: counted, sc: sc}}, {"levels", counted}} {
				b.Run(way.name, func(b *testing.B) {
					inh, _ := NewInheritableStorage(way.s)
					calls.Store(0)
					views, reads := 0, 0
					for b.Loop() {
						reads++
						paths, err := ListTree(ctx, inh, "/prod/tenants/", 0)
						if err != nil {
							b.Fatal(err)
						}
						for _, err := range ReadTree(ctx, inh, paths) {
							if err != nil {
								b.Fatal(err)
							}
							views++
						}
					}
					if views != 501*reads {
						b.Fatalf("%d views in %d reads, want 501 each", views, reads)
					}
					b.ReportMetric(float64(calls.Load())/float64(reads), "calls/op")
				})
			}
		})
	}
}

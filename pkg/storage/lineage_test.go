package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/utils"
)

// lineageTree is a sample hierarchy: every chamber inherits /, /prod/ is
// overridden in places, and /staging/ does not exist.
var lineageTree = map[string]string{
	"/":                     `{"rules":{"checkout.newFlow":{"type":"boolean","value":false},"search.provider":{"type":"string","value":"elastic"},"ui.theme":{"type":"string","value":"classic"}}}`,
	"/prod/":                `{"rules":{"checkout.newFlow":{"type":"boolean","value":false,"targeting":[{"value":true,"rollout":{"percentage":25}}]},"rate.limit":{"type":"number","value":500},"prod.audit":{"type":"boolean","value":true}}}`,
	"/prod/us-east/":        `{"rules":{"rate.limit":{"type":"number","value":1000},"search.provider":{"type":"string","value":"opensearch"},"region.failover":{"type":"boolean","value":true}}}`,
	"/prod/us-east/canary/": `{"rules":{"checkout.newFlow":{"type":"boolean","value":true}}}`,
	"/staging/eu/":          `{"rules":{"ui.theme":{"type":"string","value":"dark"}}}`,
	"/empty/child/":         `{"rules":{}}`,
	"/a/":                   `{"rules":{"k":{"type":"number","value":1},"onlyA":{"type":"number","value":1}}}`,
	"/a/b/":                 `{"rules":{"k":{"type":"number","value":2}}}`,
	"/a/b/c/":               `{"rules":{"k":{"type":"number","value":3}}}`,
	"/a/b/c/d/":             `{"rules":{}}`,
	// read at the key exactly as given, so a slashless path needs its own entry
	"/a/b": `{"rules":{"slashless":{"type":"boolean","value":true}}}`,
}

// newLineageTree returns lineageTree with overrides written and the keys in
// missing removed.
func newLineageTree(overrides map[string]string, missing ...string) *mapStorage {
	entries := maps.Clone(lineageTree)
	maps.Copy(entries, overrides)
	for _, k := range missing {
		delete(entries, k)
	}
	return &mapStorage{entries: entries}
}

// referenceGet is the inherited read written as the walk Get used before it
// was built on Lineage: top down from the root chamber, each ancestor
// inheriting from the one above. The tests compare Get with it byte for byte.
func referenceGet(ctx context.Context, src Storage, logicalPath string) ([]byte, error) {
	leafEntry, err := src.Get(ctx, logicalPath)
	if err != nil {
		return nil, err
	}
	leaf := &realm.Chamber{}
	if err := json.Unmarshal(leafEntry.Value, leaf); err != nil {
		return nil, err
	}
	if clean := path.Clean(logicalPath); clean != "/" {
		walk := []string{"/"}
		if dir := path.Dir(clean); dir != "/" {
			cur := "/"
			for _, v := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
				cur += utils.EnsureTrailingSlash(v)
				walk = append(walk, cur)
			}
		}
		c := &realm.Chamber{Rules: map[string]*realm.Flag{}}
		for _, cur := range walk {
			entry, err := src.Get(ctx, cur)
			if err != nil {
				var nf *NotFoundError
				if errors.As(err, &nf) {
					continue
				}
				return nil, err
			}
			curChamber := &realm.Chamber{Rules: map[string]*realm.Flag{}}
			if err := json.Unmarshal(entry.Value, curChamber); err != nil {
				return nil, fmt.Errorf("could not parse parent chamber %s: %w", cur, err)
			}
			curChamber.InheritFrom(c)
			c = curChamber
		}
		leaf.InheritFrom(c)
	}
	buf := new(bytes.Buffer)
	if err := utils.WriteInterfaceWith(buf, leaf, false); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func TestConsultedAncestors(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{"/", nil},
		{"//", nil},
		{"/a/", []string{"/"}},
		{"/a", []string{"/"}},
		{"/a/b/", []string{"/", "/a/"}},
		{"/a/b", []string{"/", "/a/"}},
		{"/a//b/", []string{"/", "/a/"}},
		{"/a/b/c/", []string{"/", "/a/", "/a/b/"}},
		{"/x/_realm/y/", []string{"/", "/x/", "/x/_realm/"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := consultedAncestors(tt.path)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("consultedAncestors(%q) = %v, want %v", tt.path, got, tt.want)
			}
			// the layers view relies on ancestors of a non-reserved path never
			// being reserved
			if !IsReservedPath(tt.path) {
				for _, a := range got {
					if IsReservedPath(a) {
						t.Errorf("ancestor %q of %q is reserved", a, tt.path)
					}
				}
			}
		})
	}
}

// layerSummary describes a layer as "path=status{keys}" for compact assertions.
func layerSummary(l Layer) string {
	switch {
	case l.Err != nil:
		return l.Path + "=invalid"
	case l.Chamber == nil:
		return l.Path + "=missing"
	default:
		return fmt.Sprintf("%s=ok%v", l.Path, slices.Sorted(maps.Keys(l.Chamber.Rules)))
	}
}

func TestLayers(t *testing.T) {
	boom := errors.New("storage unavailable")
	tests := []struct {
		name        string
		path        string
		overrides   map[string]string
		missing     []string
		failing     map[string]error
		wantErr     error // from Layers itself
		wantLayers  []string
		wantSources map[string]string
		wantMerge   string // substring of Merge's error, when it fails
	}{
		{
			name: "three levels with overrides, then the root",
			path: "/prod/us-east/canary/",
			wantLayers: []string{
				"/prod/us-east/canary/=ok[checkout.newFlow]",
				"/prod/us-east/=ok[rate.limit region.failover search.provider]",
				"/prod/=ok[checkout.newFlow prod.audit rate.limit]",
				"/=ok[checkout.newFlow search.provider ui.theme]",
			},
			wantSources: map[string]string{
				"checkout.newFlow": "/prod/us-east/canary/",
				"prod.audit":       "/prod/",
				"rate.limit":       "/prod/us-east/",
				"region.failover":  "/prod/us-east/",
				"search.provider":  "/prod/us-east/",
				"ui.theme":         "/",
			},
		},
		{
			name: "two levels, then the root",
			path: "/prod/us-east/",
			wantLayers: []string{
				"/prod/us-east/=ok[rate.limit region.failover search.provider]",
				"/prod/=ok[checkout.newFlow prod.audit rate.limit]",
				"/=ok[checkout.newFlow search.provider ui.theme]",
			},
			wantSources: map[string]string{
				"checkout.newFlow": "/prod/", "prod.audit": "/prod/", "rate.limit": "/prod/us-east/",
				"region.failover": "/prod/us-east/", "search.provider": "/prod/us-east/", "ui.theme": "/",
			},
		},
		{
			name:       "top-level chamber inherits the root",
			path:       "/prod/",
			wantLayers: []string{"/prod/=ok[checkout.newFlow prod.audit rate.limit]", "/=ok[checkout.newFlow search.provider ui.theme]"},
			wantSources: map[string]string{
				"checkout.newFlow": "/prod/", "prod.audit": "/prod/", "rate.limit": "/prod/",
				"search.provider": "/", "ui.theme": "/",
			},
		},
		{
			name:        "root",
			path:        "/",
			wantLayers:  []string{"/=ok[checkout.newFlow search.provider ui.theme]"},
			wantSources: map[string]string{"checkout.newFlow": "/", "search.provider": "/", "ui.theme": "/"},
		},
		{
			name:       "missing parent",
			path:       "/staging/eu/",
			wantLayers: []string{"/staging/eu/=ok[ui.theme]", "/staging/=missing", "/=ok[checkout.newFlow search.provider ui.theme]"},
			wantSources: map[string]string{
				"ui.theme": "/staging/eu/", "checkout.newFlow": "/", "search.provider": "/",
			},
		},
		{
			name:        "missing root",
			path:        "/prod/",
			missing:     []string{"/"},
			wantLayers:  []string{"/prod/=ok[checkout.newFlow prod.audit rate.limit]", "/=missing"},
			wantSources: map[string]string{"checkout.newFlow": "/prod/", "prod.audit": "/prod/", "rate.limit": "/prod/"},
		},
		{
			name:        "empty chamber under a missing parent and an empty root",
			path:        "/empty/child/",
			overrides:   map[string]string{"/": `{"rules":{}}`},
			wantLayers:  []string{"/empty/child/=ok[]", "/empty/=missing", "/=ok[]"},
			wantSources: map[string]string{},
		},
		{
			name:       "nearest definition wins",
			path:       "/a/b/c/d/",
			wantLayers: []string{"/a/b/c/d/=ok[]", "/a/b/c/=ok[k]", "/a/b/=ok[k]", "/a/=ok[k onlyA]", "/=ok[checkout.newFlow search.provider ui.theme]"},
			wantSources: map[string]string{
				"k": "/a/b/c/", "onlyA": "/a/",
				"checkout.newFlow": "/", "search.provider": "/", "ui.theme": "/",
			},
		},
		{
			name:       "slashless path",
			path:       "/a/b",
			wantLayers: []string{"/a/b/=ok[slashless]", "/a/=ok[k onlyA]", "/=ok[checkout.newFlow search.provider ui.theme]"},
			wantSources: map[string]string{
				"slashless": "/a/b/", "k": "/a/", "onlyA": "/a/",
				"checkout.newFlow": "/", "search.provider": "/", "ui.theme": "/",
			},
		},
		{
			name:      "unparsable parent",
			path:      "/prod/us-east/canary/",
			overrides: map[string]string{"/prod/": `{"rules":`},
			wantLayers: []string{
				"/prod/us-east/canary/=ok[checkout.newFlow]",
				"/prod/us-east/=ok[rate.limit region.failover search.provider]",
				"/prod/=invalid",
				"/=ok[checkout.newFlow search.provider ui.theme]",
			},
			wantMerge: "could not parse parent chamber /prod/",
		},
		{
			name:       "the farthest unparsable parent is reported",
			path:       "/a/b/c/d/",
			overrides:  map[string]string{"/a/": `{"rules":`, "/a/b/c/": `{"rules":`},
			wantLayers: []string{"/a/b/c/d/=ok[]", "/a/b/c/=invalid", "/a/b/=ok[k]", "/a/=invalid", "/=ok[checkout.newFlow search.provider ui.theme]"},
			wantMerge:  "could not parse parent chamber /a/:",
		},
		{
			name:       "an unparsable root breaks every chamber below it",
			path:       "/a/b/",
			overrides:  map[string]string{"/": `{"rules":`, "/a/": `{"rules":`},
			wantLayers: []string{"/a/b/=ok[k]", "/a/=invalid", "/=invalid"},
			wantMerge:  "could not parse parent chamber /:",
		},
		{
			name:       "unparsable chamber",
			path:       "/prod/us-east/",
			overrides:  map[string]string{"/prod/us-east/": `nope`},
			wantLayers: []string{"/prod/us-east/=invalid", "/prod/=ok[checkout.newFlow prod.audit rate.limit]", "/=ok[checkout.newFlow search.provider ui.theme]"},
			wantMerge:  "invalid character",
		},
		{
			name:    "failing parent read",
			path:    "/prod/us-east/canary/",
			failing: map[string]error{"/prod/": boom},
			wantErr: boom,
		},
		{
			name:    "failing root read",
			path:    "/prod/",
			failing: map[string]error{"/": boom},
			wantErr: boom,
		},
		{
			name:    "missing chamber",
			path:    "/nope/",
			wantErr: &NotFoundError{Key: "/nope/"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			src := newLineageTree(tt.overrides, tt.missing...)
			src.failing = tt.failing
			inh, _ := NewInheritableStorage(src)
			lin, err := inh.(Layered).Layers(ctx, tt.path)
			if tt.wantErr != nil {
				var nf *NotFoundError
				if errors.As(tt.wantErr, &nf) {
					if !errors.As(err, &nf) {
						t.Fatalf("Layers error = %v, want a NotFoundError", err)
					}
				} else if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Layers error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Layers: %v", err)
			}
			if !lin.Inheritable {
				t.Error("Inheritable = false, want true")
			}
			var got []string
			for _, l := range lin.Layers {
				got = append(got, layerSummary(l))
			}
			if !slices.Equal(got, tt.wantLayers) {
				t.Errorf("layers =\n  %v\nwant\n  %v", got, tt.wantLayers)
			}

			res, err := lin.Resolve()
			if tt.wantMerge != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantMerge) {
					t.Fatalf("Resolve error = %v, want one containing %q", err, tt.wantMerge)
				}
				if _, mergeErr := lin.Merge(); mergeErr == nil || mergeErr.Error() != err.Error() {
					t.Errorf("Merge error = %v, want %v", mergeErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.Sources == nil || !maps.Equal(res.Sources, tt.wantSources) {
				t.Errorf("sources = %v, want %v", res.Sources, tt.wantSources)
			}
			if len(res.SealedBy) != 0 || len(res.Hidden) != 0 {
				t.Errorf("an unsealed tree reports seals %v, hidden %v", res.SealedBy, res.Hidden)
			}
			merged, err := lin.Merge()
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			if keys := slices.Sorted(maps.Keys(merged.Rules)); !slices.Equal(keys, slices.Sorted(maps.Keys(tt.wantSources))) {
				t.Errorf("merged keys = %v, want the keys of sources", keys)
			}
		})
	}
}

// TestGetMatchesReference locks the inherited view: Get must return exactly
// what the walk it replaced returned, byte for byte, including its errors.
func TestGetMatchesReference(t *testing.T) {
	boom := errors.New("storage unavailable")
	tests := []struct {
		name      string
		path      string
		overrides map[string]string
		missing   []string
		failing   map[string]error
		want      string // the merged chamber's rules, when the read succeeds
	}{
		{name: "canary", path: "/prod/us-east/canary/", want: `{"checkout.newFlow":{"type":"boolean","value":true},"prod.audit":{"type":"boolean","value":true},"rate.limit":{"type":"number","value":1000},"region.failover":{"type":"boolean","value":true},"search.provider":{"type":"string","value":"opensearch"},"ui.theme":{"type":"string","value":"classic"}}`},
		{name: "us-east", path: "/prod/us-east/"},
		{name: "prod", path: "/prod/", want: `{"checkout.newFlow":{"type":"boolean","value":false,"targeting":[{"value":true,"rollout":{"percentage":25}}]},"prod.audit":{"type":"boolean","value":true},"rate.limit":{"type":"number","value":500},"search.provider":{"type":"string","value":"elastic"},"ui.theme":{"type":"string","value":"classic"}}`},
		{name: "root", path: "/", want: `{"checkout.newFlow":{"type":"boolean","value":false},"search.provider":{"type":"string","value":"elastic"},"ui.theme":{"type":"string","value":"classic"}}`},
		{name: "missing parent", path: "/staging/eu/", want: `{"checkout.newFlow":{"type":"boolean","value":false},"search.provider":{"type":"string","value":"elastic"},"ui.theme":{"type":"string","value":"dark"}}`},
		{name: "missing root", path: "/prod/", missing: []string{"/"}, want: `{"checkout.newFlow":{"type":"boolean","value":false,"targeting":[{"value":true,"rollout":{"percentage":25}}]},"prod.audit":{"type":"boolean","value":true},"rate.limit":{"type":"number","value":500}}`},
		{name: "empty chamber inherits the root", path: "/empty/child/", want: `{"checkout.newFlow":{"type":"boolean","value":false},"search.provider":{"type":"string","value":"elastic"},"ui.theme":{"type":"string","value":"classic"}}`},
		{name: "empty", path: "/empty/child/", overrides: map[string]string{"/": `{"rules":{}}`}, want: `{}`},
		{name: "nearest wins", path: "/a/b/c/d/", want: `{"checkout.newFlow":{"type":"boolean","value":false},"k":{"type":"number","value":3},"onlyA":{"type":"number","value":1},"search.provider":{"type":"string","value":"elastic"},"ui.theme":{"type":"string","value":"classic"}}`},
		{name: "slashless", path: "/a/b"},
		{name: "unparsable parent", path: "/prod/us-east/canary/", overrides: map[string]string{"/prod/": `{"rules":`}},
		{name: "two unparsable parents", path: "/a/b/c/d/", overrides: map[string]string{"/a/": `{"rules":`, "/a/b/c/": `[]`}},
		{name: "unparsable root", path: "/a/b/", overrides: map[string]string{"/": `{"rules":`, "/a/": `[]`}},
		{name: "unparsable root read at the root", path: "/", overrides: map[string]string{"/": `{"rules":`}},
		{name: "unparsable chamber", path: "/prod/us-east/", overrides: map[string]string{"/prod/us-east/": `nope`}},
		{name: "failing parent", path: "/prod/us-east/canary/", failing: map[string]error{"/prod/": boom}},
		{name: "failing root", path: "/prod/", failing: map[string]error{"/": boom}},
		{name: "missing chamber", path: "/nope/"},
		{name: "invalid path", path: "/prod/../prod/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			src := newLineageTree(tt.overrides, tt.missing...)
			src.failing = tt.failing
			inh, _ := NewInheritableStorage(src)

			got, gotErr := inh.Get(ctx, tt.path)
			var want []byte
			wantErr := ValidatePath(tt.path)
			if wantErr == nil {
				want, wantErr = referenceGet(ctx, src, tt.path)
			}
			if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
				t.Fatalf("Get error = %v, want %v", gotErr, wantErr)
			}
			if gotErr != nil {
				return
			}
			if !bytes.Equal(got.Value, want) {
				t.Fatalf("Get =\n  %s\nwant\n  %s", got.Value, want)
			}

			if tt.want != "" {
				var rules json.RawMessage
				if err := json.Unmarshal(got.Value, &struct {
					Rules *json.RawMessage `json:"rules"`
				}{&rules}); err != nil {
					t.Fatal(err)
				}
				if string(rules) != tt.want {
					t.Errorf("rules =\n  %s\nwant\n  %s", rules, tt.want)
				}
			}
		})
	}
}

func TestMergeDoesNotMutateLayers(t *testing.T) {
	inh, _ := NewInheritableStorage(newLineageTree(nil))
	lin, err := inh.(Layered).Layers(context.Background(), "/prod/us-east/canary/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lin.Resolve(); err != nil {
		t.Fatal(err)
	}
	if _, err := lin.Merge(); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(lin.Layers[0].Chamber.Rules)); !slices.Equal(got, []string{"checkout.newFlow"}) {
		t.Errorf("the chamber's own rules became %v", got)
	}
}

func TestReadLineage(t *testing.T) {
	ctx := context.Background()
	src := newLineageTree(nil)

	plain, err := ReadLineage(ctx, src, "/prod/us-east/canary/")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Inheritable || len(plain.Layers) != 1 {
		t.Fatalf("without inheritance: %+v, want one layer and no inheritance", plain)
	}
	if got := layerSummary(plain.Layers[0]); got != "/prod/us-east/canary/=ok[checkout.newFlow]" {
		t.Errorf("layer = %s", got)
	}

	inh, _ := NewInheritableStorage(src)
	layered, err := ReadLineage(ctx, inh, "/prod/us-east/canary/")
	if err != nil {
		t.Fatal(err)
	}
	if !layered.Inheritable || len(layered.Layers) != 4 {
		t.Errorf("with inheritance: %d layers, inheritable %v", len(layered.Layers), layered.Inheritable)
	}

	var nf *NotFoundError
	if _, err := ReadLineage(ctx, src, "/nope/"); !errors.As(err, &nf) {
		t.Errorf("missing chamber: %v, want a NotFoundError", err)
	}
	// backends reject parent references
	bc, err := NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLineage(ctx, bc, "/prod/../x/"); err == nil {
		t.Error("a path with .. was accepted")
	}
}

// TestInheritedRevision verifies that the inherited view's revision is the
// highest among the chamber and the parents it merges, on storage that
// tracks revisions, and 0 on storage that does not.
func TestInheritedRevision(t *testing.T) {
	ctx := context.Background()
	for name, open := range transactionalBackends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			inh, _ := NewInheritableStorage(s)
			// put writes a chamber at key and returns its revision.
			put := func(key string) uint64 {
				t.Helper()
				mustPut(t, s, key, `{"rules":{}}`)
				return versionOf(t, s, key)
			}

			own := put("/a/b/")
			if got := versionOf(t, inh, "/a/b/"); got != own {
				t.Errorf("without parents: %d, want the chamber's own %d", got, own)
			}
			for _, parent := range []string{"/a/", "/", "/a/b/"} {
				rev := put(parent)
				if got := versionOf(t, inh, "/a/b/"); got != rev {
					t.Errorf("after writing %s: %d, want %d", parent, got, rev)
				}
				lin, err := ReadLineage(ctx, inh, "/a/b/")
				if err != nil {
					t.Fatal(err)
				}
				if got := lin.Revision(); got != rev {
					t.Errorf("after writing %s: lineage revision %d, want %d", parent, got, rev)
				}
			}
		})
	}

	// storage without revisions has none to report
	plain, _ := NewInheritableStorage(newLineageTree(nil))
	if got := versionOf(t, plain, "/a/b/"); got != 0 {
		t.Errorf("unversioned storage: revision %d, want 0", got)
	}
}

// sealTree is a hierarchy with seals: / seals "entitlement", which /t/ and
// /t/a/ defined anyway; /t/ seals "limit", which /t/a/ defined anyway, and
// "own", which nothing below it defines.
var sealTree = map[string]string{
	"/":       `{"rules":{"entitlement":{"type":"boolean","value":false,"sealed":true},"open":{"type":"number","value":1}}}`,
	"/t/":     `{"rules":{"entitlement":{"type":"boolean","value":true,"sealed":true},"limit":{"type":"number","value":5,"sealed":true},"own":{"type":"string","value":"t","sealed":true},"open":{"type":"number","value":2}}}`,
	"/t/a/":   `{"rules":{"entitlement":{"type":"boolean","value":true},"limit":{"type":"number","value":50},"open":{"type":"number","value":3},"nil":null}}`,
	"/t/a/b/": `{"rules":{}}`,
	// a chamber's own seal constrains only the chambers below it
	"/u/": `{"rules":{"x":{"type":"number","value":1,"sealed":true}}}`,
}

func TestMergeSeals(t *testing.T) {
	ctx := context.Background()
	inh, _ := NewInheritableStorage(&mapStorage{entries: maps.Clone(sealTree)})

	tests := []struct {
		path         string
		wantSources  map[string]string
		wantSealedBy map[string]string
		wantHidden   map[string][]string
		wantValues   map[string]any
	}{
		{
			path: "/t/a/b/",
			wantSources: map[string]string{
				"entitlement": "/", "limit": "/t/", "own": "/t/", "open": "/t/a/", "nil": "/t/a/",
			},
			wantSealedBy: map[string]string{"entitlement": "/", "limit": "/t/", "own": "/t/"},
			wantHidden:   map[string][]string{"entitlement": {"/t/a/", "/t/"}, "limit": {"/t/a/"}},
			wantValues:   map[string]any{"entitlement": false, "limit": float64(5), "open": float64(3)},
		},
		{
			path:         "/t/",
			wantSources:  map[string]string{"entitlement": "/", "limit": "/t/", "own": "/t/", "open": "/t/"},
			wantSealedBy: map[string]string{"entitlement": "/"},
			wantHidden:   map[string][]string{"entitlement": {"/t/"}},
			wantValues:   map[string]any{"entitlement": false, "limit": float64(5)},
		},
		{
			path:         "/",
			wantSources:  map[string]string{"entitlement": "/", "open": "/"},
			wantSealedBy: map[string]string{},
			wantHidden:   map[string][]string{},
		},
		{
			path:         "/u/",
			wantSources:  map[string]string{"entitlement": "/", "open": "/", "x": "/u/"},
			wantSealedBy: map[string]string{"entitlement": "/"},
			wantHidden:   map[string][]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			lin, err := inh.(Layered).Layers(ctx, tt.path)
			if err != nil {
				t.Fatal(err)
			}
			res, err := lin.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(res.Sources, tt.wantSources) {
				t.Errorf("sources = %v, want %v", res.Sources, tt.wantSources)
			}
			if !maps.Equal(res.SealedBy, tt.wantSealedBy) {
				t.Errorf("sealedBy = %v, want %v", res.SealedBy, tt.wantSealedBy)
			}
			if !maps.EqualFunc(res.Hidden, tt.wantHidden, slices.Equal) {
				t.Errorf("hidden = %v, want %v", res.Hidden, tt.wantHidden)
			}

			// Get serves the same view
			entry, err := inh.Get(ctx, tt.path)
			if err != nil {
				t.Fatal(err)
			}
			var got realm.Chamber
			if err := json.Unmarshal(entry.Value, &got); err != nil {
				t.Fatal(err)
			}
			for key, want := range tt.wantValues {
				if f := got.Rules[key]; f == nil || f.Value != want {
					t.Errorf("%s served %+v, want value %v", key, f, want)
				}
			}

			// Ancestry agrees with the lineage
			lin, err = inh.(Layered).Layers(ctx, tt.path)
			if err != nil {
				t.Fatal(err)
			}
			a, err := lin.Ancestry()
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(a.Seals, tt.wantSealedBy) {
				t.Errorf("Ancestry seals = %v, want %v", a.Seals, tt.wantSealedBy)
			}
		})
	}
}

func TestAncestry(t *testing.T) {
	ctx := context.Background()
	entries := maps.Clone(sealTree)
	entries["/broken/"] = `{"rules":{"entitlement":{"type":"boolean","value":"nope"},"x":{"type":"number","value":1,"sealed":true}}}`
	entries["/c/"] = `{"rules":{"hold":{"type":"number","value":24,"constraints":{"maximum":72}}}}`
	entries["/c/d/"] = `{"rules":{"hold":{"type":"number","value":24,"constraints":{"maximum":48}},"plain":{"type":"number","value":1}}}`
	entries["/c/d/bad/"] = `{"rules":{"hold":{"type":"number","value":"nope"}}}`
	entries["/broken/child/"] = `{"rules":{}}`
	src := &mapStorage{entries: entries}

	ancestry := func(s Storage, inheritable bool, p string) (*Ancestry, error) {
		lin, err := BuildLineage(ctx, RawLayers(s), p, inheritable)
		if err != nil {
			return nil, err
		}
		return lin.Ancestry()
	}

	tests := []struct {
		name            string
		path            string
		wantSeals       map[string]string
		wantConstrained map[string][]string
	}{
		{"no chamber at the path", "/t/a/new/", map[string]string{"entitlement": "/", "limit": "/t/", "own": "/t/"}, nil},
		{"missing parents", "/nope/deeper/", map[string]string{"entitlement": "/"}, nil},
		{"constraints, farthest first", "/c/d/e/", map[string]string{"entitlement": "/"}, map[string][]string{"hold": {"/c/", "/c/d/"}}},
		{"the chamber's own error does not matter", "/c/d/bad/", map[string]string{"entitlement": "/"}, map[string][]string{"hold": {"/c/", "/c/d/"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ancestry(src, true, tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got.Seals, tt.wantSeals) {
				t.Errorf("seals = %v, want %v", got.Seals, tt.wantSeals)
			}
			constrained := map[string][]string{}
			for key, defs := range got.Constraints {
				for _, d := range defs {
					constrained[key] = append(constrained[key], d.Path)
				}
			}
			if len(constrained) != len(tt.wantConstrained) || !maps.EqualFunc(constrained, tt.wantConstrained, slices.Equal) {
				t.Errorf("constraints = %v, want %v", constrained, tt.wantConstrained)
			}
		})
	}

	// a parent that cannot be parsed fails the ancestry, as it fails reads
	var ce *ChamberError
	if _, err := ancestry(src, true, "/broken/child/"); !errors.As(err, &ce) || ce.Path != "/broken/" {
		t.Errorf("below a broken parent: %v, want its ChamberError", err)
	}
	inh, _ := NewInheritableStorage(src)
	if _, err := inh.Get(ctx, "/broken/child/"); !errors.As(err, &ce) || ce.Path != "/broken/" {
		t.Errorf("a read below the broken parent: %v, want the same ChamberError", err)
	}

	if got, err := ancestry(src, false, "/t/a/"); err != nil || len(got.Seals) != 0 || len(got.Constraints) != 0 {
		t.Errorf("without inheritance: %+v, %v; want nothing sealed or constrained", got, err)
	}
	if _, err := inh.(Layered).Layers(ctx, "/t/../x/"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("a path with ..: %v, want ErrInvalidPath", err)
	}
	if _, err := ReadLineage(ctx, src, "/t/../x/"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("a path with .. without inheritance: %v, want ErrInvalidPath", err)
	}
	src.failing = map[string]error{"/t/": errors.New("boom")}
	if _, err := ancestry(src, true, "/t/a/"); err == nil {
		t.Error("a failed read of a parent was ignored")
	}
}

func TestBuildLineage(t *testing.T) {
	ctx := context.Background()
	src := &mapStorage{entries: map[string]string{
		"/":     `{"rules":{"a":{"type":"boolean","value":true}}}`,
		"/x/y/": `{"rules":{"b":{"type":"boolean","value":true}}}`,
		"/bad/": `{"rules":{"a":{"type":"boolean","value":"no"}}}`,
	}}
	summary := func(lin *Lineage) []string {
		var ls []string
		for _, l := range lin.Layers {
			ls = append(ls, layerSummary(l))
		}
		return ls
	}

	lin, err := BuildLineage(ctx, RawLayers(src), "/x/y/z/", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/x/y/z/=missing", "/x/y/=ok[b]", "/x/=missing", "/=ok[a]"}; !slices.Equal(summary(lin), want) {
		t.Errorf("layers = %v, want %v, nearest first", summary(lin), want)
	}
	if lin.Stored() {
		t.Error("a path without a chamber is Stored")
	}

	lin, err = BuildLineage(ctx, RawLayers(src), "/x/y/", false)
	if err != nil || !lin.Stored() || !slices.Equal(summary(lin), []string{"/x/y/=ok[b]"}) {
		t.Errorf("without inheritance: %v, %v; want the chamber alone", lin, err)
	}

	lin, err = BuildLineage(ctx, RawLayers(src), "/bad/c/", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lin.Merge(); err == nil || !strings.Contains(err.Error(), "could not parse parent chamber /bad/") {
		t.Errorf("Merge below a broken parent: %v, want it named", err)
	}

	// reads go farthest first, so a failed read reports the farthest
	src.failing = map[string]error{"/": errors.New("root"), "/x/": errors.New("x")}
	if _, err := BuildLineage(ctx, RawLayers(src), "/x/y/", true); err == nil || err.Error() != "root" {
		t.Errorf("failed reads: %v, want the farthest's", err)
	}
}

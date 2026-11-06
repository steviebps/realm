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

// lineageTree is a sample hierarchy: /prod/ is overridden in places, / is
// never inherited, and /staging/ does not exist.
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

func newLineageTree(overrides map[string]string) *mapStorage {
	entries := maps.Clone(lineageTree)
	maps.Copy(entries, overrides)
	return &mapStorage{entries: entries}
}

// referenceGet is the inherited read as it was before Get was built on
// Lineage: a top-down walk in which each ancestor inherits from the one above.
// The tests compare Get with it byte for byte.
func referenceGet(ctx context.Context, src Storage, logicalPath string) ([]byte, error) {
	leafEntry, err := src.Get(ctx, logicalPath)
	if err != nil {
		return nil, err
	}
	leaf := &realm.Chamber{}
	if err := json.Unmarshal(leafEntry.Value, leaf); err != nil {
		return nil, err
	}
	if dir := path.Dir(path.Clean(logicalPath)); dir != "/" {
		cur := "/"
		c := &realm.Chamber{Rules: map[string]*realm.Flag{}}
		for _, v := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
			cur += utils.EnsureTrailingSlash(v)
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
		{"/a/", nil},
		{"/a", nil},
		{"/a/b/", []string{"/a/"}},
		{"/a/b", []string{"/a/"}},
		{"/a/b/c/", []string{"/a/", "/a/b/"}},
		{"/x/_realm/y/", []string{"/x/", "/x/_realm/"}},
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
		failing     map[string]error
		wantErr     error // from Layers itself
		wantLayers  []string
		wantSources map[string]string
		wantByKids  bool
		wantMerge   string // substring of Merge's error, when it fails
	}{
		{
			name: "three levels with overrides",
			path: "/prod/us-east/canary/",
			wantLayers: []string{
				"/prod/us-east/canary/=ok[checkout.newFlow]",
				"/prod/us-east/=ok[rate.limit region.failover search.provider]",
				"/prod/=ok[checkout.newFlow prod.audit rate.limit]",
			},
			wantSources: map[string]string{
				"checkout.newFlow": "/prod/us-east/canary/",
				"prod.audit":       "/prod/",
				"rate.limit":       "/prod/us-east/",
				"region.failover":  "/prod/us-east/",
				"search.provider":  "/prod/us-east/",
			},
			wantByKids: true,
		},
		{
			name:       "the root is not consulted",
			path:       "/prod/us-east/",
			wantLayers: []string{"/prod/us-east/=ok[rate.limit region.failover search.provider]", "/prod/=ok[checkout.newFlow prod.audit rate.limit]"},
			wantSources: map[string]string{
				"checkout.newFlow": "/prod/", "prod.audit": "/prod/", "rate.limit": "/prod/us-east/",
				"region.failover": "/prod/us-east/", "search.provider": "/prod/us-east/",
			},
			wantByKids: true,
		},
		{
			name:        "top-level chamber",
			path:        "/prod/",
			wantLayers:  []string{"/prod/=ok[checkout.newFlow prod.audit rate.limit]"},
			wantSources: map[string]string{"checkout.newFlow": "/prod/", "prod.audit": "/prod/", "rate.limit": "/prod/"},
			wantByKids:  true,
		},
		{
			name:        "root",
			path:        "/",
			wantLayers:  []string{"/=ok[checkout.newFlow search.provider ui.theme]"},
			wantSources: map[string]string{"checkout.newFlow": "/", "search.provider": "/", "ui.theme": "/"},
			wantByKids:  false,
		},
		{
			name:        "missing parent",
			path:        "/staging/eu/",
			wantLayers:  []string{"/staging/eu/=ok[ui.theme]", "/staging/=missing"},
			wantSources: map[string]string{"ui.theme": "/staging/eu/"},
			wantByKids:  true,
		},
		{
			name:        "empty chamber under a missing parent",
			path:        "/empty/child/",
			wantLayers:  []string{"/empty/child/=ok[]", "/empty/=missing"},
			wantSources: map[string]string{},
			wantByKids:  true,
		},
		{
			name:        "nearest definition wins",
			path:        "/a/b/c/d/",
			wantLayers:  []string{"/a/b/c/d/=ok[]", "/a/b/c/=ok[k]", "/a/b/=ok[k]", "/a/=ok[k onlyA]"},
			wantSources: map[string]string{"k": "/a/b/c/", "onlyA": "/a/"},
			wantByKids:  true,
		},
		{
			name:        "slashless path",
			path:        "/a/b",
			wantLayers:  []string{"/a/b/=ok[slashless]", "/a/=ok[k onlyA]"},
			wantSources: map[string]string{"slashless": "/a/b/", "k": "/a/", "onlyA": "/a/"},
			wantByKids:  true,
		},
		{
			name:       "unparsable parent",
			path:       "/prod/us-east/canary/",
			overrides:  map[string]string{"/prod/": `{"rules":`},
			wantLayers: []string{"/prod/us-east/canary/=ok[checkout.newFlow]", "/prod/us-east/=ok[rate.limit region.failover search.provider]", "/prod/=invalid"},
			wantByKids: true,
			wantMerge:  "could not parse parent chamber /prod/",
		},
		{
			name:       "the farthest unparsable parent is reported",
			path:       "/a/b/c/d/",
			overrides:  map[string]string{"/a/": `{"rules":`, "/a/b/c/": `{"rules":`},
			wantLayers: []string{"/a/b/c/d/=ok[]", "/a/b/c/=invalid", "/a/b/=ok[k]", "/a/=invalid"},
			wantByKids: true,
			wantMerge:  "could not parse parent chamber /a/:",
		},
		{
			name:       "unparsable chamber",
			path:       "/prod/us-east/",
			overrides:  map[string]string{"/prod/us-east/": `nope`},
			wantLayers: []string{"/prod/us-east/=invalid", "/prod/=ok[checkout.newFlow prod.audit rate.limit]"},
			wantByKids: true,
			wantMerge:  "invalid character",
		},
		{
			name:    "failing parent read",
			path:    "/prod/us-east/canary/",
			failing: map[string]error{"/prod/": boom},
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
			src := newLineageTree(tt.overrides)
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
			if lin.InheritedByChildren != tt.wantByKids {
				t.Errorf("InheritedByChildren = %v, want %v", lin.InheritedByChildren, tt.wantByKids)
			}
			var got []string
			for _, l := range lin.Layers {
				got = append(got, layerSummary(l))
			}
			if !slices.Equal(got, tt.wantLayers) {
				t.Errorf("layers =\n  %v\nwant\n  %v", got, tt.wantLayers)
			}

			sources, err := lin.Sources()
			if tt.wantMerge != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantMerge) {
					t.Fatalf("Sources error = %v, want one containing %q", err, tt.wantMerge)
				}
				if _, mergeErr := lin.Merge(); mergeErr == nil || mergeErr.Error() != err.Error() {
					t.Errorf("Merge error = %v, want %v", mergeErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Sources: %v", err)
			}
			if sources == nil || !maps.Equal(sources, tt.wantSources) {
				t.Errorf("sources = %v, want %v", sources, tt.wantSources)
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
		failing   map[string]error
		want      string // the merged chamber's rules, when the read succeeds
	}{
		{name: "canary", path: "/prod/us-east/canary/", want: `{"checkout.newFlow":{"type":"boolean","value":true},"prod.audit":{"type":"boolean","value":true},"rate.limit":{"type":"number","value":1000},"region.failover":{"type":"boolean","value":true},"search.provider":{"type":"string","value":"opensearch"}}`},
		{name: "us-east", path: "/prod/us-east/"},
		{name: "prod", path: "/prod/"},
		{name: "root", path: "/"},
		{name: "missing parent", path: "/staging/eu/"},
		{name: "empty", path: "/empty/child/", want: `{}`},
		{name: "nearest wins", path: "/a/b/c/d/", want: `{"k":{"type":"number","value":3},"onlyA":{"type":"number","value":1}}`},
		{name: "slashless", path: "/a/b"},
		{name: "unparsable parent", path: "/prod/us-east/canary/", overrides: map[string]string{"/prod/": `{"rules":`}},
		{name: "two unparsable parents", path: "/a/b/c/d/", overrides: map[string]string{"/a/": `{"rules":`, "/a/b/c/": `[]`}},
		{name: "unparsable chamber", path: "/prod/us-east/", overrides: map[string]string{"/prod/us-east/": `nope`}},
		{name: "failing parent", path: "/prod/us-east/canary/", failing: map[string]error{"/prod/": boom}},
		{name: "missing chamber", path: "/nope/"},
		{name: "invalid path", path: "/prod/../prod/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			src := newLineageTree(tt.overrides)
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
	if _, err := lin.Sources(); err != nil {
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
	if plain.Inheritable || plain.InheritedByChildren || len(plain.Layers) != 1 {
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
	if !layered.Inheritable || len(layered.Layers) != 3 {
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

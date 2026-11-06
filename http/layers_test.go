package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
)

// layersTree is a sample hierarchy written through the API: / is never
// inherited, /prod/ is partly overridden below it, and /staging/ is missing.
var layersTree = []struct{ path, body string }{
	{"/", `{"rules":{"checkout.newFlow":{"type":"boolean","value":false},"ui.theme":{"type":"string","value":"classic"}}}`},
	{"/prod/", `{"rules":{"checkout.newFlow":{"type":"boolean","value":false,"targeting":[{"value":true,"rollout":{"percentage":25}}]},"rate.limit":{"type":"number","value":500},"prod.audit":{"type":"boolean","value":true}}}`},
	{"/prod/us-east/", `{"rules":{"rate.limit":{"type":"number","value":1000},"region.failover":{"type":"boolean","value":true}}}`},
	{"/prod/us-east/canary/", `{"rules":{"checkout.newFlow":{"type":"boolean","value":true}}}`},
	{"/staging/eu/", `{"rules":{"ui.theme":{"type":"string","value":"dark"}}}`},
	{"/empty/child/", `{"rules":{}}`},
}

// seedLayersTree writes layersTree through client (nil for http.DefaultClient).
func seedLayersTree(t *testing.T, client *http.Client, base string) {
	t.Helper()
	for _, c := range layersTree {
		if status, out := call(t, client, http.MethodPost, base+"/v1/chambers"+c.path, c.body, ""); status != http.StatusCreated {
			t.Fatalf("seed %s: %d %v", c.path, status, out.Errors)
		}
	}
}

// getLayers fetches path's layers, returning the status, the decoded view
// and the raw (compacted) data.
func getLayers(t *testing.T, client *http.Client, url, token string) (int, layersResponse, string) {
	t.Helper()
	status, out := call(t, client, http.MethodGet, url, "", token)
	var resp layersResponse
	if status == http.StatusOK {
		if err := json.Unmarshal(out.Data, &resp); err != nil {
			t.Fatalf("decode %s: %v", out.Data, err)
		}
	}
	return status, resp, string(out.Data)
}

// summarize describes each layer as "path=status{keys}".
func summarize(layers []layerView) []string {
	var out []string
	for _, l := range layers {
		out = append(out, fmt.Sprintf("%s=%s%v", l.Path, l.Status, slices.Sorted(maps.Keys(l.Rules))))
	}
	return out
}

func TestLayersView(t *testing.T) {
	flag := func(v float64) *realm.Flag {
		return &realm.Flag{Rule: &realm.Rule{Type: "number", Value: v}}
	}
	parseErr := errors.New("could not parse parent chamber /a/: unexpected end of JSON input")
	tests := []struct {
		name        string
		lin         storage.Lineage
		wantLayers  []string
		wantSources string
		wantError   string
	}{
		{
			name: "ok and missing",
			lin: storage.Lineage{Inheritable: true, InheritedByChildren: true, Layers: []storage.Layer{
				{Path: "/a/b/c/", Chamber: &realm.Chamber{Rules: map[string]*realm.Flag{"x": flag(1)}}},
				{Path: "/a/b/"},
				{Path: "/a/", Chamber: &realm.Chamber{Rules: map[string]*realm.Flag{"x": flag(2), "y": flag(3)}}},
			}},
			wantLayers:  []string{"/a/b/c/=ok[x]", "/a/b/=missing[]", "/a/=ok[x y]"},
			wantSources: `{"x":"/a/b/c/","y":"/a/"}`,
		},
		{
			name: "broken view",
			lin: storage.Lineage{Inheritable: true, InheritedByChildren: true, Layers: []storage.Layer{
				{Path: "/a/b/", Chamber: &realm.Chamber{Rules: map[string]*realm.Flag{}}},
				{Path: "/a/", Err: parseErr},
			}},
			wantLayers:  []string{"/a/b/=ok[]", "/a/=invalid[]"},
			wantSources: `null`,
			wantError:   parseErr.Error(),
		},
		{
			name: "empty view",
			lin: storage.Lineage{Inheritable: true, InheritedByChildren: true, Layers: []storage.Layer{
				{Path: "/a/b/", Chamber: &realm.Chamber{Rules: map[string]*realm.Flag{}}},
				{Path: "/a/"},
			}},
			wantLayers:  []string{"/a/b/=ok[]", "/a/=missing[]"},
			wantSources: `{}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := layersView(&tt.lin)
			if got := summarize(resp.Layers); !slices.Equal(got, tt.wantLayers) {
				t.Errorf("layers = %v, want %v", got, tt.wantLayers)
			}
			if resp.Error != tt.wantError {
				t.Errorf("error = %q, want %q", resp.Error, tt.wantError)
			}
			for _, l := range resp.Layers {
				if (l.Status == layerInvalid) != (l.Error != "") {
					t.Errorf("layer %s: status %s with error %q", l.Path, l.Status, l.Error)
				}
			}
			sources, err := json.Marshal(resp.Sources)
			if err != nil {
				t.Fatal(err)
			}
			if string(sources) != tt.wantSources {
				t.Errorf("sources = %s, want %s", sources, tt.wantSources)
			}
		})
	}
}

func TestLayersEndpoint(t *testing.T) {
	srv := newInheritableTestServer(t)
	seedLayersTree(t, nil, srv.URL)
	base := srv.URL + "/v1/chambers"

	tests := []struct {
		name        string
		path        string
		want        int
		wantLayers  []string
		wantSources map[string]string
		wantByKids  bool
		wantRaw     string // a substring of the compacted data
	}{
		{
			name: "parents nearest first, the root not consulted",
			path: "/prod/us-east/canary/?inherit=layers",
			want: http.StatusOK,
			wantLayers: []string{
				"/prod/us-east/canary/=ok[checkout.newFlow]",
				"/prod/us-east/=ok[rate.limit region.failover]",
				"/prod/=ok[checkout.newFlow prod.audit rate.limit]",
			},
			wantSources: map[string]string{
				"checkout.newFlow": "/prod/us-east/canary/", "prod.audit": "/prod/",
				"rate.limit": "/prod/us-east/", "region.failover": "/prod/us-east/",
			},
			wantByKids: true,
		},
		{
			name:        "missing parent",
			path:        "/staging/eu/?inherit=layers",
			want:        http.StatusOK,
			wantLayers:  []string{"/staging/eu/=ok[ui.theme]", "/staging/=missing[]"},
			wantSources: map[string]string{"ui.theme": "/staging/eu/"},
			wantByKids:  true,
		},
		{
			name:        "empty chamber reports empty sources",
			path:        "/empty/child/?inherit=layers",
			want:        http.StatusOK,
			wantLayers:  []string{"/empty/child/=ok[]", "/empty/=missing[]"},
			wantSources: map[string]string{},
			wantByKids:  true,
			wantRaw:     `"sources":{}`,
		},
		{
			name:        "root",
			path:        "/?inherit=layers",
			want:        http.StatusOK,
			wantLayers:  []string{"/=ok[checkout.newFlow ui.theme]"},
			wantSources: map[string]string{"checkout.newFlow": "/", "ui.theme": "/"},
			wantByKids:  false,
		},
		{name: "missing chamber", path: "/nope/?inherit=layers", want: http.StatusNotFound},
		{name: "reserved path", path: "/_realm/keys/?inherit=layers", want: http.StatusBadRequest},
		{name: "with watch", path: "/prod/?inherit=layers&watch=true", want: http.StatusBadRequest},
		{name: "parent references", path: "/prod/%2E%2E/prod/?inherit=layers", want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp, raw := getLayers(t, nil, base+tt.path, "")
			if status != tt.want {
				t.Fatalf("status = %d, want %d (%s)", status, tt.want, raw)
			}
			if status != http.StatusOK {
				return
			}
			if !resp.Inheritable || resp.InheritedByChildren != tt.wantByKids || resp.Error != "" {
				t.Errorf("inheritable %v, inheritedByChildren %v, error %q", resp.Inheritable, resp.InheritedByChildren, resp.Error)
			}
			if got := summarize(resp.Layers); !slices.Equal(got, tt.wantLayers) {
				t.Errorf("layers = %v, want %v", got, tt.wantLayers)
			}
			if !maps.Equal(resp.Sources, tt.wantSources) || resp.Sources == nil {
				t.Errorf("sources = %v, want %v", resp.Sources, tt.wantSources)
			}
			if !strings.Contains(raw, tt.wantRaw) {
				t.Errorf("data %s does not contain %s", raw, tt.wantRaw)
			}
		})
	}

	// the other views of a chamber are unchanged
	resp, out := do(t, http.MethodGet, base+"/prod/us-east/canary/", "", nil)
	var merged realm.Chamber
	if resp.StatusCode != http.StatusOK || json.Unmarshal(out.Data, &merged) != nil {
		t.Fatalf("merged GET: %d %s", resp.StatusCode, out.Data)
	}
	if got := slices.Sorted(maps.Keys(merged.Rules)); !slices.Equal(got, []string{"checkout.newFlow", "prod.audit", "rate.limit", "region.failover"}) {
		t.Errorf("merged keys = %v", got)
	}
	resp, out = do(t, http.MethodGet, base+"/prod/us-east/canary/?inherit=false", "", nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" || !strings.Contains(string(out.Data), "checkout.newFlow") || strings.Contains(string(out.Data), "prod.audit") {
		t.Errorf("own GET: %d etag %q %s", resp.StatusCode, resp.Header.Get("ETag"), out.Data)
	}
	if resp, _ := do(t, http.MethodGet, base+"/prod/us-east/canary/?inherit=layers", "", nil); resp.Header.Get("ETag") != "" {
		t.Error("the layers view carries an ETag")
	}
}

func TestLayersWithoutInheritance(t *testing.T) {
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: stg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	seedLayersTree(t, nil, srv.URL)

	status, resp, raw := getLayers(t, nil, srv.URL+"/v1/chambers/prod/us-east/canary/?inherit=layers", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s)", status, raw)
	}
	if resp.Inheritable || resp.InheritedByChildren {
		t.Errorf("inheritable %v, inheritedByChildren %v, want false", resp.Inheritable, resp.InheritedByChildren)
	}
	if got := summarize(resp.Layers); !slices.Equal(got, []string{"/prod/us-east/canary/=ok[checkout.newFlow]"}) {
		t.Errorf("layers = %v", got)
	}
	if !maps.Equal(resp.Sources, map[string]string{"checkout.newFlow": "/prod/us-east/canary/"}) {
		t.Errorf("sources = %v", resp.Sources)
	}
}

func TestLayersReportUnparsableParent(t *testing.T) {
	ctx := context.Background()
	stg, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	srv := newInheritableTestServerOn(t, stg)
	if status, _ := call(t, nil, http.MethodPost, srv.URL+"/v1/chambers/a/b/", `{"rules":{}}`, ""); status != http.StatusCreated {
		t.Fatalf("create /a/b/: %d", status)
	}
	// written beneath the API, which would reject the removed field
	if err := stg.Put(ctx, storage.StorageEntry{Key: "/a/", Value: []byte(`{"rules":{"kill":{"type":"boolean","value":true,"overrides":[]}}}`)}); err != nil {
		t.Fatal(err)
	}

	status, resp, raw := getLayers(t, nil, srv.URL+"/v1/chambers/a/b/?inherit=layers", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s)", status, raw)
	}
	if got := summarize(resp.Layers); !slices.Equal(got, []string{"/a/b/=ok[]", "/a/=invalid[]"}) {
		t.Errorf("layers = %v", got)
	}
	if resp.Sources != nil || !strings.Contains(raw, `"sources":null`) {
		t.Errorf("sources = %v, want null", resp.Sources)
	}
	if !strings.Contains(resp.Layers[1].Error, "could not parse parent chamber /a/") {
		t.Errorf("layer error = %q", resp.Layers[1].Error)
	}

	// the view's error is exactly what the inherited GET fails with
	_, merged := do(t, http.MethodGet, srv.URL+"/v1/chambers/a/b/", "", nil)
	if len(merged.Errors) != 1 || merged.Errors[0] != resp.Error {
		t.Errorf("error = %q, inherited GET errors = %v", resp.Error, merged.Errors)
	}
}

func TestLayersRespectKeyScope(t *testing.T) {
	srv := newAuthTestServer(t)
	admin := srv.signIn(t, "admin@example.com")
	viewer := srv.signIn(t, "viewer@example.com")
	seedLayersTree(t, admin, srv.URL)

	mint := func(access, path string) string {
		t.Helper()
		body := fmt.Sprintf(`{"name":"k","access":%q,"path":%q}`, access, path)
		status, out := call(t, admin, http.MethodPost, srv.URL+"/v1/keys", body, "")
		var created createdKey
		if status != http.StatusCreated || json.Unmarshal(out.Data, &created) != nil {
			t.Fatalf("create key %s: %d %v", body, status, out.Errors)
		}
		return created.Token
	}
	prodRead := mint("read", "/prod/")
	canaryRead := mint("read", "/prod/us-east/canary/")
	prodEvaluate := mint("evaluate", "/prod/")

	tests := []struct {
		name   string
		client *http.Client
		token  string
		path   string
		want   int
	}{
		{"key above every parent", nil, prodRead, "/prod/us-east/canary/?inherit=layers", http.StatusOK},
		{"key at a chamber with no parents", nil, prodRead, "/prod/?inherit=layers", http.StatusOK},
		{"key beneath a parent", nil, canaryRead, "/prod/us-east/canary/?inherit=layers", http.StatusForbidden},
		{"key beneath a parent keeps the inherited view", nil, canaryRead, "/prod/us-east/canary/", http.StatusOK},
		{"evaluate key", nil, prodEvaluate, "/prod/us-east/canary/?inherit=layers", http.StatusForbidden},
		{"viewer session", viewer, "", "/prod/us-east/canary/?inherit=layers", http.StatusOK},
		{"no credentials", nil, "", "/prod/us-east/canary/?inherit=layers", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp, raw := getLayers(t, tt.client, srv.URL+"/v1/chambers"+tt.path, tt.token)
			if status != tt.want {
				t.Fatalf("status = %d, want %d (%s)", status, tt.want, raw)
			}
			if status == http.StatusOK && strings.Contains(tt.path, "inherit=layers") {
				for _, l := range resp.Layers {
					if l.Status != layerOK {
						t.Errorf("layer %s is %s", l.Path, l.Status)
					}
				}
			}
		})
	}

	// a refused layers request says nothing about what is stored
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/chambers/prod/us-east/canary/?inherit=layers", nil)
	req.Header.Set("Authorization", "Bearer "+canaryRead)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), "rate.limit") || strings.Contains(string(b), "prod.audit") {
		t.Errorf("refusal leaks flags: %s", b)
	}
}

package http

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	realm "github.com/steviebps/realm/pkg"
)

// TestSealedFlags drives seals through the API: a parent's sealed flag is
// served below it whatever the chambers there define, writes that add or
// change a sealed flag below it are refused, and definitions stored before
// the seal stay, hidden, until they are removed.
func TestSealedFlags(t *testing.T) {
	srv := newInheritableTestServer(t)
	base := srv.URL + "/v1/chambers"

	const (
		open     = `{"type":"boolean","value":false}`
		sealed   = `{"type":"boolean","value":false,"sealed":true}`
		childDef = `{"type":"boolean","value":true}`
		other    = `{"type":"number","value":1}`
	)
	steps := []struct {
		name       string
		method     string
		path       string
		body       string
		want       int
		wantErrors []string
	}{
		{name: "the parent, unsealed", method: http.MethodPost, path: "/", body: `{"rules":{"entitlement":` + open + `,"limit":` + other + `}}`, want: http.StatusCreated},
		{name: "a child overrides it", method: http.MethodPost, path: "/t/a/", body: `{"rules":{"entitlement":` + childDef + `}}`, want: http.StatusCreated},
		{name: "sealing it while a child defines it", method: http.MethodPost, path: "/", body: `{"rules":{"entitlement":` + sealed + `,"limit":` + other + `}}`, want: http.StatusCreated},
		{name: "a new definition below it", method: http.MethodPost, path: "/t/b/", body: `{"rules":{"entitlement":` + childDef + `}}`, want: http.StatusBadRequest, wantErrors: []string{`flag "entitlement" is sealed by /: chambers below it cannot define it`}},
		{name: "a definition below it, even the same", method: http.MethodPost, path: "/t/", body: `{"rules":{"entitlement":` + sealed + `}}`, want: http.StatusBadRequest, wantErrors: []string{`flag "entitlement" is sealed by /: chambers below it cannot define it`}},
		{name: "an unrelated write below it", method: http.MethodPost, path: "/t/b/", body: `{"rules":{"limit":` + other + `}}`, want: http.StatusCreated},
		{name: "keeping a definition stored before the seal", method: http.MethodPost, path: "/t/a/", body: `{"rules":{"entitlement":` + childDef + `,"x":` + other + `}}`, want: http.StatusCreated},
		{name: "changing a definition stored before the seal", method: http.MethodPost, path: "/t/a/", body: `{"rules":{"entitlement":` + open + `,"x":` + other + `}}`, want: http.StatusBadRequest, wantErrors: []string{`flag "entitlement" is sealed by /: chambers below it cannot define it`}},
		{name: "patching another flag beside it", method: http.MethodPatch, path: "/t/a/", body: `{"rules":{"y":` + other + `}}`, want: http.StatusNoContent},
		{name: "patching it unchanged", method: http.MethodPatch, path: "/t/a/", body: `{"rules":{"entitlement":` + childDef + `}}`, want: http.StatusNoContent},
		{name: "patching it", method: http.MethodPatch, path: "/t/a/", body: `{"rules":{"entitlement":` + sealed + `}}`, want: http.StatusBadRequest, wantErrors: []string{`flag "entitlement" is sealed by /: chambers below it cannot define it`}},
		{name: "a seal below the root", method: http.MethodPost, path: "/t/", body: `{"rules":{"limit":{"type":"number","value":5,"sealed":true}}}`, want: http.StatusCreated},
		{name: "every refused flag is named", method: http.MethodPost, path: "/t/c/", body: `{"rules":{"entitlement":` + childDef + `,"limit":` + other + `}}`, want: http.StatusBadRequest, wantErrors: []string{`flags are sealed by parent chambers, which chambers below them cannot define: "entitlement" (by /), "limit" (by /t/)`}},
		{name: "a parent seals for every chamber below it", method: http.MethodPost, path: "/t/c/d/", body: `{"rules":{"limit":` + other + `}}`, want: http.StatusBadRequest, wantErrors: []string{`flag "limit" is sealed by /t/: chambers below it cannot define it`}},
	}
	for _, s := range steps {
		status, out := call(t, nil, s.method, base+s.path, s.body, "")
		if status != s.want {
			t.Fatalf("%s: %s %s = %d %v, want %d", s.name, s.method, s.path, status, out.Errors, s.want)
		}
		if s.wantErrors != nil && !slices.Equal(out.Errors, s.wantErrors) {
			t.Errorf("%s: errors = %q, want %q", s.name, out.Errors, s.wantErrors)
		}
	}

	// the seal is served, not the definitions it hides
	chamberAt := func(path string) realm.Chamber {
		t.Helper()
		status, out := call(t, nil, http.MethodGet, base+path, "", "")
		var c realm.Chamber
		if status != http.StatusOK || json.Unmarshal(out.Data, &c) != nil {
			t.Fatalf("GET %s: %d %s", path, status, out.Data)
		}
		return c
	}
	got := chamberAt("/t/a/")
	if f := got.Rules["entitlement"]; f == nil || f.Value != false || !f.Sealed {
		t.Errorf("/t/a/ serves entitlement %+v, want the root's sealed definition", f)
	}
	if f := got.Rules["limit"]; f == nil || f.Value != float64(5) {
		t.Errorf("/t/a/ serves limit %+v, want /t/'s", f)
	}
	// ?inherit=false still shows what is stored
	status, out := call(t, nil, http.MethodGet, base+"/t/a/?inherit=false", "", "")
	if status != http.StatusOK || !strings.Contains(string(out.Data), `"entitlement":{"type":"boolean","value":true}`) {
		t.Errorf("own view: %d %s", status, out.Data)
	}
	// evaluation serves the seal too
	status, out = call(t, nil, http.MethodPost, srv.URL+"/v1/evaluate/t/a/", `{"key":"k"}`, "")
	if status != http.StatusOK || !strings.Contains(string(out.Data), `"entitlement":{"type":"boolean","value":false}`) {
		t.Errorf("evaluate: %d %s", status, out.Data)
	}

	// the layers view names the seals and what they hide
	status, resp, raw := getLayers(t, nil, base+"/t/a/?inherit=layers", "")
	if status != http.StatusOK {
		t.Fatalf("layers: %d %s", status, raw)
	}
	if want := map[string]string{"entitlement": "/", "limit": "/t/"}; !maps.Equal(resp.SealedBy, want) {
		t.Errorf("sealedBy = %v, want %v", resp.SealedBy, want)
	}
	if want := map[string][]string{"entitlement": {"/t/a/"}}; !maps.EqualFunc(resp.Hidden, want, slices.Equal) {
		t.Errorf("hidden = %v, want %v", resp.Hidden, want)
	}
	if resp.Sources["entitlement"] != "/" || resp.Sources["limit"] != "/t/" {
		t.Errorf("sources = %v", resp.Sources)
	}
	// a view with nothing sealed or hidden omits both
	if _, _, raw := getLayers(t, nil, base+"/?inherit=layers", ""); strings.Contains(raw, "sealedBy") || strings.Contains(raw, "hidden") {
		t.Errorf("root layers: %s", raw)
	}

	// once removed, a hidden definition cannot come back; once unsealed, it can
	for _, s := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/t/a/", `{"rules":{"x":` + other + `}}`, http.StatusCreated},
		{http.MethodPatch, "/t/a/", `{"rules":{"entitlement":` + childDef + `}}`, http.StatusBadRequest},
		{http.MethodPatch, "/", `{"rules":{"entitlement":` + open + `}}`, http.StatusNoContent},
		{http.MethodPatch, "/t/a/", `{"rules":{"entitlement":` + childDef + `}}`, http.StatusNoContent},
	} {
		if status, out := call(t, nil, s.method, base+s.path, s.body, ""); status != s.want {
			t.Fatalf("%s %s %s = %d %v, want %d", s.method, s.path, s.body, status, out.Errors, s.want)
		}
	}
	if f := chamberAt("/t/a/").Rules["entitlement"]; f == nil || f.Value != true {
		t.Errorf("after unsealing, /t/a/ serves entitlement %+v, want its own", f)
	}
}

// TestSealedFlagsWithoutInheritance verifies that a seal is stored but has no
// effect when the server does not apply inheritance.
func TestSealedFlagsWithoutInheritance(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t))
	t.Cleanup(srv.Close)
	base := srv.URL + "/v1/chambers"

	if status, out := call(t, nil, http.MethodPost, base+"/", `{"rules":{"e":{"type":"boolean","value":false,"sealed":true}}}`, ""); status != http.StatusCreated {
		t.Fatalf("PUT /: %d %v", status, out.Errors)
	}
	if status, out := call(t, nil, http.MethodPost, base+"/t/", `{"rules":{"e":{"type":"boolean","value":true}}}`, ""); status != http.StatusCreated {
		t.Fatalf("PUT /t/: %d %v", status, out.Errors)
	}
	status, out := call(t, nil, http.MethodGet, base+"/", "", "")
	if status != http.StatusOK || !strings.Contains(string(out.Data), `"sealed":true`) {
		t.Errorf("GET /: %d %s", status, out.Data)
	}
}

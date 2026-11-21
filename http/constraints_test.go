package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
)

// TestConstraintsBindChambersBelow drives constraints through the API, on
// every kind of storage (with and without range scans): a parent's
// constraints bind the definitions of its flag below it, which may tighten
// them but never widen them or change the flag's type, and a parent cannot
// add or tighten constraints that a definition below breaks, unless a seal
// hides it.
func TestConstraintsBindChambersBelow(t *testing.T) {
	for name, newStorage := range deleteBackends {
		t.Run(name, func(t *testing.T) {
			srv := newInheritableTestServerOn(t, newStorage(t))
			base := srv.URL + "/v1/chambers"

			hold := func(value int, extra string) string {
				return fmt.Sprintf(`{"rules":{"hold":{"type":"number","value":%d%s}}}`, value, extra)
			}
			const bounds = `,"constraints":{"minimum":1,"maximum":72}`
			steps := []struct {
				name       string
				method     string
				path       string
				body       string
				want       int
				wantErrors []string
			}{
				{name: "the parent constrains the flag", method: http.MethodPost, path: "/", body: hold(24, bounds), want: http.StatusCreated},
				{name: "a child within them", method: http.MethodPost, path: "/t/a/", body: hold(48, ""), want: http.StatusCreated},
				{name: "a child outside them", method: http.MethodPost, path: "/t/b/", body: hold(7200, ""), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" must satisfy its constraints in /: default value: 7200 is greater than the maximum, 72`}},
				{name: "a child's rule outside them", method: http.MethodPost, path: "/t/b/", body: hold(24, `,"targeting":[{"id":"long","value":96}]`), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" must satisfy its constraints in /: targeting rule long: 96 is greater than the maximum, 72`}},
				{name: "a child's paused rule outside them", method: http.MethodPost, path: "/t/b/", body: hold(24, `,"pausedTargeting":[{"value":0}]`), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" must satisfy its constraints in /: paused targeting rule #1: 0 is less than the minimum, 1`}},
				{name: "a child widening them", method: http.MethodPost, path: "/t/b/", body: hold(96, `,"constraints":{"maximum":100}`), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" must satisfy its constraints in /: default value: 96 is greater than the maximum, 72`}},
				{name: "a child changing the type", method: http.MethodPost, path: "/t/b/", body: `{"rules":{"hold":{"type":"string","value":"24"}}}`, want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" is a number flag in /, whose constraints bind the chambers below it, so it cannot be a string flag here`}},
				{name: "a child tightening them", method: http.MethodPost, path: "/t/b/", body: hold(24, `,"constraints":{"maximum":48}`), want: http.StatusCreated},
				{name: "the tighter bound binds below it", method: http.MethodPost, path: "/t/b/c/", body: hold(60, ""), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" must satisfy its constraints in /t/b/: default value: 60 is greater than the maximum, 48`}},
				{name: "a patch beside it", method: http.MethodPatch, path: "/t/a/", body: `{"rules":{"other":{"type":"boolean","value":true}}}`, want: http.StatusNoContent},
				{name: "a patch of it", method: http.MethodPatch, path: "/t/a/", body: hold(100, ""), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" must satisfy its constraints in /: default value: 100 is greater than the maximum, 72`}},
				{name: "tightening the parent's past a child", method: http.MethodPost, path: "/", body: hold(24, `,"constraints":{"minimum":1,"maximum":36}`), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold": chambers below define it outside its new constraints: /t/a/ (default value: 48 is greater than the maximum, 36)`}},
				{name: "changing the parent's type past the children", method: http.MethodPost, path: "/", body: `{"rules":{"hold":{"type":"string","value":"24","constraints":{"maxLength":3}}}}`, want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold": chambers below define it outside its new constraints: /t/a/ (a number flag), /t/b/ (a number flag)`}},
				{name: "a patch of the parent's is checked too", method: http.MethodPatch, path: "/", body: hold(24, `,"constraints":{"maximum":40}`), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold": chambers below define it outside its new constraints: /t/a/ (default value: 48 is greater than the maximum, 40)`}},
				{name: "rewriting the parent with the same constraints", method: http.MethodPost, path: "/", body: `{"rules":{"hold":{"type":"number","value":30` + bounds + `}}}`, want: http.StatusCreated},
				{name: "a definition below a seal", method: http.MethodPost, path: "/t/h/x/", body: hold(70, ""), want: http.StatusCreated},
				{name: "the seal", method: http.MethodPost, path: "/t/h/", body: hold(24, `,"sealed":true`), want: http.StatusCreated},
				{name: "tightening past a hidden definition", method: http.MethodPost, path: "/", body: hold(24, `,"constraints":{"minimum":1,"maximum":50}`), want: http.StatusCreated},
				{name: "removing the parent's constraints", method: http.MethodPost, path: "/", body: hold(24, ""), want: http.StatusCreated},
				{name: "the child's own still bind below it", method: http.MethodPost, path: "/t/b/c/", body: hold(60, ""), want: http.StatusBadRequest,
					wantErrors: []string{`flag "hold" must satisfy its constraints in /t/b/: default value: 60 is greater than the maximum, 48`}},
				{name: "and only below it", method: http.MethodPost, path: "/t/d/", body: hold(7200, ""), want: http.StatusCreated},
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
		})
	}
}

// TestConstraintsNameOffenders checks that a refused change of constraints
// names a bounded number of the definitions below that break it, in path
// order, and counts the rest.
func TestConstraintsNameOffenders(t *testing.T) {
	srv := newInheritableTestServer(t)
	base := srv.URL + "/v1/chambers"
	for i := range 12 {
		if status, out := call(t, nil, http.MethodPost, fmt.Sprintf("%s/t/n%02d/", base, i), `{"rules":{"hold":{"type":"number","value":60}}}`, ""); status != http.StatusCreated {
			t.Fatalf("child %d: %d %v", i, status, out.Errors)
		}
	}
	status, out := call(t, nil, http.MethodPost, base+"/", `{"rules":{"hold":{"type":"number","value":24,"constraints":{"maximum":50}}}}`, "")
	var listed []string
	for i := range 10 {
		listed = append(listed, fmt.Sprintf("/t/n%02d/ (default value: 60 is greater than the maximum, 50)", i))
	}
	want := `flag "hold": chambers below define it outside its new constraints: ` + strings.Join(listed, ", ") + ", and 2 more"
	if status != http.StatusBadRequest || len(out.Errors) != 1 || out.Errors[0] != want {
		t.Fatalf("= %d %q, want 400 %q", status, out.Errors, want)
	}
}

// TestConstraintsWithoutInheritance checks that without inheritance a
// chamber's constraints bind only itself.
func TestConstraintsWithoutInheritance(t *testing.T) {
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
	base := srv.URL + "/v1/chambers"

	for _, s := range []struct {
		path, body string
		want       int
	}{
		{"/", `{"rules":{"hold":{"type":"number","value":24,"constraints":{"maximum":72}}}}`, http.StatusCreated},
		{"/t/", `{"rules":{"hold":{"type":"number","value":7200}}}`, http.StatusCreated},
		{"/", `{"rules":{"hold":{"type":"number","value":24,"constraints":{"maximum":36}}}}`, http.StatusCreated},
		{"/", `{"rules":{"hold":{"type":"number","value":48,"constraints":{"maximum":36}}}}`, http.StatusBadRequest},
	} {
		if status, out := call(t, nil, http.MethodPost, base+s.path, s.body, ""); status != s.want {
			t.Fatalf("POST %s %s = %d %v, want %d", s.path, s.body, status, out.Errors, s.want)
		}
	}
}

// TestFlagMetadataRoundTrips checks that what a flag says about itself is
// stored and served as written.
func TestFlagMetadataRoundTrips(t *testing.T) {
	srv := newInheritableTestServer(t)
	base := srv.URL + "/v1/chambers"
	const flag = `{"type":"number","value":24,"description":"How long an online order is held before its stock is released","owner":"ordering","lifecycle":"permanent","constraints":{"minimum":1,"maximum":72,"multipleOf":1}}`
	if status, out := call(t, nil, http.MethodPost, base+"/", `{"rules":{"orders.holdHours":`+flag+`}}`, ""); status != http.StatusCreated {
		t.Fatalf("POST: %d %v", status, out.Errors)
	}
	for _, p := range []string{"/", "/t/a/"} {
		if p != "/" {
			if status, out := call(t, nil, http.MethodPost, base+p, `{"rules":{}}`, ""); status != http.StatusCreated {
				t.Fatalf("POST %s: %d %v", p, status, out.Errors)
			}
		}
		status, out := call(t, nil, http.MethodGet, base+p, "", "")
		var c struct {
			Rules map[string]json.RawMessage `json:"rules"`
		}
		if status != http.StatusOK || json.Unmarshal(out.Data, &c) != nil {
			t.Fatalf("GET %s: %d %s", p, status, out.Data)
		}
		if got := string(c.Rules["orders.holdHours"]); got != flag {
			t.Errorf("GET %s serves\n %s\nwant\n %s", p, got, flag)
		}
	}
	// the SDK reads it
	status, out := call(t, nil, http.MethodGet, base+"/t/a/", "", "")
	var c realm.Chamber
	if status != http.StatusOK || json.Unmarshal(out.Data, &c) != nil {
		t.Fatalf("GET: %d %s", status, out.Data)
	}
	f := c.Rules["orders.holdHours"]
	if f.Owner != "ordering" || f.Lifecycle != realm.LifecyclePermanent {
		t.Errorf("flag = %+v", f)
	}
}

// TestWriteGuardRechecksChanges checks that a write whose constraint changes
// differ, within its transaction, from those checked against the chambers
// below (because the chamber changed in between) is refused as a conflict,
// so that the client retries and the new changes are checked.
func TestWriteGuardRechecksChanges(t *testing.T) {
	parse := func(s string) *realm.Chamber {
		t.Helper()
		c, err := realm.ParseChamber([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	next := parse(`{"rules":{"hold":{"type":"number","value":24,"constraints":{"maximum":72}},"plain":{"type":"boolean","value":true}}}`)
	stored := &storage.StorageEntry{Key: "/t/", Value: []byte(`{"rules":{"hold":{"type":"number","value":24,"constraints":{"maximum":72}}}}`)}
	empty := &storage.Ancestry{Seals: map[string]string{}, Constraints: map[string][]storage.Constrained{}}

	tests := []struct {
		name   string
		guard  *writeGuard
		stored *storage.StorageEntry
		want   error
	}{
		{"checked", &writeGuard{ancestry: empty, inheritable: true, checkedBelow: []string{"hold"}}, nil, nil},
		{"unchanged since", &writeGuard{ancestry: empty, inheritable: true}, stored, nil},
		{"changed since", &writeGuard{ancestry: empty, inheritable: true}, nil, storage.ErrConflict},
		{"without inheritance", &writeGuard{ancestry: empty}, nil, nil},
		{"no guard", nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.guard.check(tt.stored, next); err != tt.want {
				t.Fatalf("check = %v, want %v", err, tt.want)
			}
		})
	}
}

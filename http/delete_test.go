package http

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/steviebps/realm/pkg/storage"
)

// deleteBackends are the storage types recursive delete is checked against;
// they name listed children differently ("a", "a/", ".").
var deleteBackends = map[string]func(t *testing.T) storage.Storage{
	"bigcache": func(t *testing.T) storage.Storage {
		s, err := storage.NewBigCacheStorage(map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	},
	"file": func(t *testing.T) storage.Storage {
		s, err := storage.NewFileStorage(map[string]string{"path": t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return s
	},
	"boltdb": func(t *testing.T) storage.Storage {
		s, err := storage.NewBoltStorage(map[string]string{"path": t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(context.Background()) })
		return s
	},
	"cacheable": func(t *testing.T) storage.Storage {
		cache, err := storage.NewBigCacheStorage(map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		src, err := storage.NewBoltStorage(map[string]string{"path": t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		s, err := storage.NewCacheableStorage(cache, src)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(context.Background()) })
		return s
	},
}

func TestRecursiveDelete(t *testing.T) {
	// /testing/gap/ holds no chamber but leads to one; /testing2/ shares a
	// name prefix with /testing/ and must survive.
	tree := []string{"testing/", "testing/child/", "testing/child/grand/", "testing/gap/leaf/", "testing2/", "other/"}

	for name, newStorage := range deleteBackends {
		t.Run(name, func(t *testing.T) {
			srv := newInheritableTestServerOn(t, newStorage(t))
			base := srv.URL + "/v1/chambers/"
			for _, p := range tree {
				if resp, out := do(t, http.MethodPost, base+p, `{"rules":{}}`, nil); resp.StatusCode != http.StatusCreated {
					t.Fatalf("create %s: %d %v", p, resp.StatusCode, out.Errors)
				}
			}
			// read through the cache so cacheable has entries to invalidate
			for _, p := range tree {
				do(t, http.MethodGet, base+p, "", nil)
			}

			resp, out := do(t, http.MethodDelete, base+"testing?recursive=true", "", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("recursive delete: %d %v", resp.StatusCode, out.Errors)
			}
			var result deleteResult
			if err := json.Unmarshal(out.Data, &result); err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := []string{"/testing/", "/testing/child/", "/testing/child/grand/", "/testing/gap/leaf/"}
			if !slices.Equal(result.Deleted, want) {
				t.Errorf("deleted = %v, want %v", result.Deleted, want)
			}

			for _, p := range tree {
				resp, _ := do(t, http.MethodGet, base+p+"?inherit=false", "", nil)
				gone := strings.HasPrefix(p, "testing/")
				if gone && resp.StatusCode != http.StatusNotFound {
					t.Errorf("GET %s after delete: %d, want 404", p, resp.StatusCode)
				}
				if !gone && resp.StatusCode != http.StatusOK {
					t.Errorf("GET %s after delete: %d, want 200", p, resp.StatusCode)
				}
			}

			_, out = do(t, http.MethodGet, base+"?list=true", "", nil)
			var names []string
			_ = json.Unmarshal(out.Data, &names)
			for i := range names {
				names[i] = strings.TrimSuffix(names[i], "/")
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"other", "testing2"}) {
				t.Errorf("root listing = %v, want [other testing2]", names)
			}

			// each deleted chamber records the deletion in its own history
			_, out = do(t, http.MethodGet, srv.URL+"/v1/history/testing/child/grand/", "", nil)
			var revs []Revision
			if err := json.Unmarshal(out.Data, &revs); err != nil || len(revs) != 2 || revs[0].Operation != DeleteOperation {
				t.Errorf("history of grand = %+v (%v), want a delete on top of the create", revs, err)
			}
		})
	}
}

// TestRecursiveDeleteChamberNamedLikeMetadata checks that only the root-level
// "_realm" is realm's metadata: a chamber of that name anywhere else is an
// ordinary one, and a recursive delete removes it with the rest.
func TestRecursiveDeleteChamberNamedLikeMetadata(t *testing.T) {
	tree := []string{"a/", "a/_realm/", "a/_realm/b/", "a/c/", "other/"}

	for name, newStorage := range deleteBackends {
		t.Run(name, func(t *testing.T) {
			srv := newInheritableTestServerOn(t, newStorage(t))
			base := srv.URL + "/v1/chambers/"
			for _, p := range tree {
				if resp, out := do(t, http.MethodPost, base+p, `{"rules":{}}`, nil); resp.StatusCode != http.StatusCreated {
					t.Fatalf("create %s: %d %v", p, resp.StatusCode, out.Errors)
				}
			}

			resp, out := do(t, http.MethodDelete, base+"a?recursive=true", "", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("recursive delete: %d %v", resp.StatusCode, out.Errors)
			}
			var result deleteResult
			if err := json.Unmarshal(out.Data, &result); err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := []string{"/a/", "/a/_realm/", "/a/_realm/b/", "/a/c/"}
			if !slices.Equal(result.Deleted, want) {
				t.Errorf("deleted = %v, want %v", result.Deleted, want)
			}

			for _, p := range tree {
				resp, _ := do(t, http.MethodGet, base+p+"?inherit=false", "", nil)
				want := http.StatusNotFound
				if p == "other/" {
					want = http.StatusOK
				}
				if resp.StatusCode != want {
					t.Errorf("GET %s after delete: %d, want %d", p, resp.StatusCode, want)
				}
			}
		})
	}
}

func TestRecursiveDeleteRequests(t *testing.T) {
	tests := []struct {
		name    string
		create  []string
		path    string
		headers map[string]string
		status  int
		remain  []string // chambers expected to survive
		deleted []string
	}{
		{
			name:   "plain delete keeps children",
			create: []string{"a/", "a/b/"},
			path:   "a/",
			status: http.StatusNoContent,
			remain: []string{"a/b/"},
		},
		{
			name:    "path without a chamber of its own",
			create:  []string{"a/b/", "a/c/"},
			path:    "a/?recursive=true",
			status:  http.StatusOK,
			deleted: []string{"/a/b/", "/a/c/"},
		},
		{
			name:   "nothing at or below the path",
			create: []string{"a/"},
			path:   "missing/?recursive=true",
			status: http.StatusNotFound,
			remain: []string{"a/"},
		},
		{
			name:   "root is refused",
			create: []string{"a/"},
			path:   "?recursive=true",
			status: http.StatusBadRequest,
			remain: []string{"a/"},
		},
		{
			name:   "invalid recursive value",
			create: []string{"a/", "a/b/"},
			path:   "a/?recursive=maybe",
			status: http.StatusBadRequest,
			remain: []string{"a/", "a/b/"},
		},
		{
			name:    "stale If-Match deletes nothing",
			create:  []string{"a/", "a/b/"},
			path:    "a/?recursive=true",
			headers: map[string]string{"If-Match": `"stale"`},
			status:  http.StatusPreconditionFailed,
			remain:  []string{"a/", "a/b/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newInheritableTestServer(t)
			base := srv.URL + "/v1/chambers/"
			for _, p := range tt.create {
				do(t, http.MethodPost, base+p, `{"rules":{}}`, nil)
			}
			resp, out := do(t, http.MethodDelete, base+tt.path, "", tt.headers)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d (%v)", resp.StatusCode, tt.status, out.Errors)
			}
			if tt.deleted != nil {
				var result deleteResult
				_ = json.Unmarshal(out.Data, &result)
				if !slices.Equal(result.Deleted, tt.deleted) {
					t.Errorf("deleted = %v, want %v", result.Deleted, tt.deleted)
				}
			}
			for _, p := range tt.remain {
				if resp, _ := do(t, http.MethodGet, base+p+"?inherit=false", "", nil); resp.StatusCode != http.StatusOK {
					t.Errorf("%s should remain, got %d", p, resp.StatusCode)
				}
			}
		})
	}
}

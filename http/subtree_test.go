package http

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/auth"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
)

// subtreeTestServer serves an inheriting handler over raw, whose config
// configure may change, and returns its URL.
func subtreeTestServer(t *testing.T, raw storage.Storage, configure func(*HandlerConfig)) string {
	t.Helper()
	inh, err := storage.NewInheritableStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	config := HandlerConfig{Storage: inh}
	if configure != nil {
		configure(&config)
	}
	h, err := NewHandler(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func newBigCache(t testing.TB) storage.Storage {
	t.Helper()
	s, err := storage.NewBigCacheStorage(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// subtreeTree is a sample hierarchy: tenants below /prod/tenants/, which
// has no chamber of its own, one with locations below it.
var subtreeTree = []struct{ path, body string }{
	{"/", `{"rules":{"theme":{"type":"string","value":"classic"}}}`},
	{"/prod/", `{"rules":{"limit":{"type":"number","value":500},"beta":{"type":"boolean","value":false}}}`},
	{"/prod/tenants/t-1/", `{"rules":{"limit":{"type":"number","value":1000}}}`},
	{"/prod/tenants/t-1/loc-1/", `{"rules":{"beta":{"type":"boolean","value":true}}}`},
	{"/prod/tenants/t-1/loc-2/", `{"rules":{}}`},
	{"/prod/tenants/t-2/", `{"rules":{"limit":{"type":"number","value":10}}}`},
	{"/prod-eu/", `{"rules":{}}`},
}

// seedSubtree writes subtreeTree through the API at base.
func seedSubtree(t *testing.T, base string) {
	t.Helper()
	for _, c := range subtreeTree {
		putChamber(t, base, strings.TrimPrefix(c.path, "/"), c.body)
	}
}

// subtreeView is an entry of a subtree read, or the data of a stream event.
type subtreeView struct {
	Path     string                 `json:"path"`
	Rules    map[string]*realm.Flag `json:"rules"`
	Revision uint64                 `json:"revision"`
	Error    string                 `json:"error"`
	Chambers int                    `json:"chambers"`
}

// describe summarizes a view as "path={flag:value ...}", or "path!error".
func (v subtreeView) describe() string {
	if v.Error != "" {
		return v.Path + "!" + v.Error
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(v.Rules)) {
		parts = append(parts, fmt.Sprintf("%s:%v", k, v.Rules[k].Value))
	}
	return v.Path + "={" + strings.Join(parts, " ") + "}"
}

// getSubtree reads the subtree at url (a chamber URL), returning the status,
// the entries, and the raw body.
func getSubtree(t *testing.T, url, token string) (*http.Response, []subtreeView, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Data struct {
			Chambers []subtreeView `json:"chambers"`
		} `json:"data"`
	}
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	return resp, out.Data.Chambers, string(body)
}

func describeAll(views []subtreeView) []string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.describe()
	}
	return out
}

func TestSubtreeRead(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)

	tests := []struct {
		name string
		root string
		want []string
	}{
		{
			name: "parents before children, gaps left out",
			root: "prod/",
			want: []string{
				"/prod/={beta:false limit:500 theme:classic}",
				"/prod/tenants/t-1/={beta:false limit:1000 theme:classic}",
				"/prod/tenants/t-1/loc-1/={beta:true limit:1000 theme:classic}",
				"/prod/tenants/t-1/loc-2/={beta:false limit:1000 theme:classic}",
				"/prod/tenants/t-2/={beta:false limit:10 theme:classic}",
			},
		},
		{
			name: "root without a chamber",
			root: "prod/tenants",
			want: []string{
				"/prod/tenants/={beta:false limit:500 theme:classic}",
				"/prod/tenants/t-1/={beta:false limit:1000 theme:classic}",
				"/prod/tenants/t-1/loc-1/={beta:true limit:1000 theme:classic}",
				"/prod/tenants/t-1/loc-2/={beta:false limit:1000 theme:classic}",
				"/prod/tenants/t-2/={beta:false limit:10 theme:classic}",
			},
		},
		{
			name: "leaf",
			root: "prod/tenants/t-2/",
			want: []string{"/prod/tenants/t-2/={beta:false limit:10 theme:classic}"},
		},
		{
			name: "nothing below",
			root: "nowhere/at/all/",
			want: []string{"/nowhere/at/all/={theme:classic}"},
		},
		{
			name: "everything, without realm's metadata",
			root: "",
			want: []string{
				"/={theme:classic}",
				"/prod-eu/={theme:classic}",
				"/prod/={beta:false limit:500 theme:classic}",
				"/prod/tenants/t-1/={beta:false limit:1000 theme:classic}",
				"/prod/tenants/t-1/loc-1/={beta:true limit:1000 theme:classic}",
				"/prod/tenants/t-1/loc-2/={beta:false limit:1000 theme:classic}",
				"/prod/tenants/t-2/={beta:false limit:10 theme:classic}",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, views, body := getSubtree(t, base+"/v1/chambers/"+tt.root+"?subtree=true", "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
			if got := describeAll(views); !slices.Equal(got, tt.want) {
				t.Errorf("entries =\n  %q\nwant\n  %q", got, tt.want)
			}
			// bigcache tracks no revisions
			if strings.Contains(body, `"revision"`) || resp.Header.Get(api.RevisionHeader) != "" {
				t.Errorf("revisions reported without a storage that tracks them: %s", body)
			}
			// each entry is what a plain read of its path returns
			for _, v := range views {
				_, out := do(t, http.MethodGet, base+"/v1/chambers"+v.Path, "", nil)
				var plain realm.Chamber
				if err := json.Unmarshal(out.Data, &plain); err != nil {
					continue // the root without a chamber: 404
				}
				if got := (subtreeView{Path: v.Path, Rules: plain.Rules}).describe(); got != v.describe() {
					t.Errorf("plain read %s, subtree entry %s", got, v.describe())
				}
			}
		})
	}
}

// TestSubtreeReadBody checks the body's exact shape.
func TestSubtreeReadBody(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), nil)
	putChamber(t, base, "a/b", `{"rules":{"f":{"type":"boolean","value":true}}}`)

	resp, _, body := getSubtree(t, base+"/v1/chambers/a?subtree=true", "")
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	want := `{"data":{"chambers":[{"path":"/a/","rules":{}},{"path":"/a/b/","rules":{"f":{"type":"boolean","value":true}}}]}}` + "\n"
	if body != want {
		t.Errorf("body =\n  %s\nwant\n  %s", body, want)
	}
}

// TestSubtreeReadRevisions verifies that each entry carries its view's
// revision, as a plain read reports it, and the response no header.
func TestSubtreeReadRevisions(t *testing.T) {
	base := newRevisionTestServer(t)
	seedSubtree(t, base)
	resp, views, body := getSubtree(t, base+"/v1/chambers/prod/tenants/?subtree=true", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if h := resp.Header.Get(api.RevisionHeader); h != "" {
		t.Errorf("%s = %q on a subtree read", api.RevisionHeader, h)
	}
	for _, v := range views[1:] {
		if want := getRevision(t, base+"/v1/chambers"+v.Path); v.Revision != want {
			t.Errorf("%s: revision %d, plain read %d", v.Path, v.Revision, want)
		}
	}
	// the root's view, built from /prod/ and /, has /prod/'s revision
	if want := getRevision(t, base+"/v1/chambers/prod/"); views[0].Revision != want {
		t.Errorf("root revision %d, want /prod/'s %d", views[0].Revision, want)
	}
}

// TestSubtreeReadUnparsableChamber verifies that a view that cannot be built
// has an error entry, and the read carries on.
func TestSubtreeReadUnparsableChamber(t *testing.T) {
	raw := newBigCache(t)
	base := subtreeTestServer(t, raw, nil)
	seedSubtree(t, base)
	if err := raw.Put(context.Background(), storage.StorageEntry{Key: "/prod/tenants/t-1/", Value: []byte(`{"rules":`)}); err != nil {
		t.Fatal(err)
	}
	_, views, body := getSubtree(t, base+"/v1/chambers/prod/?subtree=true", "")
	got := describeAll(views)
	want := []string{
		"/prod/={beta:false limit:500 theme:classic}",
		"/prod/tenants/t-1/!unexpected end of JSON input",
		"/prod/tenants/t-1/loc-1/!could not parse parent chamber /prod/tenants/t-1/: unexpected end of JSON input",
		"/prod/tenants/t-1/loc-2/!could not parse parent chamber /prod/tenants/t-1/: unexpected end of JSON input",
		"/prod/tenants/t-2/={beta:false limit:10 theme:classic}",
	}
	if !slices.Equal(got, want) {
		t.Errorf("entries =\n  %q\nwant\n  %q", got, want)
	}
	if strings.Contains(body, `"rules":null`) {
		t.Errorf("error entries carry rules: %s", body)
	}
}

func TestSubtreeLimit(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), func(c *HandlerConfig) { c.MaxSubtreeChambers = 3 })
	seedSubtree(t, base)

	tests := []struct {
		url  string
		want int
	}{
		// below /prod/tenants/: t-1, t-1/loc-1, t-1/loc-2, t-2
		{"/v1/chambers/prod/tenants/?subtree=true", http.StatusRequestEntityTooLarge},
		{"/v1/chambers/prod/tenants/?subtree=true&watch=true", http.StatusRequestEntityTooLarge},
		{"/v1/chambers/prod/tenants/t-1/?subtree=true", http.StatusOK},
	}
	for _, tt := range tests {
		resp, _, body := getSubtree(t, base+tt.url, "")
		if resp.StatusCode != tt.want {
			t.Errorf("%s: %d %s, want %d", tt.url, resp.StatusCode, body, tt.want)
		}
	}
}

func TestSubtreeRefusedCombinations(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)
	for _, query := range []string{
		"subtree=true&inherit=false",
		"subtree=true&inherit=layers",
		"subtree=true&inherit=false&watch=true",
		"subtree=true&inherit=layers&watch=true",
		"subtree=maybe",
	} {
		resp, _, body := getSubtree(t, base+"/v1/chambers/prod/?"+query, "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", query, resp.StatusCode, body)
		}
	}
	for _, path := range []string{"_realm/", "_realm/history/"} {
		if resp, _, body := getSubtree(t, base+"/v1/chambers/"+path+"?subtree=true", ""); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", path, resp.StatusCode, body)
		}
	}
	// subtree=false is an ordinary read
	if resp, _, body := getSubtree(t, base+"/v1/chambers/prod/?subtree=false&inherit=false", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("subtree=false: %d %s", resp.StatusCode, body)
	}
}

// TestSubtreeKeyScope verifies that reading a subtree needs read access to
// its root, which a key scoped above it covers.
func TestSubtreeKeyScope(t *testing.T) {
	const adminKey = "admin-0123456789abcdef0123456789abcdef"
	base := subtreeTestServer(t, newBigCache(t), func(c *HandlerConfig) { c.Auth = &auth.Config{AdminKey: adminKey} })
	for _, c := range subtreeTree {
		if status, out := call(t, nil, http.MethodPost, base+"/v1/chambers"+c.path, c.body, adminKey); status != http.StatusCreated {
			t.Fatalf("seed %s: %d %v", c.path, status, out.Errors)
		}
	}
	status, out := call(t, nil, http.MethodPost, base+"/v1/keys", `{"name":"svc","access":"read","path":"/prod/tenants/"}`, adminKey)
	var created createdKey
	if status != http.StatusCreated || json.Unmarshal(out.Data, &created) != nil {
		t.Fatalf("create key: %d %v", status, out.Errors)
	}

	tests := []struct {
		root string
		want int
		n    int
	}{
		{"prod/tenants/", http.StatusOK, 5},
		{"prod/tenants/t-1/", http.StatusOK, 3},
		{"prod/", http.StatusForbidden, 0},
		{"", http.StatusForbidden, 0},
	}
	for _, tt := range tests {
		for _, watch := range []bool{false, true} {
			url := base + "/v1/chambers/" + tt.root + "?subtree=true"
			if watch {
				url += "&watch=true"
				if tt.want == http.StatusOK {
					events := openSubtreeStream(t, url, created.Token)
					if got := len(untilSynced(t, events)); got != tt.n {
						t.Errorf("stream of /%s: %d chambers, want %d", tt.root, got, tt.n)
					}
					continue
				}
			}
			resp, views, body := getSubtree(t, url, created.Token)
			if resp.StatusCode != tt.want || len(views) != tt.n {
				t.Errorf("%s: %d with %d entries, want %d with %d (%s)", url, resp.StatusCode, len(views), tt.want, tt.n, body)
			}
		}
	}
}

// sseEvent is one server-sent event.
type sseEvent struct {
	name, id string
	data     subtreeView
	raw      string
}

func (e sseEvent) String() string {
	switch e.name {
	case "chamber":
		return "chamber " + e.data.describe()
	case "synced":
		return fmt.Sprintf("synced %d", e.data.Chambers)
	}
	return e.name + " " + e.data.Path
}

// openSubtreeStream opens the stream at url, with token unless it is empty,
// and returns its events.
func openSubtreeStream(t testing.TB, url, token string) <-chan sseEvent {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("open %s: %d %s", url, resp.StatusCode, body)
	}
	out := make(chan sseEvent, 1024)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, 1<<20)
		var ev sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if ev.name != "" {
					_ = json.Unmarshal([]byte(ev.raw), &ev.data)
					out <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				ev.raw += strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out
}

func nextEvent(t testing.TB, events <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("the stream ended")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
		return sseEvent{}
	}
}

// untilSynced returns the events before the next synced event, checking
// that synced counts the chamber events among them, and that those come
// first.
func untilSynced(t *testing.T, events <-chan sseEvent) []string {
	t.Helper()
	var got []string
	chambers := 0
	for {
		ev := nextEvent(t, events)
		switch ev.name {
		case "synced":
			if ev.data.Chambers != chambers {
				t.Errorf("synced counts %d chambers after %d chamber events", ev.data.Chambers, chambers)
			}
			return got
		case "chamber":
			if chambers != len(got) {
				t.Errorf("chamber event %s after other events", ev)
			}
			chambers++
		}
		got = append(got, ev.String())
	}
}

// expectEvents reads exactly want's events, then checks that no other
// arrives soon.
func expectEvents(t *testing.T, events <-chan sseEvent, want ...string) {
	t.Helper()
	var got []string
	for range want {
		got = append(got, nextEvent(t, events).String())
	}
	if !slices.Equal(got, want) {
		t.Errorf("events =\n  %q\nwant\n  %q", got, want)
	}
	select {
	case ev := <-events:
		t.Errorf("unexpected event %s", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSubtreeStream(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)
	events := openSubtreeStream(t, base+"/v1/chambers/prod/tenants/?subtree=true&watch=true", "")

	snapshot := untilSynced(t, events)
	want := []string{
		"chamber /prod/tenants/={beta:false limit:500 theme:classic}",
		"chamber /prod/tenants/t-1/={beta:false limit:1000 theme:classic}",
		"chamber /prod/tenants/t-1/loc-1/={beta:true limit:1000 theme:classic}",
		"chamber /prod/tenants/t-1/loc-2/={beta:false limit:1000 theme:classic}",
		"chamber /prod/tenants/t-2/={beta:false limit:10 theme:classic}",
	}
	if !slices.Equal(snapshot, want) {
		t.Fatalf("snapshot =\n  %q\nwant\n  %q", snapshot, want)
	}

	steps := []struct {
		name   string
		act    func(t *testing.T)
		events []string
	}{
		{
			name: "a leaf write sends that view alone",
			act: func(t *testing.T) {
				putChamber(t, base, "prod/tenants/t-1/loc-2", `{"rules":{"limit":{"type":"number","value":7}}}`)
			},
			events: []string{"chamber /prod/tenants/t-1/loc-2/={beta:false limit:7 theme:classic}"},
		},
		{
			name: "a parent write sends the views it changes",
			// every chamber below defines limit, but the root inherits it
			act: func(t *testing.T) {
				putChamber(t, base, "prod/tenants/t-1/loc-1", `{"rules":{"beta":{"type":"boolean","value":true},"limit":{"type":"number","value":1}}}`)
				// let that change be sent before the next
				time.Sleep(100 * time.Millisecond)
				putChamber(t, base, "prod", `{"rules":{"limit":{"type":"number","value":999},"beta":{"type":"boolean","value":false}}}`)
			},
			events: []string{
				"chamber /prod/tenants/t-1/loc-1/={beta:true limit:1 theme:classic}",
				"chamber /prod/tenants/={beta:false limit:999 theme:classic}",
			},
		},
		{
			name:   "a write above that changes every view",
			act:    func(t *testing.T) { putChamber(t, base, "", `{"rules":{"theme":{"type":"string","value":"dark"}}}`) },
			events: []string{"chamber /prod/tenants/={beta:false limit:999 theme:dark}", "chamber /prod/tenants/t-1/={beta:false limit:1000 theme:dark}", "chamber /prod/tenants/t-1/loc-1/={beta:true limit:1 theme:dark}", "chamber /prod/tenants/t-1/loc-2/={beta:false limit:7 theme:dark}", "chamber /prod/tenants/t-2/={beta:false limit:10 theme:dark}"},
		},
		{
			name: "a write elsewhere sends nothing",
			act: func(t *testing.T) {
				putChamber(t, base, "prod-eu", `{"rules":{"theme":{"type":"string","value":"x"}}}`)
			},
			events: nil,
		},
		{
			name:   "a new chamber is announced",
			act:    func(t *testing.T) { putChamber(t, base, "prod/tenants/t-3/loc-9", `{"rules":{}}`) },
			events: []string{"chamber /prod/tenants/t-3/loc-9/={beta:false limit:999 theme:dark}"},
		},
		{
			name: "a parent deleted",
			act: func(t *testing.T) {
				if resp, out := do(t, http.MethodDelete, base+"/v1/chambers/prod/tenants/t-1/", "", nil); resp.StatusCode != http.StatusNoContent {
					t.Fatalf("delete: %d %v", resp.StatusCode, out.Errors)
				}
			},
			// the chambers below define every flag t-1 did: their views stay
			events: []string{"deleted /prod/tenants/t-1/"},
		},
		{
			name: "a recursive delete",
			act: func(t *testing.T) {
				if resp, out := do(t, http.MethodDelete, base+"/v1/chambers/prod/tenants/t-1/?recursive=true", "", nil); resp.StatusCode != http.StatusOK {
					t.Fatalf("delete: %d %v", resp.StatusCode, out.Errors)
				}
			},
			events: []string{"deleted /prod/tenants/t-1/loc-1/", "deleted /prod/tenants/t-1/loc-2/"},
		},
		{
			name: "a root chamber created, then deleted",
			act: func(t *testing.T) {
				putChamber(t, base, "prod/tenants", `{"rules":{"limit":{"type":"number","value":3}}}`)
				time.Sleep(100 * time.Millisecond)
				if resp, out := do(t, http.MethodDelete, base+"/v1/chambers/prod/tenants/", "", nil); resp.StatusCode != http.StatusNoContent {
					t.Fatalf("delete: %d %v", resp.StatusCode, out.Errors)
				}
			},
			events: []string{
				"chamber /prod/tenants/={beta:false limit:3 theme:dark}",
				"chamber /prod/tenants/t-3/loc-9/={beta:false limit:3 theme:dark}",
				"chamber /prod/tenants/={beta:false limit:999 theme:dark}",
				"chamber /prod/tenants/t-3/loc-9/={beta:false limit:999 theme:dark}",
			},
		},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.act(t)
			expectEvents(t, events, step.events...)
		})
	}
}

// TestSubtreeStreamUnparsableChamber verifies that a view that cannot be
// built is sent with its error and without an id, in snapshots and when it
// newly fails or fails differently, and counts in synced.
func TestSubtreeStreamUnparsableChamber(t *testing.T) {
	raw := newBigCache(t)
	base := subtreeTestServer(t, raw, nil)
	seedSubtree(t, base)
	breakChamber := func(path, value string) {
		t.Helper()
		if err := raw.Put(context.Background(), storage.StorageEntry{Key: path, Value: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	breakChamber("/prod/tenants/t-1/", `nope`)
	events := openSubtreeStream(t, base+"/v1/chambers/prod/tenants/?subtree=true&watch=true", "")
	const (
		ownError    = "invalid character 'o' in literal null (expecting 'u')"
		parentError = "could not parse parent chamber /prod/tenants/t-1/: " + ownError
	)
	want := []string{
		"chamber /prod/tenants/={beta:false limit:500 theme:classic}",
		"chamber /prod/tenants/t-1/!" + ownError,
		"chamber /prod/tenants/t-1/loc-1/!" + parentError,
		"chamber /prod/tenants/t-1/loc-2/!" + parentError,
		"chamber /prod/tenants/t-2/={beta:false limit:10 theme:classic}",
	}
	var got []string
	for range want {
		ev := nextEvent(t, events)
		got = append(got, ev.String())
		if strings.Contains(ev.raw, `"rules"`) == (ev.data.Error != "") {
			t.Errorf("event data %s: want rules or an error", ev.raw)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("snapshot =\n  %q\nwant\n  %q", got, want)
	}
	if ev := nextEvent(t, events); ev.name != "synced" || ev.data.Chambers != 5 {
		t.Fatalf("event %s, want synced counting every view", ev)
	}

	// a change that leaves the errors as they were sends nothing for them
	putChamber(t, base, "prod", `{"rules":{"limit":{"type":"number","value":1}}}`)
	expectEvents(t, events, "chamber /prod/tenants/={limit:1 theme:classic}", "chamber /prod/tenants/t-2/={limit:10 theme:classic}")

	// one that changes them sends them again
	breakChamber("/prod/tenants/t-1/", `[]`)
	putChamber(t, base, "prod", `{"rules":{"limit":{"type":"number","value":2}}}`)
	const arrayError = "json: cannot unmarshal array into Go value of type realm.chamberAlias"
	expectEvents(t, events,
		"chamber /prod/tenants/={limit:2 theme:classic}",
		"chamber /prod/tenants/t-1/!"+arrayError,
		"chamber /prod/tenants/t-1/loc-1/!could not parse parent chamber /prod/tenants/t-1/: "+arrayError,
		"chamber /prod/tenants/t-1/loc-2/!could not parse parent chamber /prod/tenants/t-1/: "+arrayError,
	)

	// a view that newly fails is sent with its error
	breakChamber("/prod/tenants/t-2/", `nope`)
	putChamber(t, base, "prod", `{"rules":{"limit":{"type":"number","value":3}}}`)
	expectEvents(t, events,
		"chamber /prod/tenants/={limit:3 theme:classic}",
		"chamber /prod/tenants/t-2/!"+ownError,
	)

	// and a fixed one with its view
	putChamber(t, base, "prod/tenants/t-2", `{"rules":{}}`)
	expectEvents(t, events, "chamber /prod/tenants/t-2/={limit:3 theme:classic}")
}

// TestSubtreeStreamRevisions verifies that events carry their view's
// revision as id, and those of views that cannot be built none.
func TestSubtreeStreamRevisions(t *testing.T) {
	raw := deleteBackends["boltdb"](t)
	base := newInheritableTestServerOn(t, raw).URL
	seedSubtree(t, base)
	events := openSubtreeStream(t, base+"/v1/chambers/prod/tenants/t-1/?subtree=true&watch=true", "")
	for range 3 {
		ev := nextEvent(t, events)
		if want := fmt.Sprint(getRevision(t, base+"/v1/chambers"+ev.data.Path)); ev.id != want {
			t.Errorf("%s: id %q, want %s", ev.data.Path, ev.id, want)
		}
	}
	if ev := nextEvent(t, events); ev.name != "synced" || ev.id != "" {
		t.Errorf("event %s with id %q, want synced without one", ev, ev.id)
	}
	rev := putRevision(t, base, "prod/tenants/t-1/loc-1/", "false")
	if ev := nextEvent(t, events); ev.id != fmt.Sprint(rev) {
		t.Errorf("%s: id %q, want %d", ev, ev.id, rev)
	}

	if err := raw.Put(context.Background(), storage.StorageEntry{Key: "/prod/tenants/t-1/loc-2/", Value: []byte(`nope`)}); err != nil {
		t.Fatal(err)
	}
	putChamber(t, base, "prod/tenants/t-1", `{"rules":{}}`)
	for range 3 {
		ev := nextEvent(t, events)
		if ev.data.Error != "" && ev.id != "" {
			t.Errorf("%s: id %q, want none", ev, ev.id)
		}
		if ev.data.Error == "" && ev.id == "" {
			t.Errorf("%s: no id", ev)
		}
	}
}

// TestSubtreeStreamResync verifies that a stream that cannot tell which
// views changed sends the whole snapshot again.
func TestSubtreeStreamResync(t *testing.T) {
	old := maxDirtyPaths
	maxDirtyPaths = 2
	t.Cleanup(func() { maxDirtyPaths = old })

	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)
	events := openSubtreeStream(t, base+"/v1/chambers/prod/?subtree=true&watch=true", "")
	if got := untilSynced(t, events); len(got) != 5 {
		t.Fatalf("snapshot = %q", got)
	}

	// three chambers deleted at once overflow the stream's record
	if resp, out := do(t, http.MethodDelete, base+"/v1/chambers/prod/tenants/t-1/?recursive=true", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %v", resp.StatusCode, out.Errors)
	}
	// the whole snapshot, then what it no longer holds, as clients cannot
	// tell where a resync starts
	got := untilSynced(t, events)
	want := []string{
		"chamber /prod/={beta:false limit:500 theme:classic}",
		"chamber /prod/tenants/t-2/={beta:false limit:10 theme:classic}",
		"deleted /prod/tenants/t-1/",
		"deleted /prod/tenants/t-1/loc-1/",
		"deleted /prod/tenants/t-1/loc-2/",
	}
	if !slices.Equal(got, want) {
		t.Errorf("resync =\n  %q\nwant\n  %q", got, want)
	}
	// and carries on from there
	putChamber(t, base, "prod/tenants/t-2", `{"rules":{}}`)
	expectEvents(t, events, "chamber /prod/tenants/t-2/={beta:false limit:500 theme:classic}")
}

// TestSubtreeStreamLeavesSingleStreamsAlone verifies that single-chamber
// streams still hear only of changes at or above their chamber.
func TestSubtreeStreamLeavesSingleStreamsAlone(t *testing.T) {
	base := subtreeTestServer(t, newBigCache(t), nil)
	seedSubtree(t, base)
	resp, err := http.Get(base + "/v1/chambers/prod/?watch=true")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data := dataEvents(bufio.NewScanner(resp.Body))
	waitData(t, data, 2*time.Second)
	putChamber(t, base, "prod/tenants/t-1", `{"rules":{}}`)
	select {
	case d := <-data:
		t.Errorf("a write below the chamber was streamed: %s", d)
	case <-time.After(150 * time.Millisecond):
	}
}

// BenchmarkSubtreeStreamParentWrite measures a write to /prod/ that changes
// the view of each of its 500 descendants, while several subtree streams
// watch /prod/: each stored chamber is read about once per change, however
// many streams there are (reads/change).
func BenchmarkSubtreeStreamParentWrite(b *testing.B) {
	const (
		tenants   = 50
		locations = 9 // per tenant: 500 chambers below /prod/ in all
		streams   = 10
	)
	counting := &countingStorage{Storage: newBigCache(b)}
	inh, err := storage.NewInheritableStorage(counting)
	if err != nil {
		b.Fatal(err)
	}
	h, err := NewHandler(context.Background(), HandlerConfig{Storage: inh})
	if err != nil {
		b.Fatal(err)
	}
	srv := httptest.NewServer(h)
	b.Cleanup(srv.Close)

	putChamber(b, srv.URL, "prod", `{"rules":{"n":{"type":"number","value":0}}}`)
	for i := range tenants {
		putChamber(b, srv.URL, fmt.Sprintf("prod/t-%d", i), `{"rules":{}}`)
		for j := range locations {
			putChamber(b, srv.URL, fmt.Sprintf("prod/t-%d/l-%d", i, j), `{"rules":{}}`)
		}
	}
	const views = 1 + tenants*(1+locations)

	// await reads n chamber events from each stream.
	all := make([]<-chan sseEvent, streams)
	await := func(n int) {
		for _, events := range all {
			for range n {
				if ev := nextEvent(b, events); ev.name != "chamber" {
					b.Fatalf("unexpected event %s", ev)
				}
			}
		}
	}
	for i := range all {
		all[i] = openSubtreeStream(b, srv.URL+"/v1/chambers/prod/?subtree=true&watch=true", "")
	}
	await(views)
	for _, events := range all {
		if ev := nextEvent(b, events); ev.name != "synced" {
			b.Fatalf("unexpected event %s", ev)
		}
	}

	counting.reset()
	b.ResetTimer()
	for i := range b.N {
		putChamber(b, srv.URL, "prod", fmt.Sprintf(`{"rules":{"n":{"type":"number","value":%d}}}`, i+1))
		await(views)
	}
	b.StopTimer()

	counting.mu.Lock()
	reads := 0
	for key, n := range counting.gets {
		if !storage.IsReservedPath(key) {
			reads += n
		}
	}
	counting.mu.Unlock()
	b.ReportMetric(float64(reads)/float64(b.N), "reads/change")
	b.ReportMetric(float64(views), "views/change")
}

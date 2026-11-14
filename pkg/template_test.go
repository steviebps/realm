package realm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/client"
)

// subtreeConformance mirrors sdk/conformance/subtree.json, the path-template
// cases every realm SDK must agree on.
type subtreeConformance struct {
	Cases []struct {
		Name       string            `json:"name"`
		Template   string            `json:"template"`
		Root       string            `json:"root"`
		Paths      []string          `json:"paths"`
		Key        string            `json:"key"`
		Attributes map[string]string `json:"attributes"`
		Defaults   map[string]string `json:"defaults"`
		Expect     *string           `json:"expect"`
	} `json:"cases"`
	InvalidTemplates []string `json:"invalidTemplates"`
}

// templateRealm returns a Realm for template that never refreshes, holding a
// view (with the flag "msg" set to its path) of each of paths.
func templateRealm(t testing.TB, template string, paths []string, options ...RealmOption) *Realm {
	t.Helper()
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	rlm, err := NewRealm(append([]RealmOption{WithHttpClient(c), WithPathTemplate(template), WithLogger(zerolog.Nop())}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	if paths != nil {
		rlm.views = make(map[string]*ChamberEntry, len(paths))
		for i, p := range paths {
			c := &Chamber{Rules: map[string]*Flag{
				"msg":  {Rule: &Rule{Type: "string", Value: p}},
				"on":   {Rule: &Rule{Type: "boolean", Value: true}},
				"rate": {Rule: &Rule{Type: "number", Value: float64(i)}},
			}}
			rlm.views[p] = rlm.newView(p, c, uint64(100+i))
		}
	}
	return rlm
}

func TestSubtreeConformance(t *testing.T) {
	b, err := os.ReadFile("../sdk/conformance/subtree.json")
	if err != nil {
		t.Fatal(err)
	}
	var c subtreeConformance
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Cases) == 0 || len(c.InvalidTemplates) == 0 {
		t.Fatal("no cases")
	}

	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			tmpl, err := parsePathTemplate(tc.Template)
			if err != nil {
				t.Fatal(err)
			}
			if tmpl.root != tc.Root {
				t.Errorf("root = %q, want %q", tmpl.root, tc.Root)
			}
			want := ""
			if tc.Expect != nil {
				want = *tc.Expect
			}

			rlm := templateRealm(t, tc.Template, tc.Paths, WithAttributes(tc.Defaults))
			ec := EvaluationContext{Key: tc.Key, Attributes: tc.Attributes}
			pinned := rlm.NewContextWithEvaluation(context.Background(), ec)
			unpinned := context.WithValue(context.Background(), EvaluationContextKey, ec)
			for name, ctx := range map[string]context.Context{"pinned": pinned, "unpinned": unpinned} {
				if got := rlm.ChamberPath(ctx); got != want {
					t.Errorf("%s: ChamberPath = %q, want %q", name, got, want)
				}
				v, err := rlm.String(ctx, "msg", "default")
				switch {
				case tc.Expect == nil && (err != ErrChamberEmpty || v != "default"):
					t.Errorf("%s: msg = %q, %v; want the default and ErrChamberEmpty", name, v, err)
				case tc.Expect != nil && (err != nil || v != want):
					t.Errorf("%s: msg = %q, %v; want the view of %s", name, v, err, want)
				}
			}
		})
	}

	t.Run("invalid templates", func(t *testing.T) {
		for _, tmpl := range append(c.InvalidTemplates, "/prod/./{tenantId}/", "/prod/../{tenantId}/", "//") {
			c, _ := client.NewHttpClient(&client.HttpClientConfig{Address: "http://127.0.0.1:1"})
			if _, err := NewRealm(WithHttpClient(c), WithPathTemplate(tmpl)); err == nil {
				t.Errorf("NewRealm accepted the template %q", tmpl)
			}
		}
	})
}

func TestParsePathTemplate(t *testing.T) {
	tests := []struct {
		template string
		root     string
		segments []templateSegment
	}{
		{"/", "/", nil},
		{"/prod", "/prod/", nil},
		{"/{key}", "/", []templateSegment{{"key", true}}},
		{"/a/{b.c-d_E9}/e/{f}/", "/a/", []templateSegment{{"b.c-d_E9", true}, {"e", false}, {"f", true}}},
	}
	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			got, err := parsePathTemplate(tt.template)
			if err != nil {
				t.Fatal(err)
			}
			if got.root != tt.root || fmt.Sprint(got.segments) != fmt.Sprint(tt.segments) {
				t.Errorf("root %q, segments %v; want %q, %v", got.root, got.segments, tt.root, tt.segments)
			}
		})
	}
}

func TestNewRealmPathOptions(t *testing.T) {
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		options []RealmOption
		ok      bool
	}{
		{"a path", []RealmOption{WithPath("/a/")}, true},
		{"a template", []RealmOption{WithPathTemplate("/a/{b}/")}, true},
		{"neither", nil, false},
		{"both", []RealmOption{WithPath("/a/"), WithPathTemplate("/a/{b}/")}, false},
		{"an empty template", []RealmOption{WithPathTemplate("")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRealm(append([]RealmOption{WithHttpClient(c)}, tt.options...)...)
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok = %v", err, tt.ok)
			}
		})
	}
}

// TestSubtreeContexts verifies what a context pins, and what ChamberPath and
// Revision report for it.
func TestSubtreeContexts(t *testing.T) {
	paths := []string{"/prod/tenants/", "/prod/tenants/t-1/", "/prod/tenants/t-1/loc-1/"}
	rlm := templateRealm(t, "/prod/tenants/{tenantId}/{locationId}/", paths, WithAttributes(map[string]string{"tenantId": "t-1"}))
	bg := context.Background()
	loc := EvaluationContext{Attributes: map[string]string{"locationId": "loc-1"}}

	tests := []struct {
		name     string
		ctx      context.Context
		path     string
		revision uint64
	}{
		{"NewContext pins the root", rlm.NewContext(bg), "/prod/tenants/", 100},
		{"a bare context resolves from process-wide attributes", bg, "/prod/tenants/t-1/", 101},
		{"NewContextWithEvaluation pins its view", rlm.NewContextWithEvaluation(bg, loc), "/prod/tenants/t-1/loc-1/", 102},
		{"an evaluation context alone resolves", context.WithValue(bg, EvaluationContextKey, loc), "/prod/tenants/t-1/loc-1/", 102},
		{"process-wide attributes alone", rlm.NewContextWithEvaluation(bg, EvaluationContext{}), "/prod/tenants/t-1/", 101},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rlm.ChamberPath(tt.ctx); got != tt.path {
				t.Errorf("ChamberPath = %q, want %q", got, tt.path)
			}
			if got := rlm.Revision(tt.ctx); got != tt.revision {
				t.Errorf("Revision = %d, want %d", got, tt.revision)
			}
		})
	}

	t.Run("a pinned view outlives the snapshot", func(t *testing.T) {
		pinned := rlm.NewContextWithEvaluation(bg, loc)
		unpinned := context.WithValue(bg, EvaluationContextKey, loc)
		rlm.mu.Lock()
		delete(rlm.views, "/prod/tenants/t-1/loc-1/")
		rlm.mu.Unlock()
		if got := rlm.ChamberPath(pinned); got != "/prod/tenants/t-1/loc-1/" {
			t.Errorf("pinned: ChamberPath = %q", got)
		}
		if got, _ := rlm.String(pinned, "msg", ""); got != "/prod/tenants/t-1/loc-1/" {
			t.Errorf("pinned: msg = %q", got)
		}
		if got := rlm.ChamberPath(unpinned); got != "/prod/tenants/t-1/" {
			t.Errorf("unpinned: ChamberPath = %q, want the tenant's now", got)
		}
	})

	t.Run("before the first load", func(t *testing.T) {
		empty := templateRealm(t, "/prod/tenants/{tenantId}/", nil)
		ctx := empty.NewContextWithEvaluation(bg, EvaluationContext{Attributes: map[string]string{"tenantId": "t-1"}})
		if got := empty.ChamberPath(ctx); got != "" {
			t.Errorf("ChamberPath = %q, want none", got)
		}
		if _, err := empty.Bool(ctx, "on", false); err != ErrChamberEmpty {
			t.Errorf("err = %v, want ErrChamberEmpty", err)
		}
		// the context pinned nothing, so it resolves once views arrive
		empty.mu.Lock()
		empty.views = map[string]*ChamberEntry{"/prod/tenants/t-1/": empty.newView("/prod/tenants/t-1/", &Chamber{Rules: map[string]*Flag{}}, 0)}
		empty.mu.Unlock()
		if got := empty.ChamberPath(ctx); got != "/prod/tenants/t-1/" {
			t.Errorf("after a load: ChamberPath = %q", got)
		}
	})
}

// TestSingleChamberPath verifies ChamberPath without a template.
func TestSingleChamberPath(t *testing.T) {
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	rlm, err := NewRealm(WithHttpClient(c), WithPath("/a/"), WithLogger(zerolog.Nop()))
	if err != nil {
		t.Fatal(err)
	}
	if got := rlm.ChamberPath(context.Background()); got != "" {
		t.Errorf("before a load: ChamberPath = %q, want none", got)
	}
	rlm.setChamber(&Chamber{Rules: map[string]*Flag{}}, 0)
	if got := rlm.ChamberPath(context.Background()); got != "/a/" {
		t.Errorf("ChamberPath = %q, want the configured path", got)
	}
}

// TestSubtreeReadsDoNotAllocate verifies flag reads with a pinned subtree
// context, and resolution of an unpinned one, are allocation-free.
func TestSubtreeReadsDoNotAllocate(t *testing.T) {
	rlm := templateRealm(t, "/prod/tenants/{tenantId}/{locationId}/", []string{"/prod/tenants/", "/prod/tenants/t-1/", "/prod/tenants/t-1/loc-1/"})
	ec := EvaluationContext{Key: "user-1", Attributes: map[string]string{"tenantId": "t-1", "locationId": "loc-1"}}
	pinned := rlm.NewContextWithEvaluation(context.Background(), ec)
	unpinned := context.WithValue(context.Background(), EvaluationContextKey, ec)
	tests := []struct {
		name string
		read func()
	}{
		{"Bool", func() { _, _ = rlm.Bool(pinned, "on", false) }},
		{"String", func() { _, _ = rlm.String(pinned, "msg", "") }},
		{"Float64", func() { _, _ = rlm.Float64(pinned, "rate", 0) }},
		{"Revision", func() { _ = rlm.Revision(pinned) }},
		{"ChamberPath", func() { _ = rlm.ChamberPath(pinned) }},
		{"unpinned Bool", func() { _, _ = rlm.Bool(unpinned, "on", false) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if n := testing.AllocsPerRun(100, tt.read); n != 0 {
				t.Errorf("%v allocations per read, want 0", n)
			}
		})
	}
}

// benchmarkRealm returns a Realm holding a subtree of 10,000 locations under
// 100 tenants, and an evaluation context for one location.
func benchmarkRealm(b *testing.B) (*Realm, EvaluationContext) {
	paths := []string{"/prod/tenants/"}
	for i := range 100 {
		tenant := "/prod/tenants/t-" + strconv.Itoa(i) + "/"
		paths = append(paths, tenant)
		for j := range 100 {
			paths = append(paths, tenant+"loc-"+strconv.Itoa(j)+"/")
		}
	}
	rlm := templateRealm(b, "/prod/tenants/{tenantId}/{locationId}/", paths)
	return rlm, EvaluationContext{Key: "user-1", Attributes: map[string]string{"tenantId": "t-42", "locationId": "loc-7"}}
}

func BenchmarkRealmSubtreeBool(b *testing.B) {
	rlm, ec := benchmarkRealm(b)
	ctx := rlm.NewContextWithEvaluation(context.Background(), ec)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := rlm.Bool(ctx, "on", false); err != nil {
				b.Error(err)
			}
		}
	})
}

func BenchmarkRealmSubtreeString(b *testing.B) {
	rlm, ec := benchmarkRealm(b)
	ctx := rlm.NewContextWithEvaluation(context.Background(), ec)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := rlm.String(ctx, "msg", ""); err != nil {
				b.Error(err)
			}
		}
	})
}

func BenchmarkRealmSubtreeFloat64(b *testing.B) {
	rlm, ec := benchmarkRealm(b)
	ctx := rlm.NewContextWithEvaluation(context.Background(), ec)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := rlm.Float64(ctx, "rate", 0); err != nil {
				b.Error(err)
			}
		}
	})
}

// BenchmarkRealmSubtreeUnpinned reads a flag with a context that pins
// nothing, resolving the template on every read.
func BenchmarkRealmSubtreeUnpinned(b *testing.B) {
	rlm, ec := benchmarkRealm(b)
	ctx := context.WithValue(context.Background(), EvaluationContextKey, ec)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := rlm.Bool(ctx, "on", false); err != nil {
				b.Error(err)
			}
		}
	})
}

func BenchmarkRealmSubtreeResolve(b *testing.B) {
	rlm, ec := benchmarkRealm(b)
	ec = ec.withDefaults(rlm.attributes)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if rlm.template.resolve(rlm.views, ec) == nil {
				b.Error("no view")
			}
		}
	})
}

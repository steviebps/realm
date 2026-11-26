package realm

import (
	"context"
	"maps"
	"testing"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/internal/evaluations/evaluationstest"
)

type series = evaluationstest.Series

// chamberRealm returns a Realm for path that never refreshes, holding the
// chamber in data.
func chamberRealm(t testing.TB, path, data string) *Realm {
	t.Helper()
	c, err := client.NewHttpClient(&client.HttpClientConfig{Address: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	rlm, err := NewRealm(WithHttpClient(c), WithPath(path), WithLogger(zerolog.Nop()))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := decodeChamber([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	rlm.snap.(*chamberSnapshot).set(context.Background(), ch, 0)
	return rlm
}

// withTenant returns ctx evaluating for a context with the tenantId tenant.
func withTenant(rlm *Realm, tenant string) context.Context {
	return rlm.NewContextWithEvaluation(context.Background(), EvaluationContext{Attributes: map[string]string{"tenantId": tenant}})
}

func TestEvaluationsCountTheRuleServed(t *testing.T) {
	rlm := chamberRealm(t, "evaluations/served", `{"rules":{"checkout":{"type":"boolean","value":false,"targeting":[
		{"id":"beta","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true},
		{"clauses":[{"attribute":"tenantId","op":"in","values":["globex"]}],"value":true}]}}}`)
	for _, tenant := range []string{"acme", "acme", "globex", "initech"} {
		if _, err := rlm.Bool(withTenant(rlm, tenant), "checkout", false); err != nil {
			t.Fatal(err)
		}
	}

	path := "/evaluations/served/"
	want := map[series]int64{
		{Flag: "checkout", Path: path, Rule: "beta"}:    2,
		{Flag: "checkout", Path: path, Rule: "#2"}:      1,
		{Flag: "checkout", Path: path, Rule: "default"}: 1,
	}
	if got := evaluationstest.Collect(t, path); !maps.Equal(got, want) {
		t.Errorf("realm.flag.evaluations = %v, want %v", got, want)
	}
}

// TestEvaluationsCountWithoutAllocating checks that counting adds no
// allocation to a flag read, once the flag has been read from its view.
func TestEvaluationsCountWithoutAllocating(t *testing.T) {
	rlm := chamberRealm(t, "evaluations/allocs", `{"rules":{"checkout":{"type":"boolean","value":false,"targeting":[
		{"id":"beta","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true}]}}}`)
	for _, tenant := range []string{"acme", "initech"} {
		ctx := withTenant(rlm, tenant)
		if _, err := rlm.Bool(ctx, "checkout", false); err != nil {
			t.Fatal(err)
		}
		if n := testing.AllocsPerRun(100, func() { _, _ = rlm.Bool(ctx, "checkout", false) }); n != 0 {
			t.Errorf("Bool for %s allocates %v times per read, want 0", tenant, n)
		}
	}
}

func BenchmarkRealmBoolCounted(b *testing.B) {
	rlm := chamberRealm(b, "evaluations/bench", `{"rules":{"checkout":{"type":"boolean","value":false,"targeting":[
		{"id":"beta","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true,"rollout":{"percentage":50}}]}}}`)
	ctx := rlm.NewContextWithEvaluation(context.Background(), EvaluationContext{Key: "user-1", Attributes: map[string]string{"tenantId": "acme"}})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = rlm.Bool(ctx, "checkout", false)
		}
	})
}

// TestEvaluationsCountOnlyValuesServed checks that every getter counts the
// values it serves, and that a read returning the caller's default (a flag
// that does not exist, is of another type, or of a type this SDK does not
// know) counts nothing.
func TestEvaluationsCountOnlyValuesServed(t *testing.T) {
	rlm := chamberRealm(t, "/evaluations/getters/", `{"rules":{
		"on":{"type":"boolean","value":true},
		"msg":{"type":"string","value":"hi"},
		"rate":{"type":"number","value":0.5},
		"cfg":{"type":"custom","value":{"a":1}},
		"later":{"type":"futureType","value":1}}}`)
	ctx := rlm.NewContext(context.Background())

	if _, err := rlm.Bool(ctx, "on", false); err != nil {
		t.Fatal(err)
	}
	if _, err := rlm.String(ctx, "msg", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := rlm.Float64(ctx, "rate", 0); err != nil {
		t.Fatal(err)
	}
	var cfg struct{ A int }
	if err := rlm.CustomValue(ctx, "cfg", &cfg); err != nil {
		t.Fatal(err)
	}
	// none of these serves the flag's value
	if _, err := rlm.Bool(ctx, "missing", false); err == nil {
		t.Error("Bool of a missing flag succeeded")
	}
	if _, err := rlm.Bool(ctx, "msg", false); err == nil {
		t.Error("Bool of a string flag succeeded")
	}
	if _, err := rlm.Float64(ctx, "later", 0); err == nil {
		t.Error("Float64 of a flag of an unknown type succeeded")
	}
	var wrong []string
	if err := rlm.CustomValue(ctx, "cfg", &wrong); err == nil {
		t.Error("CustomValue into the wrong type succeeded")
	}

	path := "/evaluations/getters/"
	want := map[series]int64{
		{Flag: "on", Path: path, Rule: "default"}:   1,
		{Flag: "msg", Path: path, Rule: "default"}:  1,
		{Flag: "rate", Path: path, Rule: "default"}: 1,
		{Flag: "cfg", Path: path, Rule: "default"}:  1,
	}
	if got := evaluationstest.Collect(t, path); !maps.Equal(got, want) {
		t.Errorf("realm.flag.evaluations = %v, want %v", got, want)
	}
}

// TestEvaluationsCountAtTheViewsPath checks that with a path template, an
// evaluation counts at the path of the view the context resolves to.
func TestEvaluationsCountAtTheViewsPath(t *testing.T) {
	rlm := templateRealm(t, "/evaluations/tenants/{tenantId}/", []string{"/evaluations/tenants/", "/evaluations/tenants/acme/"})
	for _, tenant := range []string{"acme", "acme", "globex"} {
		if _, err := rlm.String(withTenant(rlm, tenant), "msg", ""); err != nil {
			t.Fatal(err)
		}
	}
	for path, n := range map[string]int64{"/evaluations/tenants/acme/": 2, "/evaluations/tenants/": 1} {
		want := map[series]int64{{Flag: "msg", Path: path, Rule: "default"}: n}
		if got := evaluationstest.Collect(t, path); !maps.Equal(got, want) {
			t.Errorf("at %s: realm.flag.evaluations = %v, want %v", path, got, want)
		}
	}
}

// TestEvaluationsAccumulateAcrossChanges checks that a rule's count keeps
// growing when the chamber changes, following the rule by its id when the
// rules are reordered.
func TestEvaluationsAccumulateAcrossChanges(t *testing.T) {
	rlm := chamberRealm(t, "evaluations/changes", `{"rules":{"checkout":{"type":"boolean","value":false,"targeting":[
		{"id":"beta","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true}]}}}`)
	if _, err := rlm.Bool(withTenant(rlm, "acme"), "checkout", false); err != nil {
		t.Fatal(err)
	}
	reordered, err := decodeChamber([]byte(`{"rules":{"checkout":{"type":"boolean","value":false,"targeting":[
		{"id":"pilot","clauses":[{"attribute":"tenantId","op":"in","values":["globex"]}],"value":true},
		{"id":"beta","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rlm.snap.(*chamberSnapshot).set(context.Background(), reordered, 0)
	for _, tenant := range []string{"acme", "globex"} {
		if _, err := rlm.Bool(withTenant(rlm, tenant), "checkout", false); err != nil {
			t.Fatal(err)
		}
	}

	path := "/evaluations/changes/"
	want := map[series]int64{
		{Flag: "checkout", Path: path, Rule: "beta"}:  2,
		{Flag: "checkout", Path: path, Rule: "pilot"}: 1,
	}
	if got := evaluationstest.Collect(t, path); !maps.Equal(got, want) {
		t.Errorf("realm.flag.evaluations = %v, want %v", got, want)
	}
}

// TestEvaluationsCountPausedTargetingAsDefault checks that a flag whose
// targeting is turned off counts its value as its default.
func TestEvaluationsCountPausedTargetingAsDefault(t *testing.T) {
	rlm := chamberRealm(t, "evaluations/paused", `{"rules":{"checkout":{"type":"boolean","value":false,"pausedTargeting":[
		{"id":"beta","clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true}]}}}`)
	if _, err := rlm.Bool(withTenant(rlm, "acme"), "checkout", false); err != nil {
		t.Fatal(err)
	}
	path := "/evaluations/paused/"
	want := map[series]int64{{Flag: "checkout", Path: path, Rule: "default"}: 1}
	if got := evaluationstest.Collect(t, path); !maps.Equal(got, want) {
		t.Errorf("realm.flag.evaluations = %v, want %v", got, want)
	}
}

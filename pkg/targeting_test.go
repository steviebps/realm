package realm

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestClauseMatches(t *testing.T) {
	ec := EvaluationContext{
		Key: "user-1",
		Attributes: map[string]string{
			"locationId":  "loc-a",
			"environment": "prod",
			"buildSha":    "3f9a2c71be04d5e6",
			"version":     "1.4.2",
		},
	}

	tests := []struct {
		name   string
		clause Clause
		want   bool
	}{
		{"in match", Clause{Attribute: "locationId", Op: OpIn, Values: []string{"loc-b", "loc-a"}}, true},
		{"in miss", Clause{Attribute: "locationId", Op: OpIn, Values: []string{"loc-b"}}, false},
		{"in on key", Clause{Attribute: "key", Op: OpIn, Values: []string{"user-1"}}, true},
		{"notIn match", Clause{Attribute: "environment", Op: OpNotIn, Values: []string{"staging"}}, true},
		{"notIn miss", Clause{Attribute: "environment", Op: OpNotIn, Values: []string{"prod"}}, false},
		{"notIn missing attribute never matches", Clause{Attribute: "region", Op: OpNotIn, Values: []string{"us-west-2"}}, false},
		{"in missing attribute", Clause{Attribute: "tenantId", Op: OpIn, Values: []string{"t1"}}, false},
		{"startsWith short sha", Clause{Attribute: "buildSha", Op: OpStartsWith, Values: []string{"3f9a2c7"}}, true},
		{"startsWith miss", Clause{Attribute: "buildSha", Op: OpStartsWith, Values: []string{"aaaa"}}, false},
		{"semverGte equal", Clause{Attribute: "version", Op: OpSemverGte, Values: []string{"v1.4.2"}}, true},
		{"semverGte below", Clause{Attribute: "version", Op: OpSemverGte, Values: []string{"1.5.0"}}, false},
		{"semverLte above", Clause{Attribute: "version", Op: OpSemverLte, Values: []string{"1.4.1"}}, false},
		{"semverLte equal", Clause{Attribute: "version", Op: OpSemverLte, Values: []string{"1.4.2"}}, true},
		{"semver on non-semver attribute", Clause{Attribute: "environment", Op: OpSemverGte, Values: []string{"1.0.0"}}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.clause.Matches(ec); got != test.want {
				t.Errorf("Matches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAttributeDefaults(t *testing.T) {
	ec := EvaluationContext{Attributes: map[string]string{"environment": "staging"}}.
		withDefaults(map[string]string{"environment": "prod", "service": "api"})

	if v, _ := ec.Attribute("environment"); v != "staging" {
		t.Errorf("request attribute should win over defaults, got %q", v)
	}
	if v, ok := ec.Attribute("service"); !ok || v != "api" {
		t.Errorf("expected default attribute service=api, got %q (%v)", v, ok)
	}
	if _, ok := ec.Attribute("key"); ok {
		t.Error("empty Key should not be reported as present")
	}
}

func TestClauseUnmarshalValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid in", `{"attribute":"locationId","op":"in","values":["a"]}`, false},
		{"valid semver without v", `{"attribute":"version","op":"semverGte","values":["1.2.0"]}`, false},
		{"missing attribute", `{"op":"in","values":["a"]}`, true},
		{"no values", `{"attribute":"a","op":"in","values":[]}`, true},
		{"unknown op", `{"attribute":"a","op":"regex","values":["a"]}`, true},
		{"semver two values", `{"attribute":"version","op":"semverGte","values":["1.0.0","2.0.0"]}`, true},
		{"semver invalid", `{"attribute":"version","op":"semverLte","values":["latest"]}`, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var c Clause
			err := json.Unmarshal([]byte(test.input), &c)
			if (err != nil) != test.wantErr {
				t.Errorf("err = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestFlagUnmarshalValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"no targeting", `{"type":"boolean","value":false}`, ""},
		{"valid targeting", `{"type":"boolean","value":false,"targeting":[{"id":"a","clauses":[{"attribute":"locationId","op":"in","values":["x"]}],"value":true,"rollout":{"percentage":50,"bucketBy":"locationId"}}]}`, ""},
		{"rule value wrong type", `{"type":"boolean","value":false,"targeting":[{"value":"yes"}]}`, "incompatible"},
		{"rule value missing", `{"type":"boolean","value":false,"targeting":[{"clauses":[]}]}`, "cannot be empty"},
		{"duplicate ids", `{"type":"string","value":"a","targeting":[{"id":"x","value":"b"},{"id":"x","value":"c"}]}`, "not unique"},
		{"rollout out of range", `{"type":"boolean","value":false,"targeting":[{"value":true,"rollout":{"percentage":101}}]}`, "range"},
		{"bad clause", `{"type":"boolean","value":false,"targeting":[{"value":true,"clauses":[{"attribute":"a","op":"nope","values":["x"]}]}]}`, "unsupported operator"},
		{"legacy overrides rejected", `{"type":"boolean","value":false,"overrides":[]}`, "no longer supported"},
		{"legacy rollout rejected", `{"type":"boolean","value":false,"rollout":{"type":"boolean","value":true,"percentage":5}}`, "no longer supported"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var f Flag
			err := json.Unmarshal([]byte(test.input), &f)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestFlagValueForFirstMatchWins(t *testing.T) {
	input := `{
		"type": "string",
		"value": "default",
		"targeting": [
			{"id": "pilot", "clauses": [{"attribute": "locationId", "op": "in", "values": ["loc-a"]}], "value": "pilot"},
			{"id": "prod", "clauses": [{"attribute": "environment", "op": "in", "values": ["prod"]}], "value": "prod"},
			{"id": "old-clients", "clauses": [{"attribute": "version", "op": "semverLte", "values": ["1.9.9"]}], "value": "legacy"}
		]
	}`
	var f Flag
	if err := json.Unmarshal([]byte(input), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	tests := []struct {
		name  string
		attrs map[string]string
		want  string
	}{
		{"first rule wins over later matches", map[string]string{"locationId": "loc-a", "environment": "prod"}, "pilot"},
		{"second rule", map[string]string{"locationId": "loc-b", "environment": "prod"}, "prod"},
		{"version rule", map[string]string{"environment": "staging", "version": "v1.2.0"}, "legacy"},
		{"fallthrough", map[string]string{"environment": "staging", "version": "v2.0.0"}, "default"},
		{"empty context falls through", nil, "default"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := f.StringValueFor("flag", EvaluationContext{Attributes: test.attrs}, "")
			if !ok || got != test.want {
				t.Errorf("got %q (%v), want %q", got, ok, test.want)
			}
		})
	}
}

func TestRolloutNotBucketedContinuesToNextRule(t *testing.T) {
	f := &Flag{
		Rule: &Rule{Type: "string", Value: "default"},
		Targeting: []*TargetingRule{
			{Value: "rolled", Rollout: &Rollout{Percentage: 0}},
			{Value: "next"},
		},
	}
	if v := f.ValueFor("flag", EvaluationContext{Key: "user-1"}); v != "next" {
		t.Errorf("expected the 0%% rollout to fall through to the next rule, got %v", v)
	}
}

func TestRolloutIncludes(t *testing.T) {
	t.Run("sticky", func(t *testing.T) {
		r := &Rollout{Percentage: 50}
		ec := EvaluationContext{Key: "user-1234"}
		first := r.Includes("flag", ec)
		for i := 0; i < 1000; i++ {
			if got := r.Includes("flag", ec); got != first {
				t.Fatalf("rollout assignment is not sticky: got %v then %v", first, got)
			}
		}
	})

	t.Run("missing bucketing attribute is excluded", func(t *testing.T) {
		r := &Rollout{Percentage: 100}
		if r.Includes("flag", EvaluationContext{}) {
			t.Error("context without a key should not be included")
		}
		byLocation := &Rollout{Percentage: 100, BucketBy: "locationId"}
		if byLocation.Includes("flag", EvaluationContext{Key: "user-1"}) {
			t.Error("context without locationId should not be included when bucketing by it")
		}
	})

	t.Run("nil rollout includes everyone", func(t *testing.T) {
		var r *Rollout
		if !r.Includes("flag", EvaluationContext{}) {
			t.Error("nil rollout should include every context")
		}
	})

	t.Run("boundaries", func(t *testing.T) {
		none := &Rollout{Percentage: 0}
		all := &Rollout{Percentage: 100}
		for i := 0; i < 1000; i++ {
			ec := EvaluationContext{Key: fmt.Sprintf("user-%d", i)}
			if none.Includes("flag", ec) {
				t.Fatalf("0%% rollout included key %q", ec.Key)
			}
			if !all.Includes("flag", ec) {
				t.Fatalf("100%% rollout excluded key %q", ec.Key)
			}
		}
	})

	t.Run("bucketBy groups subjects", func(t *testing.T) {
		r := &Rollout{Percentage: 50, BucketBy: "locationId"}
		for loc := 0; loc < 50; loc++ {
			locID := fmt.Sprintf("loc-%d", loc)
			first := r.Includes("flag", EvaluationContext{Key: "user-0", Attributes: map[string]string{"locationId": locID}})
			for u := 1; u < 20; u++ {
				ec := EvaluationContext{Key: fmt.Sprintf("user-%d", u), Attributes: map[string]string{"locationId": locID}}
				if r.Includes("flag", ec) != first {
					t.Fatalf("users in %s were split by a location-bucketed rollout", locID)
				}
			}
		}
	})

	t.Run("seed changes bucketing", func(t *testing.T) {
		a := &Rollout{Percentage: 50, Seed: "seed-a"}
		b := &Rollout{Percentage: 50, Seed: "seed-b"}
		disagreements := 0
		for i := 0; i < 1000; i++ {
			ec := EvaluationContext{Key: fmt.Sprintf("user-%d", i)}
			if a.Includes("flag", ec) != b.Includes("flag", ec) {
				disagreements++
			}
		}
		if disagreements == 0 {
			t.Error("expected different seeds to bucket differently for some keys")
		}
	})
}

func TestRolloutDistribution(t *testing.T) {
	const (
		n         = 20000
		percent   = 30.0
		tolerance = 2.0 // percentage points
	)
	r := &Rollout{Percentage: percent}

	included := 0
	for i := 0; i < n; i++ {
		if r.Includes("flag", EvaluationContext{Key: fmt.Sprintf("user-%d", i)}) {
			included++
		}
	}

	actual := float64(included) / float64(n) * 100
	if math.Abs(actual-percent) > tolerance {
		t.Errorf("expected ~%.0f%% inclusion (±%.0f), got %.2f%% (%d/%d)", percent, tolerance, actual, included, n)
	}
}

func TestFlagRoundTrips(t *testing.T) {
	input := `{"type":"custom","value":{"limit":1},"targeting":[{"id":"a","description":"pilot","clauses":[{"attribute":"tenantId","op":"in","values":["t1"]}],"value":{"limit":5},"rollout":{"percentage":50,"bucketBy":"tenantId","seed":"s"}}]}`

	var f Flag
	if err := json.Unmarshal([]byte(input), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round Flag
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("re-unmarshal: %v (%s)", err, out)
	}
	if len(round.Targeting) != 1 || round.Targeting[0].Rollout.BucketBy != "tenantId" || round.Targeting[0].Description != "pilot" {
		t.Errorf("targeting did not round-trip: %s", out)
	}

	var v struct{ Limit int }
	ec := EvaluationContext{Key: "k", Attributes: map[string]string{"tenantId": "t1"}}
	// Widen the rollout so the custom rule value is always served.
	round.Targeting[0].Rollout.Percentage = 100
	if err := round.CustomValueFor("flag", ec, &v); err != nil || v.Limit != 5 {
		t.Errorf("custom targeting value = %+v, err %v", v, err)
	}
}

func TestChamberEntryUsesProcessAttributes(t *testing.T) {
	var c Chamber
	if err := json.Unmarshal([]byte(`{"rules":{"beta":{"type":"boolean","value":false,"targeting":[
		{"clauses":[{"attribute":"environment","op":"in","values":["staging"]}],"value":true}
	]}}}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	staging := NewChamberEntry(&c, map[string]string{"environment": "staging"})
	if on, err := staging.BoolValue("beta", false); err != nil || !on {
		t.Errorf("expected process attribute to enable flag, got %v (%v)", on, err)
	}

	prod := NewChamberEntry(&c, map[string]string{"environment": "prod"})
	if on, _ := prod.BoolValue("beta", false); on {
		t.Error("expected flag off in prod")
	}
	// A request attribute overrides the process attribute.
	ec := EvaluationContext{Attributes: map[string]string{"environment": "staging"}}
	if on, _ := prod.BoolValueFor("beta", ec, false); !on {
		t.Error("expected request attribute to override process attribute")
	}
}

func BenchmarkFlagValueForTargeting(b *testing.B) {
	locations := make([]string, 200)
	for i := range locations {
		locations[i] = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
	}
	f := &Flag{
		Rule: &Rule{Type: "boolean", Value: false},
		Targeting: []*TargetingRule{
			{Clauses: []*Clause{{Attribute: "environment", Op: OpIn, Values: []string{"prod"}}, {Attribute: "locationId", Op: OpIn, Values: locations}}, Value: true},
			{Clauses: []*Clause{{Attribute: "version", Op: OpSemverGte, Values: []string{"v2.0.0"}}}, Value: true, Rollout: &Rollout{Percentage: 25, BucketBy: "locationId"}},
		},
	}
	ec := EvaluationContext{Key: "user-1", Attributes: map[string]string{
		"environment": "prod", "locationId": "not-targeted", "version": "v2.1.0",
	}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			f.BoolValueFor("flag", ec, false)
		}
	})
}

func BenchmarkRolloutIncludes(b *testing.B) {
	r := &Rollout{Percentage: 50}
	ec := EvaluationContext{Key: "user-1234"}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Includes("flag", ec)
		}
	})
}

func TestBucketMatchesHexAlgorithm(t *testing.T) {
	// Guards bucket assignments against drift: bucket must equal the value
	// derived from the 15 leading hex digits of sha1("<seed>.<key>").
	for i := 0; i < 100; i++ {
		seed, key := "flag", fmt.Sprintf("user-%d", i)
		sum := sha1.Sum([]byte(seed + "." + key))
		v, _ := strconv.ParseInt(hex.EncodeToString(sum[:])[:15], 16, 64)
		want := float64(v) / longScale * 100
		if got := bucket(seed, key); got != want {
			t.Fatalf("bucket(%q, %q) = %v, want %v", seed, key, got, want)
		}
	}
}

package realm

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

func TestRolloutUnmarshalValidation(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		errorExpected bool
	}{
		{"valid boolean rollout", `{"type":"boolean","value":true,"percentage":50}`, false},
		{"valid with seed", `{"type":"string","value":"on","percentage":25,"seed":"shared"}`, false},
		{"percentage zero", `{"type":"boolean","value":true,"percentage":0}`, false},
		{"percentage hundred", `{"type":"boolean","value":true,"percentage":100}`, false},
		{"percentage below range", `{"type":"boolean","value":true,"percentage":-1}`, true},
		{"percentage above range", `{"type":"boolean","value":true,"percentage":101}`, true},
		{"type mismatch", `{"type":"boolean","value":"nope","percentage":50}`, true},
		{"missing value", `{"type":"boolean","percentage":50}`, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var r Rollout
			err := json.Unmarshal([]byte(test.input), &r)
			if err != nil && !test.errorExpected {
				t.Errorf("unexpected error for %q: %v", test.input, err)
			}
			if err == nil && test.errorExpected {
				t.Errorf("expected error for %q but got none", test.input)
			}
		})
	}
}

func TestRolloutIncludesDeterministic(t *testing.T) {
	r := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 50}
	ec := EvaluationContext{Key: "user-1234"}

	first := r.Includes("flag", ec)
	for i := 0; i < 1000; i++ {
		if got := r.Includes("flag", ec); got != first {
			t.Fatalf("rollout assignment for %q is not sticky: got %v then %v", ec.Key, first, got)
		}
	}
}

func TestRolloutIncludesEmptyKeyIsInert(t *testing.T) {
	r := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 100}
	if r.Includes("flag", EvaluationContext{}) {
		t.Error("rollout with empty evaluation key should never include the context")
	}
}

func TestRolloutIncludesBoundaries(t *testing.T) {
	none := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 0}
	all := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 100}

	for i := 0; i < 1000; i++ {
		ec := EvaluationContext{Key: fmt.Sprintf("user-%d", i)}
		if none.Includes("flag", ec) {
			t.Fatalf("0%% rollout included key %q", ec.Key)
		}
		if !all.Includes("flag", ec) {
			t.Fatalf("100%% rollout excluded key %q", ec.Key)
		}
	}
}

func TestRolloutIncludesDistribution(t *testing.T) {
	const (
		n         = 20000
		percent   = 30.0
		tolerance = 2.0 // percentage points
	)
	r := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: percent}

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

func TestRolloutSeedChangesBucketing(t *testing.T) {
	// The same key under two different seeds should not be forced to the same
	// assignment; over many keys the two seeds must disagree at least sometimes.
	a := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 50, Seed: "seed-a"}
	b := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 50, Seed: "seed-b"}

	disagreements := 0
	for i := 0; i < 1000; i++ {
		ec := EvaluationContext{Key: fmt.Sprintf("user-%d", i)}
		if a.Includes("flag", ec) != b.Includes("flag", ec) {
			disagreements++
		}
	}
	if disagreements == 0 {
		t.Error("expected different seeds to produce different bucketing for at least some keys")
	}
}

func TestValueForAppliesRollout(t *testing.T) {
	rule := &OverrideableRule{
		Rule:    &Rule{Type: "boolean", Value: false},
		Rollout: &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 100},
	}

	// With a key and a 100% rollout, the rollout value wins.
	if v := rule.ValueFor("flag", EvaluationContext{Key: "user-1"}, ""); v != true {
		t.Errorf("expected rollout value true, got %v", v)
	}
	// Without a key, the rollout is inert and the base value is returned.
	if v := rule.ValueFor("flag", EvaluationContext{}, ""); v != false {
		t.Errorf("expected base value false when no key, got %v", v)
	}
}

func TestValueForRolloutOverridesVersionResult(t *testing.T) {
	// A rollout is applied on top of the version-resolved base value.
	rule := &OverrideableRule{
		Rule: &Rule{Type: "string", Value: "default"},
		Overrides: []*Override{
			{Rule: &Rule{Type: "string", Value: "v1"}, MinimumVersion: "v1.0.0", MaximumVersion: "v2.0.0"},
		},
		Rollout: &Rollout{Rule: &Rule{Type: "string", Value: "rolled"}, Percentage: 100},
	}

	// Version override resolves to "v1", but the 100% rollout takes precedence.
	if v := rule.ValueFor("flag", EvaluationContext{Key: "user-1"}, "v1.5.0"); v != "rolled" {
		t.Errorf("expected rollout value %q to win, got %v", "rolled", v)
	}
	// With no rollout key, the version override still applies.
	if v := rule.ValueFor("flag", EvaluationContext{}, "v1.5.0"); v != "v1" {
		t.Errorf("expected version override %q, got %v", "v1", v)
	}
}

func TestChamberEntryValueForRollout(t *testing.T) {
	chamber := &Chamber{Rules: map[string]*OverrideableRule{
		"beta": {
			Rule:    &Rule{Type: "boolean", Value: false},
			Rollout: &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 100},
		},
	}}
	entry := NewChamberEntry(chamber, "")

	on, err := entry.BoolValueFor("beta", EvaluationContext{Key: "user-1"}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !on {
		t.Error("expected 100% rollout to enable the flag for a keyed context")
	}

	// The legacy BoolValue (no evaluation context) must keep returning the base
	// value, proving backward compatibility.
	base, err := entry.BoolValue("beta", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base {
		t.Error("expected legacy BoolValue to ignore rollout and return the base value")
	}
}

func TestOverrideableRuleRolloutRoundTrips(t *testing.T) {
	input := `{"type":"boolean","value":false,"rollout":{"type":"boolean","value":true,"percentage":50}}`

	var rule OverrideableRule
	if err := json.Unmarshal([]byte(input), &rule); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if rule.Rollout == nil {
		t.Fatal("expected rollout to be parsed")
	}

	out, err := json.Marshal(&rule)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var round OverrideableRule
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("re-unmarshal failed: %v", err)
	}
	if round.Rollout == nil || round.Rollout.Percentage != 50 {
		t.Errorf("rollout did not round-trip through JSON: %s", string(out))
	}
}

func BenchmarkRolloutIncludes(b *testing.B) {
	r := &Rollout{Rule: &Rule{Type: "boolean", Value: true}, Percentage: 50}
	ec := EvaluationContext{Key: "user-1234"}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Includes("flag", ec)
		}
	})
}

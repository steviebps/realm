package realm

import (
	"encoding/json"
	"testing"
)

func TestAssertType(t *testing.T) {
	tests := []struct {
		assertedType  string
		input         json.RawMessage
		errorExpected bool
	}{
		{"boolean", []byte("false"), false},
		{"boolean", []byte("\"string\""), true},
		{"string", []byte("\"string\""), false},
		{"string", []byte("0"), true},
		{"number", []byte("1000.00"), false},
		{"number", []byte("false"), true},
	}

	for _, test := range tests {
		rule := &Rule{Type: test.assertedType}
		err := rule.assertType(test.input)
		if err != nil && !test.errorExpected {
			t.Errorf("input: %v with asserted type: %v\nreturned %v", string(test.input), test.assertedType, err)
		}
	}
}

func TestGetValueAt(t *testing.T) {
	tests := []struct {
		version string
		output  string
	}{
		{"", "default"},
		{"v1.0.0-pre.0", "default"},
		{"v1.0.0", "override1"},
		{"v1.0.1", "override1"},
		{"v1.0.2-pre.0", "override2"},
		{"v1.0.2", "override2"},
		{"v1.0.3-pre.0", "default"},
	}
	rule := &OverrideableRule{Rule: &Rule{Type: "string", Value: "default"}, Overrides: []*Override{{Rule: &Rule{Type: "string", Value: "override1"}, MinimumVersion: "v1.0.0", MaximumVersion: "v1.0.1"}, {Rule: &Rule{Type: "string", Value: "override2"}, MinimumVersion: "v1.0.1", MaximumVersion: "v1.0.2"}}}

	for _, test := range tests {
		val := rule.ValueAtVersion(test.version)
		if val != test.output {
			t.Errorf("version: %q should return %q but returned %q", test.version, test.output, val)
		}
	}
}

func BenchmarkRuleStringValue(b *testing.B) {
	t := &OverrideableRule{Rule: &Rule{Type: "string", Value: "string"}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			t.StringValue("v1.0.0", "")
		}
	})
}

func BenchmarkRuleBoolValue(b *testing.B) {
	t := &OverrideableRule{Rule: &Rule{Type: "boolean", Value: false}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			t.BoolValue("v1.0.0", false)
		}
	})
}

func BenchmarkRuleFloat64Value(b *testing.B) {
	t := &OverrideableRule{Rule: &Rule{Type: "number", Value: float64(10)}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			t.Float64Value("v1.0.0", 15)
		}
	})
}

func BenchmarkRuleCustomValue(b *testing.B) {
	type CustomStruct struct {
		Test string
	}
	raw := json.RawMessage(`{"Test":"test"}`)
	rule := &OverrideableRule{Rule: &Rule{Type: "custom", Value: &raw}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var v CustomStruct
			err := rule.CustomValue("v1.0.0", &v)
			if err != nil {
				b.Errorf("something went wrong: %v", err)
			}
		}
	})
}

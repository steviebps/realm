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

func BenchmarkRuleStringValue(b *testing.B) {
	t := &Flag{Rule: &Rule{Type: "string", Value: "string"}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			t.StringValueFor("flag", EvaluationContext{}, "")
		}
	})
}

func BenchmarkRuleBoolValue(b *testing.B) {
	t := &Flag{Rule: &Rule{Type: "boolean", Value: false}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			t.BoolValueFor("flag", EvaluationContext{}, false)
		}
	})
}

func BenchmarkRuleFloat64Value(b *testing.B) {
	t := &Flag{Rule: &Rule{Type: "number", Value: float64(10)}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			t.Float64ValueFor("flag", EvaluationContext{}, 15)
		}
	})
}

func BenchmarkRuleCustomValue(b *testing.B) {
	type CustomStruct struct {
		Test string
	}
	raw := json.RawMessage(`{"Test":"test"}`)
	rule := &Flag{Rule: &Rule{Type: "custom", Value: &raw}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var v CustomStruct
			err := rule.CustomValueFor("flag", EvaluationContext{}, &v)
			if err != nil {
				b.Errorf("something went wrong: %v", err)
			}
		}
	})
}

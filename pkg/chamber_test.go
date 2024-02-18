package realm

import (
	"encoding/json"
	"strconv"
	"testing"
)

func BenchmarkChamberStringValue(b *testing.B) {
	m := make(map[string]*OverrideableRule, 100000)
	for i := 1; i < 10000; i++ {
		m[strconv.Itoa(i)] = &OverrideableRule{Rule: &Rule{Type: "string", Value: "string"}}
	}

	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, "")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := chamber.StringValue("1", "")
			if err != nil {
				b.Errorf("should not be failing benchmark with error: %v", err)
			}
		}
	})
}

func BenchmarkChamberBoolValue(b *testing.B) {
	m := make(map[string]*OverrideableRule, 100000)
	for i := 1; i < 100000; i++ {
		m[strconv.Itoa(i)] = &OverrideableRule{Rule: &Rule{Type: "boolean", Value: false}}
	}
	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, "")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := chamber.BoolValue("1", false)
			if err != nil {
				b.Errorf("should not be failing benchmark with error: %v", err)
			}
		}
	})
}

func BenchmarkChamberFloat64Value(b *testing.B) {
	m := make(map[string]*OverrideableRule, 100000)
	for i := 1; i < 100000; i++ {
		m[strconv.Itoa(i)] = &OverrideableRule{Rule: &Rule{Type: "number", Value: float64(10)}}
	}
	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, "")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := chamber.Float64Value("1", 100)
			if err != nil {
				b.Errorf("should not be failing benchmark with error: %v", err)
			}
		}
	})
}

func BenchmarkChamberCustomValue(b *testing.B) {
	type CustomStruct struct {
		Test string
	}

	m := make(map[string]*OverrideableRule, 100000)
	for i := 0; i < 100000; i++ {
		raw := json.RawMessage(`{"Test":"test"}`)
		m[strconv.Itoa(i)] = &OverrideableRule{Rule: &Rule{Type: "custom", Value: &raw}}
	}

	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, "")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var v CustomStruct
			err := chamber.CustomValue("1", &v)
			if err != nil {
				b.Errorf("should not be failing benchmark with error: %v", err)
			}
		}
	})
}

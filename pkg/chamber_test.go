package realm

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func BenchmarkChamberStringValue(b *testing.B) {
	m := make(map[string]*Flag, 100000)
	for i := 1; i < 10000; i++ {
		m[strconv.Itoa(i)] = &Flag{Rule: &Rule{Type: "string", Value: "string"}}
	}

	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, nil)

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
	m := make(map[string]*Flag, 100000)
	for i := 1; i < 100000; i++ {
		m[strconv.Itoa(i)] = &Flag{Rule: &Rule{Type: "boolean", Value: false}}
	}
	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, nil)

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
	m := make(map[string]*Flag, 100000)
	for i := 1; i < 100000; i++ {
		m[strconv.Itoa(i)] = &Flag{Rule: &Rule{Type: "number", Value: float64(10)}}
	}
	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, nil)

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

	m := make(map[string]*Flag, 100000)
	for i := 0; i < 100000; i++ {
		raw := json.RawMessage(`{"Test":"test"}`)
		m[strconv.Itoa(i)] = &Flag{Rule: &Rule{Type: "custom", Value: &raw}}
	}

	chamber := NewChamberEntry(&Chamber{
		Rules: m,
	}, nil)

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

func TestInheritWith(t *testing.T) {
	bottom := &Chamber{
		Rules: map[string]*Flag{
			"rule2": {
				Rule: &Rule{
					Type:  "boolean",
					Value: false,
				},
			},
		},
	}
	middle := &Chamber{
		Rules: map[string]*Flag{
			"rule1": {
				Rule: &Rule{
					Type:  "boolean",
					Value: true,
				},
			},
		},
	}
	top := &Chamber{
		Rules: map[string]*Flag{
			"rule1": {
				Rule: &Rule{
					Type:  "boolean",
					Value: false,
				},
			},
		},
	}

	middle.InheritFrom(top)
	bottom.InheritFrom(middle)

	v1 := top.Rules["rule1"]
	v2 := middle.Rules["rule1"]
	v3 := bottom.Rules["rule1"]

	// should not inherit top value as is
	if v1 == v2 {
		t.Errorf("middle did not inherit properly from top: value of rule1 is: %v", v2)
	}

	// should inherit middle value as is
	if v3 != v2 {
		t.Errorf("bottom did not inherit properly from top: value of rule1 is: %v", v3)
	}
}

func TestSampleChambersParse(t *testing.T) {
	for _, path := range []string{"../configs/chamber.json", "../examples/go/chambers.json"} {
		t.Run(path, func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			// strictly, as the server would accept it
			if _, err := ParseChamber(b); err != nil {
				t.Fatalf("sample chamber does not parse: %v", err)
			}
		})
	}
}

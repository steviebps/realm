package realm

import (
	"encoding/json"
	"strconv"
	"sync"
	"testing"
)

func TestInheritWith(t *testing.T) {
	bottom := &Chamber{
		Toggles: map[string]*OverrideableToggle{
			"toggle2": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: false,
				},
			},
		},
		lock: new(sync.RWMutex),
	}
	middle := &Chamber{
		Toggles: map[string]*OverrideableToggle{
			"toggle1": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: true,
				},
			},
		},
		lock: new(sync.RWMutex),
	}
	top := &Chamber{
		Toggles: map[string]*OverrideableToggle{
			"toggle1": {
				Toggle: &Toggle{
					Type:  "boolean",
					Value: false,
				},
			},
		},
		lock: new(sync.RWMutex),
	}

	middle.InheritWith(top.Toggles)
	bottom.InheritWith(middle.Toggles)

	v1 := top.Toggles["toggle1"]
	v2 := middle.Toggles["toggle1"]
	v3 := bottom.Toggles["toggle1"]

	// should not inherit top value as is
	if v1 == v2 {
		t.Errorf("middle did not inherit properly from top: value of toggle1 is: %v", v2)
	}

	// should inherit middle value as is
	if v3 != v2 {
		t.Errorf("bottom did not inherit properly from top: value of toggle1 is: %v", v3)
	}
}

func BenchmarkChamberStringValue(b *testing.B) {
	m := make(map[string]*OverrideableToggle, 100000)
	for i := 1; i < 10000; i++ {
		m[strconv.Itoa(i)] = &OverrideableToggle{Toggle: &Toggle{Type: "string", Value: "string"}}
	}

	chamber := NewChamberEntry(&Chamber{
		Toggles: m,
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
	m := make(map[string]*OverrideableToggle, 100000)
	for i := 1; i < 100000; i++ {
		m[strconv.Itoa(i)] = &OverrideableToggle{Toggle: &Toggle{Type: "boolean", Value: false}}
	}
	chamber := NewChamberEntry(&Chamber{
		Toggles: m,
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
	m := make(map[string]*OverrideableToggle, 100000)
	for i := 1; i < 100000; i++ {
		m[strconv.Itoa(i)] = &OverrideableToggle{Toggle: &Toggle{Type: "number", Value: float64(10)}}
	}
	chamber := NewChamberEntry(&Chamber{
		Toggles: m,
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

	m := make(map[string]*OverrideableToggle, 100000)
	for i := 0; i < 100000; i++ {
		raw := json.RawMessage(`{"Test":"test"}`)
		m[strconv.Itoa(i)] = &OverrideableToggle{Toggle: &Toggle{Type: "custom", Value: &raw}}
	}

	chamber := NewChamberEntry(&Chamber{
		Toggles: m,
	}, "")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var v CustomStruct
			err := chamber.CustomValue("1", &v)
			if err != nil {
				b.Errorf("should not be failing benchmark with error: %v", err)
			}
			if i == 99999 {
				i = 0
			}
		}
	})
}

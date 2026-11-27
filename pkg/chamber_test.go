package realm

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
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

// benchChamber returns a chamber of n flags shaped like a real one: each
// type, most with targeting rules (clauses, a rollout, a schedule), some
// with constraints and labels.
func benchChamber(n int) []byte {
	const rules = `[{"id":"beta","clauses":[{"attribute":"tenant","op":"in","values":["t1","t2","t3","t4"]},{"attribute":"version","op":"semverGte","values":["2.3.0"]}],"value":%s,"rollout":{"percentage":25,"bucketBy":"key","seed":"s1"}},` +
		`{"clauses":[{"attribute":"location","op":"startsWith","values":["us-","eu-"]}],"value":%s,"schedule":{"start":"2026-01-01T00:00:00Z","end":"2027-01-01T00:00:00Z"}}]`
	const labels = `"description":"Controls an example behavior for a service","owner":"team-a","lifecycle":"temporary"`
	var b strings.Builder
	b.WriteString(`{"rules":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"flag-%04d":`, i)
		switch i % 4 {
		case 0:
			fmt.Fprintf(&b, `{"type":"boolean","value":false,%s,"targeting":%s}`, labels, fmt.Sprintf(rules, "true", "true"))
		case 1:
			fmt.Fprintf(&b, `{"type":"string","value":"control",%s,"constraints":{"type":"string","enum":["control","a","b"]},"targeting":%s}`, labels, fmt.Sprintf(rules, `"a"`, `"b"`))
		case 2:
			fmt.Fprintf(&b, `{"type":"number","value":10,"constraints":{"type":"number","minimum":0,"maximum":100},"targeting":%s}`, fmt.Sprintf(rules, "50", "75"))
		case 3:
			fmt.Fprintf(&b, `{"type":"custom","value":{"limits":{"rps":100,"burst":20},"regions":["us-east","eu-west"],"enabled":true},%s}`, labels)
		}
	}
	b.WriteString(`}}`)
	return []byte(b.String())
}

// BenchmarkChamberUnmarshal measures the lenient decode SDKs and the
// server's reads make of a chamber.
func BenchmarkChamberUnmarshal(b *testing.B) {
	for _, n := range []int{20, 500} {
		data := benchChamber(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				var c Chamber
				if err := json.Unmarshal(data, &c); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkParseChamber measures the strict decode the server makes of a
// chamber written to it.
func BenchmarkParseChamber(b *testing.B) {
	for _, n := range []int{20, 500} {
		data := benchChamber(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ParseChamber(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
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

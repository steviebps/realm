package realm

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// conformance mirrors sdk/conformance/evaluation.json, the evaluation cases
// every realm SDK must agree on.
type conformance struct {
	Buckets []struct {
		Seed   string  `json:"seed"`
		Key    string  `json:"key"`
		Bucket float64 `json:"bucket"`
	} `json:"buckets"`
	Semver []struct {
		Op      Operator `json:"op"`
		Bound   string   `json:"bound"`
		Version string   `json:"version"`
		Match   bool     `json:"match"`
	} `json:"semver"`
	Cases []struct {
		Name     string            `json:"name"`
		Defaults map[string]string `json:"defaults"`
		Chamber  Chamber           `json:"chamber"`
		Contexts []struct {
			// Now is the RFC 3339 time to evaluate schedules at; when empty,
			// the case has no schedules.
			Now     string `json:"now"`
			Context struct {
				Key        string            `json:"key"`
				Attributes map[string]string `json:"attributes"`
			} `json:"context"`
			Expect map[string]json.RawMessage `json:"expect"`
		} `json:"contexts"`
	} `json:"cases"`
}

func TestConformance(t *testing.T) {
	b, err := os.ReadFile("../sdk/conformance/evaluation.json")
	if err != nil {
		t.Fatal(err)
	}
	var c conformance
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}

	t.Run("buckets", func(t *testing.T) {
		for _, tt := range c.Buckets {
			if got := bucket(tt.Seed, tt.Key); got != tt.Bucket {
				t.Errorf("bucket(%q, %q) = %v, want %v", tt.Seed, tt.Key, got, tt.Bucket)
			}
		}
	})

	t.Run("semver", func(t *testing.T) {
		for _, tt := range c.Semver {
			cl := &Clause{Attribute: VersionAttribute, Op: tt.Op, Values: []string{tt.Bound}}
			if err := cl.validate(); err != nil {
				t.Fatalf("%s %s: %v", tt.Op, tt.Bound, err)
			}
			ec := EvaluationContext{Attributes: map[string]string{VersionAttribute: tt.Version}}
			if got := cl.Matches(ec); got != tt.Match {
				t.Errorf("%q %s %q = %v, want %v", tt.Version, tt.Op, tt.Bound, got, tt.Match)
			}
		}
	})

	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			for i, cc := range tc.Contexts {
				ec := EvaluationContext{Key: cc.Context.Key, Attributes: cc.Context.Attributes}.withDefaults(tc.Defaults)
				if cc.Now != "" {
					at, err := time.Parse(time.RFC3339, cc.Now)
					if err != nil {
						t.Fatalf("context %d: %v", i, err)
					}
					ec.at = at
				}
				got := tc.Chamber.Evaluate(ec)
				if len(got) != len(cc.Expect) {
					t.Errorf("context %d: evaluated %d flags, want %d", i, len(got), len(cc.Expect))
				}
				for key, raw := range cc.Expect {
					var want, have any
					_ = json.Unmarshal(raw, &want)
					value, _ := json.Marshal(got[key].Value)
					_ = json.Unmarshal(value, &have)
					if !reflect.DeepEqual(have, want) {
						t.Errorf("context %d: %s = %s, want %s", i, key, value, raw)
					}
				}
			}
		})
	}
}

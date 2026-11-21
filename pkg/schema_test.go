package realm

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// constraintsConformance mirrors sdk/conformance/constraints.json, the
// constraints cases the web UI checks the same way, with the same words.
type constraintsConformance struct {
	Schemas []struct {
		Name     string          `json:"name"`
		FlagType string          `json:"flagType"`
		Schema   json.RawMessage `json:"schema"`
		Error    string          `json:"error"`
	} `json:"schemas"`
	Values []struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Value  json.RawMessage `json:"value"`
		Error  string          `json:"error"`
	} `json:"values"`
}

func TestConstraintsConformance(t *testing.T) {
	b, err := os.ReadFile("../sdk/conformance/constraints.json")
	if err != nil {
		t.Fatal(err)
	}
	var c constraintsConformance
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	message := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}

	t.Run("schemas", func(t *testing.T) {
		for _, tt := range c.Schemas {
			t.Run(tt.Name, func(t *testing.T) {
				_, err := parseFlagSchema(tt.Schema, tt.FlagType)
				if got := message(err); got != tt.Error {
					t.Fatalf("parse %s for a %s flag: error %q, want %q", tt.Schema, tt.FlagType, got, tt.Error)
				}
			})
		}
	})

	t.Run("values", func(t *testing.T) {
		for _, tt := range c.Values {
			t.Run(tt.Name, func(t *testing.T) {
				s, err := ParseSchema(tt.Schema)
				if err != nil {
					t.Fatalf("parse %s: %v", tt.Schema, err)
				}
				var v any
				if err := json.Unmarshal(tt.Value, &v); err != nil {
					t.Fatal(err)
				}
				err = s.Validate(v)
				if got := message(err); got != tt.Error {
					t.Fatalf("validate %s against %s: error %q, want %q", tt.Value, tt.Schema, got, tt.Error)
				}
				var ve *ValueError
				if err != nil && !errors.As(err, &ve) {
					t.Fatalf("err = %T, want a *ValueError", err)
				}
			})
		}
	})
}

func TestParseChamberChecksConstraints(t *testing.T) {
	tests := []struct {
		name    string
		chamber string
		wantErr string
	}{
		{"metadata", `{"rules":{"a":{"type":"number","value":24,"description":"How long an order is held","owner":"ordering","lifecycle":"permanent"}}}`, ""},
		{"temporary", `{"rules":{"a":{"type":"boolean","value":false,"lifecycle":"temporary"}}}`, ""},
		{"unknown lifecycle", `{"rules":{"a":{"type":"boolean","value":false,"lifecycle":"forever"}}}`, `flag "a": lifecycle must be "temporary" or "permanent", not "forever"`},
		{"misspelled metadata", `{"rules":{"a":{"type":"boolean","value":false,"owners":"x"}}}`, `unknown field "rules.a.owners"`},
		{"within constraints", `{"rules":{"a":{"type":"number","value":24,"constraints":{"minimum":1,"maximum":72},"targeting":[{"value":48}]}}}`, ""},
		{"null constraints", `{"rules":{"a":{"type":"number","value":7200,"constraints":null}}}`, ""},
		{"default out of bounds", `{"rules":{"a":{"type":"number","value":7200,"constraints":{"minimum":1,"maximum":72}}}}`, `flag "a": default value: 7200 is greater than the maximum, 72`},
		{"rule out of bounds", `{"rules":{"a":{"type":"number","value":24,"constraints":{"maximum":72},"targeting":[{"value":48},{"id":"long","value":96}]}}}`, `flag "a": targeting rule long: 96 is greater than the maximum, 72`},
		{"paused rule out of bounds", `{"rules":{"a":{"type":"number","value":24,"constraints":{"maximum":72},"pausedTargeting":[{"value":96}]}}}`, `flag "a": paused targeting rule #1: 96 is greater than the maximum, 72`},
		{"string enum", `{"rules":{"a":{"type":"string","value":"standard","constraints":{"enum":["standard","express"]},"targeting":[{"value":"overnight"}]}}}`, `flag "a": targeting rule #1: "overnight" is not one of the allowed values: "standard", "express"`},
		{"custom", `{"rules":{"a":{"type":"custom","value":{"text":"hi"},"constraints":{"type":"object","required":["text"],"properties":{"text":{"maxLength":3}}},"targeting":[{"value":{"text":"hello"}}]}}}`, `flag "a": targeting rule #1: text: "hello" is longer than the maximum length, 3 (it has 5 characters)`},
		{"invalid constraints", `{"rules":{"a":{"type":"number","value":1,"constraints":{"minimum":"1"}}}}`, `flag "a": constraints: minimum must be a number`},
		{"unknown keyword", `{"rules":{"a":{"type":"string","value":"x","constraints":{"pattern":"x"}}}}`, `flag "a": constraints: unknown keyword "pattern": realm checks only type, enum, minimum, maximum, multipleOf, minLength, maxLength, properties, required, additionalProperties, and items`},
		{"constraints of another type", `{"rules":{"a":{"type":"string","value":"x","constraints":{"minimum":1}}}}`, `flag "a": constraints: minimum does not apply to a string flag`},
		{"first flag in key order", `{"rules":{"b":{"type":"number","value":9,"constraints":{"maximum":1}},"a":{"type":"number","value":9,"constraints":{"maximum":1}}}}`, `flag "a": default value: 9 is greater than the maximum, 1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseChamber([]byte(tt.chamber))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestConstraintsReadLeniently verifies that what writes refuse never stops
// a stored chamber from being read and served: a value outside its
// constraints, constraints this version cannot parse, and a lifecycle it does
// not know. Reads leave them as they are, so a rewrite keeps them.
func TestConstraintsReadLeniently(t *testing.T) {
	stored := `{"rules":{"hold":{"type":"number","value":7200,"description":"Hold time","owner":"ordering","lifecycle":"someday","constraints":{"maximum":72,"exclusiveMinimum":0,"x-future":{"a":[1,2]}}}}}`
	var c Chamber
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := NewChamberEntry(&c, nil).Float64Value("hold", 0)
	if err != nil || got != 7200 {
		t.Fatalf("Float64Value = %v, %v; want 7200", got, err)
	}
	f := c.Rules["hold"]
	if f.Description != "Hold time" || f.Owner != "ordering" || f.Lifecycle != "someday" {
		t.Errorf("metadata = %q, %q, %q", f.Description, f.Owner, f.Lifecycle)
	}
	out, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != stored {
		t.Errorf("round trip:\n got %s\nwant %s", out, stored)
	}
	if _, err := ParseChamber([]byte(stored)); err == nil {
		t.Error("ParseChamber accepted what reads leave alone")
	}
}

func TestFlagParseConstraints(t *testing.T) {
	var f Flag
	if err := json.Unmarshal([]byte(`{"type":"number","value":24,"constraints":{"minimum":1,"maximum":72,"multipleOf":1}}`), &f); err != nil {
		t.Fatal(err)
	}
	s, err := f.ParseConstraints()
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || *s.Minimum != 1 || *s.Maximum != 72 || *s.MultipleOf != 1 {
		t.Fatalf("schema = %+v", s)
	}
	if err := f.CheckValues(s); err != nil {
		t.Errorf("CheckValues: %v", err)
	}

	var none Flag
	if err := json.Unmarshal([]byte(`{"type":"number","value":24}`), &none); err != nil {
		t.Fatal(err)
	}
	if s, err := none.ParseConstraints(); s != nil || err != nil {
		t.Errorf("ParseConstraints without constraints = %v, %v; want nil, nil", s, err)
	}
	if err := none.CheckValues(nil); err != nil {
		t.Errorf("CheckValues(nil) = %v", err)
	}
}

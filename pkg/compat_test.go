package realm

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// futureChamber uses an operator and a flag type that a newer realm might add.
const futureChamber = `{"rules":{
	"plain": {"type":"string","value":"served"},
	"future-op": {"type":"string","value":"default","targeting":[
		{"id":"new","clauses":[{"attribute":"tenantId","op":"matchesFuture","values":["acme"]}],"value":"future"},
		{"clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":"known"}]},
	"future-type": {"type":"futureType","value":{"variant":2},"targeting":[{"value":{"variant":3}}]}
}}`

// TestForwardCompatibleReads verifies that a chamber using a flag type or an
// operator this version does not know still serves its other flags: an
// unknown operator never matches, and a flag of an unknown type returns the
// caller's default with an error.
func TestForwardCompatibleReads(t *testing.T) {
	var c Chamber
	if err := json.Unmarshal([]byte(futureChamber), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	entry := NewChamberEntry(&c, nil)
	acme := EvaluationContext{Attributes: map[string]string{"tenantId": "acme"}}
	globex := EvaluationContext{Attributes: map[string]string{"tenantId": "globex"}}

	tests := []struct {
		name    string
		get     func() (any, error)
		want    any
		wantErr bool
	}{
		{"a plain flag is served", func() (any, error) { return entry.StringValueFor("plain", acme, "fallback") }, "served", false},
		{"an unknown operator falls through to the next rule", func() (any, error) { return entry.StringValueFor("future-op", acme, "fallback") }, "known", false},
		{"an unknown operator never matches", func() (any, error) { return entry.StringValueFor("future-op", globex, "fallback") }, "default", false},
		{"an unknown type as a string", func() (any, error) { return entry.StringValueFor("future-type", acme, "fallback") }, "fallback", true},
		{"an unknown type as a bool", func() (any, error) { return entry.BoolValueFor("future-type", acme, true) }, true, true},
		{"an unknown type as a number", func() (any, error) { return entry.Float64ValueFor("future-type", acme, 1.5) }, 1.5, true},
		{"an unknown type as custom", func() (any, error) {
			var v map[string]any
			return v, entry.CustomValueFor("future-type", acme, &v)
		}, map[string]any(nil), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.get()
			if tt.wantErr {
				var cnc *ErrCouldNotConvertRule
				if !errors.As(err, &cnc) || cnc.Type != "futureType" {
					t.Errorf("err = %v, want ErrCouldNotConvertRule for type futureType", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, tt.want); gotJSON != wantJSON {
				t.Errorf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}

	flags, _ := c.Evaluate(acme)
	if keys := slices.Sorted(maps.Keys(flags)); !slices.Equal(keys, []string{"future-op", "plain"}) {
		t.Errorf("Evaluate returned %v, want the flag of an unknown type omitted", keys)
	}
	if got := c.unsupportedFlags(); !slices.Equal(got, []string{"future-op", "future-type"}) {
		t.Errorf("unsupportedFlags = %v", got)
	}

	allocs := testing.AllocsPerRun(100, func() {
		_, _ = entry.StringValueFor("future-op", acme, "fallback")
	})
	if allocs != 0 {
		t.Errorf("evaluating past an unknown operator allocated %v times", allocs)
	}
}

// TestParseChamberRejectsUnknownTypesAndOperators verifies that writes stay
// strict even though reads are lenient.
func TestParseChamberRejectsUnknownTypesAndOperators(t *testing.T) {
	tests := []struct {
		name    string
		chamber string
		wantErr string
	}{
		{"known", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"clauses":[{"attribute":"a","op":"in","values":["x"]}],"value":false}]}}}`, ""},
		{"unknown operator", futureChamber, `flag "future-op": targeting rule new: clause on "tenantId": unsupported operator "matchesFuture"`},
		{"unknown operator, other values", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"clauses":[{"attribute":"n","op":"between","values":[1,2]}],"value":false}]}}}`, `flag "a": targeting rule #1: clause on "n": unsupported operator "between"`},
		{"unknown type", `{"rules":{"a":{"type":"futureType","value":1}}}`, `flag "a": type "futureType" is currently not supported`},
		{"no type", `{"rules":{"a":{"value":1}}}`, `flag "a": type "" is currently not supported`},
		{"sealed", `{"rules":{"a":{"type":"boolean","value":true,"sealed":true}}}`, ""},
		{"paused targeting", `{"rules":{"a":{"type":"boolean","value":true,"pausedTargeting":[{"clauses":[{"attribute":"a","op":"in","values":["x"]}],"value":false}]}}}`, ""},
		{"unknown field in a paused rule", `{"rules":{"a":{"type":"boolean","value":true,"pausedTargeting":[{"value":false,"schedul":{}}]}}}`, `unknown field "rules.a.pausedTargeting[0].schedul"`},
		{"unknown field in a clause", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"value":false},{"value":false,"clauses":[{"attribute":"a","op":"in","values":["x"]},{"attribute":"a","op":"in","values":["x"],"vals":[]}]}]}}}`, `unknown field "rules.a.targeting[1].clauses[1].vals"`},
		{"unknown field in a rollout", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"value":false,"rollout":{"percentage":5,"bucket":"key"}}]}}}`, `unknown field "rules.a.targeting[0].rollout.bucket"`},
		{"unknown field at the top", `{"rules":{},"rule":{}}`, `unknown field "rule"`},
		{"empty field name", `{"rules":{"a":{"type":"boolean","value":true,"":1}}}`, `unknown field "rules.a."`},
		{"the first of two unknown fields", `{"rules":{"b":{"type":"boolean","value":true,"zz":1},"a":{"type":"boolean","value":true,"aa":1}}}`, `unknown field "rules.b.zz"`},
		{"removed field in another case", `{"rules":{"a":{"type":"boolean","value":true,"Overrides":[]}}}`, `unknown field "rules.a.Overrides"`},
		{"fields in another case", `{"RULES":{"a":{"Type":"boolean","VALUE":true,"Targeting":[{"Value":false,"Clauses":[{"Attribute":"a","OP":"in","Values":["x"]}]}]}}}`, ""},
		{"duplicate field", `{"rules":{"a":{"type":"boolean","value":true,"value":false}}}`, `duplicate field "rules.a.value"`},
		{"duplicate field in another case", `{"rules":{"a":{"type":"boolean","Type":"string","value":"x"}}}`, `duplicate field "rules.a.Type"`},
		{"duplicate rules", `{"rules":{},"RULES":{}}`, `duplicate field "RULES"`},
		{"duplicate flag", `{"rules":{"a":{"type":"boolean","value":true},"b":{"type":"boolean","value":true},"a":{"type":"string","value":"x"}}}`, `duplicate field "rules.a"`},
		{"flags differing in case", `{"rules":{"a":{"type":"boolean","value":true},"A":{"type":"boolean","value":true}}}`, ""},
		{"duplicate schedule", `{"rules":{"a":{"type":"boolean","value":false,"targeting":[{"value":true,"schedule":{"start":"2026-01-01T00:00:00Z"},"schedule":{"end":"2027-01-01T00:00:00Z"}}]}}}`, `duplicate field "rules.a.targeting[0].schedule"`},
		{"duplicate in a schedule", `{"rules":{"a":{"type":"boolean","value":false,"targeting":[{"value":true,"schedule":{"start":"2026-01-01T00:00:00Z","START":"2025-01-01T00:00:00Z"}}]}}}`, `duplicate field "rules.a.targeting[0].schedule.START"`},
		{"duplicate in a clause", `{"rules":{"a":{"type":"boolean","value":false,"pausedTargeting":[{"value":true,"clauses":[{"attribute":"a","op":"in","values":["x"],"op":"notIn"}]}]}}}`, `duplicate field "rules.a.pausedTargeting[0].clauses[0].op"`},
		{"duplicate in a custom value", `{"rules":{"a":{"type":"custom","value":{"list":[{"k":1,"j":2,"k":3}]}}}}`, `duplicate field "rules.a.value.list[0].k"`},
		{"duplicate escaped in a custom value", `{"rules":{"a":{"type":"custom","value":{"x":1,"\u0078":2}}}}`, `duplicate field "rules.a.value.x"`},
		{"custom value names differing in case", `{"rules":{"a":{"type":"custom","value":{"x":1,"X":2}}}}`, ""},
		{"duplicate in a rule's custom value", `{"rules":{"a":{"type":"custom","value":{},"targeting":[{"value":{"x":1,"x":2}}]}}}`, `duplicate field "rules.a.targeting[0].value.x"`},
		{"duplicate in constraints", `{"rules":{"a":{"type":"number","value":1,"constraints":{"maximum":10,"maximum":0}}}}`, `duplicate field "rules.a.constraints.maximum"`},
		{"invalid UTF-8 in a flag key", "{\"rules\":{\"a\xff\":{\"type\":\"boolean\",\"value\":true}}}", ""},
		{"unknown operator in a paused rule", `{"rules":{"a":{"type":"boolean","value":true,"pausedTargeting":[{"id":"p","clauses":[{"attribute":"a","op":"matchesFuture","values":["x"]}],"value":false}]}}}`, `flag "a": paused targeting rule p: clause on "a": unsupported operator "matchesFuture"`},
		{"rules in targeting and pausedTargeting", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"value":false}],"pausedTargeting":[{"value":false}]}}}`, `flag "a" has rules in both targeting and pausedTargeting: its targeting is either on (rules in targeting) or off (rules in pausedTargeting)`},
		{"no rules in pausedTargeting", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"value":false}],"pausedTargeting":[]}}}`, ""},
		{"rule id default", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"id":"default","value":false}]}}}`, `flag "a": targeting rule id "default" is reserved: it names the default value in realm.flag.evaluations`},
		{"rule id of a position", `{"rules":{"a":{"type":"boolean","value":true,"pausedTargeting":[{"id":"#2","value":false}]}}}`, `flag "a": paused targeting rule id "#2" is reserved: ids starting with "#" name rules without one in realm.flag.evaluations`},
		{"rule id containing #", `{"rules":{"a":{"type":"boolean","value":true,"targeting":[{"id":"beta#2","value":false}]}}}`, ""},
	}
	var many strings.Builder
	for i := range 40 {
		fmt.Fprintf(&many, `"f%d":{"type":"boolean","value":true},`, i)
	}
	tests = append(tests,
		struct{ name, chamber, wantErr string }{"many flags", `{"rules":{` + many.String() + `"g":{"type":"boolean","value":true}}}`, ""},
		struct{ name, chamber, wantErr string }{"duplicate among many flags", `{"rules":{` + many.String() + `"f3":{"type":"boolean","value":true}}}`, `duplicate field "rules.f3"`},
	)
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

// TestForwardCompatibleRoundTrip verifies that what this version does not
// know survives decoding and marshaling again unchanged, as a server's PATCH
// does to the stored chamber, so an older server never drops or alters a
// newer one's operators, types, or values.
func TestForwardCompatibleRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		flag string
	}{
		{"unknown operator", `{"type":"boolean","value":false,"targeting":[{"clauses":[{"attribute":"a","op":"matchesFuture","values":["x"]}],"value":true}]}`},
		{"unknown operator with other values and fields", `{"type":"boolean","value":false,"targeting":[{"id":"r","clauses":[{"attribute":"seats","op":"between","values":[1,10],"inclusive":true},{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true}]}`},
		{"unknown operator without values", `{"type":"string","value":"a","targeting":[{"clauses":[{"attribute":"beta","op":"exists"}],"value":"b"}]}`},
		{"unknown type", `{"type":"futureType","value":{"variant":2},"targeting":[{"clauses":[{"attribute":"a","op":"futureOp"}],"value":{"variant":3},"weights":[1,2]}],"description":"a new field"}`},
		{"unknown type without a value", `{"type":"futureType"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := `{"rules":{"f":` + tt.flag + `,"plain":{"type":"string","value":"x"}}}`
			var c Chamber
			if err := json.Unmarshal([]byte(stored), &c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			// a PATCH merges the request's flags into the stored chamber, then marshals it
			c.OverwriteFrom(&Chamber{Rules: map[string]*Flag{"added": {Rule: &Rule{Type: "number", Value: float64(1)}}}})
			out, err := json.Marshal(&c)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var written struct{ Rules map[string]json.RawMessage }
			if err := json.Unmarshal(out, &written); err != nil {
				t.Fatalf("decode what was written: %v", err)
			}
			want := map[string]string{"f": tt.flag, "plain": `{"type":"string","value":"x"}`, "added": `{"type":"number","value":1}`}
			for key, flag := range want {
				if got := string(written.Rules[key]); got != flag {
					t.Errorf("%s was written as %s, want %s", key, got, flag)
				}
			}

			var again Chamber
			if err := json.Unmarshal(out, &again); err != nil {
				t.Fatalf("decode again: %v", err)
			}
			if out2, _ := json.Marshal(&again); string(out2) != string(out) {
				t.Errorf("a second round trip changed the chamber:\n%s\n%s", out, out2)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// TestParseChamberRejectsNullFlags checks that a write refuses a flag written
// as null, which serves nothing, while a plain read keeps decoding one.
func TestParseChamberRejectsNullFlags(t *testing.T) {
	_, err := ParseChamber([]byte(`{"rules":{"b":{"type":"boolean","value":true},"a":null}}`))
	if err == nil || err.Error() != `flag "a" has no definition; leave it out to remove it` {
		t.Errorf("ParseChamber = %v, want the null flag refused", err)
	}
	var c Chamber
	if err := json.Unmarshal([]byte(`{"rules":{"a":null}}`), &c); err != nil {
		t.Errorf("a read of a null flag = %v, want it decoded", err)
	}
}

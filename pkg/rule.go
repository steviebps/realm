package realm

import (
	"encoding/json"
	"fmt"
)

// Rule is a typed value: a type ("string", "number", "boolean", or "custom")
// and a value that is validated against that type. It is the base of a Flag.
//
// A type this version of realm does not know (one a newer realm added) is
// decoded without error and its value kept unread, so that a newer server's
// chamber does not break older SDKs; a flag of such a type cannot be served.
// ParseChamber, which validates writes, rejects it.
type Rule struct {
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

type ruleAlias Rule

func (t *Rule) UnmarshalJSON(b []byte) error {
	var raw json.RawMessage
	alias := ruleAlias{
		Value: &raw,
	}
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}
	*t = Rule(alias)

	if !knownType(t.Type) {
		t.Value = nil
		if len(raw) > 0 {
			t.Value = &raw
		}
		return nil
	}

	if t.Value == nil || len(raw) == 0 {
		return fmt.Errorf("value cannot be empty/nil with type specified as: %q", t.Type)
	}

	if err := t.assertType(raw); err != nil {
		return fmt.Errorf("%q of the specified type %q is incompatible: %w", string(raw), t.Type, err)
	}

	return nil
}

func (t *Rule) assertType(data json.RawMessage) error {
	var err error
	switch t.Type {
	case "string":
		var s string
		if err = json.Unmarshal(data, &s); err != nil {
			return err
		}
		t.Value = s
		return nil
	case "number":
		var n float64
		if err = json.Unmarshal(data, &n); err != nil {
			return err
		}
		t.Value = n
		return nil
	case "boolean":
		var b bool
		if err = json.Unmarshal(data, &b); err != nil {
			return err
		}
		t.Value = b
		return nil
	case "custom":
		// keep value as json.RawMessage for unmarshaling later
		return nil
	}

	return &UnsupportedTypeError{t.Type}
}

// knownType reports whether this version of realm can serve flags of type t.
func knownType(t string) bool {
	switch t {
	case "string", "number", "boolean", "custom":
		return true
	}
	return false
}

type UnsupportedTypeError struct {
	RuleType string
}

func (ut *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("type %q is currently not supported", ut.RuleType)
}

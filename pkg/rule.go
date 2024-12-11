package realm

import (
	"encoding/json"
	"fmt"

	"golang.org/x/mod/semver"
)

// Rule is a feature definition structure for holding
// its name, value, type and any overrides to be parsed by the applicable realm sdk
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

type OverrideableRule struct {
	*Rule
	Overrides []*Override `json:"overrides,omitempty"`
}

type UnsupportedTypeError struct {
	RuleType string
}

func (ut *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("type %q is currently not supported", ut.RuleType)
}

// UnmarshalJSON Custom UnmarshalJSON method for validating rule Value to the RuleType
func (t *OverrideableRule) UnmarshalJSON(b []byte) error {
	var rule Rule
	err := json.Unmarshal(b, &rule)
	if err != nil {
		return err
	}
	t.Rule = &rule

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}

	if v, ok := m["overrides"]; ok {
		var overrides []*Override
		if err := json.Unmarshal(v, &overrides); err != nil {
			return err
		}
		t.Overrides = overrides
	}

	var previous *Override
	for _, override := range t.Overrides {
		// overrides should not overlap
		if previous != nil && semver.Compare(previous.MaximumVersion, override.MinimumVersion) == 1 {
			return fmt.Errorf("an override with maximum version %v is semantically greater than the next override's minimum version (%v) ", previous.MaximumVersion, override.MinimumVersion)
		}
		previous = override
	}

	return nil
}

// ValueAtVersion returns the value at the given version.
// Will return default value if version is empty string or no override is present for the specified version
func (t *OverrideableRule) ValueAtVersion(version string) interface{} {
	v := t.Value
	if version != "" {
		for _, override := range t.Overrides {
			if semver.Compare(override.MinimumVersion, version) <= 0 && semver.Compare(override.MaximumVersion, version) >= 0 {
				v = override.Value
				break
			}
		}
	}

	return v
}

// StringValue retrieves a string value of the rule
// and returns the default value if it does not exist and a bool on whether or not the rule exists
func (t *OverrideableRule) StringValue(version string, defaultValue string) (string, bool) {
	v, ok := t.ValueAtVersion(version).(string)
	if !ok {
		return defaultValue, ok
	}
	return v, ok
}

// BoolValue retrieves a bool value of the rule
// and returns the default value if it does not exist and a bool on whether or not the rule exists
func (t *OverrideableRule) BoolValue(version string, defaultValue bool) (bool, bool) {
	v, ok := t.ValueAtVersion(version).(bool)
	if !ok {
		return defaultValue, ok
	}
	return v, ok
}

// Float64Value retrieves a float64 value of the rule
// and returns the default value if it does not exist and a bool on whether or not the rule exists
func (t *OverrideableRule) Float64Value(version string, defaultValue float64) (float64, bool) {
	v, ok := t.ValueAtVersion(version).(float64)
	if !ok {
		return defaultValue, ok
	}
	return v, ok
}

// CustomValue unmarshals v into the value of the rule
func (t *OverrideableRule) CustomValue(version string, v any) error {
	raw, ok := t.ValueAtVersion(version).(*json.RawMessage)
	if !ok {
		return fmt.Errorf("rule with type %q could not be converted for unmarshalling", t.Type)
	}
	return json.Unmarshal(*raw, v)
}

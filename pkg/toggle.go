package realm

import (
	"encoding/json"
	"fmt"

	"golang.org/x/mod/semver"
)

// Toggle is a feature switch/toggle structure for holding
// its name, value, type and any overrides to be parsed by the applicable realm sdk
type Toggle struct {
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

type toggleAlias Toggle

func (t *Toggle) UnmarshalJSON(b []byte) error {
	var raw json.RawMessage
	alias := toggleAlias{
		Value: &raw,
	}
	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}

	*t = Toggle(alias)
	if err := t.assertType(raw); err != nil {
		return fmt.Errorf("%v of the specified type %q is incompatible: %w", string(raw), t.Type, err)
	}

	return nil
}

type OverrideableToggle struct {
	Toggle
	Overrides []*Override `json:"overrides,omitempty"`
}

type overrideableToggleAlias OverrideableToggle

type UnsupportedTypeError struct {
	ToggleType string
}

func (ut *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("type %q is currently not supported", ut.ToggleType)
}

func (t *Toggle) assertType(data json.RawMessage) error {
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
		return nil
	}

	return &UnsupportedTypeError{t.Type}
}

// UnmarshalJSON Custom UnmarshalJSON method for validating toggle Value to the ToggleType
func (t *OverrideableToggle) UnmarshalJSON(b []byte) error {
	var alias overrideableToggleAlias
	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}
	*t = OverrideableToggle(alias)

	var previous *Override
	for _, override := range t.Overrides {
		if override.Type == "" {
			override.Type = t.Type
		}
		// overrides should not overlap
		if previous != nil && semver.Compare(previous.MaximumVersion, override.MinimumVersion) == 1 {
			return fmt.Errorf("an override with maximum version %v is semantically greater than the next override's minimum version (%v) ", previous.MaximumVersion, override.MinimumVersion)
		}
		previous = override
	}

	return nil
}

// GetValueAt returns the value at the given version.
// Will return default value if version is empty string or no override is present for the specified version
func (t *OverrideableToggle) GetValueAt(version string) interface{} {
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

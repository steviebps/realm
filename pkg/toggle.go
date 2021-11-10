package realm

import (
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/mod/semver"
)

type Toggleable interface {
	IsValidValue(v interface{}) bool
}

var typeMap map[string]Toggleable = map[string]Toggleable{
	"boolean": BooleanToggle{},
	"string":  StringToggle{},
	"number":  NumberToggle{},
}

// Toggle is a feature switch/toggle structure for holding
// its name, value, type and any overrides to be parsed by the applicable realm sdk
type Toggle struct {
	Name  string      `json:"name"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

// IsValidValue determines whether or not the passed value's type matches the ToggleType
func (t *Toggle) IsValidValue(v interface{}) bool {
	toggleable := typeMap[t.Type]
	return toggleable.IsValidValue(v)
}

type OverrideableToggle struct {
	Toggle    *Toggle     `json:"toggle"`
	Overrides []*Override `json:"overrides,omitempty"`
}

type overrideableToggleAlias OverrideableToggle

// UnmarshalJSON Custom UnmarshalJSON method for validating toggle Value to the ToggleType
func (t *OverrideableToggle) UnmarshalJSON(b []byte) error {

	var alias overrideableToggleAlias

	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}
	*t = OverrideableToggle(alias)

	if t.Toggle == nil {
		return errors.New("toggle was not set. please check your config")
	}

	if !t.Toggle.IsValidValue(t.Toggle.Value) {
		return fmt.Errorf("%v (%T) not of the type %q from the toggle: %s", t.Toggle.Value, t.Toggle.Value, t.Toggle.Type, t.Toggle.Name)
	}

	var previous *Override
	for _, override := range t.Overrides {
		// overrides should not overlap
		if previous != nil && semver.Compare(previous.MaximumVersion, override.MinimumVersion) == 1 {
			return fmt.Errorf("an override with maximum version %v is semantically greater than the next override's minimum version (%v) ", previous.MaximumVersion, override.MinimumVersion)
		}

		if !t.Toggle.IsValidValue(override.Value) {
			return fmt.Errorf("%v (%T) not of the type %q from the toggle override: %s", override.Value, override.Value, t.Toggle.Type, t.Toggle.Name)
		}

		previous = override
	}

	return nil
}

// GetValueAt returns the value at the given version.
// Will return default value if version is empty string or no override is present for the specified version
func (t *OverrideableToggle) GetValueAt(version string) interface{} {
	v := t.Toggle.Value
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

package realm

import (
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/mod/semver"
)

// Toggle is a feature switch/toggle structure for holding
// its name, value, type and any overrides to be parsed by the applicable realm sdk
type Toggle struct {
	Name  string      `json:"name"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

type OverrideableToggle struct {
	*Toggle
	Overrides []*Override `json:"overrides,omitempty"`
}

type overrideableToggleAlias OverrideableToggle

func assertType(t string, v interface{}) bool {
	var ok bool
	switch t {
	case "string":
		_, ok = v.(string)
	case "number":
		_, ok = v.(float64)
	case "boolean":
		_, ok = v.(bool)
	}

	return ok
}

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

	if ok := assertType(t.Type, t.Value); !ok {
		return fmt.Errorf("%v (%T) not of the type %q from the toggle: %s, %w", t.Value, t.Value, t.Type, t.Name, err)
	}

	var previous *Override
	for _, override := range t.Overrides {
		// overrides should not overlap
		if previous != nil && semver.Compare(previous.MaximumVersion, override.MinimumVersion) == 1 {
			return fmt.Errorf("an override with maximum version %v is semantically greater than the next override's minimum version (%v) ", previous.MaximumVersion, override.MinimumVersion)
		}

		if ok := assertType(t.Type, t.Value); !ok {
			return fmt.Errorf("%v (%T) not of the type %q from the toggle override: %s", override.Value, override.Value, t.Type, t.Name)
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

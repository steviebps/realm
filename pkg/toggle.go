package realm

import (
	"encoding/json"
	"fmt"
	"reflect"

	"golang.org/x/mod/semver"
)

// Toggle is a feature switch/toggle structure for holding
// its name, value, type and any overrides to be parsed by the applicable realm sdk
type Toggle struct {
	Name       string      `json:"name"`
	ToggleType string      `json:"type"`
	Value      interface{} `json:"value"`
	Overrides  []*Override `json:"overrides,omitempty"`
}

type toggleAlias Toggle

func (t toggleAlias) toToggle() Toggle {
	return Toggle(t)
}

// IsValidValue determines whether or not the passed value's type matches the ToggleType
func (t Toggle) IsValidValue(value interface{}) bool {
	typeOfValue := reflect.TypeOf(value).String()

	switch typeOfValue {
	case "bool":
		return t.ToggleType == "boolean"
	case "string":
		return t.ToggleType == "string"
	case "float64":
		return t.ToggleType == "number"
	default:
		return false
	}
}

// UnmarshalJSON Custom UnmarshalJSON method for validating toggle Value to the ToggleType
func (t *Toggle) UnmarshalJSON(b []byte) error {
	var alias toggleAlias

	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}
	*t = alias.toToggle()

	if !t.IsValidValue(t.Value) {
		return fmt.Errorf("%v (%T) not of the type %q from the toggle: %s", t.Value, t.Value, t.ToggleType, t.Name)
	}

	var previous *Override
	for _, override := range t.Overrides {
		// overrides should not overlap
		if previous != nil && semver.Compare(previous.MaximumVersion, override.MinimumVersion) == 1 {
			return fmt.Errorf("an override with maximum version %v is semantically greater than the next override's minimum version (%v) ", previous.MaximumVersion, override.MinimumVersion)
		}

		if !t.IsValidValue(override.Value) {
			return fmt.Errorf("%v (%T) not of the type %q from the toggle override: %s", override.Value, override.Value, t.ToggleType, t.Name)
		}

		previous = override
	}

	return nil
}

// GetValue returns the value for the given version. Will return default value if no override is present for the specified version
func (t *Toggle) GetValue(version string) interface{} {
	if version != "" {
		if override := t.GetOverride(version); override != nil {
			return override.Value
		}
	}

	return t.Value
}

// GetOverride returns the first override that encapsulates version passed
func (t *Toggle) GetOverride(version string) *Override {

	for _, override := range t.Overrides {
		if semver.Compare(override.MinimumVersion, version) <= 0 && semver.Compare(override.MaximumVersion, version) >= 0 {
			return override
		}
	}

	return nil
}

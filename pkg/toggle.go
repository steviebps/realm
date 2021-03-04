package rein

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"golang.org/x/mod/semver"
)

// Toggle is a feature switch/toggle structure for holding
// its name, value, type and any overrides to be parsed by the applicable rein sdk
type Toggle struct {
	Name       string      `json:"name"`
	ToggleType string      `json:"type"`
	Value      interface{} `json:"value"`
	Overrides  []*Override `json:"overrides"`
}

type toggleAlias Toggle

func (t toggleAlias) toToggle() Toggle {
	return Toggle{
		t.Name,
		t.ToggleType,
		t.Value,
		t.Overrides,
	}
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
		errMsg := fmt.Sprintf("%v (%T) not of the type \"%s\" from the toggle: %s", t.Value, t.Value, t.ToggleType, t.Name)
		return errors.New(errMsg)
	}

	for i := range t.Overrides {
		if !t.IsValidValue(t.Overrides[i].Value) {
			errMsg := fmt.Sprintf("%v (%T) not of the type \"%s\" from the toggle override: %s", t.Overrides[i].Value, t.Overrides[i].Value, t.ToggleType, t.Name)
			return errors.New(errMsg)
		}
	}

	return nil
}

// GetValue returns the value for the given version. Will return default value if no override is present for the specified version
func (t *Toggle) GetValue(version string) interface{} {

	if override := t.GetOverride(version); override != nil {
		return override.Value
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

package rein

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// Toggle is a feature switch/toggle structure for holding
// its name, value, type and any overrides to be parsed by the applicable rein sdk
type Toggle struct {
	Name       string      `json:"name"`
	ToggleType string      `json:"type"`
	Value      interface{} `json:"value"`
	Overrides  []Override  `json:"overrides"`
}

// IsValidValue determines whether or not the passed value's type matches the ToggleType
func (t Toggle) IsValidValue(value interface{}) bool {
	typ := reflect.TypeOf(value).String()

	switch typ {
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

type toggleAlias Toggle

func (t toggleAlias) toToggle() Toggle {
	return Toggle{
		t.Name,
		t.ToggleType,
		t.Value,
		t.Overrides,
	}
}

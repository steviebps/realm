package rein

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// Override is a Toggle value to be consumed by and restricted to a semantic version range
type Override struct {
	VersionRange string      `json:"range"`
	Value        interface{} `json:"value"`
}

// Toggle is a feature switch/toggle structure for holding
// its name, value, type and any overrides to be parsed by the applicable rein sdk
type Toggle struct {
	Name       string      `json:"name"`
	ToggleType string      `json:"type"`
	Value      interface{} `json:"value"`
	Overrides  []Override  `json:"overrides"`
}

// UnmarshalJSON Custom UnmarshalJSON method for validating toggle Value to the ToggleType
func (t *Toggle) UnmarshalJSON(b []byte) error {
	var alias toggleAlias
	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}

	if !isValidType(alias.Value, alias.ToggleType) {
		errMsg := fmt.Sprintf("%v (%T) not of the type %s from the toggle: %s", alias.Value, alias.Value, alias.ToggleType, alias.Name)
		return errors.New(errMsg)
	}

	for i := range alias.Overrides {
		if !isValidType(alias.Overrides[i].Value, alias.ToggleType) {
			errMsg := fmt.Sprintf("%v (%T) not of the type %s from the toggle override: %s", alias.Overrides[i].Value, alias.Overrides[i].Value, alias.ToggleType, alias.Name)
			return errors.New(errMsg)
		}
	}

	*t = alias.toToggle()

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

func isValidType(value interface{}, expected string) bool {
	typ := reflect.TypeOf(value).String()

	switch typ {
	case "bool":
		return expected == "boolean"
	case "string":
		return expected == "string"
	case "float64":
		return expected == "number"
	default:
		return false
	}
}

package rein

import (
	"encoding/json"
	"errors"
	"reflect"
)

type Toggle struct {
	Name       string      `json:"name"`
	ToggleType string      `json:"type"`
	Value      interface{} `json:"value"`
}

func (t *Toggle) UnmarshalJSON(b []byte) error {
	var alias toggleAlias
	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}

	if !isValidType(alias.Value, alias.ToggleType) {
		return errors.New("Toggle is not the right type")
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

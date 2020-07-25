package rein

import (
	"reflect"
)

type Toggle struct {
	Name       string      `json:"name"`
	ToggleType string      `json:"type"`
	Value      interface{} `json:"value"`
}

func (t Toggle) IsValidType() bool {
	typ := reflect.TypeOf(t.Value).String()

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

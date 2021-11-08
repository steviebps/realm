package realm

import "reflect"

type StringToggle struct {
}

func (st StringToggle) IsValidValue(v interface{}) bool {
	t := reflect.TypeOf(v)
	return t.String() == "string"
}

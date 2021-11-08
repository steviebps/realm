package realm

import "reflect"

type BooleanToggle struct {
}

func (bt BooleanToggle) IsValidValue(v interface{}) bool {
	t := reflect.TypeOf(v)
	return t.String() == "bool"
}

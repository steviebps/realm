package realm

import (
	"reflect"
)

type NumberToggle struct {
}

func (nt NumberToggle) IsValidValue(v interface{}) bool {
	t := reflect.TypeOf(v)
	return t.String() == "float64"
}

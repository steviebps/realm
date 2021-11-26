package realm

import (
	"fmt"
	"reflect"
)

type BooleanToggle bool

func (bt BooleanToggle) ValidateValue(v interface{}) error {
	t := reflect.TypeOf(v)
	if t.String() != "bool" {
		return fmt.Errorf("%v is invalid", v)
	}
	return nil
}

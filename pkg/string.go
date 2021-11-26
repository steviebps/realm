package realm

import (
	"fmt"
	"reflect"
)

type StringToggle string

func (st StringToggle) ValidateValue(v interface{}) error {
	t := reflect.TypeOf(v)
	if t.String() != "string" {
		return fmt.Errorf("%v is invalid", v)
	}
	return nil
}

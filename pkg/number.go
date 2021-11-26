package realm

import (
	"fmt"
	"reflect"
)

type NumberToggle float64

func (nt NumberToggle) ValidateValue(v interface{}) error {
	t := reflect.TypeOf(v)
	if t.String() != "float64" {
		return fmt.Errorf("%v is invalid", v)
	}
	return nil
}

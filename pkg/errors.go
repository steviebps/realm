package realm

import (
	"errors"
	"fmt"
)

var (
	ErrChamberEmpty = errors.New("chamber is nil")
)

type ErrToggleNotFound struct {
	Key string
}

func (tnf *ErrToggleNotFound) Error() string {
	return fmt.Sprintf("%v does not exist", tnf.Key)
}

type ErrCouldNotConvertToggle struct {
	Key  string
	Type string
}

func (cnc *ErrCouldNotConvertToggle) Error() string {
	return fmt.Sprintf("%q could not be converted: it is of type %q", cnc.Key, cnc.Type)
}

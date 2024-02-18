package realm

import (
	"errors"
	"fmt"
)

var (
	ErrChamberEmpty = errors.New("chamber is nil")
)

type ErrRuleNotFound struct {
	Key string
}

func (tnf *ErrRuleNotFound) Error() string {
	return fmt.Sprintf("%v does not exist", tnf.Key)
}

type ErrCouldNotConvertRule struct {
	Key  string
	Type string
}

func (cnc *ErrCouldNotConvertRule) Error() string {
	return fmt.Sprintf("%q could not be converted: it is of type %q", cnc.Key, cnc.Type)
}

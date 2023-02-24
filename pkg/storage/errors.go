package storage

import "fmt"

type NotFoundError struct {
	Key string
}

func (nf *NotFoundError) Error() string {
	return fmt.Sprintf("%q does not exist", nf.Key)
}

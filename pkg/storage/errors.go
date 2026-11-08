package storage

import (
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type NotFoundError struct {
	Key string
}

func (nf *NotFoundError) Error() string {
	return fmt.Sprintf("%v does not exist", nf.Key)
}

// ErrInvalidPath is wrapped by the errors for a key or path that storage
// cannot hold: one that references a parent (see ValidatePath), or one a
// backend cannot represent (see KeyValidator), such as a name too long for
// the file backend's file system. It is the caller's mistake, not a failure.
var ErrInvalidPath = errors.New("invalid path")

// ErrTooLarge is wrapped by the errors for a change a backend refuses for its
// size, such as an etcd transaction with more operations than the cluster's
// --max-txn-ops. The backend wrote nothing; the server answers 413.
var ErrTooLarge = errors.New("the change is larger than the storage accepts")

// isNotFoundErr reports whether err reports a missing entry.
func isNotFoundErr(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

// recordError records err on span and marks the span failed, unless err only
// reports a missing entry, which is an ordinary outcome.
func recordError(span trace.Span, err error) {
	span.RecordError(err)
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		span.SetStatus(codes.Error, err.Error())
	}
}

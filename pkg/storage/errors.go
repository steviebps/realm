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

// recordError records err on span and marks the span failed, unless err only
// reports a missing entry, which is an ordinary outcome.
func recordError(span trace.Span, err error) {
	span.RecordError(err)
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		span.SetStatus(codes.Error, err.Error())
	}
}

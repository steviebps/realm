// Package evaluations counts the flag values realm serves, as the
// OpenTelemetry counter realm.flag.evaluations, for the Go SDK (package
// realm) and the server's evaluation endpoint alike. It is internal so that
// only realm adds to the counts.
package evaluations

import (
	"context"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Series is the count of one series of realm.flag.evaluations: the values
// one rule of one flag served at one path.
type Series struct {
	n     atomic.Int64
	attrs attribute.Set
}

// Add counts one value served.
func (s *Series) Add() {
	s.n.Add(1)
}

// key names a Series.
type key struct{ path, flag, rule string }

// series maps the key of every series this process has counted to it (a
// *Series). A series is kept once created, so that its count only grows:
// their number is bounded by the paths, flags, and rules served. Reading it
// takes no lock, so servers counting on every core do not contend.
var series sync.Map

func init() {
	meter := otel.Meter("github.com/steviebps/realm")
	counter, err := meter.Int64ObservableCounter("realm.flag.evaluations",
		metric.WithDescription("Flag values served, by flag, path, and the targeting rule that served them (its id, its position such as #2 when it has none, or default)."),
		metric.WithUnit("{evaluation}"))
	if err != nil {
		return // without a working meter, evaluations simply go unreported
	}
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		series.Range(func(_, v any) bool {
			s := v.(*Series)
			// a rule not yet served is not reported
			if n := s.n.Load(); n > 0 {
				o.ObserveInt64(counter, n, metric.WithAttributeSet(s.attrs))
			}
			return true
		})
		return nil
	}, counter)
}

// Of returns the series of the values rule of flag served at path, creating
// it at zero. path is a canonical chamber path, such as "/prod/web/"; rule is
// the id of the targeting rule, its position ("#2") when it has none, or
// "default". Finding a series that exists allocates nothing.
func Of(path, flag, rule string) *Series {
	if s, ok := series.Load(key{path: path, flag: flag, rule: rule}); ok {
		return s.(*Series)
	}
	return create(path, flag, rule)
}

// create creates the series of rule of flag at path, unless another caller
// just did. It is apart from Of so that Of's key stays off the heap.
func create(path, flag, rule string) *Series {
	s, _ := series.LoadOrStore(key{path: path, flag: flag, rule: rule}, &Series{attrs: attribute.NewSet(
		attribute.String("flag", flag),
		attribute.String("path", path),
		attribute.String("rule", rule),
	)})
	return s.(*Series)
}

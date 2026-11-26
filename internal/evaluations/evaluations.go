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

// DefaultRule is the rule a flag's value is counted under when the flag
// serves its default value: no targeting rule applied, or its targeting is
// turned off.
const DefaultRule = "default"

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

var (
	mu sync.RWMutex
	// series holds every series this process has counted. A series is kept
	// once created, so that its count only grows: their number is bounded
	// by the paths, flags, and rules served.
	series = map[key]*Series{}
)

func init() {
	meter := otel.Meter("github.com/steviebps/realm")
	counter, err := meter.Int64ObservableCounter("realm.flag.evaluations",
		metric.WithDescription("Flag values served, by flag, path, and the targeting rule that served them (its id, its position such as #2 when it has none, or default)."),
		metric.WithUnit("{evaluation}"))
	if err != nil {
		return // without a working meter, evaluations simply go unreported
	}
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		mu.RLock()
		defer mu.RUnlock()
		for _, s := range series {
			// a rule not yet served is not reported
			if n := s.n.Load(); n > 0 {
				o.ObserveInt64(counter, n, metric.WithAttributeSet(s.attrs))
			}
		}
		return nil
	}, counter)
}

// Of returns the series of the values rule of flag served at path, creating
// it at zero. path is a canonical chamber path, such as "/prod/web/"; rule is
// the id of the targeting rule, its position ("#2") when it has none, or
// DefaultRule.
func Of(path, flag, rule string) *Series {
	k := key{path: path, flag: flag, rule: rule}
	mu.RLock()
	s, ok := series[k]
	mu.RUnlock()
	if ok {
		return s
	}
	mu.Lock()
	defer mu.Unlock()
	if s, ok := series[k]; ok {
		return s
	}
	s = &Series{attrs: attribute.NewSet(
		attribute.String("flag", flag),
		attribute.String("path", path),
		attribute.String("rule", rule),
	)}
	series[k] = s
	return s
}

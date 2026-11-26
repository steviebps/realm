// Package evaluationstest reads realm.flag.evaluations in tests, as an
// OpenTelemetry meter provider collects it.
package evaluationstest

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Series names one series of realm.flag.evaluations.
type Series struct{ Flag, Path, Rule string }

var (
	once   sync.Once
	reader *sdkmetric.ManualReader
)

// Collect returns the series of realm.flag.evaluations at path, with their
// counts, as a meter provider collects them. The first call installs that
// provider as the global one. The counts are process-wide, so each test
// counts at a path of its own.
func Collect(t testing.TB, path string) map[Series]int64 {
	t.Helper()
	once.Do(func() {
		reader = sdkmetric.NewManualReader()
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	})
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[Series]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "realm.flag.evaluations" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || !sum.IsMonotonic {
				t.Fatalf("realm.flag.evaluations is a %T, want a monotonic Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				var s Series
				for _, kv := range dp.Attributes.ToSlice() {
					switch kv.Key {
					case "flag":
						s.Flag = kv.Value.AsString()
					case "path":
						s.Path = kv.Value.AsString()
					case "rule":
						s.Rule = kv.Value.AsString()
					default:
						t.Errorf("realm.flag.evaluations has an unexpected attribute %s", kv.Key)
					}
				}
				if s.Path == path {
					got[s] = dp.Value
				}
			}
		}
	}
	return got
}

package server

import (
	"go.opentelemetry.io/otel/metric"
	metrinop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenop "go.opentelemetry.io/otel/trace/noop"
)

// noopInstruments returns providers that discard everything;
// used when the caller does not supply observability primitives.
func noopInstruments() (trace.Tracer, metric.Meter) {
	// Tracer() and Meter() return a single value in otel v1.47,
	// unlike the two-value form used by earlier major versions.
	tracer := tracenop.NewTracerProvider().Tracer("lampa-go")
	meter := metrinop.NewMeterProvider().Meter("lampa-go")
	return tracer, meter
}

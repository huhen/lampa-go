// Package obs wires OpenTelemetry providers for traces, metrics and logs.
// When telemetry is disabled it installs noop providers and a stdout logger.
package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metrinop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenop "go.opentelemetry.io/otel/trace/noop"

	"lampa-go/internal/config"
)

const scopeName = "lampa-go"

// Core holds the runtime observability primitives.
type Core struct {
	Logger *slog.Logger
	Tracer trace.Tracer
	Meter  metric.Meter

	shutdown []func(context.Context) error
}

// Shutdown flushes and stops all providers (reverse registration order).
func (c *Core) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(c.shutdown) - 1; i >= 0; i-- {
		if err := c.shutdown[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Setup initializes the observability stack.
func Setup(ctx context.Context, otelCfg config.OTel, logCfg config.Log) (*Core, error) {
	level := parseLevel(logCfg.Level)
	stdout := stdoutHandler(logCfg.Format, level)
	core := &Core{}

	if !otelCfg.Enable {
		core.Logger = slog.New(stdout)
		core.Tracer = tracenop.NewTracerProvider().Tracer(scopeName)
		core.Meter = metrinop.NewMeterProvider().Meter(scopeName)
		slog.SetDefault(core.Logger)
		return core, nil
	}

	if otelCfg.Endpoint == "" {
		return nil, fmt.Errorf("otel.endpoint is required when otel.enable is true")
	}

	res := newResource(otelCfg.ServiceName)

	traceExp, err := otlptracegrpc.New(ctx,
		append([]otlptracegrpc.Option{otlptracegrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureTrace(otelCfg)...)...)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	core.shutdown = append(core.shutdown, tp.Shutdown)
	core.Tracer = tp.Tracer(scopeName)
	otel.SetTracerProvider(tp)

	metricExp, err := otlpmetricgrpc.New(ctx,
		append([]otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureMetric(otelCfg)...)...)
	if err != nil {
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	core.shutdown = append(core.shutdown, mp.Shutdown)
	core.Meter = mp.Meter(scopeName)
	otel.SetMeterProvider(mp)

	logExp, err := otlploggrpc.New(ctx,
		append([]otlploggrpc.Option{otlploggrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureLog(otelCfg)...)...)
	if err != nil {
		return nil, fmt.Errorf("create log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	core.shutdown = append(core.shutdown, lp.Shutdown)
	core.Logger = slog.New(multiHandler{
		stdout,
		otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(lp)),
	})
	slog.SetDefault(core.Logger)

	return core, nil
}

func newResource(serviceName string) *resource.Resource {
	return resource.NewWithAttributes("", attribute.String("service.name", serviceName))
}

func withInsecureTrace(cfg config.OTel) []otlptracegrpc.Option {
	if cfg.Insecure {
		return []otlptracegrpc.Option{otlptracegrpc.WithInsecure()}
	}
	return nil
}

func withInsecureMetric(cfg config.OTel) []otlpmetricgrpc.Option {
	if cfg.Insecure {
		return []otlpmetricgrpc.Option{otlpmetricgrpc.WithInsecure()}
	}
	return nil
}

func withInsecureLog(cfg config.OTel) []otlploggrpc.Option {
	if cfg.Insecure {
		return []otlploggrpc.Option{otlploggrpc.WithInsecure()}
	}
	return nil
}

func stdoutHandler(format string, level slog.Level) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if format == "json" {
		return slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.NewTextHandler(os.Stdout, opts)
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.TrimSpace(s))); err != nil {
		return slog.LevelInfo
	}
	return l
}

type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}

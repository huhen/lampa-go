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
	"time"

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

const (
	// setupShutdownBudget bounds cleanup of already-created providers when
	// Setup fails partway through.
	setupShutdownBudget = 3 * time.Second
	// providerShutdownBudget bounds each provider shutdown when the caller
	// did not pass a context with a deadline.
	providerShutdownBudget = 5 * time.Second
)

// Core holds the runtime observability primitives.
type Core struct {
	Logger *slog.Logger
	Tracer trace.Tracer
	Meter  metric.Meter

	shutdown []func(context.Context) error
}

// Shutdown flushes and stops all providers (reverse registration order).
// If ctx carries no deadline, each provider gets its own shutdown budget so a
// hanging exporter cannot starve the remaining ones.
func (c *Core) Shutdown(ctx context.Context) error {
	_, hasDeadline := ctx.Deadline()
	var errs []error
	for i := len(c.shutdown) - 1; i >= 0; i-- {
		runCtx := ctx
		if !hasDeadline {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(context.Background(), providerShutdownBudget)
			defer cancel()
		}
		if err := c.shutdown[i](runCtx); err != nil {
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
		return nil, errors.New("otel.endpoint is required when otel.enable is true")
	}

	res, err := newResource(otelCfg.ServiceName)
	if err != nil {
		return nil, fmt.Errorf("create otel resource: %w", err)
	}

	// Create all exporters and providers first; only once every one of them
	// succeeded, wire up shutdowns, expose fields and install globals. On any
	// failure the providers created so far are shut down before returning.

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

	metricExp, err := otlpmetricgrpc.New(ctx,
		append([]otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureMetric(otelCfg)...)...)
	if err != nil {
		return nil, setupFailed(fmt.Errorf("create metric exporter: %w", err), tp.Shutdown)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	logExp, err := otlploggrpc.New(ctx,
		append([]otlploggrpc.Option{otlploggrpc.WithEndpoint(otelCfg.Endpoint)},
			withInsecureLog(otelCfg)...)...)
	if err != nil {
		return nil, setupFailed(fmt.Errorf("create log exporter: %w", err), mp.Shutdown, tp.Shutdown)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)

	core.shutdown = []func(context.Context) error{tp.Shutdown, mp.Shutdown, lp.Shutdown}
	core.Tracer = tp.Tracer(scopeName)
	core.Meter = mp.Meter(scopeName)
	core.Logger = slog.New(multiHandler{
		stdout,
		otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(lp)),
	})

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	// Surface internal exporter errors on stdout only: routing them through
	// core.Logger would feed SDK errors back into the export pipeline.
	otelLogger := slog.New(stdout)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		otelLogger.Error("otel", "error", err)
	}))
	slog.SetDefault(core.Logger)

	return core, nil
}

// setupFailed shuts down the providers created so far with a short budget and
// joins their errors with the cause of the failure.
func setupFailed(cause error, shutdowns ...func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), setupShutdownBudget)
	defer cancel()
	errs := []error{cause}
	for _, fn := range shutdowns {
		if err := fn(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newResource(serviceName string) (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes("", attribute.String("service.name", serviceName)),
	)
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

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type middleware func(http.Handler) http.Handler

// chain applies middlewares so that the first one is the outermost.
func chain(mws ...middleware) middleware {
	return func(next http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			next = mws[i](next)
		}
		return next
	}
}

type requestIDKey struct{}

// statusWriter captures the response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the wrapped writer so streaming handlers (for example
// the cub reverse proxy) keep their periodic flushes working through the
// wrapper; without it http.Flusher assertions would fail and stall streaming.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// requestID assigns an X-Request-ID to every request and response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			var buf [8]byte
			if _, err := rand.Read(buf[:]); err == nil {
				id = hex.EncodeToString(buf[:])
			}
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverMiddleware converts handler panics into 500 responses.
func recoverMiddleware(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic in handler",
						slog.Any("panic", rec),
						slog.String("path", r.URL.Path))
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// observe wires tracing, metrics and request logging into one middleware.
func observe(tracer trace.Tracer, meter metric.Meter, logger *slog.Logger) middleware {
	counter, _ := meter.Int64Counter("http.server.requests")
	duration, _ := meter.Float64Histogram("http.server.duration", metric.WithUnit("ms"))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			// ServeMux stamps the matched pattern onto the request it passes
			// downstream, so keep the reference to read the route afterwards.
			req := r.WithContext(ctx)
			next.ServeHTTP(sw, req)

			route := req.Pattern
			if route != "" {
				span.SetName(r.Method + " " + route)
			}
			span.SetAttributes(attribute.Int("http.response.status_code", sw.status))

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Duration("duration", time.Since(start)),
			}
			if route != "" {
				attrs = append(attrs, slog.String("route", route))
			}
			if id, _ := r.Context().Value(requestIDKey{}).(string); id != "" {
				attrs = append(attrs, slog.String("request_id", id))
			}
			if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.HasTraceID() {
				attrs = append(attrs, slog.String("trace_id", spanCtx.TraceID().String()))
			}
			logger.LogAttrs(ctx, slog.LevelInfo, "http request", attrs...)

			stdAttrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
			}
			if counter != nil {
				counter.Add(ctx, 1, metric.WithAttributes(append(stdAttrs,
					attribute.Int("http.response.status_code", sw.status))...))
			}
			if duration != nil {
				duration.Record(ctx, float64(time.Since(start).Milliseconds()),
					metric.WithAttributes(stdAttrs...))
			}
		})
	}
}

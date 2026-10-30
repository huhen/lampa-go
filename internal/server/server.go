// Package server assembles the HTTP server: middleware chain and routes.
package server

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"lampa-go/internal/api"
	"lampa-go/internal/config"
	"lampa-go/internal/cubproxy"
	"lampa-go/internal/web"
)

// Deps carries everything the server needs to run.
//
// Tracer and Meter are optional: when either is nil, BOTH fall back to noop
// instruments, so a real instrument supplied alongside a nil counterpart is
// discarded — pass both or neither. Logger falls back to slog.Default().
type Deps struct {
	Config config.Config
	Logger *slog.Logger
	Tracer trace.Tracer
	Meter  metric.Meter
	DB     *sql.DB
}

// New builds the fully wired (but not started) HTTP server.
func New(d Deps) (*http.Server, error) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Tracer == nil || d.Meter == nil {
		// Tests may pass only the logger; fall back to noop instruments.
		tracer, meter := noopInstruments()
		d.Tracer, d.Meter = tracer, meter
	}

	upstream, err := url.Parse(d.Config.Cub.Upstream)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	api.Register(mux, api.Deps{
		DB:         d.DB,
		GeoHeader:  d.Config.Cub.GeoHeader,
		GeoDefault: d.Config.Cub.GeoDefault,
		Logger:     d.Logger,
	})

	cub := cubproxy.New(cubproxy.Config{
		Upstream:         upstream,
		Timeout:          d.Config.Cub.Timeout.Std(),
		SubdomainMarkers: d.Config.Cub.SubdomainMarkers,
	}, d.Logger, nil)
	mux.Handle("/cub/{rest...}", cub)

	// /api/* intentionally falls through to the static handler (404) in v1:
	// the path is reserved for the app's own API.
	mux.Handle("/", web.New(d.Config.Server.StaticDir))

	// requestID is outermost so every log line and metric can carry it;
	// recover is innermost so panics become 500s that observe still records.
	handler := chain(
		requestID,
		observe(d.Tracer, d.Meter, d.Logger),
		recoverMiddleware(d.Logger),
	)(mux)

	return &http.Server{
		Addr:              d.Config.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// No WriteTimeout on purpose: proxied streams must not be cut off.
	}, nil
}

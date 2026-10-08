// Package api implements the application's own HTTP endpoints:
// health probes and the local answers for the cub API.
package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
)

// Deps carries the dependencies the API endpoints need.
type Deps struct {
	DB         *sql.DB
	GeoHeader  string
	GeoDefault string
	Logger     *slog.Logger
}

// Register mounts the API endpoints on mux.
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready(d.DB))
	mux.HandleFunc("GET /cub/api/checker", checker)
	mux.HandleFunc("POST /cub/api/checker", checker)
	mux.HandleFunc("GET /cub/api/plugins/blacklist", blacklist)
	mux.HandleFunc("GET /cub/api/metric/{rest...}", metric)
	mux.HandleFunc("GET /cub/api/ad/{rest...}", ads)
	mux.HandleFunc("GET /cub/geo", geo(d.GeoHeader, d.GeoDefault))
	// Subtree variant: the frontend overlay rewrites geo.<our-domain>/ (root
	// path) to /cub/geo/ with a trailing slash. The subtree pattern is more
	// specific than the /cub/{rest...} proxy, so it wins; nothing lives
	// under /cub/geo/.
	mux.HandleFunc("GET /cub/geo/", geo(d.GeoHeader, d.GeoDefault))
}

func writeJSON(w http.ResponseWriter, v any) {
	// Marshal first so the response body has no trailing newline; an
	// encoder would append one and clients compare bodies verbatim.
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encoding error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(data)
}

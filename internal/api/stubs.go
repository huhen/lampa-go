package api

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const maxCheckerBody = 1 << 20

// checker answers the Lampa mirror liveness probe locally:
// GET returns "ok", POST echoes back the first form value.
func checker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		_, _ = w.Write([]byte("ok"))
		return
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/x-www-form-urlencoded" {
		http.Error(w, "error", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCheckerBody))
	if err != nil {
		http.Error(w, "error", http.StatusBadRequest)
		return
	}
	raw := string(body)
	i := strings.IndexByte(raw, '=')
	if i < 0 {
		http.Error(w, "error", http.StatusBadRequest)
		return
	}
	value := raw[i+1:]
	if j := strings.IndexByte(value, '&'); j >= 0 {
		value = value[:j]
	}
	_, _ = w.Write([]byte(value))
}

// blacklist returns an empty plugin blacklist.
func blacklist(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, []any{})
}

// metric accepts client metrics and returns an empty success.
func metric(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"secuses": true})
}

// ads serves empty advertisement payloads so the client shows nothing.
func ads(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("rest") == "vast" {
		now := time.Now()
		writeJSON(w, map[string]any{
			"secuses":       true,
			"ad":            []any{},
			"day_of_month":  now.Day(),
			"days_in_month": 31,
			"month":         int(now.Month()),
		})
		return
	}
	writeJSON(w, map[string]any{"secuses": true})
}

// geo returns the client country code taken from the reverse-proxy header.
func geo(header, fallback string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		country := strings.TrimSpace(r.Header.Get(header))
		if country == "" {
			country = fallback
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(country))
	}
}

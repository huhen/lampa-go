// Package web serves the built Lampa frontend from a static directory.
package web

import (
	"net/http"
	"os"
	"strings"
)

// New returns a handler serving files from dir.
// index.html and assembly.json get no-cache; everything else is cached for a day.
// Requests for "/index.html" are rewritten in place to "/" (mutating r.URL.Path),
// and any other directory path gets a 404 instead of a listing.
func New(dir string) http.Handler {
	files := http.FileServerFS(os.DirFS(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// http.FileServer redirects "/index.html" to "./"; rewrite it to the
		// directory root so the file is served in place with the no-cache
		// header set below instead of a redirect.
		if r.URL.Path == "/index.html" {
			r.URL.Path = "/"
		}

		// Directories are not listed: paths ending in "/" (except the root,
		// which serves index.html) get a 404, matching the reference backend.
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name == "index.html" || name == "assembly.json" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		files.ServeHTTP(w, r)
	})
}

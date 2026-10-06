// Package web serves the built Lampa frontend from a static directory.
package web

import (
	"net/http"
	"os"
	"strings"
)

// New returns a handler serving files from dir.
// index.html and assembly.json get no-cache; everything else is cached for a day.
func New(dir string) http.Handler {
	files := http.FileServerFS(os.DirFS(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name == "index.html" || name == "assembly.json" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		// http.FileServer redirects "/index.html" to "./"; rewrite it to the
		// directory root so the file is served in place with the no-cache
		// header set above instead of a redirect.
		if r.URL.Path == "/index.html" {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

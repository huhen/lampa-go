// Package cubproxy forwards /cub/* requests to the configured upstream service.
package cubproxy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Config configures the proxy.
type Config struct {
	Upstream         *url.URL
	Timeout          time.Duration
	SubdomainMarkers []string
}

// Proxy forwards /cub/<suffix> to <upstream>/<suffix>.
type Proxy struct {
	upstream *url.URL
	markers  map[string]struct{}
	timeout  time.Duration
	rp       *httputil.ReverseProxy
}

// New builds the proxy. transport may be nil (http.DefaultTransport is used).
func New(cfg Config, logger *slog.Logger, transport http.RoundTripper) *Proxy {
	p := &Proxy{
		upstream: cfg.Upstream,
		markers:  make(map[string]struct{}, len(cfg.SubdomainMarkers)),
		timeout:  cfg.Timeout,
	}
	for _, m := range cfg.SubdomainMarkers {
		p.markers[strings.ToLower(m)] = struct{}{}
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	if logger == nil {
		logger = slog.Default()
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:   p.rewrite,
		Transport: headerFilter{next: transport},
		// Stream the body as it arrives (no buffering).
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.ErrorContext(r.Context(), "cub proxy request failed",
				slog.String("path", r.URL.Path), slog.Any("error", err))
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}
	return p
}

// ServeHTTP forwards the request with the configured timeout.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	p.rp.ServeHTTP(w, r.WithContext(ctx))
}

// rewrite maps /cub/<suffix> to <upstream>/<suffix>. A leading marker
// segment (for example "tmdb") selects the upstream subdomain:
// /cub/tmdb/3/movie/1 -> https://tmdb.<upstream-host>/3/movie/1.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	suffix := strings.TrimPrefix(pr.In.URL.Path, "/cub/")
	host := p.upstream.Host
	if i := strings.IndexByte(suffix, '/'); i > 0 {
		if _, ok := p.markers[strings.ToLower(suffix[:i])]; ok {
			host = strings.ToLower(suffix[:i]) + "." + host
			suffix = suffix[i+1:]
		}
	}
	pr.SetXForwarded()
	pr.Out.URL.Scheme = p.upstream.Scheme
	pr.Out.URL.Host = host
	pr.Out.URL.Path = "/" + suffix
	pr.Out.URL.RawPath = ""
	pr.Out.URL.RawQuery = pr.In.URL.RawQuery
	pr.Out.Host = host
}

// filteredHeaderNames are dropped from upstream responses entirely.
var filteredHeaderNames = map[string]struct{}{
	"server":                  {},
	"content-security-policy": {},
	"content-disposition":     {},
}

// filteredHeaderPrefixes are dropped from upstream responses by prefix.
var filteredHeaderPrefixes = []string{"x-", "alt-", "access-control-"}

type headerFilter struct{ next http.RoundTripper }

func (f headerFilter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := f.next.RoundTrip(req)
	if resp == nil {
		return resp, err
	}
	for name := range resp.Header {
		if filteredHeader(name) {
			resp.Header.Del(name)
		}
	}
	return resp, err
}

func filteredHeader(name string) bool {
	lower := strings.ToLower(name)
	if _, ok := filteredHeaderNames[lower]; ok {
		return true
	}
	for _, prefix := range filteredHeaderPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

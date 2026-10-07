package cubproxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newUpstream starts a fake upstream that records the last request
// and answers "upstream:<path>?<query>".
func newUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *url.URL) {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "upstream:"+r.URL.Path+"?"+r.URL.RawQuery)
		}
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return srv, u
}

// newLocalDialTransport dials the upstream host even when the proxy
// rewrites the URL to a marker subdomain: names like "tmdb.127.0.0.1"
// do not resolve through DNS in the test environment, so the dialer
// strips the marker label before connecting.
func newLocalDialTransport(upstream *url.URL) http.RoundTripper {
	upHost := upstream.Hostname()
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if i := strings.IndexByte(host, '.'); i > 0 && host[i+1:] == upHost {
				host = host[i+1:]
			}
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
	}
}

func newProxy(t *testing.T, upstream *url.URL, timeout time.Duration) *Proxy {
	t.Helper()
	return New(Config{
		Upstream:         upstream,
		Timeout:          timeout,
		SubdomainMarkers: []string{"tmdb", "geo", "ws"},
	}, slog.New(slog.DiscardHandler), newLocalDialTransport(upstream))
}

func TestPathPreservingProxy(t *testing.T) {
	var gotPath, gotQuery, gotXFF string
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotXFF = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Forwarded-For")
		_, _ = io.WriteString(w, "upstream:"+r.URL.Path+"?"+r.URL.RawQuery)
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/api/users/get?email=abc", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotPath != "/api/users/get" || gotQuery != "email=abc" {
		t.Errorf("upstream saw %q?%q, want /api/users/get?email=abc", gotPath, gotQuery)
	}
	if gotXFF == "" {
		t.Error("X-Forwarded-For must be set")
	}
	if rec.Body.String() != "upstream:/api/users/get?email=abc" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestSubdomainMarker(t *testing.T) {
	var gotHost, gotPath string
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/tmdb/3/movie/1", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if !strings.HasPrefix(gotHost, "tmdb.") {
		t.Errorf("upstream host = %q, want prefix tmdb.", gotHost)
	}
	if gotPath != "/3/movie/1" {
		t.Errorf("upstream path = %q, want /3/movie/1", gotPath)
	}
}

func TestResponseHeaderFiltering(t *testing.T) {
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "secret")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("X-Trace-Id", "42")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/api/x", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	for _, name := range []string{"Server", "Content-Security-Policy", "X-Trace-Id", "Access-Control-Allow-Origin"} {
		if rec.Header().Get(name) != "" {
			t.Errorf("header %s must be filtered, got %q", name, rec.Header().Get(name))
		}
	}
	if rec.Header().Get("Content-Type") == "" {
		t.Error("Content-Type must be preserved")
	}
}

func TestPostBodyForwarded(t *testing.T) {
	var gotBody string
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, time.Second)

	req := httptest.NewRequest(http.MethodPost, "http://local/cub/api/bookmarks/add",
		strings.NewReader(`{"type":"book"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if gotBody != `{"type":"book"}` {
		t.Errorf("upstream body = %q", gotBody)
	}
}

func TestTimeoutReturns502(t *testing.T) {
	_, upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "ok")
	})
	p := newProxy(t, upstream, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "http://local/cub/api/slow", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

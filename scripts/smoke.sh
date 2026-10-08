#!/usr/bin/env bash
# smoke.sh — boot the server with a temp config and check the key endpoints.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${SMOKE_PORT:-18091}"
UPSTREAM_PORT="${SMOKE_UPSTREAM_PORT:-18092}"
BASE="http://127.0.0.1:${PORT}"
TMP="$(mktemp -d)"
SERVER_PID=""
UPSTREAM_PID=""

cleanup() {
  # SERVER_PID is normally handled (kill+wait) in the main flow; these lines
  # only fire on failure paths where the server is still up.
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  [ -n "$SERVER_PID" ] && wait "$SERVER_PID" 2>/dev/null || true
  [ -n "$UPSTREAM_PID" ] && kill "$UPSTREAM_PID" 2>/dev/null || true
  wait "$UPSTREAM_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

fail() { echo "SMOKE FAIL: $1" >&2; exit 1; }

command -v python3 >/dev/null || fail "python3 is required for the smoke test"

# Pre-flight: a stale listener on the smoke port would make every check hit
# an old server; fail loudly instead of reporting bogus results.
curl -fsS --max-time 1 "$BASE/healthz" >/dev/null 2>&1 && fail "port ${PORT} already in use (stale server?); set SMOKE_PORT"

check() { # check <name> <expected-substr> <url>
  local name="$1" expected="$2" url="$3"
  local body
  body="$(curl -fsS "$url")" || fail "$name: request to $url failed"
  case "$body" in
    *"$expected"*) echo "ok   $name" ;;
    *) fail "$name: expected '$expected' got: $body" ;;
  esac
}

mkdir -p "$TMP/web" "$TMP/data"
printf '<html><body>lampa-smoke</body></html>' > "$TMP/web/index.html"

cat > "$TMP/upstream.py" <<PY
import http.server, socketserver

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = ("upstream:" + self.path).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Server", "fake-upstream")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass

socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", ${UPSTREAM_PORT}), Handler) as srv:
    srv.serve_forever()
PY

cat > "$TMP/config.yaml" <<EOF
server:
  listen: "127.0.0.1:${PORT}"
  static_dir: ${TMP}/web
  base_domain: localhost
cub:
  # "localhost" (not 127.0.0.1): a subdomain marker rewrites the upstream host
  # to e.g. "tmdb.<host>", and "tmdb.localhost" resolves to loopback while
  # "tmdb.127.0.0.1" is not a valid IP literal and never resolves.
  upstream: http://localhost:${UPSTREAM_PORT}
  timeout: 5s
db:
  driver: sqlite
  dsn: ${TMP}/data/smoke.db
otel:
  enable: false
log:
  level: info
  format: text
EOF

python3 -I "$TMP/upstream.py" &
UPSTREAM_PID=$!

"$ROOT/bin/lampa-go" -config "$TMP/config.yaml" >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

ready=0
for _ in $(seq 1 50); do
  if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
done
[ "$ready" = "1" ] || { cat "$TMP/server.log" >&2; fail "server did not start"; }

check "healthz" "ok" "$BASE/healthz"
check "readyz" "ok" "$BASE/readyz"
check "checker" "ok" "$BASE/cub/api/checker"
check "blacklist" "[]" "$BASE/cub/api/plugins/blacklist"
check "metric" "secuses" "$BASE/cub/api/metric/unic"
check "proxy-path" "upstream:/api/users/get" "$BASE/cub/api/users/get"
check "proxy-marker" "upstream:/3/movie/1" "$BASE/cub/tmdb/3/movie/1"
check "static-index" "lampa-smoke" "$BASE/"
check "geo-default" "US" "$BASE/cub/geo"

HDRS="$(curl -sS -D - -o /dev/null "$BASE/cub/api/anything")" || fail "proxy headers: request failed"
# No -f above: the status line must be parsed here, so assert 200 explicitly
# (otherwise a proxy error page could pass the leak check vacuously).
case "$HDRS" in
  HTTP/1.1\ 200*|HTTP/1.0\ 200*) ;;
  *) fail "proxy headers: expected 200" ;;
esac
case "$HDRS" in
  *Server:*fake-upstream*) fail "proxy headers: upstream Server header leaked" ;;
  *) echo "ok   proxy header filtering" ;;
esac

# Graceful shutdown: SIGTERM must stop the server cleanly and be logged.
kill "$SERVER_PID" || fail "graceful shutdown: server not running"
wait "$SERVER_PID" || fail "graceful shutdown: server exited non-zero"
grep -q "shutting down" "$TMP/server.log" || fail "graceful shutdown: 'shutting down' not logged"
echo "ok   graceful shutdown"
SERVER_PID=""

echo "SMOKE OK"

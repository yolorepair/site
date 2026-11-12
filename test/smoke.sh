#!/usr/bin/env bash
# Smoke test for the site image: run it, then check status codes, headers,
# compression and the link-preview tags.
#
#   test/smoke.sh [image]        # default image: yolorepair-site:test
set -euo pipefail

IMAGE="${1:-yolorepair-site:test}"
PORT="${PORT:-18080}"
BASE="http://127.0.0.1:${PORT}"
NAME="yolorepair-site-smoke-$$"
fails=0

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; fails=$((fails + 1)); }

# check <label> <expected> <actual>
check() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (want '$2', got '$3')"; fi; }

status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
# header <name> <curl args...> -> value, lowercased name match, CR stripped
header() {
  local name="$1"; shift
  curl -s -o /dev/null -D - "$@" | tr -d '\r' |
    awk -v n="$name" -F': ' 'tolower($1) == tolower(n) { sub(/^[^:]*: /, ""); print; exit }'
}
has() { if grep -qF -- "$2" <<<"$3"; then pass "$1"; else fail "$1 (missing: $2)"; fi; }

# Same constraints the cluster applies: non-root, no extra capabilities.
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:8080" \
  --cap-drop ALL --security-opt no-new-privileges "$IMAGE" >/dev/null

for _ in $(seq 1 30); do
  [ "$(status "$BASE/healthz" || true)" = "200" ] && break
  sleep 0.5
done

check "container runs as non-root" "101" "$(docker exec "$NAME" id -u)"
check "GET /healthz is 200" "200" "$(status "$BASE/healthz")"
check "GET / is 200" "200" "$(status "$BASE/")"

for f in favicon.svg favicon-32.png apple-touch-icon.png og-image.png; do
  check "GET /$f is 200" "200" "$(status "$BASE/$f")"
done
check "favicon.svg is image/svg+xml" "image/svg+xml" "$(header content-type "$BASE/favicon.svg")"
check "og-image.png is image/png" "image/png" "$(header content-type "$BASE/og-image.png")"

# 404s are real 404s with the branded page, for pages and for assets.
check "GET /nope is 404" "404" "$(status "$BASE/nope")"
check "GET /nope.png is 404" "404" "$(status "$BASE/nope.png")"
has "404 serves the custom page" "OPEN CIRCUIT" "$(curl -s "$BASE/nope")"
check "GET /404.html directly is 404" "404" "$(status "$BASE/404.html")"

# Caching
check "HTML is no-cache" "no-cache" "$(header cache-control "$BASE/")"
check "assets cache for a day" "public, max-age=86400" "$(header cache-control "$BASE/og-image.png")"
check "404 is not cached" "no-cache" "$(header cache-control "$BASE/nope.png")"

# Compression
check "HTML is gzipped" "gzip" "$(header content-encoding -H 'Accept-Encoding: gzip' "$BASE/")"
check "SVG is gzipped" "gzip" "$(header content-encoding -H 'Accept-Encoding: gzip' "$BASE/favicon.svg")"

# Security headers on a page, an asset and the 404.
for path in / /og-image.png /nope; do
  check "nosniff on $path" "nosniff" "$(header x-content-type-options "$BASE$path")"
  check "X-Frame-Options on $path" "DENY" "$(header x-frame-options "$BASE$path")"
  check "Referrer-Policy on $path" "strict-origin-when-cross-origin" "$(header referrer-policy "$BASE$path")"
  has "CSP on $path" "default-src 'self'" "$(header content-security-policy "$BASE$path")"
done
check "no nginx version in Server" "nginx" "$(header server "$BASE/")"
check "no HTTPS redirect from nginx" "200" "$(status -H 'X-Forwarded-Proto: http' "$BASE/")"

# Head tags
html="$(curl -s "$BASE/")"
has "canonical link" '<link rel="canonical" href="https://yolorepair.com/">' "$html"
has "theme-color" '<meta name="theme-color" content="#1E4D9B">' "$html"
has "SVG favicon" '<link rel="icon" href="/favicon.svg" type="image/svg+xml">' "$html"
has "PNG favicon fallback" 'href="/favicon-32.png"' "$html"
has "apple-touch-icon" '<link rel="apple-touch-icon" href="/apple-touch-icon.png">' "$html"
has "og:type" '<meta property="og:type" content="website">' "$html"
has "og:url" '<meta property="og:url" content="https://yolorepair.com/">' "$html"
has "og:image absolute" '<meta property="og:image" content="https://yolorepair.com/og-image.png">' "$html"
has "og:image width" '<meta property="og:image:width" content="1200">' "$html"
has "og:image height" '<meta property="og:image:height" content="630">' "$html"
has "og:title" '<meta property="og:title"' "$html"
has "og:description" '<meta property="og:description"' "$html"
has "twitter:card" '<meta name="twitter:card" content="summary_large_image">' "$html"

echo
if [ "$fails" -gt 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "all checks passed"

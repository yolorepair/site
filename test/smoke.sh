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
# Pages that exist but have no copy yet.
DRAFTS="how-it-works contact"

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
lacks() { if grep -qF -- "$2" <<<"$3"; then fail "$1 (found: $2)"; else pass "$1"; fi; }

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

# Every page: 200 at the slash form, its own canonical, and a relative 301
# from the slashless form (never an absolute one, which would leak :8080).
for page in repairs how-it-works contact; do
  check "GET /$page/ is 200" "200" "$(status "$BASE/$page/")"
  body="$(curl -s "$BASE/$page/")"
  has "/$page/ canonical" "<link rel=\"canonical\" href=\"https://yolorepair.com/$page/\">" "$body"
  has "/$page/ og:url" "<meta property=\"og:url\" content=\"https://yolorepair.com/$page/\">" "$body"
  has "/$page/ has a description" '<meta name="description"' "$body"
  has "/$page/ has the nav" '<nav aria-label="Site">' "$body"
  has "/$page/ marks itself current" "<a href=\"/$page/\" aria-current=\"page\">" "$body"
  check "GET /$page is 301" "301" "$(status "$BASE/$page")"
  check "/$page redirects relatively" "/$page/" "$(header location "$BASE/$page")"
  # Drafts are served, but not for search engines. Take a page out of DRAFTS
  # when its copy lands.
  sitemap="$(curl -s "$BASE/sitemap-0.xml")"
  case " $DRAFTS " in
    *" $page "*)
      has "/$page/ is noindex while a draft" '<meta name="robots" content="noindex">' "$body"
      lacks "/$page/ is not in the sitemap" "yolorepair.com/$page/" "$sitemap" ;;
    *)
      lacks "/$page/ is indexable" 'content="noindex"' "$body"
      has "/$page/ is in the sitemap" "<loc>https://yolorepair.com/$page/</loc>" "$sitemap" ;;
  esac
done

# The facts the copy has to keep straight.
home="$(curl -s "$BASE/")"
repairs="$(curl -s "$BASE/repairs/")"
has "home links to repairs" '<a href="/repairs/">' "$home"
has "home says quote first" "before any work starts" "$home"
has "repairs says quote first" "before any work starts" "$repairs"
has "repairs lists phones as not taken" "PHONES AND TABLETS" "$repairs"
for page in "$home" "$repairs"; do
  lacks "no price printed" '$' "$page"
  lacks "no warranty promised" "arranty" "$page"
  lacks "no phone number" "tel:" "$page"
done

# Sitemap and robots.txt
check "GET /robots.txt is 200" "200" "$(status "$BASE/robots.txt")"
has "robots.txt points at the sitemap" "Sitemap: https://yolorepair.com/sitemap-index.xml" "$(curl -s "$BASE/robots.txt")"
check "GET /sitemap-index.xml is 200" "200" "$(status "$BASE/sitemap-index.xml")"
has "sitemap index lists the sitemap" "<loc>https://yolorepair.com/sitemap-0.xml</loc>" "$(curl -s "$BASE/sitemap-index.xml")"
has "sitemap lists the home page" "<loc>https://yolorepair.com/</loc>" "$(curl -s "$BASE/sitemap-0.xml")"
lacks "home page is indexable" 'content="noindex"' "$(curl -s "$BASE/")"
has "404 is noindex" '<meta name="robots" content="noindex">' "$(curl -s "$BASE/nope")"
lacks "404 has no canonical" 'rel="canonical"' "$(curl -s "$BASE/nope")"

# 404s are real 404s with the branded page, for pages and for assets.
check "GET /nope is 404" "404" "$(status "$BASE/nope")"
check "GET /nope.png is 404" "404" "$(status "$BASE/nope.png")"
has "404 serves the custom page" "OPEN CIRCUIT" "$(curl -s "$BASE/nope")"
check "GET /404.html directly is 404" "404" "$(status "$BASE/404.html")"

# Caching
check "HTML is no-cache" "no-cache" "$(header cache-control "$BASE/")"
check "assets cache for a day" "public, max-age=86400" "$(header cache-control "$BASE/og-image.png")"
check "404 is not cached" "no-cache" "$(header cache-control "$BASE/nope.png")"

# Built assets: the stylesheets the page links and the fonts those load, all
# under /_astro/ with hashed names.
html="$(curl -s "$BASE/")"
css_paths="$(grep -oE '/_astro/[^"]+\.css' <<<"$html" | sort -u)"
has "page links a built stylesheet" "/_astro/" "$css_paths"
css=""
for p in $css_paths; do
  check "GET $p is 200" "200" "$(status "$BASE$p")"
  check "$p is text/css" "text/css" "$(header content-type "$BASE$p")"
  check "$p is immutable" "public, max-age=31536000, immutable" "$(header cache-control "$BASE$p")"
  css+="$(curl -s "$BASE$p")"
done
font_paths="$(grep -oE '/_astro/[^)"]+\.woff2' <<<"$css" | sort -u)"
check "stylesheets load four font files" "4" "$(grep -c . <<<"$font_paths")"
for p in $font_paths; do
  check "GET $p is 200" "200" "$(status "$BASE$p")"
  check "$p is font/woff2" "font/woff2" "$(header content-type "$BASE$p")"
  check "$p is immutable" "public, max-age=31536000, immutable" "$(header cache-control "$BASE$p")"
done
check "missing /_astro/ file is 404" "404" "$(status "$BASE/_astro/nope.css")"
check "missing /_astro/ file is not cached" "no-cache" "$(header cache-control "$BASE/_astro/nope.css")"

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

# Nothing loads from a third party, and no inline styles.
csp="$(header content-security-policy "$BASE/")"
has "CSP style-src is self only" "style-src 'self';" "$csp"
has "CSP font-src is self only" "font-src 'self';" "$csp"
lacks "CSP has no unsafe-inline" "unsafe-inline" "$csp"
lacks "CSP has no Google hosts" "google" "$csp"
lacks "CSP has no gstatic" "gstatic" "$csp"
for path in / /repairs/ /how-it-works/ /contact/ /nope; do
  page="$(curl -s "$BASE$path")"
  lacks "no Google Fonts in $path" "fonts.googleapis.com" "$page"
  lacks "no gstatic in $path" "fonts.gstatic.com" "$page"
  lacks "no inline <style> in $path" "<style" "$page"
  lacks "no style attribute in $path" ' style="' "$page"
done
lacks "no third-party URL in the stylesheets" "url(http" "$css"
check "no nginx version in Server" "nginx" "$(header server "$BASE/")"
check "no HTTPS redirect from nginx" "200" "$(status -H 'X-Forwarded-Proto: http' "$BASE/")"

# Head tags
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

# Body
has "links back to mrcupp.com" '<a href="https://mrcupp.com/">mrcupp.com</a>' "$html"

echo
if [ "$fails" -gt 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "all checks passed"

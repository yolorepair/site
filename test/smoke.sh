#!/usr/bin/env bash
# Smoke test for the site image: run it, then check status codes, headers,
# compression and the link-preview tags. The last section starts the image a
# few more times to check the repair-request form's path to the relay.
#
#   test/smoke.sh [image]        # default image: yolorepair-site:test
#
# A registry image (anything with a "/" in its name, like
# ghcr.io/yolorepair/site:latest) is pulled first. Docker would otherwise run
# whatever copy of that tag it already has, which can be days old. A local tag
# is run as it is. Set SMOKE_NO_PULL=1 to skip the pull.
set -euo pipefail

IMAGE="${1:-yolorepair-site:test}"
PORT="${PORT:-18080}"
BASE="http://127.0.0.1:${PORT}"
NAME="yolorepair-site-smoke-$$"
fails=0
# Pages that exist but have no copy yet.
DRAFTS=""

cleanup() {
  docker rm -f "$NAME" "$NAME-noresolve" "$NAME-badurl" "$NAME-proxy" "$NAME-relay" >/dev/null 2>&1 || true
  docker network rm "$NAME-net" >/dev/null 2>&1 || true
}
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

case "$IMAGE" in
  */*)
    if [ "${SMOKE_NO_PULL:-0}" = "1" ]; then
      echo "note: SMOKE_NO_PULL=1, testing the local copy of $IMAGE, which may be stale"
    elif ! docker pull -q "$IMAGE" >/dev/null; then
      # No fallback to a cached copy: passing or failing against an old image
      # says nothing about the published one.
      echo "error: could not pull $IMAGE (not logged in to the registry?)" >&2
      echo "       log in and retry, or set SMOKE_NO_PULL=1 to test the local copy" >&2
      exit 2
    fi ;;
esac
echo "image: $IMAGE ($(docker image inspect "$IMAGE" --format '{{.Id}}, created {{.Created}}' 2>/dev/null || echo 'not found locally'))"

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
for page in repairs how-it-works request contact; do
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
has "home lists power amps" "POWER AMPS" "$home"
has "repairs has a power amps section" "<h2>Power amps</h2>" "$repairs"
has "repairs lists phones as not taken" "PHONES AND TABLETS" "$repairs"
how="$(curl -s "$BASE/how-it-works/")"
contact="$(curl -s "$BASE/contact/")"
has "how-it-works says quote first" "before any work starts" "$how"
has "how-it-works says drop-off is by appointment" "by appointment" "$how"
has "contact has the email" 'href="mailto:hello@yolorepair.com"' "$contact"
has "contact has Instagram" 'href="https://instagram.com/yolo.repair"' "$contact"
has "contact says drop-off is by appointment" "by appointment" "$contact"
for page in "$home" "$repairs" "$how" "$contact"; do
  lacks "no price printed" '$' "$page"
  lacks "no warranty promised" "arranty" "$page"
  lacks "no phone number" "tel:" "$page"
done

# The repair-request pages. The form is a listed page, checked with the rest
# above. Its thanks and problem pages are served but not to be found: noindex
# and out of the sitemap.
sitemap="$(curl -s "$BASE/sitemap-0.xml")"
for page in request request/thanks request/problem; do
  check "GET /$page/ is 200" "200" "$(status "$BASE/$page/")"
  body="$(curl -s "$BASE/$page/")"
  if [ "$page" != request ]; then
    has "/$page/ is noindex" '<meta name="robots" content="noindex">' "$body"
    lacks "/$page/ is not in the sitemap" "yolorepair.com/$page/" "$sitemap"
  fi
  has "/$page/ has the nav" '<nav aria-label="Site">' "$body"
  lacks "/$page/ has no script" "<script" "$body"
  lacks "/$page/ has no phone number" "tel:" "$body"
  lacks "/$page/ prints no price" '$' "$body"
  lacks "/$page/ promises no warranty" "arranty" "$body"
done
for path in / /repairs/ /how-it-works/ /contact/; do
  page="$(curl -s "$BASE$path")"
  has "$path links to the form in its copy" '<a href="/request/">' "${page#*</header>}"
  has "$path keeps the email as the fallback" 'href="mailto:hello@yolorepair.com"' "$page"
done
has "the nav links to the form" '<a href="/request/">Request a repair</a>' "$(curl -s "$BASE/" | sed 's/<\/header>.*//')"
has "the form says where what you type goes" 'class="hint privacy"' "$(curl -s "$BASE/request/")"

# The form. Names and caps are intake's contract (the relay's copy is
# relay/testdata/request-contract.json); the relay drops anything that fills
# in "website".
form="$(curl -s "$BASE/request/")"
has "form posts to /request/submit" '<form class="request" method="post" action="/request/submit">' "$form"
has "form has the name field" 'name="customer_name" type="text" required maxlength="120"' "$form"
has "form requires an email" 'name="email" type="email" required maxlength="254"' "$form"
has "form has the device field" 'name="device" type="text" required maxlength="200"' "$form"
has "form has the symptoms field" 'name="symptoms" required maxlength="4000"' "$form"
has "form offers drop-off" 'name="handoff" value="drop_off" required' "$form"
has "form offers mail-in" 'name="handoff" value="mail_in" required' "$form"
has "form has the return address" 'name="return_address" maxlength="500"' "$form"
has "form has the honeypot" 'name="website" type="text" tabindex="-1" autocomplete="off"' "$form"
lacks "form has no phone field" 'name="phone"' "$form"
has "problem page has the email" 'href="mailto:hello@yolorepair.com"' "$(curl -s "$BASE/request/problem/")"
has "problem page links back to the form" '<a href="/request/">' "$(curl -s "$BASE/request/problem/")"

# This container has no RELAY_URL, the way the image runs until the relay is
# deployed. The form has nowhere to go, so it lands on the problem page.
GOOD_FORM='customer_name=Sam+Example&email=sam%40example.com&device=Game+Boy&symptoms=No+sound&handoff=drop_off'
# post <base> [curl args...] -> "status location" for a posted form
post() { local base="$1"; shift; curl -s -o /dev/null -w '%{http_code} %header{location}' -d "$GOOD_FORM" "$@" "$base/request/submit"; }
check "no RELAY_URL: a posted form goes to the problem page" "303 /request/problem/" "$(post "$BASE")"
check "no RELAY_URL: GET /request/submit goes back to the form" "303 /request/" \
  "$(curl -s -o /dev/null -w '%{http_code} %header{location}' "$BASE/request/submit")"
check "the submit path isn't cached" "no-store" "$(header cache-control -d "$GOOD_FORM" "$BASE/request/submit")"
has "CSP on the submit path" "default-src 'self'" "$(header content-security-policy -d "$GOOD_FORM" "$BASE/request/submit")"
has "start-up log says there's no relay" "RELAY_URL isn't set" "$(docker logs "$NAME" 2>&1)"

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
# "|| true": grep finding nothing is a failed check below, not a reason for the
# script to stop here without a summary.
css_paths="$(grep -oE '/_astro/[^"]+\.css' <<<"$html" | sort -u || true)"
has "page links a built stylesheet" "/_astro/" "$css_paths"
css=""
for p in $css_paths; do
  check "GET $p is 200" "200" "$(status "$BASE$p")"
  check "$p is text/css" "text/css" "$(header content-type "$BASE$p")"
  check "$p is immutable" "public, max-age=31536000, immutable" "$(header cache-control "$BASE$p")"
  css+="$(curl -s "$BASE$p")"
done
font_paths="$(grep -oE '/_astro/[^)"]+\.woff2' <<<"$css" | sort -u || true)"
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
for path in / /repairs/ /how-it-works/ /contact/ /request/ /request/thanks/ /request/problem/ /nope; do
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

# The form's path to the relay. The rule: the form may break, the site may
# not. Each case below starts the image again with a different RELAY_URL.

# start <suffix> <docker run args...> -> starts a container, prints its base URL
start() {
  local name="$NAME-$1"; shift
  docker run -d --name "$name" -p 127.0.0.1::8080 --cap-drop ALL --security-opt no-new-privileges "$@" >/dev/null
  local port; port="$(docker port "$name" 8080/tcp | head -1 | sed 's/.*://')"
  for _ in $(seq 1 30); do
    [ "$(status "http://127.0.0.1:$port/healthz" || true)" = "200" ] && break
    sleep 0.5
  done
  echo "http://127.0.0.1:$port"
}

# A relay whose name doesn't resolve: not deployed yet, or a typo. nginx would
# refuse to start if it looked the name up at start.
base="$(start noresolve -e RELAY_URL=http://relay.invalid:8080 "$IMAGE")"
check "unresolvable relay: the site still serves" "200" "$(status "$base/")"
check "unresolvable relay: a posted form goes to the problem page" "303 /request/problem/" "$(post "$base")"

# A RELAY_URL that would break nginx's config if it were written into it.
base="$(start badurl -e 'RELAY_URL=http://relay:8080/; } bogus {' "$IMAGE")"
check "unusable RELAY_URL: the site still serves" "200" "$(status "$base/")"
check "unusable RELAY_URL: a posted form goes to the problem page" "303 /request/problem/" "$(post "$base")"
has "unusable RELAY_URL: the start-up log says so" "isn't a plain http://host:port address" "$(docker logs "$NAME-badurl" 2>&1)"

# A relay that answers. This one is a stand-in: nginx from the same image,
# saying what the real relay says to a good form and echoing back what it was
# sent. It redirects with a full URL (http://relay:8080/...), which the real
# relay doesn't, so this also shows that name never reaches the browser. The
# real relay is tested against the real site in the end-to-end job.
docker network create "$NAME-net" >/dev/null
docker run -d --name "$NAME-relay" --network "$NAME-net" --network-alias relay --entrypoint sh "$IMAGE" -c '
  cat >/tmp/stand-in.conf <<CONF
pid /tmp/stand-in.pid;
events {}
http {
  server {
    listen 8080;
    location = /submit {
      add_header X-Saw-Method \$request_method always;
      add_header X-Saw-Client \$http_cf_connecting_ip always;
      add_header X-Saw-Type \$content_type always;
      return 303 /request/thanks/;
    }
    location = /submit-broken { return 500; }
  }
}
CONF
  exec nginx -c /tmp/stand-in.conf -g "daemon off;"' >/dev/null
base="$(start proxy --network "$NAME-net" -e RELAY_URL=http://relay:8080/ "$IMAGE")"
has "start-up log names the relay" "/request/submit goes to http://relay:8080" "$(docker logs "$NAME-proxy" 2>&1)"
check "relay up: a posted form goes where the relay says" "303 /request/thanks/" "$(post "$base")"
answer="$(curl -s -o /dev/null -D - -d "$GOOD_FORM" -H 'CF-Connecting-IP: 203.0.113.7' "$base/request/submit" | tr -d '\r')"
has "relay is sent a POST" "X-Saw-Method: POST" "$answer"
has "relay is sent the visitor's address" "X-Saw-Client: 203.0.113.7" "$answer"
has "relay is sent a form" "X-Saw-Type: application/x-www-form-urlencoded" "$answer"
has "relay's answer isn't cached" "Cache-Control: no-store" "$answer"
has "relay's answer carries the CSP" "Content-Security-Policy: default-src 'self'" "$answer"
check "relay up: GET /request/submit goes back to the form" "303 /request/" \
  "$(curl -s -o /dev/null -w '%{http_code} %header{location}' "$base/request/submit")"
check "only /request/submit is proxied" "404" "$(status -d "$GOOD_FORM" "$base/request/submit-broken")"
# 64k is the cap, the same as the relay's own.
big="$(head -c 70000 /dev/zero | tr '\0' 'a')"
check "a form over 64k goes to the problem page" "303 /request/problem/" \
  "$(curl -s -o /dev/null -w '%{http_code} %header{location}' -d "symptoms=$big" "$base/request/submit")"
check "a 60k form reaches the relay" "303 /request/thanks/" \
  "$(curl -s -o /dev/null -w '%{http_code} %header{location}' -d "symptoms=${big:0:60000}" "$base/request/submit")"

# The relay goes away while the site is running.
docker rm -f "$NAME-relay" >/dev/null
check "relay down: a posted form goes to the problem page" "303 /request/problem/" "$(post "$base")"
check "relay down: the site still serves" "200" "$(status "$base/")"
check "relay down: the form page still serves" "200" "$(status "$base/request/")"

echo
if [ "$fails" -gt 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "all checks passed"

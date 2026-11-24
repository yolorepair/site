#!/bin/sh
# Runs at container start, after 40-relay-location.sh. Writes the location
# behind the request form's address suggestions: GET /request/address?q=...,
# handed to the Photon geocoder that PHOTON_URL names.
#
#   PHOTON_URL=http://photon.photon.svc.cluster.local:2322
#
# Same rule as the relay: suggestions may break, the site may not. With
# PHOTON_URL unset, unusable, or Photon down, the path answers with an empty
# list and the form's boxes are plain text fields. Same price, too: the name
# is resolved per request by nginx's own resolver, which ignores search
# domains, so in Kubernetes it has to be the full …svc.cluster.local name.
#
# Only q reaches Photon. Everything else the browser sends is dropped and the
# rest of the query is fixed here, so this path can't be used to ask Photon
# anything but "addresses like this, near Sacramento, five at most".
set -u

out=/tmp/address-location.conf
say() { echo "41-address-location.sh: $*"; }

# The answer when there's nothing to suggest. Photon's own shape, empty.
EMPTY='{"type":"FeatureCollection","features":[]}'

no_photon() {
  say "$1; /request/address will answer with no suggestions"
  cat >"$out" <<CONF
# Written at start by 41-address-location.sh. No geocoder to ask.
location = /request/address {
    include /etc/nginx/snippets/security-headers.conf;
    add_header Cache-Control "no-store" always;
    default_type application/json;
    access_log off;
    return 200 '$EMPTY';
}
CONF
}

with_photon() {
  resolvers="$(awk '$1 == "nameserver" { printf "%s ", ($2 ~ /:/) ? "[" $2 "]" : $2 }' /etc/resolv.conf 2>/dev/null)"
  resolver_line=""
  [ -n "$resolvers" ] && resolver_line="resolver ${resolvers}valid=30s;"
  cat >"$out" <<CONF
# Written at start by 41-address-location.sh from PHOTON_URL.
location = /request/address {
    include /etc/nginx/snippets/security-headers.conf;
    add_header Cache-Control "no-store" always;
    default_type application/json;
    # What someone types is part of their address. It stays out of the logs,
    # the same way the relay keeps form contents out of its own. nginx puts
    # the request line in error messages too (a rate limit hit, Photon down),
    # so only critical errors are logged here.
    access_log off;
    error_log /dev/stderr crit;
    limit_req_log_level info;

    limit_except GET { deny all; }

    # Per visitor and for everyone together (zones in default.conf). A
    # person typing makes a request every quarter second at most.
    limit_req zone=address_visitor burst=20 nodelay;
    limit_req zone=address_all burst=60 nodelay;
    limit_req_status 429;

    # No q, or a q longer than any street line, gets nothing. \$arg_q is
    # still percent-encoded here, so it can't carry a raw & or another
    # parameter into the query below.
    if (\$arg_q = "") { return 200 '$EMPTY'; }
    if (\$arg_q ~ "^.{301,}") { return 200 '$EMPTY'; }

    ${resolver_line}
    resolver_timeout 2s;
    set \$photon "$1";
    # Houses and streets, five at most, biased toward the shop.
    proxy_pass \$photon/api?q=\$arg_q&limit=5&lang=en&layer=house&layer=street&lat=38.58&lon=-121.53;

    # Nothing about the visitor goes to Photon.
    proxy_set_header Cookie "";
    proxy_set_header Referer "";
    proxy_set_header User-Agent "";
    proxy_hide_header Set-Cookie;

    proxy_connect_timeout 1s;
    proxy_send_timeout 3s;
    proxy_read_timeout 3s;

    # Photon down, not ready, or confused: no suggestions, never an error.
    proxy_intercept_errors on;
    error_page 400 404 405 408 500 502 503 504 = @address_empty;
}

location @address_empty {
    access_log off;
    error_log /dev/stderr crit;
    include /etc/nginx/snippets/security-headers.conf;
    add_header Cache-Control "no-store" always;
    default_type application/json;
    return 200 '$EMPTY';
}
CONF
}

url="${PHOTON_URL:-}"
url="${url%/}"

if [ -z "$url" ]; then
  no_photon "PHOTON_URL isn't set"
  exit 0
fi

case "$url" in
  *[!A-Za-z0-9.:/-]*) url="" ;;
esac
if ! printf '%s' "$url" | grep -Eq '^https?://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$'; then
  no_photon "PHOTON_URL isn't a plain http://host:port address"
  exit 0
fi

with_photon "$url"
if ! nginx -t -q 2>/dev/null; then
  no_photon "nginx turned down the config written for PHOTON_URL"
  exit 0
fi
say "/request/address goes to $url"
exit 0

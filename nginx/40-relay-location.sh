#!/bin/sh
# Runs at container start, before nginx (the image runs everything in
# /docker-entrypoint.d/). Writes the one location that leaves the pod:
# POST /request/submit, handed to the relay that RELAY_URL names.
#
#   RELAY_URL=http://site-relay.shop.svc.cluster.local:8080
#
# The rule here: the form is allowed to break, the site is not. So this never
# stops nginx from starting. With RELAY_URL unset, or set to something nginx
# won't take, the path answers with a redirect to /request/problem/, which
# tells people to email.
#
# The relay's name is looked up per request, not at start. nginx refuses to
# start when a proxy_pass hostname doesn't resolve, and a relay that isn't
# deployed yet would take the whole site down with it. The price: nginx's own
# resolver ignores the search domains in /etc/resolv.conf, so in Kubernetes
# RELAY_URL needs the full service name (…svc.cluster.local), not a short one.
set -u

out=/tmp/relay-location.conf
say() { echo "40-relay-location.sh: $*"; }

# Both versions send anything that isn't a POST back to the form, and neither
# lets the answer be cached.
no_relay() {
  say "$1; /request/submit will answer with the problem page"
  cat >"$out" <<'CONF'
# Written at start by 40-relay-location.sh. No relay to send to.
location = /request/submit {
    include /etc/nginx/snippets/security-headers.conf;
    add_header Cache-Control "no-store" always;
    if ($request_method != POST) { return 303 /request/; }
    return 303 /request/problem/;
}
CONF
}

with_relay() {
  # nginx wants IPv6 resolver addresses in brackets.
  resolvers="$(awk '$1 == "nameserver" { printf "%s ", ($2 ~ /:/) ? "[" $2 "]" : $2 }' /etc/resolv.conf 2>/dev/null)"
  resolver_line=""
  # No nameserver is fine when RELAY_URL is an IP address. With a hostname,
  # every request fails over to the problem page.
  [ -n "$resolvers" ] && resolver_line="resolver ${resolvers}valid=30s;"
  cat >"$out" <<CONF
# Written at start by 40-relay-location.sh from RELAY_URL.
location = /request/submit {
    include /etc/nginx/snippets/security-headers.conf;
    add_header Cache-Control "no-store" always;
    if (\$request_method != POST) { return 303 /request/; }

    # A form, not an upload. Same cap as the relay's own.
    client_max_body_size 64k;

    # A variable makes nginx resolve the name per request.
    ${resolver_line}
    resolver_timeout 3s;
    set \$relay "$1";
    proxy_pass \$relay/submit;
    # The relay redirects to a path. If it ever sends a full URL, keep the
    # path and drop the rest, so the relay's in-cluster name can't reach a
    # browser.
    proxy_redirect ~^https?://[^/]+(/.*)\$ \$1;

    # The relay rate-limits on this. Cloudflare sets it; nothing else can
    # reach this server from outside.
    proxy_set_header CF-Connecting-IP \$http_cf_connecting_ip;

    proxy_connect_timeout 3s;
    proxy_send_timeout 10s;
    proxy_read_timeout 20s;

    # The relay only ever answers a POST with a redirect. Anything else, and
    # the relay being down, ends on the problem page.
    proxy_intercept_errors on;
    error_page 400 404 405 408 413 500 502 503 504 = @request_problem;
}

location @request_problem {
    include /etc/nginx/snippets/security-headers.conf;
    add_header Cache-Control "no-store" always;
    return 303 /request/problem/;
}
CONF
}

url="${RELAY_URL:-}"
url="${url%/}"

if [ -z "$url" ]; then
  no_relay "RELAY_URL isn't set"
  exit 0
fi

# http or https, a hostname or IPv4 address, an optional port, nothing else.
# The value is written into nginx's config, so anything outside that (spaces,
# quotes, semicolons, a path) is turned away here.
case "$url" in
  *[!A-Za-z0-9.:/-]*) url="" ;;
esac
if ! printf '%s' "$url" | grep -Eq '^https?://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$'; then
  no_relay "RELAY_URL isn't a plain http://host:port address"
  exit 0
fi

with_relay "$url"
if ! nginx -t -q 2>/dev/null; then
  no_relay "nginx turned down the config written for RELAY_URL"
  exit 0
fi
say "/request/submit goes to $url"
exit 0

#!/usr/bin/env bash
# Smoke test for the relay image: start it and check it from the outside.
# The Go tests cover what the relay does with a form; this covers the image.
#
#   relay/smoke.sh                       # tests yolorepair-site-relay:test
#   relay/smoke.sh <image>               # tests another image
#
# Intake is pointed at a port nothing listens on, so a good form has to end
# on the problem page.
set -uo pipefail

IMAGE="${1:-yolorepair-site-relay:test}"
NAME="relay-smoke-$$"
fails=0

pass() { echo "ok    $1"; }
fail() { echo "FAIL  $1"; fails=$((fails + 1)); }
check() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (got '$2', want '$3')"; fi; }

cleanup() { docker rm -f "$NAME" "$NAME-noconfig" >/dev/null 2>&1; }
trap cleanup EXIT

# It has to refuse to start without its settings.
docker run --name "$NAME-noconfig" "$IMAGE" >/dev/null 2>&1
check "exits 1 with no config" "$?" "1"

docker run -d --name "$NAME" -p 127.0.0.1::8080 \
  -e INTAKE_URL=http://127.0.0.1:9 -e INTAKE_SUBMIT_TOKEN=smoke-test-token \
  -e RATE_LIMIT_PER_IP=2 \
  "$IMAGE" >/dev/null || { echo "FAIL  the container didn't start"; exit 1; }
port="$(docker port "$NAME" 8080/tcp | head -1 | sed 's/.*://')"
base="http://127.0.0.1:$port"

up=""
for _ in $(seq 1 50); do
  if curl -fsS -o /dev/null "$base/healthz" 2>/dev/null; then up=1; break; fi
  sleep 0.2
done
if [ -z "$up" ]; then
  echo "FAIL  /healthz never answered"
  docker logs "$NAME"
  exit 1
fi

check "runs as 65532" "$(docker inspect -f '{{.Config.User}}' "$NAME")" "65532:65532"
check "/healthz says ok" "$(curl -s "$base/healthz")" "ok"
check "GET /submit is 405" "$(curl -s -o /dev/null -w '%{http_code}' "$base/submit")" "405"
check "GET / is 404" "$(curl -s -o /dev/null -w '%{http_code}' "$base/")" "404"

form='customer_name=Sam+Example&email=sam%40example.com&device=Game+Boy&symptoms=No+sound&handoff=drop_off'
check "a good form with intake down goes to the problem page" \
  "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -d "$form" "$base/submit")" \
  "303 $base/request/problem/"
check "a bad form goes to the problem page" \
  "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -d 'customer_name=Sam' "$base/submit")" \
  "303 $base/request/problem/"

check "a filled honeypot is shown the thanks page" \
  "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -d "$form&website=x" "$base/submit")" \
  "303 $base/request/thanks/"

# The limit is 2 per address here. The good form above was the first.
curl -s -o /dev/null -d "$form" "$base/submit"
curl -s -o /dev/null -d "$form" "$base/submit"

logs="$(docker logs "$NAME" 2>&1)"
for outcome in intake_unreachable invalid honeypot rate_limited; do
  if grep -q "\"outcome\":\"$outcome\"" <<<"$logs"; then pass "logs $outcome"; else fail "logs $outcome"; fi
done
check "the third good form from one address is rate limited" \
  "$(grep -c '"outcome":"intake_unreachable"' <<<"$logs")" "2"
if grep -qE 'Sam|example\.com|Game Boy|smoke-test-token' <<<"$logs"; then
  fail "logs carry nothing from the form or the token"
else
  pass "logs carry nothing from the form or the token"
fi

if [ "$fails" -gt 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "all checks passed"

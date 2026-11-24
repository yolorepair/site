#!/usr/bin/env bash
# End-to-end test for the repair-request form: the site image, the relay image
# and a fake intake on one Docker network. Each image has its own smoke test;
# this one catches the two drifting apart.
#
#   test/e2e.sh [site-image] [relay-image]
#   defaults: yolorepair-site:test yolorepair-site-relay:test
#
# It fills in the form the site actually serves, field by field, so a renamed
# or added field shows up here. Needs docker, curl and jq.
set -euo pipefail
cd "$(dirname "$0")/.."

SITE_IMAGE="${1:-yolorepair-site:test}"
RELAY_IMAGE="${2:-yolorepair-site-relay:test}"
INTAKE_IMAGE="yolorepair-fake-intake:e2e"
NAME="yolorepair-e2e-$$"
TOKEN="e2e-submit-token"
# The relay forwards this many per sender per hour. Small, so the test can
# reach it.
PER_SENDER=3
fails=0

cleanup() {
  docker rm -f "$NAME-site" "$NAME-relay" "$NAME-intake" >/dev/null 2>&1 || true
  docker network rm "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; fails=$((fails + 1)); }
check() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (want '$2', got '$3')"; fi; }
lacks() { if grep -qF -- "$2" <<<"$3"; then fail "$1 (found: $2)"; else pass "$1"; fi; }

# port <container> <port> -> the host port it's published on
port() { docker port "$1" "$2/tcp" | head -1 | sed 's/.*://'; }
# wait_for <url>
wait_for() {
  for _ in $(seq 1 50); do
    curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
    sleep 0.2
  done
  echo "FAIL  $1 never answered"; exit 1
}

# The fake intake is built with the same pinned Go image as the relay.
go_image="$(sed -nE 's/^FROM .*(golang:[^ ]+) AS build$/\1/p' relay/Dockerfile)"
[ -n "$go_image" ] || { echo "error: no golang image found in relay/Dockerfile" >&2; exit 2; }
docker build -q -t "$INTAKE_IMAGE" --build-arg "GO_IMAGE=$go_image" test/e2e/fake-intake >/dev/null

# Wired the way the cluster wires them: the site knows the relay, the relay
# knows intake, and only the site is the front door. (The fake intake's port
# is published too, so this script can ask it what it received.)
docker network create "$NAME" >/dev/null
docker run -d --name "$NAME-intake" --network "$NAME" --network-alias intake \
  -p 127.0.0.1::8000 -e "SUBMIT_TOKEN=$TOKEN" "$INTAKE_IMAGE" >/dev/null
docker run -d --name "$NAME-relay" --network "$NAME" --network-alias relay \
  -e INTAKE_URL=http://intake:8000 -e "INTAKE_SUBMIT_TOKEN=$TOKEN" \
  -e "RATE_LIMIT_PER_IP=$PER_SENDER" "$RELAY_IMAGE" >/dev/null
docker run -d --name "$NAME-site" --network "$NAME" \
  -p 127.0.0.1::8080 -e RELAY_URL=http://relay:8080 "$SITE_IMAGE" >/dev/null

SITE="http://127.0.0.1:$(port "$NAME-site" 8080)"
INTAKE="http://127.0.0.1:$(port "$NAME-intake" 8000)"
wait_for "$SITE/healthz"
wait_for "$INTAKE/healthz"

received() { curl -fsS "$INTAKE/received"; }
count() { received | jq 'length'; }

# What a person would type into each field. A field the form has and this
# doesn't know is a failure: someone changed the form, so look at the relay.
value_for() {
  case "$1" in
    customer_name)  echo "Sam Example" ;;
    email)          echo "sam@example.com" ;;
    device)         echo "Technics SL-1200MK2" ;;
    symptoms)       printf 'Platter spins, no audio out of the left channel.\nIt quit after a gig.' ;;
    handoff)        echo "mail_in" ;;
    address_line1)  echo "123 Example St" ;;
    address_line2)  echo "Unit 4" ;;
    address_city)   echo "Sacramento" ;;
    address_state)  echo "CA" ;;
    address_zip)    echo "95814" ;;
    website)        echo "" ;;
    *)              return 1 ;;
  esac
}

# The form as served: where it posts, and the names of its fields.
page="$(curl -fsS "$SITE/request/")"
form="${page#*<form}"
form="${form%%</form>*}"
action="$(grep -oE 'action="[^"]+"' <<<"$form" | head -1 | cut -d'"' -f2)"
names="$(grep -oE 'name="[^"]+"' <<<"$form" | cut -d'"' -f2 | sort -u)"
check "the form posts to /request/submit" "/request/submit" "$action"

args=()
for n in $names; do
  if v="$(value_for "$n")"; then
    args+=(--data-urlencode "$n=$v")
  else
    fail "the form has a field this test doesn't know: $n"
  fi
done

# submit <sender address> [extra curl args...] -> "status location"
submit() {
  local from="$1"; shift
  curl -s -o /dev/null -w '%{http_code} %header{location}' \
    -H "CF-Connecting-IP: $from" "${args[@]}" "$@" "$SITE$action"
}

# 1. A good request goes all the way through.
check "a good form goes to the thanks page" "303 /request/thanks/" "$(submit 203.0.113.7)"
check "the thanks page is there" "200" "$(curl -s -o /dev/null -w '%{http_code}' "$SITE/request/thanks/")"
check "intake received one request" "1" "$(count)"
got="$(received | jq '.[0]')"
check "it came with the submit token" "Bearer $TOKEN" "$(jq -r '.authorization' <<<"$got")"
check "it came as JSON" "application/json" "$(jq -r '.content_type' <<<"$got")"
if jq -e '(.idempotency_key // "") | test("^[0-9a-f]{32}$")' <<<"$got" >/dev/null; then
  pass "it came with an idempotency key"
else
  fail "it came with an idempotency key (got '$(jq -r '.idempotency_key' <<<"$got")')"
fi
# Exactly these fields and values: nothing added, the honeypot not passed on,
# the line break in the description kept as one character, and the address
# boxes joined into intake's one return_address.
want="$(jq -nS '{
  customer_name: "Sam Example",
  email: "sam@example.com",
  device: "Technics SL-1200MK2",
  symptoms: "Platter spins, no audio out of the left channel.\nIt quit after a gig.",
  handoff: "mail_in",
  return_address: "123 Example St\nUnit 4\nSacramento, CA 95814"
}')"
check "intake received what was typed, and nothing else" "$want" "$(jq -S '.body' <<<"$got")"

# 2. The honeypot: the bot is told it worked, and nothing is sent.
check "a filled honeypot is shown the thanks page" "303 /request/thanks/" \
  "$(submit 203.0.113.8 --data-urlencode 'website=https://spam.example')"
check "nothing reached intake from the honeypot" "1" "$(count)"

# 3. A form the relay turns down.
check "a form with no email goes to the problem page" "303 /request/problem/" \
  "$(curl -s -o /dev/null -w '%{http_code} %header{location}' -H 'CF-Connecting-IP: 203.0.113.9' \
     -d 'customer_name=Sam&device=Amp&symptoms=Hum&handoff=drop_off' "$SITE$action")"
check "nothing reached intake from the bad form" "1" "$(count)"

# 4. The rate limit, which only works if the visitor's address makes it from
#    the site to the relay. One sender is cut off; another isn't.
for _ in $(seq 2 "$PER_SENDER"); do submit 203.0.113.7 >/dev/null; done
check "one sender's first $PER_SENDER requests reached intake" "$PER_SENDER" "$(count)"
check "the next one from that sender goes to the problem page" "303 /request/problem/" "$(submit 203.0.113.7)"
check "and doesn't reach intake" "$PER_SENDER" "$(count)"
check "a different sender still gets through" "303 /request/thanks/" "$(submit 198.51.100.4)"
check "and reaches intake" "$((PER_SENDER + 1))" "$(count)"
keys="$(received | jq -r '.[].idempotency_key' | sort -u | grep -c .)"
check "every request had its own idempotency key" "$((PER_SENDER + 1))" "$keys"

# 5. Nothing a person typed is in the relay's log, and neither is the token.
relay_log="$(docker logs "$NAME-relay" 2>&1)"
for secret in "Sam" "example.com" "Technics" "Platter" "Example St" "$TOKEN" "203.0.113" "198.51.100"; do
  lacks "the relay's log doesn't have '$secret'" "$secret" "$relay_log"
done

# 6. Intake down: the form lands on the problem page and the site stays up.
docker rm -f "$NAME-intake" >/dev/null
check "intake down: the form goes to the problem page" "303 /request/problem/" "$(submit 192.0.2.10)"
check "intake down: the site still serves" "200" "$(curl -s -o /dev/null -w '%{http_code}' "$SITE/")"

# 7. Relay down: the same.
docker rm -f "$NAME-relay" >/dev/null
check "relay down: the form goes to the problem page" "303 /request/problem/" "$(submit 192.0.2.11)"
check "relay down: the problem page is there" "200" "$(curl -s -o /dev/null -w '%{http_code}' "$SITE/request/problem/")"

echo
if [ "$fails" -gt 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "all checks passed"

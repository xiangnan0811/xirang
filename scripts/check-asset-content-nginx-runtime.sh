#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEMPLATE=${ASSET_CONTENT_NGINX_TEMPLATE:-"$ROOT_DIR/deploy/nginx/templates/default.conf.template"}
NGINX_IMAGE=${ASSET_CONTENT_NGINX_IMAGE:-"nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de"}
TMP_DIR=$(mktemp -d)
CONTAINER_ID=

cleanup() {
  if [[ -n "$CONTAINER_ID" ]]; then
    docker rm -f "$CONTAINER_ID" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$TMP_DIR"
}
trap cleanup EXIT

fail() {
  echo "asset-content nginx runtime check: $*" >&2
  exit 1
}

if [[ ! -f "$TEMPLATE" ]]; then
  fail "template not found"
fi
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  fail "docker daemon is required"
fi
if ! command -v curl >/dev/null 2>&1; then
  fail "curl is required"
fi

TEMPLATE_DIR=$(CDPATH= cd -- "$(dirname -- "$TEMPLATE")" && pwd -P)
TEMPLATE="$TEMPLATE_DIR/$(basename -- "$TEMPLATE")"
mkdir -p "$TMP_DIR/logs"
chmod 0777 "$TMP_DIR/logs"
DELIVERY_ID=0123456789abcdef0123456789abcdef
CONTENT_PATH="/api/v1/asset-content/$DELIVERY_ID"
SHAPED_PATH=/api/v1/asset-content/shaped-probe
CANARY_XFF=198.51.100.77
CANARY_XRI=192.0.2.88
CANARY_COOKIE=FAKE_ASSET_CONTENT_COOKIE_FOR_TEST_ONLY
CANARY_RANGE='bytes=2-7'
CANARY_IF_RANGE='"FAKE_ETAG_FOR_TEST_ONLY"'

cat >"$TMP_DIR/probe.conf" <<NGINX
map \$http_cookie \$probe_cookie_present {
  default no;
  ~*xirang_asset_content=$CANARY_COOKIE yes;
}

server {
  listen 3000;
  server_name _;

  location = /healthz {
    return 200 "ok";
  }

  location = /readyz {
    return 200 "ready";
  }

  location = /api/v1/probe-ticket {
    add_header Set-Cookie "xirang_asset_content=$CANARY_COOKIE; Path=$CONTENT_PATH; HttpOnly; SameSite=Strict" always;
    return 200 "ticket-issued";
  }

  location = $CONTENT_PATH {
    add_header X-Probe-Host \$http_host always;
    add_header X-Probe-Proto \$http_x_forwarded_proto always;
    add_header X-Probe-Xff \$http_x_forwarded_for always;
    add_header X-Probe-Real-Ip \$http_x_real_ip always;
    add_header X-Probe-Range \$http_range always;
    add_header X-Probe-If-Range \$http_if_range always;
    add_header X-Probe-Cookie-Present \$probe_cookie_present always;
    add_header X-Probe-Method \$request_method always;
    default_type application/octet-stream;
    return 200 "probe-bytes";
  }

  location = $SHAPED_PATH {
    add_header X-Probe-Proto \$http_x_forwarded_proto always;
    add_header X-Probe-Xff \$http_x_forwarded_for always;
    add_header X-Probe-Real-Ip \$http_x_real_ip always;
    return 200 "shaped-probe";
  }
}
NGINX

CONTAINER_ID=$(docker run -d \
  -e CSP_CONNECT_SRC_EXTRA= \
  -p 127.0.0.1::10761 \
  --mount "type=bind,src=$TEMPLATE,dst=/etc/nginx/templates/default.conf.template,readonly" \
  --mount "type=bind,src=$TMP_DIR/probe.conf,dst=/etc/nginx/conf.d/probe.conf,readonly" \
  --mount "type=bind,src=$TMP_DIR/logs,dst=/logs" \
  "$NGINX_IMAGE")

endpoint=$(docker port "$CONTAINER_ID" 10761/tcp | head -n 1)
PORT=${endpoint##*:}
if [[ ! "$PORT" =~ ^[0-9]+$ ]]; then
  fail "could not resolve published listener"
fi
PUBLISHED_PEER=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}' "$CONTAINER_ID")
if [[ -z "$PUBLISHED_PEER" || "$PUBLISHED_PEER" == *[[:space:],]* ]]; then
  fail "could not resolve the published-listener peer"
fi
BASE_URL="http://127.0.0.1:$PORT"

ready=0
for _ in $(seq 1 40); do
  if curl -fsS --max-time 1 "$BASE_URL/readyz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
if [[ "$ready" != "1" ]]; then
  docker logs "$CONTAINER_ID" >&2 || true
  fail "official nginx listener did not become ready"
fi

curl -fsS --max-time 5 \
  --noproxy '*' \
  --connect-to "content.test:10761:127.0.0.1:$PORT" \
  -X POST \
  -c "$TMP_DIR/cookies.txt" \
  -D "$TMP_DIR/ticket.headers" \
  -o "$TMP_DIR/ticket.body" \
  "http://content.test:10761/api/v1/probe-ticket"

request_common=(
  --max-time 5
  --noproxy '*'
  --connect-to "content.test:10761:127.0.0.1:$PORT"
  -H 'X-Forwarded-Proto: https'
  -H "X-Forwarded-For: $CANARY_XFF"
  -H "X-Real-IP: $CANARY_XRI"
  -H "Range: $CANARY_RANGE"
  -H "If-Range: $CANARY_IF_RANGE"
  -b "$TMP_DIR/cookies.txt"
)

curl -fsS "${request_common[@]}" \
  -D "$TMP_DIR/get.headers" \
  -o "$TMP_DIR/get.body" \
  "http://content.test:10761$CONTENT_PATH"
curl -fsS "${request_common[@]}" \
  -I \
  -D "$TMP_DIR/head.headers" \
  -o /dev/null \
  "http://content.test:10761$CONTENT_PATH"
curl -fsS --max-time 5 \
  --noproxy '*' \
  --connect-to "content.test:10761:127.0.0.1:$PORT" \
  -H 'X-Forwarded-Proto: https' \
  -H "X-Forwarded-For: $CANARY_XFF" \
  -H "X-Real-IP: $CANARY_XRI" \
  -D "$TMP_DIR/shaped.headers" \
  -o "$TMP_DIR/shaped.body" \
  "http://content.test:10761$SHAPED_PATH"

failures=()
CONTENT_LOG="$TMP_DIR/logs/nginx-asset-content.log"
if [[ ! -f "$CONTENT_LOG" ]]; then
  failures+=("dedicated content log was not created")
fi

header_value() {
  local file=$1
  local name=$2
  awk -v wanted="$name" '
    {
      separator = index($0, ":")
      if (separator > 0 && tolower(substr($0, 1, separator - 1)) == tolower(wanted)) {
        value = substr($0, separator + 1)
        sub(/^[[:space:]]+/, "", value)
        sub(/\r$/, "", value)
        print value
        exit
      }
    }
  ' "$file"
}

assert_header() {
  local file=$1
  local name=$2
  local expected=$3
  local actual
  actual=$(header_value "$file" "$name")
  if [[ "$actual" != "$expected" ]]; then
    failures+=("$(basename "$file") $name expected '$expected', got '$actual'")
  fi
}

EXPECTED_XFF="$CANARY_XFF, $PUBLISHED_PEER"
for response in "$TMP_DIR/get.headers" "$TMP_DIR/head.headers"; do
  assert_header "$response" X-Probe-Host 'content.test:10761'
  assert_header "$response" X-Probe-Proto http
  assert_header "$response" X-Probe-Xff "$EXPECTED_XFF"
  assert_header "$response" X-Probe-Real-Ip ''
  assert_header "$response" X-Probe-Range "$CANARY_RANGE"
  assert_header "$response" X-Probe-If-Range "$CANARY_IF_RANGE"
  assert_header "$response" X-Probe-Cookie-Present yes
done
assert_header "$TMP_DIR/get.headers" X-Probe-Method GET
assert_header "$TMP_DIR/head.headers" X-Probe-Method HEAD
assert_header "$TMP_DIR/shaped.headers" X-Probe-Proto http
assert_header "$TMP_DIR/shaped.headers" X-Probe-Xff ''
assert_header "$TMP_DIR/shaped.headers" X-Probe-Real-Ip ''

if [[ "$(<"$TMP_DIR/get.body")" != "probe-bytes" ]]; then
  failures+=("GET body did not preserve probe bytes")
fi
if [[ "$(<"$TMP_DIR/shaped.body")" != "shaped-probe" ]]; then
  failures+=("shaped fallback did not preserve probe bytes")
fi
if ! awk -v value="$CANARY_COOKIE" '$6 == "xirang_asset_content" && $7 == value { found = 1 } END { exit !found }' "$TMP_DIR/cookies.txt"; then
  failures+=("ticket cookie was not retained by the client")
fi
for canary in "$DELIVERY_ID" "$CANARY_COOKIE" "$CANARY_XFF" "$CANARY_XRI" 'shaped-probe' 'content.test' "$CANARY_RANGE" "$CANARY_IF_RANGE"; do
  if [[ -f "$CONTENT_LOG" ]] && grep -Fq -- "$canary" "$CONTENT_LOG"; then
    failures+=("dedicated content log leaked canary material")
  fi
done
if [[ ! -f "$CONTENT_LOG" ]] || [[ $(wc -l <"$CONTENT_LOG") -ne 3 ]]; then
  failures+=("dedicated content log did not contain exactly GET, HEAD, and shaped events")
elif ! awk 'NR == 1 { get_bytes = $3 } NR == 2 { head_bytes = $3 } END { exit !(get_bytes == 11 && head_bytes == 0) }' "$CONTENT_LOG"; then
  failures+=("dedicated content log did not prove GET bytes and an empty HEAD body")
fi

if (( ${#failures[@]} > 0 )); then
  printf 'asset-content nginx runtime check: FAIL\n' >&2
  printf ' - %s\n' "${failures[@]}" >&2
  exit 1
fi

echo "asset-content nginx runtime check: PASS"

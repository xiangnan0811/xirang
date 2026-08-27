#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEMPLATE=${ASSET_CONTENT_NGINX_TEMPLATE:-"$ROOT_DIR/deploy/nginx/templates/default.conf.template"}
NGINX_IMAGE=${ASSET_CONTENT_NGINX_IMAGE:-nginx:1.29-alpine}
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

fail() {
  echo "asset-content nginx check: $*" >&2
  exit 1
}

if [[ ! -f "$TEMPLATE" ]]; then
  fail "template not found: $TEMPLATE"
fi
if ! command -v docker >/dev/null 2>&1; then
  fail "docker is required for the official nginx envsubst and nginx -t path"
fi

TEMPLATE_DIR=$(CDPATH= cd -- "$(dirname -- "$TEMPLATE")" && pwd -P)
TEMPLATE="$TEMPLATE_DIR/$(basename -- "$TEMPLATE")"
RENDERED="$TMP_DIR/nginx-rendered.txt"

if ! docker run --rm --network none \
  -e CSP_CONNECT_SRC_EXTRA= \
  --mount "type=bind,src=$TEMPLATE,dst=/etc/nginx/templates/default.conf.template,readonly" \
  --tmpfs /logs:rw,noexec,nosuid,size=16m \
  "$NGINX_IMAGE" nginx -T >"$RENDERED" 2>&1; then
  sed -n '1,240p' "$RENDERED" >&2
  fail "official nginx template rendering or nginx -t failed"
fi

require_once_line() {
  local file=$1
  local expected=$2
  local count
  count=$(grep -Fxc -- "$expected" "$file" || true)
  if [[ "$count" -ne 1 ]]; then
    fail "expected exactly one directive: $expected"
  fi
}

require_unique_prefix() {
  local file=$1
  local prefix=$2
  local expected=$3
  local count
  count=$(awk -v prefix="$prefix" 'index($0, prefix) == 1 { count++ } END { print count + 0 }' "$file")
  if [[ "$count" -ne 1 ]]; then
    fail "expected exactly one directive with prefix: $prefix"
  fi
  require_once_line "$file" "$expected"
}

normalize_block() {
  sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "$1" >"$2"
}

extract_simple_block() {
  local source=$1
  local opening=$2
  local destination=$3
  awk -v opening="$opening" '
    function trim(value) {
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      return value
    }
    {
      line = trim($0)
      if (!inside && line == opening) {
        inside = 1
      }
      if (inside) {
        print $0
        if (line == "}") {
          exit
        }
      }
    }
  ' "$source" >"$destination"
  if [[ ! -s "$destination" ]]; then
    fail "missing block: $opening"
  fi
}

CONTENT_OPENING='location ~ "^/api/v1/asset-content/[0-9a-f]{32}$" {'
CONTENT_COUNT=$(grep -Fc -- "$CONTENT_OPENING" "$RENDERED" || true)
if [[ "$CONTENT_COUNT" -ne 1 ]]; then
  fail "the exact asset-content location must occur once"
fi

CONTENT_RAW="$TMP_DIR/content.raw"
CONTENT_BLOCK="$TMP_DIR/content.block"
extract_simple_block "$RENDERED" "$CONTENT_OPENING" "$CONTENT_RAW"
normalize_block "$CONTENT_RAW" "$CONTENT_BLOCK"

require_unique_prefix "$CONTENT_BLOCK" 'access_log ' 'access_log /logs/nginx-asset-content.log xirang_asset_content;'
require_unique_prefix "$CONTENT_BLOCK" 'error_log ' 'error_log /dev/null crit;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_pass ' 'proxy_pass http://127.0.0.1:3000;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_http_version ' 'proxy_http_version 1.1;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_buffering ' 'proxy_buffering off;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_request_buffering ' 'proxy_request_buffering off;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_cache ' 'proxy_cache off;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_max_temp_file_size ' 'proxy_max_temp_file_size 0;'
require_unique_prefix "$CONTENT_BLOCK" 'gzip ' 'gzip off;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_read_timeout ' 'proxy_read_timeout 75s;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_send_timeout ' 'proxy_send_timeout 75s;'
require_unique_prefix "$CONTENT_BLOCK" 'send_timeout ' 'send_timeout 75s;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_set_header Host ' 'proxy_set_header Host $http_host;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_set_header X-Forwarded-Proto ' 'proxy_set_header X-Forwarded-Proto $scheme;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_set_header X-Forwarded-For ' 'proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_set_header X-Real-IP ' 'proxy_set_header X-Real-IP "";'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_set_header Range ' 'proxy_set_header Range $http_range;'
require_unique_prefix "$CONTENT_BLOCK" 'proxy_set_header If-Range ' 'proxy_set_header If-Range $http_if_range;'

ADD_HEADER_COUNT=$(grep -Ec '^add_header[[:space:]]' "$CONTENT_BLOCK" || true)
if [[ "$ADD_HEADER_COUNT" -ne 3 ]]; then
  fail "asset-content location must own exactly three safe response headers"
fi
require_once_line "$CONTENT_BLOCK" 'add_header X-Content-Type-Options "nosniff" always;'
require_once_line "$CONTENT_BLOCK" 'add_header Referrer-Policy "no-referrer" always;'
require_once_line "$CONTENT_BLOCK" 'add_header Cross-Origin-Resource-Policy "same-origin" always;'

SHAPED_OPENING='location ~ "^/api/v1/asset-content(?:/|$)" {'
SHAPED_COUNT=$(grep -Fc -- "$SHAPED_OPENING" "$RENDERED" || true)
if [[ "$SHAPED_COUNT" -ne 1 ]]; then
  fail "the redacted asset-content-shaped fallback must occur once"
fi
SHAPED_RAW="$TMP_DIR/content-shaped.raw"
SHAPED_BLOCK="$TMP_DIR/content-shaped.block"
extract_simple_block "$RENDERED" "$SHAPED_OPENING" "$SHAPED_RAW"
normalize_block "$SHAPED_RAW" "$SHAPED_BLOCK"
require_unique_prefix "$SHAPED_BLOCK" 'access_log ' 'access_log /logs/nginx-asset-content.log xirang_asset_content;'
require_unique_prefix "$SHAPED_BLOCK" 'error_log ' 'error_log /dev/null crit;'
require_unique_prefix "$SHAPED_BLOCK" 'proxy_pass ' 'proxy_pass http://127.0.0.1:3000;'
require_unique_prefix "$SHAPED_BLOCK" 'proxy_http_version ' 'proxy_http_version 1.1;'
require_unique_prefix "$SHAPED_BLOCK" 'proxy_set_header Host ' 'proxy_set_header Host $http_host;'
require_unique_prefix "$SHAPED_BLOCK" 'proxy_set_header X-Forwarded-Proto ' 'proxy_set_header X-Forwarded-Proto $scheme;'
require_unique_prefix "$SHAPED_BLOCK" 'proxy_set_header X-Forwarded-For ' 'proxy_set_header X-Forwarded-For "";'
require_unique_prefix "$SHAPED_BLOCK" 'proxy_set_header X-Real-IP ' 'proxy_set_header X-Real-IP "";'

SHAPED_ADD_HEADER_COUNT=$(grep -Ec '^add_header[[:space:]]' "$SHAPED_BLOCK" || true)
if [[ "$SHAPED_ADD_HEADER_COUNT" -ne 3 ]]; then
  fail "asset-content-shaped fallback must own exactly three safe response headers"
fi
require_once_line "$SHAPED_BLOCK" 'add_header X-Content-Type-Options "nosniff" always;'
require_once_line "$SHAPED_BLOCK" 'add_header Referrer-Policy "no-referrer" always;'
require_once_line "$SHAPED_BLOCK" 'add_header Cross-Origin-Resource-Policy "same-origin" always;'
if grep -Eq '^(proxy_buffering|proxy_request_buffering|proxy_cache|proxy_max_temp_file_size|gzip|proxy_read_timeout|proxy_send_timeout|send_timeout|proxy_set_header Range|proxy_set_header If-Range)[[:space:]]' "$SHAPED_BLOCK"; then
  fail "asset-content-shaped fallback inherited exact-route streaming policy"
fi

LOG_COUNT=$(grep -Ec '^[[:space:]]*log_format[[:space:]]+xirang_asset_content([[:space:]]|$)' "$RENDERED" || true)
if [[ "$LOG_COUNT" -ne 1 ]]; then
  fail "dedicated xirang_asset_content log format must occur once"
fi
LOG_BLOCK="$TMP_DIR/content-log.block"
awk '
  /^[[:space:]]*log_format[[:space:]]+xirang_asset_content([[:space:]]|$)/ { inside = 1 }
  inside {
    print
    if (index($0, ";") > 0) {
      exit
    }
  }
' "$RENDERED" >"$LOG_BLOCK"

LOG_VARIABLES="$TMP_DIR/content-log.variables"
grep -oE '\$[A-Za-z_][A-Za-z0-9_]*' "$LOG_BLOCK" | sort -u >"$LOG_VARIABLES"
while IFS= read -r variable; do
  case "$variable" in
    '$request_id'|'$status'|'$body_bytes_sent'|'$request_time'|'$upstream_connect_time'|'$upstream_header_time'|'$upstream_response_time') ;;
    *) fail "forbidden variable in asset-content access log: $variable" ;;
  esac
done <"$LOG_VARIABLES"
for variable in '$request_id' '$status' '$body_bytes_sent' '$request_time' '$upstream_connect_time' '$upstream_header_time' '$upstream_response_time'; do
  if ! grep -Fxq -- "$variable" "$LOG_VARIABLES"; then
    fail "required asset-content access-log variable missing: $variable"
  fi
done

if grep -Fq -- '$http_x_forwarded_proto' "$CONTENT_BLOCK" ||
   grep -Fq -- '$http_x_forwarded_proto' "$SHAPED_BLOCK" ||
   grep -Fq -- '$xirang_effective_proto' "$CONTENT_BLOCK" ||
   grep -Fq -- '$xirang_effective_proto' "$SHAPED_BLOCK"; then
  fail "content routes must overwrite forwarded scheme with the actual nginx scheme"
fi

GENERIC_OPENING='location /api/v1/ {'
GENERIC_COUNT=$(grep -Fc -- "$GENERIC_OPENING" "$RENDERED" || true)
if [[ "$GENERIC_COUNT" -ne 1 ]]; then
  fail "generic /api/v1/ location must occur once"
fi
GENERIC_RAW="$TMP_DIR/generic.raw"
GENERIC_BLOCK="$TMP_DIR/generic.block"
extract_simple_block "$RENDERED" "$GENERIC_OPENING" "$GENERIC_RAW"
normalize_block "$GENERIC_RAW" "$GENERIC_BLOCK"
require_unique_prefix "$GENERIC_BLOCK" 'client_max_body_size ' 'client_max_body_size 16m;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_pass ' 'proxy_pass http://127.0.0.1:3000;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_http_version ' 'proxy_http_version 1.1;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_set_header Host ' 'proxy_set_header Host $host;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_set_header X-Real-IP ' 'proxy_set_header X-Real-IP $remote_addr;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_set_header X-Forwarded-For ' 'proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_set_header X-Forwarded-Proto ' 'proxy_set_header X-Forwarded-Proto $scheme;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_set_header Upgrade ' 'proxy_set_header Upgrade $http_upgrade;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_set_header Connection ' 'proxy_set_header Connection $connection_upgrade;'
require_unique_prefix "$GENERIC_BLOCK" 'proxy_read_timeout ' 'proxy_read_timeout 3600s;'

CONTENT_LINE=$(grep -nF -- "$CONTENT_OPENING" "$RENDERED" | cut -d: -f1)
SHAPED_LINE=$(grep -nF -- "$SHAPED_OPENING" "$RENDERED" | cut -d: -f1)
GENERIC_LINE=$(grep -nF -- "$GENERIC_OPENING" "$RENDERED" | cut -d: -f1)
if (( CONTENT_LINE >= SHAPED_LINE || SHAPED_LINE >= GENERIC_LINE )); then
  fail "exact and shaped asset-content locations must precede the generic API location in that order"
fi

LISTEN_COUNT=$(grep -Ec '^[[:space:]]*listen[[:space:]]+' "$RENDERED" || true)
LISTEN_10761_COUNT=$(grep -Ec '^[[:space:]]*listen[[:space:]]+10761;[[:space:]]*$' "$RENDERED" || true)
if [[ "$LISTEN_COUNT" -ne 1 || "$LISTEN_10761_COUNT" -ne 1 ]]; then
  fail "the all-in-one nginx entrypoint must remain exactly HTTP port 10761"
fi
if ! grep -Fq -- 'location = /healthz {' "$RENDERED"; then
  fail "healthz proxy route is missing"
fi
if grep -Eq '^[[:space:]]*(ssl_certificate|ssl_certificate_key)[[:space:]]|^[[:space:]]*listen[[:space:]].*(443|ssl)' "$RENDERED"; then
  fail "TLS directives are outside the all-in-one image contract"
fi

echo "asset-content nginx check: PASS"

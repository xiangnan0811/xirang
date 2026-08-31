#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CHECKER="$SCRIPT_DIR/check-asset-content-nginx.sh"
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

BASE_TEMPLATE="$TMP_DIR/default.conf.template"
cat >"$BASE_TEMPLATE" <<'NGINX'
map $http_upgrade $connection_upgrade {
  default upgrade;
  '' close;
}

log_format xirang_access '$request_method $uri $status';
log_format xirang_asset_content
    '$request_id $status $body_bytes_sent '
    'rt=$request_time uct=$upstream_connect_time '
    'uht=$upstream_header_time urt=$upstream_response_time';

server {
  listen 10761;
  server_name _;

  gzip on;
  access_log /logs/nginx-access.log xirang_access;
  error_log /logs/nginx-error.log warn;

  location ~ "^/api/v1/asset-content/[0-9a-f]{32}$" {
    access_log /logs/nginx-asset-content.log xirang_asset_content;
    error_log /dev/null crit;
    add_header X-Content-Type-Options "nosniff" always;
    add_header Referrer-Policy "no-referrer" always;
    add_header Cross-Origin-Resource-Policy "same-origin" always;
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;
    proxy_buffering off;
    proxy_request_buffering off;
    proxy_cache off;
    proxy_max_temp_file_size 0;
    gzip off;
    proxy_read_timeout 75s;
    proxy_send_timeout 75s;
    send_timeout 75s;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP "";
    proxy_set_header Range $http_range;
    proxy_set_header If-Range $http_if_range;
  }

  location ~ "^/api/v1/asset-content(?:/|$)" {
    access_log /logs/nginx-asset-content.log xirang_asset_content;
    error_log /dev/null crit;
    add_header X-Content-Type-Options "nosniff" always;
    add_header Referrer-Policy "no-referrer" always;
    add_header Cross-Origin-Resource-Policy "same-origin" always;
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For "";
    proxy_set_header X-Real-IP "";
  }

  location /api/v1/ {
    client_max_body_size 16m;
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_read_timeout 3600s;
  }

  location = /healthz {
    proxy_pass http://127.0.0.1:3000/healthz;
  }

  location = /readyz {
    proxy_pass http://127.0.0.1:3000/readyz;
  }

  location / {
    try_files $uri $uri/ /index.html;
  }
}
NGINX

fail() {
  echo "asset-content nginx checker self-test: $*" >&2
  exit 1
}

run_checker() {
  local template=$1
  ASSET_CONTENT_NGINX_TEMPLATE="$template" bash "$CHECKER"
}

expect_mutation_failure() {
  local name=$1
  local expression=$2
  local candidate="$TMP_DIR/$name.conf.template"
  local output="$TMP_DIR/$name.log"

  sed "$expression" "$BASE_TEMPLATE" >"$candidate"
  if cmp -s "$BASE_TEMPLATE" "$candidate"; then
    fail "mutation did not change fixture: $name"
  fi
  if run_checker "$candidate" >"$output" 2>&1; then
    fail "checker accepted unsafe mutation: $name"
  fi
}

run_checker "$BASE_TEMPLATE"

expect_mutation_failure raw-uri-log 's/\$request_id \$status/\$request_uri \$status/'
expect_mutation_failure cookie-log 's/\$request_id \$status/\$http_cookie \$status/'
expect_mutation_failure generic-content-log 's#nginx-asset-content.log xirang_asset_content#nginx-asset-content.log xirang_access#'
expect_mutation_failure missing-content-route 's#location ~ "\^/api/v1/asset-content/#location ~ "^/api/v1/asset-download/#'
expect_mutation_failure missing-content-shaped-fallback 's#location ~ "\^/api/v1/asset-content(?:/|\$)" {#location ~ "^/api/v1/asset-download(?:/|$)" {#'
expect_mutation_failure inherited-error-log 's#error_log /dev/null crit;#error_log /logs/nginx-error.log warn;#'
expect_mutation_failure proxy-buffering-on 's/proxy_buffering off;/proxy_buffering on;/'
expect_mutation_failure request-buffering-on 's/proxy_request_buffering off;/proxy_request_buffering on;/'
expect_mutation_failure cache-not-disabled 's/proxy_cache off;/proxy_cache_bypass 0;/'
expect_mutation_failure temp-files-enabled 's/proxy_max_temp_file_size 0;/proxy_max_temp_file_size 1024m;/'
expect_mutation_failure gzip-enabled '0,/gzip off;/s//gzip on;/'
expect_mutation_failure infinite-read-timeout 's/proxy_read_timeout 75s;/proxy_read_timeout 0;/'
expect_mutation_failure missing-send-timeout 's/proxy_send_timeout 75s;/# proxy_send_timeout removed;/'
expect_mutation_failure changed-port 's/listen 10761;/listen 10762;/'
expect_mutation_failure missing-generic-api 's#location /api/v1/ {#location /api/v2/ {#'
expect_mutation_failure changed-generic-timeout 's/proxy_read_timeout 3600s;/proxy_read_timeout 0;/'
expect_mutation_failure host-port-loss 's/proxy_set_header Host \$http_host;/proxy_set_header Host \$host;/'
expect_mutation_failure untrusted-forwarded-proto '0,/proxy_set_header X-Forwarded-Proto \$scheme;/s//proxy_set_header X-Forwarded-Proto \$http_x_forwarded_proto;/'
expect_mutation_failure shaped-untrusted-forwarded-proto '0,/proxy_set_header X-Forwarded-Proto \$scheme;/! s//proxy_set_header X-Forwarded-Proto \$http_x_forwarded_proto;/'
expect_mutation_failure missing-content-xff 's/proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;/# exact content XFF removed;/'
expect_mutation_failure missing-content-x-real-ip '0,/proxy_set_header X-Real-IP "";/s//# exact content X-Real-IP removal removed;/'
expect_mutation_failure content-x-real-ip '0,/proxy_set_header X-Real-IP "";/s//proxy_set_header X-Real-IP \$remote_addr;/'
expect_mutation_failure missing-shaped-xff '0,/proxy_set_header X-Forwarded-For "";/s//# shaped XFF removal removed;/'
expect_mutation_failure shaped-forwarded-xff '0,/proxy_set_header X-Forwarded-For "";/s//proxy_set_header X-Forwarded-For \$http_x_forwarded_for;/'
expect_mutation_failure missing-shaped-x-real-ip '0,/proxy_set_header X-Real-IP "";/! s//# shaped X-Real-IP removal removed;/'
expect_mutation_failure shaped-x-real-ip '0,/proxy_set_header X-Real-IP "";/! s//proxy_set_header X-Real-IP \$remote_addr;/'
expect_mutation_failure missing-nosniff 's/add_header X-Content-Type-Options "nosniff" always;/# nosniff removed;/'
expect_mutation_failure stripped-range 's/proxy_set_header Range \$http_range;/proxy_set_header Range "";/'

echo "asset-content nginx checker self-test: PASS"

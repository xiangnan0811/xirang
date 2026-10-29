#!/usr/bin/env bash
# Exercise the live probe's final status with a controlled Docker/curl boundary.
set -euo pipefail
umask 077

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
CHECKER="$SCRIPT_DIR/check-asset-content-nginx-runtime.sh"
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT
mkdir -p "$WORK/bin"
printf -v fixture_nginx_id '%064d' 4

cat >"$WORK/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
set -euo pipefail
printf -v nginx_id '%064d' 4
case "$1" in
  info) exit 0 ;;
  run)
    cidfile=
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == --cidfile ]]; then cidfile=$2; break; fi
      shift
    done
    [[ -n "$cidfile" ]] || exit 2
    printf '%s\n' "$nginx_id" >"$cidfile"
    case "$FIXTURE_CASE" in
      signal-start) kill -TERM "$PPID" ;;
      partial-start) exit 2 ;;
    esac
    echo "$nginx_id"
    ;;
  port)
    if [[ "$FIXTURE_BAD_PORT" == 1 ]]; then echo invalid; else echo 127.0.0.1:12345; fi
    ;;
  inspect)
    if [[ "$3" == '{{.Image}}' ]]; then printf 'sha256:%s\n' "$nginx_id"; else echo 172.18.0.1; fi
    ;;
  rm)
    [[ "$2" == -f && "$3" == "$nginx_id" ]] || exit 2
    echo "$3" >>"$FIXTURE_TRACE"
    if [[ "$FIXTURE_FAIL_RM" == 1 ]]; then
      echo 'injected nginx container removal failure' >&2
      exit 1
    fi
    ;;
  *) echo "unexpected fake Docker command: $*" >&2; exit 2 ;;
esac
DOCKER

cat >"$WORK/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
headers= output= cookie= method=GET
while [[ $# -gt 0 ]]; do
  case "$1" in
    -D) headers=$2; shift 2 ;;
    -o) output=$2; shift 2 ;;
    -c) cookie=$2; shift 2 ;;
    -I) method=HEAD; shift ;;
    --max-time|--noproxy|--connect-to|-H|-X|-b) shift 2 ;;
    *) shift ;;
  esac
done
[[ -n "$headers" ]] || exit 0
directory=${headers%/*}
case "${headers##*/}" in
  ticket.headers)
    printf 'HTTP/1.1 200 OK\r\n' >"$headers"
    printf '.test\tTRUE\t/\tFALSE\t0\txirang_asset_content\tFAKE_ASSET_CONTENT_COOKIE_FOR_TEST_ONLY\n' >"$cookie"
    printf 'ticket-issued' >"$output"
    ;;
  get.headers|head.headers)
    cat >"$headers" <<HEADERS
X-Probe-Host: content.test:10761
X-Probe-Proto: http
X-Probe-Xff: 198.51.100.77, 172.18.0.1
X-Probe-Range: bytes=2-7
X-Probe-If-Range: "FAKE_ETAG_FOR_TEST_ONLY"
X-Probe-Cookie-Present: yes
X-Probe-Method: $method
HEADERS
    if [[ "$method" == GET ]]; then
      printf 'probe-bytes' >"$output"
      printf 'event ok 11\n' >>"$directory/logs/nginx-asset-content.log"
    else
      printf 'event ok 0\n' >>"$directory/logs/nginx-asset-content.log"
    fi
    ;;
  shaped.headers)
    printf 'X-Probe-Proto: http\n' >"$headers"
    printf 'shaped-probe' >"$output"
    printf 'event ok 0\n' >>"$directory/logs/nginx-asset-content.log"
    ;;
  *) exit 2 ;;
esac
CURL
chmod 0700 "$WORK/bin/docker" "$WORK/bin/curl"

fail() {
  echo "asset-content nginx runtime cleanup self-test: $*" >&2
  exit 1
}

run_case() {
  local name=$1 expected=$2 fail_rm=$3 bad_port=$4 status=0
  PATH="$WORK/bin:$PATH" ASSET_CONTENT_NGINX_IMAGE=fixture-image \
    FIXTURE_FAIL_RM="$fail_rm" FIXTURE_BAD_PORT="$bad_port" FIXTURE_CASE="$name" \
    FIXTURE_TRACE="$WORK/$name.trace" \
    bash "$CHECKER" >"$WORK/$name.log" 2>&1 || status=$?
  [[ "$status" -eq "$expected" ]] || fail "$name returned $status, expected $expected"
}

run_case success 0 0 0
grep -Fq 'asset-content nginx runtime check: PASS' "$WORK/success.log" || fail 'successful probe did not report PASS'
run_case cleanup-failure 1 1 0
grep -Fq "failed to remove container $fixture_nginx_id" "$WORK/cleanup-failure.log" || fail 'cleanup failure was hidden'
! grep -Fq 'asset-content nginx runtime check: PASS' "$WORK/cleanup-failure.log" || fail 'failed cleanup reported PASS'
run_case probe-and-cleanup-failure 1 1 1
grep -Fq 'could not resolve published listener' "$WORK/probe-and-cleanup-failure.log" || fail 'original probe failure was lost'
grep -Fq "failed to remove container $fixture_nginx_id" "$WORK/probe-and-cleanup-failure.log" || fail 'cleanup failure after probe failure was hidden'
run_case partial-start 1 0 0
grep -Fq 'could not start nginx container' "$WORK/partial-start.log" || fail 'partial start failure was lost'
[[ -s "$WORK/partial-start.trace" ]] || fail 'partial start leaked a container'
run_case signal-start 143 0 0
[[ -s "$WORK/signal-start.trace" ]] || fail 'signal during start leaked a container'

echo 'asset-content nginx runtime cleanup self-test: PASS'

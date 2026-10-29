#!/usr/bin/env bash
# Exercise the real wrapper's ownership and cleanup decisions at the Docker boundary.
set -euo pipefail
umask 077

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
WRAPPER="$SCRIPT_DIR/cron-backup-isolated.integration.test.sh"
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT
mkdir -p "$WORK/bin"

cat >"$WORK/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
set -euo pipefail
state=$FIXTURE_STATE
printf '%s\n' "$@" >>"$state/commands"
printf -v network_id '%064d' 1
printf -v postgres_id '%064d' 2
printf -v tools_id '%064d' 3
printf -v image_id '%064d' 5

case "$1" in
  info) exit 0 ;;
  image) exit 0 ;;
  network)
    case "$2" in
      create)
        label=$4
        name=$5
        printf '%s\n' "${label#*=}" >"$state/token"
        printf '%s\n' "$name" >"$state/network-name"
        : >"$state/network"
        if [[ "$FIXTURE_CASE" == signal-create ]]; then kill -TERM "$PPID"; fi
        echo "$network_id"
        ;;
      ls)
        [[ ! -f "$state/network" ]] || echo "$network_id"
        ;;
      inspect)
        printf '%s %s\n' "$(<"$state/network-name")" "$(<"$state/token")"
        ;;
      rm)
        if [[ "$FIXTURE_CASE" == fail-network-removal || -f "$state/postgres" || -f "$state/tools" ]]; then
          echo 'injected network removal failure' >&2
          exit 1
        fi
        rm -f -- "$state/network"
        ;;
      *) exit 2 ;;
    esac
    ;;
  create)
    name=
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == --name ]]; then name=$2; break; fi
      shift
    done
    case "$name" in
      *-postgres)
        printf '%s\n' "$name" >"$state/postgres"
        echo "$postgres_id"
        ;;
      *-tools)
        printf '%s\n' "$name" >"$state/tools"
        if [[ "$FIXTURE_CASE" == partial-create ]]; then exit 2; fi
        echo "$tools_id"
        ;;
      *) exit 2 ;;
    esac
    ;;
  inspect)
    id=${@: -1}
    if [[ "$2" == --format && "$3" == '{{.Image}}' ]]; then
      printf 'sha256:%s\n' "$image_id"
    else
      case "$id" in
        "$postgres_id") name=$(<"$state/postgres") ;;
        "$tools_id") name=$(<"$state/tools") ;;
        *) exit 2 ;;
      esac
      printf '/%s %s\n' "$name" "$(<"$state/token")"
    fi
    ;;
  start)
    if [[ "$2" == --attach ]]; then
      case "$FIXTURE_CASE" in
        signal-attach) kill -TERM "$PPID" ;;
        suite-failure) exit 33 ;;
      esac
      echo 'fixture: tools container exited successfully'
    fi
    ;;
  exec) exit 0 ;;
  ps)
    [[ ! -f "$state/postgres" ]] || echo "$postgres_id"
    [[ ! -f "$state/tools" ]] || echo "$tools_id"
    ;;
  rm)
    id=${@: -1}
    case "$id" in
      "$tools_id")
        if [[ "$FIXTURE_CASE" == fail-container-removal ]]; then
          echo 'injected container removal failure' >&2
          exit 1
        fi
        rm -f -- "$state/tools"
        ;;
      "$postgres_id") rm -f -- "$state/postgres" ;;
      *) exit 2 ;;
    esac
    ;;
  *) echo "unexpected fake Docker command: $*" >&2; exit 2 ;;
esac
DOCKER
chmod 0700 "$WORK/bin/docker"

fail() {
  echo "isolated cron wrapper self-test: $*" >&2
  exit 1
}

run_case() {
  local name=$1 expected=$2 status=0
  local state="$WORK/$name"
  mkdir -m 0700 "$state"
  PATH="$WORK/bin:$PATH" FIXTURE_STATE="$state" FIXTURE_CASE="$name" \
    TEST_POSTGRES_DSN='BUSINESS_DSN_MUST_NOT_BE_USED' DB_DSN='BUSINESS_DSN_MUST_NOT_BE_USED' \
    bash "$WRAPPER" >"$state/output" 2>&1 || status=$?
  [[ "$status" -eq "$expected" ]] || fail "$name returned $status, expected $expected"
  if grep -Fq 'BUSINESS_DSN_MUST_NOT_BE_USED' "$state/commands"; then
    fail "$name passed the caller's DSN to Docker"
  fi
}

run_case success 0
success="$WORK/success"
[[ ! -e "$success/network" && ! -e "$success/postgres" && ! -e "$success/tools" ]] || fail 'success leaked a resource'
grep -Fqx -- '/var/lib/postgresql:rw,nosuid,noexec,uid=70,gid=70,mode=0700' "$success/commands" || fail 'PostgreSQL data is not on the expected tmpfs'
grep -Fqx -- '/work:rw,nosuid,nodev,exec,mode=0700' "$success/commands" || fail 'executable private scratch is missing'
grep -Fq -- 'dst=/source,readonly' "$success/commands" || fail 'source mount is not read-only'
if grep -Eq -- '^(-p|--publish|--publish-all|--privileged)$|/var/run/docker.sock' "$success/commands"; then
  fail 'the isolated fixture exposed a port or Docker socket'
fi
grep -Fq 'isolated cron integration: PASS (owned resources removed)' "$success/output" || fail 'success was reported before cleanup'

run_case suite-failure 1
[[ ! -e "$WORK/suite-failure/network" && ! -e "$WORK/suite-failure/postgres" && ! -e "$WORK/suite-failure/tools" ]] || fail 'suite failure leaked a resource'
run_case partial-create 1
[[ ! -e "$WORK/partial-create/network" && ! -e "$WORK/partial-create/postgres" && ! -e "$WORK/partial-create/tools" ]] || fail 'partial create leaked a resource'
run_case signal-create 143
[[ ! -e "$WORK/signal-create/network" ]] || fail 'signal during create leaked the network'
run_case signal-attach 143
[[ ! -e "$WORK/signal-attach/network" && ! -e "$WORK/signal-attach/postgres" && ! -e "$WORK/signal-attach/tools" ]] || fail 'signal during the suite leaked a resource'
run_case fail-container-removal 1
grep -Fq 'failed to remove container' "$WORK/fail-container-removal/output" || fail 'container cleanup failure was hidden'
! grep -Fq 'isolated cron integration: PASS' "$WORK/fail-container-removal/output" || fail 'failed container cleanup reported success'
run_case fail-network-removal 1
grep -Fq 'failed to remove network' "$WORK/fail-network-removal/output" || fail 'network cleanup failure was hidden'
! grep -Fq 'isolated cron integration: PASS' "$WORK/fail-network-removal/output" || fail 'failed network cleanup reported success'

echo 'isolated cron wrapper self-test: PASS'

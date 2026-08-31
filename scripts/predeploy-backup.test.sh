#!/usr/bin/env bash
# Focused three-state contract test for scripts/predeploy-backup.sh.

set -euo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/predeploy-backup.sh"
WORK="$(mktemp -d)"
STUB_BIN="$WORK/bin"
DOCKER_LOG="$WORK/docker.log"

cleanup() {
  rm -rf -- "$WORK"
}
trap cleanup EXIT

mkdir -p "$STUB_BIN"
REAL_SHA256SUM="$(command -v sha256sum)"
export PREDEPLOY_STUB_LOG="$DOCKER_LOG" REAL_SHA256SUM

assert_mode() {
  local path="$1"
  local expected="$2"
  local actual
  actual="$(stat -c '%a' -- "$path")"
  [[ "$actual" == "$expected" ]] || {
    echo "FAIL: expected mode $expected for $path, got $actual" >&2
    exit 1
  }
}

cat >"$STUB_BIN/sha256sum" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
target=""
for target in "$@"; do :; done
if [[ "$target" == */backups/* && "${PREDEPLOY_IN_CONTAINER:-0}" != "1" ]]; then
  exit 77
fi
exec "${REAL_SHA256SUM:?}" "$@"
STUB
chmod +x "$STUB_BIN/sha256sum"

cat >"$STUB_BIN/docker" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${PREDEPLOY_STUB_LOG:?}"

case "${1:-}" in
  inspect)
    case "${PREDEPLOY_STUB_MODE:-missing}" in
      running) printf 'true\n' ;;
      stopped) printf 'false\n' ;;
      missing) exit 1 ;;
      daemon-error) exit 1 ;;
      *) exit 2 ;;
    esac
    ;;
  info)
    [[ "${PREDEPLOY_STUB_MODE:-missing}" != "daemon-error" ]]
    ;;
  pull)
    [[ "${PREDEPLOY_STUB_FAIL_PULL:-0}" != "1" ]]
    ;;
  compose)
    [[ "${IMAGE_TAG:-}" == "v-contract" ]] || exit 43
    artifact="${PWD}/backups/db/xirang-sqlite-contract.db"
    if [[ "$*" == *'/usr/local/bin/backup-db.sh'* ]]; then
      [[ "${PREDEPLOY_STUB_FAIL_BACKUP:-0}" != "1" ]] || exit 42
      if [[ "$*" == *'chmod 0700 -- /backup'* ]]; then
        chmod 0700 "${PWD}/backups"
      fi
      mkdir -p -- "$(dirname -- "$artifact")"
      printf 'backup fixture\n' >"$artifact"
      PREDEPLOY_IN_CONTAINER=1 sha256sum "$artifact" | awk -v artifact="$artifact" \
        '{print $1 "  " artifact}' >"$artifact.sha256"
      if [[ "$*" == *'exec /usr/local/bin/backup-db.sh'* ]]; then
        chmod 0700 "$(dirname -- "$artifact")"
        chmod 0600 "$artifact" "$artifact.sha256"
      fi
      printf 'BACKUP_FILE=/backup/db/%s\n' "${artifact##*/}"
      exit 0
    fi
    if [[ "$*" == *'chown xirang:xirang'* ]]; then
      [[ "${PREDEPLOY_STUB_FAIL_FINALIZE:-0}" != "1" ]] || exit 45
      chmod 0700 "$(dirname -- "$artifact")"
      chmod 0600 "$artifact" "$artifact.sha256"
      expected="$(awk 'NF {print $1; exit}' "$artifact.sha256")"
      actual="$(PREDEPLOY_IN_CONTAINER=1 sha256sum "$artifact" | awk '{print $1}')"
      [[ "$expected" == "$actual" ]] || exit 46
      exit 0
    fi
    exit 47
    ;;
  *) exit 44 ;;
esac
STUB
chmod +x "$STUB_BIN/docker"

new_case() {
  local name="$1"
  local dir="$WORK/$name"
  mkdir -p "$dir/data"
  printf 'services:\n  xirang: {}\n' >"$dir/docker-compose.yml"
  printf '%s\n' "$dir"
}

umask_failure_dir="$(new_case umask-failure)"
if (cd "$umask_failure_dir" && SCRIPT="$SCRIPT" bash -c '
  umask() { return 92; }
  export -f umask
  IMAGE_TAG=v-contract bash "$SCRIPT"
' >"$WORK/umask-failure-predeploy.out" 2>&1); then
  echo "FAIL: predeploy backup ignored failure to set its private umask" >&2
  exit 1
fi
grep -Fq '无法设置部署前数据库备份私有 umask' "$WORK/umask-failure-predeploy.out" || {
  echo "FAIL: predeploy backup did not fail at private umask setup" >&2
  exit 1
}
[[ ! -e "$umask_failure_dir/backups" ]] || {
  echo "FAIL: predeploy backup created host output after private umask setup failed" >&2
  exit 1
}

first_dir="$(new_case first-deploy)"
: >"$DOCKER_LOG"
first_output="$(cd "$first_dir" && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=missing \
  IMAGE_TAG=v-contract bash "$SCRIPT")"
grep -Fq 'PREDEPLOY_BACKUP=skipped_no_persistent_data' <<<"$first_output" || {
  echo "FAIL: first deployment did not explicitly report a safe skip" >&2
  exit 1
}
if grep -Fq 'compose' "$DOCKER_LOG"; then
  echo "FAIL: first deployment without persistent data attempted a backup" >&2
  exit 1
fi

running_dir="$(new_case running-upgrade)"
mkdir -p "$running_dir/backups"
chmod 0777 "$running_dir/backups"
: >"$DOCKER_LOG"
running_output="$(cd "$running_dir" && umask 022 && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=running \
  IMAGE_TAG=v-contract bash "$SCRIPT")"
grep -Fq 'PREDEPLOY_BACKUP_STATE=running_upgrade' <<<"$running_output" || {
  echo "FAIL: running upgrade state was not reported" >&2
  exit 1
}
grep -Fq 'compose' "$DOCKER_LOG" || {
  echo "FAIL: running upgrade did not execute a Compose-mounted backup" >&2
  exit 1
}
assert_mode "$running_dir/backups" 700
assert_mode "$running_dir/backups/db" 700
assert_mode "$running_dir/backups/db/xirang-sqlite-contract.db" 600
assert_mode "$running_dir/backups/db/xirang-sqlite-contract.db.sha256" 600
grep -Fq 'chown xirang:xirang' "$DOCKER_LOG" || {
  echo "FAIL: predeploy backup did not transfer exact artifacts to the runtime owner" >&2
  exit 1
}

zero_umask_dir="$(new_case running-upgrade-umask-000)"
: >"$DOCKER_LOG"
zero_umask_output="$(cd "$zero_umask_dir" && umask 000 && PATH="$STUB_BIN:$PATH" \
  PREDEPLOY_STUB_MODE=running IMAGE_TAG=v-contract bash "$SCRIPT")"
grep -Fq 'PREDEPLOY_BACKUP=completed' <<<"$zero_umask_output" || {
  echo "FAIL: umask 000 predeploy backup did not complete" >&2
  exit 1
}
assert_mode "$zero_umask_dir/backups" 700
assert_mode "$zero_umask_dir/backups/db" 700
assert_mode "$zero_umask_dir/backups/db/xirang-sqlite-contract.db" 600
assert_mode "$zero_umask_dir/backups/db/xirang-sqlite-contract.db.sha256" 600

stopped_dir="$(new_case stopped-upgrade)"
printf 'persistent data\n' >"$stopped_dir/data/xirang.db"
: >"$DOCKER_LOG"
stopped_output="$(cd "$stopped_dir" && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=stopped \
  IMAGE_TAG=v-contract bash "$SCRIPT")"
grep -Fq 'PREDEPLOY_BACKUP_STATE=stopped_upgrade' <<<"$stopped_output" || {
  echo "FAIL: stopped-container upgrade state was not reported" >&2
  exit 1
}
grep -Fq 'compose' "$DOCKER_LOG" || {
  echo "FAIL: stopped-container upgrade did not execute a backup" >&2
  exit 1
}

missing_data_dir="$(new_case missing-container-with-data)"
printf 'persistent data\n' >"$missing_data_dir/data/xirang.db"
mkdir -p "$missing_data_dir/empty-decoy"
: >"$DOCKER_LOG"
missing_output="$(cd "$missing_data_dir" && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=missing \
  XIRANG_DATA_DIR=empty-decoy IMAGE_TAG=v-contract bash "$SCRIPT")"
grep -Fq 'PREDEPLOY_BACKUP_STATE=persistent_data_without_container' <<<"$missing_output" || {
  echo "FAIL: missing-container persistent-data state was not reported" >&2
  exit 1
}
grep -Fq 'compose' "$DOCKER_LOG" || {
  echo "FAIL: persistent data without a container was not backed up" >&2
  exit 1
}

external_pg_dir="$(new_case missing-container-with-postgres)"
printf '  DB_TYPE = "postgres" # external database\nDB_DSN=postgres://redacted\n' >"$external_pg_dir/.env"
: >"$DOCKER_LOG"
external_pg_output="$(cd "$external_pg_dir" && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=missing \
  IMAGE_TAG=v-contract bash "$SCRIPT")"
grep -Fq 'PREDEPLOY_BACKUP_STATE=persistent_data_without_container' <<<"$external_pg_output" || {
  echo "FAIL: configured PostgreSQL was mistaken for an empty first deployment" >&2
  exit 1
}

failure_dir="$(new_case backup-failure)"
printf 'persistent data\n' >"$failure_dir/data/xirang.db"
if (cd "$failure_dir" && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=missing \
  PREDEPLOY_STUB_FAIL_BACKUP=1 IMAGE_TAG=v-contract bash "$SCRIPT" >/dev/null 2>&1); then
  echo "FAIL: a required backup failure did not block deployment" >&2
  exit 1
fi

finalize_failure_dir="$(new_case finalize-failure)"
printf 'persistent data\n' >"$finalize_failure_dir/data/xirang.db"
if (cd "$finalize_failure_dir" && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=missing \
  PREDEPLOY_STUB_FAIL_FINALIZE=1 IMAGE_TAG=v-contract bash "$SCRIPT" >/dev/null 2>&1); then
  echo "FAIL: exact artifact ownership/permission finalization failure did not block deployment" >&2
  exit 1
fi

daemon_dir="$(new_case daemon-failure)"
if (cd "$daemon_dir" && PATH="$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=daemon-error \
  IMAGE_TAG=v-contract bash "$SCRIPT" >/dev/null 2>&1); then
  echo "FAIL: Docker daemon failure was treated as a safe first deployment" >&2
  exit 1
fi

find_failure_dir="$(new_case unreadable-data-state)"
find_failure_bin="$WORK/find-failure-bin"
mkdir -p "$find_failure_bin"
cat >"$find_failure_bin/find" <<'STUB'
#!/usr/bin/env bash
exit 17
STUB
chmod +x "$find_failure_bin/find"
if (cd "$find_failure_dir" && PATH="$find_failure_bin:$STUB_BIN:$PATH" PREDEPLOY_STUB_MODE=missing \
  IMAGE_TAG=v-contract bash "$SCRIPT" >/dev/null 2>&1); then
  echo "FAIL: unreadable persistent-data state was treated as an empty first deployment" >&2
  exit 1
fi

backup_step="$(awk '
  /- name: Backup database before upgrade/ { in_step=1 }
  /- name: Deploy via SSH/ { in_step=0 }
  in_step { print }
' "$ROOT_DIR/.github/workflows/deploy.yml")"
grep -Fq 'envs: IMAGE_TAG' <<<"$backup_step" || {
  echo "FAIL: manual deploy backup step does not pass IMAGE_TAG to SSH" >&2
  exit 1
}
grep -Fq 'envs: IMAGE_TAG,DEPLOY_PATH' <<<"$backup_step" || {
  echo "FAIL: manual deploy backup step does not pass DEPLOY_PATH to SSH" >&2
  exit 1
}
grep -Fq 'script_path: scripts/predeploy-backup.sh' <<<"$backup_step" || {
  echo "FAIL: manual deploy does not upload and run the tested predeploy backup gate" >&2
  exit 1
}

printf 'predeploy backup three-state contract self-test: PASS\n'

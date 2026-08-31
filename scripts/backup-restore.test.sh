#!/usr/bin/env bash
# Focused contract test for scripts/backup-db.sh and scripts/restore-db.sh.

set -euo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
BACKUP_SCRIPT="$ROOT_DIR/scripts/backup-db.sh"
RESTORE_SCRIPT="$ROOT_DIR/scripts/restore-db.sh"
WORK="$(mktemp -d)"
XIRANG_PID=""
STUB_BIN="$WORK/bin"
DOCKER_STUB_LOG="$WORK/docker.log"
PG_STUB_LOG="$WORK/postgres-client.log"
MODE_LOG="$WORK/modes.log"
cleanup() {
  if [[ -n "$XIRANG_PID" ]]; then
    kill "$XIRANG_PID" 2>/dev/null || true
    wait "$XIRANG_PID" 2>/dev/null || true
  fi
  rm -rf -- "$WORK"
}
trap cleanup EXIT

mkdir -p "$STUB_BIN"
REAL_MV="$(command -v mv)"
REAL_CHMOD="$(command -v chmod)"
export DOCKER_STUB_LOG PG_STUB_LOG MODE_LOG REAL_MV REAL_CHMOD

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

assert_logged_mode() {
  local needle="$1"
  local expected="$2"
  if ! awk -F '\t' -v needle="$needle" -v expected="$expected" '
    index($1, needle) > 0 { found=1; if ($2 != expected) bad=1 }
    END { exit (!found || bad) }
  ' "$MODE_LOG"; then
    echo "FAIL: no $needle temporary artifact was observed exclusively at mode $expected" >&2
    exit 1
  fi
}

umask_failure_backup_dir="$WORK/umask-failure-backups"
if BACKUP_SCRIPT="$BACKUP_SCRIPT" SOURCE_DB="$WORK/umask-probe.db" \
  OUTPUT_DIR="$umask_failure_backup_dir" bash -c '
    umask() { return 92; }
    export -f umask
    DB_TYPE=sqlite SQLITE_PATH="$SOURCE_DB" bash "$BACKUP_SCRIPT" "$OUTPUT_DIR"
  ' >"$WORK/umask-failure-backup.out" 2>&1; then
  echo "FAIL: backup ignored failure to set its private umask" >&2
  exit 1
fi
grep -Fq '无法设置数据库备份私有 umask' "$WORK/umask-failure-backup.out" || {
  echo "FAIL: backup did not fail at private umask setup" >&2
  exit 1
}
[[ ! -e "$umask_failure_backup_dir" ]] || {
  echo "FAIL: backup created output after private umask setup failed" >&2
  exit 1
}

umask_failure_live="$WORK/umask-failure-live.db"
if RESTORE_SCRIPT="$RESTORE_SCRIPT" BACKUP_INPUT="$WORK/umask-probe.db" \
  LIVE_DB="$umask_failure_live" bash -c '
    umask() { return 92; }
    export -f umask
    XIRANG_RESTORE_OFFLINE=1 DB_TYPE=sqlite SQLITE_PATH="$LIVE_DB" \
      bash "$RESTORE_SCRIPT" "$BACKUP_INPUT"
  ' >"$WORK/umask-failure-restore.out" 2>&1; then
  echo "FAIL: restore ignored failure to set its private umask" >&2
  exit 1
fi
grep -Fq '无法设置数据库恢复私有 umask' "$WORK/umask-failure-restore.out" || {
  echo "FAIL: restore did not fail at private umask setup" >&2
  exit 1
}
[[ ! -e "$umask_failure_live" ]] || {
  echo "FAIL: restore created a live database after private umask setup failed" >&2
  exit 1
}

cat >"$STUB_BIN/mv" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
paths=()
after_options=0
for arg in "$@"; do
  if [[ "$arg" == "--" ]]; then
    after_options=1
    continue
  fi
  if [[ "$after_options" -eq 0 && "$arg" == -* ]]; then
    continue
  fi
  paths+=("$arg")
done
if [[ "${#paths[@]}" -ge 2 ]]; then
  source_index=$((${#paths[@]} - 2))
  source_path="${paths[$source_index]}"
  if [[ -e "$source_path" ]]; then
    printf '%s\t%s\n' "$source_path" "$(stat -c '%a' -- "$source_path")" >>"${MODE_LOG:?}"
  fi
fi
exec "${REAL_MV:?}" "$@"
STUB
chmod +x "$STUB_BIN/mv"

# Keep the host's real Docker state out of this contract test. The stub can
# report exactly one named container as running and records which fixed name the
# restore guard inspected.
cat >"$STUB_BIN/docker" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${DOCKER_STUB_LOG:?}"
if [[ "${1:-}" == "inspect" ]]; then
  inspected="${*: -1}"
  if [[ "${DOCKER_STUB_INSPECT_ERROR:-}" == "1" ]]; then
    exit 125
  fi
  if [[ -n "${DOCKER_STUB_RUNNING_NAME:-}" && "${inspected}" == "${DOCKER_STUB_RUNNING_NAME}" ]]; then
    printf 'true\n'
    exit 0
  fi
  exit 1
fi
if [[ "${1:-}" == "ps" ]]; then
  if [[ "${DOCKER_STUB_STATE_QUERY_ERROR:-}" == "1" ]]; then
    exit 125
  fi
  exit 0
fi
exit 1
STUB
chmod +x "$STUB_BIN/docker"

cat >"$STUB_BIN/postgres-client-stub" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
tool="${0##*/}"
for arg in "$@"; do
  printf '%s\t%s\n' "$tool" "$arg" >>"${PG_STUB_LOG:?}"
done
if [[ "${PG_STUB_FAIL_TOOL:-}" == "${tool}" ]]; then
  exit 23
fi
STUB
chmod +x "$STUB_BIN/postgres-client-stub"
ln -s postgres-client-stub "$STUB_BIN/pg_restore"
ln -s postgres-client-stub "$STUB_BIN/psql"

cat >"$STUB_BIN/pg_dump" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
output=""
take_next=0
for arg in "$@"; do
  printf '%s\t%s\n' pg_dump "$arg" >>"${PG_STUB_LOG:?}"
  if [[ "$take_next" -eq 1 ]]; then
    output="$arg"
    take_next=0
    continue
  fi
  case "$arg" in
    --file) take_next=1 ;;
    --file=*) output="${arg#--file=}" ;;
  esac
done
[[ -n "$output" ]] || exit 24
printf 'FAKE_POSTGRES_DUMP_FOR_TEST_ONLY\n' >"$output"
STUB
chmod +x "$STUB_BIN/pg_dump"

live="$WORK/live.db"
source_db="$WORK/source.db"
backup_dir="$WORK/backups"
mkdir -p "$backup_dir"
sqlite3 "$live" "CREATE TABLE values_table(value TEXT); INSERT INTO values_table VALUES ('old');"
sqlite3 "$source_db" "CREATE TABLE values_table(value TEXT); INSERT INTO values_table VALUES ('new');"
chmod 0600 "$live"

: >"$MODE_LOG"
backup_output="$(umask 022; PATH="$STUB_BIN:$PATH" DB_TYPE=sqlite SQLITE_PATH="$source_db" bash "$BACKUP_SCRIPT" "$backup_dir")"
backup_file="$(printf '%s\n' "$backup_output" | awk -F= '$1 == "BACKUP_FILE" {value=substr($0, index($0, "=") + 1)} END {print value}')"
[[ -n "$backup_file" && -s "$backup_file" && -s "$backup_file.sha256" ]] || {
  echo "FAIL: backup artifact or checksum is missing" >&2
  exit 1
}
assert_mode "$backup_dir" 700
assert_mode "$backup_file" 600
assert_mode "$backup_file.sha256" 600
assert_logged_mode ".db.tmp." 600
assert_logged_mode ".sha256.tmp." 600
[[ "$(sqlite3 "$backup_file" 'PRAGMA integrity_check;')" == "ok" ]] || {
  echo "FAIL: backup artifact failed SQLite integrity check" >&2
  exit 1
}

sqlite_zero_dir="$WORK/sqlite-backups-000"
: >"$MODE_LOG"
sqlite_zero_output="$(umask 000; PATH="$STUB_BIN:$PATH" DB_TYPE=sqlite SQLITE_PATH="$source_db" \
  bash "$BACKUP_SCRIPT" "$sqlite_zero_dir")"
sqlite_zero_file="$(printf '%s\n' "$sqlite_zero_output" | awk -F= '$1 == "BACKUP_FILE" {value=substr($0, index($0, "=") + 1)} END {print value}')"
assert_mode "$sqlite_zero_dir" 700
assert_mode "$sqlite_zero_file" 600
assert_mode "$sqlite_zero_file.sha256" 600
assert_logged_mode ".db.tmp." 600
assert_logged_mode ".sha256.tmp." 600

for caller_umask in 000 022; do
  postgres_backup_dir="$WORK/postgres-backups-$caller_umask"
  : >"$MODE_LOG"
  postgres_output="$(umask "$caller_umask"; PATH="$STUB_BIN:$PATH" DB_TYPE=postgres \
    DB_DSN='postgres://FAKE_USER_FOR_TEST_ONLY@contract.invalid/test' \
    bash "$BACKUP_SCRIPT" "$postgres_backup_dir")"
  postgres_backup_file="$(printf '%s\n' "$postgres_output" | awk -F= '$1 == "BACKUP_FILE" {value=substr($0, index($0, "=") + 1)} END {print value}')"
  [[ -n "$postgres_backup_file" && -s "$postgres_backup_file" && -s "$postgres_backup_file.sha256" ]] || {
    echo "FAIL: PostgreSQL stub backup artifact or checksum is missing for umask $caller_umask" >&2
    exit 1
  }
  assert_mode "$postgres_backup_dir" 700
  assert_mode "$postgres_backup_file" 600
  assert_mode "$postgres_backup_file.sha256" 600
  assert_logged_mode ".dump.tmp." 600
  assert_logged_mode ".sha256.tmp." 600
done

restore_input="$WORK/permissive-restore-input.db"
cp -- "$backup_file" "$restore_input"
printf '%s  %s\n' "$(sha256sum -- "$restore_input" | awk '{print $1}')" "$restore_input" >"$restore_input.sha256"
chmod 0644 "$restore_input" "$restore_input.sha256"

if DB_TYPE=sqlite SQLITE_PATH="$live" bash "$RESTORE_SCRIPT" "$restore_input" >/dev/null 2>&1; then
  echo "FAIL: restore accepted a missing offline confirmation" >&2
  exit 1
fi
[[ "$(sqlite3 "$live" 'SELECT value FROM values_table;')" == "old" ]] || {
  echo "FAIL: rejected restore changed the live database" >&2
  exit 1
}

# A detected Xirang process must be rejected even when the maintenance flag is set.
bash -c 'exec -a /usr/local/bin/xirang sleep 30' &
XIRANG_PID=$!
sleep 0.1
if XIRANG_RESTORE_OFFLINE=1 DB_TYPE=sqlite SQLITE_PATH="$live" bash "$RESTORE_SCRIPT" "$restore_input" >/dev/null 2>&1; then
  echo "FAIL: restore accepted a running Xirang process" >&2
  exit 1
fi
kill "$XIRANG_PID" 2>/dev/null || true
wait "$XIRANG_PID" 2>/dev/null || true
XIRANG_PID=""

# An operator-provided container-name override must not bypass the fixed
# Compose service guard. Docker is isolated through the PATH stub above.
: >"$DOCKER_STUB_LOG"
if PATH="$STUB_BIN:$PATH" DOCKER_STUB_RUNNING_NAME=xirang \
  XIRANG_CONTAINER_NAME=unrelated-container XIRANG_RESTORE_OFFLINE=1 \
  DB_TYPE=sqlite SQLITE_PATH="$live" bash "$RESTORE_SCRIPT" "$restore_input" >/dev/null 2>&1; then
  echo "FAIL: restore accepted a running fixed xirang container via a name override" >&2
  exit 1
fi
grep -Fq 'xirang' "$DOCKER_STUB_LOG" || {
  echo "FAIL: restore guard did not inspect the fixed xirang container" >&2
  exit 1
}
if grep -Fq 'unrelated-container' "$DOCKER_STUB_LOG"; then
  echo "FAIL: restore guard honored XIRANG_CONTAINER_NAME" >&2
  exit 1
fi

# A failed inspect is not proof that the fixed Compose container is absent. If
# Docker also cannot answer the fallback state query, the guard must fail closed
# instead of treating an empty state as safe.
: >"$DOCKER_STUB_LOG"
if PATH="$STUB_BIN:$PATH" DOCKER_STUB_INSPECT_ERROR=1 \
  DOCKER_STUB_STATE_QUERY_ERROR=1 XIRANG_RESTORE_OFFLINE=1 \
  DB_TYPE=sqlite SQLITE_PATH="$live" bash "$RESTORE_SCRIPT" "$restore_input" \
  >"$WORK/ambiguous-docker-state.out" 2>&1; then
  echo "FAIL: restore accepted an ambiguous Docker container state" >&2
  exit 1
fi
grep -Fq 'inspect' "$DOCKER_STUB_LOG" || {
  echo "FAIL: restore guard did not inspect the fixed xirang container" >&2
  exit 1
}
grep -Fq 'ps' "$DOCKER_STUB_LOG" || {
  echo "FAIL: restore guard did not run the fallback Docker state query" >&2
  exit 1
}
grep -Fq '无法确认 Xirang Compose 容器状态' "$WORK/ambiguous-docker-state.out" || {
  echo "FAIL: ambiguous Docker state did not return the sanitized fail-closed error" >&2
  exit 1
}

printf 'stale wal' >"$live-wal"
printf 'stale shm' >"$live-shm"
: >"$MODE_LOG"
PATH="$STUB_BIN:$PATH" XIRANG_RESTORE_OFFLINE=1 DB_TYPE=sqlite SQLITE_PATH="$live" \
  bash "$RESTORE_SCRIPT" "$restore_input" >/dev/null
[[ "$(sqlite3 "$live" 'SELECT value FROM values_table;')" == "new" ]] || {
  echo "FAIL: offline restore did not publish the source database" >&2
  exit 1
}
[[ ! -e "$live-wal" && ! -e "$live-shm" ]] || {
  echo "FAIL: offline restore left stale WAL/SHM sidecars" >&2
  exit 1
}
rollback_count="$(find "$WORK" -maxdepth 1 -type f -name 'live.db.before-restore.*' | wc -l)"
[[ "$rollback_count" -eq 1 ]] || {
  echo "FAIL: offline restore did not retain a rollback copy" >&2
  exit 1
}
rollback_file="$(find "$WORK" -maxdepth 1 -type f -name 'live.db.before-restore.*' -print -quit)"
assert_mode "$rollback_file" 600
assert_mode "$live" 600
assert_logged_mode ".restore." 600

for caller_umask in 000 022; do
  private_live="$WORK/restore-$caller_umask/private/live.db"
  : >"$MODE_LOG"
  (umask "$caller_umask"; PATH="$STUB_BIN:$PATH" XIRANG_RESTORE_OFFLINE=1 \
    DB_TYPE=sqlite SQLITE_PATH="$private_live" bash "$RESTORE_SCRIPT" "$restore_input" >/dev/null)
  assert_mode "$(dirname -- "$private_live")" 700
  assert_mode "$private_live" 600
  assert_logged_mode ".restore." 600
done

CHMOD_FAIL_BIN="$WORK/chmod-fail-bin"
mkdir -p "$CHMOD_FAIL_BIN"
cat >"$CHMOD_FAIL_BIN/chmod" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
target=""
for target in "$@"; do :; done
if [[ "$target" == *"${CHMOD_FAIL_PATTERN:?}"* ]]; then
  exit 91
fi
exec "${REAL_CHMOD:?}" "$@"
STUB
chmod +x "$CHMOD_FAIL_BIN/chmod"

chmod_failure_dir="$WORK/chmod-failure-backups"
if (umask 022; PATH="$CHMOD_FAIL_BIN:$STUB_BIN:$PATH" CHMOD_FAIL_PATTERN='.sha256.tmp.' \
  DB_TYPE=sqlite SQLITE_PATH="$source_db" bash "$BACKUP_SCRIPT" "$chmod_failure_dir" \
  >"$WORK/chmod-failure-backup.out" 2>&1); then
  echo "FAIL: backup ignored a checksum permission-hardening failure" >&2
  exit 1
fi
if find "$chmod_failure_dir" -type f -print -quit 2>/dev/null | grep -q .; then
  echo "FAIL: backup permission-hardening failure left an artifact" >&2
  exit 1
fi

chmod_failure_live="$WORK/chmod-failure-live/live.db"
mkdir -p "$(dirname -- "$chmod_failure_live")"
sqlite3 "$chmod_failure_live" "CREATE TABLE values_table(value TEXT); INSERT INTO values_table VALUES ('old');"
chmod 0600 "$chmod_failure_live"
if PATH="$CHMOD_FAIL_BIN:$STUB_BIN:$PATH" CHMOD_FAIL_PATTERN='.restore.' \
  XIRANG_RESTORE_OFFLINE=1 DB_TYPE=sqlite SQLITE_PATH="$chmod_failure_live" \
  bash "$RESTORE_SCRIPT" "$restore_input" >"$WORK/chmod-failure-restore.out" 2>&1; then
  echo "FAIL: restore ignored a temporary-file permission-hardening failure" >&2
  exit 1
fi
[[ "$(sqlite3 "$chmod_failure_live" 'SELECT value FROM values_table;')" == "old" ]] || {
  echo "FAIL: restore permission-hardening failure replaced the live database" >&2
  exit 1
}
assert_mode "$chmod_failure_live" 600

invalid="$WORK/invalid.db"
printf 'not a sqlite database\n' >"$invalid"
printf '%s  %s\n' "$(sha256sum "$invalid" | awk '{print $1}')" "$invalid" >"$invalid.sha256"
if XIRANG_RESTORE_OFFLINE=1 DB_TYPE=sqlite SQLITE_PATH="$live" bash "$RESTORE_SCRIPT" "$invalid" >/dev/null 2>&1; then
  echo "FAIL: restore accepted an invalid SQLite source" >&2
  exit 1
fi

postgres_missing_checksum="$WORK/missing-checksum.dump"
printf 'custom dump fixture\n' >"$postgres_missing_checksum"
: >"$PG_STUB_LOG"
if PATH="$STUB_BIN:$PATH" DB_TYPE=postgres DB_DSN='postgres://contract.invalid/test' \
  bash "$RESTORE_SCRIPT" "$postgres_missing_checksum" >/dev/null 2>&1; then
  echo "FAIL: PostgreSQL restore accepted a backup without a checksum" >&2
  exit 1
fi
[[ ! -s "$PG_STUB_LOG" ]] || {
  echo "FAIL: PostgreSQL client ran before the missing checksum was rejected" >&2
  exit 1
}

postgres_dump="$WORK/valid.dump"
printf 'custom dump fixture\n' >"$postgres_dump"
printf '%s  %s\n' "$(sha256sum "$postgres_dump" | awk '{print $1}')" "$postgres_dump" >"$postgres_dump.sha256"
: >"$PG_STUB_LOG"
PATH="$STUB_BIN:$PATH" DB_TYPE=postgres DB_DSN='postgres://contract.invalid/test' \
  bash "$RESTORE_SCRIPT" "$postgres_dump" >/dev/null
grep -Fxq $'pg_restore\t--single-transaction' "$PG_STUB_LOG" || {
  echo "FAIL: custom dump restore omitted --single-transaction" >&2
  exit 1
}
grep -Fxq $'pg_restore\t--clean' "$PG_STUB_LOG" || {
  echo "FAIL: custom dump restore omitted --clean" >&2
  exit 1
}

postgres_sql="$WORK/valid.sql"
printf 'SELECT 1;\n' >"$postgres_sql"
printf '%s  %s\n' "$(sha256sum "$postgres_sql" | awk '{print $1}')" "$postgres_sql" >"$postgres_sql.sha256"
: >"$PG_STUB_LOG"
PATH="$STUB_BIN:$PATH" DB_TYPE=postgres DB_DSN='postgres://contract.invalid/test' \
  bash "$RESTORE_SCRIPT" "$postgres_sql" >/dev/null
grep -Fxq $'psql\t--single-transaction' "$PG_STUB_LOG" || {
  echo "FAIL: SQL restore omitted --single-transaction" >&2
  exit 1
}
grep -Fxq $'psql\tON_ERROR_STOP=on' "$PG_STUB_LOG" || {
  echo "FAIL: SQL restore omitted ON_ERROR_STOP=on" >&2
  exit 1
}

if PATH="$STUB_BIN:$PATH" PG_STUB_FAIL_TOOL=pg_restore DB_TYPE=postgres \
  DB_DSN='postgres://contract.invalid/test' bash "$RESTORE_SCRIPT" "$postgres_dump" \
  >"$WORK/failed-restore.out" 2>&1; then
  echo "FAIL: PostgreSQL client failure did not fail the restore" >&2
  exit 1
fi
if grep -Fq '恢复完成' "$WORK/failed-restore.out"; then
  echo "FAIL: failed PostgreSQL restore reported success" >&2
  exit 1
fi

if PATH="$STUB_BIN:$PATH" PG_STUB_FAIL_TOOL=psql DB_TYPE=postgres \
  DB_DSN='postgres://contract.invalid/test' bash "$RESTORE_SCRIPT" "$postgres_sql" \
  >"$WORK/failed-sql-restore.out" 2>&1; then
  echo "FAIL: psql failure did not fail the restore" >&2
  exit 1
fi
if grep -Fq '恢复完成' "$WORK/failed-sql-restore.out"; then
  echo "FAIL: failed SQL restore reported success" >&2
  exit 1
fi

printf 'backup and restore contract self-test: PASS\n'

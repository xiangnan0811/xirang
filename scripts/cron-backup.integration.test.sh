#!/usr/bin/env bash
# Real cron-runner round-trip contract for SQLite and PostgreSQL.
#
# This test is intentionally destructive to TEST_POSTGRES_DSN.  The supplied
# PostgreSQL database must be an isolated database that this test may restore.

set -Eeuo pipefail
if ! umask 077; then
  echo "FAIL: cannot set private umask for the cron backup integration test" >&2
  exit 1
fi

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="${ROOT_DIR}/backend"
BACKUP_SCRIPT="${ROOT_DIR}/scripts/backup-db.sh"
RESTORE_SCRIPT="${ROOT_DIR}/scripts/restore-db.sh"
SCRATCH_ROOT="${ROOT_DIR}/.tmp/agent"
POSTGRES_DSN="${TEST_POSTGRES_DSN:-}"
MAX_AGE_HOURS=26

WORK=""
WORK_REAL=""
SCRATCH_REAL=""
PG_TABLE=""

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM

  if [[ -n "${PG_TABLE}" && -n "${POSTGRES_DSN}" ]] && command -v psql >/dev/null 2>&1; then
    psql --set ON_ERROR_STOP=on --dbname "${POSTGRES_DSN}" \
      --command "DROP TABLE IF EXISTS public.${PG_TABLE}" >/dev/null 2>&1 || true
  fi

  if [[ -n "${WORK}" && -n "${WORK_REAL}" && -d "${WORK}" && ! -L "${WORK}" ]]; then
    local current_real
    current_real="$(realpath -e -- "${WORK}" 2>/dev/null || true)"
    if [[ "${current_real}" == "${WORK_REAL}" ]]; then
      rm -rf -- "${WORK}" || status=1
    else
      echo "FAIL: refusing to remove an unexpected integration-test path" >&2
      status=1
    fi
  fi

  exit "${status}"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

[[ -n "${POSTGRES_DSN}" ]] || fail "TEST_POSTGRES_DSN is required; this test never skips PostgreSQL"

for required in go sqlite3 psql pg_dump pg_restore stat realpath mktemp cmp awk find grep basename; do
  command -v "${required}" >/dev/null 2>&1 || fail "${required} is required for the cron backup integration test"
done

if command -v sha256sum >/dev/null 2>&1; then
  CHECKSUM_TOOL="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  CHECKSUM_TOOL="shasum"
else
  fail "sha256sum or shasum is required for the cron backup integration test"
fi

[[ -f "${BACKUP_SCRIPT}" && -x "${BACKUP_SCRIPT}" ]] || fail "scripts/backup-db.sh must be executable"
[[ -f "${RESTORE_SCRIPT}" && -x "${RESTORE_SCRIPT}" ]] || fail "scripts/restore-db.sh must be executable"
[[ -d "${BACKEND_DIR}" && -f "${BACKEND_DIR}/go.mod" ]] || fail "backend module is missing"

[[ ! -L "${ROOT_DIR}/.tmp" && ! -L "${SCRATCH_ROOT}" ]] || fail "refusing a symlinked project scratch path"
[[ ! -e "${ROOT_DIR}/.tmp" || -d "${ROOT_DIR}/.tmp" ]] || fail "project scratch parent is not a directory"
[[ ! -e "${SCRATCH_ROOT}" || -d "${SCRATCH_ROOT}" ]] || fail "project scratch path is not a directory"
mkdir -p -- "${SCRATCH_ROOT}" || fail "cannot create project scratch directory"
SCRATCH_REAL="$(realpath -e -- "${SCRATCH_ROOT}")" || fail "project scratch directory cannot be resolved"
ROOT_REAL="$(realpath -e -- "${ROOT_DIR}")" || fail "repository root cannot be resolved"
[[ "${SCRATCH_REAL}" == "${ROOT_REAL}/.tmp/agent" ]] || fail "project scratch directory escaped the repository"

WORK="$(mktemp -d "${SCRATCH_REAL}/cron-backup.integration.XXXXXX")" || fail "cannot reserve a unique project-local scratch directory"
WORK_REAL="$(realpath -e -- "${WORK}")" || fail "integration scratch directory cannot be resolved"
[[ "${WORK_REAL}" == "${SCRATCH_REAL}"/* ]] || fail "integration scratch directory escaped project scratch"
chmod 0700 -- "${WORK}" || fail "cannot make integration scratch private"
[[ "$(stat -c '%a' -- "${WORK}")" == "700" ]] || fail "integration scratch directory is not mode 0700"

mkdir -m 0700 -- "${WORK}/bin" "${WORK}/go-tmp" "${WORK}/go-cache" || fail "cannot create private build directories"
RUNNER="${WORK}/bin/xirang-cron-db-backup"
BUILD_STDOUT="${WORK}/build.stdout"
BUILD_STDERR="${WORK}/build.stderr"
if ! (
  cd -- "${BACKEND_DIR}"
  GOCACHE="${WORK}/go-cache" GOTMPDIR="${WORK}/go-tmp" \
    go build -o "${RUNNER}" ./cmd/cron-db-backup
) >"${BUILD_STDOUT}" 2>"${BUILD_STDERR}"; then
  fail "failed to build ./cmd/cron-db-backup"
fi
[[ -f "${RUNNER}" && -x "${RUNNER}" && ! -L "${RUNNER}" ]] || fail "cron backup runner was not built privately"

FAIL_SCRIPT="${WORK}/backup-failure.sh"
cat >"${FAIL_SCRIPT}" <<'FAILURE_SCRIPT'
#!/usr/bin/env bash
set -eu
exit 73
FAILURE_SCRIPT
chmod 0700 -- "${FAIL_SCRIPT}" || fail "cannot make failure fixture private"

checksum_for() {
  local path="$1"
  if [[ "${CHECKSUM_TOOL}" == "sha256sum" ]]; then
    sha256sum -- "${path}" | awk '{print $1}'
  else
    shasum -a 256 -- "${path}" | awk '{print $1}'
  fi
}

json_string() {
  local path="$1"
  local key="$2"
  awk -v key="${key}" '
    {
      needle = "\"" key "\":\""
      offset = index($0, needle)
      if (offset > 0) {
        value = substr($0, offset + length(needle))
        sub(/\".*/, "", value)
        print value
        found = 1
        exit
      }
    }
    END { if (!found) exit 1 }
  ' "${path}"
}

json_number() {
  local path="$1"
  local key="$2"
  awk -v key="${key}" '
    {
      needle = "\"" key "\":"
      offset = index($0, needle)
      if (offset > 0) {
        value = substr($0, offset + length(needle))
        sub(/[^0-9].*/, "", value)
        if (value != "") {
          print value
          found = 1
          exit
        }
      }
    }
    END { if (!found) exit 1 }
  ' "${path}"
}

assert_mode() {
  local path="$1"
  local expected="$2"
  local actual
  [[ -e "${path}" && ! -L "${path}" ]] || fail "expected a private path: ${path}"
  actual="$(stat -c '%a' -- "${path}")" || fail "cannot inspect private path: ${path}"
  [[ "${actual}" == "${expected}" ]] || fail "expected mode ${expected} for ${path}, got ${actual}"
}
assert_empty_dir() {
  local path="$1"
  [[ -d "${path}" && ! -L "${path}" ]] || fail "expected a private output directory: ${path}"
  [[ -z "$(find "${path}" -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail "unexpected output remained in ${path}"
}

assert_state_layout() {
  local state_dir="$1"
  local engine="$2"
  local engine_dir="${state_dir}/${engine}"
  local state_file="${engine_dir}/state.json"

  assert_mode "${state_dir}" 700
  assert_mode "${engine_dir}" 700
  assert_mode "${engine_dir}/run.lock" 600
  assert_mode "${engine_dir}/state.lock" 600
  assert_mode "${state_file}" 600
}

initialize_engine() {
  local engine="$1"
  local state_dir="$2"
  local state_file="${state_dir}/${engine}/state.json"
  local snapshot="${WORK}/${engine}.initialized.state"
  local source_id revision

  if ! DB_TYPE="${engine}" CRON_DB_BACKUP_STATE_DIR="${state_dir}" CRON_DB_BACKUP_MAX_AGE_HOURS="${MAX_AGE_HOURS}" \
    "${RUNNER}" init >/dev/null 2>&1; then
    fail "${engine} runner init failed"
  fi
  assert_state_layout "${state_dir}" "${engine}"

  source_id="$(json_string "${state_file}" source_id)" || fail "${engine} init state has no source_id"
  revision="$(json_number "${state_file}" revision)" || fail "${engine} init state has no revision"
  [[ "${source_id}" =~ ^[0-9a-f]{32}$ ]] || fail "${engine} init source_id is not a private 32-byte hex identity"
  [[ "${revision}" == "1" ]] || fail "${engine} init revision is not 1"
  if grep -Fq '"latest_attempt"' "${state_file}" || grep -Fq '"last_success"' "${state_file}"; then
    fail "${engine} init unexpectedly created a run record"
  fi

  cp -- "${state_file}" "${snapshot}" || fail "cannot snapshot ${engine} init state"
  if ! DB_TYPE="${engine}" CRON_DB_BACKUP_STATE_DIR="${state_dir}" CRON_DB_BACKUP_MAX_AGE_HOURS="${MAX_AGE_HOURS}" \
    "${RUNNER}" init >/dev/null 2>&1; then
    fail "${engine} repeated init failed"
  fi
  cmp -s "${snapshot}" "${state_file}" || fail "${engine} repeated init changed state"
}

run_success() {
  local engine="$1"
  local state_dir="$2"
  local output_dir="$3"
  local sqlite_path="${4:-}"

  if [[ "${engine}" == "sqlite" ]]; then
    if ! DB_TYPE=sqlite CRON_DB_BACKUP_STATE_DIR="${state_dir}" CRON_DB_BACKUP_MAX_AGE_HOURS="${MAX_AGE_HOURS}" SQLITE_PATH="${sqlite_path}" \
      TZ=UTC "${RUNNER}" run --script "${BACKUP_SCRIPT}" "${output_dir}" >/dev/null 2>&1; then
      fail "SQLite cron runner failed"
    fi
  else
    if ! DB_TYPE=postgres CRON_DB_BACKUP_STATE_DIR="${state_dir}" CRON_DB_BACKUP_MAX_AGE_HOURS="${MAX_AGE_HOURS}" DB_DSN="${POSTGRES_DSN}" \
      TZ=UTC "${RUNNER}" run --script "${BACKUP_SCRIPT}" "${output_dir}" >/dev/null 2>&1; then
      fail "PostgreSQL cron runner failed"
    fi
  fi
}

run_expected_failure() {
  local engine="$1"
  local state_dir="$2"
  local output_dir="$3"
  local sqlite_path="${4:-}"

  if [[ "${engine}" == "sqlite" ]]; then
    if DB_TYPE=sqlite CRON_DB_BACKUP_STATE_DIR="${state_dir}" CRON_DB_BACKUP_MAX_AGE_HOURS="${MAX_AGE_HOURS}" SQLITE_PATH="${sqlite_path}" \
      TZ=UTC "${RUNNER}" run --script "${FAIL_SCRIPT}" "${output_dir}" >/dev/null 2>&1; then
      fail "SQLite failing cron script was reported as successful"
    fi
  else
    if DB_TYPE=postgres CRON_DB_BACKUP_STATE_DIR="${state_dir}" CRON_DB_BACKUP_MAX_AGE_HOURS="${MAX_AGE_HOURS}" DB_DSN="${POSTGRES_DSN}" \
      TZ=UTC "${RUNNER}" run --script "${FAIL_SCRIPT}" "${output_dir}" >/dev/null 2>&1; then
      fail "PostgreSQL failing cron script was reported as successful"
    fi
  fi
}

assert_success_artifact() {
  local engine="$1"
  local state_dir="$2"
  local output_dir="$3"
  local state_file="${state_dir}/${engine}/state.json"
  local result artifact_name artifact checksum_expected checksum_actual artifact_count artifact_pattern

  result="$(json_string "${state_file}" result)" || fail "${engine} state has no latest result after success"
  [[ "${result}" == "success" ]] || fail "${engine} state did not record a successful receipt"
  artifact_name="$(json_string "${state_file}" artifact_name)" || fail "${engine} state has no successful receipt artifact"
  case "${engine}" in
    sqlite) [[ "${artifact_name}" =~ ^xirang-sqlite-[0-9]{8}-[0-9]{6}\.db$ ]] || fail "SQLite receipt artifact name is invalid" ;;
    postgres) [[ "${artifact_name}" =~ ^xirang-postgres-[0-9]{8}-[0-9]{6}\.dump$ ]] || fail "PostgreSQL receipt artifact name is invalid" ;;
    *) fail "unknown test engine ${engine}" ;;
  esac
  artifact_pattern="xirang-${engine}-*.db"
  [[ "${engine}" == "postgres" ]] && artifact_pattern="xirang-${engine}-*.dump"
  [[ "${artifact_name}" != */* ]] || fail "${engine} receipt contained a path instead of a basename"

  artifact="${output_dir}/${artifact_name}"
  checksum="${artifact}.sha256"
  [[ -f "${artifact}" && ! -L "${artifact}" && -s "${artifact}" ]] || fail "${engine} receipt points to a missing artifact"
  [[ -f "${checksum}" && ! -L "${checksum}" && -s "${checksum}" ]] || fail "${engine} artifact checksum is missing"
  assert_mode "${output_dir}" 700
  assert_mode "${artifact}" 600
  assert_mode "${checksum}" 600

  artifact_count="$(find "${output_dir}" -maxdepth 1 -type f -name "${artifact_pattern}" -printf '%f\n' | awk 'END { print NR + 0 }')"
  [[ "${artifact_count}" == "1" ]] || fail "${engine} success produced an unexpected artifact count"

  checksum_expected="$(awk 'NF {print $1; exit}' "${checksum}")"
  checksum_actual="$(checksum_for "${artifact}")"
  [[ "${checksum_expected}" =~ ^[[:xdigit:]]{64}$ && "${checksum_actual}" == "${checksum_expected}" ]] || {
    fail "${engine} artifact SHA-256 does not match its published checksum"
  }

  if [[ "${engine}" == "sqlite" ]]; then
    [[ "$(sqlite3 "${artifact}" 'PRAGMA integrity_check;' 2>/dev/null)" == "ok" ]] || fail "SQLite artifact failed integrity_check"
  else
    pg_restore --list "${artifact}" >/dev/null 2>&1 || fail "PostgreSQL artifact is not a readable custom dump"
  fi

  printf '%s\n' "${artifact}"
}

assert_failed_preserves_success() {
  local engine="$1"
  local state_dir="$2"
  local output_dir="$3"
  local prior_artifact="$4"
  local sqlite_path="${5:-}"
  local state_file="${state_dir}/${engine}/state.json"
  local result failure_code preserved_artifact revision
  local before_init="${WORK}/${engine}.failure.state"

  run_expected_failure "${engine}" "${state_dir}" "${output_dir}" "${sqlite_path}"

  result="$(json_string "${state_file}" result)" || fail "${engine} failure state has no latest result"
  failure_code="$(json_string "${state_file}" failure_code)" || fail "${engine} failure state has no failure code"
  [[ "${result}" == "failed" && "${failure_code}" == "backup_failed" ]] || fail "${engine} failure was not recorded as backup_failed"
  preserved_artifact="$(json_string "${state_file}" artifact_name)" || fail "${engine} failure discarded last_success"
  [[ "${preserved_artifact}" == "$(basename -- "${prior_artifact}")" ]] || fail "${engine} failure changed last_success artifact"
  [[ -f "${prior_artifact}" && -s "${prior_artifact}" ]] || fail "${engine} failure removed the successful artifact"
  revision="$(json_number "${state_file}" revision)" || fail "${engine} failure state has no revision"
  [[ "${revision}" =~ ^[0-9]+$ && "${revision}" -gt 2 ]] || fail "${engine} failure did not advance state revision"

  cp -- "${state_file}" "${before_init}" || fail "cannot snapshot ${engine} failure state"
  if ! DB_TYPE="${engine}" CRON_DB_BACKUP_STATE_DIR="${state_dir}" CRON_DB_BACKUP_MAX_AGE_HOURS="${MAX_AGE_HOURS}" \
    "${RUNNER}" init >/dev/null 2>&1; then
    fail "${engine} init after failure failed"
  fi
  cmp -s "${before_init}" "${state_file}" || fail "${engine} init after failure reset durable state"
}

SQLITE_ROOT="${WORK}/sqlite"
SQLITE_STATE="${SQLITE_ROOT}/state"
SQLITE_OUTPUT="${SQLITE_ROOT}/backups"
SQLITE_SOURCE="${SQLITE_ROOT}/source.db"
SQLITE_LIVE="${SQLITE_ROOT}/live.db"
mkdir -m 0700 -- "${SQLITE_ROOT}" "${SQLITE_OUTPUT}" || fail "cannot create private SQLite fixture directories"

sqlite3 "${SQLITE_SOURCE}" <<'SQL' >/dev/null
CREATE TABLE cron_roundtrip (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO cron_roundtrip (id, value) VALUES (1, 'before-backup');
SQL
sqlite3 "${SQLITE_LIVE}" <<'SQL' >/dev/null
CREATE TABLE cron_roundtrip (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO cron_roundtrip (id, value) VALUES (1, 'live-before-restore');
SQL
chmod 0600 -- "${SQLITE_SOURCE}" "${SQLITE_LIVE}" || fail "cannot make SQLite fixture databases private"
RECEIPT_INVALID_EMPTY="${SQLITE_ROOT}/receipt-invalid-empty"
RECEIPT_INVALID_OTHER="${SQLITE_ROOT}/receipt-invalid-other"
RECEIPT_CLOSED_FD="${SQLITE_ROOT}/receipt-closed-fd"
mkdir -m 0700 -- "${RECEIPT_INVALID_EMPTY}" "${RECEIPT_INVALID_OTHER}" "${RECEIPT_CLOSED_FD}" || fail "cannot create private receipt fixtures"

if DB_TYPE=sqlite SQLITE_PATH="${SQLITE_SOURCE}" XIRANG_BACKUP_RECEIPT_FD="" \
  bash "${BACKUP_SCRIPT}" "${RECEIPT_INVALID_EMPTY}" >/dev/null 2>&1; then
  fail "backup accepted an explicitly empty receipt FD"
fi
assert_empty_dir "${RECEIPT_INVALID_EMPTY}"

if DB_TYPE=sqlite SQLITE_PATH="${SQLITE_SOURCE}" XIRANG_BACKUP_RECEIPT_FD=4 \
  bash "${BACKUP_SCRIPT}" "${RECEIPT_INVALID_OTHER}" >/dev/null 2>&1; then
  fail "backup accepted a receipt FD other than 3"
fi
assert_empty_dir "${RECEIPT_INVALID_OTHER}"

if (exec 3>&-; DB_TYPE=sqlite SQLITE_PATH="${SQLITE_SOURCE}" XIRANG_BACKUP_RECEIPT_FD=3 \
  bash "${BACKUP_SCRIPT}" "${RECEIPT_CLOSED_FD}") >"${WORK}/receipt-closed.stdout" 2>"${WORK}/receipt-closed.stderr"; then
  fail "backup reported success with a closed receipt FD"
fi
assert_empty_dir "${RECEIPT_CLOSED_FD}"


initialize_engine sqlite "${SQLITE_STATE}"
run_success sqlite "${SQLITE_STATE}" "${SQLITE_OUTPUT}" "${SQLITE_SOURCE}"
SQLITE_ARTIFACT="$(assert_success_artifact sqlite "${SQLITE_STATE}" "${SQLITE_OUTPUT}")"

sqlite3 "${SQLITE_LIVE}" "UPDATE cron_roundtrip SET value = 'after-backup' WHERE id = 1;" >/dev/null
if ! XIRANG_RESTORE_OFFLINE=1 DB_TYPE=sqlite SQLITE_PATH="${SQLITE_LIVE}" \
  TZ=UTC bash "${RESTORE_SCRIPT}" "${SQLITE_ARTIFACT}" >/dev/null 2>&1; then
  fail "SQLite offline restore failed"
fi
[[ "$(sqlite3 "${SQLITE_LIVE}" 'SELECT value FROM cron_roundtrip WHERE id = 1;' 2>/dev/null)" == "before-backup" ]] || fail "SQLite offline restore did not recover the known value"
[[ "$(sqlite3 "${SQLITE_LIVE}" 'PRAGMA integrity_check;' 2>/dev/null)" == "ok" ]] || fail "restored SQLite database failed integrity_check"

assert_failed_preserves_success sqlite "${SQLITE_STATE}" "${SQLITE_OUTPUT}" "${SQLITE_ARTIFACT}" "${SQLITE_SOURCE}"

PG_ROOT="${WORK}/postgres"
PG_STATE="${PG_ROOT}/state"
PG_OUTPUT="${PG_ROOT}/backups"
mkdir -m 0700 -- "${PG_ROOT}" "${PG_OUTPUT}" || fail "cannot create private PostgreSQL fixture directories"
PG_TABLE="cron_backup_roundtrip_${BASHPID}_${RANDOM}"

if ! psql --set ON_ERROR_STOP=on --dbname "${POSTGRES_DSN}" >/dev/null 2>&1 <<SQL
CREATE TABLE public.${PG_TABLE} (id integer PRIMARY KEY, value text NOT NULL);
INSERT INTO public.${PG_TABLE} (id, value) VALUES (1, 'before-backup');
SQL
then
  fail "cannot create the isolated PostgreSQL round-trip fixture"
fi

initialize_engine postgres "${PG_STATE}"
run_success postgres "${PG_STATE}" "${PG_OUTPUT}"
POSTGRES_ARTIFACT="$(assert_success_artifact postgres "${PG_STATE}" "${PG_OUTPUT}")"

if ! psql --set ON_ERROR_STOP=on --dbname "${POSTGRES_DSN}" \
  --command "UPDATE public.${PG_TABLE} SET value = 'after-backup' WHERE id = 1" >/dev/null 2>&1; then
  fail "cannot mutate the PostgreSQL fixture before restore"
fi
if ! DB_TYPE=postgres DB_DSN="${POSTGRES_DSN}" \
  bash "${RESTORE_SCRIPT}" "${POSTGRES_ARTIFACT}" >/dev/null 2>&1; then
  fail "PostgreSQL offline restore failed"
fi
POSTGRES_VALUE="$(psql --set ON_ERROR_STOP=on --tuples-only --no-align \
  --dbname "${POSTGRES_DSN}" --command "SELECT value FROM public.${PG_TABLE} WHERE id = 1" 2>/dev/null | awk 'NF {print; exit}')"
[[ "${POSTGRES_VALUE}" == "before-backup" ]] || fail "PostgreSQL offline restore did not recover the known value"

assert_failed_preserves_success postgres "${PG_STATE}" "${PG_OUTPUT}" "${POSTGRES_ARTIFACT}"

printf 'cron backup SQLite and PostgreSQL runner round-trip: PASS\n'

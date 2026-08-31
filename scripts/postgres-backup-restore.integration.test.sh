#!/usr/bin/env bash
# Destructive round-trip contract for an isolated PostgreSQL test database.

set -euo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
BACKUP_SCRIPT="$ROOT_DIR/scripts/backup-db.sh"
RESTORE_SCRIPT="$ROOT_DIR/scripts/restore-db.sh"
dsn="${TEST_POSTGRES_DSN:-}"

if [[ -z "${dsn}" ]]; then
  echo "FAIL: TEST_POSTGRES_DSN is required for the PostgreSQL backup/restore integration test" >&2
  exit 1
fi

for tool in psql pg_dump pg_restore; do
  command -v "${tool}" >/dev/null 2>&1 || {
    echo "FAIL: ${tool} is required for the PostgreSQL backup/restore integration test" >&2
    exit 1
  }
done

WORK="$(mktemp -d)"
contract_table="backup_restore_roundtrip_${BASHPID}"
cleanup() {
  psql --set ON_ERROR_STOP=on --dbname "${dsn}" \
    --command "DROP TABLE IF EXISTS public.${contract_table}" >/dev/null 2>&1 || true
  rm -rf -- "$WORK"
}
trap cleanup EXIT

psql --set ON_ERROR_STOP=on --dbname "${dsn}" >/dev/null <<SQL
CREATE TABLE public.${contract_table} (id integer PRIMARY KEY, value text NOT NULL);
INSERT INTO public.${contract_table} (id, value) VALUES (1, 'before-backup');
SQL

backup_output="$(umask 022; DB_TYPE=postgres DB_DSN="${dsn}" bash "$BACKUP_SCRIPT" "$WORK")"
backup_file="$(printf '%s\n' "$backup_output" | awk -F= '
  $1 == "BACKUP_FILE" { value=substr($0, index($0, "=") + 1) }
  END { print value }
')"
[[ -n "${backup_file}" && -s "${backup_file}" && -s "${backup_file}.sha256" ]] || {
  echo "FAIL: PostgreSQL backup did not publish an artifact and checksum" >&2
  exit 1
}
[[ "$(stat -c '%a' -- "$WORK")" == 700 ]] || {
  echo "FAIL: PostgreSQL backup directory is not mode 0700" >&2
  exit 1
}
for private_file in "$backup_file" "$backup_file.sha256"; do
  [[ "$(stat -c '%a' -- "$private_file")" == 600 ]] || {
    echo "FAIL: PostgreSQL backup artifact is not mode 0600: $private_file" >&2
    exit 1
  }
done

psql --set ON_ERROR_STOP=on --dbname "${dsn}" \
  --command "UPDATE public.${contract_table} SET value = 'after-backup' WHERE id = 1" >/dev/null

DB_TYPE=postgres DB_DSN="${dsn}" bash "$RESTORE_SCRIPT" "$backup_file" >/dev/null

restored_value="$(psql --set ON_ERROR_STOP=on --tuples-only --no-align --dbname "${dsn}" \
  --command "SELECT value FROM public.${contract_table} WHERE id = 1")"
[[ "${restored_value}" == "before-backup" ]] || {
  echo "FAIL: PostgreSQL round-trip restored '${restored_value}', expected 'before-backup'" >&2
  exit 1
}

printf 'PostgreSQL backup and restore integration round-trip: PASS\n'

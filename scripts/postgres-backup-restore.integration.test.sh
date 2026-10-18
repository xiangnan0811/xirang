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

for tool in psql pg_dump pg_restore sha256sum cmp; do
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

# Use real pg_dump products for every spelling, including the no-argument
# default. Each pair must remain usable after moving to another mount root
# without rewriting its checksum record.
for output_case in absolute default relative noncanonical trailing_slash; do
  (
    case_root="$WORK/$output_case"
    mkdir -p -- "$case_root"
    cd -- "$case_root"
    case "$output_case" in
      absolute) output_dir="$case_root/backups" ;;
      default) output_dir="./backups" ;;
      relative) output_dir="relative-backups" ;;
      noncanonical)
        mkdir -p -- path-parent
        output_dir="path-parent/../noncanonical-backups"
        ;;
      trailing_slash) output_dir="trailing-backups/" ;;
    esac
    backup_args=()
    [[ "$output_case" == default ]] || backup_args+=("$output_dir")
    backup_output="$(umask 022; DB_TYPE=postgres DB_DSN="${dsn}" \
      XIRANG_BACKUP_RECEIPT_FD=3 bash "$BACKUP_SCRIPT" "${backup_args[@]}" 3>receipt)"
    backup_file="$(printf '%s\n' "$backup_output" | awk -F= '
      $1 == "BACKUP_FILE" { value=substr($0, index($0, "=") + 1) }
      END { print value }
    ')"
    backup_name="${backup_file##*/}"
    [[ "$backup_name" =~ ^xirang-postgres-[0-9]{8}-[0-9]{6}\.dump$ && \
      "$backup_file" == "$output_dir/$backup_name" && \
      -s "$backup_file" && -s "$backup_file.sha256" ]] || {
      echo "FAIL: $output_case PostgreSQL backup changed its BACKUP_FILE path contract" >&2
      exit 1
    }
    cmp -s receipt <(printf '%s\n' "$backup_name") || {
      echo "FAIL: $output_case PostgreSQL backup changed its basename receipt contract" >&2
      exit 1
    }
    [[ "$(stat -c '%a' -- "$output_dir")" == 700 ]] || {
      echo "FAIL: PostgreSQL backup directory is not mode 0700" >&2
      exit 1
    }
    for private_file in "$backup_file" "$backup_file.sha256"; do
      [[ "$(stat -c '%a' -- "$private_file")" == 600 ]] || {
        echo "FAIL: PostgreSQL backup artifact is not mode 0600: $private_file" >&2
        exit 1
      }
    done
    read -r digest checksum_label <"$backup_file.sha256"
    [[ "$digest" =~ ^[0-9a-f]{64}$ && "$checksum_label" == "$backup_name" && \
      "$(wc -l <"$backup_file.sha256")" -eq 1 ]] || {
      echo "FAIL: $output_case PostgreSQL backup did not publish one basename checksum record" >&2
      exit 1
    }
    (cd -- "$output_dir"; sha256sum --strict --check -- "$backup_name.sha256") >/dev/null
    mkdir -p -- relocated-mount
    cp -- "$backup_file" "$backup_file.sha256" relocated-mount/
    rm -rf -- "$output_dir"
    (cd -- relocated-mount; sha256sum --strict --check -- "$backup_name.sha256") >/dev/null
    pg_restore --list "relocated-mount/$backup_name" >/dev/null

    psql --set ON_ERROR_STOP=on --dbname "${dsn}" \
      --command "UPDATE public.${contract_table} SET value = 'after-backup' WHERE id = 1" >/dev/null
    DB_TYPE=postgres DB_DSN="${dsn}" \
      bash "$RESTORE_SCRIPT" "relocated-mount/$backup_name" >/dev/null
    restored_value="$(psql --set ON_ERROR_STOP=on --tuples-only --no-align --dbname "${dsn}" \
      --command "SELECT value FROM public.${contract_table} WHERE id = 1")"
    [[ "${restored_value}" == "before-backup" ]] || {
      echo "FAIL: $output_case PostgreSQL round-trip restored '${restored_value}', expected 'before-backup'" >&2
      exit 1
    }
  )
done

printf 'PostgreSQL backup and restore integration round-trip: PASS\n'

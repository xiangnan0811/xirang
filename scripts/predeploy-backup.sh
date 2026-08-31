#!/usr/bin/env bash
# Fail-closed database backup gate used immediately before a manual deployment.

set -euo pipefail

fail() {
  echo "❌ $*" >&2
  exit 1
}

if ! umask 077; then
  fail "无法设置部署前数据库备份私有 umask"
fi

image_tag="${IMAGE_TAG:-}"
compose_file="docker-compose.yml"
data_dir="data"
backup_dir="backups"

if [[ -n "${DEPLOY_PATH:-}" ]]; then
  cd -- "${DEPLOY_PATH}" || fail "无法进入部署目录"
fi

[[ -n "${image_tag}" ]] || fail "IMAGE_TAG 未设置，拒绝执行部署前备份"
[[ -f "${compose_file}" ]] || fail "Compose 文件不存在：${compose_file}"
command -v docker >/dev/null 2>&1 || fail "未找到 Docker CLI，无法判断部署状态"

container_state="missing"
if inspected_state="$(docker inspect --format='{{.State.Running}}' xirang 2>/dev/null)"; then
  case "${inspected_state}" in
    true) container_state="running" ;;
    false) container_state="stopped" ;;
    *) fail "无法识别 xirang 容器状态：${inspected_state}" ;;
  esac
else
  # docker inspect uses the same non-zero status for an absent container and an
  # unreachable daemon. Only the former can qualify as a first deployment.
  docker info >/dev/null 2>&1 || fail "Docker daemon 不可用，拒绝将状态视为首次部署"
fi

has_local_data=0
if [[ -e "${data_dir}" || -L "${data_dir}" ]]; then
  [[ -d "${data_dir}" ]] || fail "持久数据路径不是目录：${data_dir}"
  if ! first_data_entry="$(find "${data_dir}" -mindepth 1 -print -quit 2>/dev/null)"; then
    fail "无法检查持久数据目录，拒绝将状态视为首次部署"
  fi
  [[ -n "${first_data_entry}" ]] && has_local_data=1
fi

uses_postgres=0
if [[ -f .env ]]; then
  configured_db_type="$(awk -F= '
    /^[[:space:]]*#/ { next }
    /^[[:space:]]*DB_TYPE[[:space:]]*=/ {
      value = substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      first = substr(value, 1, 1)
      single_quote = sprintf("%c", 39)
      if (first == "\"" || first == single_quote) {
        value = substr(value, 2)
        closing_quote = index(value, first)
        if (closing_quote > 0) {
          value = substr(value, 1, closing_quote - 1)
        }
      } else {
        sub(/[[:space:]]+#.*$/, "", value)
      }
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      print tolower(value)
      exit
    }
  ' .env)"
  [[ "${configured_db_type}" == "postgres" ]] && uses_postgres=1
fi

if [[ "${container_state}" == "missing" && "${has_local_data}" -eq 0 && "${uses_postgres}" -eq 0 ]]; then
  echo "PREDEPLOY_BACKUP=skipped_no_persistent_data"
  echo "首次部署且未检测到持久数据，明确跳过升级前备份"
  exit 0
fi

case "${container_state}" in
  running) backup_state="running_upgrade" ;;
  stopped) backup_state="stopped_upgrade" ;;
  missing) backup_state="persistent_data_without_container" ;;
  *) fail "未知部署状态：${container_state}" ;;
esac
echo "PREDEPLOY_BACKUP_STATE=${backup_state}"

mkdir -p -- "${backup_dir}"
[[ -d "${backup_dir}" ]] || fail "宿主备份路径不是目录：${backup_dir}"
image="docker.io/linnea7171/xirang:${image_tag}"
docker pull "${image}"

# Compose run preserves the service's env_file, network, and persistent mounts.
# It therefore works both for a running upgrade and when the old container has
# stopped or disappeared while its data remains.
backup_output="$(IMAGE_TAG="${image_tag}" docker compose -f "${compose_file}" run --rm --no-deps \
  --entrypoint /bin/sh xirang -ceu '
    chown xirang:xirang -- /backup
    chmod 0700 -- /backup
    [ "$(stat -c "%a" -- /backup)" = 700 ]
    exec /usr/local/bin/backup-db.sh /backup/db
  ')"
printf '%s\n' "${backup_output}"

backup_file="$(printf '%s\n' "${backup_output}" | awk -F= '
  $1 == "BACKUP_FILE" { value=substr($0, index($0, "=") + 1) }
  END { print value }
')"
[[ -n "${backup_file}" ]] || fail "数据库备份命令未报告产物"

case "${backup_file}" in
  /backup/*)
    relative_backup="${backup_file#/backup/}"
    ;;
  *)
    fail "数据库备份路径越过 /backup：${backup_file}"
    ;;
esac
case "/${relative_backup}/" in
  *'/../'*|*'/./'*|*'//'*) fail "数据库备份路径包含不安全片段：${backup_file}" ;;
esac
[[ -n "${relative_backup}" && "${relative_backup}" != /* ]] || \
  fail "数据库备份路径无效：${backup_file}"

host_backup="${backup_dir}/${relative_backup}"
container_backup_dir="${backup_file%/*}"
if ! IMAGE_TAG="${image_tag}" docker compose -f "${compose_file}" run --rm --no-deps \
  --entrypoint /bin/sh xirang -ceu '
    backup_file="$1"
    checksum_file="$2"
    backup_dir="$3"
    [ -s "${backup_file}" ] && [ -s "${checksum_file}" ]
    chown xirang:xirang -- "${backup_dir}" "${backup_file}" "${checksum_file}"
    chmod 0700 -- "${backup_dir}"
    chmod 0600 -- "${backup_file}" "${checksum_file}"
    [ "$(stat -c "%a" -- "${backup_dir}")" = 700 ]
    [ "$(stat -c "%a" -- "${backup_file}")" = 600 ]
    [ "$(stat -c "%a" -- "${checksum_file}")" = 600 ]
    expected="$(awk "NF {print \$1; exit}" "${checksum_file}")"
    actual="$(sha256sum -- "${backup_file}" | awk "{print \$1}")"
    case "${expected}" in
      ""|*[!0-9a-fA-F]*) exit 1 ;;
    esac
    [ "${#expected}" -eq 64 ] && [ "${expected}" = "${actual}" ]
  ' sh "${backup_file}" "${backup_file}.sha256" "${container_backup_dir}" >/dev/null; then
  fail "数据库备份权限、所有权或校验确认失败：${host_backup}"
fi

echo "PREDEPLOY_BACKUP=completed"
echo "PREDEPLOY_BACKUP_FILE=${host_backup}"

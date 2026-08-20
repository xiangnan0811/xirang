#!/bin/sh
set -eu

WORKER_UID=10000
WORKER_GID=10000
UPDATER_UID=10002
UPDATER_GID=10002
RUNTIME_ROOT=/run/xirang
WORKER_SOCKET_DIR=$RUNTIME_ROOT/worker
WORKER_SOCKET=$WORKER_SOCKET_DIR/asset-worker.sock
UPDATER_SOCKET=$RUNTIME_ROOT/asset-worker-updater.sock
WORKSPACE_ROOT=$RUNTIME_ROOT/asset-jobs
BUNDLE_ROOT=/var/lib/xirang/asset-worker-bundles
DERIVED_ROOT=/var/lib/xirang-asset-runtime/derived
EXPORT_ROOT=/var/lib/xirang-asset-runtime/export
INBOX_ROOT=/var/lib/xirang/asset-worker-inbox
TRUST_FILE=/run/secrets/asset-worker-updater-trust.json

fail() {
  echo "asset-worker entrypoint rejected the runtime contract" >&2
  exit 1
}

require_identity() {
  [ "$(id -u)" = "$1" ] || fail
  [ "$(id -g)" = "$2" ] || fail
}

require_exact_groups() {
	expected_gid=$1
	group_ids=$(id -G) || fail
	[ "$group_ids" = "$expected_gid" ] || fail
}

require_directory() {
  path=$1
  mode=$2
  uid=$3
  gid=$4
  [ -d "$path" ] && [ ! -L "$path" ] || fail
  [ "$(stat -c '%a:%u:%g' "$path")" = "$mode:$uid:$gid" ] || fail
}

require_file() {
  path=$1
  mode=$2
  uid=$3
  gid=$4
  [ -f "$path" ] && [ ! -L "$path" ] || fail
  [ "$(stat -c '%a:%u:%g' "$path")" = "$mode:$uid:$gid" ] || fail
}

require_socket() {
  path=$1
  mode=$2
  uid=$3
  gid=$4
  [ -S "$path" ] || fail
  [ "$(stat -c '%a:%u:%g' "$path")" = "$mode:$uid:$gid" ] || fail
}

require_absent() {
  [ ! -e "$1" ] && [ ! -L "$1" ] || fail
}

require_mount_option() {
  target=$1
  option=$2
  awk -v target="$target" -v option="$option" '
    $5 == target {
      count = split($6, options, ",")
      for (field = 1; field <= count; field++) {
        if (options[field] == option) {
          found = 1
        }
      }
    }
    END { exit found ? 0 : 1 }
  ' /proc/self/mountinfo || fail
}

require_private_tmpfs() {
  target=$1
  uid=$2
  gid=$3
  require_directory "$target" 700 "$uid" "$gid"
  awk -v target="$target" '
    $5 == target {
      exact_mounts++
      separator = 0
      for (field = 7; field <= NF; field++) {
        if ($field == "-") {
          separator = field
          break
        }
      }
      if (separator == 0) {
        next
      }
      filesystem = $(separator + 1)
      for (group = 1; group <= 2; group++) {
        value = group == 1 ? $6 : $(separator + 3)
        count = split(value, options, ",")
        for (field = 1; field <= count; field++) {
          if (options[field] == "rw") rw = 1
          if (options[field] == "noexec") noexec = 1
          if (options[field] == "nosuid") nosuid = 1
          if (options[field] == "nodev") nodev = 1
        }
      }
    }
    END { exit exact_mounts == 1 && filesystem == "tmpfs" && rw && noexec && nosuid && nodev ? 0 : 1 }
  ' /proc/self/mountinfo || fail
}

run_worker() {
	require_identity "$WORKER_UID" "$WORKER_GID"
	require_exact_groups "$WORKER_GID"
  require_private_tmpfs /tmp "$WORKER_UID" "$WORKER_GID"
  require_directory "$WORKER_SOCKET_DIR" 700 "$WORKER_UID" "$WORKER_GID"
  require_private_tmpfs "$WORKSPACE_ROOT" "$WORKER_UID" "$WORKER_GID"
  require_directory "$BUNDLE_ROOT" 2750 "$UPDATER_UID" "$WORKER_GID"
  require_socket "$WORKER_SOCKET" 600 "$WORKER_UID" "$WORKER_GID"
  require_absent "$UPDATER_SOCKET"
  require_mount_option "$WORKER_SOCKET_DIR" ro
  require_mount_option "$WORKSPACE_ROOT" rw
  require_mount_option "$WORKSPACE_ROOT" noexec
  require_mount_option "$WORKSPACE_ROOT" nosuid
  require_mount_option "$WORKSPACE_ROOT" nodev
  require_mount_option "$BUNDLE_ROOT" ro
  exec /usr/local/bin/asset-worker \
    --local-socket "$WORKER_SOCKET" \
    --workspace-root "$WORKSPACE_ROOT"
}

run_updater() {
	require_identity "$UPDATER_UID" "$UPDATER_GID"
	require_exact_groups "$UPDATER_GID"
  require_directory "$RUNTIME_ROOT" 2770 "$WORKER_UID" "$UPDATER_GID"
  require_directory "$BUNDLE_ROOT" 2750 "$UPDATER_UID" "$WORKER_GID"
  require_directory "$INBOX_ROOT" 555 "$UPDATER_UID" "$UPDATER_GID"
  require_directory /tmp 700 "$UPDATER_UID" "$UPDATER_GID"
  require_file "$TRUST_FILE" 440 "$UPDATER_UID" "$UPDATER_GID"
  require_socket "$UPDATER_SOCKET" 660 "$WORKER_UID" "$UPDATER_GID"
  require_absent "$WORKER_SOCKET"
  require_mount_option "$RUNTIME_ROOT" ro
  require_mount_option "$BUNDLE_ROOT" rw
  require_mount_option "$INBOX_ROOT" ro
  require_mount_option /tmp rw
  require_mount_option /tmp noexec
  require_mount_option /tmp nosuid
  require_mount_option /tmp nodev
  exec /usr/local/bin/asset-worker-updater
}

initialize_volumes() {
  require_identity 0 0
  [ -d "$RUNTIME_ROOT" ] && [ ! -L "$RUNTIME_ROOT" ] || fail
  [ -d "$BUNDLE_ROOT" ] && [ ! -L "$BUNDLE_ROOT" ] || fail
  [ -d "$DERIVED_ROOT" ] && [ ! -L "$DERIVED_ROOT" ] || fail
  [ -d "$EXPORT_ROOT" ] && [ ! -L "$EXPORT_ROOT" ] || fail
  require_mount_option "$RUNTIME_ROOT" rw
  require_mount_option "$WORKER_SOCKET_DIR" rw
  require_mount_option "$BUNDLE_ROOT" rw
  require_mount_option "$DERIVED_ROOT" rw
  require_mount_option "$EXPORT_ROOT" rw

  mkdir -p "$WORKER_SOCKET_DIR"
  chown "$WORKER_UID:$UPDATER_GID" "$RUNTIME_ROOT"
  chmod 2770 "$RUNTIME_ROOT"
  chown "$WORKER_UID:$WORKER_GID" "$WORKER_SOCKET_DIR"
  chmod 0700 "$WORKER_SOCKET_DIR"
  chown "$UPDATER_UID:$WORKER_GID" "$BUNDLE_ROOT"
  chmod 2750 "$BUNDLE_ROOT"
  chown "$WORKER_UID:$WORKER_GID" "$DERIVED_ROOT"
  chmod 0700 "$DERIVED_ROOT"
  require_directory "$DERIVED_ROOT" 700 "$WORKER_UID" "$WORKER_GID"
  chown "$WORKER_UID:$WORKER_GID" "$EXPORT_ROOT"
  chmod 0700 "$EXPORT_ROOT"
  require_directory "$EXPORT_ROOT" 700 "$WORKER_UID" "$WORKER_GID"
}

case "${1:-}" in
  worker)
    [ "$#" -eq 1 ] || fail
    run_worker
    ;;
  updater)
    [ "$#" -eq 1 ] || fail
    run_updater
    ;;
  init)
    [ "$#" -eq 1 ] || fail
    initialize_volumes
    ;;
  *) fail ;;
esac

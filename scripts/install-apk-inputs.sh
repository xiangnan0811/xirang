#!/bin/sh
# Consume a digest-pinned input image without consulting an Alpine repository.
set -eu
export LC_ALL=C

inventory() {
    awk 'BEGIN { RS=""; FS="\n" }
        { name=""; version=""; arch="";
          for (i=1; i<=NF; i++) {
            if (substr($i,1,2)=="P:") name=substr($i,3);
            if (substr($i,1,2)=="V:") version=substr($i,3);
            if (substr($i,1,2)=="A:") arch=substr($i,3);
          }
          if (name=="" || version=="" || arch=="") exit 1;
          print name "\t" version "\t" arch;
        }' /lib/apk/db/installed
}

if [ "${1:-}" = --inventory ]; then
    inventory
    exit
fi

fail() { printf 'APK input verification failed: %s\n' "$*" >&2; exit 1; }
[ "$#" -ge 4 ] || fail 'role, target architecture and explicit package roots are required'
role=$1
arch=$2
shift 2
[ "$1" = -- ] || fail 'missing package-root separator'
shift
case "$arch" in amd64) alpine_arch=x86_64 ;; arm64) alpine_arch=aarch64 ;; *) fail 'unsupported target architecture' ;; esac
input_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[ "$(cat "$input_dir/role.txt")" = "$role" ] || fail 'input role mismatch'
[ "$(cat "$input_dir/platform.txt")" = "linux/$arch" ] || fail 'input platform mismatch'
[ "$(apk --print-arch)" = "$alpine_arch" ] || fail 'native APK architecture mismatch'
[ "$(cat "$input_dir/alpine-arch.txt")" = "$alpine_arch" ] || fail 'declared APK architecture mismatch'
[ "$(apk --version)" = "$(cat "$input_dir/apk-tools.txt")" ] || fail 'apk-tools identity mismatch'

tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT HUP INT TERM
printf '%s\n' "$@" | sort >"$tmp/roots.txt"
cmp -s "$tmp/roots.txt" "$input_dir/roots.txt" || fail 'explicit package roots differ from the snapshot'
inventory >"$tmp/before.unsorted"
sort "$tmp/before.unsorted" >"$tmp/before.tsv"
cmp -s "$tmp/before.tsv" "$input_dir/before.tsv" || fail 'base package inventory mismatch'

# The OCI digest is the authenticated outer boundary. These checks also catch
# damaged local transfers; Alpine signatures are checked against the base keys.
(cd "$input_dir" && sha256sum -c files.sha256) || fail 'input bytes mismatch'
[ -z "$(find "$input_dir/apks" -mindepth 1 ! -type f -print -quit)" ] || fail 'non-regular package input'
find "$input_dir/apks" -mindepth 1 -maxdepth 1 -type f -exec basename {} \; | sort >"$tmp/apks.files"
cmp -s "$tmp/apks.files" "$input_dir/apks.files" || fail 'missing or additional APK'
set --
while IFS= read -r file; do
    case "$file" in ''|*[!a-zA-Z0-9+_.-]*|.*) fail 'invalid APK filename' ;; esac
    set -- "$@" "$input_dir/apks/$file"
done <"$input_dir/apks.files"
[ "$#" -gt 0 ] || fail 'empty APK transaction'
apk --no-network verify "$@" || fail 'Alpine package signature verification'
apk --no-network --no-cache --repositories-file /dev/null add "$@"
inventory >"$tmp/installed.unsorted"
sort "$tmp/installed.unsorted" >"$tmp/installed.tsv"
cmp -s "$tmp/installed.tsv" "$input_dir/installed.tsv" || fail 'installed package inventory mismatch'

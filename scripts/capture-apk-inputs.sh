#!/bin/sh
# Runs only in a fresh native container of the exact upstream platform digest.
set -eu
export LC_ALL=C
[ "$#" -gt 0 ] || exit 2
mkdir -p /output/apks /output/evidence/trusted-keys
apk --version > /output/apk-tools.txt
apk --print-arch > /output/alpine-arch.txt
printf '%s\n' "$@" | sort > /output/roots.txt
sh /input-install.sh --inventory > /output/before.unsorted
sort /output/before.unsorted > /output/before.tsv
rm /output/before.unsorted
if [ -f /etc/apk/repositories ]; then cp /etc/apk/repositories /output/evidence/repositories; fi
if [ -d /etc/apk/repositories.d ]; then cp -R /etc/apk/repositories.d /output/evidence/repositories.d; fi
# /etc keys shadow identically named /lib keys in apk-tools.
for dir in /lib/apk/keys /etc/apk/keys; do
    for key in "$dir"/*.pub; do
        [ ! -f "$key" ] || cp "$key" /output/evidence/trusted-keys/
    done
done
[ -n "$(find /output/evidence/trusted-keys -type f -print -quit)" ]
rm -rf /etc/apk/cache
ln -s /output/apks /etc/apk/cache
apk --update-cache --cache-dir /output/apks add "$@"
apk --keys-dir /output/evidence/trusted-keys verify /output/apks/*.apk
sh /input-install.sh --inventory > /output/installed.unsorted
sort /output/installed.unsorted > /output/installed.tsv
rm /output/installed.unsorted
# Retain signed indices for audit, without making them an installation source.
for file in /output/apks/*; do
    case "$file" in *.apk) ;; *) mv "$file" /output/evidence/ ;; esac
done
# apk index reads package metadata across supported APK formats. This temporary
# unsigned index is solely a metadata decoder, never an installation repository.
: > /output/downloaded.tsv
for file in /output/apks/*.apk; do
    apk index --no-warnings --output /tmp/input-metadata.tar.gz "$file" > /dev/null
    printf '%s\t' "$(basename "$file")" >> /output/downloaded.tsv
    tar -xOf /tmp/input-metadata.tar.gz APKINDEX | awk '
        /^P:/ {name=substr($0,3); p++} /^V:/ {version=substr($0,3); v++} /^A:/ {arch=substr($0,3); a++}
        END { if (p!=1 || v!=1 || a!=1 || name=="" || version=="" || arch=="") exit 1;
              print name "\t" version "\t" arch }' >> /output/downloaded.tsv
done
rm /tmp/input-metadata.tar.gz
# The native runner owns its bind-mounted output after the root package install.
case "${INPUT_UID:-}:${INPUT_GID:-}" in *[!0-9:]*|:|:*) exit 2 ;; esac
[ -n "${INPUT_UID:-}" ] && [ -n "${INPUT_GID:-}" ]
chown -R "$INPUT_UID:$INPUT_GID" /output

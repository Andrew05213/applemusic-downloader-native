#!/usr/bin/env bash

set -Eeuo pipefail

mode="${1:-}"
epoch="${2:-}"
version="${3:-}"
runtime_sha256="${4:-}"
runtime_transaction_id="${5:-}"
[[ "$mode" == "rootfs" || "$mode" == "runtime" ]] || { echo "invalid package mode" >&2; exit 2; }
[[ "$epoch" =~ ^[0-9]+$ ]] || { echo "invalid source date epoch" >&2; exit 2; }
[[ -n "$version" ]] || { echo "missing appliance version" >&2; exit 2; }

work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
mkdir -p -- "$work/image"
tar -xf - -C "$work/image"

if [[ "$mode" == "rootfs" ]]; then
  root="$work/image"
  for directory in var/log var/cache var/lib/apt/lists run tmp var/tmp var/lib/applemusic-downloader; do
    if [[ -d "$root/$directory" ]]; then find "$root/$directory" -mindepth 1 -delete; fi
  done
  mkdir -p -- \
    "$root/var/lib/applemusic-downloader/data" \
    "$root/var/lib/applemusic-downloader/wrapper-state" \
    "$root/run/applemusic-downloader" \
    "$root/srv/applemusic-downloader/downloads" \
    "$root/var/lib/dbus"
  chmod 0700 \
    "$root/var/lib/applemusic-downloader" \
    "$root/var/lib/applemusic-downloader/data" \
    "$root/var/lib/applemusic-downloader/wrapper-state" \
    "$root/run/applemusic-downloader"
  chmod 0755 "$root/srv/applemusic-downloader/downloads"
  for fonts in "$root/usr/share/fonts" "$root/usr/local/share/fonts"; do
    if [[ -d "$fonts" ]]; then find "$fonts" -name .uuid -delete; fi
  done
  rm -f -- "$root/var/lib/systemd/random-seed"

  release="$root/opt/applemusic-downloader/releases/$version"
  [[ -d "$release" ]] || { echo "rootfs release directory is missing" >&2; exit 1; }
  [[ "$runtime_sha256" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid initial runtime SHA-256" >&2; exit 2; }
  [[ "$runtime_transaction_id" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid initial runtime transaction ID" >&2; exit 2; }
  [[ ! -e "$release/.amdl-release.json" ]] || { echo "reserved release metadata already exists" >&2; exit 1; }
  printf '{"schemaVersion":1,"version":"%s","runtimeSha256":"%s","transactionId":"%s"}\n' \
    "$version" "$runtime_sha256" "$runtime_transaction_id" > "$release/.amdl-release.json"
  chmod 0644 "$release/.amdl-release.json"
  for host_file in etc/hostname etc/hosts etc/resolv.conf; do
    rm -f -- "$root/$host_file"
    install -D -m 0644 /dev/null "$root/$host_file"
  done
  : > "$root/etc/machine-id"
  rm -f -- "$root/var/lib/dbus/machine-id"
  ln -s /etc/machine-id "$root/var/lib/dbus/machine-id"
else
  source="$work/image/opt/applemusic-downloader/releases/$version"
  [[ -d "$source" ]] || { echo "runtime release directory is missing" >&2; exit 1; }
  root="$work/runtime"
  destination="$root/opt/applemusic-downloader/releases/$version"
  mkdir -p -- "$(dirname -- "$destination")"
  mv -- "$source" "$destination"
  rm -f -- "$destination/.amdl-release.json"
  if [[ -d "$destination/rootfs/data" ]]; then find "$destination/rootfs/data" -mindepth 1 -delete; fi
fi

if find "$root" -type f \( -name jobs.db -o -name jobs.db-wal -o -name jobs.db-shm \
  -o -name .env -o -name 2fa.txt -o -iname '*credential*' \) -print -quit | grep -q .; then
  echo "$mode archive contains forbidden runtime or credential state" >&2
  exit 1
fi

(
  cd -- "$root"
  find . -print0 | LC_ALL=C sort -z | TZ=UTC LC_ALL=C tar \
    --null --no-recursion --files-from=- \
    --format=posix --numeric-owner --owner=0 --group=0 --mtime="@$epoch" \
    --pax-option=delete=atime,delete=ctime,exthdr.name=%d/PaxHeaders/%f \
    -cf -
) | gzip -n -9

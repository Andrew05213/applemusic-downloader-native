#!/bin/sh
# Run inside the locked private build image with an empty /out bind-mounted on E:.
# This collects candidates, not proof that QEMU or the combined installer is cleared.
set -eu
export LC_ALL=C
test -d /out
test -z "$(ls -A /out)" || { echo 'output directory must be empty' >&2; exit 1; }
test "$(id -u)" = 0
mkdir -p /out/apt/lists/partial /out/apt/cache/archives/partial /out/linux /out/busybox
printf '%s\n' \
  'deb-src https://snapshot.debian.org/archive/debian/20260728T000000Z/ bookworm main' \
  'deb-src https://snapshot.debian.org/archive/debian-security/20260728T000000Z/ bookworm-security main' > /out/sources.list
aptget() {
  apt-get -o Acquire::Check-Valid-Until=false \
    -o Dir::Etc::sourcelist=/out/sources.list -o Dir::Etc::sourceparts=- \
    -o Dir::State::lists=/out/apt/lists -o Dir::Cache=/out/apt/cache "$@"
}
aptget update
(cd /out/linux && aptget source --download-only 'linux=6.1.177-1')
(cd /out/busybox && aptget source --download-only 'busybox=1:1.35.0-4+deb12u1')
# Validate source archive checksums without unpacking the large kernel tree.
# APT verifies snapshot indexes; .dsc signature verification is separate.
for package in linux busybox; do
  cd "/out/$package"
  set -- ./*.dsc
  test "$#" -eq 1 && test -f "$1"
  awk '/^Checksums-Sha256:/{inside=1;next} inside && /^[^ ]/{exit} inside && /^ / {
    if (length($1)!=64 || $1 !~ /^[0-9a-f]+$/ || $3 ~ /\// || $3 ~ /^\./) exit 1;
    print $1 "  " $3; count++
  } END {if (!count) exit 1}' "$1" > SHA256SUMS.source
  sha256sum -c SHA256SUMS.source
  find . -maxdepth 1 -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
  cd /out
done
printf '%s\n' 'Snapshot: 20260728T000000Z' 'Linux binary: linux-image-6.1.0-51-amd64=6.1.177-1' \
  'BusyBox binary: busybox-static=1:1.35.0-4+deb12u1+b1' \
  'BusyBox source: 1:1.35.0-4+deb12u1 (binary-only +b1 rebuild)' > SOURCE-IDENTITY.txt
echo 'Debian source candidates collected; independent binary correspondence and license review remain required.'

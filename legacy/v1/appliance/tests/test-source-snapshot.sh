#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
builder="${repo_root}/appliance/scripts/build-source-snapshot.sh"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/amdl-source-snapshot.XXXXXX")"
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT

fixture="${tmp}/fixture"
output="${tmp}/out"
mkdir -p "${fixture}/appliance/dist" "${fixture}/nested" "${fixture}/.cache" \
  "${fixture}/appliance/tests/network-preflight-fixtures" "${fixture}/wrapper-linux/rootfs/data"
git -C "$fixture" init -q
git -C "$fixture" config core.autocrlf false
git -C "$fixture" config user.email 'snapshot-test@example.invalid'
git -C "$fixture" config user.name 'snapshot test'
printf 'APPLIANCE_VERSION=1.3.0\nSOURCE_DATE_EPOCH=1786320000\n' > "${fixture}/appliance/versions.lock"
printf '#!/bin/sh\nprintf source\n' > "${fixture}/app.sh"
chmod 0755 "${fixture}/app.sh"
printf 'must-not-be-read\n' > "${fixture}/config.yaml"
mkdir -p "${fixture}/.gocache" "${fixture}/nested/.gocache-api"
printf 'cache-must-not-enter-snapshot\n' > "${fixture}/.gocache/cache.bin"
printf 'cache-must-not-enter-snapshot\n' > "${fixture}/nested/.gocache-api/cache.bin"
printf 'must-not-be-read\n' > "${fixture}/credentials.bin"
printf 'must-not-be-read\n' > "${fixture}/DEBUG_REPORT_fixture.md"
mkdir -p "${fixture}/appliance/.source-v130-stale"
printf 'generated\n' > "${fixture}/appliance/.source-v130-stale/archive.tar.gz"
printf 'temporary lock\n' > "${fixture}/appliance/local-inputs.v130.tmp.lock"
printf 'generated\n' > "${fixture}/appliance/dist/old.tar.gz"
printf 'cached\n' > "${fixture}/.cache/cache.txt"
printf 'measurement\n' > "${fixture}/.tmp-v1215-measure.log"
cp -- "${repo_root}/appliance/tests/network-preflight-fixtures/server.key" \
  "${fixture}/appliance/tests/network-preflight-fixtures/server.key"
cp -- "${repo_root}/appliance/tests/network-preflight-fixtures/trusted-ca.key" \
  "${fixture}/appliance/tests/network-preflight-fixtures/trusted-ca.key"
cp -- "${repo_root}/appliance/tests/network-preflight-fixtures/untrusted-ca.key" \
  "${fixture}/appliance/tests/network-preflight-fixtures/untrusted-ca.key"
printf 'must-not-be-snapshotted\n' > "${fixture}/appliance/tests/network-preflight-fixtures/unexpected.key"
printf 'must-not-be-snapshotted\n' > "${fixture}/nested/private.key"
printf 'must-not-be-read\n' > "${fixture}/wrapper-linux/rootfs/data/2fa.txt"
git -C "$fixture" add appliance/versions.lock app.sh config.yaml credentials.bin DEBUG_REPORT_fixture.md
git -C "$fixture" commit -qm fixture
printf 'uncommitted source\n' > "${fixture}/nested/new-source.js"

bash "$builder" --repo-root "$fixture" --dist "$output"
archive="${output}/applemusic-downloader-source-1.3.0.tar.gz"
metadata="${output}/applemusic-downloader-source-1.3.0.manifest.json"
sidecar="${archive}.sha256"
[[ -f "$archive" && -f "$metadata" && -f "$sidecar" ]]
gzip -t "$archive"
(cd "$output" && sha256sum -c "$(basename -- "$sidecar")")
first_archive_sha="$(sha256sum "$archive" | awk '{print $1}')"
cp -- "$metadata" "${tmp}/first-manifest.json"
tar -tzf "$archive" > "${tmp}/entries.txt"
grep -Fx 'applemusic-downloader-source-1.3.0/app.sh' "${tmp}/entries.txt"
grep -Fx 'applemusic-downloader-source-1.3.0/nested/new-source.js' "${tmp}/entries.txt"
grep -Fx 'applemusic-downloader-source-1.3.0/appliance/tests/network-preflight-fixtures/server.key' "${tmp}/entries.txt"
grep -Fx 'applemusic-downloader-source-1.3.0/appliance/tests/network-preflight-fixtures/trusted-ca.key' "${tmp}/entries.txt"
grep -Fx 'applemusic-downloader-source-1.3.0/appliance/tests/network-preflight-fixtures/untrusted-ca.key' "${tmp}/entries.txt"
grep -Fx 'applemusic-downloader-source-1.3.0/SOURCE_MANIFEST.json' "${tmp}/entries.txt"
for forbidden in config.yaml credentials.bin DEBUG_REPORT_fixture.md appliance/dist/old.tar.gz appliance/.source-v130-stale/archive.tar.gz appliance/local-inputs.v130.tmp.lock .cache/cache.txt \
  .gocache/cache.bin nested/.gocache-api/cache.bin .tmp-v1215-measure.log nested/private.key wrapper-linux/rootfs/data/2fa.txt \
  appliance/tests/network-preflight-fixtures/unexpected.key; do
  if grep -Fq "/${forbidden}" "${tmp}/entries.txt"; then
    echo "forbidden source snapshot entry: ${forbidden}" >&2
    exit 1
  fi
done
node - "$metadata" "$fixture" <<'NODE'
'use strict';
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
const [metadataPath, fixture] = process.argv.slice(2);
const value = JSON.parse(fs.readFileSync(metadataPath, 'utf8'));
if (value.schemaVersion !== 1 || value.private !== true || value.productVersion !== '1.3.0'
    || value.sourceDateEpoch !== 1786320000 || !Array.isArray(value.files)
    || !/^[a-f0-9]{64}$/.test(value.archive?.sha256 || '')) {
  throw new Error('source snapshot metadata shape is invalid');
}
const head = execFileSync('git', ['-C', fixture, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
if (value.baseHead !== head) throw new Error('source snapshot base head mismatch');
const entries = new Map(value.files.map((entry) => [entry.file, entry]));
if (entries.get('app.sh')?.mode !== '0755' || !entries.has('nested/new-source.js')) {
  throw new Error('source snapshot file inventory is incomplete');
}
for (const allowedFixtureKey of [
  'appliance/tests/network-preflight-fixtures/server.key',
  'appliance/tests/network-preflight-fixtures/trusted-ca.key',
  'appliance/tests/network-preflight-fixtures/untrusted-ca.key',
]) {
  if (!entries.has(allowedFixtureKey)) {
    throw new Error(`source snapshot omitted fixed offline TLS fixture key: ${allowedFixtureKey}`);
  }
}
for (const forbidden of [
  'config.yaml', 'credentials.bin', 'DEBUG_REPORT_fixture.md', 'appliance/dist/old.tar.gz', 'appliance/.source-v130-stale/archive.tar.gz', 'appliance/local-inputs.v130.tmp.lock', '.cache/cache.txt',
  '.tmp-v1215-measure.log', 'nested/private.key', 'wrapper-linux/rootfs/data/2fa.txt',
  'appliance/tests/network-preflight-fixtures/unexpected.key',
]) {
  if (entries.has(forbidden)) throw new Error(`forbidden source snapshot inventory entry: ${forbidden}`);
}
NODE

bash "$builder" --repo-root "$fixture" --dist "$output"
[[ "$(sha256sum "$archive" | awk '{print $1}')" == "$first_archive_sha" ]]
cmp -- "${tmp}/first-manifest.json" "$metadata"

# Even a known fixture path cannot be repurposed for an arbitrary key.
printf 'tampered-fixture-key\n' > "${fixture}/appliance/tests/network-preflight-fixtures/server.key"
if bash "$builder" --repo-root "$fixture" --dist "${tmp}/tampered-output"; then
  echo 'source snapshot accepted a tampered fixture key' >&2
  exit 1
fi
echo 'source snapshot tests passed'

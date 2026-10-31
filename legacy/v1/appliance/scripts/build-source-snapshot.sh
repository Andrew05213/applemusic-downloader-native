#!/usr/bin/env bash

# Produce a private, byte-reproducible source evidence bundle.  It is never
# referenced by the Appliance manifest or NSIS resources.
set -Eeuo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/common.sh"

repo_root="$DEFAULT_REPO_ROOT"
dist_dir=""

usage() {
  cat <<'USAGE'
Usage: build-source-snapshot.sh [--repo-root <repository>] [--dist <directory>]

Creates a private deterministic source archive and its inventory metadata.
The archive excludes real configuration, credential-shaped files, debug
reports, caches, and generated build artifacts.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root)
      [[ $# -ge 2 ]] || die "--repo-root requires a value"
      repo_root="$2"
      shift 2
      ;;
    --dist)
      [[ $# -ge 2 ]] || die "--dist requires a value"
      dist_dir="$2"
      shift 2
      ;;
    --help)
      usage
      exit 0
      ;;
    *) die "unknown option: $1" ;;
  esac
done

require_command git
require_command tar
require_command gzip
require_command sha256sum
require_command node
require_command realpath
require_command stat

repo_root="$(cd -- "$repo_root" && pwd -P)"
[[ -d "${repo_root}/.git" || -f "${repo_root}/.git" ]] || die "repo root is not a Git worktree"
versions_lock="${repo_root}/appliance/versions.lock"
load_versions() {
  declare -gA VERSIONS=()
  read_lock "$versions_lock" VERSIONS
}
load_versions
version="${VERSIONS[APPLIANCE_VERSION]:-}"
epoch="${VERSIONS[SOURCE_DATE_EPOCH]:-}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "versions.lock has an invalid APPLIANCE_VERSION"
[[ "$epoch" =~ ^[0-9]+$ ]] || die "versions.lock has an invalid SOURCE_DATE_EPOCH"

if [[ -z "$dist_dir" ]]; then
  dist_dir="${repo_root}/appliance/dist"
fi
mkdir -p -- "$dist_dir"
dist_dir="$(cd -- "$dist_dir" && pwd -P)"

archive_name="applemusic-downloader-source-${version}.tar.gz"
metadata_name="applemusic-downloader-source-${version}.manifest.json"
sidecar_name="${archive_name}.sha256"
archive_path="${dist_dir}/${archive_name}"
metadata_path="${dist_dir}/${metadata_name}"
sidecar_path="${dist_dir}/${sidecar_name}"
base_head="$(git -C "$repo_root" rev-parse --verify HEAD)"
[[ "$base_head" =~ ^[0-9a-f]{40}$ ]] || die "Git HEAD is not a canonical SHA-1 object id"

is_excluded_path() {
  case "$1" in
    # These are fixed, public test fixtures needed to reproduce the offline
    # Android/Bionic TLS harness. Every other private-key-shaped file remains
    # excluded below, including any unexpected file in this fixture directory.
    appliance/tests/network-preflight-fixtures/server.key|appliance/tests/network-preflight-fixtures/trusted-ca.key|appliance/tests/network-preflight-fixtures/untrusted-ca.key)
      return 1
      ;;
    config.yaml|*/config.yaml|.env|*/.env|.env.*|*/.env.*|credentials.bin|*/credentials.bin|credentials.bin.*|*/credentials.bin.*|2fa.txt|*/2fa.txt)
      return 0
      ;;
    DEBUG_REPORT_*.md|*/DEBUG_REPORT_*.md|debug.log|*/debug.log|.tmp-v*|*/.tmp-v*|appliance/dist/*|appliance/.work/*|appliance/.source-*/*|appliance/local-inputs.v*.lock|dist/*|dist-*/*|electron/generated/*|electron/bin/*.exe|node_modules/*|*/node_modules/*|.git/*|*/.git/*|.cache/*|*/.cache/*|.uv-cache/*|*/.uv-cache/*|.gocache/*|*/.gocache/*|.gocache-*/*|*/.gocache-*/*|.tmp-gocache/*|*/.tmp-gocache/*|.tmp-gocache-*/*|*/.tmp-gocache-*/*|.gotelemetry/*|*/.gotelemetry/*|.tmp/*|*/.tmp/*|__pycache__/*|*/__pycache__/*|.pytest_cache/*|*/.pytest_cache/*|test-results/*|playwright-report/*|coverage/*|*.exe|*.dll|*.so|*.a|*.o|*.obj|*.img|*.vhdx|*.tar|*.tar.gz|*.key|*.pem|*.p12|*.pfx|*.pyc|*.pyo)
      return 0
      ;;
  esac
  return 1
}

approved_fixture_key_sha256() {
  case "$1" in
    appliance/tests/network-preflight-fixtures/server.key)
      printf '%s\n' '5ad3478e779787808289af45b7e026ff40212a2028d1f8b16423f48fc22f3b99'
      ;;
    appliance/tests/network-preflight-fixtures/trusted-ca.key)
      printf '%s\n' '418dd52371efccfb33092e4a3cdc8c742f624b7c86b062a38db9bd593b388acc'
      ;;
    appliance/tests/network-preflight-fixtures/untrusted-ca.key)
      printf '%s\n' '531d0fd39242aab0d5a88f7fbadfebba64ad6668874f24afa6c04c363b960855'
      ;;
    *)
      return 1
      ;;
  esac
}

validate_relative_path() {
  local relative="$1"
  # Git's NUL-delimited listing already guarantees that this value contains no
  # NUL byte; Bash variables cannot represent one reliably anyway.
  [[ -n "$relative" && "$relative" != /* ]] || return 1
  local component
  IFS='/' read -r -a components <<< "$relative"
  for component in "${components[@]}"; do
    [[ -n "$component" && "$component" != '.' && "$component" != '..' ]] || return 1
  done
}

work_dir="$(mktemp -d "${dist_dir}/.source-snapshot.${version}.XXXXXX")"
cleanup() { rm -rf -- "$work_dir"; }
trap cleanup EXIT

staging_root="${work_dir}/applemusic-downloader-source-${version}"
inventory="${work_dir}/inventory.nul"
manifest_input="${work_dir}/source-manifest.json"
archive_tmp="${work_dir}/${archive_name}"
metadata_tmp="${work_dir}/${metadata_name}"
sidecar_tmp="${work_dir}/${sidecar_name}"
mkdir -p -- "$staging_root"
chmod 0755 -- "$staging_root"

file_count=0
while IFS= read -r -d '' relative; do
  is_excluded_path "$relative" && continue
  validate_relative_path "$relative" || die "source snapshot found an unsafe repository path"
  expected_fixture_key_sha256="$(approved_fixture_key_sha256 "$relative" || true)"
  source_path="${repo_root}/${relative}"
  # Filter occurs before lstat/copy so a real config.yaml, credential, or 2FA
  # state file is never opened by this script.
  [[ -f "$source_path" && ! -L "$source_path" ]] || die "source snapshot only accepts regular non-symlink files"
  source_real="$(realpath -e -- "$source_path")"
  [[ "$source_real" == "${repo_root}/"* ]] || die "source snapshot path escaped the repository"
  mode="$(stat -c '%a' -- "$source_path")"
  size="$(stat -c '%s' -- "$source_path")"
  digest="$(sha256_file "$source_path")"
  if [[ -n "$expected_fixture_key_sha256" && "$digest" != "$expected_fixture_key_sha256" ]]; then
    die "source snapshot fixture key identity is invalid"
  fi
  [[ "$mode" =~ ^[0-7]{3,4}$ && "$size" =~ ^[0-9]+$ ]] || die "source snapshot file metadata is invalid"
  destination="${staging_root}/${relative}"
  mkdir -p -- "$(dirname -- "$destination")"
  cp -p -- "$source_path" "$destination"
  printf '%s\0%s\0%s\0%s\0' "$relative" "$mode" "$size" "$digest" >> "$inventory"
  file_count=$((file_count + 1))
done < <(git -C "$repo_root" ls-files -z --cached --others --exclude-standard)

(( file_count > 0 )) || die "source snapshot has no eligible files"
find "$staging_root" -type d -exec chmod 0755 {} +

node - "$inventory" "$manifest_input" "$version" "$epoch" "$base_head" <<'NODE'
'use strict';
const fs = require('node:fs');
const [inventoryPath, outputPath, productVersion, sourceDateEpoch, baseHead] = process.argv.slice(2);
const fields = fs.readFileSync(inventoryPath).toString('utf8').split('\0');
fields.pop();
if (fields.length === 0 || fields.length % 4 !== 0) throw new Error('invalid source snapshot inventory');
const files = [];
for (let index = 0; index < fields.length; index += 4) {
  const [file, mode, size, sha256] = fields.slice(index, index + 4);
  if (!/^[0-9a-f]{64}$/.test(sha256) || !/^[0-7]{3,4}$/.test(mode) || !/^\d+$/.test(size)) {
    throw new Error('invalid source snapshot inventory entry');
  }
  files.push({ file, mode: `0${mode}`, size: Number(size), sha256 });
}
files.sort((left, right) => (left.file < right.file ? -1 : (left.file > right.file ? 1 : 0)));
fs.writeFileSync(outputPath, `${JSON.stringify({
  schemaVersion: 1,
  private: true,
  productVersion,
  sourceDateEpoch: Number(sourceDateEpoch),
  baseHead,
  files,
}, null, 2)}\n`, { encoding: 'utf8', mode: 0o644 });
NODE
cp -- "$manifest_input" "${staging_root}/SOURCE_MANIFEST.json"
chmod 0644 -- "${staging_root}/SOURCE_MANIFEST.json"

tar --format=posix --sort=name --mtime="@${epoch}" --owner=0 --group=0 --numeric-owner \
  --pax-option=delete=atime,delete=ctime -C "$work_dir" -cf - "$(basename -- "$staging_root")" \
  | gzip -n > "$archive_tmp"

archive_sha256="$(sha256_file "$archive_tmp")"
archive_size="$(stat -c '%s' -- "$archive_tmp")"
[[ "$archive_sha256" =~ ^[0-9a-f]{64}$ && "$archive_size" =~ ^[1-9][0-9]*$ ]] || die "source snapshot archive identity is invalid"
node - "$manifest_input" "$metadata_tmp" "$archive_name" "$archive_sha256" "$archive_size" <<'NODE'
'use strict';
const fs = require('node:fs');
const [sourceManifestPath, outputPath, file, sha256, size] = process.argv.slice(2);
const source = JSON.parse(fs.readFileSync(sourceManifestPath, 'utf8'));
if (source?.schemaVersion !== 1 || source?.private !== true || !Array.isArray(source.files)) {
  throw new Error('invalid source snapshot manifest');
}
fs.writeFileSync(outputPath, `${JSON.stringify({
  ...source,
  archive: { file, sha256, size: Number(size) },
}, null, 2)}\n`, { encoding: 'utf8', mode: 0o644 });
NODE
printf '%s  %s\n' "$archive_sha256" "$archive_name" > "$sidecar_tmp"

# Every source byte is rechecked after staging. A concurrent edit makes the
# snapshot fail closed rather than reporting a mixed working tree as evidence.
while IFS= read -r -d '' relative && IFS= read -r -d '' mode && IFS= read -r -d '' size && IFS= read -r -d '' digest; do
  source_path="${repo_root}/${relative}"
  [[ -f "$source_path" && ! -L "$source_path" ]] || die "source changed while snapshot was built"
  [[ "$(stat -c '%a' -- "$source_path")" == "$mode" && "$(stat -c '%s' -- "$source_path")" == "$size" \
      && "$(sha256_file "$source_path")" == "$digest" ]] || die "source changed while snapshot was built"
done < "$inventory"

mv -f -- "$archive_tmp" "$archive_path"
mv -f -- "$metadata_tmp" "$metadata_path"
mv -f -- "$sidecar_tmp" "$sidecar_path"
note "private deterministic source snapshot: ${archive_path}"

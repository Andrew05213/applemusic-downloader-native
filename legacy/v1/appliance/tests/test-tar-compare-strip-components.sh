#!/usr/bin/env bash

set -Eeuo pipefail
export LC_ALL=C

if ! tar --version | head -n 1 | grep -Fq 'GNU tar'; then
  echo 'GNU tar is required for the runtime content proof test' >&2
  exit 1
fi

fixture="$(mktemp -d "${TMPDIR:-/tmp}/amdl-tar-compare.XXXXXXXX")"
cleanup() { rm -rf -- "$fixture"; }
trap cleanup EXIT

version=1.2.0
archive_tree="$fixture/archive"
release="$archive_tree/opt/applemusic-downloader/releases/$version"
current="$fixture/current"
archive="$fixture/runtime.tar.gz"

mkdir -p -- "$release/rootfs/data"
printf '#!/usr/bin/env bash\nexit 0\n' > "$release/amdl-appliance"
printf 'runtime-content\n' > "$release/apple-music-downloader"
printf 'state-placeholder\n' > "$release/rootfs/data/.keep"
chmod 0755 "$release/amdl-appliance"
chmod 0644 "$release/apple-music-downloader" "$release/rootfs/data/.keep"
find "$archive_tree" -exec touch -h -d '@1700000000' -- {} +

tar --create --gzip --file "$archive" --directory "$archive_tree" .
cp -a -- "$release" "$current"

compare=(
  tar --compare --gzip --one-file-system --no-recursion --strip-components=5
  --show-transformed-names --quoting-style=escape
  --exclude=. --exclude=./opt --exclude=./opt/applemusic-downloader
  --exclude=./opt/applemusic-downloader/releases
  --exclude="./opt/applemusic-downloader/releases/$version"
  --directory "$current" --file "$archive"
)

set +e
"${compare[@]}" > "$fixture/equal.stdout" 2> "$fixture/equal.stderr"
status=$?
set -e
[[ "$status" -eq 0 && ! -s "$fixture/equal.stdout" && ! -s "$fixture/equal.stderr" ]] || {
  echo 'GNU tar strip-components comparison rejected identical content' >&2
  cat "$fixture/equal.stdout" "$fixture/equal.stderr" >&2
  exit 1
}

chmod 0700 "$current/amdl-appliance"
set +e
"${compare[@]}" > "$fixture/mode.stdout" 2> "$fixture/mode.stderr"
status=$?
set -e
mode_comparison_supported=1
if [[ "$status" -ne 1 ]]; then
  case "$(uname -s)" in
    MINGW*|MSYS*)
      # Git for Windows GNU tar intentionally ignores POSIX mode differences
      # on its NTFS fixture. Production comparison runs inside WSL/ext4, while
      # verify.sh independently validates every archived mode from tar headers.
      [[ "$status" -eq 0 ]] || {
        echo "GNU tar returned $status for a mode-only difference on Git for Windows" >&2
        cat "$fixture/mode.stdout" "$fixture/mode.stderr" >&2
        exit 1
      }
      mode_comparison_supported=0
      ;;
    *)
      echo "GNU tar returned $status instead of 1 for a mode-only difference" >&2
      cat "$fixture/mode.stdout" "$fixture/mode.stderr" >&2
      exit 1
      ;;
  esac
fi
if [[ "$mode_comparison_supported" -eq 1 ]] && \
    ! grep -Eh '^(\./opt/applemusic-downloader/releases/1\.2\.0/)?amdl-appliance: Mode differs$' \
      "$fixture/mode.stdout" "$fixture/mode.stderr" >/dev/null; then
  echo 'GNU tar did not report the expected transformed mode-difference member' >&2
  cat "$fixture/mode.stdout" "$fixture/mode.stderr" >&2
  exit 1
fi

chmod 0755 "$current/amdl-appliance"
printf 'changed\n' >> "$current/apple-music-downloader"
set +e
"${compare[@]}" > "$fixture/content.stdout" 2> "$fixture/content.stderr"
status=$?
set -e
[[ "$status" -eq 1 ]] || {
  echo "GNU tar returned $status instead of 1 for a content difference" >&2
  cat "$fixture/content.stdout" "$fixture/content.stderr" >&2
  exit 1
}
if ! grep -Eh '^(\./opt/applemusic-downloader/releases/1\.2\.0/)?apple-music-downloader: (Contents|Size) differs$' \
    "$fixture/content.stdout" "$fixture/content.stderr" >/dev/null; then
  echo 'GNU tar did not report the expected transformed content-difference member' >&2
  cat "$fixture/content.stdout" "$fixture/content.stderr" >&2
  exit 1
fi

echo 'GNU tar strip-components comparison behavior verified'

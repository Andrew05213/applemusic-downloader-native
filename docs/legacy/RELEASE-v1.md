# Historical 1.x Release Runbook

This archived WSL/Appliance procedure is not used by 2.0 CI or releases.
Use [the native release runbook](../RELEASE.md) for current delivery.
Paths and commands below refer to the historical 1.x checkout, not current main
or the frozen `legacy/v1/` archive. Use the [separate maintenance checkout](README.md#维护-1x)
for a complete 1.x tree.

This release keeps Docker Web, Go CLI and the Windows WSL2 desktop appliance on
the same backend/Web source. CI and ordinary validation must not contain Apple
credentials, Tokens or downloaded media.

## 0. Redistribution hard gate

Read `NOTICE`, `docs/UPSTREAMS.md` and `appliance/release-policy.json` first.
Public upload of a WSL rootfs, combined Docker image or Windows installer is
forbidden while the Wrapper/downloader license and Android/Apple/CDM
redistribution authorization remain unverified. `--channel public` and
`AMDL_PUBLIC_RELEASE=1` must fail until the checked-in policy is intentionally
cleared by a reviewed provenance change.

Local PoCs and private CI builds may continue. An environment variable alone
must never bypass the repository policy file. The allowed `local-poc`,
`private-ci` and `private-build` uses are controlled validation scopes, not a
redistribution grant: keep combined artifacts on access-controlled builders,
do not upload them as Release assets, public CI artifacts or public registry
images, and do not distribute them outside the authorized private validation
scope while the gate is closed.

## 1. Validate source and existing delivery modes

From a fresh clone:

```bash
cd apple-music-downloader
go mod download
go test -race ./...
go vet ./...
go build .
go build ./cmd/amdl-appliance
cd ..
npm ci
npm run build:windows-helper
npm test
npm run test:web
docker compose config --quiet
version="$(node -p "require('./package.json').version")"
docker build --platform linux/amd64 -t "applemusic-downloader:$version" .
```

The Windows helper and its recovery descriptor are test prerequisites on Linux
and Windows; the helper build cross-compiles the required artifacts. On Windows
also run the WSL manager mock tests. The race build requires a C toolchain.

Hosted PR validation uses synthetic rootfs and runtime packaging fixtures whose
version follows `package.json`. Both filenames and manifest blockers retain
`CI-SYNTHETIC-NOT-FOR-RUNTIME`; the fixtures contain no executable runtime or
third-party payload, cannot boot and must not be published. Installer checks use
the current versioned online/offline names and verify both embedded archives.

Docker upgrade validation must use the current Compose file. It has no
`env_file` and must not map `APPLE_EMAIL`, `APPLE_PASSWORD`,
`MEDIA_USER_TOKEN`, `AUTHORIZATION_TOKEN` or `AMDL_API_TOKEN` into the service
environment. It exports only the non-secret
`AMDL_CREDENTIALS_FILE=/run/secrets/amdl-credentials.env` path and bind-mounts
the host file selected by `AMDL_DOCKER_CREDENTIALS_FILE` read-only. Existing
operators may retain their ignored `.env` after adding
`AMDL_DOCKER_CREDENTIALS_FILE=./.env`; remove any old custom `env_file` or
direct secret environment mappings. With no `.env`, the reviewed empty
`.env.example` mount must still let the container become healthy.

Record the API authentication migration in release notes and client checks.
Static assets plus `/api/v1/health/live` and `/api/v1/health/ready` remain
unauthenticated; every other `/api/` route must return
`401 api_auth_required` without `Authorization: Bearer <key>` and succeed with
the installation's 64-hex key. `/api/v1/session` is removed, legacy
`amdl_session` cookies are expired rather than accepted, and SSE/file clients
must use a header-capable transport. Never put the key in a URL, cookie, test
log or command-line argument.

### Desktop DRM runtime checks

Use Node.js 22 and a Windows x64 host for the desktop runtime. The Electron
source archive is declared once in `package.json`; the lockfile records its
integrity, and `electron/drm-runtime.js` defines the required version and
installation locations. After `npm ci`, close any running development app
before repairing a missing or occupied runtime with `npm run install:drm`.
That optional command installs the same declared source into the isolated
`node_modules/.amdl-drm` directory, disables npm lifecycle scripts, then runs
only the checked Electron package installer once. It does not refresh the
project lockfile. Startup and packaging refuse stock or mismatched runtimes.

Configuration and packaging fixtures run without an installed runtime and use
synthetic binaries. Before a private Windows candidate build, separately run:

```powershell
npm run test:electron
npm run smoke:drm
```

The smoke may contact the Widevine component service. Its runner creates a new
temporary profile, removes `ELECTRON_RUN_AS_NODE`, enforces a 90-second process
limit, and deletes only that profile's owned run directory after exit. It reads
no installed application data, uses only synthetic credentials, and opens only
a loopback fixture page. Success covers CDM readiness, EME access, safeStorage
round-trip and named synthetic-cookie handling, not Apple authentication,
subscription status, production VMP acceptance or actual song playback.
Failure receipts name the stage without forwarding raw runtime diagnostics.
Do not replace this check with the mocked browser tests.

## 2. Build locked WSL artifacts

The Verify workflow's Appliance reproducibility job uses the tracked
`appliance/local-inputs.poc.lock`, calls the build with explicit
`--channel private-ci`, and uploads no artifacts. Source or runtime changes
invalidate this reviewed input lock. Update it intentionally after reviewing
the final source; CI must continue rejecting a stale lock rather than
regenerating it implicitly. Static validation and the public-release rejection
gate remain separate checks.

Use amd64 Linux/CI with Docker Buildx, or Windows 11 x64 with Git for Windows
Git Bash and Docker Desktop in Linux-container mode. End users do not need
Docker. Start from a clean checkout whose source already matches the reviewed
input and package locks. Before the final build, measure the locked Android CA
bundle and source-built native pins twice, review the matching output, then
regenerate the local input lock twice and compare its bytes:

```bash
./appliance/scripts/measure-android-ca-bundle.sh --runs 2
./appliance/scripts/measure-native-pins.sh --runs 2
./appliance/scripts/lock-local-inputs.sh --output /tmp/amdl-inputs-a.lock
./appliance/scripts/lock-local-inputs.sh --output /tmp/amdl-inputs-b.lock
cmp /tmp/amdl-inputs-a.lock /tmp/amdl-inputs-b.lock
./appliance/scripts/build-all.sh --channel private-ci
./appliance/scripts/verify.sh --dist appliance/dist --require-artifacts
./appliance/scripts/verify-reproducible.sh --channel private-ci
```

Do not regenerate locks immediately before validation. If an intentional
source input changed, run `lock-local-inputs.sh` earlier and review its diff
with that source change. Run `resolve-packages.sh --accept` only for a reviewed
`packages.lock` change. Windows maintainers must use Git Bash rather than
PowerShell/`cmd.exe`/WSL `bash.exe`, must not build from UNC paths, and must not
export `MSYS_NO_PATHCONV` globally. See `appliance/README.md`.

The outputs, in this order, are:

1. `applemusic-downloader-wsl-rootfs-<version>-amd64.tar.gz`
2. `applemusic-downloader-runtime-<version>-amd64.tar.gz`
3. versioned manifest, individual `.sha256` files and `SHA256SUMS`

For a private, non-distributable source audit, run
`npm run build:source-snapshot`. It records the current Git HEAD, a sorted
file inventory, and a reproducible `applemusic-downloader-source-<version>.tar.gz`
under `appliance/dist`. The snapshot excludes real configuration, credentials,
debug reports, caches, and build outputs, and is never embedded in NSIS.

Confirm both consecutive builds produce identical archives, manifests,
individual sidecars and checksum lists. Rootfs/runtime extraction,
normalization and canonical tar/gzip creation run in a disposable Linux
packager container so Windows filesystem metadata cannot enter the artifacts.
Inspect the archives for `/var/lib` runtime data, secrets, logs, machine-id,
jobs.db, 2FA files and media; any match is a release failure.

## 3. Build online and offline Windows installers

On Windows x64 with Go, Node, the verified DRM runtime and the locked manifest:

```powershell
$version = node -p "require('./package.json').version"
$env:AMDL_APPLIANCE_MANIFEST = (Resolve-Path "appliance\dist\applemusic-downloader-wsl-manifest-$version-amd64.json")
npm run dist:win:online

$env:AMDL_OFFLINE_ROOTFS = (Resolve-Path "appliance\dist\applemusic-downloader-wsl-rootfs-$version-amd64.tar.gz")
$env:AMDL_OFFLINE_RUNTIME = (Resolve-Path "appliance\dist\applemusic-downloader-runtime-$version-amd64.tar.gz")
npm run dist:win:offline
```

A `local-poc` or private manifest with `file:` artifact URLs produces only a
same-machine validation build. It is not an online installer that can be sent
to another user. The two `dist:win:*` commands are deliberately private-build
entries and pass `--publish never`, so electron-builder cannot infer a publish
provider from CI, a Git tag or a token-bearing environment. They reject public
manifests.

A future public online build requires a separate reviewed entry plus a manifest
whose version exactly matches the desktop package, whose channel is `public`,
whose rootfs and runtime use credential-free HTTPS URLs, and whose policy
digest and verified Wrapper provenance match the reviewed repository policy.
That entry must require both `AMDL_PUBLIC_RELEASE=1` and
`AMDL_REDISTRIBUTION_APPROVED=1`; it must remain unavailable while
`appliance/release-policy.json` keeps the redistribution gate closed.

The offline installer embeds and verifies both rootfs and runtime so future
runtime repair/update does not depend on a path from the build machine. Both
installers include the reviewed `NOTICE` and the applicable runv2 third-party
license. Including those notices does not clear the separate redistribution
gate for Wrapper, Android/Apple or CDM material.

For the selected package version the expected names are `AppleMusicDownloader-<version>-Setup.exe` and
`AppleMusicDownloader-<version>-Offline-Setup.exe`, each with a `.sha256` sidecar. The
installer is unsigned when none of `CSC_LINK`, `WIN_CSC_LINK` or `CSC_NAME` is
configured; this path deliberately avoids downloading the winCodeSign helper
and does not require Windows symbolic-link privileges. When one of those
certificate selectors is configured, both online and offline builds retain
electron-builder's executable editing/signing path. Never print the selector
value. Release notes for unsigned builds must explain SmartScreen and checksum
verification.

The DRM packaging hooks retain the upstream EXE and detached `.sig` bytes and
refuse an upstream runtime that requires embedded ASAR integrity. They never
flip that fuse. Certificate selectors keep the existing builder signing path;
they do not provide production VMP authorization. Changing this executable or
its signing strategy requires separate VMP review and is outside this private
build procedure.

## 4. Real Windows 11 x64 WSL smoke

Use a clean test distribution or the dedicated `AppleMusicDownloader` name
only after verifying it is absent. Never change the default distro, other
distros, `.wslconfig`, or run `wsl --shutdown`.

Verify:

- import into `%LOCALAPPDATA%\AppleMusicDownloader\wsl` and systemd startup;
- Windows localhost health identity `service=applemusic-downloader`,
  `runtime=wsl-appliance`, the selected package version without Docker Desktop;
- Chinese/space-containing Music path, same-directory atomic final rename and
  task deletion semantics;
- Electron exit leaves the service/download active; explicit stop confirms
  active jobs;
- queued recovery and running/cancelling → interrupted after termination;
- same-version runtime repair, database backup, health failure rollback and
  uninstall with both preserve/delete-task-data choices;
- committed/rolled-back runtime receipts survive a supervisor restart and
  bind the exact source/target digest plus transaction identity; a missing,
  malformed, non-0600 or identity-conflicting receipt must fail closed;
- sentinel credentials absent from argv, environ, journal, diagnostics,
  SQLite, SSE, HTTP errors, rootfs and runtime bundles.

`credentialsConfigured` remains a compatibility field for the Supervisor's
in-memory credentials selected for the next backend launch. It does not prove
that an Apple session exists and does not mean the Wrapper is online; use the
Wrapper status for those states.

Wrapper startup now attempts to restore a previously verified account session
when its private receipt matches the configured account and password. The
receipt only selects restoration: fresh native account-token initialization
must succeed before the current process reports login completion. Only an
explicit native credentials challenge with controlled exit 78 permits one
password-login fallback; network failures, crashes and watchdog timeouts do
not. Normal desktop reopening reconnects without resending unchanged
credentials or restarting an already-running backend. Pending credential
changes retain the existing idle-restart path. Official browser login is
independent and unchanged.

A successful login with this implementation is required to create the initial
receipt. Expired/revoked sessions and account/password changes can still
legitimately require 2FA. Include cold-start restoration and controlled expiry
in separately authorized private-account acceptance; offline fixtures cannot
prove Apple accepts a persisted session after an actual machine reboot.

Do not use a real Apple account or download media for this smoke unless the
owner separately authorizes it.

For the CA/TLS acceptance, use a newly installed, stopped Appliance with the candidate package version
and the final manifest:

```powershell
$version = node -p "require('./package.json').version"
$manifest = Resolve-Path "appliance\dist\applemusic-downloader-wsl-manifest-$version-amd64.json"
npm run smoke:wsl -- --network-only --manifest $manifest
```

This path does not inspect, read, or deliver credentials, and must not run
configure, start, update, terminate, unregister, or global WSL shutdown. A
machine with any other installed Appliance version must return
`runtime_reinstall_required`; do not migrate or delete that runtime
automatically.

Run `npm run smoke:wsl` for the final read-only health check. Private runtime
repair is a separate, same-version default-preflight entry:

```powershell
npm run smoke:wsl:repair -- --manifest $env:AMDL_APPLIANCE_MANIFEST
npm run smoke:wsl:repair -- --manifest $env:AMDL_APPLIANCE_MANIFEST --execute-private-runtime
```

Only an already-owned early local PoC with proven legacy Wrapper mount residue
may add `--quiesce-legacy-mounts`. That path persists a recovery marker before
terminate side effects, treats WslManager's durable update transaction as the
submission boundary, and never replays an update whose outcome is unknown. A
verified target remains as a durable terminal receipt, so a lost success
response cannot turn the next run into another terminate/update. Preserve the
marker and distro when recovery cannot prove either the target or rollback
state; do not delete it or use `unregister` as repair. See `docs/WSL-SMOKE.md`
for the exact safety and resumption rules.

## 5. Authorized media gate and publication order

On a trusted private machine, manually verify AAC-LC, ALAC, AAC, Atmos and 2FA,
then scan again for secrets. After the redistribution gate is cleared, publish
in this fixed order:

1. tests and reproducibility evidence;
2. Docker `linux/amd64` image, then resolve its immutable digest;
3. rootfs and runtime archives plus manifest/checksums;
4. online/offline installers built from that exact manifest;
5. CLI artifact, migration notes, checksums and release notes.

Current desktop compatibility targets Windows 10 21H2/22H2 (19044+) or
Windows 11 x64 + WSL2. Windows 10 real-machine acceptance is still required;
see [the compatibility matrix](../WINDOWS_COMPATIBILITY.md). Other boundaries are Linux/Wrapper amd64,
one Job queue worker, and local-only Web access. Track downloads within a Job
default to concurrency 3, adjustable from 1 to 8. A different installed WSL
Appliance version must return `runtime_reinstall_required`; only a fresh install
or same-version repair is supported. ARM64, remote Web exposure, multiple Job
workers and mandatory code signing remain out of scope.

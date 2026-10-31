# 2.0 Credential-free Native Guest Implementation Plan

> **For agentic workers:** Execute inline in the existing v2 worktree. No account/config import, login, media request, system installation, WSL mutation, commit or public package.

**Goal:** Produce a pinned private Lite/QEMU bundle and verify its real Windows TCG boot, status and teardown without credentials or outbound guest networking.

**Architecture:** Build Lite Release with the already cached, identity-checked NDK/CMake image, and install kernel/modules/busybox from a fixed Debian snapshot in an ephemeral container. An owned minimal init mounts a newly created state image and starts only Lite service mode. Extract the checksum-verified QEMU NSIS installer without running it; export a manifest consumed by the new native control EXE.

**Tech Stack:** Docker build/export only, NDK r23b, CMake 3.25.3, Debian snapshot 20260728T000000Z, Node orchestration, shell guest init, Windows QEMU 11.1.0 with TCG.

---

## Task 1: Pin inputs and repair source-time trust regressions

**Files:** Create `native-runtime/inputs.lock.json`, `native-runtime/lite-native.patch`, `test/electron/native-guest.test.js`.

- [x] Pin Wrapper commit `2fd7e0eaef081f93cf687428b965674937832ba4` and downloaded source archive SHA-256 `cc9fca1a70692ee7feb73c1aa74805cd6749d29200dad34ddbab3d17467464e8`.
- [x] Pin QEMU installer 20260811 to its published SHA-512; use installed full 7-Zip for NSIS extraction because repository 7za supports only a reduced format set. Never execute the installer.
- [x] Pin toolchain image ID, cJSON/Dobby commits, NDK/CMake versions, Debian snapshot and package versions. Record full installed package inventory in private build evidence.
- [x] Add deterministic assertions for strict TLS patch and input identities; implement reviewed hunks: remove TLS-bypass hook and debug toggle; initialize CurlEasy with peer=1, host=2 and `/usr/local/android/cert.pem`; remove per-request verification=0; redact token-prefix logs; use only guest-local QEMU DNS; do not fetch/refresh tokens when no music token exists.

Required patch invariant:

```js
assert.match(patch, /CURLOPT_CAINFO/);
assert.match(patch, /\/usr\/local\/android\/cert\.pem/);
assert.match(patch, /-.*DobbyHook.*curl_easy_setopt/);
```

- [x] Run `node --test test/electron/native-guest.test.js`, and apply the patch with `git apply --recount --check` against the exact source in the Docker build.

## Task 2: Build a minimal pinned guest

**Files:** Create `native-runtime/Dockerfile`, `native-runtime/build-guest.sh`, `native-runtime/init`.

- [x] Add static tests proving Release, disconnected FetchContent, fixed snapshot, no Docker runtime entrypoint that logs in, and fail-closed `/data` mount.
- [x] Build only the source-matched `lite` target. Disable debug TLS bypass independently of build type; copy no old native main, credentials, `/data` or host configuration into the guest.
- [x] Install `linux-image-6.1.0-51-amd64=6.1.177-1`, `busybox-static=1:1.35.0-4+deb12u1+b1`, `cpio=2.13+dfsg-7.1`, `kmod=30+20221128-1`, `ca-certificates=20230311+deb12u1` from the pinned snapshot.
- [x] Resolve and include only required e1000/virtio/CRC32C/ext4/fw_cfg module dependency closures from that kernel, not modules from an unrelated host kernel.
- [x] Produce sorted newc + `gzip -n` initramfs and an empty 64 MiB ext4 template using fixed epoch/UUID; copy kernel, CA digest, package inventory and compiled Lite digest to evidence. Verify two forced guest exports byte-for-byte before calling the assembly/export reproducible (not a claim of full cross-machine toolchain rebuild).

Guest service is fixed:

```sh
/system/bin/lite --base-dir /data --host 0.0.0.0 --port 12340
```

`/data` mount failure powers off with a fixed diagnostic; there is no fallback to ephemeral token storage or interactive rescue shell.

## Task 3: Export Windows bundle and exercise native control

**Files:** Create `electron/build-native-runtime.js`, `test/native/native-guest-live.cjs`; modify `package.json`, `internal/nativeruntime/bundle.go` and its tests.

- [x] Add a bundle `isolated` boolean. Real phase-1 bundles set it true and append QEMU `restrict=on` to the user network while preserving loopback hostfwd. Test that normal/isolated commands remain explicit and never select WHPX/WSL.
- [x] Build driver verifies cached download digests and toolchain image ID before Docker use. Docker context contains only explicit build inputs. Output directory must be new; stage/export using a named per-run container, then remove only that container. Do not prune shared images or volumes.
- [x] Extract only x86_64 EXE, support DLLs, firmware and notices; skip installer plugins and other architecture executables. Compute ordered SHA-256/size manifest for all delivered immutable files. Record QEMU version without installing it.
- [x] Native live harness owns a fresh state directory and unused port, sends check/start/status/stop/shutdown requests, requires full matching receipts and `regions=[]`, `sessionVerified=false`, then confirms port/process teardown, occupied-port rejection, startup cancellation and EOF cleanup. No credential or media commands exist in the harness.
- [x] Run a second boot against the same fixture state and verify the immutable template did not change. This does not prove account/session persistence or graceful shutdown of future authenticated state.

## Task 4: Verification and boundary

- [x] Run native package race, full Linux race in Docker with source read-only and network disabled, Node tests, native EXE smoke, source lock/manifest verification and staged/unstaged diff checks.
- [x] Record real boot timing and failures; distinguish Windows QEMU runtime from Docker build. On failure inspect the owned fixture's serial/kernel diagnostics rather than bypass TLS, enable WSL or modify Windows features.
- [x] Update architecture/developer docs. Real login, secure guest credential transport, personal library rights, media/decryption, graceful state shutdown, continuous health and desktop UI integration remain subsequent gates.

## Results and bounded deviations

The real Windows fixture passed two TCG boots (29461 / 29431 ms), occupied-port rejection, cancellation, EOF and stop/shutdown receipts; evidence is `.tmp-v2-native/live-04ac0be6-83e1-406b-aeec-bcd3abb55694/evidence.json`. Two forced assemblies matched all 190 delivered files; evidence is `.tmp-v2-native/repro-b960cfc4-c92e-4af7-873c-0fd3cdad6b2d.json`.

Actual boot diagnostics required adding CRC32C (ext4 loads it dynamically), and staging verified ASCII-relative guest inputs under the Unicode-safe Windows CWD because QEMU could not open absolute Chinese paths. Regression tests first failed on the old CWD/argument contract and on a real comma-containing firmware name, then passed with the repair. Reproducibility-tool tests also reject changed bytes and reused assembly IDs.

The broader phase-1 clean Windows/Win10 machine and reboot gates are not completed by this developer-machine fixture. No account/session or media behavior was tested, and the running desktop entry is still 1.4.3. There is no new installer, commit, push or public release.

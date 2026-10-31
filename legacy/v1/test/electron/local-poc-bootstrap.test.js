'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fsp = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');

const {
  bootstrapLocalPocIdentity,
  bootstrapProofEnvironment,
  canonicalManifestDigest,
  cleanupImmutableBundleBridge,
  cleanupLegacyWrapperMounts,
  createBootstrapMarker,
  filterVerifiedWslHostWarnings,
  immutableBridgeCommands,
  immutableBridgePaths,
  legacyUpdateDocument,
  loadTargetManifest,
  metadataDocument,
  parseArchiveMembers,
  parseBridgeMount,
  parseCurrentMembers,
  publishReleaseIdentityNoReplace,
  removePublishedReleaseIdentity,
  runtimeProofCommands,
  assertOnlyExpectedRuntimeMounts,
  quiesceLegacyAppliance,
  validateBootstrapMarker,
  validateCompareOutput,
  windowsFileToVerifiedWslPath,
} = require('../../electron/local-poc-bootstrap');
const { WslSmokeError } = require('../../electron/wsl-smoke');
const { parseMode } = require('../../electron/run-wsl-smoke');
const { createManifest } = require('./fixtures/appliance-manifest-document');

const INSTALL_DIR = 'C:\\Users\\测试 用户\\AppData\\Local\\AppleMusicDownloader\\wsl';
const APP_DATA = 'C:\\Users\\测试 用户\\AppData\\Local\\AppleMusicDownloader';
const LEGACY_SHA = '1'.repeat(64);
const TARGET_SHA = '2'.repeat(64);

test('bootstrap CLI requires all three explicit paths and rejects ambiguous arguments', () => {
  assert.deepEqual(parseMode([
    '--bootstrap-local-poc-identity',
    '--legacy-runtime', 'C:\\旧包\\runtime.tar.gz',
    '--target-manifest', 'C:\\新包\\manifest.json',
    '--target-runtime', 'C:\\新包\\runtime.tar.gz',
  ]), {
    mode: 'bootstrap-local-poc-identity',
    legacyRuntimePath: 'C:\\旧包\\runtime.tar.gz',
    targetManifestPath: 'C:\\新包\\manifest.json',
    targetRuntimePath: 'C:\\新包\\runtime.tar.gz',
  });
  assert.throws(() => parseMode(['--bootstrap-local-poc-identity']), (error) => error.code === 'unsafe_process');
  assert.throws(() => parseMode([
    '--bootstrap-local-poc-identity', '--legacy-runtime', 'C:\\old.tar.gz',
    '--target-manifest', 'C:\\manifest.json', '--target-runtime', 'C:\\new.tar.gz',
    '--target-runtime', 'C:\\second.tar.gz',
  ]), (error) => error.code === 'unsafe_process');
});

function localManifest(runtimeSha256, runtimeSize) {
  return createManifest({
    channel: 'local-poc',
    runtime: {
      ...createManifest().runtime,
      sha256: runtimeSha256,
      size: runtimeSize,
      compressedBytes: runtimeSize,
    },
  });
}

function orchestratorFixture({ recovery = false } = {}) {
  const legacyManifest = localManifest(LEGACY_SHA, 2048);
  const targetManifest = localManifest(TARGET_SHA, 4096);
  const targetManifestSha256 = canonicalManifestDigest(targetManifest);
  const transactionId = 'a'.repeat(64);
  const state = {
    schemaVersion: 1,
    distroName: 'AppleMusicDownloader',
    installDir: INSTALL_DIR,
    manifest: legacyManifest,
    installedVersion: legacyManifest.version,
    installedRuntimeSha256: null,
    installedRuntimeTransactionId: null,
  };
  const bridge = {
    ...immutableBridgePaths({ appDataDir: APP_DATA }, transactionId),
    placeholderWslPath: '/mnt/c/Users/test/bootstrap-bridge/runtime.bundle',
  };
  if (recovery) {
    state.localPocBootstrap = createBootstrapMarker({
      legacyManifest,
      targetManifest,
      targetManifestSha256,
      transactionId,
      bridge,
    });
  }
  const calls = {
    lock: 0,
    lockHeld: false,
    candidate: 0,
    legacyProof: 0,
    quiesce: 0,
    bridge: 0,
    update: 0,
    target: 0,
    cleanup: 0,
    publish: 0,
    remove: 0,
    adopt: 0,
    snapshot: 0,
    restore: 0,
    keeper: 0,
    wsl: 0,
  };
  const manager = {
    appDataDir: APP_DATA,
    ensureKeeper: async () => { calls.keeper += 1; },
    localPocMigrationLocker: async (_manager, operation) => {
      calls.lock += 1;
      assert.equal(calls.lockHeld, false);
      calls.lockHeld = true;
      try { return await operation({ testLock: true }); } finally { calls.lockHeld = false; }
    },
    readState: async () => {
      assert.equal(calls.lockHeld, true);
      return structuredClone(state);
    },
    writeState: async (update) => { Object.assign(state, structuredClone(update)); return structuredClone(state); },
    runWsl: async (args) => {
      calls.wsl += 1;
      if (args.includes('--terminate')) throw new Error('unexpected recovery terminate');
      return { stdout: '', stderr: '' };
    },
  };
  const dependencies = {
    manifestLoader: async () => ({ manifest: targetManifest, manifestSha256: targetManifestSha256, path: 'C:\\target.json' }),
    artifactResolver: async (_manager, filename, artifact) => ({
      windowsPath: filename,
      wslPath: artifact.sha256 === LEGACY_SHA ? '/mnt/c/legacy-runtime.tar.gz' : '/mnt/c/target-runtime.tar.gz',
      sha256: artifact.sha256,
      size: artifact.size,
    }),
    candidateValidator: async () => { calls.candidate += 1; return { manifest: structuredClone(legacyManifest) }; },
    legacyContentProver: async () => { calls.legacyProof += 1; return { memberCount: 20 }; },
    quiescer: async () => { calls.quiesce += 1; },
    legacyRestorer: async () => { calls.restore += 1; },
    bridgePreparer: async (_manager, { transactionId: generated }) => {
      calls.bridge += 1;
      return {
        ...immutableBridgePaths({ appDataDir: APP_DATA }, generated),
        placeholderWslPath: '/mnt/c/Users/test/bootstrap-bridge/runtime.bundle',
      };
    },
    snapshotVerifier: async () => { calls.snapshot += 1; },
    updateExecutor: async () => { calls.update += 1; throw new Error('synthetic lost response'); },
    targetVerifier: async () => { calls.target += 1; return { proof: { memberCount: 42 } }; },
    bridgeCleaner: async () => { calls.cleanup += 1; },
    metadataPublisher: async () => { calls.publish += 1; },
    metadataRemover: async () => { calls.remove += 1; },
    publishedVerifier: async (_manager, metadata) => ({
      service: 'applemusic-downloader',
      runtime: 'wsl-appliance',
      version: metadata.version,
      runtimeSha256: metadata.runtimeSha256,
      runtimeTransactionId: metadata.transactionId,
      credentialsConfigured: false,
      updateInProgress: false,
      configured: true,
      state: 'running',
    }),
    adopter: async () => { assert.equal(calls.lockHeld, true); calls.adopt += 1; return { adopted: true }; },
  };
  return { manager, state, calls, dependencies, legacyManifest, targetManifest, bridge };
}

test('runtime proof and immutable bridge commands are fixed argv arrays without shell strings', () => {
  const proof = runtimeProofCommands({ version: '1.2.0', artifactWslPath: '/mnt/c/测试 用户/legacy-runtime.tar.gz' });
  const bridge = immutableBridgeCommands({
    snapshotWslPath: `/var/lib/applemusic-downloader/.bootstrap-${'a'.repeat(64)}.bundle`,
    placeholderWslPath: '/mnt/c/Users/test/bootstrap-bridge/runtime.bundle',
  });
  for (const args of [...Object.values(proof), ...Object.values(bridge)]) {
    assert.ok(Array.isArray(args));
    assert.equal(args.includes('sh'), false);
    assert.equal(args.includes('-c'), false);
    assert.equal(args.includes('cmd'), false);
    assert.equal(args.some((argument) => argument.includes('\0')), false);
  }
  assert.equal(proof.currentList.filter((argument) => argument === '%P\\0').length, 2);
  assert.equal(bridge.snapshotWrite.includes('conv=fsync,excl'), true);
  assert.equal(bridge.snapshotWrite.includes('oflag=excl'), false);
  assert.equal(proof.compare.includes('--no-recursion'), true);
  assert.equal(proof.compare.includes('--strip-components=5'), true);
  assert.equal(proof.compare.includes('--strip-components=4'), false);
  assert.equal(proof.compare.includes('--exclude=.'), true);
  assert.equal(proof.compare.includes('--exclude=./opt/applemusic-downloader/releases/1.2.0'), true);
  assert.deepEqual(proof.mounts.slice(-4), ['/usr/bin/findmnt', '--json', '--output', 'TARGET,SOURCE,FSTYPE']);
  assert.deepEqual(bridge.bind.slice(-3), [
    '--bind',
    `/var/lib/applemusic-downloader/.bootstrap-${'a'.repeat(64)}.bundle`,
    '/mnt/c/Users/test/bootstrap-bridge/runtime.bundle',
  ]);
  assert.equal(bridge.bind.includes('--bind'), true);
  assert.equal(bridge.readOnly.includes('remount,bind,ro'), true);
  assert.equal(bridge.unmount.includes('/usr/bin/umount'), true);
  const update = legacyUpdateDocument('1.2.0', {
    snapshotWslPath: `/var/lib/applemusic-downloader/.bootstrap-${'a'.repeat(64)}.bundle`,
    placeholderWslPath: '/mnt/c/Users/test/bootstrap-bridge/runtime.bundle',
  }, TARGET_SHA);
  assert.deepEqual(Object.keys(update), ['schemaVersion', 'mode', 'version', 'bundlePath', 'sha256']);
  assert.equal(update.mode, 'repair');
});

test('proof environment replaces every case variant with one fixed WSL_UTF8=1 entry', () => {
  const source = {
    Path: 'C:\\Windows\\System32',
    wsl_utf8: '0',
    Wsl_Utf8: 'false',
    WSL_UTF8: 'legacy',
  };
  const result = bootstrapProofEnvironment(source);
  assert.deepEqual(result, { Path: 'C:\\Windows\\System32', WSL_UTF8: '1' });
  assert.equal(Object.keys(result).filter((name) => name.toLowerCase() === 'wsl_utf8').length, 1);
  assert.equal(source.wsl_utf8, '0');
});

test('stderr filter removes only exact complete verified localhost proxy warning lines', () => {
  const english = 'wsl: A localhost proxy configuration was detected but not mirrored into WSL. WSL in NAT mode does not support localhost proxies.';
  const chinese = 'wsl: 检测到 localhost 代理配置，但未镜像到 WSL。NAT 模式下的 WSL 不支持 localhost 代理。';
  const malicious = 'tar: unexpected diagnostic must remain';
  const mixed = Buffer.from(`${english}\r\n${malicious}\n${chinese}\n`, 'utf8');
  assert.equal(
    filterVerifiedWslHostWarnings(mixed, 'bootstrap_runtime_content').toString('utf8'),
    `${malicious}\n`,
  );
  for (const nearMatch of [`${english} `, `${chinese}额外内容`, `prefix ${english}`]) {
    assert.equal(
      filterVerifiedWslHostWarnings(Buffer.from(`${nearMatch}\n`), 'bootstrap_runtime_content').toString('utf8'),
      `${nearMatch}\n`,
    );
  }
  const systemdWarning = 'wsl: Failed to start the systemd user session for root.';
  assert.equal(
    filterVerifiedWslHostWarnings(Buffer.from(`${systemdWarning}\n`), 'bootstrap_runtime_content').toString('utf8'),
    `${systemdWarning}\n`,
  );
});

test('verified WSL warning filtering preserves tar mode differences for the existing parser', () => {
  const english = 'wsl: A localhost proxy configuration was detected but not mirrored into WSL. WSL in NAT mode does not support localhost proxies.';
  const archive = parseArchiveMembers([
    '.',
    './opt/applemusic-downloader/releases/1.2.0',
    './opt/applemusic-downloader/releases/1.2.0/amdl-appliance',
    './opt/applemusic-downloader/releases/1.2.0/apple-music-downloader',
    './opt/applemusic-downloader/releases/1.2.0/rootfs',
    './opt/applemusic-downloader/releases/1.2.0/rootfs/data',
  ].join('\n') + '\n', '1.2.0');
  const stderr = filterVerifiedWslHostWarnings(Buffer.from(
    `${english}\n./opt/applemusic-downloader/releases/1.2.0/amdl-appliance: Mode differs\n`,
  ), 'bootstrap_runtime_content');
  assert.equal(validateCompareOutput({ exitCode: 1, stdout: Buffer.alloc(0), stderr }, archive, '1.2.0'), 1);
});

function legacyMountDocument({ includeData = true, procLayers = 0, urandomLayers = 0, extra = [] } = {}) {
  const release = '/opt/applemusic-downloader/releases/1.2.0';
  return JSON.stringify({
    filesystems: [{
      target: '/',
      source: '/dev/sdf',
      fstype: 'ext4',
      children: [
        ...(includeData ? [{
          target: `${release}/rootfs/data`,
          source: '/dev/sdf[/var/lib/applemusic-downloader/wrapper-state]',
          fstype: 'ext4',
        }] : []),
        ...Array.from({ length: procLayers }, () => ({
          target: `${release}/rootfs/proc`, source: 'proc', fstype: 'proc',
        })),
        ...Array.from({ length: urandomLayers }, () => ({
          target: `${release}/rootfs/dev/urandom`, source: 'none[/urandom]', fstype: 'devtmpfs',
        })),
        ...extra,
      ],
    }],
  });
}

test('mount proof requires one wrapper-state mount and only exact bounded legacy leak identities', () => {
  assert.deepEqual(assertOnlyExpectedRuntimeMounts(
    legacyMountDocument({ procLayers: 3, urandomLayers: 4 }),
    '1.2.0',
  ), { data: 1, proc: 3, urandom: 4 });
  assert.deepEqual(assertOnlyExpectedRuntimeMounts(
    legacyMountDocument({ includeData: false, procLayers: 3, urandomLayers: 4 }),
    '1.2.0',
    { expectedDataLayers: 0 },
  ), { data: 0, proc: 3, urandom: 4 });
  for (const extra of [
    { target: '/opt/applemusic-downloader/releases/1.2.0/rootfs/proc/sys', source: 'proc', fstype: 'proc' },
    { target: '/opt/applemusic-downloader/releases/1.2.0/rootfs/dev/urandom', source: 'devtmpfs[/urandom]', fstype: 'devtmpfs' },
    { target: '/opt/applemusic-downloader/releases/1.2.0/rootfs/data', source: '/dev/sdg[/var/lib/applemusic-downloader/wrapper-state]', fstype: 'ext4' },
  ]) {
    assert.throws(
      () => assertOnlyExpectedRuntimeMounts(legacyMountDocument({ extra: [extra] }), '1.2.0'),
      (error) => error.code === 'local_poc_bootstrap_rejected' && error.step === 'bootstrap_mounts',
    );
  }
  assert.throws(
    () => assertOnlyExpectedRuntimeMounts(legacyMountDocument({ procLayers: 33 }), '1.2.0'),
    (error) => error.code === 'local_poc_bootstrap_rejected',
  );
});

test('quiesce mount cleanup removes only exact proc and urandom layers with bounded plain umount calls', async () => {
  const release = '/opt/applemusic-downloader/releases/1.2.0';
  const targets = {
    proc: `${release}/rootfs/proc`,
    urandom: `${release}/rootfs/dev/urandom`,
  };
  const layers = { proc: 3, urandom: 4 };
  const verifiedWarning = 'wsl: A localhost proxy configuration was detected but not mirrored into WSL. WSL in NAT mode does not support localhost proxies.\n';
  const calls = [];
  const manager = {
    runWsl: async (args) => {
      calls.push([...args]);
      if (args.includes('/usr/bin/findmnt') && !args.includes('--mountpoint')) {
        return { stdout: legacyMountDocument({ includeData: false, procLayers: layers.proc, urandomLayers: layers.urandom }), stderr: verifiedWarning };
      }
      if (args.includes('/usr/bin/findmnt')) {
        const target = args[args.indexOf('--mountpoint') + 1];
        const kind = target === targets.proc ? 'proc' : target === targets.urandom ? 'urandom' : '';
        if (!kind || layers[kind] === 0) {
          const error = new Error('not mounted');
          error.exitCode = 1;
          throw error;
        }
        return { stdout: JSON.stringify({ filesystems: [{
          target,
          source: kind === 'proc' ? 'proc' : 'none[/urandom]',
          fstype: kind === 'proc' ? 'proc' : 'devtmpfs',
        }] }), stderr: verifiedWarning };
      }
      if (args.includes('/usr/bin/umount')) {
        const target = args.at(-1);
        const kind = target === targets.proc ? 'proc' : target === targets.urandom ? 'urandom' : '';
        assert.ok(kind);
        layers[kind] -= 1;
        return { stdout: '', stderr: verifiedWarning };
      }
      throw new Error('unexpected command');
    },
  };
  assert.deepEqual(await cleanupLegacyWrapperMounts(manager, '1.2.0'), { proc: 3, urandom: 4 });
  assert.deepEqual(layers, { proc: 0, urandom: 0 });
  const unmounts = calls.filter((args) => args.includes('/usr/bin/umount'));
  assert.equal(unmounts.length, 7);
  for (const args of unmounts) {
    assert.deepEqual(args.slice(-3, -1), ['/usr/bin/umount', '--']);
    assert.equal([targets.proc, targets.urandom].includes(args.at(-1)), true);
    assert.equal(args.some((arg) => ['-a', '-l', '-R', '--recursive'].includes(arg)), false);
    assert.notEqual(args.at(-1), `${release}/rootfs/data`);
  }
});

test('quiesce mount inspection removes a verified warning but rejects mixed unknown stderr', async () => {
  const warning = 'wsl: A localhost proxy configuration was detected but not mirrored into WSL. WSL in NAT mode does not support localhost proxies.';
  let unmounts = 0;
  const manager = {
    runWsl: async (args) => {
      if (args.includes('/usr/bin/findmnt') && !args.includes('--mountpoint')) {
        return {
          stdout: legacyMountDocument({ includeData: false }),
          stderr: `${warning}\nunverified stderr\n`,
        };
      }
      if (args.includes('/usr/bin/umount')) unmounts += 1;
      return { stdout: '', stderr: '' };
    },
  };
  await assert.rejects(
    cleanupLegacyWrapperMounts(manager, '1.2.0'),
    (error) => error.code === 'local_poc_bootstrap_failed' && error.step === 'bootstrap_quiesce_mounts',
  );
  assert.equal(unmounts, 0);
});

test('quiesce mount cleanup refuses a foreign top layer before any umount', async () => {
  const release = '/opt/applemusic-downloader/releases/1.2.0';
  let unmounts = 0;
  const manager = {
    runWsl: async (args) => {
      if (args.includes('/usr/bin/findmnt') && !args.includes('--mountpoint')) {
        return { stdout: legacyMountDocument({ includeData: false, urandomLayers: 1 }), stderr: '' };
      }
      if (args.includes('/usr/bin/findmnt')) return { stdout: JSON.stringify({ filesystems: [{
        target: `${release}/rootfs/dev/urandom`, source: '/dev/random', fstype: 'devtmpfs',
      }] }), stderr: '' };
      if (args.includes('/usr/bin/umount')) unmounts += 1;
      return { stdout: '', stderr: '' };
    },
  };
  await assert.rejects(
    cleanupLegacyWrapperMounts(manager, '1.2.0'),
    (error) => error.code === 'local_poc_bootstrap_rejected' && error.step === 'bootstrap_mounts',
  );
  assert.equal(unmounts, 0);
});

test('quiesce starts the fixed keeper after terminate before supervisor wait and mount cleanup', async () => {
  const events = [];
  const stopped = {
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: '1.2.0',
    state: 'stopped',
    credentialsConfigured: false,
  };
  const manager = {
    activeJobs: async () => { events.push('active'); return []; },
    runControl: async (command) => {
      events.push(`control:${command}`);
      return { stdout: JSON.stringify(stopped), stderr: '' };
    },
    runWsl: async (args) => {
      assert.deepEqual(args, ['--terminate', 'AppleMusicDownloader']);
      events.push('terminate');
      return { stdout: '', stderr: '' };
    },
    ensureKeeper: async () => { events.push('keeper'); },
    waitForSupervisorReady: async () => { events.push('wait'); },
    status: async () => { events.push('status'); return stopped; },
    keeperProcess: { old: true },
  };
  await quiesceLegacyAppliance(manager, '1.2.0', {
    mountCleaner: async () => { events.push('mount-cleanup'); },
    beforeTerminate: async () => { events.push('marker:terminating'); },
    ownershipVerifier: async (phase) => { events.push(`ownership:${phase}`); },
  });
  assert.equal(manager.keeperProcess, null);
  assert.deepEqual(events, [
    'active', 'ownership:before_stop', 'control:stop', 'marker:terminating',
    'ownership:before_terminate', 'terminate', 'keeper', 'wait', 'status',
    'ownership:before_mount_cleanup', 'mount-cleanup',
  ]);
});

test('keeper failure during quiesce restores legacy lifecycle before marker or update', async () => {
  const fixture = orchestratorFixture();
  fixture.manager.ensureKeeper = async () => {
    fixture.calls.keeper += 1;
    if (fixture.calls.keeper === 2) throw new Error('synthetic keeper failure');
  };
  fixture.dependencies.quiescer = async (manager) => { await manager.ensureKeeper(); };
  await assert.rejects(
    bootstrapLocalPocIdentity(fixture.manager, {
      legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
      targetManifestPath: 'C:\\target-manifest.json',
      targetRuntimePath: 'C:\\target-runtime.tar.gz',
      ...fixture.dependencies,
    }),
    /synthetic keeper failure/,
  );
  assert.equal(fixture.calls.restore, 1);
  assert.equal(fixture.calls.bridge, 0);
  assert.equal(fixture.calls.update, 0);
  assert.equal(fixture.state.localPocBootstrap, undefined);
});

test('bootstrap manifest and runtime files use the strict file helper instead of directory canonicalization', async () => {
  const legacyManifest = localManifest(LEGACY_SHA, 2048);
  const targetManifest = localManifest(TARGET_SHA, 4096);
  const manifestPath = 'C:\\测试 用户\\target-manifest.json';
  const runtimePath = 'C:\\测试 用户\\target-runtime.tar.gz';
  const canonicalized = [];
  const manager = {
    canonicalizeWindowsFileWithHelper: async (filename) => {
      canonicalized.push(filename);
      return { canonicalPath: filename, fileSystem: 'NTFS' };
    },
    canonicalizeWindowsPathWithHelper: async () => {
      throw new Error('directory canonicalizer must not receive bootstrap files');
    },
    runWsl: async (args) => {
      if (args.includes('/usr/bin/wslpath')) return { stdout: '/mnt/c/测试 用户/target-runtime.tar.gz\n', stderr: '' };
      if (args.includes('/usr/bin/stat')) return { stdout: '4096\n', stderr: '' };
      if (args.includes('/usr/bin/sha256sum')) return { stdout: `${TARGET_SHA}  runtime.tar.gz\n`, stderr: '' };
      return { stdout: '', stderr: '' };
    },
  };
  const loaded = await loadTargetManifest(manager, manifestPath, legacyManifest, {
    manifestReader: async () => structuredClone(targetManifest),
  });
  assert.equal(loaded.manifest.runtime.sha256, TARGET_SHA);
  const runtime = await windowsFileToVerifiedWslPath(manager, runtimePath, targetManifest.runtime, {
    artifactVerifier: async (_artifact, filename) => assert.equal(filename, runtimePath),
  });
  assert.equal(runtime.wslPath, '/mnt/c/测试 用户/target-runtime.tar.gz');
  assert.deepEqual(canonicalized, [manifestPath, runtimePath]);
});

test('content proof accepts only mode/mtime differences and exact member sets', () => {
  const archive = parseArchiveMembers([
    '.',
    './opt/applemusic-downloader/releases/1.2.0',
    './opt/applemusic-downloader/releases/1.2.0/amdl-appliance',
    './opt/applemusic-downloader/releases/1.2.0/apple-music-downloader',
    './opt/applemusic-downloader/releases/1.2.0/rootfs',
    './opt/applemusic-downloader/releases/1.2.0/rootfs/data',
  ].join('\n') + '\n', '1.2.0');
  const current = parseCurrentMembers(Buffer.from('\0amdl-appliance\0apple-music-downloader\0rootfs\0rootfs/data\0rootfs/proc\0'));
  assert.deepEqual([...archive].sort(), [...current].sort());
  assert.equal(validateCompareOutput({
    exitCode: 1,
    stdout: Buffer.alloc(0),
    stderr: Buffer.from('./opt/applemusic-downloader/releases/1.2.0/amdl-appliance: Mode differs\n./opt/applemusic-downloader/releases/1.2.0/rootfs: Mod time differs\ntar: Exiting with failure status due to previous errors\n'),
  }, archive, '1.2.0'), 2);
  assert.throws(() => validateCompareOutput({
    exitCode: 1,
    stdout: Buffer.alloc(0),
    stderr: Buffer.from('./opt/applemusic-downloader/releases/1.2.0/amdl-appliance: Contents differ\n'),
  }, archive, '1.2.0'), (error) => error.code === 'local_poc_bootstrap_rejected');
});

test('bind mount proof requires the exact ext4 source, target and read-only option', () => {
  const snapshotWslPath = `/var/lib/applemusic-downloader/.bootstrap-${'a'.repeat(64)}.bundle`;
  const placeholderWslPath = '/mnt/c/Users/test/bootstrap-bridge/runtime.bundle';
  assert.equal(parseBridgeMount(JSON.stringify({ filesystems: [{
    target: placeholderWslPath,
    source: `/dev/sdc[${snapshotWslPath}]`,
    fstype: 'ext4',
    options: 'ro,relatime,bind',
  }] }), { snapshotWslPath, placeholderWslPath }), true);
  assert.throws(() => parseBridgeMount(JSON.stringify({ filesystems: [{
    target: placeholderWslPath,
    source: '/mnt/c/original.tar.gz',
    fstype: '9p',
    options: 'rw,bind',
  }] }), { snapshotWslPath, placeholderWslPath }), (error) => error.code === 'local_poc_bootstrap_rejected');
});

test('bootstrap migrates once, recovers a lost update response, persists identity, then adopts', async () => {
  const fixture = orchestratorFixture();
  const result = await bootstrapLocalPocIdentity(fixture.manager, {
    legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
    targetManifestPath: 'C:\\target-manifest.json',
    targetRuntimePath: 'C:\\target-runtime.tar.gz',
    ...fixture.dependencies,
  });
  assert.equal(result.bootstrapped, true);
  assert.equal(fixture.calls.lock, 1);
  assert.equal(fixture.calls.lockHeld, false);
  assert.equal(result.recoveredResponse, true);
  assert.equal(result.contentMembers, 42);
  assert.equal(fixture.calls.candidate, 2);
  assert.equal(fixture.calls.update, 1);
  assert.equal(fixture.calls.publish, 1);
  assert.equal(fixture.calls.adopt, 1);
  assert.equal(fixture.state.manifest.runtime.sha256, TARGET_SHA);
  assert.equal(fixture.state.installedRuntimeSha256, TARGET_SHA);
  assert.match(fixture.state.installedRuntimeTransactionId, /^[a-f0-9]{64}$/);
  assert.equal(fixture.state.localPocBootstrap, null);
});

test('recovery marker never repeats old update and continues only from verified target snapshot', async () => {
  const fixture = orchestratorFixture({ recovery: true });
  const result = await bootstrapLocalPocIdentity(fixture.manager, {
    legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
    targetManifestPath: 'C:\\target-manifest.json',
    targetRuntimePath: 'C:\\target-runtime.tar.gz',
    ...fixture.dependencies,
  });
  assert.equal(result.updateAttempted, false);
  assert.equal(result.recoveredResponse, true);
  assert.equal(fixture.calls.update, 0);
  assert.equal(fixture.calls.snapshot, 1);
  assert.equal(fixture.calls.candidate, 0);
  assert.equal(fixture.calls.adopt, 1);
});

test('marker mismatch fails closed before update or metadata publication', async () => {
  const fixture = orchestratorFixture({ recovery: true });
  fixture.state.localPocBootstrap.targetRuntimeSha256 = 'f'.repeat(64);
  await assert.rejects(
    bootstrapLocalPocIdentity(fixture.manager, {
      legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
      targetManifestPath: 'C:\\target-manifest.json',
      targetRuntimePath: 'C:\\target-runtime.tar.gz',
      ...fixture.dependencies,
    }),
    (error) => error instanceof WslSmokeError && error.code === 'local_poc_bootstrap_rejected',
  );
  assert.equal(fixture.calls.update, 0);
  assert.equal(fixture.calls.publish, 0);
});

test('post-publication failure removes only this migration metadata before state commit', async () => {
  const fixture = orchestratorFixture();
  fixture.dependencies.publishedVerifier = async () => { throw new WslSmokeError('local_poc_bootstrap_rejected', 'bootstrap_status_identity'); };
  await assert.rejects(
    bootstrapLocalPocIdentity(fixture.manager, {
      legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
      targetManifestPath: 'C:\\target-manifest.json',
      targetRuntimePath: 'C:\\target-runtime.tar.gz',
      ...fixture.dependencies,
    }),
    (error) => error.code === 'local_poc_bootstrap_rejected',
  );
  assert.equal(fixture.calls.publish, 1);
  assert.equal(fixture.calls.remove, 1);
  assert.notEqual(fixture.state.installedRuntimeSha256, TARGET_SHA);
});

test('bridge preparation failure restores the legacy running lifecycle before returning', async () => {
  const fixture = orchestratorFixture();
  fixture.dependencies.bridgePreparer = async () => { throw new Error('synthetic bridge failure'); };
  await assert.rejects(
    bootstrapLocalPocIdentity(fixture.manager, {
      legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
      targetManifestPath: 'C:\\target-manifest.json',
      targetRuntimePath: 'C:\\target-runtime.tar.gz',
      ...fixture.dependencies,
    }),
    /synthetic bridge failure/,
  );
  assert.equal(fixture.calls.restore, 1);
  assert.equal(fixture.calls.update, 0);
  assert.equal(fixture.calls.publish, 0);
});

test('recovery recreates a missing immutable snapshot but still never repeats update', async () => {
  const fixture = orchestratorFixture({ recovery: true });
  fixture.dependencies.snapshotVerifier = async () => { fixture.calls.snapshot += 1; throw new Error('snapshot missing after reboot'); };
  const result = await bootstrapLocalPocIdentity(fixture.manager, {
    legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
    targetManifestPath: 'C:\\target-manifest.json',
    targetRuntimePath: 'C:\\target-runtime.tar.gz',
    ...fixture.dependencies,
  });
  assert.equal(result.updateAttempted, false);
  assert.equal(fixture.calls.update, 0);
  assert.equal(fixture.calls.bridge, 1);
  assert.equal(fixture.calls.cleanup, 2);
});

test('ambiguous update outcome keeps recovery state and never publishes metadata', async () => {
  const fixture = orchestratorFixture();
  fixture.dependencies.targetVerifier = async () => { fixture.calls.target += 1; throw new Error('target cannot be proved'); };
  await assert.rejects(
    bootstrapLocalPocIdentity(fixture.manager, {
      legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
      targetManifestPath: 'C:\\target-manifest.json',
      targetRuntimePath: 'C:\\target-runtime.tar.gz',
      ...fixture.dependencies,
    }),
    (error) => error.code === 'local_poc_bootstrap_failed' && error.step === 'bootstrap_update_recovery',
  );
  assert.equal(fixture.calls.update, 1);
  assert.equal(fixture.calls.target, 1);
  assert.equal(fixture.calls.wsl, 0);
  assert.equal(fixture.calls.cleanup, 0);
  assert.equal(fixture.calls.publish, 0);
  assert.equal(fixture.state.localPocBootstrap.phase, 'update_sent');
});

test('bridge cleanup failure blocks metadata and preserves target-installed recovery marker', async () => {
  const fixture = orchestratorFixture();
  fixture.dependencies.bridgeCleaner = async () => { fixture.calls.cleanup += 1; throw new Error('synthetic cleanup failure'); };
  await assert.rejects(
    bootstrapLocalPocIdentity(fixture.manager, {
      legacyRuntimePath: 'C:\\legacy-runtime.tar.gz',
      targetManifestPath: 'C:\\target-manifest.json',
      targetRuntimePath: 'C:\\target-runtime.tar.gz',
      ...fixture.dependencies,
    }),
    (error) => error.code === 'local_poc_bootstrap_failed' && error.step === 'bootstrap_bridge_cleanup',
  );
  assert.equal(fixture.calls.publish, 0);
  assert.equal(fixture.state.localPocBootstrap.phase, 'target_installed');
});

test('metadata no-replace refuses a dangling or existing target before stdin publication', async () => {
  let inputCalls = 0;
  const manager = {
    processEnvironment: {},
    resolveWslExecutable: async () => 'C:\\Windows\\System32\\wsl.exe',
    runner: { input: async () => { inputCalls += 1; } },
    runWsl: async (args) => {
      if (args.includes('/usr/bin/test')) throw new Error('target exists');
      return { stdout: '', stderr: '' };
    },
  };
  await assert.rejects(
    publishReleaseIdentityNoReplace(manager, metadataDocument('1.2.0', TARGET_SHA, 'a'.repeat(64))),
    (error) => error.code === 'local_poc_bootstrap_rejected',
  );
  assert.equal(inputCalls, 0);
});

test('rollback rejects non-exact metadata bytes and never unlinks replacement content', async () => {
  let unlinks = 0;
  const target = '/opt/applemusic-downloader/releases/1.2.0/.amdl-release.json';
  const manager = {
    runWsl: async (args) => {
      if (args.includes('/usr/bin/test') && args.includes('-e')) {
        if (args.at(-1) !== target) return { stdout: '', stderr: '' };
        const error = new Error('entry exists');
        error.exitCode = 1;
        throw error;
      }
      if (args.includes('/usr/bin/sha256sum')) return { stdout: `${'f'.repeat(64)}  metadata\n`, stderr: '' };
      if (args.includes('/usr/bin/stat')) return { stdout: '1:2:1:1:644:root\n', stderr: '' };
      if (args.includes('/usr/bin/unlink')) unlinks += 1;
      return { stdout: '', stderr: '' };
    },
  };
  await assert.rejects(
    removePublishedReleaseIdentity(manager, metadataDocument('1.2.0', TARGET_SHA, 'a'.repeat(64))),
    (error) => error.code === 'local_poc_bootstrap_failed' && error.step === 'bootstrap_metadata_rollback',
  );
  assert.equal(unlinks, 0);
});

test('metadata rollback quarantines by rename and restores a raced replacement without deleting it', async () => {
  const metadata = metadataDocument('1.2.0', TARGET_SHA, 'a'.repeat(64));
  const payload = Buffer.from(`${JSON.stringify(metadata)}\n`, 'utf8');
  const expectedDigest = crypto.createHash('sha256').update(payload).digest('hex');
  const target = '/opt/applemusic-downloader/releases/1.2.0/.amdl-release.json';
  let targetEntry = { device: '7', inode: '11', digest: expectedDigest, size: payload.length };
  let quarantineEntry = null;
  let targetUnlinks = 0;
  let quarantinePath = '';
  const missing = () => { const error = new Error('missing'); error.exitCode = 1; throw error; };
  const manager = {
    runWsl: async (args) => {
      const executable = args.find((value) => typeof value === 'string' && value.startsWith('/usr/bin/'));
      const filename = args.at(-1);
      const entry = filename === target ? targetEntry : filename === quarantinePath ? quarantineEntry : null;
      if (executable === '/usr/bin/test') {
        if (args.includes('-f')) return entry ? { stdout: '', stderr: '' } : missing();
        if (args.includes('!') && args.includes('-e')) return entry ? missing() : { stdout: '', stderr: '' };
        if (args.includes('!') && args.includes('-L')) return { stdout: '', stderr: '' };
      }
      if (executable === '/usr/bin/stat') {
        if (!entry) return missing();
        if (args.includes('%d:%i')) return { stdout: `${entry.device}:${entry.inode}\n`, stderr: '' };
        return { stdout: `${entry.device}:${entry.inode}:1:${entry.size}:644:root\n`, stderr: '' };
      }
      if (executable === '/usr/bin/sha256sum') {
        if (!entry) return missing();
        return { stdout: `${entry.digest}  ${filename}\n`, stderr: '' };
      }
      if (executable === '/usr/bin/mv') {
        quarantinePath = filename;
        // A replacement wins the path immediately before the atomic rename.
        targetEntry = { device: '7', inode: '99', digest: 'f'.repeat(64), size: payload.length };
        quarantineEntry = targetEntry;
        targetEntry = null;
        return { stdout: '', stderr: '' };
      }
      if (executable === '/usr/bin/ln') {
        if (targetEntry || !quarantineEntry) throw new Error('no-replace restore failed');
        targetEntry = quarantineEntry;
        return { stdout: '', stderr: '' };
      }
      if (executable === '/usr/bin/unlink') {
        if (filename === target) { targetUnlinks += 1; targetEntry = null; } else quarantineEntry = null;
        return { stdout: '', stderr: '' };
      }
      return { stdout: '', stderr: '' };
    },
  };
  await assert.rejects(
    removePublishedReleaseIdentity(manager, metadata),
    (error) => error.code === 'local_poc_bootstrap_failed' && error.step === 'bootstrap_metadata_rollback',
  );
  assert.equal(targetUnlinks, 0);
  assert.equal(targetEntry.inode, '99');
  assert.equal(targetEntry.digest, 'f'.repeat(64));
  assert.equal(quarantineEntry, null);
});

async function bridgeCleanupFixture(runWsl) {
  const bridgeDirectory = await fsp.mkdtemp(path.join(os.tmpdir(), 'amdl-bootstrap-cleanup-'));
  const placeholderWindowsPath = path.join(bridgeDirectory, 'runtime.bundle');
  await fsp.writeFile(placeholderWindowsPath, 'placeholder');
  const bridge = {
    snapshotWslPath: `/var/lib/applemusic-downloader/.bootstrap-${'a'.repeat(64)}.bundle`,
    placeholderWslPath: '/mnt/c/Users/test/bootstrap-bridge/runtime.bundle',
    bridgeDirectory,
    placeholderWindowsPath,
  };
  return {
    bridge,
    manager: { runWsl },
    dispose: () => fsp.rm(bridgeDirectory, { recursive: true, force: true }),
  };
}

function exitOne(message) {
  const error = new Error(message);
  error.exitCode = 1;
  return error;
}

test('bridge cleanup unmounts the exact mount and removes both snapshot and placeholder', async () => {
  let mounted = true;
  let snapshotPresent = true;
  let unmounts = 0;
  let snapshotUnlinks = 0;
  const fixture = await bridgeCleanupFixture(async (args) => {
    if (args.includes('/usr/bin/findmnt')) {
      if (!mounted) throw exitOne('not mounted');
      return { stdout: JSON.stringify({ filesystems: [{
        target: fixture.bridge.placeholderWslPath,
        source: `/dev/sdc[${fixture.bridge.snapshotWslPath}]`,
        fstype: 'ext4',
        options: 'ro,bind',
      }] }) };
    }
    if (args.includes('/usr/bin/umount')) { unmounts += 1; mounted = false; return {}; }
    if (args.includes('/usr/bin/test') && args.includes('-e')) {
      if (!snapshotPresent) throw exitOne('snapshot absent');
      return {};
    }
    if (args.includes('/usr/bin/test') && args.includes('-L')) {
      if (args.includes('!')) return {};
      throw exitOne('not a symlink');
    }
    if (args.includes('/usr/bin/unlink')) { snapshotUnlinks += 1; snapshotPresent = false; return {}; }
    return {};
  });
  try {
    await cleanupImmutableBundleBridge(fixture.manager, fixture.bridge);
    assert.equal(unmounts, 1);
    assert.equal(snapshotUnlinks, 1);
    await assert.rejects(fsp.stat(fixture.bridge.placeholderWindowsPath), { code: 'ENOENT' });
  } finally {
    await fixture.dispose();
  }
});

test('bridge cleanup continues after umount failure but reports cleanup failure', async () => {
  let snapshotPresent = true;
  let snapshotUnlinks = 0;
  const fixture = await bridgeCleanupFixture(async (args) => {
    if (args.includes('/usr/bin/findmnt')) return { stdout: JSON.stringify({ filesystems: [{
      target: fixture.bridge.placeholderWslPath,
      source: fixture.bridge.snapshotWslPath,
      fstype: 'ext4',
      options: 'ro,bind',
    }] }) };
    if (args.includes('/usr/bin/umount')) throw new Error('synthetic umount failure');
    if (args.includes('/usr/bin/test') && args.includes('-e')) {
      if (!snapshotPresent) throw exitOne('snapshot absent');
      return {};
    }
    if (args.includes('/usr/bin/test') && args.includes('-L')) {
      if (args.includes('!')) return {};
      throw exitOne('not a symlink');
    }
    if (args.includes('/usr/bin/unlink')) { snapshotUnlinks += 1; snapshotPresent = false; return {}; }
    return {};
  });
  try {
    await assert.rejects(
      cleanupImmutableBundleBridge(fixture.manager, fixture.bridge),
      (error) => error.code === 'local_poc_bootstrap_failed' && error.step === 'bootstrap_bridge_cleanup',
    );
    assert.equal(snapshotUnlinks, 1);
    await assert.rejects(fsp.stat(fixture.bridge.placeholderWindowsPath), { code: 'ENOENT' });
  } finally {
    await fixture.dispose();
  }
});

test('bridge cleanup reports snapshot unlink failure after removing the Windows placeholder', async () => {
  const fixture = await bridgeCleanupFixture(async (args) => {
    if (args.includes('/usr/bin/findmnt')) throw exitOne('not mounted');
    if (args.includes('/usr/bin/test') && args.includes('-e')) return {};
    if (args.includes('/usr/bin/test') && args.includes('!')) return {};
    if (args.includes('/usr/bin/unlink')) throw new Error('synthetic snapshot unlink failure');
    return {};
  });
  try {
    await assert.rejects(
      cleanupImmutableBundleBridge(fixture.manager, fixture.bridge),
      (error) => error.code === 'local_poc_bootstrap_failed' && error.step === 'bootstrap_bridge_cleanup',
    );
    await assert.rejects(fsp.stat(fixture.bridge.placeholderWindowsPath), { code: 'ENOENT' });
  } finally {
    await fixture.dispose();
  }
});

test('bootstrap marker schema is exact and binds all target identities', () => {
  const fixture = orchestratorFixture({ recovery: true });
  const expected = {
    legacyManifest: fixture.legacyManifest,
    targetManifest: fixture.targetManifest,
    targetManifestSha256: canonicalManifestDigest(fixture.targetManifest),
    appDataDir: APP_DATA,
  };
  assert.equal(validateBootstrapMarker(fixture.state.localPocBootstrap, expected).phase, 'update_sent');
  const mutated = { ...fixture.state.localPocBootstrap, unexpected: true };
  assert.throws(() => validateBootstrapMarker(mutated, expected), (error) => error.code === 'local_poc_bootstrap_rejected');
});

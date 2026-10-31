'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fsp = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const {
  AuditedProcessRunner,
  WslSmokeError,
  publicSmokeError,
  runWslSmoke,
  safeNetworkPreflight,
} = require('../../electron/wsl-smoke');
const {
  closeDesktopResources,
  loadCheckOnlyManifest,
  parseMode,
} = require('../../electron/run-wsl-smoke');
const { createArtifact, createManifest } = require('./fixtures/appliance-manifest-document');
const { BOOTSTRAP_FILE, INCIDENT_SOURCE, WINDOWS_HELPER_FILE } = require('../../electron/wslinterop-recovery');

const VERSION = '1.2.0';
const HEALTH = { service: 'applemusic-downloader', runtime: 'wsl-appliance', version: VERSION };

function readyNetworkV2() {
  return {
    schemaVersion: 2,
    path: 'windows_stdio_bridge',
    state: 'ready',
    stage: 'complete',
    windowsPath: 'reachable',
    wslDirectPath: 'unknown',
    proxyPropagated: true,
    generationId: '9'.repeat(64),
    caSource: 'bundled_v1',
    curlCode: 0,
    sslVerifyResult: 0,
  };
}

class MockManager {
  constructor(shared = { state: 'running', active: [] }, { owned = true, manifest = null } = {}) {
    this.shared = shared;
    this.owned = owned;
    this.manifest = manifest;
    this.runtimeTransactionId = 'e'.repeat(64);
    this.windowsHelperSha256 = 'f'.repeat(64);
    this.bootstrapSha256 = 'd'.repeat(64);
    this.windowsProbeExe = 'C:\\Program Files\\Apple Music Downloader\\resources\\bin\\amdl-windows-probe.exe';
    this.interopBootstrapExe = `C:\\Program Files\\Apple Music Downloader\\resources\\bin\\${BOOTSTRAP_FILE}`;
    this.interopRecoveryIdentityProvider = () => ({
      sha256: 'c'.repeat(64),
      descriptor: {
        schemaVersion: 1,
        targetVersion: this.manifest?.version || VERSION,
        source: { ...INCIDENT_SOURCE },
        bootstrap: { file: BOOTSTRAP_FILE, sha256: this.bootstrapSha256, size: 1 },
        windowsHelper: { file: WINDOWS_HELPER_FILE, sha256: this.windowsHelperSha256, size: 1 },
      },
    });
    this.network = {
      schemaVersion: 1,
      path: 'windows_stdio_bridge',
      state: 'unprepared',
      windowsPath: 'unknown',
      wslDirectPath: 'unknown',
      proxyPropagated: false,
    };
    this.downloadDir = 'C:\\Users\\测试 用户\\Music\\Apple Music Downloader';
    this.calls = [];
  }

  identity() {
    if (!this.manifest) return HEALTH;
    return {
      service: 'applemusic-downloader',
      runtime: 'wsl-appliance',
      version: this.manifest.version,
      productVersion: this.manifest.productVersion,
      applianceBuildId: this.manifest.applianceBuildId,
      runtimeSha256: this.manifest.runtime.sha256,
      runtimeTransactionId: this.runtimeTransactionId,
      androidMainBuiltSha256: this.manifest.inputs.androidMainBuiltSha256,
      androidCaBundlePath: this.manifest.inputs.androidCaBundlePath,
      androidCaBundleSha256: this.manifest.inputs.androidCaBundleSha256,
      rendererSha256: this.manifest.inputs.rendererSha256,
      windowsHelperSha256: this.windowsHelperSha256,
    };
  }

  async readState() {
    if (!this.manifest) return { installedVersion: VERSION };
    return {
      installedVersion: this.manifest.version,
      installedRuntimeSha256: this.manifest.runtime.sha256,
      installedRuntimeTransactionId: this.runtimeTransactionId,
      manifest: this.manifest,
    };
  }
  async registeredDistro() { this.calls.push('registered'); return { name: 'AppleMusicDownloader', version: 2 }; }
  async verifyDistroOwnership() {
    this.calls.push('ownership');
    if (!this.owned) throw new Error('foreign owner secret detail');
    return { ownerId: 'not-returned', appliance: this.identity() };
  }

  async probe() {
    this.calls.push('probe');
    return {
      windows: { architecture: 'x64', virtualizationEnabled: true },
      wsl: { installed: true },
      distro: { version: 2, appliance: this.identity() },
    };
  }

  async status() {
    this.calls.push('status');
    return {
      ...this.identity(),
      state: this.shared.state,
      configured: true,
      updateInProgress: false,
      network: { ...this.network },
    };
  }

  async canonicalDownloadPathOutsideRoots(value) {
    this.calls.push('canonical-path');
    return { canonicalPath: value, fileSystem: 'NTFS' };
  }

  async windowsPathToWsl() {
    this.calls.push('wslpath');
    return '/mnt/c/Users/测试 用户/Music/Apple Music Downloader';
  }

  async prepareLocalEndpoint() { this.calls.push('endpoint'); }
  async waitForReady() { this.calls.push('ready'); return this.identity(); }
  async activeJobs() { this.calls.push('active'); return this.shared.active; }
  async start(credentials) {
    this.calls.push(['start', credentials]);
    this.shared.state = 'running';
    return { ...this.identity(), state: 'running' };
  }

  async stop() {
    this.calls.push('stop');
    this.shared.state = 'stopped';
    return { stopped: true };
  }

  async prepareNetworkForStart() {
    this.calls.push('network-preflight');
    this.network = {
      schemaVersion: 1,
      path: 'windows_stdio_bridge',
      state: 'ready',
      stage: 'complete',
      windowsPath: 'reachable',
      wslDirectPath: 'unknown',
      proxyPropagated: true,
      generationId: '9'.repeat(64),
    };
    return { ...this.network };
  }

  async cleanupNetworkBridge() {
    this.calls.push('network-cleanup');
    this.network = {
      schemaVersion: this.manifest?.version === '1.3.3' ? 2 : 1,
      path: 'windows_stdio_bridge',
      state: 'unprepared',
      windowsPath: 'unknown',
      wslDirectPath: 'unknown',
      proxyPropagated: false,
      ...(this.manifest?.version === '1.3.3'
        ? { caSource: 'bundled_v1', curlCode: 0, sslVerifyResult: 0 }
        : {}),
    };
    return {
      cleaned: true,
      status: {
        ...this.identity(),
        state: this.shared.state,
        configured: true,
        updateInProgress: false,
        network: { ...this.network },
      },
    };
  }

  async fileSha256(filename) {
    this.calls.push(['hash-helper', filename]);
    return filename === this.interopBootstrapExe ? this.bootstrapSha256 : this.windowsHelperSha256;
  }
}

test('check-only smoke verifies ownership before probe, path or health', async () => {
  const manager = new MockManager(undefined, { owned: false });
  await assert.rejects(
    runWslSmoke({ manager }),
    (error) => error instanceof WslSmokeError && error.code === 'owner_identity_mismatch',
  );
  assert.deepEqual(manager.calls, ['registered', 'ownership']);
  assert.doesNotMatch(JSON.stringify(publicSmokeError(new WslSmokeError('owner_identity_mismatch', 'ownership'))), /secret|ownerId/);
});

test('check-only smoke covers owned running health and Unicode path without lifecycle changes', async () => {
  const manager = new MockManager();
  const result = await runWslSmoke({ manager });
  assert.equal(result.ok, true);
  assert.equal(result.mode, 'check-only');
  assert.equal(result.initial.health.service, 'applemusic-downloader');
  assert.equal(result.initial.downloadPath.containsNonASCII, true);
  assert.equal(result.initial.downloadPath.containsWhitespace, true);
  assert.equal(result.checks.updateAttempted, false);
  assert.equal(manager.calls.includes('stop'), false);
  assert.equal(manager.calls.some((call) => Array.isArray(call) && call[0] === 'start'), false);
});

test('manifest-bound check-only smoke runs the stopped-state bridge and Bag preflight without credentials or backend start', async () => {
  const manifest = createManifest({ version: '1.2.14' });
  const manager = new MockManager({ state: 'stopped', active: [] }, { manifest });

  const result = await runWslSmoke({ manager, expectedManifest: manifest });

  assert.equal(result.ok, true);
  assert.equal(result.target.version, manifest.version);
  assert.equal(result.target.applianceBuildId, manifest.applianceBuildId);
  assert.equal(result.finalBuildIdentity.runtimeSha256, manifest.runtime.sha256);
  assert.equal(result.finalBuildIdentity.windowsHelperSha256, 'f'.repeat(64));
  assert.deepEqual(result.networkPreflight, {
    schemaVersion: 1,
    path: 'windows_stdio_bridge',
    state: 'ready',
    stage: 'complete',
    windowsPath: 'reachable',
    wslDirectPath: 'unknown',
    proxyPropagated: true,
  });
  assert.equal(result.checks.manifestBoundFinalIdentity, true);
  assert.equal(result.checks.stoppedStateNetworkPreflight, true);
  assert.equal(manager.calls.includes('network-preflight'), true);
  assert.equal(manager.calls.includes('stop'), false);
  assert.equal(manager.calls.some((call) => Array.isArray(call) && call[0] === 'start'), false);
});

test('manifest-bound check-only smoke fails closed before bridge preparation for a running or mismatched runtime', async () => {
  const manifest = createManifest({ version: '1.2.14' });
  const running = new MockManager({ state: 'running', active: [] }, { manifest });
  await assert.rejects(
    runWslSmoke({ manager: running, expectedManifest: manifest }),
    (error) => error instanceof WslSmokeError && error.code === 'network_preflight_state',
  );
  assert.equal(running.calls.includes('network-preflight'), false);

  const mismatched = new MockManager({ state: 'stopped', active: [] }, { manifest });
  mismatched.readState = async () => ({
    installedVersion: manifest.version,
    installedRuntimeSha256: '0'.repeat(64),
    installedRuntimeTransactionId: 'e'.repeat(64),
    manifest,
  });
  await assert.rejects(
    runWslSmoke({ manager: mismatched, expectedManifest: manifest }),
    (error) => error instanceof WslSmokeError && error.code === 'target_manifest_mismatch',
  );
  assert.equal(mismatched.calls.includes('network-preflight'), false);
});

test('network-only smoke is manifest-bound, never examines credentials, and only prepares then cleans a stopped bridge', async () => {
  const manifest = createManifest({ version: '1.3.3' });
  const manager = new MockManager({ state: 'stopped', active: [] }, { manifest });
  manager.prepareNetworkForStart = async () => {
    manager.calls.push('network-preflight');
    manager.network = readyNetworkV2();
    return { ...manager.network };
  };

  const result = await runWslSmoke({ manager, expectedManifest: manifest, networkOnly: true });

  assert.equal(result.ok, true);
  assert.equal(result.mode, 'network-only');
  assert.equal(result.checks.noCredentialAccess, true);
  assert.equal(result.checks.noCredentialDelivery, true);
  assert.equal(result.checks.backendStartAttempted, false);
  assert.equal(result.cleanup.stopped, true);
  assert.equal(result.networkPreflight.schemaVersion, 2);
  assert.equal(result.networkPreflight.caSource, 'bundled_v1');
  assert.equal(manager.calls.includes('network-preflight'), true);
  assert.equal(manager.calls.includes('network-cleanup'), true);
  assert.equal(result.cleanup.brokerReleased, true);
  assert.equal(manager.calls.includes('stop'), false);
  for (const forbiddenCall of ['probe', 'canonical-path', 'wslpath', 'endpoint', 'ready', 'active']) {
    assert.equal(manager.calls.includes(forbiddenCall), false, `${forbiddenCall} must not run in network-only mode`);
  }
  assert.equal(manager.calls.some((call) => Array.isArray(call) && call[0] === 'start'), false);
});

test('network-only smoke releases the broker in finally when native preflight fails', async () => {
  const manifest = createManifest({ version: '1.3.3' });
  const manager = new MockManager({ state: 'stopped', active: [] }, { manifest });
  manager.prepareNetworkForStart = async () => {
    manager.calls.push('network-preflight');
    manager.network = {
      ...readyNetworkV2(),
      state: 'failed',
      stage: 'tls',
      windowsPath: 'unreachable',
      proxyPropagated: false,
      curlCode: 60,
      sslVerifyResult: 20,
    };
    throw new Error('fixed native preflight failure');
  };

  await assert.rejects(
    runWslSmoke({ manager, expectedManifest: manifest, networkOnly: true }),
    (error) => error instanceof WslSmokeError && error.code === 'network_preflight_failed',
  );
  assert.equal(manager.calls.includes('network-preflight'), true);
  assert.equal(manager.calls.includes('network-cleanup'), true);
  assert.equal(manager.calls.includes('stop'), false);
  assert.equal(manager.calls.some((call) => Array.isArray(call) && call[0] === 'start'), false);
});

test('network-only smoke fails closed before bridge preparation for a running or mismatched Appliance', async () => {
  const manifest = createManifest({ version: '1.3.3' });
  const running = new MockManager({ state: 'running', active: [] }, { manifest });
  await assert.rejects(
    runWslSmoke({ manager: running, expectedManifest: manifest, networkOnly: true }),
    (error) => error instanceof WslSmokeError && error.code === 'network_preflight_state',
  );
  assert.equal(running.calls.includes('network-preflight'), false);

  const mismatched = new MockManager({ state: 'stopped', active: [] }, { manifest });
  mismatched.readState = async () => ({
    installedVersion: '1.2.14',
    installedRuntimeSha256: manifest.runtime.sha256,
    installedRuntimeTransactionId: 'e'.repeat(64),
    manifest,
  });
  await assert.rejects(
    runWslSmoke({ manager: mismatched, expectedManifest: manifest, networkOnly: true }),
    (error) => error instanceof WslSmokeError && error.code === 'target_manifest_mismatch',
  );
  assert.equal(mismatched.calls.includes('network-preflight'), false);
});

test('network-only schema-v2 status accepts only bounded CA diagnostics', () => {
  const valid = readyNetworkV2();
  assert.deepEqual(safeNetworkPreflight(valid), {
    schemaVersion: 2,
    path: 'windows_stdio_bridge',
    state: 'ready',
    stage: 'complete',
    windowsPath: 'reachable',
    wslDirectPath: 'unknown',
    proxyPropagated: true,
    caSource: 'bundled_v1',
    curlCode: 0,
    sslVerifyResult: 0,
  });
  for (const invalid of [
    { ...valid, curlCode: 100 },
    { ...valid, sslVerifyResult: 65536 },
    { ...valid, caSource: 'ambient' },
    { ...valid, generationId: 'not-a-digest' },
  ]) {
    assert.equal(safeNetworkPreflight(invalid), null);
  }
});

test('network-only smoke rejects an installed Android CA identity drift before bridge preparation', async () => {
  const manifest = createManifest({ version: '1.3.3' });
  const manager = new MockManager({ state: 'stopped', active: [] }, { manifest });
  manager.identity = () => ({
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: manifest.version,
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: manager.runtimeTransactionId,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    androidCaBundlePath: manifest.inputs.androidCaBundlePath,
    androidCaBundleSha256: '0'.repeat(64),
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: manager.windowsHelperSha256,
  });
  await assert.rejects(
    runWslSmoke({ manager, expectedManifest: manifest, networkOnly: true }),
    (error) => error instanceof WslSmokeError && error.code === 'target_manifest_mismatch',
  );
  assert.equal(manager.calls.includes('network-preflight'), false);
});

test('network-only smoke rejects an installed Android CA path drift before bridge preparation', async () => {
  const manifest = createManifest({ version: '1.3.3' });
  const manager = new MockManager({ state: 'stopped', active: [] }, { manifest });
  manager.identity = () => ({
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: manifest.version,
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: manager.runtimeTransactionId,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    androidCaBundlePath: '/etc/ssl/certs/ca-certificates.crt',
    androidCaBundleSha256: manifest.inputs.androidCaBundleSha256,
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: manager.windowsHelperSha256,
  });
  await assert.rejects(
    runWslSmoke({ manager, expectedManifest: manifest, networkOnly: true }),
    (error) => error instanceof WslSmokeError && error.code === 'target_manifest_mismatch',
  );
  assert.equal(manager.calls.includes('network-preflight'), false);
});

test('check-only manifest parsing accepts only one controlled final artifact path', () => {
  const target = 'E:\\repo\\appliance\\dist\\applemusic-downloader-wsl-manifest-1.2.14-amd64.json';
  assert.deepEqual(parseMode(['--check-only', '--manifest', target]), {
    mode: 'check-only',
    manifestPath: target,
  });
  assert.deepEqual(parseMode(['--manifest', target]), {
    mode: 'check-only',
    manifestPath: target,
  });
  assert.deepEqual(parseMode(['--network-only', '--manifest', target]), {
    mode: 'network-only',
    manifestPath: target,
  });
  for (const argv of [
    ['--lifecycle', '--manifest', target],
    ['--check-only', '--manifest', target, '--manifest', target],
    ['--check-only', '--manifest'],
    ['--check-only', '--manifest', '--lifecycle'],
    ['--network-only'],
    ['--network-only', '--manifest', target, '--manifest', target],
    ['--network-only', '--manifest', target, '--lifecycle'],
  ]) {
    assert.throws(() => parseMode(argv), (error) => error instanceof WslSmokeError && error.code === 'unsafe_process');
  }
});

test('check-only manifest loader verifies the exact current private artifacts before a WSL command is possible', async () => {
  // Hosted Windows TEMP may be a short-path or junction alias. Match the
  // production loader's canonical-path requirement before writing the fixture.
  const repositoryRoot = await fsp.realpath(
    await fsp.mkdtemp(path.join(os.tmpdir(), 'amdl-smoke-manifest-')),
  );
  try {
    const distDirectory = path.join(repositoryRoot, 'appliance', 'dist');
    await fsp.mkdir(distDirectory, { recursive: true });
    const rootfs = Buffer.from('rootfs-fixture');
    const runtime = Buffer.from('runtime-fixture');
    const rootfsName = 'applemusic-downloader-wsl-rootfs-1.2.14-amd64.tar.gz';
    const runtimeName = 'applemusic-downloader-runtime-1.2.14-amd64.tar.gz';
    const rootfsPath = path.join(distDirectory, rootfsName);
    const runtimePath = path.join(distDirectory, runtimeName);
    await fsp.writeFile(rootfsPath, rootfs);
    await fsp.writeFile(runtimePath, runtime);
    const artifact = (filename, payload) => createArtifact('rootfs', {
      file: filename,
      url: pathToFileURL(path.join(distDirectory, filename)).toString(),
      sha256: crypto.createHash('sha256').update(payload).digest('hex'),
      size: payload.length,
      uncompressedSize: payload.length,
      compressedBytes: payload.length,
      uncompressedBytes: payload.length,
    });
    const manifest = createManifest({
      version: '1.2.14',
      channel: 'local-poc',
      rootfs: artifact(rootfsName, rootfs),
      runtime: artifact(runtimeName, runtime),
    });
    const manifestPath = path.join(distDirectory, 'applemusic-downloader-wsl-manifest-1.2.14-amd64.json');
    await fsp.writeFile(manifestPath, JSON.stringify(manifest));

    const loaded = await loadCheckOnlyManifest(repositoryRoot, manifestPath, { packageVersion: '1.2.14' });
    assert.equal(loaded.manifest.applianceBuildId, manifest.applianceBuildId);
    assert.equal(loaded.artifactsVerified, 2);
    await assert.rejects(
      loadCheckOnlyManifest(repositoryRoot, path.join(repositoryRoot, 'other.json'), { packageVersion: '1.2.14' }),
      (error) => error instanceof WslSmokeError && error.code === 'target_manifest_mismatch',
    );
  } finally {
    await fsp.rm(repositoryRoot, { recursive: true, force: true });
  }
});

test('check-only desktop cleanup is a no-op when this process never owned a proxy', async () => {
  const persistedEndpoint = {
    stateRevision: 41,
    localPort: 54321,
    usesLoopbackProxy: true,
  };
  const before = JSON.stringify(persistedEndpoint);
  let discardCalls = 0;
  const manager = {
    proxy: null,
    proxyStopPending: false,
    async discardLocalProxy() {
      discardCalls += 1;
      persistedEndpoint.localPort = null;
      persistedEndpoint.usesLoopbackProxy = false;
      persistedEndpoint.stateRevision += 1;
    },
  };

  await closeDesktopResources([manager]);

  assert.equal(discardCalls, 0);
  assert.equal(JSON.stringify(persistedEndpoint), before);
});

test('lifecycle smoke refuses credentials or active jobs before explicit stop', async () => {
  const credentialsManager = new MockManager();
  await assert.rejects(
    runWslSmoke({ manager: credentialsManager, lifecycle: true, credentialsPresent: true }),
    (error) => error.code === 'credentials_present',
  );
  assert.equal(credentialsManager.calls.includes('stop'), false);

  const activeManager = new MockManager({ state: 'running', active: [{ id: 'queued', status: 'queued' }] });
  await assert.rejects(
    runWslSmoke({
      manager: activeManager,
      lifecycle: true,
      managerFactory: async () => new MockManager(activeManager.shared),
      detachedStarter: async () => {},
    }),
    (error) => error.code === 'active_jobs',
  );
  assert.equal(activeManager.calls.includes('stop'), false);
});

test('lifecycle smoke proves process exit persistence, explicit stop/restart and restores initial state', async () => {
  const shared = { state: 'running', active: [] };
  const manager = new MockManager(shared);
  let detachedStarts = 0;
  const result = await runWslSmoke({
    manager,
    lifecycle: true,
    credentialsPresent: false,
    detachedStarter: async () => {
      detachedStarts += 1;
      shared.state = 'running';
    },
    managerFactory: async () => new MockManager(shared),
    mountInspector: async () => ({ procLayers: 0, urandomLayers: 0, clean: true }),
  });
  assert.equal(result.lifecycle.managerExitedWithoutStoppingBackend, true);
  assert.equal(result.lifecycle.explicitStop, true);
  assert.equal(result.lifecycle.downloadAtomicProbe, true);
  assert.equal(result.checks.wrapperMountResidueZero, true);
  assert.deepEqual(result.lifecycle.wrapperMountsAfterStops, [
    { procLayers: 0, urandomLayers: 0, clean: true },
    { procLayers: 0, urandomLayers: 0, clean: true },
  ]);
  assert.equal(detachedStarts, 1);
  assert.equal(shared.state, 'running');
});

test('process audit permits only helper/wsl argv arrays and rejects secrets and destructive controls', async () => {
  const calls = [];
  const delegate = {
    exec: async (...args) => { calls.push(args); return { stdout: '', stderr: '' }; },
    input: async (...args) => { calls.push(args); return { stdout: '', stderr: '' }; },
    spawn: (...args) => { calls.push(args); return { unref() {}, once() {} }; },
  };
  const audit = new AuditedProcessRunner(delegate);
  await audit.exec('C:\\Windows\\System32\\wsl.exe', ['--list'], { env: { PATH: 'safe' } });
  assert.throws(
    () => audit.exec('C:\\Windows\\System32\\wsl.exe', ['--list'], { env: { APPLE_PASSWORD: 'stdin-only-sentinel' } }),
    (error) => error.code === 'unsafe_process',
  );
  assert.throws(
    () => audit.input('C:\\Windows\\System32\\wsl.exe', ['--exec', '/control', 'credential-stdin'], Buffer.alloc(0), { env: {} }),
    (error) => error.code === 'unsafe_process',
  );
  assert.throws(
    () => audit.input('C:\\Windows\\System32\\wsl.exe', ['--exec', '/control', 'update-stdin'], Buffer.alloc(0), { env: {} }),
    (error) => error.code === 'unsafe_process',
  );
  assert.throws(
    () => audit.exec('C:\\Windows\\System32\\wsl.exe', ['--shutdown'], { env: {} }),
    (error) => error.code === 'unsafe_process',
  );
  assert.throws(
    () => audit.exec('C:\\Program Files\\Docker\\docker.exe', ['ps'], { env: {} }),
    (error) => error.code === 'unsafe_process',
  );
  assert.deepEqual(audit.summary(), {
    commandCount: 1,
    binaries: { 'wsl.exe': 1 },
    dockerCommandsObserved: 0,
    forbiddenCommandsObserved: 0,
    updateCommandsObserved: 0,
    sensitiveEnvironmentKeysObserved: 0,
    stdinOnlyControlDocuments: 0,
  });

  const bootstrapAudit = new AuditedProcessRunner(delegate, { allowTerminate: true, allowUpdate: true });
  await bootstrapAudit.input(
    'C:\\Windows\\System32\\wsl.exe',
    ['--distribution', 'AppleMusicDownloader', '--exec', '/control', 'update-stdin'],
    Buffer.from('{}'),
    { env: { PATH: 'safe' } },
  );
  await bootstrapAudit.exec(
    'C:\\Windows\\System32\\wsl.exe',
    ['--terminate', 'AppleMusicDownloader'],
    { env: { PATH: 'safe' } },
  );
  assert.equal(bootstrapAudit.summary().updateCommandsObserved, 1);

  const networkOnlyAudit = new AuditedProcessRunner(delegate, { networkOnly: true });
  await networkOnlyAudit.input(
    'C:\\Windows\\System32\\wsl.exe',
    ['--distribution', 'AppleMusicDownloader', '--exec', '/control', 'network-prepare-stdin'],
    Buffer.from('{}'),
    { env: { PATH: 'safe' } },
  );
  await networkOnlyAudit.exec(
    'C:\\Windows\\System32\\wsl.exe',
    ['--distribution', 'AppleMusicDownloader', '--exec', '/control', 'network-preflight'],
    { env: { PATH: 'safe' } },
  );
  await networkOnlyAudit.input(
    'C:\\Windows\\System32\\wsl.exe',
    ['--distribution', 'AppleMusicDownloader', '--exec', '/control', 'network-cleanup-stdin'],
    Buffer.from('{"schemaVersion":1,"generationId":"' + 'a'.repeat(64) + '","version":"1.3.0"}'),
    { env: { PATH: 'safe' } },
  );
  for (const operation of ['credential-stdin', 'start', 'update-stdin', '--terminate', '--unregister', '--shutdown']) {
    assert.throws(
      () => networkOnlyAudit.exec('C:\\Windows\\System32\\wsl.exe', ['--exec', '/control', operation], { env: {} }),
      (error) => error.code === 'unsafe_process',
    );
  }
});

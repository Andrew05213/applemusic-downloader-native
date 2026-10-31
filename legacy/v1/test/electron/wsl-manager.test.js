const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const { EventEmitter } = require('node:events');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { PassThrough } = require('node:stream');

const {
  CONTROLLED_DOWNLOAD_MOUNT,
  DESKTOP_PROXY_HEADER,
  GO_UPDATE_CLIENT_TIMEOUT_MS,
  KEEPALIVE_PATH,
  SUPERVISOR_READY_TIMEOUT_MS,
  SUPERVISOR_UPDATE_READY_TIMEOUT_MS,
  UPDATE_TIMEOUT_MS,
  WINDOWS_LOCK_COMMANDS,
  WslManager,
  WslManagerError,
  acquireWindowsProcessLock,
  classifyWslFailure,
  controlErrorFromProcess,
  diagnosticBuildIdentity,
  parseControlStatus,
  parseDistroList,
  sanitizeProcessEnvironment,
  validateDownloadPathOutsideRoots,
  validateWindowsDownloadPath,
} = require('../../electron/wsl-manager');
const { createManifest } = require('./fixtures/appliance-manifest-document');
const { validateApplianceManifest } = require('../../electron/appliance-manifest');
const {
  BOOTSTRAP_FILE,
  INCIDENT_SOURCE,
  WINDOWS_HELPER_FILE,
  createInteropRecoveryDescriptor,
} = require('../../electron/wslinterop-recovery');
const { createInteropIncidentManifest } = require('./fixtures/wslinterop-incident-manifest');

const TRUSTED_WSL = 'C:\\Windows\\System32\\wsl.exe';
const WINDOWS_HELPER_PATH = path.resolve(__dirname, '../../electron/bin/amdl-windows-probe.exe');
const WINDOWS_HELPER_WSL_PATH = '/mnt/c/Program Files/Apple Music Downloader/resources/bin/amdl-windows-probe.exe';
const WINDOWS_HELPER_SHA256 = crypto.createHash('sha256').update(fs.readFileSync(WINDOWS_HELPER_PATH)).digest('hex');
const WINDOWS_HELPER_SIZE = fs.lstatSync(WINDOWS_HELPER_PATH).size;
const INITIAL_RUNTIME_TRANSACTION = '1'.repeat(64);
const INSTALL_OBJECT_ID = '00112233:445566778899aabb';
const HEALTH = { statusCode: 200, payload: { service: 'applemusic-downloader', runtime: 'wsl-appliance', version: '1.2.0' } };
const NETWORK_GENERATION = '9'.repeat(64);

test('desktop build identity requires the complete matching v1.2.13 runtime contract', () => {
  const manifest = validateApplianceManifest(createManifest({ version: '1.2.13' }));
  const status = {
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: WINDOWS_HELPER_SHA256,
  };
  const identity = diagnosticBuildIdentity(manifest, status, WINDOWS_HELPER_SHA256);
  assert.equal(identity.productVersion, '1.2.13');
  assert.match(identity.desktopBuildId, /^[a-f0-9]{64}$/);
  assert.equal(identity.runtimeTransactionId, INITIAL_RUNTIME_TRANSACTION);

  const stoppedIdentity = diagnosticBuildIdentity(
    manifest,
    { ...status, windowsHelperSha256: '', network: { state: 'unprepared' } },
    WINDOWS_HELPER_SHA256,
  );
  assert.equal(stoppedIdentity.windowsHelperSha256, WINDOWS_HELPER_SHA256);
  assert.throws(
    () => diagnosticBuildIdentity(
      manifest,
      { ...status, windowsHelperSha256: '', network: { state: 'ready' } },
      WINDOWS_HELPER_SHA256,
    ),
    /windowsHelperSha256 mismatch/,
  );

  assert.throws(
    () => diagnosticBuildIdentity(manifest, { ...status, rendererSha256: 'f'.repeat(64) }, WINDOWS_HELPER_SHA256),
    /rendererSha256 mismatch/,
  );
  assert.notEqual(
    identity.desktopBuildId,
    diagnosticBuildIdentity(
      manifest,
      { ...status, windowsHelperSha256: 'e'.repeat(64) },
      'e'.repeat(64),
    ).desktopBuildId,
  );
});

test('v1.3.3 desktop build identity binds the fixed Android CA path and measured digest', () => {
  const manifest = validateApplianceManifest(createManifest({ version: '1.3.3' }));
  const recovery = {
    sha256: 'c'.repeat(64),
    descriptor: {
      schemaVersion: 1,
      targetVersion: manifest.version,
      source: { ...INCIDENT_SOURCE },
      bootstrap: { file: BOOTSTRAP_FILE, sha256: 'b'.repeat(64), size: 1 },
      windowsHelper: { file: WINDOWS_HELPER_FILE, sha256: WINDOWS_HELPER_SHA256, size: 1 },
    },
  };
  const recoveryIdentity = {
    bootstrapSha256: recovery.descriptor.bootstrap.sha256,
    recovery,
  };
  const status = {
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    androidCaBundlePath: manifest.inputs.androidCaBundlePath,
    androidCaBundleSha256: manifest.inputs.androidCaBundleSha256,
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: WINDOWS_HELPER_SHA256,
  };
  const identity = diagnosticBuildIdentity(manifest, status, WINDOWS_HELPER_SHA256, recoveryIdentity);
  assert.equal(identity.androidCaBundlePath, '/usr/local/android/cert.pem');
  assert.equal(identity.androidCaBundleSha256, manifest.inputs.androidCaBundleSha256);
  assert.throws(
    () => diagnosticBuildIdentity(
      manifest,
      { ...status, androidCaBundlePath: '/etc/ssl/certs/ca-certificates.crt' },
      WINDOWS_HELPER_SHA256,
      recoveryIdentity,
    ),
    /androidCaBundlePath mismatch/,
  );
  assert.throws(
    () => diagnosticBuildIdentity(
      manifest,
      { ...status, androidCaBundleSha256: '0'.repeat(64) },
      WINDOWS_HELPER_SHA256,
      recoveryIdentity,
    ),
    /androidCaBundleSha256 mismatch/,
  );
  const { androidCaBundleSha256: omitted, ...withoutCA } = status;
  assert.equal(omitted, manifest.inputs.androidCaBundleSha256);
  assert.throws(
    () => diagnosticBuildIdentity(manifest, withoutCA, WINDOWS_HELPER_SHA256, recoveryIdentity),
    /androidCaBundleSha256 mismatch/,
  );
  const { androidCaBundlePath: omittedPath, ...withoutCAPath } = status;
  assert.equal(omittedPath, manifest.inputs.androidCaBundlePath);
  assert.throws(
    () => diagnosticBuildIdentity(manifest, withoutCAPath, WINDOWS_HELPER_SHA256, recoveryIdentity),
    /androidCaBundlePath mismatch/,
  );
});

test('v1.3.5 desktop build identity binds the standalone native preflight digest', () => {
  const manifest = validateApplianceManifest(createManifest({ version: '1.3.5' }));
  const recovery = {
    sha256: 'c'.repeat(64),
    descriptor: {
      schemaVersion: 1,
      targetVersion: manifest.version,
      source: { ...INCIDENT_SOURCE },
      bootstrap: { file: BOOTSTRAP_FILE, sha256: 'b'.repeat(64), size: 1 },
      windowsHelper: { file: WINDOWS_HELPER_FILE, sha256: WINDOWS_HELPER_SHA256, size: 1 },
    },
  };
  const recoveryIdentity = {
    bootstrapSha256: recovery.descriptor.bootstrap.sha256,
    recovery,
  };
  const status = {
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    androidNetworkPreflightBuiltSha256: manifest.inputs.androidNetworkPreflightBuiltSha256,
    androidCaBundlePath: manifest.inputs.androidCaBundlePath,
    androidCaBundleSha256: manifest.inputs.androidCaBundleSha256,
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: WINDOWS_HELPER_SHA256,
  };
  const identity = diagnosticBuildIdentity(manifest, status, WINDOWS_HELPER_SHA256, recoveryIdentity);
  assert.equal(
    identity.androidNetworkPreflightBuiltSha256,
    manifest.inputs.androidNetworkPreflightBuiltSha256,
  );
  assert.throws(
    () => diagnosticBuildIdentity(
      manifest,
      { ...status, androidNetworkPreflightBuiltSha256: '0'.repeat(64) },
      WINDOWS_HELPER_SHA256,
      recoveryIdentity,
    ),
    /androidNetworkPreflightBuiltSha256 mismatch/,
  );
  const { androidNetworkPreflightBuiltSha256: omitted, ...withoutProbe } = status;
  assert.equal(omitted, manifest.inputs.androidNetworkPreflightBuiltSha256);
  assert.throws(
    () => diagnosticBuildIdentity(manifest, withoutProbe, WINDOWS_HELPER_SHA256, recoveryIdentity),
    /androidNetworkPreflightBuiltSha256 mismatch/,
  );
});

test('v1.2.14 desktop build identity binds recovery descriptor and bootstrap bytes', () => {
  const manifest = validateApplianceManifest(createManifest({ version: '1.2.14' }));
  const status = {
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: WINDOWS_HELPER_SHA256,
  };
  const recovery = {
    sha256: 'c'.repeat(64),
    descriptor: {
      schemaVersion: 1,
      targetVersion: manifest.version,
      source: { ...INCIDENT_SOURCE },
      bootstrap: { file: BOOTSTRAP_FILE, sha256: 'b'.repeat(64), size: 1 },
      windowsHelper: { file: WINDOWS_HELPER_FILE, sha256: WINDOWS_HELPER_SHA256, size: 1 },
    },
  };
  const identity = diagnosticBuildIdentity(manifest, status, WINDOWS_HELPER_SHA256, {
    bootstrapSha256: recovery.descriptor.bootstrap.sha256,
    recovery,
  });
  assert.equal(identity.wslInteropBootstrapSha256, recovery.descriptor.bootstrap.sha256);
  assert.equal(identity.wslInteropRecoveryDescriptorSha256, recovery.sha256);
  assert.match(identity.desktopBuildId, /^[a-f0-9]{64}$/);
  assert.throws(
    () => diagnosticBuildIdentity(manifest, status, WINDOWS_HELPER_SHA256, {
      bootstrapSha256: recovery.descriptor.bootstrap.sha256,
      recovery: { ...recovery, sha256: 'D'.repeat(64) },
    }),
    /recovery descriptor identity is unavailable/,
  );
  assert.throws(
    () => diagnosticBuildIdentity(manifest, status, WINDOWS_HELPER_SHA256, {
      bootstrapSha256: 'd'.repeat(64),
      recovery,
    }),
    /bootstrap SHA-256 mismatch/,
  );
});

test('diagnostics publishes the complete desktop and appliance build identity together', async () => {
  const manifest = validateApplianceManifest(createManifest({ version: '1.2.13' }));
  const status = {
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: manifest.version,
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: WINDOWS_HELPER_SHA256,
  };
  const manager = new WslManager(baseOptions({
    runner: {
      exec: async (_command, args) => {
        if (args.at(-1) === 'status') {
          return { stdout: JSON.stringify({ ok: true, status }), stderr: '' };
        }
        if (args.at(-1) === 'diagnostics') {
          return { stdout: JSON.stringify({ ok: true, status, logs: [] }), stderr: '' };
        }
        return { stdout: 'AppleMusicDownloader Running 2', stderr: '' };
      },
      input: async () => ({ stdout: '', stderr: '' }),
    },
    fileSha256: async () => WINDOWS_HELPER_SHA256,
  }));
  await manager.writeState({ manifest, installedVersion: manifest.version });

  const output = await manager.diagnostics();
  const first = JSON.parse(output.split(/\r?\n/, 1)[0]);
  assert.equal(first.buildIdentity.productVersion, '1.2.13');
  assert.equal(first.buildIdentity.applianceBuildId, manifest.applianceBuildId);
  assert.equal(first.buildIdentity.runtimeSha256, manifest.runtime.sha256);
  assert.equal(first.buildIdentity.runtimeTransactionId, INITIAL_RUNTIME_TRANSACTION);
  assert.equal(first.buildIdentity.androidMainBuiltSha256, manifest.inputs.androidMainBuiltSha256);
  assert.equal(first.buildIdentity.rendererSha256, manifest.inputs.rendererSha256);
  assert.equal(first.buildIdentity.windowsHelperSha256, WINDOWS_HELPER_SHA256);
  assert.match(first.buildIdentity.desktopBuildId, /^[a-f0-9]{64}$/);
});

test('stopped diagnostics still publishes identity from the measured packaged helper', async () => {
  const manifest = validateApplianceManifest(createManifest({ version: '1.2.13' }));
  const status = {
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: manifest.version,
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    runtimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256: '',
    network: { state: 'unprepared' },
  };
  const manager = new WslManager(baseOptions({
    runner: {
      exec: async (_command, args) => {
        if (args.at(-1) === 'status') return { stdout: JSON.stringify({ ok: true, status }), stderr: '' };
        if (args.at(-1) === 'diagnostics') return { stdout: JSON.stringify({ ok: true, status, logs: [] }), stderr: '' };
        return { stdout: 'AppleMusicDownloader Stopped 2', stderr: '' };
      },
      input: async () => ({ stdout: '', stderr: '' }),
    },
    fileSha256: async () => WINDOWS_HELPER_SHA256,
  }));
  await manager.writeState({ manifest, installedVersion: manifest.version });

  const output = await manager.diagnostics();
  const first = JSON.parse(output.split(/\r?\n/, 1)[0]);
  assert.equal(first.buildIdentity.windowsHelperSha256, WINDOWS_HELPER_SHA256);
  assert.match(first.buildIdentity.desktopBuildId, /^[a-f0-9]{64}$/);
});

function networkControlStatus(generationId, {
  state,
  stage,
  windowsPath,
  wslDirectPath = 'unknown',
  proxyPropagated,
}) {
  return {
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: '1.2.0',
    state: 'stopped',
    supervisorPid: 9100,
    network: {
      schemaVersion: 1,
      path: 'windows_stdio_bridge',
      state,
      stage,
      windowsPath,
      wslDirectPath,
      proxyPropagated,
      generationId,
    },
  };
}

function unpreparedNetworkStatus() {
  return {
    schemaVersion: 1,
    path: 'windows_stdio_bridge',
    state: 'unprepared',
    windowsPath: 'unknown',
    wslDirectPath: 'unknown',
    proxyPropagated: false,
  };
}

test('Electron network status validation matches the Go supervisor JSON field set', () => {
  const source = fs.readFileSync(path.join(
    __dirname,
    '..',
    '..',
    'apple-music-downloader',
    'internal',
    'appliance',
    'types.go',
  ), 'utf8');
  const networkStatus = /type NetworkStatus struct \{([\s\S]*?)\n\}/.exec(source);
  assert.ok(networkStatus);
  const fields = [...networkStatus[1].matchAll(/\x60json:\"([^\",]+)(?:,omitempty)?\"\x60/g)]
    .map((match) => match[1])
    .sort();
  assert.deepEqual(fields, [
    'caSource',
    'curlCode',
    'generationId',
    'path',
    'proxyPropagated',
    'schemaVersion',
    'sslVerifyResult',
    'stage',
    'state',
    'windowsPath',
    'wslDirectPath',
  ]);
  assert.equal(/\bReady\b/.test(networkStatus[1]), false);
});

function manifestDigest(manifest) {
  const normalized = validateApplianceManifest(manifest);
  return crypto.createHash('sha256').update(JSON.stringify(normalized), 'utf8').digest('hex');
}

function tempAppData() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-wsl-manager-'));
}

function baseOptions(overrides = {}) {
  const appDataDir = overrides.appDataDir || tempAppData();
  const installDir = overrides.installDir || path.join(appDataDir, 'wsl');
  return {
    platform: 'win32',
    arch: 'x64',
    release: '10.0.26100',
    appDataDir,
    installDir,
    wslExe: TRUSTED_WSL,
    windowsProbeExe: WINDOWS_HELPER_PATH,
    fileSha256: async () => WINDOWS_HELPER_SHA256,
    windowsHelperBindingProvider: () => ({
      file: WINDOWS_HELPER_FILE,
      sha256: WINDOWS_HELPER_SHA256,
      size: WINDOWS_HELPER_SIZE,
    }),
    networkGenerationProvider: () => NETWORK_GENERATION,
    downloadDir: path.join(appDataDir, '测试 用户', 'Music', 'Apple Music Downloader'),
    pathCanonicalizer: async (value) => ({
      version: 1,
      canonicalPath: path.win32.normalize(value),
      fileSystem: 'NTFS',
      reparsePoint: false,
      shortPathAlias: false,
      objectId: INSTALL_OBJECT_ID,
    }),
    registrationIdentityProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      basePath: installDir,
      wslVersion: 2,
    }),
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
    systemInfo: async () => ({ virtualizationEnabled: true, rebootRequired: false }),
    selectBackendPort: async (preferred) => preferred,
    verifyArtifact: async (_artifact, filename) => filename,
    processLockProvider: async () => ({
      assertHeld() { return this; },
      async release() {},
    }),
    keeperLauncher: () => ({ exitCode: null, killed: false, once() {}, unref() {} }),
    proxyFactory: () => {
      let listeningPort = null;
      return {
        start: async (port) => {
          if (listeningPort === null) listeningPort = port === 0 ? 54321 : port;
          return listeningPort;
        },
        stop: async () => { listeningPort = null; },
        diagnostics: () => '',
      };
    },
    ...overrides,
  };
}

function healthyRunner({
  installed = false,
  calls = [],
  initialServiceState,
  initialNetworkState,
  runtimeVersion: initialRuntimeVersion = '1.2.0',
  runtimeSha256: initialRuntimeSha256 = 'b'.repeat(64),
  runtimeTransactionId: initialRuntimeTransactionId = INITIAL_RUNTIME_TRANSACTION,
  statusIdentity = {},
} = {}) {
  let distroInstalled = installed;
  let serviceState = initialServiceState || (installed ? 'running' : 'stopped');
  let supervisorPid = 9100;
  let runtimeVersion = initialRuntimeVersion;
  let runtimeSha256 = initialRuntimeSha256;
  let runtimeTransactionId = initialRuntimeTransactionId;
  let ownerMarker = '';
  let networkState = initialNetworkState
    || ((serviceState === 'running' || serviceState === 'starting') ? 'ready' : 'unprepared');
  let networkGenerationId = networkState === 'unprepared' ? '' : NETWORK_GENERATION;
  const status = () => ({
    ...statusIdentity,
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: runtimeVersion,
    runtimeSha256,
    runtimeTransactionId,
    updateInProgress: false,
    state: serviceState,
    supervisorPid,
    network: networkGenerationId
      ? networkControlStatus(networkGenerationId, networkState === 'ready' ? {
        state: 'ready',
        stage: 'complete',
        windowsPath: 'reachable',
        proxyPropagated: true,
      } : {
        state: 'prepared',
        stage: 'prepared',
        windowsPath: 'unknown',
        proxyPropagated: false,
      }).network
      : unpreparedNetworkStatus(),
  });
  const applyRuntimeUpdate = (document) => {
    runtimeVersion = document.version;
    runtimeSha256 = document.sha256;
    runtimeTransactionId = document.transactionId;
    supervisorPid += 1;
    serviceState = 'stopped';
    networkGenerationId = '';
    networkState = 'unprepared';
  };
  return {
    commitUpdate: applyRuntimeUpdate,
    exec: async (command, args, options) => {
      calls.push({ type: 'exec', command, args: [...args], options });
      if (args[0] === '--version') return { stdout: 'WSL version: 2.6.3.0\n', stderr: '' };
      if (args[0] === '--status') return { stdout: 'Default Version: 2\n', stderr: '' };
      if (args[0] === '--list') {
        return { stdout: distroInstalled ? 'NAME                   STATE           VERSION\n* AppleMusicDownloader  Stopped         2\n' : 'NAME STATE VERSION\n', stderr: '' };
      }
      if (args.at(-1) === 'probe') return { stdout: JSON.stringify({ service: 'applemusic-downloader', runtime: 'wsl-appliance', version: runtimeVersion }), stderr: '' };
      if (args.at(-1) === 'status') return { stdout: JSON.stringify(status()), stderr: '' };
      if (command === WINDOWS_HELPER_PATH && args[0] === 'canonicalize-file') {
        return {
          stdout: JSON.stringify({
            version: 1,
            canonicalPath: WINDOWS_HELPER_PATH,
            fileSystem: 'NTFS',
            reparsePoint: false,
            shortPathAlias: false,
            objectId: INSTALL_OBJECT_ID,
            objectType: 'file',
          }),
          stderr: '',
        };
      }
      if (args.includes('/usr/bin/cat') && args.includes('/var/lib/applemusic-downloader/desktop-owner-id')) {
        return { stdout: ownerMarker, stderr: '' };
      }
      if (args.at(-1) === 'start') {
        if (!networkGenerationId || networkState !== 'ready') {
          throw new Error('backend start requires a prepared network generation');
        }
        serviceState = 'running';
        return { stdout: JSON.stringify(status()), stderr: '' };
      }
      if (args.at(-1) === 'stop') {
        serviceState = 'stopped';
        networkGenerationId = '';
        networkState = 'unprepared';
        return { stdout: JSON.stringify(status()), stderr: '' };
      }
      if (args.at(-1) === 'network-preflight') {
        if (!networkGenerationId) throw new Error('network preflight requires a prepared generation');
        networkState = 'ready';
        return {
          stdout: JSON.stringify({
            ...status(),
            network: networkControlStatus(networkGenerationId, {
              state: 'ready',
              stage: 'complete',
              windowsPath: 'reachable',
              proxyPropagated: true,
            }).network,
          }),
          stderr: '',
        };
      }
      if (args.includes('/usr/bin/wslpath') && args.at(-1) === WINDOWS_HELPER_PATH) {
        return { stdout: `${WINDOWS_HELPER_WSL_PATH}\n`, stderr: '' };
      }
      if (args.includes('/usr/bin/wslpath')) return { stdout: '/mnt/c/Users/测试 用户/Music/Apple Music Downloader\n', stderr: '' };
      if (args[0] === 'configure-download-locked') {
        return {
          stdout: JSON.stringify({
            version: 1,
            canonicalPath: args[1],
            fileSystem: 'NTFS',
            reparsePoint: false,
            shortPathAlias: false,
            ...(args[2] ? { objectId: args[2] } : {}),
            wslPath: '/mnt/c/Users/测试 用户/Music/Apple Music Downloader',
          }),
          stderr: '',
        };
      }
      if (args[0] === 'import-rootfs-locked') return { stdout: JSON.stringify({ version: 1 }), stderr: '' };
      if (args[0] === '--import') {
        distroInstalled = true;
        return { stdout: '', stderr: '' };
      }
      if (args[0] === '--unregister') {
        distroInstalled = false;
        return { stdout: '', stderr: '' };
      }
      return { stdout: '{}', stderr: '' };
    },
    input: async (command, args, input, options) => {
      calls.push({ type: 'input', command, args: [...args], input: Buffer.from(input), options });
      if (args.includes('/usr/bin/dd')) ownerMarker = input.toString('ascii');
      if (args.at(-1) === 'update-stdin') {
        const document = JSON.parse(input.toString('utf8'));
        applyRuntimeUpdate(document);
      }
      if (args.at(-1) === 'network-prepare-stdin') {
        const document = JSON.parse(input.toString('utf8'));
        networkGenerationId = document.generationId;
        networkState = 'prepared';
        return {
          stdout: JSON.stringify({
            ...status(),
            network: networkControlStatus(networkGenerationId, {
              state: 'prepared',
              stage: 'prepared',
              windowsPath: 'unknown',
              proxyPropagated: false,
            }).network,
          }),
          stderr: '',
        };
      }
      if (args.at(-1) === 'network-cleanup-stdin') {
        const document = JSON.parse(input.toString('utf8'));
        if (serviceState !== 'stopped'
            || document.schemaVersion !== 1
            || document.version !== runtimeVersion
            || document.generationId !== networkGenerationId) {
          throw new Error('network cleanup rejected');
        }
        networkGenerationId = '';
        networkState = 'unprepared';
        return { stdout: JSON.stringify(status()), stderr: '' };
      }
      return { stdout: JSON.stringify(status()), stderr: '' };
    },
  };
}

function createInteropRecoveryResources(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl recovery-'));
  const windowsHelperPath = path.join(directory, WINDOWS_HELPER_FILE);
  const bootstrapPath = path.join(directory, BOOTSTRAP_FILE);
  fs.writeFileSync(windowsHelperPath, Buffer.from('test Windows helper resource\n', 'utf8'), { mode: 0o600 });
  fs.writeFileSync(bootstrapPath, Buffer.from('test Linux bootstrap resource\n', 'utf8'), { mode: 0o600 });
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  return {
    windowsHelperPath,
    bootstrapPath,
    descriptor: createInteropRecoveryDescriptor({
      targetVersion: '1.2.14',
      bootstrapPath,
      windowsHelperPath,
    }),
  };
}

function attachInteropRecoveryResourceProbes(runner, calls, { windowsHelperPath, bootstrapPath }) {
  const originalExec = runner.exec;
  const controlled = new Set([
    path.win32.normalize(windowsHelperPath).toLowerCase(),
    path.win32.normalize(bootstrapPath).toLowerCase(),
  ]);
  runner.exec = async (command, args, options) => {
    if (command === windowsHelperPath && args[0] === 'canonicalize-file') {
      const candidate = path.win32.normalize(args[1]);
      if (!controlled.has(candidate.toLowerCase())) throw new Error('unexpected recovery resource probe');
      calls.push({ type: 'exec', command, args: [...args], options });
      return {
        stdout: JSON.stringify({
          version: 1,
          canonicalPath: candidate,
          fileSystem: 'NTFS',
          reparsePoint: false,
          shortPathAlias: false,
          objectId: candidate.toLowerCase() === path.win32.normalize(windowsHelperPath).toLowerCase()
            ? INSTALL_OBJECT_ID
            : '11223344:5566778899aabbcc',
          objectType: 'file',
        }),
        stderr: '',
      };
    }
    if (args.includes('/usr/bin/wslpath') && controlled.has(path.win32.normalize(args.at(-1)).toLowerCase())) {
      calls.push({ type: 'exec', command, args: [...args], options });
      return {
        stdout: `/mnt/c/Program Files/Apple Music Downloader/resources/bin/${path.win32.basename(args.at(-1))}\n`,
        stderr: '',
      };
    }
    return originalExec(command, args, options);
  };
}

function incidentSupervisorIdentity(overrides = {}) {
  return {
    productVersion: INCIDENT_SOURCE.productVersion,
    applianceBuildId: INCIDENT_SOURCE.applianceBuildId,
    androidMainBuiltSha256: INCIDENT_SOURCE.androidMainBuiltSha256,
    rendererSha256: INCIDENT_SOURCE.rendererSha256,
    windowsHelperSha256: INCIDENT_SOURCE.windowsHelperSha256,
    ...overrides,
  };
}

function windowsLockSpawner() {
  const held = new Set();
  const calls = [];
  const spawn = (command, args, options) => {
    const child = new EventEmitter();
    child.stdin = new PassThrough();
    child.stdout = new PassThrough();
    child.stderr = new PassThrough();
    child.closed = false;
    const lockCommand = args[0];
    const close = (code, signal = null) => {
      if (child.closed) return;
      child.closed = true;
      queueMicrotask(() => child.emit('close', code, signal));
    };
    child.kill = () => {
      if (child.acquired) held.delete(lockCommand);
      close(null, 'SIGTERM');
    };
    child.stdin.once('finish', () => {
      if (child.acquired) held.delete(lockCommand);
      close(child.acquired ? 0 : 3);
    });
    calls.push({ command, args: [...args], options: { ...options } });
    queueMicrotask(() => {
      if (held.has(lockCommand)) {
        child.stdout.write(`${JSON.stringify({ version: 1, acquired: false, reason: 'busy' })}\n`);
      } else {
        held.add(lockCommand);
        child.acquired = true;
        child.stdout.write(`${JSON.stringify({ version: 1, acquired: true })}\n`);
      }
    });
    return child;
  };
  return { calls, held, spawn };
}

async function seedInstalledState(manager, manifest = createManifest(), { ownership = true } = {}) {
  await manager.writeState({
    manifest,
    installedVersion: manifest.version,
    installedRuntimeSha256: manifest.runtime.sha256,
    installedRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
  });
  if (ownership) await seedOwnership(manager);
}

async function seedOwnership(manager, ownerId = 'e'.repeat(64)) {
  fs.mkdirSync(path.dirname(manager.ownershipPath), { recursive: true });
  fs.writeFileSync(manager.ownershipPath, ownerId, { encoding: 'utf8', mode: 0o600 });
  await manager.writeState({
    ownership: {
      schemaVersion: 1,
      ownerId,
      distroName: 'AppleMusicDownloader',
      installDir: manager.installDir,
      createdAt: '2026-07-30T00:00:00.000Z',
    },
    installDir: manager.installDir,
  });
  await manager.writeDistroOwnership(ownerId);
}

test('update timeout layers remain strictly outside the Go client and supervisor readiness windows', () => {
  assert.equal(SUPERVISOR_READY_TIMEOUT_MS, 180_000);
  assert.equal(SUPERVISOR_UPDATE_READY_TIMEOUT_MS < GO_UPDATE_CLIENT_TIMEOUT_MS, true);
  assert.equal(GO_UPDATE_CLIENT_TIMEOUT_MS < UPDATE_TIMEOUT_MS, true);
});

test('v1.2.15 refuses a cross-version Appliance before artifacts, proxy, control or credentials can be touched', async () => {
  const existing = createManifest({ version: '1.2.14' });
  const target = createManifest({ version: '1.2.15' });
  const manager = new WslManager(baseOptions());
  await seedInstalledState(manager, existing, { ownership: false });
  let artifacts = 0;
  let controls = 0;
  let proxyStops = 0;
  let registeredDistroChecks = 0;
  let localEndpoints = 0;
  let ownershipChecks = 0;
  let credentialReads = 0;
  manager.cacheArtifact = async () => { artifacts += 1; throw new Error('not reached'); };
  manager.runControl = async () => { controls += 1; throw new Error('not reached'); };
  manager.ensureProxyStopConfirmed = async () => { proxyStops += 1; throw new Error('not reached'); };
  manager.registeredDistro = async () => { registeredDistroChecks += 1; throw new Error('not reached'); };
  manager.prepareLocalEndpoint = async () => { localEndpoints += 1; throw new Error('not reached'); };
  manager.verifyDistroOwnership = async () => { ownershipChecks += 1; throw new Error('not reached'); };
  manager.credentialProvider = async () => { credentialReads += 1; throw new Error('not reached'); };

  await assert.rejects(
    manager.install(target),
    (error) => error instanceof WslManagerError && error.code === 'runtime_reinstall_required',
  );
  await assert.rejects(
    manager.update(target),
    (error) => error instanceof WslManagerError && error.code === 'runtime_reinstall_required',
  );
  await assert.rejects(
    manager.repair(target),
    (error) => error instanceof WslManagerError && error.code === 'runtime_reinstall_required',
  );
  assert.equal(artifacts, 0);
  assert.equal(controls, 0);
  assert.equal(proxyStops, 0);
  assert.equal(registeredDistroChecks, 0);
  assert.equal(localEndpoints, 0);
  assert.equal(ownershipChecks, 0);
  assert.equal(credentialReads, 0);
});

test('v1.2.15 gives observed distro identity precedence over a matching stale desktop record', async () => {
  const target = createManifest({ version: '1.2.15' });
  const manager = new WslManager(baseOptions());
  await seedInstalledState(manager, target, { ownership: false });
  let artifactRequests = 0;
  let configureCalls = 0;
  let startCalls = 0;
  let updateCalls = 0;
  manager.probe = async () => ({
    ok: true,
    distro: {
      installed: true,
      appliance: { version: '1.2.14' },
      supervisor: { status: { version: '1.2.14' } },
    },
  });
  manager.cacheArtifact = async () => { artifactRequests += 1; throw new Error('not reached'); };
  manager.configureDownloadPath = async () => { configureCalls += 1; throw new Error('not reached'); };
  manager.start = async () => { startCalls += 1; throw new Error('not reached'); };
  manager.update = async () => { updateCalls += 1; throw new Error('not reached'); };

  await assert.rejects(
    manager.install(target),
    (error) => error instanceof WslManagerError && error.code === 'runtime_reinstall_required',
  );
  assert.equal(artifactRequests, 0);
  assert.equal(configureCalls, 0);
  assert.equal(startCalls, 0);
  assert.equal(updateCalls, 0);
});

test('state and lifecycle locks use separate fixed helper argv and remain held until stdin closes', async () => {
  const harness = windowsLockSpawner();
  const manager = new WslManager(baseOptions({
    processEnvironment: { PATH: 'C:\\Windows\\System32' },
    runner: { spawn: harness.spawn },
  }));
  const stateLock = await acquireWindowsProcessLock(manager, 'state');
  const lifecycleLock = await acquireWindowsProcessLock(manager, 'lifecycle');
  assert.deepEqual(new Set(harness.held), new Set(Object.values(WINDOWS_LOCK_COMMANDS)));
  assert.deepEqual(harness.calls.slice(0, 2).map((call) => call.args), [
    [WINDOWS_LOCK_COMMANDS.state],
    [WINDOWS_LOCK_COMMANDS.lifecycle],
  ]);
  await assert.rejects(
    acquireWindowsProcessLock(manager, 'state', { timeoutMs: 1 }),
    (error) => error.code === 'windows_lock_timeout' && error.step === 'state_lock_timeout',
  );
  assert.equal(harness.held.has(WINDOWS_LOCK_COMMANDS.state), true);
  assert.equal(harness.calls.slice(2).every((call) => call.args[0] === WINDOWS_LOCK_COMMANDS.state), true);
  for (const call of harness.calls) {
    assert.equal(call.command, manager.windowsProbeExe);
    assert.equal(call.options.shell, false);
    assert.deepEqual(call.options.stdio, ['pipe', 'pipe', 'pipe']);
  }
  await lifecycleLock.release();
  assert.deepEqual(new Set(harness.held), new Set([WINDOWS_LOCK_COMMANDS.state]));
  await stateLock.release();
  assert.equal(harness.held.size, 0);
});

test('lock release rejects helper replacement observed after the child exits', async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-windows-lock-binding-'));
  const helperPath = path.join(directory, WINDOWS_HELPER_FILE);
  fs.copyFileSync(WINDOWS_HELPER_PATH, helperPath);
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const binding = {
    file: WINDOWS_HELPER_FILE,
    sha256: crypto.createHash('sha256').update(fs.readFileSync(helperPath)).digest('hex'),
    size: fs.lstatSync(helperPath).size,
  };
  const harness = windowsLockSpawner();
  const manager = new WslManager(baseOptions({
    windowsProbeExe: helperPath,
    windowsHelperBindingProvider: () => binding,
    runner: { spawn: harness.spawn },
  }));

  const lock = await acquireWindowsProcessLock(manager, 'state');
  fs.appendFileSync(helperPath, Buffer.from('binding drift\n', 'utf8'));
  await assert.rejects(
    lock.release(),
    (error) => error?.code === 'windows_lock_release_failed' && error?.step === 'state_lock_helper_identity',
  );
  assert.equal(harness.held.size, 0);
});

test('lock coordination failures surface stable Chinese remediation without raw helper details', async () => {
  const manager = new WslManager(baseOptions({
    processLockProvider: async () => {
      const error = new Error('Windows Appliance state_lock_timeout failed');
      error.code = 'windows_lock_timeout';
      throw error;
    },
  }));
  await assert.rejects(
    manager.writeState({ backendPort: 43124 }),
    (error) => error instanceof WslManagerError
      && error.code === 'windows_lock_timeout'
      && error.message === '另一个桌面操作仍在进行。'
      && !error.message.includes('state_lock_timeout'),
  );
});

test('Windows download paths reject relative, UNC, device and drive-root locations', () => {
  assert.equal(validateWindowsDownloadPath('C:\\Users\\测试 用户\\Music'), 'C:\\Users\\测试 用户\\Music');
  for (const candidate of ['relative\\Music', '\\\\server\\share', '\\\\?\\C:\\Music', 'C:\\']) {
    assert.throws(() => validateWindowsDownloadPath(candidate), (error) => error instanceof WslManagerError && error.code === 'path_unavailable');
  }
});

test('Windows download paths accept supported non-C local volumes', () => {
  const candidates = [
    'D:\\下载\\Apple Music Downloader',
    'E:\\开发\\Apple Music Downloader 中文',
    'F:\\2024元旦\\Music',
    'G:\\新建文件夹\\Apple Music Downloader',
  ];
  for (const candidate of candidates) {
    assert.equal(validateWindowsDownloadPath(candidate), candidate);
  }
});

test('download paths cannot overlap protected application or installation directories', () => {
  const protectedRoot = 'C:\\Users\\Andrew\\AppData\\Local\\Programs\\Apple Music Downloader';
  assert.throws(
    () => validateDownloadPathOutsideRoots(`${protectedRoot}\\Downloads`, [protectedRoot]),
    (error) => error.code === 'path_unavailable',
  );
  assert.throws(
    () => validateDownloadPathOutsideRoots('C:\\Users\\Andrew\\AppData\\Local\\Programs', [protectedRoot]),
    (error) => error.code === 'path_unavailable',
  );
  assert.equal(
    validateDownloadPathOutsideRoots('C:\\Users\\Andrew\\Music\\Apple Music Downloader', [protectedRoot]),
    'C:\\Users\\Andrew\\Music\\Apple Music Downloader',
  );
});

test('native canonical path results reject reparse points, short aliases and final-path collisions', async () => {
  const protectedRoot = 'C:\\Program Files\\Apple Music Downloader';
  for (const rejectedField of ['reparsePoint', 'shortPathAlias']) {
    const manager = new WslManager(baseOptions({
      protectedDownloadRoots: [],
      pathCanonicalizer: async (value) => ({
        version: 1,
        canonicalPath: value,
        fileSystem: 'NTFS',
        reparsePoint: rejectedField === 'reparsePoint',
        shortPathAlias: rejectedField === 'shortPathAlias',
      }),
    }));
    await assert.rejects(manager.canonicalDownloadPathOutsideRoots(manager.downloadDir), (error) => error.code === 'path_unavailable');
  }

  const manager = new WslManager(baseOptions({
    protectedDownloadRoots: [protectedRoot],
    downloadDir: 'C:\\PROGRA~1\\AMDL-DOWNLOADS',
    pathCanonicalizer: async (value) => ({
      version: 1,
      canonicalPath: value.includes('PROGRA~1') ? `${protectedRoot}\\Downloads` : protectedRoot,
      fileSystem: 'NTFS',
      reparsePoint: false,
      shortPathAlias: false,
    }),
  }));
  await assert.rejects(manager.canonicalDownloadPathOutsideRoots(manager.downloadDir), (error) => error.code === 'path_unavailable');
});

test('regular file canonicalization uses only the fixed canonicalize-file helper argv', async () => {
  const calls = [];
  const filename = 'C:\\Users\\测试 用户\\runtime bundle.tar.gz';
  const manager = new WslManager(baseOptions({
    runner: {
      exec: async (command, args, options) => {
        calls.push({ command, args: [...args], options });
        return { stdout: JSON.stringify({
          version: 1,
          canonicalPath: filename,
          fileSystem: 'NTFS',
          reparsePoint: false,
          shortPathAlias: false,
          objectType: 'file',
        }), stderr: '' };
      },
    },
  }));
  assert.deepEqual(await manager.canonicalizeWindowsFileWithHelper(filename), {
    canonicalPath: filename,
    fileSystem: 'NTFS',
  });
  assert.deepEqual(calls[0].args, ['canonicalize-file', filename]);
  assert.equal(calls[0].options.env, manager.processEnvironment);

  manager.runner.exec = async () => ({ stdout: JSON.stringify({
    version: 1,
    canonicalPath: filename,
    fileSystem: 'NTFS',
    reparsePoint: false,
    shortPathAlias: false,
    objectType: 'directory',
  }) });
  await assert.rejects(
    manager.canonicalizeWindowsFileWithHelper(filename),
    (error) => error.code === 'path_unavailable',
  );
});

test('default directory helper identity is normalized exactly once in protected-root checks', async () => {
  const calls = [];
  const downloadDir = 'C:\\Users\\Andrew\\Music\\Apple Music Downloader 中文';
  const protectedRoot = 'C:\\Users\\Andrew\\AppData\\Local\\AppleMusicDownloader';
  const options = baseOptions({
    downloadDir,
    protectedDownloadRoots: [protectedRoot],
    runner: {
      exec: async (_command, args) => {
        calls.push([...args]);
        const selected = args.at(-1);
        return { stdout: JSON.stringify({
          version: 1,
          canonicalPath: selected,
          fileSystem: 'NTFS',
          reparsePoint: false,
          shortPathAlias: false,
        }), stderr: '' };
      },
    },
  });
  delete options.pathCanonicalizer;
  const manager = new WslManager(options);
  assert.deepEqual(await manager.canonicalDownloadPathOutsideRoots(downloadDir), {
    canonicalPath: downloadDir,
    fileSystem: 'NTFS',
  });
  assert.deepEqual(calls.map((args) => args.slice(0, 2)), [
    ['canonicalize-path', downloadDir],
    ['canonicalize-path', protectedRoot],
  ]);
});

test('download directory identity is revalidated immediately before configure-stdin', async () => {
  const calls = [];
  let canonicalizations = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    pathCanonicalizer: async (value) => {
      canonicalizations += 1;
      return {
        version: 1,
        canonicalPath: value,
        fileSystem: 'NTFS',
        reparsePoint: canonicalizations === 2,
        shortPathAlias: false,
      };
    },
  }));
  await seedOwnership(manager);
  canonicalizations = 0;
  await assert.rejects(manager.configureDownloadPath(), (error) => error.code === 'path_unavailable');
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'configure-stdin'), false);
});

test('registered Unicode bind alias is safely reconstructed from its unbound Windows parent', async () => {
  const calls = [];
  const windowsPath = 'C:\\Users\\测试 用户\\Music\\Apple Music Downloader Smoke 中文 空格';
  const parent = 'C:\\Users\\测试 用户\\Music';
  const expected = '/mnt/c/Users/测试 用户/Music/Apple Music Downloader Smoke 中文 空格';
  const runner = {
    exec: async (command, args, options) => {
      calls.push({ command, args: [...args], options });
      if (args.at(-1) === windowsPath) return { stdout: `${CONTROLLED_DOWNLOAD_MOUNT}\n`, stderr: '' };
      if (args.at(-1) === parent) return { stdout: '/mnt/c/Users/测试 用户/Music\n', stderr: '' };
      throw new Error('unexpected command');
    },
    input: async () => { throw new Error('unexpected input'); },
  };
  const manager = new WslManager(baseOptions({ runner }));
  await manager.writeState({ downloadDir: windowsPath, downloadWslPath: expected });

  assert.equal(await manager.windowsPathToWsl(windowsPath), expected);
  assert.deepEqual(calls.map((call) => call.args), [
    ['--distribution', 'AppleMusicDownloader', '--exec', '/usr/bin/wslpath', '-a', '-u', windowsPath],
    ['--distribution', 'AppleMusicDownloader', '--exec', '/usr/bin/wslpath', '-a', '-u', parent],
  ]);
});

test('controlled bind fallback rejects mismatched persisted Windows and WSL identities', async () => {
  const windowsPath = 'C:\\Users\\测试 用户\\Music\\Apple Music Downloader';
  const parent = 'C:\\Users\\测试 用户\\Music';
  const calls = [];
  const runner = {
    exec: async (_command, args) => {
      calls.push([...args]);
      return args.at(-1) === parent
        ? { stdout: '/mnt/c/Users/测试 用户/Music\n', stderr: '' }
        : { stdout: `${CONTROLLED_DOWNLOAD_MOUNT}\n`, stderr: '' };
    },
    input: async () => { throw new Error('unexpected input'); },
  };
  const manager = new WslManager(baseOptions({ runner }));
  await manager.writeState({
    downloadDir: 'C:\\Users\\Other\\Music\\Apple Music Downloader',
    downloadWslPath: '/mnt/c/Users/Other/Music/Apple Music Downloader',
  });
  await assert.rejects(manager.windowsPathToWsl(windowsPath), (error) => error.code === 'path_unavailable');
  assert.equal(calls.length, 1);

  await manager.writeState({
    downloadDir: windowsPath,
    downloadWslPath: '/mnt/c/Users/测试 用户/Music/Apple Music Downloader-prefix-collision',
  });
  await assert.rejects(manager.windowsPathToWsl(windowsPath), (error) => error.code === 'path_unavailable');
  assert.equal(calls.length, 3);
});

test('bind fallback rejects other srv aliases and controlled-mount prefix collisions', async () => {
  const windowsPath = 'C:\\Users\\Andrew\\Music\\Apple Music Downloader';
  for (const alias of [
    '/srv/other/downloads',
    '/srv/applemusic-downloader/downloads-evil',
    '/srv/applemusic-downloader/downloads/child',
  ]) {
    const manager = new WslManager(baseOptions({
      runner: {
        exec: async () => ({ stdout: `${alias}\n`, stderr: '' }),
        input: async () => { throw new Error('unexpected input'); },
      },
    }));
    await manager.writeState({ downloadDir: windowsPath, downloadWslPath: '/mnt/c/Users/Andrew/Music/Apple Music Downloader' });
    await assert.rejects(manager.windowsPathToWsl(windowsPath), (error) => error.code === 'path_unavailable');
  }
});

test('bind fallback rejects an invalid parent or Windows device-name leaf', async () => {
  for (const windowsPath of ['C:\\OnlyLeaf', 'C:\\Users\\Andrew\\Music\\CON']) {
    let calls = 0;
    const manager = new WslManager(baseOptions({
      runner: {
        exec: async () => { calls += 1; return { stdout: `${CONTROLLED_DOWNLOAD_MOUNT}\n`, stderr: '' }; },
        input: async () => { throw new Error('unexpected input'); },
      },
    }));
    await manager.writeState({ downloadDir: windowsPath, downloadWslPath: '/mnt/c/invalid' });
    await assert.rejects(manager.windowsPathToWsl(windowsPath), (error) => error.code === 'path_unavailable');
    assert.equal(calls, 1);
  }
});

test('a protected download path is rejected before any WSL backend process starts', async () => {
  const calls = [];
  const protectedRoot = 'C:\\Users\\Andrew\\AppData\\Local\\Programs\\Apple Music Downloader';
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    protectedDownloadRoots: [protectedRoot],
    downloadDir: `${protectedRoot}\\Downloads`,
  }));
  await assert.rejects(manager.start(), (error) => error.code === 'path_unavailable');
  assert.equal(calls.length, 0);
});

test('distro parser handles UTF-16-decoded text, spaces and the default marker', () => {
  const rows = parseDistroList('NAME STATE VERSION\n* AppleMusicDownloader  Running  2\nMy Test Distro Stopped 1\n');
  assert.deepEqual(rows, [
    { name: 'AppleMusicDownloader', version: 2, raw: 'AppleMusicDownloader  Running  2' },
    { name: 'My Test Distro', version: 1, raw: 'My Test Distro Stopped 1' },
  ]);
});

test('control responses unwrap the supervisor status envelope', () => {
  assert.deepEqual(parseControlStatus(JSON.stringify({ ok: true, status: { service: 'applemusic-downloader', state: 'running' } }), 'status'), {
    service: 'applemusic-downloader', state: 'running',
  });
});

test('non-zero control responses preserve the appliance error code and action', () => {
  const processError = Object.assign(new Error('process exited'), {
    stdout: JSON.stringify({
      ok: false,
      status: { state: 'running' },
      error: { code: 'service_running', message: '服务运行中。', action: '请先停止服务。' },
    }),
  });
  const parsed = controlErrorFromProcess(processError, 'configure');
  assert.equal(parsed.name, 'ApplianceControlError');
  assert.equal(parsed.controlCode, 'service_running');
  assert.equal(parsed.message, '服务运行中。');
  assert.equal(parsed.controlAction, '请先停止服务。');
});

test('real HCS failures override unreliable firmware virtualization flags', () => {
  assert.equal(classifyWslFailure(new Error('WslRegisterDistribution failed with error: 0x80370102'), 'import_failed'), 'virtualization_disabled');
  assert.equal(classifyWslFailure(new Error('kernel component update required (0x800701bc)'), 'import_failed'), 'wsl_update_required');
});

test('WSL commands resolve through the native absolute System32 identity and never PATH', async () => {
  assert.throws(() => new WslManager(baseOptions({ wslExe: 'wsl.exe' })), /absolute trusted path/);
  const calls = [];
  const delegated = healthyRunner({ installed: false, calls });
  const manager = new WslManager(baseOptions({
    wslExe: null,
    runner: {
      exec: async (command, args, options) => {
        if (args[0] === 'system-paths') {
          return {
            stdout: JSON.stringify({
              version: 1,
              systemDirectory: 'C:\\Windows\\System32',
              wslPath: TRUSTED_WSL,
              shutdownPath: 'C:\\Windows\\System32\\shutdown.exe',
            }),
            stderr: '',
          };
        }
        return delegated.exec(command, args, options);
      },
      input: delegated.input,
    },
  }));
  const result = await manager.probe({ throwOnError: true });
  assert.equal(result.ok, true);
  assert.equal(calls.length > 0, true);
  assert.equal(calls.every((call) => call.command === TRUSTED_WSL), true);
});

test('Windows 10 and 11 probe the same owned WSL2 appliance without changing host networking', async () => {
  for (const release of ['10.0.19044', '10.0.19045', '10.0.19045.6093', '10.0.22000', '10.0.26100']) {
    for (const installed of [false, true]) {
      const calls = [];
      const manager = new WslManager(baseOptions({ release, runner: healthyRunner({ installed, calls }) }));
      if (installed) await seedOwnership(manager);
      const result = await manager.probe();
      assert.equal(result.ok, true, `${release}, installed=${installed}: ${result.error?.code}`);
      assert.equal(result.distro.installed, installed);
      assert.ok(calls.some((call) => call.args[0] === '--version'));
      assert.ok(calls.every((call) => !call.args.some((arg) => ['--shutdown', '--terminate', '--unregister', '--set-default', '--install', '--update'].includes(arg))));
    }
  }
});

test('old or malformed Windows releases fail before executing WSL', async () => {
  for (const release of ['10.0.19043', '6.3.9600', '10.0.NaN', '10.0.', 'garbage', '6.3.26100', '10.0.19045junk']) {
    const calls = [];
    const manager = new WslManager(baseOptions({ release, runner: healthyRunner({ calls }) }));
    const result = await manager.probe();
    assert.equal(result.error?.code, 'unsupported_windows', release);
    assert.equal(calls.length, 0);
    assert.match(result.error.action, /Windows 10/);
  }
});

test('Windows 10 still requires virtualization, reboot completion and a working modern WSL command', async () => {
  for (const [system, code] of [
    [{ virtualizationEnabled: false, rebootRequired: false }, 'virtualization_disabled'],
    [{ virtualizationEnabled: true, rebootRequired: true }, 'reboot_required'],
  ]) {
    const calls = [];
    const manager = new WslManager(baseOptions({ release: '10.0.19045', systemInfo: async () => system, runner: healthyRunner({ calls }) }));
    assert.equal((await manager.probe()).error?.code, code);
    assert.equal(calls.length, 0);
  }
  const manager = new WslManager(baseOptions({
    release: '10.0.19045', runner: { exec: async () => { throw new Error('Invalid command line option: --version'); } },
  }));
  assert.equal((await manager.probe()).error?.code, 'wsl_update_required');
});

test('probe reports Windows, virtualization, WSL and owned distro state', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await seedOwnership(manager);
  const result = await manager.probe({ throwOnError: true });
  assert.equal(result.ok, true);
  assert.equal(result.distro.installed, true);
  assert.equal(result.distro.version, 2);
  assert.equal(result.distro.supervisor.socketHealthy, true);
  assert.equal(result.distro.supervisor.status.supervisorPid, 9100);
  assert.equal(calls.every((call) => Array.isArray(call.args)), true);
  assert.equal(calls.some((call) => call.args.includes('probe')), true);
  assert.equal(calls.some((call) => call.args.includes('status')), true);
});

test('probe treats a valid appliance CLI with an unavailable supervisor socket as repairable corruption', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'status') throw new Error('connect appliance control socket: no such file');
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedOwnership(manager);

  const result = await manager.probe();

  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'distro_corrupt');
  assert.equal(result.error.details.installationIdentity.version, '1.2.0');
  assert.equal(result.error.details.supervisorSocketHealthy, false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'probe'), true);
});

test('probe treats an owned distro with a broken appliance CLI as repairable corruption', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'probe') {
      calls.push({ type: 'exec', command, args: [...args], options });
      throw new Error('exec appliance control: file not found');
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedOwnership(manager);

  const result = await manager.probe();

  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'distro_corrupt');
  assert.match(result.error.details.reason, /file not found/);
  assert.equal(calls.some((call) => call.args.at(-1) === 'probe'), true);
});

test('probe rejects an installed supervisor that omits release digest or transaction identity', async () => {
  const runner = healthyRunner({ installed: true });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    const result = await originalExec(command, args, options);
    if (args.at(-1) === 'status') {
      const status = JSON.parse(result.stdout);
      delete status.runtimeTransactionId;
      return { ...result, stdout: JSON.stringify(status) };
    }
    return result;
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedOwnership(manager);

  const result = await manager.probe();

  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'distro_corrupt');
  assert.match(result.error.details.reason, /\.amdl-release\.json/);
});

test('probe reconciles an interrupted update only from runtime digest and transaction identity', async () => {
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true }) }));
  await seedOwnership(manager);
  const sourceManifest = createManifest({
    runtime: { ...createManifest().runtime, sha256: 'a'.repeat(64) },
  });
  const targetManifest = createManifest();
  await manager.writeState({
    installedVersion: '1.2.0',
    installedRuntimeSha256: 'a'.repeat(64),
    installedRuntimeTransactionId: '0'.repeat(64),
    manifest: sourceManifest,
    pendingManifest: targetManifest,
    updateTransaction: {
      schemaVersion: 2,
      id: INITIAL_RUNTIME_TRANSACTION,
      mode: 'update',
      fromVersion: '1.2.0',
      toVersion: '1.2.0',
      fromRuntimeSha256: 'a'.repeat(64),
      toRuntimeSha256: 'b'.repeat(64),
      fromRuntimeTransactionId: '0'.repeat(64),
      fromManifestSha256: manifestDigest(sourceManifest),
      toManifestSha256: manifestDigest(targetManifest),
      startedAt: '2026-01-01T00:00:00.000Z',
    },
  });
  const result = await manager.probe({ throwOnError: true });
  assert.equal(result.ok, true);
  const state = await manager.readState();
  assert.equal(state.installedVersion, '1.2.0');
  assert.equal(state.installedRuntimeSha256, 'b'.repeat(64));
  assert.equal(state.installedRuntimeTransactionId, INITIAL_RUNTIME_TRANSACTION);
  assert.equal(state.updateTransaction, null);
});

test('same-version repair transaction stays unresolved when runtime transaction identity differs', async () => {
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true }) }));
  await seedOwnership(manager);
  const sourceManifest = createManifest();
  const targetManifest = createManifest();
  await manager.writeState({
    installedVersion: '1.2.0',
    installedRuntimeSha256: 'b'.repeat(64),
    installedRuntimeTransactionId: '0'.repeat(64),
    manifest: sourceManifest,
    pendingManifest: targetManifest,
    updateTransaction: {
      schemaVersion: 2,
      id: '2'.repeat(64),
      mode: 'repair',
      fromVersion: '1.2.0',
      toVersion: '1.2.0',
      fromRuntimeSha256: 'b'.repeat(64),
      toRuntimeSha256: 'b'.repeat(64),
      fromRuntimeTransactionId: '0'.repeat(64),
      fromManifestSha256: manifestDigest(sourceManifest),
      toManifestSha256: manifestDigest(targetManifest),
      startedAt: '2026-01-01T00:00:00.000Z',
    },
  });
  const result = await manager.probe();
  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'distro_corrupt');
  assert.match(result.error.details.reason, /digest\/transaction identity/);
  assert.equal((await manager.readState()).updateTransaction.id, '2'.repeat(64));
});

test('probe preserves a persisted host transaction while the matching supervisor update is still active', async () => {
  const transactionId = '2'.repeat(64);
  const sourceManifest = createManifest();
  const targetManifest = createManifest({
    version: '1.2.1',
    runtime: { ...createManifest().runtime, sha256: 'c'.repeat(64) },
  });
  const runner = healthyRunner({ installed: true });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    const result = await originalExec(command, args, options);
    if (args.at(-1) === 'status') {
      const status = JSON.parse(result.stdout);
      status.updateInProgress = true;
      status.pendingTransactionId = transactionId;
      status.updatePhase = 'database-backup';
      return { ...result, stdout: JSON.stringify(status) };
    }
    return result;
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedOwnership(manager);
  await manager.writeState({
    installedVersion: '1.2.0',
    installedRuntimeSha256: 'b'.repeat(64),
    installedRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    manifest: sourceManifest,
    pendingManifest: targetManifest,
    updateTransaction: {
      schemaVersion: 2,
      id: transactionId,
      mode: 'update',
      fromVersion: '1.2.0',
      toVersion: '1.2.1',
      fromRuntimeSha256: 'b'.repeat(64),
      toRuntimeSha256: 'c'.repeat(64),
      fromRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
      fromManifestSha256: manifestDigest(sourceManifest),
      toManifestSha256: manifestDigest(targetManifest),
      startedAt: '2026-01-01T00:00:00.000Z',
    },
  });

  const result = await manager.probe({ throwOnError: true });

  assert.equal(result.ok, true);
  const state = await manager.readState();
  assert.equal(state.updateTransaction.id, transactionId);
  assert.ok(state.pendingManifest);
});

test('probe clears a rolled-back host transaction only from a matching durable supervisor receipt', async () => {
  const transactionId = '2'.repeat(64);
  const sourceManifest = createManifest();
  const targetManifest = createManifest({
    version: '1.2.1',
    runtime: { ...createManifest().runtime, sha256: 'c'.repeat(64) },
  });
  const runner = healthyRunner({ installed: true });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    const result = await originalExec(command, args, options);
    if (args.at(-1) === 'status') {
      const status = JSON.parse(result.stdout);
      status.lastUpdateTransactionId = transactionId;
      status.lastUpdateOutcome = 'rolled_back';
      return { ...result, stdout: JSON.stringify(status) };
    }
    return result;
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedOwnership(manager);
  await manager.writeState({
    installedVersion: sourceManifest.version,
    installedRuntimeSha256: sourceManifest.runtime.sha256,
    installedRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    manifest: sourceManifest,
    pendingManifest: targetManifest,
    updateTransaction: {
      schemaVersion: 2,
      id: transactionId,
      mode: 'update',
      fromVersion: sourceManifest.version,
      toVersion: targetManifest.version,
      fromRuntimeSha256: sourceManifest.runtime.sha256,
      toRuntimeSha256: targetManifest.runtime.sha256,
      fromRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
      fromManifestSha256: manifestDigest(sourceManifest),
      toManifestSha256: manifestDigest(targetManifest),
      startedAt: '2026-01-01T00:00:00.000Z',
    },
  });

  const result = await manager.probe({ throwOnError: true });

  assert.equal(result.ok, true);
  const state = await manager.readState();
  assert.equal(state.manifest.runtime.sha256, sourceManifest.runtime.sha256);
  assert.equal(state.updateTransaction, null);
  assert.equal(state.pendingManifest, null);
});

test('a stale reconciliation receipt cannot clear a newer update transaction', async () => {
  const firstTransactionId = '2'.repeat(64);
  const secondTransactionId = '3'.repeat(64);
  const sourceManifest = createManifest();
  const firstTarget = createManifest({
    version: '1.2.1',
    runtime: { ...createManifest().runtime, sha256: 'c'.repeat(64) },
  });
  const secondTarget = createManifest({
    version: '1.2.3',
    runtime: { ...createManifest().runtime, sha256: 'd'.repeat(64) },
  });
  const manager = new WslManager(baseOptions());
  await manager.writeState({
    installedVersion: sourceManifest.version,
    installedRuntimeSha256: sourceManifest.runtime.sha256,
    installedRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    manifest: sourceManifest,
    pendingManifest: firstTarget,
    updateTransaction: {
      schemaVersion: 2,
      id: firstTransactionId,
      mode: 'update',
      fromVersion: sourceManifest.version,
      toVersion: firstTarget.version,
      fromRuntimeSha256: sourceManifest.runtime.sha256,
      toRuntimeSha256: firstTarget.runtime.sha256,
      fromRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
      fromManifestSha256: manifestDigest(sourceManifest),
      toManifestSha256: manifestDigest(firstTarget),
      startedAt: '2026-01-01T00:00:00.000Z',
    },
  });

  const originalWriteState = manager.writeState.bind(manager);
  let injected = false;
  manager.writeState = async (update, options) => {
    if (!injected && update.updateTransaction === null) {
      injected = true;
      await originalWriteState({
        pendingManifest: secondTarget,
        updateTransaction: {
          schemaVersion: 2,
          id: secondTransactionId,
          mode: 'update',
          fromVersion: firstTarget.version,
          toVersion: secondTarget.version,
          fromRuntimeSha256: firstTarget.runtime.sha256,
          toRuntimeSha256: secondTarget.runtime.sha256,
          fromRuntimeTransactionId: firstTransactionId,
          fromManifestSha256: manifestDigest(firstTarget),
          toManifestSha256: manifestDigest(secondTarget),
          startedAt: '2026-01-01T00:01:00.000Z',
        },
      });
    }
    return originalWriteState(update, options);
  };

  await assert.rejects(
    manager.reconcileUpdateState({
      service: 'applemusic-downloader',
      runtime: 'wsl-appliance',
      version: firstTarget.version,
      runtimeSha256: firstTarget.runtime.sha256,
      runtimeTransactionId: firstTransactionId,
      updateInProgress: false,
      lastUpdateTransactionId: firstTransactionId,
      lastUpdateOutcome: 'committed',
    }),
    (error) => error instanceof WslManagerError && error.code === 'state_conflict',
  );
  const state = await manager.readState();
  assert.equal(state.updateTransaction.id, secondTransactionId);
  assert.equal(state.pendingManifest.runtime.sha256, secondTarget.runtime.sha256);
});

test('matching rollback receipt cannot clear a host transaction with tampered manifest binding', async () => {
  const transactionId = '2'.repeat(64);
  const sourceManifest = createManifest();
  const targetManifest = createManifest({
    version: '1.2.1',
    runtime: { ...createManifest().runtime, sha256: 'c'.repeat(64) },
  });
  const runner = healthyRunner({ installed: true });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    const result = await originalExec(command, args, options);
    if (args.at(-1) === 'status') {
      const status = JSON.parse(result.stdout);
      status.lastUpdateTransactionId = transactionId;
      status.lastUpdateOutcome = 'rolled_back';
      return { ...result, stdout: JSON.stringify(status) };
    }
    return result;
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedOwnership(manager);
  await manager.writeState({
    installedVersion: sourceManifest.version,
    installedRuntimeSha256: sourceManifest.runtime.sha256,
    installedRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
    manifest: sourceManifest,
    pendingManifest: targetManifest,
    updateTransaction: {
      schemaVersion: 2,
      id: transactionId,
      mode: 'update',
      fromVersion: sourceManifest.version,
      toVersion: targetManifest.version,
      fromRuntimeSha256: sourceManifest.runtime.sha256,
      toRuntimeSha256: targetManifest.runtime.sha256,
      fromRuntimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
      fromManifestSha256: 'f'.repeat(64),
      toManifestSha256: manifestDigest(targetManifest),
      startedAt: '2026-01-01T00:00:00.000Z',
    },
  });

  const result = await manager.probe();

  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'distro_corrupt');
  assert.match(result.error.details.reason, /manifest\/runtime bindings/);
  assert.equal((await manager.readState()).updateTransaction.id, transactionId);
});

test('probe returns stable Chinese remediation for disabled virtualization', async () => {
  const manager = new WslManager(baseOptions({
    runner: healthyRunner(),
    systemInfo: async () => ({ virtualizationEnabled: false, rebootRequired: false }),
  }));
  const result = await manager.probe();
  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'virtualization_disabled');
  assert.match(result.error.message, /虚拟化/);
  assert.match(result.error.action, /BIOS\/UEFI/);
});

test('credentials use credential-stdin and never appear in argv or returned status', async () => {
  const calls = [];
  const sentinel = 'SENTINEL-password-do-not-leak';
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async () => HEALTH,
    processEnvironment: {
      PATH: 'C:\\Windows\\System32',
      APPLE_PASSWORD: sentinel,
      MEDIA_USER_TOKEN: sentinel,
      AMDl_Some_Secret: sentinel,
    },
  }));
  await seedOwnership(manager);

  const result = await manager.start({ appleEmail: 'person@example.test', applePassword: sentinel, storefront: 'us' });
  assert.equal(result.state, 'running');
  assert.equal(result.port, 54321);
  assert.equal(result.downloadDir, manager.downloadDir);
  const credentialCall = calls.find((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin');
  assert.ok(credentialCall);
  assert.equal(credentialCall.args.join(' ').includes(sentinel), false);
  assert.equal(credentialCall.input.toString('utf8').includes(sentinel), true);
  assert.equal(credentialCall.options.secrets.includes(sentinel), true);
  assert.deepEqual(JSON.parse(credentialCall.input.toString('utf8')), {
    schemaVersion: 1,
    credentials: { appleEmail: 'person@example.test', applePassword: sentinel, storefront: 'us' },
  });
  assert.deepEqual(credentialCall.options.env, { PATH: 'C:\\Windows\\System32', WSL_UTF8: '1' });
  assert.equal(JSON.stringify(calls.map((call) => call.args)).includes(sentinel), false);
  const stopIndex = calls.findIndex((call) => call.args.at(-1) === 'stop');
  const prepareIndex = calls.findIndex((call, index) => index > stopIndex
    && call.type === 'input'
    && call.args.at(-1) === 'network-prepare-stdin');
  const preflightIndex = calls.findIndex((call, index) => index > prepareIndex
    && call.type === 'exec'
    && call.args.at(-1) === 'network-preflight');
  const credentialIndex = calls.indexOf(credentialCall);
  const restartIndex = calls.findIndex((call, index) => index > credentialIndex && call.args.at(-1) === 'start');
  assert.equal(stopIndex >= 0, true);
  assert.equal(prepareIndex > stopIndex, true);
  assert.equal(preflightIndex > prepareIndex, true);
  assert.equal(credentialIndex > preflightIndex, true);
  assert.equal(restartIndex > credentialIndex, true);
});

test('invalid or oversized credentials are rejected before a healthy backend or network generation is touched', async (t) => {
  const scenarios = [
    {
      name: 'invalid credential field type',
      credentials: { applePassword: 42 },
    },
    {
      name: 'credential document exceeds the stdin limit',
      credentials: {
        applePassword: 'p'.repeat(4096),
        mediaUserToken: 'm'.repeat(32768),
        authorizationToken: 'a'.repeat(32768),
      },
    },
  ];

  for (const scenario of scenarios) {
    await t.test(scenario.name, async () => {
      const calls = [];
      const manager = new WslManager(baseOptions({
        runner: healthyRunner({ installed: true, calls }),
        request: async () => HEALTH,
      }));
      await seedOwnership(manager);

      await assert.rejects(
        manager.start(scenario.credentials),
        (error) => error instanceof WslManagerError && error.code === 'credential_delivery_failed',
      );

      assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), false);
      assert.equal(calls.some((call) => call.args.at(-1) === 'network-prepare-stdin'), false);
      assert.equal(calls.some((call) => call.args.at(-1) === 'network-preflight'), false);
      assert.equal(calls.some((call) => call.args.at(-1) === 'credential-stdin'), false);
      assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
    });
  }
});

test('fixed helper identity and same-stack preflight complete before credentials or backend start', async () => {
  const calls = [];
  let credentialProviderCalls = 0;
  const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped' });
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    fileSha256: async (filename) => {
      calls.push({ type: 'hash', filename });
      return WINDOWS_HELPER_SHA256;
    },
  }));
  await seedOwnership(manager);

  await manager.start(undefined, {
    credentialProvider: async () => {
      credentialProviderCalls += 1;
      calls.push({ type: 'credential-provider' });
      return { appleEmail: 'person@example.test', applePassword: 'network-order-sentinel' };
    },
  });

  const canonicalizations = calls.filter((call) => call.type === 'exec'
    && call.command === WINDOWS_HELPER_PATH
    && call.args[0] === 'canonicalize-file');
  const hash = calls.find((call) => call.type === 'hash');
  const wslpath = calls.find((call) => call.type === 'exec'
    && call.command === TRUSTED_WSL
    && call.args.includes('/usr/bin/wslpath')
    && call.args.at(-1) === WINDOWS_HELPER_PATH);
  const prepare = calls.find((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin');
  const preflight = calls.find((call) => call.type === 'exec' && call.args.at(-1) === 'network-preflight');
  const provider = calls.find((call) => call.type === 'credential-provider');
  const credential = calls.find((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin');
  const backendStart = calls.find((call) => call.type === 'exec' && call.args.at(-1) === 'start');
  assert.equal(canonicalizations.length, 2);
  assert.deepEqual(canonicalizations.map((call) => call.args), [
    ['canonicalize-file', WINDOWS_HELPER_PATH],
    ['canonicalize-file', WINDOWS_HELPER_PATH],
  ]);
  assert.equal(hash, undefined);
  assert.deepEqual(wslpath.args, [
    '--distribution', 'AppleMusicDownloader',
    '--exec', '/usr/bin/wslpath', '-a', '-u', '--', WINDOWS_HELPER_PATH,
  ]);
  assert.ok(prepare);
  assert.ok(preflight);
  assert.ok(provider);
  assert.ok(credential);
  assert.ok(backendStart);
  assert.equal(calls.indexOf(canonicalizations[0]) < calls.indexOf(canonicalizations[1]), true);
  assert.equal(calls.indexOf(canonicalizations[1]) < calls.indexOf(wslpath), true);
  assert.equal(calls.indexOf(wslpath) < calls.indexOf(prepare), true);
  assert.equal(calls.indexOf(prepare) < calls.indexOf(preflight), true);
  assert.equal(calls.indexOf(preflight) < calls.indexOf(provider), true);
  assert.equal(calls.indexOf(provider) < calls.indexOf(credential), true);
  assert.equal(calls.indexOf(credential) < calls.indexOf(backendStart), true);
  assert.equal(credentialProviderCalls, 1);
  assert.deepEqual(JSON.parse(prepare.input.toString('utf8')), {
    schemaVersion: 1,
    generationId: NETWORK_GENERATION,
    helperPath: WINDOWS_HELPER_WSL_PATH,
    helperSha256: WINDOWS_HELPER_SHA256,
  });
  assert.deepEqual(Object.keys(JSON.parse(prepare.input.toString('utf8'))).sort(), [
    'generationId', 'helperPath', 'helperSha256', 'schemaVersion',
  ]);
  assert.equal(prepare.input.includes(Buffer.from('network-order-sentinel')), false);
});

test('standard network preparation rejects a descriptor helper mismatch before helper execution', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls, initialServiceState: 'stopped' }),
    windowsHelperBindingProvider: () => ({
      file: WINDOWS_HELPER_FILE,
      sha256: '0'.repeat(64),
      size: 1,
    }),
  }));

  await assert.rejects(
    manager.prepareNetworkForStart(),
    (error) => error instanceof WslManagerError && error.code === 'network_preflight_failed',
  );
  assert.equal(calls.some((call) => call.command === WINDOWS_HELPER_PATH), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin'), false);
});

test('broker-only cleanup binds the stopped current generation and never invokes stop or terminate', async () => {
  const calls = [];
  const source = createManifest();
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({
      installed: true,
      calls,
      initialServiceState: 'stopped',
      initialNetworkState: 'prepared',
      runtimeVersion: source.version,
      runtimeSha256: source.runtime.sha256,
    }),
  }));
  await seedInstalledState(manager, source);

  const result = await manager.cleanupNetworkBridge({ expectedVersion: source.version });

  assert.equal(result.cleaned, true);
  assert.equal(result.status.state, 'stopped');
  assert.equal(result.status.network.state, 'unprepared');
  const cleanup = calls.find((call) => call.type === 'input' && call.args.at(-1) === 'network-cleanup-stdin');
  assert.ok(cleanup);
  assert.deepEqual(JSON.parse(cleanup.input.toString('utf8')), {
    schemaVersion: 1,
    generationId: NETWORK_GENERATION,
    version: source.version,
  });
  assert.deepEqual(Object.keys(JSON.parse(cleanup.input.toString('utf8'))).sort(), [
    'generationId', 'schemaVersion', 'version',
  ]);
  assert.equal(calls.some((call) => call.args?.at(-1) === 'stop'), false);
  assert.equal(calls.some((call) => call.args?.includes('--terminate')), false);
});

test('network preflight failure never resolves deferred credentials, invokes the egress helper, or starts the backend', async () => {
  const calls = [];
  let credentialProviderCalls = 0;
  const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped' });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'network-preflight') {
      calls.push({ type: 'exec', command, args: [...args], options });
      const error = new Error('fixed network preflight failure');
      error.stdout = JSON.stringify({
        ok: false,
        status: networkControlStatus(NETWORK_GENERATION, {
          state: 'failed',
          stage: 'dns',
          windowsPath: 'unreachable',
          proxyPropagated: false,
        }),
        error: {
          code: 'network_preflight_failed',
          message: 'WSL network preflight failed.',
          action: 'Retry the supported network path.',
        },
      });
      throw error;
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
  }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.start(undefined, {
      credentialProvider: async () => {
        credentialProviderCalls += 1;
        return { appleEmail: 'person@example.test', applePassword: 'preflight-secret-sentinel' };
      },
    }),
    (error) => error instanceof WslManagerError
      && error.code === 'network_preflight_failed'
      && error.details.stage === 'dns',
  );

  assert.equal(credentialProviderCalls, 0);
  assert.equal(calls.some((call) => call.command === WINDOWS_HELPER_PATH
    && call.args[0] === 'egress-connect-stdio'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'credential-stdin'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
  assert.equal(JSON.stringify(calls.map((call) => call.args)).includes('preflight-secret-sentinel'), false);
});

test('trusted broker failure stages cross the Electron boundary without arbitrary details', async (t) => {
  for (const stage of ['helper_identity', 'helper_launch', 'helper_protocol', 'proxy_resolution']) {
    await t.test(stage, async () => {
      const calls = [];
      const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped' });
      const originalExec = runner.exec;
      runner.exec = async (command, args, options) => {
        if (args.at(-1) === 'network-preflight') {
          calls.push({ type: 'exec', command, args: [...args], options });
          const error = new Error('trusted broker failure');
          error.stdout = JSON.stringify({
            ok: false,
            status: networkControlStatus(NETWORK_GENERATION, {
              state: 'failed',
              stage,
              windowsPath: 'unreachable',
              proxyPropagated: false,
            }),
            error: { code: 'network_preflight_failed', message: 'trusted broker failure' },
          });
          throw error;
        }
        return originalExec(command, args, options);
      };
      const manager = new WslManager(baseOptions({ runner }));

      await assert.rejects(
        manager.prepareNetworkForStart(),
        (error) => error instanceof WslManagerError
          && error.code === 'network_preflight_failed'
          && JSON.stringify(error.details) === JSON.stringify({ stage }),
      );
      assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin'), false);
    });
  }
});

test('a failure stage from a stale network generation is not attributed to the current attempt', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped' });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'network-preflight') {
      calls.push({ type: 'exec', command, args: [...args], options });
      const error = new Error('stale network preflight failure');
      error.stdout = JSON.stringify({
        ok: false,
        status: networkControlStatus('8'.repeat(64), {
          state: 'failed',
          stage: 'tls',
          windowsPath: 'unreachable',
          proxyPropagated: false,
        }),
        error: {
          code: 'network_preflight_failed',
          message: 'WSL network preflight failed.',
          action: 'Retry the supported network path.',
        },
      });
      throw error;
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({ runner, request: async () => HEALTH }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.start({ applePassword: 'stale-stage-secret' }),
    (error) => error instanceof WslManagerError
      && error.code === 'network_preflight_failed'
      && error.details.stage === undefined,
  );

  assert.equal(calls.some((call) => call.args.at(-1) === 'credential-stdin'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
});

test('a stale supervisor network generation is rejected before preflight or credential delivery', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped' });
  const originalInput = runner.input;
  runner.input = async (command, args, input, options) => {
    if (args.at(-1) === 'network-prepare-stdin') {
      calls.push({ type: 'input', command, args: [...args], input: Buffer.from(input), options });
      return {
        stdout: JSON.stringify(networkControlStatus('8'.repeat(64), {
          state: 'prepared',
          stage: 'prepared',
          windowsPath: 'unknown',
          proxyPropagated: false,
        })),
        stderr: '',
      };
    }
    return originalInput(command, args, input, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
  }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.start({ applePassword: 'stale-generation-secret' }),
    (error) => error instanceof WslManagerError && error.code === 'network_preflight_failed',
  );

  assert.equal(calls.some((call) => call.args.at(-1) === 'network-preflight'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'credential-stdin'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
});

test('legacy or incomplete network prepare schemas fail closed before preflight', async (t) => {
  const scenarios = [
    {
      name: 'legacy ready boolean',
      mutate(network) { network.ready = false; },
    },
    {
      name: 'missing WSL direct-path observation',
      mutate(network) { delete network.wslDirectPath; },
    },
    {
      name: 'prepared generation claims proxy propagation',
      mutate(network) { network.proxyPropagated = true; },
    },
  ];

  for (const scenario of scenarios) {
    await t.test(scenario.name, async () => {
      const calls = [];
      const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped' });
      const originalInput = runner.input;
      runner.input = async (command, args, input, options) => {
        if (args.at(-1) !== 'network-prepare-stdin') {
          return originalInput(command, args, input, options);
        }
        calls.push({ type: 'input', command, args: [...args], input: Buffer.from(input), options });
        const status = networkControlStatus(NETWORK_GENERATION, {
          state: 'prepared',
          stage: 'prepared',
          windowsPath: 'unknown',
          proxyPropagated: false,
        });
        scenario.mutate(status.network);
        return { stdout: JSON.stringify(status), stderr: '' };
      };
      const manager = new WslManager(baseOptions({ runner, request: async () => HEALTH }));
      await seedOwnership(manager);

      await assert.rejects(
        manager.start({ applePassword: 'network-schema-secret' }),
        (error) => error instanceof WslManagerError && error.code === 'network_preflight_failed',
      );

      assert.equal(calls.some((call) => call.args.at(-1) === 'network-preflight'), false);
      assert.equal(calls.some((call) => call.args.at(-1) === 'credential-stdin'), false);
      assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
    });
  }
});

test('helper identity, descriptor binding, and converted path changes fail closed before network preparation', async (t) => {
  const scenarios = [
    {
      name: 'object identity changes after hashing',
      mutateProbe(document, count) {
        if (count === 2) document.objectId = '11223344:5566778899aabbcc';
      },
    },
    {
      name: 'canonical path changes after hashing',
      mutateProbe(document, count) {
        if (count === 2) document.canonicalPath = 'C:\\Temp\\amdl-windows-probe.exe';
      },
    },
    {
      name: 'wslpath result escapes the Windows drive mount',
      convertedPath: '/tmp/amdl-windows-probe.exe',
    },
    {
      name: 'wslpath result contains a Windows separator',
      convertedPath: '/mnt/c/Program Files\\Apple Music Downloader\\amdl-windows-probe.exe',
    },
    {
      name: 'wslpath result contains a control character',
      convertedPath: '/mnt/c/Program Files/Apple Music Downloader/\tamdl-windows-probe.exe',
    },
  ];

  for (const scenario of scenarios) {
    await t.test(scenario.name, async () => {
      const calls = [];
      const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped' });
      const originalExec = runner.exec;
      let canonicalizationCount = 0;
      runner.exec = async (command, args, options) => {
        if (command === WINDOWS_HELPER_PATH && args[0] === 'canonicalize-file') {
          calls.push({ type: 'exec', command, args: [...args], options });
          canonicalizationCount += 1;
          const document = {
            version: 1,
            canonicalPath: WINDOWS_HELPER_PATH,
            fileSystem: 'NTFS',
            reparsePoint: false,
            shortPathAlias: false,
            objectId: INSTALL_OBJECT_ID,
            objectType: 'file',
          };
          scenario.mutateProbe?.(document, canonicalizationCount);
          return { stdout: JSON.stringify(document), stderr: '' };
        }
        if (scenario.convertedPath
            && args.includes('/usr/bin/wslpath')
            && args.at(-1) === WINDOWS_HELPER_PATH) {
          calls.push({ type: 'exec', command, args: [...args], options });
          return { stdout: scenario.convertedPath + '\n', stderr: '' };
        }
        return originalExec(command, args, options);
      };
      const manager = new WslManager(baseOptions({
        runner,
        request: async () => HEALTH,
      }));
      await seedOwnership(manager);

      await assert.rejects(
        manager.start({ applePassword: 'identity-hash-path-secret' }),
        (error) => error instanceof WslManagerError && error.code === 'network_preflight_failed',
      );

      assert.equal(calls.some((call) => call.args?.at(-1) === 'network-prepare-stdin'), false);
      assert.equal(calls.some((call) => call.args?.at(-1) === 'network-preflight'), false);
      assert.equal(calls.some((call) => call.args?.at(-1) === 'credential-stdin'), false);
      assert.equal(calls.some((call) => call.args?.at(-1) === 'start'), false);
      assert.equal(JSON.stringify(calls.map((call) => call.args)).includes('identity-hash-path-secret'), false);
    });
  }
});

test('credential delivery fails closed before credential-stdin when ownership is missing', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async () => HEALTH,
  }));

  await assert.rejects(
    manager.start({ appleEmail: 'person@example.test', applePassword: 'must-not-be-delivered' }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin'), false);
});

test('registered BasePath mismatch blocks probe and credentials before the named distro receives secrets', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === 'wsl-appliance-identity') {
      return {
        stdout: JSON.stringify({
          version: 1,
          distributionName: 'AppleMusicDownloader',
          basePath: 'C:\\Users\\attacker\\replaced-distro',
          wslVersion: 2,
        }),
        stderr: '',
      };
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({ runner, request: async () => HEALTH }));
  manager.registrationIdentityProvider = async () => ({
    version: 1,
    distributionName: 'AppleMusicDownloader',
    basePath: 'C:\\Users\\attacker\\replaced-distro',
    wslVersion: 2,
  });
  await seedInstalledState(manager);
  calls.length = 0;

  const probe = await manager.probe();
  assert.equal(probe.ok, false);
  assert.equal(probe.error.code, 'distro_name_conflict');
  await assert.rejects(
    manager.start({ applePassword: 'basepath-secret-sentinel' }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin'), false);
});

test('fresh rootfs import is verified and executed by the fixed Windows helper action', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: false, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === 'import-rootfs-locked') {
      calls.push({ type: 'exec', command, args: [...args], options });
      throw new Error('synthetic locked-import boundary');
    }
    if (command === TRUSTED_WSL && args[0] === '--import') {
      calls.push({ type: 'exec', command, args: [...args], options });
      throw new Error('unlocked wsl import must not be reachable');
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    downloadArtifact: async (_artifact, destination) => destination,
  }));

  await assert.rejects(manager.install(createManifest()), (error) => error instanceof WslManagerError && error.code === 'import_failed');
  const helperCall = calls.find((call) => call.command === manager.windowsProbeExe && call.args[0] === 'import-rootfs-locked');
  assert.ok(helperCall);
  assert.equal(helperCall.args[1], manager.installDir);
  assert.equal(helperCall.args[2], INSTALL_OBJECT_ID);
  assert.equal(helperCall.args[4], createManifest().rootfs.sha256);
  assert.equal(calls.some((call) => call.command === TRUSTED_WSL && call.args[0] === '--import'), false);
});

test('running backend changes download mapping only after a safe stop and restart', async () => {
  const calls = [];
  const objectId = '00112233:445566778899aabb';
  const previousDirectory = path.join(tempAppData(), 'Music', 'Apple Music Downloader');
  const selectedDirectory = path.resolve(__dirname, '..', '..', 'downloads', 'AM-DL downloads');
  const selectedDrive = path.win32.parse(selectedDirectory).root[0].toLowerCase();
  const selectedWslPath = `/mnt/${selectedDrive}/下载目录 中文/Apple Music Downloader`;
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  let installDir = '';
  runner.exec = async (command, args, options) => {
    if (args[0] === 'wsl-appliance-identity') {
      return { stdout: JSON.stringify({ version: 1, distributionName: 'AppleMusicDownloader', basePath: installDir, wslVersion: 2 }), stderr: '' };
    }
    if (args[0] === 'configure-download-locked') {
      calls.push({ type: 'exec', command, args: [...args], options });
      return {
        stdout: JSON.stringify({
          version: 1,
          canonicalPath: args[1],
          fileSystem: 'NTFS',
          reparsePoint: false,
          shortPathAlias: false,
          objectId,
          wslPath: selectedWslPath,
        }),
        stderr: '',
      };
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    downloadDir: previousDirectory,
    request: async () => HEALTH,
    pathCanonicalizer: async (value) => ({
      version: 1,
      canonicalPath: path.win32.normalize(value),
      fileSystem: 'NTFS',
      reparsePoint: false,
      shortPathAlias: false,
      objectId,
    }),
  }));
  installDir = manager.installDir;
  await seedInstalledState(manager);
  await seedOwnership(manager);
  calls.length = 0;

  const result = await manager.configureDownloadPath(selectedDirectory, { ensureBackendPort: false, expectedVersion: '1.2.0' });
  assert.deepEqual(result, {
    windowsPath: selectedDirectory,
    wslPath: selectedWslPath,
    serviceRestarted: true,
  });
  const stopIndex = calls.findIndex((call) => call.args.at(-1) === 'stop');
  const helperCall = calls.find((call) => call.command === manager.windowsProbeExe && call.args[0] === 'configure-download-locked');
  assert.ok(helperCall);
  assert.deepEqual(helperCall.args.slice(1), [path.win32.normalize(selectedDirectory), objectId, '8080']);
  const helperIndex = calls.indexOf(helperCall);
  const startIndex = calls.findIndex((call) => call.args.at(-1) === 'start');
  assert.equal(stopIndex >= 0 && stopIndex < helperIndex && helperIndex < startIndex, true);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'configure-stdin'), false);
  assert.equal(calls.some((call) => call.command === TRUSTED_WSL && call.args.includes('/usr/bin/wslpath')), true);
});

test('running backend refuses download mapping change when supervisor reports active jobs', async () => {
  const calls = [];
  const objectId = '00112233:445566778899aabb';
  const previousDirectory = path.join(tempAppData(), 'Music', 'Apple Music Downloader');
  const selectedDirectory = path.resolve(__dirname, '..', '..', 'downloads', 'AM-DL active-job');
  const runner = healthyRunner({ installed: true, calls });
  const healthyExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'stop') {
      calls.push({ type: 'exec', command, args: [...args], options });
      const response = JSON.stringify({
        ok: false,
        status: {
          service: 'applemusic-downloader',
          runtime: 'wsl-appliance',
          version: '1.2.0',
          state: 'running',
        },
        error: {
          code: 'active_jobs',
          message: '仍有排队或运行中的任务，不能停止服务。',
          action: '请等待任务完成或取消任务后重试。',
        },
      });
      const error = new Error(response);
      error.stdout = response;
      throw error;
    }
    return healthyExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    downloadDir: previousDirectory,
  }));
  await seedInstalledState(manager);
  await seedOwnership(manager);

  await assert.rejects(
    manager.configureDownloadPath(selectedDirectory, { ensureBackendPort: false, expectedVersion: '1.2.0' }),
    (error) => error instanceof WslManagerError && error.code === 'path_change_active_jobs',
  );
  assert.equal(calls.some((call) => call.command === manager.windowsProbeExe && call.args[0] === 'configure-download-locked'), false);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal((await manager.readState()).downloadDir, undefined);
  assert.equal(manager.downloadDir, previousDirectory);
});

test('a failed running-backend path change rolls back the old mapping and restarts without resolving credentials', async () => {
  const calls = [];
  const objectId = '00112233:445566778899aabb';
  const previousDirectory = path.join(tempAppData(), 'Music', 'Apple Music Downloader');
  const selectedDirectory = path.resolve(__dirname, '..', '..', 'downloads', 'AM-DL rollback');
  const previousWslPath = '/mnt/c/Users/测试 用户/Music/Apple Music Downloader';
  const runner = healthyRunner({ installed: true, calls });
  const healthyExec = runner.exec;
  let credentialReads = 0;
  let installDir = '';
  runner.exec = async (command, args, options) => {
    if (args[0] === 'wsl-appliance-identity') {
      return { stdout: JSON.stringify({ version: 1, distributionName: 'AppleMusicDownloader', basePath: installDir, wslVersion: 2 }), stderr: '' };
    }
    if (args[0] === 'configure-download-locked') {
      calls.push({ type: 'exec', command, args: [...args], options });
      if (path.win32.normalize(args[1]).toLowerCase() === path.win32.normalize(selectedDirectory).toLowerCase()) {
        throw new Error('target helper failed');
      }
      return {
        stdout: JSON.stringify({
          version: 1,
          canonicalPath: args[1],
          fileSystem: 'NTFS',
          reparsePoint: false,
          shortPathAlias: false,
          objectId: args[2],
          wslPath: previousWslPath,
        }),
        stderr: '',
      };
    }
    return healthyExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    downloadDir: previousDirectory,
    credentialProvider: async () => { credentialReads += 1; return { appleEmail: 'not-used@example.test' }; },
    pathCanonicalizer: async (value) => ({
      version: 1,
      canonicalPath: path.win32.normalize(value),
      fileSystem: 'NTFS',
      reparsePoint: false,
      shortPathAlias: false,
      objectId,
    }),
  }));
  installDir = manager.installDir;
  await seedInstalledState(manager);
  await seedOwnership(manager);

  await assert.rejects(
    manager.configureDownloadPath(selectedDirectory, { ensureBackendPort: false, expectedVersion: '1.2.0' }),
    (error) => error instanceof WslManagerError && error.code === 'path_change_failed_recovered',
  );
  assert.equal(manager.downloadDir, previousDirectory);
  const state = await manager.readState();
  assert.equal(state.downloadDir, previousDirectory);
  assert.equal(state.downloadWslPath, previousWslPath);
  assert.equal(state.downloadPathTransaction, null);
  assert.equal(credentialReads, 0);
  const pathCalls = calls.filter((call) => call.command === manager.windowsProbeExe && call.args[0] === 'configure-download-locked');
  assert.equal(pathCalls.length, 2);
  assert.equal(path.win32.normalize(pathCalls[0].args[1]).toLowerCase(), path.win32.normalize(selectedDirectory).toLowerCase());
  assert.equal(path.win32.normalize(pathCalls[1].args[1]).toLowerCase(), path.win32.normalize(previousDirectory).toLowerCase());
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), true);
});

test('a path-change rollback failure leaves a durable recovery marker and a stopped service', async () => {
  const calls = [];
  const previousDirectory = path.join(tempAppData(), 'Music', 'Apple Music Downloader');
  const selectedDirectory = path.resolve(__dirname, '..', '..', 'downloads', 'AM-DL unrecoverable');
  const runner = healthyRunner({ installed: true, calls });
  const healthyExec = runner.exec;
  const manager = new WslManager(baseOptions({
    runner,
    downloadDir: previousDirectory,
    pathCanonicalizer: async (value) => ({
      version: 1,
      canonicalPath: path.win32.normalize(value),
      fileSystem: 'NTFS',
      reparsePoint: false,
      shortPathAlias: false,
      objectId: INSTALL_OBJECT_ID,
    }),
  }));
  const originalInstallDir = manager.installDir;
  runner.exec = async (command, args, options) => {
    if (args[0] === 'configure-download-locked') throw new Error('helper unavailable');
    return healthyExec(command, args, options);
  };
  await seedInstalledState(manager);
  await seedOwnership(manager);

  await assert.rejects(
    manager.configureDownloadPath(selectedDirectory, { ensureBackendPort: false, expectedVersion: '1.2.0' }),
    (error) => error instanceof WslManagerError && error.code === 'path_change_recovery_failed',
  );
  const state = await manager.readState();
  assert.equal(state.downloadDir, undefined);
  assert.equal(state.downloadPathTransaction.phase, 'rollback_required');
  assert.equal(manager.downloadDir, previousDirectory);
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
  assert.equal(originalInstallDir, manager.installDir);
});

test('same-path configuration is idempotent and does not invoke an unavailable helper', async () => {
  const calls = [];
  const selectedDirectory = path.join(os.tmpdir(), 'AMDL same-path helper failure');
  const runner = healthyRunner({ installed: true, initialServiceState: 'stopped', calls });
  const healthyExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (command === WINDOWS_HELPER_PATH && args[0] === 'configure-download-locked') {
      calls.push({ type: 'exec', command, args: [...args], options });
      throw new Error('helper unavailable');
    }
    return healthyExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    downloadDir: selectedDirectory,
    pathCanonicalizer: async (value) => ({
      version: 1,
      canonicalPath: path.win32.normalize(value),
      fileSystem: 'NTFS',
      reparsePoint: false,
      shortPathAlias: false,
      objectId: INSTALL_OBJECT_ID,
    }),
  }));
  await seedInstalledState(manager);
  await seedOwnership(manager);
  await manager.writeState({
    downloadDir: selectedDirectory,
    downloadWslPath: '/mnt/i/音乐',
  });

  const result = await manager.configureDownloadPath(selectedDirectory, {
    ensureBackendPort: false,
    expectedVersion: '1.2.0',
  });
  assert.deepEqual(result, {
    windowsPath: path.win32.normalize(selectedDirectory),
    wslPath: '/mnt/i/音乐',
    serviceRestarted: false,
  });
  assert.equal(calls.some((call) => call.command === WINDOWS_HELPER_PATH
    && call.args[0] === 'configure-download-locked'), false);
  const state = await manager.readState();
  assert.equal(state.downloadPathTransaction, null);
});

test('download configuration re-proves ownership immediately before the locked helper action', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  let ownershipReads = 0;
  runner.exec = async (command, args, options) => {
    if (args.includes('/usr/bin/cat') && args.includes('/var/lib/applemusic-downloader/desktop-owner-id')) {
      ownershipReads += 1;
      if (ownershipReads >= 2) {
        calls.push({ type: 'exec', command, args: [...args], options });
        return { stdout: 'f'.repeat(64), stderr: '' };
      }
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedInstalledState(manager);
  calls.length = 0;
  ownershipReads = 0;

  await assert.rejects(
    manager.configureDownloadPath(manager.downloadDir, { ensureBackendPort: false, expectedVersion: '1.2.0' }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(calls.some((call) => call.args[0] === 'configure-download-locked'), false);
});

test('start establishes a detached secret-free keeper with fixed argv before control operations', async () => {
  const calls = [];
  const keeperCalls = [];
  let unrefCalls = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async () => HEALTH,
    processEnvironment: { PATH: 'C:\\Windows\\System32', APPLE_PASSWORD: 'keeper-secret-sentinel' },
    keeperLauncher: (command, args, options) => {
      keeperCalls.push({ command, args: [...args], options });
      return { exitCode: null, killed: false, once() {}, unref() { unrefCalls += 1; } };
    },
  }));
  await seedOwnership(manager);

  await manager.start();

  assert.equal(keeperCalls.length, 1);
  assert.equal(keeperCalls[0].command, TRUSTED_WSL);
  assert.deepEqual(keeperCalls[0].args, [
    '--distribution', 'AppleMusicDownloader', '--exec', KEEPALIVE_PATH,
  ]);
  assert.equal(keeperCalls[0].options.shell, false);
  assert.equal(keeperCalls[0].options.detached, true);
  assert.equal(keeperCalls[0].options.stdio, 'ignore');
  assert.deepEqual(keeperCalls[0].options.env, { PATH: 'C:\\Windows\\System32', WSL_UTF8: '1' });
  assert.equal(JSON.stringify(keeperCalls).includes('keeper-secret-sentinel'), false);
  assert.equal(unrefCalls, 1);
  const probeIndex = calls.findIndex((call) => call.args.includes(KEEPALIVE_PATH) && call.args.includes('-x'));
  const statusIndex = calls.findIndex((call) => call.args.at(-1) === 'status');
  assert.equal(probeIndex >= 0 && probeIndex < statusIndex, true);
});

test('an old runtime without the keepalive helper fails with repairable distro_corrupt before start', async () => {
  const calls = [];
  let keeperLaunches = 0;
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.includes(KEEPALIVE_PATH) && args.includes('-x')) throw new Error('test: helper missing');
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    keeperLauncher: () => { keeperLaunches += 1; return { exitCode: null, killed: false, once() {}, unref() {} }; },
  }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.start(),
    (error) => error instanceof WslManagerError
      && error.code === 'distro_corrupt'
      && /force-update the runtime/.test(error.details.reason),
  );
  assert.equal(keeperLaunches, 0);
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
});

test('credential restart is deferred when a queued or running job exists', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [{ id: 'queued-1', status: 'queued' }] } }
      : HEALTH,
  }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.start({ mediaUserToken: 'deferred-token-sentinel' }),
    (error) => error instanceof WslManagerError && error.code === 'active_jobs' && error.details.credentialDeliveryDeferred === true,
  );
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'network-preflight'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
});

test('credential restart quiesces the old proxy before starting a new generation', async () => {
  const calls = [];
  const proxyEvents = [];
  let proxyGeneration = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [] } }
      : HEALTH,
    proxyFactory: () => {
      const generation = ++proxyGeneration;
      return {
        start: async () => { proxyEvents.push(`start:${generation}`); return 54340 + generation; },
        stop: async () => { proxyEvents.push(`stop:${generation}`); },
        diagnostics: () => '',
      };
    },
  }));
  await seedOwnership(manager);

  await manager.start({ mediaUserToken: 'restart-token-sentinel' });
  assert.equal(proxyGeneration, 2);
  assert.deepEqual(proxyEvents, ['start:1', 'stop:1', 'start:2']);
  const stopIndex = calls.findIndex((call) => call.args.at(-1) === 'stop');
  const secondStartIndex = calls.findIndex((call, index) => index > stopIndex && call.args.at(-1) === 'start');
  assert.equal(stopIndex >= 0, true);
  assert.equal(secondStartIndex > stopIndex, true);
  assert.equal(manager.serviceBaseUrl, 'http://127.0.0.1:54342');
});

test('activeJobs uses the server-side active page so terminal history cannot hide work', async () => {
  const requests = [];
  const manager = new WslManager(baseOptions({
    request: async (url) => {
      requests.push(url);
      if (url.endsWith('/api/v1/jobs?active=true&limit=100')) {
        return { statusCode: 200, payload: { jobs: [{ id: 'queued-behind-history', status: 'queued' }] } };
      }
      return {
        statusCode: 200,
        payload: { jobs: Array.from({ length: 50 }, (_, index) => ({ id: `done-${index}`, status: 'succeeded' })) },
      };
    },
  }));
  manager.proxy = { stop: async () => {} };
  manager.proxyLocalPort = 43190;
  manager.proxyBackendPort = manager.backendPort;
  manager.serviceBaseUrl = 'http://127.0.0.1:43190';

  const jobs = await manager.activeJobs();

  assert.deepEqual(jobs.map((job) => job.id), ['queued-behind-history']);
  assert.deepEqual(requests, ['http://127.0.0.1:43190/api/v1/jobs?active=true&limit=100']);
});

test('environment sanitizer removes secrets, ambient proxies, and WSLInterop state case-insensitively', () => {
  assert.deepEqual(sanitizeProcessEnvironment({
    Path: 'C:\\Windows',
    wsl_utf8: '0',
    Wsl_Utf8: 'legacy',
    apple_email: 'mail-secret',
    APPLE_PASSWORD: 'password-secret',
    media_user_token: 'media-secret',
    AMDL_LOGIN_TOKEN: 'login-secret',
    HTTPS_PROXY: 'http://proxy.example.test:8080',
    all_proxy: 'socks5://proxy.example.test:1080',
    WSL_INTEROP: '/run/WSL/123_interop',
    wslenv: 'PATH/u',
  }), { Path: 'C:\\Windows', WSL_UTF8: '1' });
});

test('install imports WSL2, preserves non-ASCII path as one argument and configures through stdin', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: false, calls }),
    request: async () => HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  const manifest = createManifest();

  const result = await manager.install(manifest);
  assert.equal(result.alreadyInstalled, false);
  const importCall = calls.find((call) => call.command === manager.windowsProbeExe && call.args[0] === 'import-rootfs-locked');
  assert.equal(importCall.args[1], manager.installDir);
  assert.equal(importCall.args[2], INSTALL_OBJECT_ID);
  assert.equal(importCall.args[4], manifest.rootfs.sha256);
  const configure = calls.find((call) => call.command === manager.windowsProbeExe && call.args[0] === 'configure-download-locked');
  assert.equal(configure.args[1], manager.downloadDir);
  assert.equal(configure.args[3], '8080');
  assert.equal(calls.some((call) => call.command === TRUSTED_WSL && call.args.includes('/usr/bin/wslpath')), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'configure-stdin'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
  const importIndex = calls.indexOf(importCall);
  const supervisorStatusIndex = calls.findIndex((call, index) => index > importIndex && call.args.at(-1) === 'status');
  const configureIndex = calls.indexOf(configure);
  assert.equal(importIndex < supervisorStatusIndex && supervisorStatusIndex < configureIndex, true);
});

test('install persists a nonce-bound transaction before import and mirrors ownership through stdin', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: false, calls });
  let manager;
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === 'import-rootfs-locked') {
      const state = JSON.parse(fs.readFileSync(manager.statePath, 'utf8'));
      assert.equal(state.installTransaction.phase, 'prepared');
      assert.match(state.installTransaction.id, /^[a-f0-9]{64}$/);
      assert.equal(state.installTransaction.ownershipId, fs.readFileSync(manager.ownershipPath, 'utf8'));
    }
    return originalExec(command, args, options);
  };
  manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await manager.install(createManifest());

  const markerWrite = calls.find((call) => call.type === 'input' && call.args.includes('/usr/bin/dd'));
  assert.ok(markerWrite);
  assert.equal(markerWrite.args.join(' ').includes(markerWrite.input.toString('ascii')), false);
  assert.equal(markerWrite.input.toString('ascii'), fs.readFileSync(manager.ownershipPath, 'utf8'));
  assert.equal((await manager.readState()).installTransaction, null);
});

test('install atomically rebinds a valid completed orphan before import', async () => {
  const calls = [];
  const manifest = createManifest();
  const runner = healthyRunner({ installed: false, calls });
  const originalExec = runner.exec;
  let preparedState = null;
  let manager;
  runner.exec = async (command, args, options) => {
    if (args[0] === 'import-rootfs-locked') {
      preparedState = JSON.parse(fs.readFileSync(manager.statePath, 'utf8'));
    }
    return originalExec(command, args, options);
  };
  manager = new WslManager(baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager, manifest);
  await manager.writeState({
    backendPort: 43199,
    localPort: 43200,
    usesLoopbackProxy: true,
    pendingManifest: manifest,
    updateTransaction: { id: 'f'.repeat(64), phase: 'prepared' },
  });
  const ownerBefore = fs.readFileSync(manager.ownershipPath, 'utf8');

  const result = await manager.install(manifest);

  assert.equal(result.alreadyInstalled, false);
  assert.ok(preparedState);
  assert.equal(preparedState.installTransaction.phase, 'prepared');
  assert.equal(preparedState.installTransaction.ownershipId, ownerBefore);
  assert.equal(preparedState.ownership.ownerId, ownerBefore);
  assert.equal(preparedState.manifest, null);
  assert.equal(preparedState.installedVersion, null);
  assert.equal(preparedState.installedRuntimeSha256, null);
  assert.equal(preparedState.installedRuntimeTransactionId, null);
  assert.equal(preparedState.updateTransaction, null);
  assert.equal(preparedState.pendingManifest, null);
  assert.equal(preparedState.backendPort, null);
  assert.equal(preparedState.localPort, null);
  assert.equal(preparedState.usesLoopbackProxy, false);
  assert.equal(fs.readFileSync(manager.ownershipPath, 'utf8'), ownerBefore);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('repair reimports a safely orphaned appliance without update or unregister', async () => {
  const calls = [];
  const manifest = createManifest();
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: false, calls }),
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager, manifest);

  const result = await manager.repair(manifest);

  assert.equal(result.alreadyInstalled, false);
  assert.equal(calls.filter((call) => call.args[0] === 'import-rootfs-locked').length, 1);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('an unregistered orphan with any remaining install directory fails closed', async () => {
  const calls = [];
  let downloads = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: false, calls }),
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
    downloadArtifact: async (_artifact, destination) => { downloads += 1; return destination; },
  }));
  await seedInstalledState(manager);
  fs.mkdirSync(manager.installDir, { recursive: true });
  const sentinel = path.join(manager.installDir, 'ext4.vhdx');
  fs.writeFileSync(sentinel, 'must-survive');
  const stateBefore = fs.readFileSync(manager.statePath);
  const ownerBefore = fs.readFileSync(manager.ownershipPath);

  await assert.rejects(
    manager.install(createManifest()),
    (error) => error instanceof WslManagerError && error.code === 'orphaned_install_state',
  );

  assert.equal(downloads, 0);
  assert.equal(fs.readFileSync(sentinel, 'utf8'), 'must-survive');
  assert.deepEqual(fs.readFileSync(manager.statePath), stateBefore);
  assert.deepEqual(fs.readFileSync(manager.ownershipPath), ownerBefore);
  assert.equal(calls.some((call) => call.args[0] === 'import-rootfs-locked'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('an install directory appearing after orphan proof is preserved and blocks import', async () => {
  const calls = [];
  const manifest = createManifest();
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: false, calls }),
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager, manifest);
  const originalPrepare = manager.prepareInstallTransaction.bind(manager);
  const sentinel = path.join(manager.installDir, 'late-arrival.vhdx');
  manager.prepareInstallTransaction = async (...args) => {
    const transaction = await originalPrepare(...args);
    fs.mkdirSync(manager.installDir, { recursive: true });
    fs.writeFileSync(sentinel, 'must-survive');
    return transaction;
  };

  await assert.rejects(
    manager.install(manifest),
    (error) => error instanceof WslManagerError && error.code === 'orphaned_install_state',
  );

  assert.equal(fs.readFileSync(sentinel, 'utf8'), 'must-survive');
  assert.equal((await manager.readState()).installTransaction.phase, 'prepared');
  assert.equal(calls.some((call) => call.args[0] === 'import-rootfs-locked'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('CLI and registry disagreement blocks orphan recovery before cache or state mutation', async () => {
  const calls = [];
  let downloads = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: false, calls }),
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: true,
      matchCount: 1,
      basePath: 'C:\\Users\\attacker\\replacement-distro',
      wslVersion: 2,
    }),
    downloadArtifact: async (_artifact, destination) => { downloads += 1; return destination; },
  }));
  await seedInstalledState(manager);
  const stateBefore = fs.readFileSync(manager.statePath);
  const ownerBefore = fs.readFileSync(manager.ownershipPath);

  await assert.rejects(
    manager.install(createManifest()),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );

  assert.equal(downloads, 0);
  assert.deepEqual(fs.readFileSync(manager.statePath), stateBefore);
  assert.deepEqual(fs.readFileSync(manager.ownershipPath), ownerBefore);
  assert.equal(calls.some((call) => call.args[0] === 'import-rootfs-locked'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('pre-registration import failure preserves its nonce-bound retry transaction', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: false, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === 'import-rootfs-locked') throw new Error('synthetic pre-registration import failure');
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    downloadArtifact: async (_artifact, destination) => destination,
  }));

  await assert.rejects(manager.install(createManifest()), (error) => error.code === 'import_failed');

  const ownerId = fs.readFileSync(manager.ownershipPath, 'utf8');
  const state = await manager.readState();
  assert.equal(state.ownership.ownerId, ownerId);
  assert.equal(state.installTransaction.ownershipId, ownerId);
  assert.equal(state.installTransaction.phase, 'failed');
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('orphan recovery CAS preserves a concurrent state update and refuses import', async () => {
  const calls = [];
  const manifest = createManifest();
  let downloads = 0;
  let presenceCalls = 0;
  let manager;
  manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: false, calls }),
    registrationPresenceProvider: async () => {
      presenceCalls += 1;
      if (presenceCalls === 2) await manager.writeState({ backendPort: 44001 });
      return {
        version: 1,
        distributionName: 'AppleMusicDownloader',
        registered: false,
        matchCount: 0,
      };
    },
    downloadArtifact: async (_artifact, destination) => { downloads += 1; return destination; },
  }));
  await seedInstalledState(manager, manifest);
  const ownerBefore = fs.readFileSync(manager.ownershipPath, 'utf8');

  await assert.rejects(
    manager.install(manifest),
    (error) => error instanceof WslManagerError && error.code === 'state_conflict',
  );

  const state = await manager.readState();
  assert.equal(downloads, 1);
  assert.equal(presenceCalls, 2);
  assert.equal(state.backendPort, 44001);
  assert.equal(state.installTransaction, undefined);
  assert.equal(state.manifest.version, manifest.version);
  assert.equal(fs.readFileSync(manager.ownershipPath, 'utf8'), ownerBefore);
  assert.equal(calls.some((call) => call.args[0] === 'import-rootfs-locked'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('a pending install transaction bound to another install directory is rejected', async () => {
  const manifest = createManifest();
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: false }) }));
  const ownerId = 'd'.repeat(64);
  await seedOwnership(manager, ownerId);
  await manager.writeState({
    installTransaction: {
      schemaVersion: 1,
      id: 'c'.repeat(64),
      phase: 'prepared',
      ownershipId: ownerId,
      distroName: 'AppleMusicDownloader',
      installDir: 'C:\\Users\\attacker\\replacement-distro',
      version: manifest.version,
      manifestSha256: manifestDigest(manifest),
      rootfsSha256: manifest.rootfs.sha256,
      runtimeSha256: manifest.runtime.sha256,
      startedAt: '2026-07-31T00:00:00.000Z',
    },
  });

  await assert.rejects(
    manager.prepareInstallTransaction(manifest),
    (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
  );
});

test('a failed pre-registration transaction is reused after manager restart', async () => {
  const calls = [];
  const manifest = createManifest();
  const appDataDir = tempAppData();
  const runner = healthyRunner({ installed: false, calls });
  const originalExec = runner.exec;
  let failImport = true;
  const importedTransactionIds = [];
  const importedInstallObjectIds = [];
  runner.exec = async (command, args, options) => {
    if (args[0] === 'import-rootfs-locked') {
      const state = JSON.parse(fs.readFileSync(path.join(appDataDir, 'appliance-state.json'), 'utf8'));
      importedTransactionIds.push(state.installTransaction.id);
      importedInstallObjectIds.push(args[2]);
      if (failImport) {
        failImport = false;
        throw new Error('synthetic interrupted import');
      }
    }
    return originalExec(command, args, options);
  };
  const options = {
    appDataDir,
    runner,
    request: async () => HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
  };
  const firstManager = new WslManager(baseOptions(options));

  await assert.rejects(firstManager.install(manifest), (error) => error.code === 'import_failed');
  const failedState = await firstManager.readState();
  const ownerId = fs.readFileSync(firstManager.ownershipPath, 'utf8');
  const transactionId = failedState.installTransaction.id;
  assert.equal(failedState.installTransaction.phase, 'failed');

  const secondManager = new WslManager(baseOptions(options));
  const result = await secondManager.install(manifest);

  assert.equal(result.alreadyInstalled, false);
  assert.equal(fs.readFileSync(secondManager.ownershipPath, 'utf8'), ownerId);
  assert.equal(calls.filter((call) => call.args[0] === 'import-rootfs-locked').length, 1);
  assert.deepEqual(importedTransactionIds, [transactionId, transactionId]);
  assert.deepEqual(importedInstallObjectIds, [INSTALL_OBJECT_ID, INSTALL_OBJECT_ID]);
  assert.equal((await secondManager.readState()).installTransaction, null);
});

test('install re-verifies the complete cached rootfs immediately before wsl import', async () => {
  const runner = healthyRunner({ installed: false });
  const originalExec = runner.exec;
  let verifiedPath = '';
  let importedPath = '';
  runner.exec = async (command, args, options) => {
    if (args[0] === 'import-rootfs-locked') {
      importedPath = args[3];
      assert.equal(verifiedPath, importedPath);
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
    verifyArtifact: async (_artifact, filename) => { verifiedPath = filename; return filename; },
  }));

  await manager.install(createManifest());

  assert.notEqual(verifiedPath, '');
  assert.equal(importedPath, verifiedPath);
});

test('rootfs use-time checksum failure aborts before import and preserves the fresh host transaction', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: false, calls }),
    downloadArtifact: async (_artifact, destination) => destination,
    verifyArtifact: async () => { throw new Error('Artifact SHA-256 mismatch after cache mutation'); },
  }));

  await assert.rejects(manager.install(createManifest()), (error) => error.code === 'checksum_mismatch');

  assert.equal(calls.some((call) => call.args[0] === '--import'), false);
  assert.equal(calls.some((call) => call.args[0] === 'import-rootfs-locked'), false);
  assert.equal(fs.existsSync(manager.ownershipPath), true);
  assert.equal((await manager.readState()).installTransaction.phase, 'failed');
});

test('locked helper rootfs checksum failure is classified as checksum_mismatch', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: false, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === 'import-rootfs-locked') {
      calls.push({ type: 'exec', command, args: [...args], options });
      throw new Error('locked rootfs SHA-256 mismatch');
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    downloadArtifact: async (_artifact, destination) => destination,
  }));

  await assert.rejects(manager.install(createManifest()), (error) => error.code === 'checksum_mismatch');

  assert.equal(calls.some((call) => call.args[0] === 'import-rootfs-locked'), true);
  assert.equal(fs.existsSync(manager.ownershipPath), true);
  assert.equal((await manager.readState()).installTransaction.phase, 'failed');
});

test('install waits for the real supervisor socket and maps a bounded failure to import_failed', async () => {
  let tick = 0;
  const runner = healthyRunner({ installed: false });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--unregister') {
      const result = await originalExec(command, args, options);
      await fs.promises.rm(manager.installDir, { recursive: true, force: true });
      return result;
    }
    if (args.at(-1) === 'status') throw new Error('connect appliance control socket: no such file');
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    downloadArtifact: async (_artifact, destination) => destination,
    sleep: async () => {},
    now: () => { tick += 30_000; return tick; },
  }));
  const manifest = createManifest();

  await assert.rejects(
    manager.install(manifest),
    (error) => error instanceof WslManagerError
      && error.code === 'import_failed'
      && error.details.safeRollbackCompleted === true,
  );
  const ownedState = await manager.readState();
  assert.equal(ownedState.installedVersion, undefined);
  assert.equal(fs.existsSync(manager.ownershipPath), false);
});

test('fresh import rejects incomplete release metadata instead of persisting null runtime identity', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: false, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    const result = await originalExec(command, args, options);
    if (args[0] === '--unregister') {
      await fs.promises.rm(manager.installDir, { recursive: true, force: true });
      return result;
    }
    if (args.at(-1) === 'status') {
      const status = JSON.parse(result.stdout);
      delete status.runtimeSha256;
      delete status.runtimeTransactionId;
      return { ...result, stdout: JSON.stringify(status) };
    }
    return result;
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
  }));

  await assert.rejects(
    manager.install(createManifest()),
    (error) => error instanceof WslManagerError
      && error.code === 'import_failed'
      && error.details.safeRollbackCompleted === true,
  );
  const state = await manager.readState();
  assert.equal(state.installedRuntimeSha256, undefined);
  assert.equal(state.installedRuntimeTransactionId, undefined);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), true);
});

test('local proxy always requests an OS-assigned port instead of the WSL backend port', async () => {
  const starts = [];
  const proxy = {
    start: async (port) => {
      starts.push(port);
      return 43123;
    },
    stop: async () => {},
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true }),
    request: async (url) => url.includes(':8080/')
      ? { statusCode: 200, payload: { service: 'some-other-service', runtime: 'unknown' } }
      : HEALTH,
    proxyFactory: () => proxy,
  }));

  const endpoint = await manager.prepareLocalEndpoint();
  assert.deepEqual(starts, [0]);
  assert.deepEqual(endpoint, { proxied: true, port: 43123, backendPort: 8080 });
  assert.equal(manager.serviceBaseUrl, 'http://127.0.0.1:43123');
  await manager.waitForReady({ timeoutMs: 100, intervalMs: 1 });
});

test('owned proxy requests require an in-memory desktop token that is never persisted', async () => {
  const requests = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true }),
    request: async (url, options = {}) => {
      requests.push({ url, headers: { ...(options.headers || {}) } });
      return HEALTH;
    },
    proxyFactory: () => ({
      start: async () => 43190,
      stop: async () => {},
      diagnostics: () => '',
    }),
  }));
  await seedOwnership(manager);
  await manager.start();
  const headers = manager.desktopProxyRequestHeaders();
  const token = headers['X-AMDL-Desktop-Token'];
  assert.match(token, /^[a-f0-9]{64}$/);
  assert.equal(manager.authorizeDesktopProxyRequest({ 'x-amdl-desktop-token': token }), true);
  assert.equal(manager.authorizeDesktopProxyRequest({ 'x-amdl-desktop-token': '0'.repeat(64) }), false);
  assert.equal(manager.authorizeDesktopProxyRequest({}), false);
  assert.equal(requests.filter((entry) => entry.url.includes(':43190/'))
    .every((entry) => entry.headers['X-AMDL-Desktop-Token'] === token), true);
  assert.equal(requests.filter((entry) => entry.url.includes(':8080/'))
    .every((entry) => !entry.headers['X-AMDL-Desktop-Token']), true);
  assert.equal(fs.readFileSync(manager.statePath, 'utf8').includes(token), false);
});

test('default proxy authorization callback reads headers from handshake metadata', async () => {
  const manager = new WslManager(baseOptions({ proxyFactory: null }));
  const proxy = manager.proxyFactory();
  const token = manager.desktopProxyRequestHeaders()['X-AMDL-Desktop-Token'];
	assert.equal(proxy.windowsProbeExe, manager.windowsProbeExe);
	assert.deepEqual(proxy.commandArguments(), ['proxy-wsl-stdio']);

  assert.equal(await proxy.authorizeRequest({
    method: 'GET',
    target: '/api/v1/jobs',
    headers: { 'x-amdl-desktop-token': [token] },
  }), true);
  assert.equal(await proxy.authorizeRequest({
    headers: { 'x-amdl-desktop-token': ['0'.repeat(64)] },
  }), false);
  assert.equal(await proxy.authorizeRequest({ 'x-amdl-desktop-token': [token] }), false,
    'a raw headers object must not be mistaken for handshake metadata');
});

test('a foreign Windows service on 8080 selects and configures a free backend port before start', async () => {
  const calls = [];
  const selected = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls, initialServiceState: 'stopped' }),
    request: async (url) => url.includes(':8080/')
      ? { statusCode: 200, payload: { service: 'docker-service', runtime: 'container' } }
      : HEALTH,
    selectBackendPort: async (preferred) => {
      selected.push(preferred);
      return 43124;
    },
  }));
  await seedOwnership(manager);

  const status = await manager.start();

  assert.deepEqual(selected, [8080]);
  const configure = calls.find((call) => call.args[0] === 'configure-download-locked');
  assert.equal(configure.args[3], '43124');
  assert.equal(calls.indexOf(configure) < calls.findIndex((call) => call.args.at(-1) === 'start'), true);
  assert.equal(status.backendPort, 43124);
  assert.equal(status.port, 54321);
  assert.equal((await manager.readState()).backendPort, 43124);
});

test('an unused preferred port is retained for backend configuration', async () => {
  const calls = [];
  const selected = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls, initialServiceState: 'stopped' }),
    request: async (url) => url.endsWith('/api/v1/health/ready') && calls.some((call) => call.args.at(-1) === 'start')
      ? HEALTH
      : Promise.reject(new Error('ECONNREFUSED')),
    selectBackendPort: async (preferred) => {
      selected.push(preferred);
      return preferred;
    },
  }));
  await seedOwnership(manager);

  await manager.start();

  assert.deepEqual(selected, [8080]);
  const configure = calls.find((call) => call.args[0] === 'configure-download-locked');
  assert.equal(configure.args[3], '8080');
});

test('an already running healthy appliance is never moved or reprobed', async () => {
  let selectorCalls = 0;
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async () => HEALTH,
    selectBackendPort: async () => {
      selectorCalls += 1;
      return 43125;
    },
  }));
  await seedOwnership(manager);

  const status = await manager.start();

  assert.equal(selectorCalls, 0);
  assert.equal(status.backendPort, 8080);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'configure-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'network-preflight'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'start'), false);
});

test('normal startup reuses a running appliance without resolving or resending unchanged credentials', async () => {
  const calls = [];
  let reads = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }), request: async () => HEALTH,
  }));
  await seedOwnership(manager);
  await manager.start(undefined, {
    reuseRunning: true,
    credentialProvider: async () => { reads += 1; throw new Error('unchanged credentials must stay deferred'); },
  });
  assert.equal(reads, 0);
  assert.equal(calls.some(call => call.args.at(-1) === 'stop'), false);
  assert.equal(calls.some(call => call.args.at(-1) === 'start'), false);
  assert.equal(calls.some(call => call.args.at(-1) === 'credential-stdin'), false);
});

test('an already running appliance without a ready broker fails closed without replacing its generation', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls, initialNetworkState: 'unprepared' }),
    request: async () => HEALTH,
  }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.start(),
    (error) => error instanceof WslManagerError && error.code === 'network_preflight_failed',
  );

  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'start'), false);
});

test('state writes from interleaved manager instances are serialized without losing disjoint fields', async () => {
  const appDataDir = tempAppData();
  const statePath = path.join(appDataDir, 'appliance-state.json');
  const first = new WslManager(baseOptions({ appDataDir, statePath }));
  const second = new WslManager(baseOptions({ appDataDir, statePath }));
  const originalRead = first.readState.bind(first);
  let entered;
  let resume;
  const readEntered = new Promise((resolve) => { entered = resolve; });
  const resumeRead = new Promise((resolve) => { resume = resolve; });
  let pauseFirstRead = true;
  first.readState = async () => {
    const state = await originalRead();
    if (pauseFirstRead) {
      pauseFirstRead = false;
      entered();
      await resumeRead;
    }
    return state;
  };

  const firstWrite = first.writeState({ terminating: { phase: 'requested' } });
  await readEntered;
  let secondFinished = false;
  const secondWrite = second.writeState({ updateTransaction: { id: 'transaction-1' } })
    .finally(() => { secondFinished = true; });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(secondFinished, false);
  resume();
  await Promise.all([firstWrite, secondWrite]);

  const state = await first.readState();
  assert.deepEqual(state.terminating, { phase: 'requested' });
  assert.deepEqual(state.updateTransaction, { id: 'transaction-1' });
  assert.equal(state.stateRevision, 2);
  assert.deepEqual(fs.readdirSync(appDataDir).filter((name) => name.endsWith('.tmp')), []);
});

test('a stale state snapshot cannot overwrite a newer transaction or recovery marker', async () => {
  const manager = new WslManager(baseOptions());
  await manager.writeState({ backendPort: 43125 });
  const stale = await manager.readState();
  await manager.writeState({
    updateTransaction: { id: 'durable-transaction' },
    localPocRepair: { phase: 'target_verified' },
  });

  await assert.rejects(
    manager.writeState({ ...stale, backendPort: 43126, updateTransaction: null, localPocRepair: null }),
    (error) => error instanceof WslManagerError
      && error.code === 'state_conflict'
      && error.details.expectedRevision === stale.stateRevision,
  );
  const state = await manager.readState();
  assert.equal(state.backendPort, 43125);
  assert.deepEqual(state.updateTransaction, { id: 'durable-transaction' });
  assert.deepEqual(state.localPocRepair, { phase: 'target_verified' });
});

test('a new manager restores the persisted backend port independently of its local proxy port', async () => {
  const appDataDir = tempAppData();
  const statePath = path.join(appDataDir, 'appliance-state.json');
  const first = new WslManager(baseOptions({ appDataDir, statePath }));
  await first.writeState({ backendPort: 43126, localPort: 54326, usesLoopbackProxy: true });
  const calls = [];
  const selected = [];
  const restarted = new WslManager(baseOptions({
    appDataDir,
    statePath,
    runner: healthyRunner({ installed: true, calls, initialServiceState: 'stopped' }),
    request: async (url) => calls.some((call) => call.args.at(-1) === 'start')
      && (url.includes(':43126/') || url.includes(':54321/'))
      ? HEALTH
      : Promise.reject(new Error('ECONNREFUSED')),
    selectBackendPort: async (preferred) => {
      selected.push(preferred);
      return preferred;
    },
  }));
  await seedOwnership(restarted);

  const status = await restarted.start();

  assert.deepEqual(selected, [43126]);
  assert.equal(status.backendPort, 43126);
  assert.equal(status.port, 54321);
  const configure = calls.find((call) => call.args[0] === 'configure-download-locked');
  assert.equal(configure.args[3], '43126');
});

test('proxy localPort remains separate from the persisted WSL backendPort', async () => {
  const starts = [];
  const proxy = {
    start: async (port) => { starts.push(port); return 54327; },
    stop: async () => {},
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43127,
    request: async (url) => url.includes(':54327/') ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => proxy,
  }));
  await manager.writeState({ backendPort: 43127, localPort: 49999 });

  const endpoint = await manager.prepareLocalEndpoint();
  const state = await manager.readState();

  assert.deepEqual(starts, [0]);
  assert.deepEqual(endpoint, { proxied: true, port: 54327, backendPort: 43127 });
  assert.equal(state.backendPort, 43127);
  assert.equal(state.localPort, 54327);
  assert.equal(state.usesLoopbackProxy, true);
});

test('a legacy same-port proxy is stopped and rebuilt on a dynamic Windows port', async () => {
  const starts = [];
  const selected = [];
  let directRequests = 0;
  let legacyStops = 0;
  let replacementStops = 0;
  const legacy = {
    start: async () => { throw new Error('legacy proxy must not be reused'); },
    stop: async () => { legacyStops += 1; },
    diagnostics: () => '',
  };
  const replacement = {
    start: async (port) => { starts.push(port); return 54328; },
    stop: async () => { replacementStops += 1; },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43128,
    request: async (url) => {
      if (url.includes(':43128/')) directRequests += 1;
      return url.includes(':54328/') ? HEALTH : Promise.reject(new Error('direct forwarding unavailable'));
    },
    proxyFactory: () => replacement,
    selectBackendPort: async (port) => { selected.push(port); return port + 1; },
  }));
  manager.proxy = legacy;
  manager.proxyLocalPort = 43128;
  manager.proxyBackendPort = 43128;
  manager.serviceBaseUrl = 'http://127.0.0.1:43128';
  await manager.writeState({ backendPort: 43128, localPort: 43128, usesLoopbackProxy: true });

  await manager.ensureBackendPort();

  const endpoint = await manager.prepareLocalEndpoint();
  const state = await manager.readState();

  assert.equal(directRequests, 0);
  assert.deepEqual(selected, []);
  assert.equal(legacyStops, 1);
  assert.equal(replacementStops, 0);
  assert.deepEqual(starts, [0]);
  assert.deepEqual(endpoint, { proxied: true, port: 54328, backendPort: 43128 });
  assert.equal(state.localPort, 54328);
  assert.equal(state.usesLoopbackProxy, true);
});

test('a dynamic allocator collision with the WSL backend is stopped and retried', async () => {
  const starts = [];
  const stops = [];
  let factoryCalls = 0;
  const manager = new WslManager(baseOptions({
    backendPort: 43129,
    request: async (url) => url.includes(':54329/') ? HEALTH : Promise.reject(new Error('direct forwarding unavailable')),
    proxyFactory: () => {
      const index = factoryCalls;
      factoryCalls += 1;
      return {
        start: async (port) => {
          starts.push({ index, port });
          return index === 0 ? 43129 : 54329;
        },
        stop: async () => { stops.push(index); },
        diagnostics: () => '',
      };
    },
  }));

  const endpoint = await manager.prepareLocalEndpoint();
  const state = await manager.readState();

  assert.deepEqual(starts, [{ index: 0, port: 0 }, { index: 1, port: 0 }]);
  assert.deepEqual(stops, [0]);
  assert.deepEqual(endpoint, { proxied: true, port: 54329, backendPort: 43129 });
  assert.equal(state.localPort, 54329);
  assert.equal(state.usesLoopbackProxy, true);
});

test('an unconfirmed proxy stop stays tracked and blocks every replacement attempt', async () => {
  const appDataDir = tempAppData();
  const statePath = path.join(appDataDir, 'appliance-state.json');
  let stopCalls = 0;
  let factoryCalls = 0;
  const stuckProxy = {
    start: async () => { throw new Error('stuck proxy must never restart'); },
    stop: async () => { stopCalls += 1; throw new Error('helper exit was not confirmed'); },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    appDataDir,
    statePath,
    backendPort: 43135,
    request: async (url) => url.includes(':54335/') ? HEALTH : Promise.reject(new Error('direct unavailable')),
    proxyFactory: () => {
      factoryCalls += 1;
      return {
        start: async () => 54335,
        stop: async () => {},
        diagnostics: () => '',
      };
    },
  }));
  manager.proxy = stuckProxy;
  manager.proxyLocalPort = 43135;
  manager.proxyBackendPort = 43135;
  manager.serviceBaseUrl = 'http://127.0.0.1:43135';
  await manager.writeState({ backendPort: 43135, localPort: 43135, usesLoopbackProxy: true });

  for (let attempt = 0; attempt < 2; attempt += 1) {
    await assert.rejects(
      manager.prepareLocalEndpoint(),
      (error) => error instanceof WslManagerError && error.code === 'port_in_use',
    );
  }

  const state = await manager.readState();
  assert.equal(stopCalls, 2);
  assert.equal(factoryCalls, 0, 'a new helper was created while the old helper exit was unconfirmed');
  assert.equal(manager.proxy, stuckProxy);
  assert.equal(manager.proxyLocalPort, null);
  assert.equal(manager.proxyBackendPort, null);
  assert.equal(manager.serviceBaseUrl, '');
  assert.equal(state.localPort, null);
  assert.equal(state.usesLoopbackProxy, false);
});

test('three dynamic allocator collisions fail closed without publishing a backend URL', async () => {
  const starts = [];
  const stops = [];
  let factoryCalls = 0;
  const manager = new WslManager(baseOptions({
    backendPort: 43130,
    request: async () => Promise.reject(new Error('direct forwarding unavailable')),
    proxyFactory: () => {
      const index = factoryCalls;
      factoryCalls += 1;
      return {
        start: async (port) => { starts.push({ index, port }); return 43130; },
        stop: async () => { stops.push(index); },
        diagnostics: () => '',
      };
    },
  }));
  await manager.writeState({ backendPort: 43130, localPort: 43130, usesLoopbackProxy: true });

  await assert.rejects(
    manager.prepareLocalEndpoint(),
    (error) => error instanceof WslManagerError && error.code === 'port_in_use',
  );

  const state = await manager.readState();
  assert.deepEqual(starts, [{ index: 0, port: 0 }, { index: 1, port: 0 }, { index: 2, port: 0 }]);
  assert.deepEqual(stops, [0, 1, 2]);
  assert.equal(manager.proxy, null);
  assert.equal(manager.proxyLocalPort, null);
  assert.equal(manager.proxyBackendPort, null);
  assert.equal(manager.serviceBaseUrl, '');
  assert.equal(state.localPort, null);
  assert.equal(state.usesLoopbackProxy, false);
});

test('a new manager heals a persisted same-port endpoint without moving its WSL backend', async () => {
  const appDataDir = tempAppData();
  const statePath = path.join(appDataDir, 'appliance-state.json');
  const first = new WslManager(baseOptions({ appDataDir, statePath }));
  await first.writeState({ backendPort: 43131, localPort: 43131, usesLoopbackProxy: true });
  const starts = [];
  const selected = [];
  const restarted = new WslManager(baseOptions({
    appDataDir,
    statePath,
    request: async (url) => url.includes(':54331/') ? HEALTH : Promise.reject(new Error('direct forwarding unavailable')),
    proxyFactory: () => ({
      start: async (port) => { starts.push(port); return 54331; },
      stop: async () => {},
      diagnostics: () => '',
    }),
    selectBackendPort: async (port) => { selected.push(port); return port + 1; },
  }));

  const endpoint = await restarted.prepareLocalEndpoint();
  const state = await restarted.readState();

  assert.deepEqual(starts, [0]);
  assert.deepEqual(selected, []);
  assert.deepEqual(endpoint, { proxied: true, port: 54331, backendPort: 43131 });
  assert.equal(state.backendPort, 43131);
  assert.equal(state.localPort, 54331);
  assert.equal(state.usesLoopbackProxy, true);
});

test('proxy factory, allocation and state-publication failures leave no usable endpoint', async () => {
  const cases = [
    {
      name: 'factory failure',
      proxyFactory: () => { throw new Error('factory failed'); },
      expectedStops: 0,
    },
    {
      name: 'start failure',
      proxyFactory: () => {
        let stops = 0;
        return Object.assign({
          start: async () => { throw new Error('start failed'); },
          stop: async () => { stops += 1; },
          diagnostics: () => '',
        }, { getStops: () => stops });
      },
      expectedStops: 1,
    },
    {
      name: 'invalid allocator result',
      proxyFactory: () => {
        let stops = 0;
        return Object.assign({
          start: async () => 0,
          stop: async () => { stops += 1; },
          diagnostics: () => '',
        }, { getStops: () => stops });
      },
      expectedStops: 1,
    },
  ];
  for (const scenario of cases) {
    let created;
    const manager = new WslManager(baseOptions({
      backendPort: 43132,
      request: async () => Promise.reject(new Error('direct forwarding unavailable')),
      proxyFactory: () => {
        created = scenario.proxyFactory();
        return created;
      },
    }));
    await manager.writeState({ backendPort: 43132, localPort: 43132, usesLoopbackProxy: true });

    await assert.rejects(
      manager.prepareLocalEndpoint(),
      (error) => error instanceof WslManagerError && error.code === 'port_in_use',
      scenario.name,
    );

    const state = await manager.readState();
    assert.equal(manager.proxy, null, scenario.name);
    assert.equal(manager.proxyLocalPort, null, scenario.name);
    assert.equal(manager.proxyBackendPort, null, scenario.name);
    assert.equal(manager.serviceBaseUrl, '', scenario.name);
    assert.equal(state.localPort, null, scenario.name);
    assert.equal(state.usesLoopbackProxy, false, scenario.name);
    assert.equal(created ? created.getStops() : 0, scenario.expectedStops, scenario.name);
  }
});

test('endpoint state publication failure closes the listener without publishing a URL', async () => {
  let stops = 0;
  const manager = new WslManager(baseOptions({
    backendPort: 43133,
    request: async (url) => url.includes(':54333/')
      ? HEALTH
      : Promise.reject(new Error('direct forwarding unavailable')),
    proxyFactory: () => ({
      start: async () => 54333,
      stop: async () => { stops += 1; },
      diagnostics: () => '',
    }),
  }));
  const originalWriteState = manager.writeState.bind(manager);
  let writes = 0;
  manager.writeState = async (update, options) => {
    writes += 1;
    if (writes === 2) throw new Error('state publication failed');
    return originalWriteState(update, options);
  };

  await assert.rejects(manager.prepareLocalEndpoint(), /state publication failed/);

  const state = await manager.readState();
  assert.equal(stops, 1);
  assert.equal(manager.proxy, null);
  assert.equal(manager.serviceBaseUrl, '');
  assert.equal(state.localPort, null);
  assert.equal(state.usesLoopbackProxy, false);
});

test('candidate endpoint health failure never publishes a reusable URL or persisted port', async () => {
  let stops = 0;
  const manager = new WslManager(baseOptions({
    backendPort: 43136,
    request: async (url) => url.includes(':54336/')
      ? { statusCode: 503, payload: { error: { code: 'backend_unavailable' } } }
      : Promise.reject(new Error('direct forwarding unavailable')),
    proxyFactory: () => ({
      start: async () => 54336,
      stop: async () => { stops += 1; },
      diagnostics: () => '',
    }),
  }));

  await assert.rejects(
    manager.prepareLocalEndpoint(),
    (error) => error instanceof WslManagerError && error.code === 'health_timeout',
  );
  const state = await manager.readState();
  assert.equal(stops, 1);
  assert.equal(manager.proxy, null);
  assert.equal(manager.serviceBaseUrl, '');
  assert.equal(manager.proxyLocalPort, null);
  assert.equal(state.localPort, null);
  assert.equal(state.usesLoopbackProxy, false);
});

test('candidate endpoint identity mismatch is rejected before endpoint publication', async () => {
  let stops = 0;
  const manager = new WslManager(baseOptions({
    backendPort: 43141,
    request: async (url) => url.includes(':54341/')
      ? { statusCode: 200, payload: { service: 'foreign-service', runtime: 'foreign-runtime', version: '1.2.0' } }
      : Promise.reject(new Error('direct forwarding unavailable')),
    proxyFactory: () => ({
      start: async () => 54341,
      stop: async () => { stops += 1; },
      diagnostics: () => '',
    }),
  }));
  await assert.rejects(
    manager.prepareLocalEndpoint(),
    (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
  );
  assert.equal(stops, 1);
  assert.equal(manager.serviceBaseUrl, '');
  assert.equal((await manager.readState()).localPort, null);
});

test('proxy stop failure blocks WSL terminate and preserves proxy ownership', async () => {
  const calls = [];
  const proxy = {
    start: async () => 54337,
    stop: async () => { throw new Error('helper exit was not confirmed'); },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43137,
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.includes(':54337/') ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => proxy,
  }));
  await seedOwnership(manager);
  await manager.prepareLocalEndpoint();

  await assert.rejects(
    manager.stop(),
    (error) => error instanceof WslManagerError && error.code === 'port_in_use',
  );
  assert.equal(manager.proxy, proxy);
  assert.equal(manager.serviceBaseUrl, '');
  assert.equal((await manager.readState()).usesLoopbackProxy, false);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), true);
});

test('unregister propagates proxy stop failure and never unregisters the owned distro', async () => {
  const calls = [];
  const proxy = {
    start: async () => 54338,
    stop: async () => { throw new Error('helper exit was not confirmed'); },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43138,
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.includes(':54338/') ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => proxy,
  }));
  await seedOwnership(manager);
  await manager.prepareLocalEndpoint();

  await assert.rejects(
    manager.unregisterVerifiedDistro(),
    (error) => error instanceof WslManagerError && error.code === 'port_in_use',
  );
  assert.equal(manager.proxy, proxy);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('concurrent endpoint preparation shares one dynamic listener and publishes it once', async () => {
  let factoryCalls = 0;
  let starts = 0;
  let stops = 0;
  let releaseStart;
  const enteredStart = new Promise((resolve) => { releaseStart = resolve; });
  let continueStart;
  const allowStart = new Promise((resolve) => { continueStart = resolve; });
  const manager = new WslManager(baseOptions({
    backendPort: 43134,
    request: async (url) => url.includes(':54334/') ? HEALTH : Promise.reject(new Error('direct forwarding unavailable')),
    proxyFactory: () => {
      factoryCalls += 1;
      return {
        start: async (port) => {
          starts += 1;
          assert.equal(port, 0);
          releaseStart();
          await allowStart;
          return 54334;
        },
        stop: async () => { stops += 1; },
        diagnostics: () => '',
      };
    },
  }));

  const first = manager.prepareLocalEndpoint();
  await enteredStart;
  const second = manager.prepareLocalEndpoint();
  continueStart();
  const [endpoint, secondEndpoint] = await Promise.all([first, second]);
  const state = await manager.readState();

  assert.equal(factoryCalls, 1);
  assert.equal(starts, 1);
  assert.equal(stops, 0);
  assert.deepEqual(endpoint, { proxied: true, port: 54334, backendPort: 43134 });
  assert.deepEqual(secondEndpoint, endpoint);
  assert.equal(state.localPort, 54334);
  assert.equal(state.usesLoopbackProxy, true);
});

test('stop serializes behind endpoint preparation and cannot republish a stale local endpoint', async () => {
  const calls = [];
  let lifecycleAcquisitions = 0;
  let starts = 0;
  let stops = 0;
  let listening = false;
  let releaseStart;
  const enteredStart = new Promise((resolve) => { releaseStart = resolve; });
  let continueStart;
  const allowStart = new Promise((resolve) => { continueStart = resolve; });
  const lock = {
    assertHeld() { return this; },
    async release() {},
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43135,
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.includes(':54335/') && listening
      ? HEALTH
      : Promise.reject(new Error('forwarding unavailable')),
    processLockProvider: async (kind) => {
      if (kind === 'lifecycle') lifecycleAcquisitions += 1;
      return lock;
    },
    proxyFactory: () => ({
      start: async (port) => {
        starts += 1;
        assert.equal(port, 0);
        releaseStart();
        await allowStart;
        listening = true;
        return 54335;
      },
      stop: async () => {
        stops += 1;
        listening = false;
      },
      diagnostics: () => '',
    }),
  }));
  await seedOwnership(manager);

  const preparing = manager.prepareLocalEndpoint();
  await enteredStart;
  try {
    assert.equal(lifecycleAcquisitions, 1,
      'endpoint preparation must hold the same lifecycle lock as stop');
    const stopping = manager.stop();
    continueStart();
    await Promise.all([preparing, stopping]);
  } finally {
    continueStart();
    await preparing.catch(() => {});
  }

  const state = await manager.readState();
  assert.equal(starts, 1);
  assert.equal(stops, 1);
  assert.equal(listening, false);
  assert.equal(manager.proxy, null);
  assert.equal(manager.proxyLocalPort, null);
  assert.equal(manager.proxyBackendPort, null);
  assert.equal(manager.serviceBaseUrl, '');
  assert.equal(state.localPort, null);
  assert.equal(state.usesLoopbackProxy, false);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), true);
});

test('a lifecycle owner never waits on an endpoint preparation queued outside its lock', async () => {
  let starts = 0;
  let releaseOwner;
  const ownerMayPrepare = new Promise((resolve) => { releaseOwner = resolve; });
  let signalOwner;
  const ownerEntered = new Promise((resolve) => { signalOwner = resolve; });
  const manager = new WslManager(baseOptions({
    backendPort: 43136,
    request: async (url) => url.includes(':54336/') ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => ({
      start: async (port) => {
        starts += 1;
        assert.equal(port, 0);
        return 54336;
      },
      stop: async () => {},
      diagnostics: () => '',
    }),
  }));

  const owner = manager.withLifecycleLock(async () => {
    signalOwner();
    await ownerMayPrepare;
    return manager.prepareLocalEndpoint();
  });
  await ownerEntered;
  const outside = manager.prepareLocalEndpoint();
  try {
    assert.equal(manager.endpointPreparation, null,
      'a request queued for the lifecycle lock must not become the shared endpoint preparation');
    releaseOwner();
    const [ownedEndpoint, outsideEndpoint] = await Promise.all([owner, outside]);
    assert.deepEqual(ownedEndpoint, { proxied: true, port: 54336, backendPort: 43136 });
    assert.deepEqual(outsideEndpoint, ownedEndpoint);
    assert.equal(starts, 1);
  } finally {
    // Keep the pre-fix red path from retaining a queued promise after its
    // expected assertion failure; production code never performs this reset.
    manager.endpointPreparation = null;
    releaseOwner();
    await Promise.allSettled([owner, outside]);
  }
});

test('a failed backend reconfigures a newly selected port before it is restarted', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls, initialServiceState: 'failed' }),
    request: async (url) => calls.some((call) => call.args.at(-1) === 'start')
      && (url.includes(':43129/') || url.includes(':54321/'))
      ? HEALTH
      : Promise.reject(new Error('backend unavailable')),
    selectBackendPort: async (preferred) => {
      assert.equal(preferred, 43128);
      return 43129;
    },
  }));
  await manager.writeState({ backendPort: 43128, installedVersion: '1.2.0' });
  await seedOwnership(manager);

  const status = await manager.start();

  const configure = calls.find((call) => call.args[0] === 'configure-download-locked');
  const startIndex = calls.findIndex((call) => call.args.at(-1) === 'start');
  assert.equal(configure.args[3], '43129');
  assert.equal(calls.indexOf(configure) < startIndex, true);
  assert.equal(status.state, 'running');
  assert.equal(status.backendPort, 43129);
});

test('repeated endpoint and backend-port preparation preserves a valid owned dynamic proxy', async () => {
  const requests = [];
  const selected = [];
  const proxyLocalPort = 54330;
  let proxyStops = 0;
  let proxyStarts = 0;
  const proxy = {
    start: async (port) => {
      proxyStarts += 1;
      assert.equal(port, 0);
      return proxyLocalPort;
    },
    stop: async () => { proxyStops += 1; },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43130,
    request: async (url, options = {}) => {
      requests.push({ url, headers: { ...(options.headers || {}) } });
      if (proxyLocalPort !== null && url.includes(`:${proxyLocalPort}/`)) {
        return options.headers?.[DESKTOP_PROXY_HEADER]
          ? HEALTH
          : { statusCode: 403, payload: { error: 'desktop proxy token required' } };
      }
      throw new Error('forwarding unavailable');
    },
    proxyFactory: () => proxy,
    selectBackendPort: async (preferred) => {
      selected.push(preferred);
      return preferred + 1;
    },
  }));

  assert.deepEqual(await manager.prepareLocalEndpoint(), { proxied: true, port: 54330, backendPort: 43130 });
  requests.length = 0;
  await manager.ensureBackendPort();
  assert.deepEqual(await manager.prepareLocalEndpoint(), { proxied: true, port: 54330, backendPort: 43130 });

  const state = await manager.readState();
  assert.equal(requests.length > 0, true);
  assert.equal(requests.every((request) => request.headers[DESKTOP_PROXY_HEADER]), true);
  assert.deepEqual(selected, []);
  assert.equal(proxyStops, 0);
  assert.equal(proxyStarts, 1);
  assert.equal(manager.backendPort, 43130);
  assert.equal(state.backendPort, 43130);
  assert.equal(state.usesLoopbackProxy, true);
  assert.equal(state.localPort, 54330);
});

test('ensureBackendPort follows the backend witnessed by an owned proxy instead of moving it', async () => {
  const selected = [];
  const manager = new WslManager(baseOptions({
    backendPort: 43139,
    selectBackendPort: async (port) => { selected.push(port); return port + 1; },
    request: async () => { throw new Error('direct forwarding unavailable'); },
  }));
  await manager.writeState({ backendPort: 43139 });
  manager.proxy = { start: async () => 54339, stop: async () => {}, diagnostics: () => '' };
  manager.proxyBackendPort = 43140;
  manager.proxyLocalPort = 54339;
  manager.serviceBaseUrl = 'http://127.0.0.1:54339';

  const result = await manager.ensureBackendPort();
  assert.deepEqual(result, { port: 43140, existingService: false, changed: true });
  assert.equal(manager.backendPort, 43140);
  assert.deepEqual(selected, []);
});

test('endpoint creation and an external backend-port check cannot cross-bind generations', async () => {
  const state = { schemaVersion: 1, distroName: 'AppleMusicDownloader', stateRevision: 1, backendPort: 43150 };
  let releaseStart;
  let signalStart;
  const proxyStartEntered = new Promise((resolve) => { signalStart = resolve; });
  const proxyStartReleased = new Promise((resolve) => { releaseStart = resolve; });
  let selected = 0;
  const manager = new WslManager(baseOptions({
    backendPort: 43150,
    request: async (url) => {
      if (url.includes(':54350/')) return HEALTH;
      throw new Error(`direct forwarding unavailable for ${url}`);
    },
    selectBackendPort: async (port) => {
      selected += 1;
      return port + 1;
    },
    proxyFactory: () => ({
      start: async (port) => {
        assert.equal(port, 0);
        signalStart();
        await proxyStartReleased;
        return 54350;
      },
      stop: async () => {},
      diagnostics: () => '',
    }),
  }));
  manager.readState = async () => ({ ...state });
  manager.writeState = async (update) => {
    Object.assign(state, update);
    state.stateRevision += 1;
    return { ...state };
  };

  const preparing = manager.prepareLocalEndpoint();
  await proxyStartEntered;
  const selecting = manager.ensureBackendPort();
  try {
    // All of the pre-selection awaits resolve from in-memory fakes.  A direct
    // (unlocked) ensureBackendPort would now change backendPort while the
    // listener is still waiting; the lifecycle wrapper must keep it queued.
    for (let index = 0; index < 12; index += 1) await Promise.resolve();
    assert.equal(manager.backendPort, 43150);
    assert.equal(selected, 0);
  } finally {
    // A red assertion must not strand the intentionally paused fake listener
    // and cancel unrelated tests in this file.
    releaseStart();
  }
  assert.deepEqual(await preparing, { proxied: true, port: 54350, backendPort: 43150 });
  assert.deepEqual(await selecting, { port: 43150, existingService: false, changed: false });
  assert.equal(manager.proxyBackendPort, 43150);
  assert.equal(manager.backendPort, 43150);
  assert.equal(state.backendPort, 43150);
  assert.equal(state.localPort, 54350);
});

test('explicit stop closes only the owned proxy and clears stale local endpoint state', async () => {
  const calls = [];
  let proxyStops = 0;
  let proxyListening = false;
  const proxy = {
    start: async () => { proxyListening = true; return 54331; },
    stop: async () => { proxyListening = false; proxyStops += 1; },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43131,
    runner: healthyRunner({ installed: true, calls }),
    request: async () => proxyListening ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => proxy,
  }));
  await seedOwnership(manager);
  await manager.prepareLocalEndpoint();

  await manager.stop();

  const state = await manager.readState();
  assert.equal(proxyStops, 1);
  assert.equal(calls.filter((call) => call.args.at(-1) === 'stop').length, 1);
  const stopIndex = calls.findIndex((call) => call.args.at(-1) === 'stop');
  const terminateIndex = calls.findIndex((call) => call.args[0] === '--terminate');
  assert.deepEqual(calls[terminateIndex].args, ['--terminate', 'AppleMusicDownloader']);
  assert.equal(stopIndex < terminateIndex, true);
  assert.equal(calls.some((call) => call.args.includes('--shutdown')), false);
  assert.equal(state.backendPort, 43131);
  assert.equal(state.localPort, null);
  assert.equal(state.usesLoopbackProxy, false);

  const restarted = await manager.start();
  const restartedState = await manager.readState();
  assert.equal(restarted.state, 'running');
  assert.equal(restarted.backendPort, 43131);
  assert.equal(restarted.port, 54331);
  assert.equal(restartedState.usesLoopbackProxy, true);
  assert.equal(restartedState.localPort, 54331);
});

test('stop fails closed before the control stop command when ownership is missing', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));

  await assert.rejects(
    manager.stop(),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );

  assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), false);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
});

test('stop re-proves the same owner and BasePath before terminating WSL', async () => {
  const calls = [];
  let identityReads = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    registrationIdentityProvider: async () => {
      identityReads += 1;
      return {
        version: 1,
        distributionName: 'AppleMusicDownloader',
        basePath: identityReads === 1 ? manager.installDir : 'C:\\Users\\attacker\\replacement-distro',
        wslVersion: 2,
      };
    },
  }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.stop(),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );

  assert.equal(calls.filter((call) => call.args.at(-1) === 'stop').length, 1);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(identityReads, 2);
});

test('active-job stop rejection never terminates WSL or clears the live endpoint', async () => {
  const calls = [];
  let proxyStops = 0;
  let proxyListening = false;
  const runner = healthyRunner({ installed: true, calls });
  const healthyExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'stop') {
      calls.push({ type: 'exec', command, args: [...args], options });
      const response = JSON.stringify({
        ok: false,
        status: {
          service: 'applemusic-downloader',
          runtime: 'wsl-appliance',
          version: '1.2.0',
          state: 'running',
        },
        error: {
          code: 'active_jobs',
          message: '仍有排队或运行中的任务，不能停止服务。',
          action: '请等待任务完成，或先取消任务后重试。',
        },
      });
      const error = new Error(response);
      error.stdout = response;
      throw error;
    }
    return healthyExec(command, args, options);
  };
  const proxy = {
    start: async () => { proxyListening = true; return 54333; },
    stop: async () => { proxyListening = false; proxyStops += 1; },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43133,
    runner,
    request: async () => proxyListening ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => proxy,
  }));
  await seedOwnership(manager);
  await manager.prepareLocalEndpoint();
  const before = await manager.readState();

  await assert.rejects(manager.stop(), (error) => error instanceof WslManagerError && error.code === 'active_jobs');

  const after = await manager.readState();
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(proxyStops, 0);
  assert.equal(proxyListening, true);
  assert.equal(after.localPort, before.localPort);
  assert.equal(after.usesLoopbackProxy, true);
  assert.notEqual(manager.serviceBaseUrl, '');
});

test('a second manager reconnects after the first process proxy closes without stopping the backend', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const appDataDir = tempAppData();
  const statePath = path.join(appDataDir, 'appliance-state.json');
  let firstProxyListening = false;
  let secondProxyListening = false;
  const first = new WslManager(baseOptions({
    appDataDir,
    statePath,
    backendPort: 43132,
    runner,
    request: async () => firstProxyListening ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => ({
      start: async () => { firstProxyListening = true; return 54331; },
      stop: async () => { firstProxyListening = false; },
      diagnostics: () => '',
    }),
  }));
  await seedOwnership(first);
  await first.writeState({ backendPort: 43132, installedVersion: '1.2.0' });
  await first.prepareLocalEndpoint({ expectedVersion: '1.2.0' });
  // Simulate the first Electron/Node process exiting: its local server closes,
  // while no appliance stop command is sent to WSL.
  await first.proxy.stop();
  first.proxy = null;
  first.proxyLocalPort = null;

  const second = new WslManager(baseOptions({
    appDataDir,
    statePath,
    runner,
    request: async () => secondProxyListening ? HEALTH : Promise.reject(new Error('forwarding unavailable')),
    proxyFactory: () => ({
      start: async () => { secondProxyListening = true; return 54332; },
      stop: async () => { secondProxyListening = false; },
      diagnostics: () => '',
    }),
  }));
  const status = await second.start();

  assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), false);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(status.state, 'running');
  assert.equal(status.backendPort, 43132);
  assert.equal(status.port, 54332);
  const state = await second.readState();
  assert.equal(state.backendPort, 43132);
  assert.equal(state.localPort, 54332);
  assert.equal(state.usesLoopbackProxy, true);
});

test('waitForReady rejects wrong health identity and returns health_timeout', async () => {
  let tick = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner(),
    request: async () => ({ statusCode: 200, payload: { service: 'wrong', runtime: 'wrong' } }),
    sleep: async () => {},
    now: () => tick++ * 50,
  }));
  manager.proxy = { stop: async () => {} };
  manager.proxyLocalPort = 43191;
  manager.proxyBackendPort = manager.backendPort;
  manager.serviceBaseUrl = 'http://127.0.0.1:43191';
  await assert.rejects(
    manager.waitForReady({ timeoutMs: 100, intervalMs: 1 }),
    (error) => error instanceof WslManagerError && error.code === 'health_timeout',
  );
});

test('waitForReady rejects the right service running the wrong manifest version', async () => {
  let tick = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner(),
    request: async () => HEALTH,
    sleep: async () => {},
    now: () => tick++ * 50,
  }));
  manager.proxy = { stop: async () => {} };
  manager.proxyLocalPort = 43192;
  manager.proxyBackendPort = manager.backendPort;
  manager.serviceBaseUrl = 'http://127.0.0.1:43192';
  await assert.rejects(
    manager.waitForReady({ timeoutMs: 100, intervalMs: 1, expectedVersion: '1.2.1' }),
    (error) => error instanceof WslManagerError
      && error.code === 'health_timeout'
      && /does not match 1\.2\.1/.test(error.details.reason),
  );
});

test('terminal readiness failure invalidates the endpoint and persisted local port', async () => {
  for (const scenario of [
    {
      name: 'HTTP 503',
      request: async () => ({ statusCode: 503, payload: { error: { code: 'backend_not_running' } } }),
    },
    {
      name: 'identity mismatch',
      request: async () => ({ statusCode: 200, payload: { service: 'foreign', runtime: 'foreign', version: '1.2.0' } }),
    },
  ]) {
    let tick = 0;
    let stops = 0;
    const manager = new WslManager(baseOptions({
      request: scenario.request,
      sleep: async () => {},
      now: () => tick++ * 50,
      proxyFactory: () => ({
        start: async () => 43193,
        stop: async () => { stops += 1; },
        diagnostics: () => '',
      }),
    }));
    manager.proxy = {
      stop: async () => { stops += 1; },
      diagnostics: () => '',
    };
    manager.proxyLocalPort = 43193;
    manager.proxyBackendPort = manager.backendPort;
    manager.serviceBaseUrl = 'http://127.0.0.1:43193';
    await manager.writeState({ backendPort: manager.backendPort, localPort: 43193, usesLoopbackProxy: true });

    await assert.rejects(
      manager.waitForReady({ timeoutMs: 100, intervalMs: 1 }),
      (error) => error instanceof WslManagerError && error.code === 'health_timeout',
      scenario.name,
    );
    const state = await manager.readState();
    assert.equal(stops, 1, scenario.name);
    assert.equal(manager.proxy, null, scenario.name);
    assert.equal(manager.serviceBaseUrl, '', scenario.name);
    assert.equal(manager.proxyLocalPort, null, scenario.name);
    assert.equal(manager.proxyBackendPort, null, scenario.name);
    assert.equal(manager.hasReusableProxyForBackend(manager.backendPort), false, scenario.name);
    assert.equal(state.localPort, null, scenario.name);
    assert.equal(state.usesLoopbackProxy, false, scenario.name);
  }
});

test('a proxy stop that is not confirmed blocks a subsequent backend start', async () => {
  let stopAttempts = 0;
  const calls = [];
  const proxy = {
    stop: async () => {
      stopAttempts += 1;
      throw new Error('helper exit was not confirmed');
    },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
  }));
  manager.proxy = proxy;
  manager.proxyLocalPort = 43194;
  manager.proxyBackendPort = manager.backendPort;
  manager.serviceBaseUrl = 'http://127.0.0.1:43194';

  await assert.rejects(
    manager.discardLocalProxy({ clearPersistedEndpoint: true }),
    (error) => error instanceof WslManagerError && error.code === 'port_in_use',
  );
  assert.equal(manager.proxyStopPending, true);

  await assert.rejects(
    manager.start(),
    (error) => error instanceof WslManagerError && error.code === 'port_in_use',
  );
  assert.equal(stopAttempts, 2, 'start must retry the owned proxy stop before touching WSL');
  assert.equal(calls.some((call) => call.args.at(-1) === 'start'), false);
  assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), false);
  assert.equal(manager.proxy, proxy);
});

test('a pending proxy stop is latched before await and concurrent callers share one stop', async () => {
  let releaseStop;
  let stopEntered;
  const entered = new Promise((resolve) => { stopEntered = resolve; });
  const stopped = new Promise((resolve) => { releaseStop = resolve; });
  let stopCalls = 0;
  const proxy = {
    stop: async () => {
      stopCalls += 1;
      stopEntered();
      await stopped;
    },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    backendPort: 43196,
    request: async () => HEALTH,
  }));
  manager.proxy = proxy;
  manager.proxyLocalPort = 54396;
  manager.proxyBackendPort = manager.backendPort;
  manager.serviceBaseUrl = 'http://127.0.0.1:54396';

  const first = manager.discardLocalProxy({ clearPersistedEndpoint: true });
  await entered;
  assert.equal(manager.proxyStopPending, true, 'stop admission must close before proxy.stop awaits');
  const joined = manager.ensureProxyStopConfirmed();
  let joinedSettled = false;
  void joined.finally(() => { joinedSettled = true; });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(joinedSettled, false, 'a concurrent caller must join the unresolved stop');

  releaseStop();
  await Promise.all([first, joined]);
  assert.equal(stopCalls, 1, 'the helper tree must be stopped exactly once');
  assert.equal(manager.proxy, null);
  assert.equal(manager.proxyStopPending, false);
});

test('update quiesces an owned proxy before submitting update-stdin', async () => {
  const calls = [];
  const events = [];
  let updated = false;
  let generation = 0;
  const runner = healthyRunner({ installed: true, calls });
  const originalInput = runner.input;
  runner.input = async (command, args, input, options) => {
    if (args.at(-1) === 'update-stdin') {
      events.push('update-stdin');
      updated = true;
    }
    return originalInput(command, args, input, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async (url) => {
      if (url.endsWith('/api/v1/jobs?active=true&limit=100')) {
        return { statusCode: 200, payload: { jobs: [] } };
      }
      const version = updated ? '1.2.1' : '1.2.0';
      return { statusCode: 200, payload: { ...HEALTH.payload, version } };
    },
    proxyFactory: () => {
      const id = ++generation;
      return {
        start: async () => { events.push(`start:${id}`); return 43195 + id; },
        stop: async () => { events.push(`stop:${id}`); },
        diagnostics: () => '',
      };
    },
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager);
  await manager.prepareLocalEndpoint();
  events.length = 0;
  calls.length = 0;

  const status = await manager.update(createManifest({ version: '1.2.1' }), { force: true });
  assert.equal(status.version, '1.2.1');
  const updateIndex = calls.findIndex((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.equal(updateIndex >= 0, true);
  assert.equal(events.includes('stop:1'), true);
  // The old listener must be stopped before update-stdin is handed to WSL.
  assert.equal(events.indexOf('stop:1') < events.indexOf('update-stdin'), true);
  assert.equal(generation >= 2, true);
});

test('pending proxy ownership blocks forced update before update-stdin', async () => {
  const calls = [];
  const proxy = {
    stop: async () => { throw new Error('helper exit was not confirmed'); },
    diagnostics: () => '',
  };
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
  }));
  manager.proxy = proxy;
  manager.proxyStopPending = true;

  await assert.rejects(
    manager.update(createManifest({ version: '1.2.1' }), { force: true }),
    (error) => error instanceof WslManagerError && error.code === 'port_in_use',
  );
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
  assert.equal(manager.proxy, proxy);
  assert.equal(manager.proxyStopPending, true);
});

test('update refuses active jobs and delegates the complete transaction to update-stdin', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [{ id: '1', status: 'running' }] } }
      : {
        statusCode: 200,
        payload: {
          ...HEALTH.payload,
          version: calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin') ? '1.2.1' : '1.2.0',
        },
      },
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  const manifest = createManifest({ version: '1.2.1' });
  await seedInstalledState(manager);
  await assert.rejects(manager.update(manifest), (error) => error.code === 'active_jobs');
  await manager.update(manifest, { force: true });
  const updateCall = calls.find((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.ok(updateCall);
  assert.equal(updateCall.options.timeoutMs, UPDATE_TIMEOUT_MS);
  const updateDocument = JSON.parse(updateCall.input.toString('utf8'));
  assert.match(updateDocument.transactionId, /^[a-f0-9]{64}$/);
  assert.equal(updateDocument.sha256, manifest.runtime.sha256);
  const updatedState = await manager.readState();
  assert.equal(updatedState.installedRuntimeSha256, manifest.runtime.sha256);
  assert.equal(updatedState.installedRuntimeTransactionId, updateDocument.transactionId);
  assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), false);
  assert.equal(calls.some((call) => call.type === 'input'
    && call.args.at(-1) === 'update-stdin'
    && JSON.parse(call.input.toString('utf8')).mode === 'rollback'), false);
  assert.equal(calls.some((call) => call.args.includes('--unregister')), false);
});

test('a 1.2.13 incident fails closed before recovery resources, bridge admission, artifact download, or update', async (t) => {
  const calls = [];
  const source = createInteropIncidentManifest();
  const target = createManifest({ version: '1.2.14' });
  const resources = createInteropRecoveryResources(t);
  let downloads = 0;
  const runner = healthyRunner({
    installed: true,
    calls,
    initialServiceState: 'stopped',
    initialNetworkState: 'unprepared',
    runtimeVersion: source.version,
    runtimeSha256: source.runtime.sha256,
    statusIdentity: incidentSupervisorIdentity({ windowsHelperSha256: '' }),
  });
  attachInteropRecoveryResourceProbes(runner, calls, resources);
  const manager = new WslManager(baseOptions({
    runner,
    windowsProbeExe: resources.windowsHelperPath,
    interopBootstrapExe: resources.bootstrapPath,
    windowsHelperBindingProvider: () => resources.descriptor.windowsHelper,
    request: async (url) => {
      if (url.endsWith('/api/v1/jobs?active=true&limit=100')) {
        throw new Error('a stopped legacy runtime must not be started to query jobs');
      }
      const updated = calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
      return { statusCode: 200, payload: { ...HEALTH.payload, version: updated ? target.version : source.version } };
    },
    downloadArtifact: async (_artifact, destination) => { downloads += 1; return destination; },
  }));
  await seedInstalledState(manager, source);

  await assert.rejects(
    manager.update(target, { interopRecovery: resources.descriptor }),
    (error) => error instanceof WslManagerError && error.code === 'runtime_reinstall_required',
  );

  assert.equal(downloads, 0);
  assert.equal(calls.some((call) => call.command === resources.windowsHelperPath && call.args[0] === 'canonicalize-file'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'network-preflight'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'start'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
  assert.equal(calls.some((call) => call.args.includes('--unregister')), false);
});

test('a busy running 1.2.13 incident still fails closed at the reinstall gate', async () => {
  const calls = [];
  const source = createInteropIncidentManifest();
  const target = createManifest({ version: '1.2.14' });
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({
      installed: true,
      calls,
      runtimeVersion: source.version,
      runtimeSha256: source.runtime.sha256,
      statusIdentity: incidentSupervisorIdentity(),
    }),
    request: async () => {
      throw new Error('the legacy reinstall gate must run before querying active jobs');
    },
  }));
  await seedInstalledState(manager, source);

  await assert.rejects(
    manager.update(target),
    (error) => error instanceof WslManagerError && error.code === 'runtime_reinstall_required',
  );

  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'network-preflight'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'start'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
});

test('a ready running 1.2.13 incident is not sent to update-stdin', async () => {
  const calls = [];
  const source = createInteropIncidentManifest();
  const target = createManifest({ version: '1.2.14' });
  const runner = healthyRunner({
    installed: true,
    calls,
    runtimeVersion: source.version,
    runtimeSha256: source.runtime.sha256,
    statusIdentity: incidentSupervisorIdentity(),
  });
  const manager = new WslManager(baseOptions({
    runner,
    request: async (url) => {
      if (url.endsWith('/api/v1/jobs?active=true&limit=100')) {
        return { statusCode: 200, payload: { jobs: [] } };
      }
      const updated = calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
      return { statusCode: 200, payload: { ...HEALTH.payload, version: updated ? target.version : source.version } };
    },
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager, source);

  await assert.rejects(
    manager.update(target),
    (error) => error instanceof WslManagerError && error.code === 'runtime_reinstall_required',
  );

  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'network-prepare-stdin'), false);
  assert.equal(calls.some((call) => call.type === 'exec' && call.args.at(-1) === 'network-preflight'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
});

test('a stopped 1.2.12 runtime is updated before any unsupported network operation or old backend start', async () => {
  const calls = [];
  const source = createManifest({ version: '1.2.12' });
  const target = createManifest({ version: '1.2.14' });
  const runner = healthyRunner({
    installed: true,
    calls,
    initialServiceState: 'stopped',
    initialNetworkState: 'unprepared',
    runtimeVersion: source.version,
    runtimeSha256: source.runtime.sha256,
  });
  const manager = new WslManager(baseOptions({
    runner,
    request: async (url) => {
      if (url.endsWith('/api/v1/jobs?active=true&limit=100')) {
        throw new Error('a stopped 1.2.12 runtime must not be queried through a new backend');
      }
      const updated = calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
      return { statusCode: 200, payload: { ...HEALTH.payload, version: updated ? target.version : source.version } };
    },
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager, source);

  const status = await manager.update(target);

  assert.equal(status.version, target.version);
  const updateIndex = calls.findIndex((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.equal(updateIndex >= 0, true);
  assert.equal(calls.slice(0, updateIndex).some((call) => call.type === 'input'
    && call.args.at(-1) === 'network-prepare-stdin'), false);
  assert.equal(calls.slice(0, updateIndex).some((call) => call.type === 'exec'
    && call.args.at(-1) === 'network-preflight'), false);
  assert.equal(calls.slice(0, updateIndex).some((call) => call.type === 'exec'
    && call.args.at(-1) === 'start'), false);
  assert.equal(calls.slice(updateIndex + 1).some((call) => call.type === 'input'
    && call.args.at(-1) === 'network-prepare-stdin'), true);
  assert.equal(calls.slice(updateIndex + 1).some((call) => call.type === 'exec'
    && call.args.at(-1) === 'start'), true);
});

for (const failure of ['', 'preflight', 'rollback']) {
test(`stopped modern runtime preserves upgrade admission and state (${failure || 'success'})`, async () => {
  const calls = [];
  const source = createManifest({ version: '1.3.4' });
  const target = createManifest({ version: '1.4.3' });
  const runner = healthyRunner({ installed: true, calls, initialServiceState: 'stopped',
    initialNetworkState: 'unprepared', runtimeVersion: source.version, runtimeSha256: source.runtime.sha256 });
  for (const method of ['exec', 'input']) {
    const original = runner[method];
    runner[method] = async (...args) => {
      if (failure === 'preflight' && args[1].at(-1) === 'network-preflight') {
        calls.push({ type: method, args: args[1] });
        throw new Error('fixture preflight rejected');
      }
      if (failure === 'rollback' && args[1].at(-1) === 'update-stdin') {
        calls.push({ type: method, args: args[1] });
        const response = await runner.exec('wsl', ['status']);
        const error = new Error('fixture update rejected');
        error.controlStatus = JSON.parse(response.stdout);
        error.controlStatus.lastUpdateOutcome = 'rolled_back';
        error.controlStatus.lastUpdateTransactionId = JSON.parse(args[2].toString()).transactionId;
        throw error;
      }
      const result = await original(...args);
      try {
        const status = JSON.parse(result.stdout);
        if (status.network) {
          Object.assign(status.network, { schemaVersion: 2, caSource: 'bundled_v1', curlCode: 0, sslVerifyResult: 0 });
          return { ...result, stdout: JSON.stringify(status) };
        }
      } catch { /* Non-JSON system command output. */ }
      return result;
    };
  }
  const manager = new WslManager(baseOptions({ runner,
    request: async () => ({ statusCode: 200, payload: { ...HEALTH.payload, version: target.version } }),
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  let credentialsRead = 0;
  manager.credentialProvider = () => { credentialsRead += 1; throw new Error('credentials must remain deferred'); };
  await seedInstalledState(manager, source);
  if (failure) {
    await assert.rejects(manager.update(target));
    assert.equal(credentialsRead, 0);
    assert.equal(calls.some(call => ['credential-stdin', 'start'].includes(call.args.at(-1))), false);
    assert.equal((await manager.readState()).installedVersion, source.version);
    assert.equal((await manager.readState()).upgradeProgress.phase,
      failure === 'preflight' ? 'network' : 'reconciling');
    if (failure === 'preflight') {
      assert.equal(calls.some(call => call.args.at(-1) === 'update-stdin'), false);
      assert.equal(Boolean((await manager.readState()).updateTransaction), false);
    }
    return;
  }
  manager.credentialProvider = null;
  await manager.update(target);
  assert.deepEqual((await manager.readState()).upgradeProgress, {
    phase: 'complete', fromVersion: source.version, toVersion: target.version,
  });
  const submitted = calls.findIndex(call => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.ok(submitted >= 0);
  const before = calls.slice(0, submitted);
  const prepared = before.findIndex(call => call.args.at(-1) === 'network-prepare-stdin');
  const preflight = before.findIndex(call => call.args.at(-1) === 'network-preflight');
  assert.ok(prepared >= 0, 'old supervisor must receive network preparation before update');
  assert.ok(preflight > prepared, 'native preflight must follow preparation before update');
  assert.equal(before.some(call => ['credential-stdin', 'start'].includes(call.args.at(-1))), false);
});
}

test('ordinary update re-proves ownership immediately before update-stdin submission', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  let ownershipReads = 0;
  runner.exec = async (command, args, options) => {
    if (args.includes('/usr/bin/cat') && args.includes('/var/lib/applemusic-downloader/desktop-owner-id')) {
      ownershipReads += 1;
      if (ownershipReads >= 2) {
        calls.push({ type: 'exec', command, args: [...args], options });
        return { stdout: 'f'.repeat(64), stderr: '' };
      }
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => ({ statusCode: 200, payload: { jobs: [] } }),
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager);
  calls.length = 0;
  ownershipReads = 0;

  await assert.rejects(
    manager.update(createManifest({ version: '1.2.1' }), { force: true }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
  assert.equal((await manager.readState()).updateTransaction ?? null, null);
});

test('update rejects a caller-supplied malformed transaction identity before caching or control input', async () => {
  const calls = [];
  const downloaded = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async () => ({ statusCode: 200, payload: { jobs: [] } }),
    downloadArtifact: async (artifact, destination) => { downloaded.push({ artifact, destination }); return destination; },
  }));
  await seedInstalledState(manager);

  await assert.rejects(
    manager.update(createManifest({ version: '1.2.1' }), { force: true, transactionId: 'not-a-transaction' }),
    (error) => error.code === 'update_failed',
  );

  assert.equal(downloaded.length, 0);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
});

test('update rejects a restarted supervisor that does not report the manifest target version', async () => {
  const calls = [];
  let tick = 0;
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    const result = await originalExec(command, args, options);
    if (args.at(-1) === 'status' && calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin')) {
      const wrong = JSON.parse(result.stdout);
      wrong.version = '1.2.0';
      return { ...result, stdout: JSON.stringify(wrong) };
    }
    return result;
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
    sleep: async () => {},
    now: () => { tick += 30_000; return tick; },
  }));
  const manifest = createManifest({ version: '1.2.1' });
  await seedInstalledState(manager);

  await assert.rejects(
    manager.update(manifest, { force: true }),
    (error) => error instanceof WslManagerError && error.code === 'health_timeout' && /does not match 1\.2\.1/.test(error.details.reason),
  );
  const state = await manager.readState();
  assert.equal(state.installedVersion, '1.2.0');
  assert.equal(state.updateTransaction.toVersion, '1.2.1');
});

test('a rolled-back update restores a stopped backend through the full network and credential startup path', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  const originalInput = runner.input;
  let updateDocument;
  runner.input = async (command, args, input, options) => {
    if (args.at(-1) !== 'update-stdin') return originalInput(command, args, input, options);
    calls.push({ type: 'input', command, args: [...args], input: Buffer.from(input), options });
    updateDocument = JSON.parse(input.toString('utf8'));
    await originalExec(command, [
      '--distribution', 'AppleMusicDownloader',
      '--exec', '/opt/applemusic-downloader/current/amdl-appliance', 'stop',
    ], options);
    const error = new Error('synthetic rolled-back update response');
    error.stdout = JSON.stringify({
      ok: false,
      status: {
        service: 'applemusic-downloader',
        runtime: 'wsl-appliance',
        version: '1.2.0',
        runtimeSha256: 'b'.repeat(64),
        runtimeTransactionId: INITIAL_RUNTIME_TRANSACTION,
        updateInProgress: false,
        state: 'stopped',
        supervisorPid: 9100,
        lastUpdateTransactionId: updateDocument.transactionId,
        lastUpdateOutcome: 'rolled_back',
      },
      error: {
        code: 'update_failed',
        message: 'The synthetic update rolled back.',
        action: 'Restore the source runtime.',
      },
    });
    throw error;
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    credentialProvider: async () => ({ applePassword: 'rollback-restart-secret' }),
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  const sourceManifest = createManifest();
  const targetManifest = createManifest({ version: '1.2.1' });
  delete sourceManifest.inputs.networkPreflightSourceSha256;
  delete targetManifest.inputs.networkPreflightSourceSha256;
  await seedInstalledState(manager, sourceManifest);

  await assert.rejects(
    manager.update(targetManifest, { force: true }),
    (error) => error instanceof WslManagerError && error.code === 'update_failed',
  );

  const updateIndex = calls.findIndex((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  const prepareIndex = calls.findIndex((call, index) => index > updateIndex
    && call.type === 'input'
    && call.args.at(-1) === 'network-prepare-stdin');
  const preflightIndex = calls.findIndex((call, index) => index > prepareIndex
    && call.type === 'exec'
    && call.args.at(-1) === 'network-preflight');
  const credentialIndex = calls.findIndex((call, index) => index > preflightIndex
    && call.type === 'input'
    && call.args.at(-1) === 'credential-stdin');
  const startIndex = calls.findIndex((call, index) => index > credentialIndex
    && call.type === 'exec'
    && call.args.at(-1) === 'start');
  assert.equal(prepareIndex > updateIndex, true);
  assert.equal(preflightIndex > prepareIndex, true);
  assert.equal(credentialIndex > preflightIndex, true);
  assert.equal(startIndex > credentialIndex, true);
  assert.equal(JSON.stringify(calls.map((call) => call.args)).includes('rollback-restart-secret'), false);
});

test('post-update restart does not resolve credentials when native network preflight fails', async () => {
  const calls = [];
  let credentialProviderCalls = 0;
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'network-preflight') {
      calls.push({ type: 'exec', command, args: [...args], options });
      const error = new Error('fixed post-update preflight failure');
      error.stdout = JSON.stringify({
        ok: false,
        status: networkControlStatus(NETWORK_GENERATION, {
          state: 'failed',
          stage: 'tls',
          windowsPath: 'unreachable',
          proxyPropagated: false,
        }),
        error: {
          code: 'network_preflight_failed',
          message: 'WSL network preflight failed.',
          action: 'Retry the supported network path.',
        },
      });
      throw error;
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => HEALTH,
    credentialProvider: async () => {
      credentialProviderCalls += 1;
      return { applePassword: 'post-update-secret' };
    },
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager);

  await assert.rejects(
    manager.update(createManifest({ version: '1.2.1' }), { force: true }),
    (error) => error instanceof WslManagerError && error.code === 'network_preflight_failed',
  );

  assert.equal(credentialProviderCalls, 0);
  const updateIndex = calls.findIndex((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.equal(updateIndex >= 0, true);
  assert.equal(calls.slice(updateIndex + 1).some((call) => call.type === 'input'
    && call.args.at(-1) === 'credential-stdin'), false);
});

test('post-commit Windows endpoint failure never sends a second rollback transaction', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async () => { throw new Error('localhost forwarding unavailable'); },
    proxyFactory: () => ({
      start: async () => { throw new Error('proxy unavailable'); },
      stop: async () => {},
      diagnostics: () => '',
    }),
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  const manifest = createManifest({ version: '1.2.3' });
  await seedInstalledState(manager);

  await assert.rejects(manager.update(manifest, { force: true }), (error) => ['update_failed', 'port_in_use'].includes(error.code));
  const interruptedState = await manager.readState();
  assert.equal(interruptedState.installedVersion, '1.2.0');
  assert.equal(interruptedState.updateTransaction.toVersion, '1.2.3');
  const updateDocuments = calls
    .filter((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin')
    .map((call) => JSON.parse(call.input.toString('utf8')));
  assert.deepEqual(updateDocuments.map((document) => document.mode), ['update']);
  assert.equal(calls.some((call) => call.args.at(-1) === 'stop'), false);
});

test('same-version repair reconciles a committed runtime after the update response is lost', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalInput = runner.input;
  let lostResponse = false;
  runner.input = async (command, args, input, options) => {
    const result = await originalInput(command, args, input, options);
    if (!lostResponse && args.at(-1) === 'update-stdin') {
      lostResponse = true;
      throw new Error('synthetic wsl.exe response loss after durable commit');
    }
    return result;
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [] } }
      : HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  const previousManifest = createManifest();
  const repairedManifest = createManifest({
    runtime: {
      ...previousManifest.runtime,
      url: 'https://downloads.example.test/repaired-after-lost-response.tar.gz',
      sha256: 'c'.repeat(64),
    },
  });
  await seedInstalledState(manager, previousManifest);
  await seedOwnership(manager);

  const status = await manager.repair(repairedManifest);

  assert.equal(status.runtimeSha256, repairedManifest.runtime.sha256);
  const updateCalls = calls.filter((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.equal(updateCalls.length, 1);
  const transactionId = JSON.parse(updateCalls[0].input.toString('utf8')).transactionId;
  const state = await manager.readState();
  assert.equal(state.manifest.runtime.sha256, repairedManifest.runtime.sha256);
  assert.equal(state.installedRuntimeTransactionId, transactionId);
  assert.equal(state.updateTransaction, null);
  assert.equal(state.pendingManifest, null);
});

test('transport reconciliation never mistakes an in-progress source identity for rollback', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  const originalInput = runner.input;
  let updateDocument = null;
  let statusPolls = 0;
  let committed = false;
  runner.input = async (_command, args, input) => {
    if (args.at(-1) === 'update-stdin') {
      updateDocument = JSON.parse(input.toString('utf8'));
      throw new Error('synthetic client disconnect while supervisor continues');
    }
    return originalInput(_command, args, input);
  };
  runner.exec = async (command, args, options) => {
    if (updateDocument && args.at(-1) === 'status') {
      statusPolls += 1;
      if (statusPolls <= 3) {
        const result = await originalExec(command, args, options);
        const status = JSON.parse(result.stdout);
        status.updateInProgress = true;
        status.pendingTransactionId = updateDocument.transactionId;
        status.updatePhase = 'activating';
        return { ...result, stdout: JSON.stringify(status) };
      }
      if (!committed) {
        runner.commitUpdate(updateDocument);
        committed = true;
      }
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => ({ statusCode: 200, payload: { ...HEALTH.payload, version: '1.2.1' } }),
    downloadArtifact: async (_artifact, destination) => destination,
    sleep: async () => {},
  }));
  await seedInstalledState(manager);

  await manager.update(createManifest({ version: '1.2.1' }), { force: true });

  assert.equal(statusPolls >= 4, true);
  const state = await manager.readState();
  assert.equal(state.installedVersion, '1.2.1');
  assert.equal(state.installedRuntimeTransactionId, updateDocument.transactionId);
  assert.equal(state.updateTransaction, null);
});

test('transport loss before a queued update begins never treats a bare source identity as rollback', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalInput = runner.input;
  runner.input = async (command, args, input, options) => {
    if (args.at(-1) === 'update-stdin') {
      throw new Error('synthetic transport loss while request is queued behind lifecycle lock');
    }
    return originalInput(command, args, input, options);
  };
  let tick = 0;
  const manager = new WslManager(baseOptions({
    runner,
    request: async () => ({ statusCode: 200, payload: { jobs: [] } }),
    downloadArtifact: async (_artifact, destination) => destination,
    sleep: async () => {},
    now: () => { tick += 60_000; return tick; },
  }));
  await seedInstalledState(manager);

  await assert.rejects(
    manager.update(createManifest({ version: '1.2.1' }), { force: true }),
    (error) => error instanceof WslManagerError
      && error.code === 'update_failed'
      && error.details.updateStatePreserved === true,
  );
  const state = await manager.readState();
  assert.equal(state.updateTransaction.toVersion, '1.2.1');
  assert.ok(state.pendingManifest);
});

test('repair refuses to interrupt an active queue', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [{ id: 'running-1', status: 'running' }] } }
      : HEALTH,
  }));
  const manifest = createManifest();
  await seedInstalledState(manager, manifest);
  await seedOwnership(manager);
  await assert.rejects(manager.repair(), (error) => error.code === 'active_jobs');
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
});

test('repair leaves a stopped backend down until update-stdin owns maintenance admission', async () => {
  const calls = [];
  let activeJobRequests = 0;
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls, initialServiceState: 'stopped' }),
    request: async (url) => {
      if (url.endsWith('/api/v1/jobs?active=true&limit=100')) {
        activeJobRequests += 1;
        throw new Error('a stopped backend must not be started merely to query jobs');
      }
      return HEALTH;
    },
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager);
  await seedOwnership(manager);

  await manager.repair();

  const updateIndex = calls.findIndex((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.equal(updateIndex >= 0, true);
  assert.equal(activeJobRequests, 0);
  assert.equal(calls.slice(0, updateIndex).some((call) => call.type === 'exec' && call.args.at(-1) === 'start'), false);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'credential-stdin'), false);
  assert.equal(calls.slice(updateIndex + 1).some((call) => call.type === 'exec' && call.args.at(-1) === 'start'), true);
});

test('repair accepts the current packaged manifest for a forced same-version runtime replacement', async () => {
  const calls = [];
  const downloaded = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [] } }
      : HEALTH,
    downloadArtifact: async (artifact, destination) => { downloaded.push(artifact); return destination; },
  }));
  const oldManifest = createManifest();
  const currentManifest = createManifest({
    runtime: {
      ...oldManifest.runtime,
      url: 'https://downloads.example.test/rebuilt-same-version-runtime.tar.gz',
      sha256: 'c'.repeat(64),
    },
  });
  await seedInstalledState(manager, oldManifest);
  await seedOwnership(manager);

  await manager.repair(currentManifest);

  const updateCall = calls.find((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  const document = JSON.parse(updateCall.input.toString('utf8'));
  assert.equal(document.version, '1.2.0');
  assert.equal(downloaded[0].url, 'https://downloads.example.test/rebuilt-same-version-runtime.tar.gz');
  assert.equal((await manager.readState()).manifest.runtime.sha256, 'c'.repeat(64));
});

test('concurrent repair requests share one lifecycle transaction across manager instances', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const appDataDir = tempAppData();
  const statePath = path.join(appDataDir, 'appliance-state.json');
  const oldManifest = createManifest();
  const repairedManifest = createManifest({
    runtime: {
      ...oldManifest.runtime,
      url: 'https://downloads.example.test/concurrent-repair-runtime.tar.gz',
      sha256: 'c'.repeat(64),
    },
  });
  const sharedOptions = {
    appDataDir,
    statePath,
    runner,
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [] } }
      : HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
  };
  const first = new WslManager(baseOptions(sharedOptions));
  const second = new WslManager(baseOptions(sharedOptions));
  await seedInstalledState(first, oldManifest);
  await seedOwnership(first);

  const [firstStatus, secondStatus] = await Promise.all([
    first.repair(repairedManifest),
    second.repair(repairedManifest),
  ]);

  assert.equal(firstStatus.runtimeSha256, repairedManifest.runtime.sha256);
  assert.equal(secondStatus.runtimeSha256, repairedManifest.runtime.sha256);
  const updateCalls = calls.filter((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin');
  assert.equal(updateCalls.length, 1);
  const state = await first.readState();
  assert.equal(state.updateTransaction, null);
  assert.equal(state.installedRuntimeSha256, repairedManifest.runtime.sha256);
});

test('repair rejects missing ownership before update submission', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [] } }
      : HEALTH,
  }));
  await seedInstalledState(manager, createManifest(), { ownership: false });

  await assert.rejects(manager.repair(), (error) => error.code === 'distro_name_conflict');
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
  assert.equal((await manager.readState()).updateTransaction ?? null, null);
});

test('repair re-proves ownership immediately before submission and rejects a changed marker', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec.bind(runner);
  let ownershipReads = 0;
  runner.exec = async (command, args, options) => {
    if (args.includes('/usr/bin/cat') && args.includes('/var/lib/applemusic-downloader/desktop-owner-id')) {
      ownershipReads += 1;
      if (ownershipReads >= 2) {
        calls.push({ type: 'exec', command, args: [...args], options });
        return { stdout: 'f'.repeat(64), stderr: '' };
      }
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    request: async (url) => url.endsWith('/api/v1/jobs?active=true&limit=100')
      ? { statusCode: 200, payload: { jobs: [] } }
      : HEALTH,
    downloadArtifact: async (_artifact, destination) => destination,
  }));
  await seedInstalledState(manager);
  await seedOwnership(manager);

  await assert.rejects(manager.repair(), (error) => error.code === 'distro_name_conflict');
  assert.equal(ownershipReads, 2);
  assert.equal(calls.some((call) => call.type === 'input' && call.args.at(-1) === 'update-stdin'), false);
  const state = await manager.readState();
  assert.equal(state.updateTransaction ?? null, null);
  assert.equal(state.pendingManifest ?? null, null);
});

test('uninstall preserves the distro by default and unregisters only after explicit data deletion', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await seedOwnership(manager);
  assert.deepEqual(await manager.uninstall(), { preserved: true, unregistered: false });
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
  assert.deepEqual(await manager.uninstall({ preserveData: false }), { preserved: false, unregistered: true });
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 1);
  assert.equal(calls.some((call) => call.args[0] === '--shutdown'), false);
});

test('uninstall never unregisters a same-name distro without matching host and distro ownership identities', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await assert.rejects(
    manager.uninstall({ preserveData: false }),
    (error) => error.code === 'distro_name_conflict' || error.code === 'distro_corrupt',
  );
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);

  await seedOwnership(manager, 'e'.repeat(64));
  fs.writeFileSync(manager.ownershipPath, 'f'.repeat(64));
  await assert.rejects(manager.uninstall({ preserveData: false }), (error) => error.code === 'distro_name_conflict');
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('unregister re-proves ownership after the durable journal and before terminate', async () => {
  const calls = [];
  let replacementAppeared = false;
  const options = baseOptions({
    runner: healthyRunner({ installed: true, calls }),
    registrationIdentityProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      basePath: replacementAppeared ? 'C:\\replacement\\AppleMusicDownloader' : options.installDir,
      wslVersion: 2,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const prepare = manager.prepareUnregisterJournal.bind(manager);
  manager.prepareUnregisterJournal = async () => {
    await prepare();
    replacementAppeared = true;
  };

  await assert.rejects(
    manager.uninstall({ preserveData: false }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
  assert.equal(fs.existsSync(manager.stateBackupPath), true);
  assert.equal(fs.existsSync(manager.statePath), true);
  assert.equal(fs.existsSync(manager.ownershipPath), true);
});

test('unregister rechecks the registration identity after terminate before unregister', async () => {
  const calls = [];
  let replacementAppeared = false;
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--terminate') replacementAppeared = true;
    return originalExec(command, args, options);
  };
  const options = baseOptions({
    runner,
    registrationIdentityProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      basePath: replacementAppeared ? 'C:\\replacement\\AppleMusicDownloader' : options.installDir,
      wslVersion: 2,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);

  await assert.rejects(
    manager.uninstall({ preserveData: false }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(calls.filter((call) => call.args[0] === '--terminate').length, 1);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
  assert.equal(fs.existsSync(manager.stateBackupPath), true);
  assert.equal(fs.existsSync(manager.statePath), true);
  assert.equal(fs.existsSync(manager.ownershipPath), true);
});

test('unregister reports host marker removal failure without deleting ownership state', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await seedOwnership(manager);
  const ownerBefore = fs.readFileSync(manager.ownershipPath, 'utf8');
  const stateBefore = await manager.readState();
  const originalUnlink = fs.promises.unlink;
  fs.promises.unlink = async (target, ...args) => {
    if (path.resolve(target) === path.resolve(manager.ownershipPath)) {
      const error = new Error('ownership unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.unlink = originalUnlink;
  }

  assert.equal(fs.readFileSync(manager.ownershipPath, 'utf8'), ownerBefore);
  const state = JSON.parse(fs.readFileSync(manager.statePath, 'utf8'));
  assert.equal(state.ownership.ownerId, ownerBefore);
  assert.equal(state.stateRevision > stateBefore.stateRevision, true, 'restoration must never reuse a stale state revision');
  await assert.rejects(
    manager.readState(),
    (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
  );
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), true);
});

test('unregister preserves ownership state when state-file deletion fails after WSL removal', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await seedOwnership(manager);
  const ownerBefore = fs.readFileSync(manager.ownershipPath, 'utf8');
  const originalUnlink = fs.promises.unlink;
  fs.promises.unlink = async (target, ...args) => {
    if (path.resolve(target) === path.resolve(manager.statePath)) {
      const error = new Error('state unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.unlink = originalUnlink;
  }

  assert.equal(fs.readFileSync(manager.ownershipPath, 'utf8'), ownerBefore);
  const state = JSON.parse(fs.readFileSync(manager.statePath, 'utf8'));
  assert.equal(state.ownership.ownerId, ownerBefore);
  await assert.rejects(
    manager.readState(),
    (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
  );
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), true);
});

test('a fresh manager resumes a state-unlink cleanup journal without touching the already-unregistered distro', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const options = baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const originalUnlink = fs.promises.unlink;
  let failStateRemoval = true;
  fs.promises.unlink = async (target, ...args) => {
    if (failStateRemoval && path.resolve(target) === path.resolve(manager.statePath)) {
      failStateRemoval = false;
      const error = new Error('state unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.unlink = originalUnlink;
  }

  assert.equal(fs.existsSync(manager.statePath), true);
  assert.equal(fs.existsSync(manager.ownershipPath), true);
  assert.equal(fs.existsSync(manager.stateBackupPath), true);
  await assert.rejects(
    manager.readState(),
    (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
  );
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--list') return { stdout: 'NAME STATE VERSION\n', stderr: '' };
    return originalExec(command, args, options);
  };

  const resumed = new WslManager(baseOptions({
    ...options,
    appDataDir: options.appDataDir,
    installDir: options.installDir,
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
  }));
  assert.deepEqual(await resumed.uninstall({ preserveData: false }), { preserved: false, unregistered: true });
  assert.equal(fs.existsSync(resumed.statePath), false);
  assert.equal(fs.existsSync(resumed.ownershipPath), false);
  assert.equal(fs.existsSync(resumed.stateBackupPath), false);
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 1);
});

test('a fresh manager clears a journal left after host cleanup committed without rerunning unregister', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const options = baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const originalUnlink = fs.promises.unlink;
  let failJournalRemoval = true;
  fs.promises.unlink = async (target, ...args) => {
    if (failJournalRemoval && path.resolve(target) === path.resolve(manager.stateBackupPath)) {
      failJournalRemoval = false;
      const error = new Error('journal unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.unlink = originalUnlink;
  }

  assert.equal(fs.existsSync(manager.statePath), false);
  assert.equal(fs.existsSync(manager.ownershipPath), false);
  assert.equal(fs.existsSync(manager.stateBackupPath), true);
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--list') return { stdout: 'NAME STATE VERSION\n', stderr: '' };
    return originalExec(command, args, options);
  };

  const resumed = new WslManager(baseOptions({
    ...options,
    appDataDir: options.appDataDir,
    installDir: options.installDir,
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
  }));
  assert.deepEqual(await resumed.uninstall({ preserveData: false }), { preserved: false, unregistered: true });
  assert.equal(fs.existsSync(resumed.statePath), false);
  assert.equal(fs.existsSync(resumed.ownershipPath), false);
  assert.equal(fs.existsSync(resumed.stateBackupPath), false);
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 1);
});

test('unregister refuses to terminate or unregister when its prepared journal cannot be persisted', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await seedOwnership(manager);
  const originalRename = fs.promises.rename;
  fs.promises.rename = async (source, target, ...args) => {
    if (path.resolve(target) === path.resolve(manager.stateBackupPath)) {
      const error = new Error('prepared journal rename sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalRename.call(fs.promises, source, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.rename = originalRename;
  }

  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
  assert.equal(fs.existsSync(manager.stateBackupPath), false);
  assert.ok((await manager.readState()).ownership);
});

test('a failed prepared-to-unregistered journal promotion resumes after dual absence proof without a second unregister', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const options = baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const originalRename = fs.promises.rename;
  let journalRenames = 0;
  fs.promises.rename = async (source, target, ...args) => {
    if (path.resolve(target) === path.resolve(manager.stateBackupPath)) {
      journalRenames += 1;
      if (journalRenames === 2) {
        const error = new Error('journal promotion rename sentinel');
        error.code = 'EACCES';
        throw error;
      }
    }
    return originalRename.call(fs.promises, source, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.rename = originalRename;
  }

  const journal = JSON.parse(fs.readFileSync(manager.stateBackupPath, 'utf8'));
  assert.equal(journal.phase, 'prepared');
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--list') return { stdout: 'NAME STATE VERSION\n', stderr: '' };
    return originalExec(command, args, options);
  };
  const resumed = new WslManager(baseOptions({
    ...options,
    appDataDir: options.appDataDir,
    installDir: options.installDir,
    runner,
    registrationPresenceProvider: options.registrationPresenceProvider,
  }));
  assert.deepEqual(await resumed.uninstall({ preserveData: false }), { preserved: false, unregistered: true });
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 1);
  assert.equal(fs.existsSync(resumed.statePath), false);
  assert.equal(fs.existsSync(resumed.ownershipPath), false);
  assert.equal(fs.existsSync(resumed.stateBackupPath), false);
});

test('a prepared journal is cleared only after it re-proves the still-registered owned distro, then retries unregister', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  let unregisterAttempts = 0;
  let registered = true;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--unregister') {
      unregisterAttempts += 1;
      if (unregisterAttempts === 1) throw new Error('unregister transport sentinel');
      registered = false;
    }
    return originalExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered,
      matchCount: registered ? 1 : 0,
    }),
  }));
  await seedOwnership(manager);

  await assert.rejects(
    manager.uninstall({ preserveData: false }),
    (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
  );
  assert.equal(JSON.parse(fs.readFileSync(manager.stateBackupPath, 'utf8')).phase, 'prepared');
  await assert.rejects(
    manager.readState(),
    (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
  );

  assert.deepEqual(await manager.uninstall({ preserveData: false }), { preserved: false, unregistered: true });
  assert.equal(unregisterAttempts, 2);
  assert.equal(fs.existsSync(manager.statePath), false);
  assert.equal(fs.existsSync(manager.ownershipPath), false);
  assert.equal(fs.existsSync(manager.stateBackupPath), false);
});

test('unregister cleanup keeps the state lock across snapshot, marker failure, and restoration', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await seedOwnership(manager);
  const ownerBefore = fs.readFileSync(manager.ownershipPath, 'utf8');
  let releaseStateRemoval;
  let signalStateRemoval;
  const stateRemovalEntered = new Promise((resolve) => { signalStateRemoval = resolve; });
  const stateRemovalReleased = new Promise((resolve) => { releaseStateRemoval = resolve; });
  let releaseMarkerFailure;
  let signalMarkerFailure;
  const markerFailureEntered = new Promise((resolve) => { signalMarkerFailure = resolve; });
  const markerFailureReleased = new Promise((resolve) => { releaseMarkerFailure = resolve; });
  const originalUnlink = fs.promises.unlink;
  fs.promises.unlink = async (target, ...args) => {
    if (path.resolve(target) === path.resolve(manager.statePath)) {
      signalStateRemoval();
      await stateRemovalReleased;
      return originalUnlink.call(fs.promises, target, ...args);
    }
    if (path.resolve(target) === path.resolve(manager.ownershipPath)) {
      signalMarkerFailure();
      await markerFailureReleased;
      const error = new Error('ownership unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  try {
    const uninstall = manager.uninstall({ preserveData: false });
    await stateRemovalEntered;
    const competingWrite = manager.writeState({ concurrentStateWrite: true });
    let competingSettled = false;
    void competingWrite.then(
      () => { competingSettled = true; },
      () => { competingSettled = true; },
    );
    releaseStateRemoval();
    await markerFailureEntered;
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(competingSettled, false, 'a state writer must not enter between the cleanup snapshot and restoration');
    releaseMarkerFailure();
    await assert.rejects(uninstall, (error) => error instanceof WslManagerError);
    await assert.rejects(
      competingWrite,
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.unlink = originalUnlink;
  }

  const state = JSON.parse(fs.readFileSync(manager.statePath, 'utf8'));
  assert.equal(state.ownership.ownerId, ownerBefore);
  assert.equal(state.concurrentStateWrite, undefined);
  assert.equal(fs.readFileSync(manager.ownershipPath, 'utf8'), ownerBefore);
});

test('unregister leaves a durable recovery journal when state restoration fails', async () => {
  const calls = [];
  const manager = new WslManager(baseOptions({ runner: healthyRunner({ installed: true, calls }) }));
  await seedOwnership(manager);
  const ownerBefore = fs.readFileSync(manager.ownershipPath, 'utf8');
  const originalUnlink = fs.promises.unlink;
  const originalRename = fs.promises.rename;
  let markerFailureSeen = false;
  fs.promises.unlink = async (target, ...args) => {
    if (path.resolve(target) === path.resolve(manager.ownershipPath)) {
      markerFailureSeen = true;
      const error = new Error('ownership unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  fs.promises.rename = async (source, target, ...args) => {
    if (markerFailureSeen && path.resolve(target) === path.resolve(manager.statePath)) {
      const error = new Error('state restoration rename sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalRename.call(fs.promises, source, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.unlink = originalUnlink;
    fs.promises.rename = originalRename;
  }

  assert.equal(fs.existsSync(manager.statePath), false);
  assert.equal(fs.readFileSync(manager.ownershipPath, 'utf8'), ownerBefore);
  assert.equal(fs.existsSync(manager.stateBackupPath), true);
  const journal = JSON.parse(fs.readFileSync(manager.stateBackupPath, 'utf8'));
  assert.equal(journal.ownerId, ownerBefore);
  assert.equal(journal.state.ownership.ownerId, ownerBefore);
  await assert.rejects(manager.readState(), (error) => error instanceof WslManagerError && error.code === 'distro_corrupt');
});

test('a fresh manager restores owner-plus-journal state after restoration I/O failure and completes cleanup', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const options = baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const originalUnlink = fs.promises.unlink;
  const originalRename = fs.promises.rename;
  let ownerFailureSeen = false;
  fs.promises.unlink = async (target, ...args) => {
    if (!ownerFailureSeen && path.resolve(target) === path.resolve(manager.ownershipPath)) {
      ownerFailureSeen = true;
      const error = new Error('ownership unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  fs.promises.rename = async (source, target, ...args) => {
    if (ownerFailureSeen && path.resolve(target) === path.resolve(manager.statePath)) {
      const error = new Error('state restoration rename sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalRename.call(fs.promises, source, target, ...args);
  };
  try {
    await assert.rejects(
      manager.uninstall({ preserveData: false }),
      (error) => error instanceof WslManagerError && error.code === 'distro_corrupt',
    );
  } finally {
    fs.promises.unlink = originalUnlink;
    fs.promises.rename = originalRename;
  }

  assert.equal(fs.existsSync(manager.statePath), false);
  assert.equal(fs.existsSync(manager.ownershipPath), true);
  assert.equal(fs.existsSync(manager.stateBackupPath), true);
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--list') return { stdout: 'NAME STATE VERSION\n', stderr: '' };
    return originalExec(command, args, options);
  };
  const resumed = new WslManager(baseOptions({
    ...options,
    appDataDir: options.appDataDir,
    installDir: options.installDir,
    runner,
    registrationPresenceProvider: options.registrationPresenceProvider,
  }));
  assert.deepEqual(await resumed.uninstall({ preserveData: false }), { preserved: false, unregistered: true });
  assert.equal(fs.existsSync(resumed.statePath), false);
  assert.equal(fs.existsSync(resumed.ownershipPath), false);
  assert.equal(fs.existsSync(resumed.stateBackupPath), false);
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 1);
});

test('a re-registered same-name distro blocks pending host cleanup without deleting its journal', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  let reappeared = false;
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--list' && reappeared) {
      return { stdout: 'NAME                   STATE           VERSION\n* AppleMusicDownloader  Stopped         2\n', stderr: '' };
    }
    return originalExec(command, args, options);
  };
  const options = baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: reappeared,
      matchCount: reappeared ? 1 : 0,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const originalUnlink = fs.promises.unlink;
  let failStateRemoval = true;
  fs.promises.unlink = async (target, ...args) => {
    if (failStateRemoval && path.resolve(target) === path.resolve(manager.statePath)) {
      failStateRemoval = false;
      const error = new Error('state unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  try {
    await assert.rejects(manager.uninstall({ preserveData: false }), (error) => error.code === 'distro_corrupt');
  } finally {
    fs.promises.unlink = originalUnlink;
  }
  reappeared = true;

  const resumed = new WslManager(baseOptions({
    ...options,
    appDataDir: options.appDataDir,
    installDir: options.installDir,
    runner,
    registrationPresenceProvider: options.registrationPresenceProvider,
  }));
  await assert.rejects(
    resumed.uninstall({ preserveData: false }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(fs.existsSync(resumed.statePath), true);
  assert.equal(fs.existsSync(resumed.ownershipPath), true);
  assert.equal(fs.existsSync(resumed.stateBackupPath), true);
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 1);
});

test('pending unregister cleanup rechecks registration immediately before host deletion', async () => {
  const calls = [];
  let presenceChecks = 0;
  const runner = healthyRunner({ installed: true, calls });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--list') {
      const reappeared = presenceChecks >= 1;
      return {
        stdout: reappeared
          ? 'NAME                   STATE           VERSION\n* AppleMusicDownloader  Stopped         2\n'
          : 'NAME STATE VERSION\n',
        stderr: '',
      };
    }
    return originalExec(command, args, options);
  };
  const options = baseOptions({
    runner,
    registrationPresenceProvider: async () => {
      presenceChecks += 1;
      const reappeared = presenceChecks >= 2;
      return {
        version: 1,
        distributionName: 'AppleMusicDownloader',
        registered: reappeared,
        matchCount: reappeared ? 1 : 0,
      };
    },
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const ownerId = fs.readFileSync(manager.ownershipPath, 'utf8');
  const snapshot = await manager.readState();
  fs.writeFileSync(manager.stateBackupPath, `${JSON.stringify({
    schemaVersion: 1,
    kind: 'post_unregister_cleanup',
    phase: 'unregistered',
    distroName: 'AppleMusicDownloader',
    ownerId,
    state: snapshot,
  })}\n`, 'utf8');

  await assert.rejects(
    manager.uninstall({ preserveData: false }),
    (error) => error instanceof WslManagerError && error.code === 'distro_name_conflict',
  );
  assert.equal(presenceChecks, 2);
  assert.equal(fs.existsSync(manager.stateBackupPath), true);
  assert.equal(fs.existsSync(manager.statePath), true);
  assert.equal(fs.existsSync(manager.ownershipPath), true);
  assert.equal(calls.some((call) => call.args[0] === '--terminate'), false);
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
});

test('a residual installation directory blocks absent-journal cleanup and preserves all ownership files', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const options = baseOptions({
    runner,
    registrationPresenceProvider: async () => ({
      version: 1,
      distributionName: 'AppleMusicDownloader',
      registered: false,
      matchCount: 0,
    }),
  });
  const manager = new WslManager(options);
  await seedOwnership(manager);
  const originalUnlink = fs.promises.unlink;
  let failStateRemoval = true;
  fs.promises.unlink = async (target, ...args) => {
    if (failStateRemoval && path.resolve(target) === path.resolve(manager.statePath)) {
      failStateRemoval = false;
      const error = new Error('state unlink sentinel');
      error.code = 'EACCES';
      throw error;
    }
    return originalUnlink.call(fs.promises, target, ...args);
  };
  try {
    await assert.rejects(manager.uninstall({ preserveData: false }), (error) => error.code === 'distro_corrupt');
  } finally {
    fs.promises.unlink = originalUnlink;
  }
  fs.mkdirSync(manager.installDir, { recursive: true });
  const originalExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args[0] === '--list') return { stdout: 'NAME STATE VERSION\n', stderr: '' };
    return originalExec(command, args, options);
  };

  const resumed = new WslManager(baseOptions({
    ...options,
    appDataDir: options.appDataDir,
    installDir: options.installDir,
    runner,
    registrationPresenceProvider: options.registrationPresenceProvider,
  }));
  await assert.rejects(
    resumed.uninstall({ preserveData: false }),
    (error) => error instanceof WslManagerError && error.code === 'orphaned_install_state',
  );
  assert.equal(fs.existsSync(resumed.statePath), true);
  assert.equal(fs.existsSync(resumed.ownershipPath), true);
  assert.equal(fs.existsSync(resumed.stateBackupPath), true);
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 1);
});

test('preserving an appliance never reports success when its service cannot be stopped', async () => {
  const calls = [];
  const runner = healthyRunner({ installed: true, calls });
  const healthyExec = runner.exec;
  runner.exec = async (command, args, options) => {
    if (args.at(-1) === 'stop') throw new Error('synthetic stop failure');
    return healthyExec(command, args, options);
  };
  const manager = new WslManager(baseOptions({ runner }));
  await seedOwnership(manager);
  await assert.rejects(manager.uninstall(), (error) => error.code === 'distro_corrupt');
  assert.equal(calls.some((call) => call.args[0] === '--unregister'), false);
  await assert.rejects(manager.uninstall({ preserveData: false }), (error) => error.code === 'distro_corrupt');
  assert.equal(calls.filter((call) => call.args[0] === '--unregister').length, 0);
});

test('diagnostics keeps the latest 200 lines and redacts supplied secrets', async () => {
  const sentinel = 'SENTINEL-diagnostic-secret';
  const runner = {
    exec: async (_command, args) => ({
      stdout: args.at(-1) === 'diagnostics'
        ? `${Array.from({ length: 205 }, (_, index) => `line-${index + 1}`).join('\n')}\n${sentinel}`
        : '{}',
      stderr: '',
    }),
    input: async () => ({ stdout: '', stderr: '' }),
  };
  const manager = new WslManager(baseOptions({ runner }));
  const output = await manager.diagnostics([sentinel]);
  assert.equal(output.includes(sentinel), false);
  assert.equal(output.includes('[REDACTED]'), true);
  assert.equal(output.split(/\r?\n/).length <= 200, true);
  assert.equal(output.includes('line-205'), true);
});

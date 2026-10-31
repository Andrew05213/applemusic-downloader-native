'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

const {
  EXECUTE_ARGUMENT,
  QUIESCE_ARGUMENT,
  REPAIR_STATE_FIELD,
  RepairAuditedProcessRunner,
  RepairSmokeError,
  createRepairWslManager,
  isPermittedRepairVersionTransition,
  loadRepairTargetManifest,
  parseArguments,
  publicRepairError,
  runRepairSmoke,
  stopBackendForRecovery,
} = require('../../electron/run-wsl-repair-smoke');
const { manifestSha256 } = require('../../electron/wsl-manager');
const { INCIDENT_SOURCE } = require('../../electron/wslinterop-recovery');
const { createInputs, createManifest } = require('./fixtures/appliance-manifest-document');

const VERSION = '1.2.0';
const PACKAGE_VERSION = require('../../electron/legacy-desktop-package.json').version;
const DISTRO = 'AppleMusicDownloader';
const OWNER = 'a'.repeat(64);
const SOURCE_TX = 'b'.repeat(64);
const TARGET_TX = 'c'.repeat(64);
const TRUSTED_WSL = 'C:\\Windows\\System32\\wsl.exe';

function digest(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function artifactDocument(base, filename, bytes) {
  return {
    ...base,
    file: path.basename(filename),
    url: pathToFileURL(filename).href,
    sha256: digest(bytes),
    size: bytes.length,
    uncompressedSize: bytes.length,
    compressedBytes: bytes.length,
    uncompressedBytes: bytes.length,
  };
}

function createDistFixture(t) {
  // Hosted Windows TEMP may be a short-path or junction alias. The loader
  // intentionally accepts only canonical paths, including for synthetic inputs.
  const repositoryRoot = fs.realpathSync(
    fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-repair-target-')),
  );
  t.after(() => fs.rmSync(repositoryRoot, { recursive: true, force: true }));
  const dist = path.join(repositoryRoot, 'appliance', 'dist');
  fs.mkdirSync(dist, { recursive: true });
  const rootfsBytes = Buffer.from('synthetic-rootfs');
  const runtimeBytes = Buffer.from('synthetic-runtime');
  const rootfsName = `applemusic-downloader-wsl-rootfs-${PACKAGE_VERSION}-amd64.tar.gz`;
  const runtimeName = `applemusic-downloader-runtime-${PACKAGE_VERSION}-amd64.tar.gz`;
  const rootfsPath = path.join(dist, rootfsName);
  const runtimePath = path.join(dist, runtimeName);
  fs.writeFileSync(rootfsPath, rootfsBytes);
  fs.writeFileSync(runtimePath, runtimeBytes);
  const base = createManifest();
  const manifest = createManifest({
    version: PACKAGE_VERSION,
    channel: 'local-poc',
    rootfs: artifactDocument(base.rootfs, rootfsPath, rootfsBytes),
    runtime: artifactDocument(base.runtime, runtimePath, runtimeBytes),
  });
  const manifestPath = path.join(dist, `applemusic-downloader-wsl-manifest-${PACKAGE_VERSION}-amd64.json`);
  fs.writeFileSync(manifestPath, JSON.stringify(manifest));
  return { repositoryRoot, dist, manifest, manifestPath, runtimePath };
}

function createAudit({ allowTerminate = false } = {}) {
  const calls = [];
  const delegate = {
    exec: async (command, args, options) => {
      calls.push({ kind: 'exec', command, args: [...args], env: { ...(options.env || {}) } });
      return { stdout: '', stderr: '' };
    },
    input: async (command, args, _input, options) => {
      calls.push({ kind: 'stdin', command, args: [...args], env: { ...(options.env || {}) } });
      return { stdout: '', stderr: '' };
    },
    spawn: (command, args, options) => {
      calls.push({ kind: 'spawn', command, args: [...args], env: { ...(options.env || {}) } });
      return { once() {}, unref() {}, exitCode: null, killed: false };
    },
  };
  return { audit: new RepairAuditedProcessRunner(delegate, { allowTerminate }), calls };
}

function managerFixture({
  stateName = 'stopped',
  activeJobs = [],
  hostTransaction = false,
  maliciousRepair = false,
  digestChanged = true,
  configureBeforeUpdate = false,
  allowTerminate = false,
  ownershipMismatchAt = 0,
  updateFailure = '',
} = {}) {
  const source = createManifest({
    runtime: { ...createManifest().runtime, sha256: '1'.repeat(64) },
  });
  const target = createManifest({
    channel: 'local-poc',
    runtime: {
      ...createManifest().runtime,
      sha256: digestChanged ? '2'.repeat(64) : source.runtime.sha256,
    },
  });
  const state = {
    schemaVersion: 1,
    distroName: DISTRO,
    manifest: source,
    installedVersion: VERSION,
    installedRuntimeSha256: source.runtime.sha256,
    installedRuntimeTransactionId: SOURCE_TX,
    installTransaction: null,
    updateTransaction: hostTransaction ? { id: 'pending' } : null,
    pendingManifest: null,
    localPocBootstrap: null,
    ownership: {
      schemaVersion: 1,
      ownerId: OWNER,
      distroName: DISTRO,
      installDir: 'C:\\Users\\Test\\AppData\\Local\\AppleMusicDownloader\\wsl',
      createdAt: '2026-07-29T00:00:00.000Z',
    },
    [REPAIR_STATE_FIELD]: null,
    downloadDir: 'C:\\Users\\Test\\Music\\Apple Music Downloader',
  };
  let status = {
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: VERSION,
    runtimeSha256: source.runtime.sha256,
    runtimeTransactionId: SOURCE_TX,
    updateInProgress: false,
    pendingTransactionId: '',
    configured: true,
    credentialsConfigured: false,
    lastUpdateTransactionId: '',
    lastUpdateOutcome: '',
    state: stateName,
    supervisorPid: 101,
  };
  let updateFailureMode = updateFailure === true ? 'transport_unknown' : updateFailure;
  const { audit, calls } = createAudit({ allowTerminate });
  const counters = {
    repair: 0, update: 0, active: 0, ownership: 0, terminate: 0, umount: 0, start: 0, stop: 0,
  };
  const manager = {
    readState: async () => structuredClone(state),
    writeState: async (patch) => {
      Object.assign(state, structuredClone(patch));
      return structuredClone(state);
    },
    registeredDistro: async () => ({ name: DISTRO, version: 2 }),
    verifyDistroOwnership: async () => {
      counters.ownership += 1;
      return {
        ownerId: ownershipMismatchAt && counters.ownership >= ownershipMismatchAt ? 'd'.repeat(64) : OWNER,
        appliance: { service: 'applemusic-downloader', runtime: 'wsl-appliance', version: status.version },
      };
    },
    status: async () => structuredClone(status),
    prepareLocalEndpoint: async () => {},
    waitForReady: async () => ({ service: 'applemusic-downloader', runtime: 'wsl-appliance', version: VERSION }),
    activeJobs: async () => { counters.active += 1; return activeJobs; },
    start: async () => {
      counters.start += 1;
      status = { ...status, state: 'running' };
      return structuredClone(status);
    },
    ensureKeeper: async () => {},
    waitForSupervisorReady: async () => {},
    runControl: async (operation) => {
      if (operation !== 'stop') throw new Error('unexpected control operation');
      counters.stop += 1;
      status = { ...status, state: 'stopped' };
      return { stdout: JSON.stringify({ status }) };
    },
    runWsl: async (args) => {
      if (!args.includes('--terminate')) throw new Error('unexpected WSL operation');
      counters.terminate += 1;
      return audit.exec(TRUSTED_WSL, args, { env: { PATH: 'safe' } });
    },
    update: async (manifest, options) => {
      counters.update += 1;
      assert.equal(status.state, 'stopped');
      if (options.beforeSubmit) await options.beforeSubmit();
      if (updateFailureMode === 'before_persist') {
        throw Object.assign(new Error('synthetic failure before durable update transaction'), { code: 'update_failed' });
      }
      const transaction = {
        schemaVersion: 2,
        id: options.transactionId,
        mode: 'repair',
        fromVersion: state.installedVersion,
        toVersion: manifest.version,
        fromRuntimeSha256: state.installedRuntimeSha256,
        toRuntimeSha256: manifest.runtime.sha256,
        fromRuntimeTransactionId: state.installedRuntimeTransactionId,
        fromManifestSha256: manifestSha256(state.manifest),
        toManifestSha256: manifestSha256(manifest),
        startedAt: '2026-07-30T00:00:00.000Z',
      };
      state.pendingManifest = manifest;
      state.updateTransaction = transaction;
      if (updateFailureMode === 'transport_unknown') {
        status = {
          ...status,
          updateInProgress: true,
          pendingTransactionId: options.transactionId,
        };
      }
      await audit.input(
        TRUSTED_WSL,
        ['--distribution', DISTRO, '--exec', '/usr/local/bin/amdl-appliance-control', 'update-stdin'],
        Buffer.from('{}'),
        { env: { PATH: 'safe' } },
      );
      if (updateFailureMode === 'transport_unknown') {
        throw Object.assign(new Error('synthetic transport unknown'), { code: 'update_failed' });
      }
      assert.equal(options.force, true);
      assert.equal(options.repair, true);
      assert.match(options.transactionId, /^[a-f0-9]{64}$/);
      assert.notEqual(options.transactionId, SOURCE_TX);
      state.manifest = manifest;
      state.installedVersion = manifest.version;
      state.installedRuntimeSha256 = manifest.runtime.sha256;
      state.installedRuntimeTransactionId = options.transactionId;
      state.updateTransaction = null;
      state.pendingManifest = null;
      status = {
        ...status,
        version: manifest.version,
        runtimeSha256: manifest.runtime.sha256,
        runtimeTransactionId: options.transactionId,
        updateInProgress: false,
        pendingTransactionId: '',
        lastUpdateTransactionId: options.transactionId,
        lastUpdateOutcome: 'committed',
        state: 'running',
        supervisorPid: 202,
      };
      if (updateFailureMode === 'committed_response_loss') {
        throw Object.assign(new Error('synthetic response loss after committed host state'), { code: 'update_failed' });
      }
      return structuredClone(status);
    },
    reconcileUpdateState: async () => {
      if (!state.updateTransaction) return { outcome: 'none', state: structuredClone(state) };
      if (status.updateInProgress === true) {
        return { outcome: 'in_progress', state: structuredClone(state), transaction: structuredClone(state.updateTransaction) };
      }
      if (status.runtimeSha256 === state.updateTransaction.toRuntimeSha256
          && status.runtimeTransactionId === state.updateTransaction.id) {
        state.manifest = state.pendingManifest;
        state.installedVersion = state.updateTransaction.toVersion;
        state.installedRuntimeSha256 = state.updateTransaction.toRuntimeSha256;
        state.installedRuntimeTransactionId = state.updateTransaction.id;
        state.updateTransaction = null;
        state.pendingManifest = null;
        return { outcome: 'committed', state: structuredClone(state) };
      }
      if (status.lastUpdateTransactionId === state.updateTransaction.id
          && status.lastUpdateOutcome === 'rolled_back') {
        state.updateTransaction = null;
        state.pendingManifest = null;
        return { outcome: 'rolled_back', state: structuredClone(state) };
      }
      throw new Error('synthetic unresolved update transaction');
    },
    repair: async (manifest) => {
      counters.repair += 1;
      if (maliciousRepair) {
        await audit.exec(TRUSTED_WSL, ['--terminate', DISTRO], { env: { PATH: 'safe' } });
      }
      if (configureBeforeUpdate) {
        await audit.input(
          TRUSTED_WSL,
          ['--distribution', DISTRO, '--exec', '/usr/local/bin/amdl-appliance-control', 'configure-stdin'],
          Buffer.from('{}'),
          { env: { PATH: 'safe' } },
        );
      }
      await audit.input(
        TRUSTED_WSL,
        ['--distribution', DISTRO, '--exec', '/usr/local/bin/amdl-appliance-control', 'update-stdin'],
        Buffer.from('{}'),
        { env: { PATH: 'safe' } },
      );
      state.manifest = manifest;
      state.installedRuntimeSha256 = manifest.runtime.sha256;
      state.installedRuntimeTransactionId = TARGET_TX;
      status = {
        ...status,
        runtimeSha256: manifest.runtime.sha256,
        runtimeTransactionId: TARGET_TX,
        state: 'running',
        supervisorPid: 202,
      };
      return structuredClone(status);
    },
  };
  return {
    manager,
    source,
    target,
    state,
    status: () => status,
    setStatusState: (value) => { status = { ...status, state: value }; },
    setUpdateFailure: (value) => { updateFailureMode = value; },
    commitPendingRuntime: () => {
      const transaction = state.updateTransaction;
      if (!transaction) throw new Error('no pending update transaction');
      status = {
        ...status,
        runtimeSha256: transaction.toRuntimeSha256,
        runtimeTransactionId: transaction.id,
        updateInProgress: false,
        pendingTransactionId: '',
        lastUpdateTransactionId: transaction.id,
        lastUpdateOutcome: 'committed',
        state: 'running',
        supervisorPid: 202,
      };
    },
    rollBackPendingRuntime: () => {
      const transaction = state.updateTransaction;
      if (!transaction) throw new Error('no pending update transaction');
      status = {
        ...status,
        runtimeSha256: transaction.fromRuntimeSha256,
        runtimeTransactionId: transaction.fromRuntimeTransactionId,
        updateInProgress: false,
        pendingTransactionId: '',
        lastUpdateTransactionId: transaction.id,
        lastUpdateOutcome: 'rolled_back',
        state: 'running',
      };
    },
    audit,
    calls,
    counters,
  };
}

function quiescedRepairOptions(fixture, { mountFailure = false } = {}) {
  const mountCleaner = async () => {
    if (mountFailure) throw new Error('synthetic mount cleanup failure');
    fixture.counters.umount += 1;
  };
  const quiescer = async (manager, _version, {
    ownershipVerifier,
    beforeTerminate,
    mountCleaner: selectedMountCleaner,
  }) => {
    const active = await manager.activeJobs();
    if (active.length !== 0) throw new Error('active jobs');
    await ownershipVerifier('before_stop');
    await manager.runControl('stop');
    await beforeTerminate();
    await ownershipVerifier('before_terminate');
    await manager.runWsl(['--terminate', DISTRO]);
    await manager.ensureKeeper();
    await manager.waitForSupervisorReady();
    await ownershipVerifier('before_mount_cleanup');
    await selectedMountCleaner(manager, VERSION);
    return fixture.status();
  };
  return {
    manager: fixture.manager,
    targetManifest: fixture.target,
    execute: true,
    quiesceLegacyMounts: true,
    audit: fixture.audit,
    credentialsPresent: async () => false,
    migrationLocker: async (_manager, operation) => operation({ held: true }),
    migrationLockVerifier: (lock) => assert.equal(lock.held, true),
    transactionIdFactory: () => TARGET_TX,
    quiescer,
    mountCleaner,
  };
}

test('repair smoke arguments default to preflight and require one explicit execution gate', () => {
  assert.deepEqual(parseArguments(['--manifest', 'C:\\dist\\manifest.json']), {
    help: false, execute: false, quiesceLegacyMounts: false, manifestPath: 'C:\\dist\\manifest.json',
  });
  assert.equal(parseArguments(['--manifest', 'C:\\dist\\manifest.json', EXECUTE_ARGUMENT]).execute, true);
  assert.equal(parseArguments(['--manifest', 'C:\\dist\\manifest.json', EXECUTE_ARGUMENT, QUIESCE_ARGUMENT]).quiesceLegacyMounts, true);
  assert.equal(parseArguments(['--help']).help, true);
  for (const args of [[], [EXECUTE_ARGUMENT], [QUIESCE_ARGUMENT], ['--manifest', 'a', QUIESCE_ARGUMENT], ['--manifest'], ['--manifest', 'a', '--manifest', 'b'], ['--unknown']]) {
    assert.throws(() => parseArguments(args), (error) => error instanceof RepairSmokeError && error.code === 'invalid_arguments');
  }
});

test('private repair admits only an exact same-version target', () => {
  const source = createManifest({
    version: INCIDENT_SOURCE.productVersion,
    productVersion: INCIDENT_SOURCE.productVersion,
    applianceBuildId: INCIDENT_SOURCE.applianceBuildId,
    runtime: { ...createManifest().runtime, sha256: INCIDENT_SOURCE.runtimeSha256 },
    inputs: createInputs({
      androidMainBuiltSha256: INCIDENT_SOURCE.androidMainBuiltSha256,
      rendererSha256: INCIDENT_SOURCE.rendererSha256,
    }),
  });
  const target = createManifest({ version: '1.2.14' });
  const descriptor = {
    schemaVersion: 1,
    targetVersion: target.version,
    source: { ...INCIDENT_SOURCE },
    bootstrap: { file: 'amdl-wslinterop-bootstrap.exe', sha256: 'a'.repeat(64), size: 1 },
    windowsHelper: { file: 'amdl-windows-probe.exe', sha256: 'b'.repeat(64), size: 1 },
  };
  const status = {
    version: INCIDENT_SOURCE.productVersion,
    productVersion: INCIDENT_SOURCE.productVersion,
    applianceBuildId: INCIDENT_SOURCE.applianceBuildId,
    runtimeSha256: INCIDENT_SOURCE.runtimeSha256,
    runtimeTransactionId: SOURCE_TX,
    androidMainBuiltSha256: INCIDENT_SOURCE.androidMainBuiltSha256,
    rendererSha256: INCIDENT_SOURCE.rendererSha256,
    windowsHelperSha256: INCIDENT_SOURCE.windowsHelperSha256,
    network: { state: 'unprepared' },
  };
  const state = {
    installedVersion: INCIDENT_SOURCE.productVersion,
    installedRuntimeSha256: INCIDENT_SOURCE.runtimeSha256,
    installedRuntimeTransactionId: SOURCE_TX,
  };

  assert.equal(isPermittedRepairVersionTransition(source, source), true);
  assert.equal(isPermittedRepairVersionTransition(source, target, status, state, descriptor), false);
  assert.equal(isPermittedRepairVersionTransition(source, createManifest({ version: '1.2.15' })), false);
});

test('recovery service stop uses only supervisor control and never WSL termination', async () => {
  const source = createManifest({ channel: 'local-poc' });
  const state = {
    schemaVersion: 1,
    distroName: DISTRO,
    manifest: source,
    installedVersion: source.version,
    installedRuntimeSha256: source.runtime.sha256,
    installedRuntimeTransactionId: SOURCE_TX,
    installTransaction: null,
    updateTransaction: null,
    pendingManifest: null,
    localPocBootstrap: null,
    [REPAIR_STATE_FIELD]: null,
    ownership: { ownerId: OWNER },
    downloadDir: 'C:\\Users\\Test\\Music\\Apple Music Downloader',
  };
  let status = {
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: source.version,
    runtimeSha256: source.runtime.sha256,
    runtimeTransactionId: SOURCE_TX,
    updateInProgress: false,
    pendingTransactionId: '',
    configured: true,
    credentialsConfigured: false,
    supervisorPid: 101,
    state: 'running',
  };
  const manager = {
    readState: async () => structuredClone(state),
    status: async () => structuredClone(status),
    runControl: async (operation) => {
      assert.equal(operation, 'stop');
      status = { ...status, state: 'stopped' };
      return { stdout: JSON.stringify({ status }) };
    },
  };
  const stopped = await stopBackendForRecovery(manager);
  assert.equal(stopped.status.state, 'stopped');
  assert.equal(status.state, 'stopped');
});

test('target loader accepts only the exact versioned local-poc dist manifest and verifies both artifacts', async (t) => {
  const fixture = createDistFixture(t);
  const loaded = await loadRepairTargetManifest(fixture.repositoryRoot, fixture.manifestPath);
  assert.equal(loaded.manifest.channel, 'local-poc');
  assert.equal(loaded.artifactsVerified, 2);

  const generated = path.join(fixture.repositoryRoot, 'electron', 'generated', 'appliance-manifest.json');
  fs.mkdirSync(path.dirname(generated), { recursive: true });
  fs.copyFileSync(fixture.manifestPath, generated);
  await assert.rejects(
    loadRepairTargetManifest(fixture.repositoryRoot, generated),
    (error) => error.code === 'invalid_target',
  );

  fs.appendFileSync(fixture.runtimePath, 'mutation');
  await assert.rejects(
    loadRepairTargetManifest(fixture.repositoryRoot, fixture.manifestPath),
    (error) => error.code === 'invalid_target',
  );
});

test('default repair smoke is preflight-only and keeps a stopped backend unchanged', async () => {
  const fixture = managerFixture({ stateName: 'stopped' });
  const result = await runRepairSmoke({
    manager: fixture.manager,
    targetManifest: fixture.target,
    audit: fixture.audit,
    credentialsPresent: async () => false,
  });
  assert.equal(result.ok, true);
  assert.equal(result.mode, 'preflight');
  assert.equal(result.checks.queueCheck, 'deferred_until_execute');
  assert.equal(result.checks.repairRequired, true);
  assert.equal(fixture.counters.repair, 0);
  assert.equal(fixture.counters.active, 0);
  assert.equal(result.audit.updateCommandsObserved, 0);
});

test('preflight rejects credentials, host transactions and active jobs before repair', async () => {
  const credentials = managerFixture();
  await assert.rejects(
    runRepairSmoke({ manager: credentials.manager, targetManifest: credentials.target, audit: credentials.audit, credentialsPresent: async () => true }),
    (error) => error.code === 'credentials_present',
  );
  assert.equal(credentials.counters.repair, 0);

  const transaction = managerFixture({ hostTransaction: true });
  await assert.rejects(
    runRepairSmoke({ manager: transaction.manager, targetManifest: transaction.target, audit: transaction.audit }),
    (error) => error.code === 'host_state',
  );
  assert.equal(transaction.counters.repair, 0);

  const busy = managerFixture({ stateName: 'running', activeJobs: [{ id: 'queued', status: 'queued' }] });
  await assert.rejects(
    runRepairSmoke({ manager: busy.manager, targetManifest: busy.target, audit: busy.audit }),
    (error) => error.code === 'active_jobs',
  );
  assert.equal(busy.counters.repair, 0);
});

test('explicit repair executes once and publishes only safe identities and counts', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true });
  const result = await runRepairSmoke({
    manager: fixture.manager,
    targetManifest: fixture.target,
    execute: true,
    audit: fixture.audit,
    credentialsPresent: async () => false,
  });
  assert.equal(result.ok, true);
  assert.equal(result.mode, 'execute-private-runtime');
  assert.equal(fixture.counters.repair, 1);
  assert.equal(result.audit.updateCommandsObserved, 1);
  assert.equal(result.audit.stdinOnlyControlDocuments, 1);
  assert.equal(result.checks.activeJobs, 0);
  assert.equal(result.checks.digestChanged, true);
  const serialized = JSON.stringify(result);
  for (const forbidden of [fixture.target.runtime.sha256, TARGET_TX, OWNER, fixture.state.downloadDir]) {
    assert.equal(serialized.includes(forbidden), false);
  }
});

test('explicit repair sends one update for the same runtime digest to verify the durable receipt', async () => {
  const fixture = managerFixture({ stateName: 'stopped', digestChanged: false, configureBeforeUpdate: true });
  const result = await runRepairSmoke({
    manager: fixture.manager,
    targetManifest: fixture.target,
    execute: true,
    audit: fixture.audit,
    credentialsPresent: async () => false,
  });
  assert.equal(result.ok, true);
  assert.equal(result.checks.digestChanged, false);
  assert.equal(fixture.counters.repair, 1);
  assert.equal(result.audit.updateCommandsObserved, 1);
  assert.equal(result.audit.updateControlDocumentsObserved, 1);
  assert.equal(result.audit.configureControlDocumentsObserved, 1);
  assert.equal(result.audit.stdinOnlyControlDocuments, 2);
});

test('explicit legacy-mount repair quiesces once and updates once even for the same digest', async () => {
  const fixture = managerFixture({ stateName: 'running', digestChanged: false, allowTerminate: true });
  const result = await runRepairSmoke(quiescedRepairOptions(fixture));
  assert.equal(result.ok, true);
  assert.equal(result.checks.digestChanged, false);
  assert.equal(fixture.counters.repair, 0);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'target_verified');
  assert.equal(result.checks.durableTerminalReceipt, true);
  assert.equal(result.audit.terminateCommandsObserved, 1);
  assert.equal(result.audit.updateControlDocumentsObserved, 1);
});

test('legacy-mount repair default transaction factory creates a fresh strict identity', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true });
  const options = quiescedRepairOptions(fixture);
  delete options.transactionIdFactory;
  const sourceTransaction = (await options.manager.readState()).installedRuntimeTransactionId;

  await runRepairSmoke(options);

  const targetTransaction = (await options.manager.readState()).installedRuntimeTransactionId;
  assert.match(targetTransaction, /^[a-f0-9]{64}$/);
  assert.notEqual(targetTransaction, sourceTransaction);
});

test('legacy-mount repair ownership TOCTOU fails before terminate, umount and update', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true, ownershipMismatchAt: 4 });
  await assert.rejects(
    runRepairSmoke(quiescedRepairOptions(fixture)),
    (error) => error.code === 'ownership_mismatch',
  );
  assert.equal(fixture.counters.terminate, 0);
  assert.equal(fixture.counters.umount, 0);
  assert.equal(fixture.counters.update, 0);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'terminating');
});

test('terminating marker resumes cleanup without a second terminate and submits update once', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true });
  await assert.rejects(runRepairSmoke(quiescedRepairOptions(fixture, { mountFailure: true })));
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'terminating');
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 0);

  const result = await runRepairSmoke(quiescedRepairOptions(fixture));
  assert.equal(result.ok, true);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'target_verified');
});

test('durable pending transaction from transport unknown is fail-closed and never repeats update', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true, updateFailure: 'transport_unknown' });
  const options = quiescedRepairOptions(fixture);
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_failed');
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'quiesced');
  assert.equal(fixture.state.updateTransaction.id, TARGET_TX);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_recovery_required');
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
});

test('failure before a durable update transaction retries with the same identity but never terminates twice', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true, updateFailure: 'before_persist' });
  const options = quiescedRepairOptions(fixture);
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_failed');
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'quiesced');
  assert.equal(fixture.state.updateTransaction, null);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);

  fixture.setUpdateFailure('');
  const result = await runRepairSmoke(options);
  assert.equal(result.ok, true);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 2);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'target_verified');
});

test('committed target with a lost response becomes a durable terminal receipt without replay', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true, updateFailure: 'committed_response_loss' });
  const options = quiescedRepairOptions(fixture);
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_failed');
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'quiesced');
  assert.equal(fixture.state.installedRuntimeTransactionId, TARGET_TX);
  assert.equal(fixture.counters.update, 1);

  fixture.audit.records.length = 0;
  fixture.audit.stdinControls.length = 0;
  fixture.audit.terminateCommands = 0;
  const result = await runRepairSmoke(options);
  assert.equal(result.checks.recoveredWithoutReplay, true);
  assert.equal(result.checks.durableTerminalReceipt, true);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'target_verified');
});

test('pending transaction reconciles an exact committed runtime without a second update', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true, updateFailure: 'transport_unknown' });
  const options = quiescedRepairOptions(fixture);
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_failed');
  fixture.commitPendingRuntime();
  fixture.audit.records.length = 0;
  fixture.audit.stdinControls.length = 0;
  fixture.audit.terminateCommands = 0;

  const result = await runRepairSmoke(options);
  assert.equal(result.checks.reconciledDurableUpdateTransaction, true);
  assert.equal(result.checks.recoveredWithoutReplay, true);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
  assert.equal(fixture.state.updateTransaction, null);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'target_verified');
});

test('matching rolled-back receipt remains a terminal blocker and is never auto-replayed', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true, updateFailure: 'transport_unknown' });
  const options = quiescedRepairOptions(fixture);
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_failed');
  fixture.rollBackPendingRuntime();
  fixture.audit.records.length = 0;
  fixture.audit.stdinControls.length = 0;
  fixture.audit.terminateCommands = 0;

  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_recovery_required');
  assert.equal(fixture.state.updateTransaction, null);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'quiesced');
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_recovery_required');
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
});

test('terminal repair receipt survives a stopped target and prevents repeat terminate or update', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true });
  const options = quiescedRepairOptions(fixture);
  await runRepairSmoke(options);
  fixture.setStatusState('stopped');
  fixture.audit.records.length = 0;
  fixture.audit.stdinControls.length = 0;
  fixture.audit.terminateCommands = 0;

  const result = await runRepairSmoke(options);
  assert.equal(result.checks.recoveredWithoutReplay, true);
  assert.equal(result.checks.durableTerminalReceipt, true);
  assert.equal(fixture.counters.start >= 1, true);
  assert.equal(fixture.counters.terminate, 1);
  assert.equal(fixture.counters.update, 1);
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'target_verified');
});

test('fresh repair rejects a reused source transaction identity before lifecycle mutation', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true });
  const options = quiescedRepairOptions(fixture);
  options.transactionIdFactory = () => SOURCE_TX;
  await assert.rejects(runRepairSmoke(options), (error) => error.code === 'repair_failed');
  assert.equal(fixture.state[REPAIR_STATE_FIELD], null);
  assert.equal(fixture.counters.terminate, 0);
  assert.equal(fixture.counters.umount, 0);
  assert.equal(fixture.counters.update, 0);
});

test('terminating recovery checks the queue before stop and mount cleanup', async () => {
  const activeJobs = [];
  const fixture = managerFixture({ stateName: 'running', activeJobs, allowTerminate: true });
  const options = quiescedRepairOptions(fixture, { mountFailure: true });
  await assert.rejects(runRepairSmoke(options));
  assert.equal(fixture.state[REPAIR_STATE_FIELD].phase, 'terminating');
  activeJobs.push({ id: 'queued-after-crash', status: 'queued' });
  const stopsBefore = fixture.counters.stop;
  const unmountsBefore = fixture.counters.umount;

  await assert.rejects(runRepairSmoke(quiescedRepairOptions(fixture)), (error) => error.code === 'active_jobs');
  assert.equal(fixture.counters.stop, stopsBefore);
  assert.equal(fixture.counters.umount, unmountsBefore);
  assert.equal(fixture.counters.update, 0);
});

test('terminating recovery revalidates the migration lock before restarting the backend', async () => {
  const fixture = managerFixture({ stateName: 'running', allowTerminate: true });
  await assert.rejects(runRepairSmoke(quiescedRepairOptions(fixture, { mountFailure: true })));
  const startsBefore = fixture.counters.start;
  const stopsBefore = fixture.counters.stop;
  const unmountsBefore = fixture.counters.umount;
  const updatesBefore = fixture.counters.update;
  let verifications = 0;
  const options = quiescedRepairOptions(fixture);
  options.migrationLockVerifier = (lock) => {
    assert.equal(lock.held, true);
    verifications += 1;
    if (verifications === 2) throw new Error('synthetic migration lock loss');
  };

  await assert.rejects(runRepairSmoke(options), /synthetic migration lock loss/);
  assert.equal(fixture.counters.start, startsBefore);
  assert.equal(fixture.counters.stop, stopsBefore);
  assert.equal(fixture.counters.umount, unmountsBefore);
  assert.equal(fixture.counters.update, updatesBefore);
});

test('repair audit rejects a second terminate before delegating it', async () => {
  const { audit, calls } = createAudit({ allowTerminate: true });
  await audit.exec(TRUSTED_WSL, ['--terminate', DISTRO], { env: { PATH: 'safe' } });
  assert.throws(
    () => audit.exec(TRUSTED_WSL, ['--terminate', DISTRO], { env: { PATH: 'safe' } }),
    (error) => error instanceof RepairSmokeError && error.code === 'audit_failed',
  );
  assert.equal(calls.filter((call) => call.args.includes('--terminate')).length, 1);
  assert.equal(audit.summary().terminateCommandsObserved, 1);
});

test('repair audit covers proxy spawn and rejects every stdin control except configure and update', () => {
  const { audit, calls } = createAudit();
  class CapturingManager {
    constructor(options) { this.options = options; }
  }
  const manager = createRepairWslManager({ marker: true }, audit, CapturingManager);
  manager.options.spawn(
    TRUSTED_WSL,
    ['--distribution', DISTRO, '--exec', '/usr/local/bin/amdl-appliance-control', 'proxy-stdio'],
    { env: { PATH: 'safe' } },
  );
  assert.equal(calls.filter((call) => call.kind === 'spawn').length, 1);
  assert.throws(
    () => audit.input(
      TRUSTED_WSL,
      ['--distribution', DISTRO, '--exec', '/usr/local/bin/amdl-appliance-control', 'credential-stdin'],
      Buffer.from('{}'),
      { env: { PATH: 'safe' } },
    ),
    (error) => error instanceof RepairSmokeError && error.code === 'audit_failed',
  );
  assert.throws(
    () => audit.input(
      TRUSTED_WSL,
      ['--distribution', DISTRO, '--exec', '/usr/local/bin/amdl-appliance-control', 'unexpected-stdin'],
      Buffer.from('{}'),
      { env: { PATH: 'safe' } },
    ),
    (error) => error instanceof RepairSmokeError && error.code === 'audit_failed',
  );
});

test('process audit aborts a repair that attempts terminate before update', async () => {
  const fixture = managerFixture({ stateName: 'running', maliciousRepair: true });
  await assert.rejects(
    runRepairSmoke({ manager: fixture.manager, targetManifest: fixture.target, execute: true, audit: fixture.audit }),
    (error) => error.code === 'repair_failed',
  );
  assert.equal(fixture.counters.repair, 1);
  assert.equal(fixture.audit.summary().updateCommandsObserved, 0);
});

test('public repair errors never expose underlying details', () => {
  const secret = 'repair-secret-sentinel';
  const result = publicRepairError(Object.assign(new Error(secret), { code: secret, step: secret }));
  assert.equal(JSON.stringify(result).includes(secret), false);
});

test('public repair errors expose only allowlisted upstream codes and error types', () => {
  const lock = publicRepairError(Object.assign(new Error('private detail'), {
    name: 'WslSmokeError',
    code: 'local_poc_migration_lock_failed',
    step: 'migration_lock_spawn',
  }));
  assert.equal(lock.upstreamCode, 'local_poc_migration_lock_failed');
  assert.equal(lock.step, 'migration_lock_spawn');
  assert.equal(JSON.stringify(lock).includes('private detail'), false);

  const type = publicRepairError(new TypeError('private type detail'));
  assert.equal(type.errorType, 'TypeError');
  assert.equal(JSON.stringify(type).includes('private type detail'), false);

  const framed = new TypeError('private framed detail');
  framed.stack = 'TypeError: private framed detail\n    at main (C:\\repo\\electron\\run-wsl-repair-smoke.js:401:17)';
  const framedResult = publicRepairError(framed);
  assert.equal(framedResult.debugFrame, 'run-wsl-repair-smoke.js:401:17');
  assert.equal(JSON.stringify(framedResult).includes('private framed detail'), false);
});

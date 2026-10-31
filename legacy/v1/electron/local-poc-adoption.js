'use strict';

const crypto = require('node:crypto');
const fsp = require('node:fs/promises');
const path = require('node:path');

const { validateApplianceManifest } = require('./appliance-manifest');
const {
  EXPECTED_RUNTIME,
  EXPECTED_SERVICE,
  applianceIdentityMatches,
} = require('./wsl-manager');
const {
  assertLocalPocMigrationLock,
  withLocalPocMigrationLock,
} = require('./local-poc-migration-lock');
const { WslSmokeError, safeIdentity } = require('./wsl-smoke');

const DISTRO_NAME = 'AppleMusicDownloader';
const DISTRO_OWNERSHIP_PATH = '/var/lib/applemusic-downloader/desktop-owner-id';
const DISTRO_STATE_DIRECTORY = '/var/lib/applemusic-downloader';
const SHA256_PATTERN = /^[a-f0-9]{64}$/;

function reject(step) {
  throw new WslSmokeError('local_poc_adoption_rejected', step);
}

function sameWindowsPath(left, right) {
  return typeof left === 'string' && typeof right === 'string'
    && path.win32.normalize(left).toLowerCase() === path.win32.normalize(right).toLowerCase();
}

function parseRegistrationIdentity(stdout) {
  let value;
  try {
    value = JSON.parse(stdout);
  } catch {
    reject('adoption_registration');
  }
  if (value?.version !== 1 || value.distributionName !== DISTRO_NAME || value.wslVersion !== 2
      || typeof value.basePath !== 'string' || !path.win32.isAbsolute(value.basePath)) {
    reject('adoption_registration');
  }
  return value;
}

async function registrationIdentity(manager) {
  try {
    const result = await manager.runner.exec(
      manager.windowsProbeExe,
      ['wsl-appliance-identity'],
      { timeoutMs: 15_000, env: manager.processEnvironment },
    );
    return parseRegistrationIdentity(result.stdout);
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('adoption_registration');
  }
}

async function assertDistroMarkerAbsent(manager) {
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/test', '!', '-e', DISTRO_OWNERSHIP_PATH,
    ], { timeoutMs: 15_000 });
  } catch {
    reject('adoption_distro_marker');
  }
}

async function validateCandidate(manager, {
  credentialsPresent = false,
  expectedHostOwner = '',
} = {}) {
  const state = await manager.readState();
  if (!state || state.schemaVersion !== 1 || state.distroName !== DISTRO_NAME
      || state.ownership !== undefined && state.ownership !== null
      || state.installTransaction !== undefined && state.installTransaction !== null
      || state.updateTransaction !== undefined && state.updateTransaction !== null
      || state.pendingManifest !== undefined && state.pendingManifest !== null
      || credentialsPresent) {
    reject('adoption_host_state');
  }

  let hostOwner;
  try { hostOwner = await manager.readOwnershipId(); } catch { reject('adoption_host_marker'); }
  if (expectedHostOwner ? hostOwner !== expectedHostOwner : hostOwner !== '') reject('adoption_host_marker');
  await assertDistroMarkerAbsent(manager);

  const distro = await manager.registeredDistro();
  if (!distro || distro.name !== DISTRO_NAME || distro.version !== 2) reject('adoption_distro');
  const registered = await registrationIdentity(manager);
  let expectedBase;
  try { expectedBase = await manager.canonicalizeWindowsPathWithHelper(manager.installDir); } catch { reject('adoption_registration'); }
  if (!sameWindowsPath(registered.basePath, expectedBase.canonicalPath)
      || !sameWindowsPath(state.installDir, expectedBase.canonicalPath)) {
    reject('adoption_registration');
  }

  let manifest;
  try { manifest = validateApplianceManifest(state.manifest); } catch { reject('adoption_manifest'); }
  const versionMissing = state.installedVersion === undefined || state.installedVersion === null;
  const digestMissing = state.installedRuntimeSha256 === undefined || state.installedRuntimeSha256 === null;
  const transactionMissing = state.installedRuntimeTransactionId === undefined || state.installedRuntimeTransactionId === null;
  if (manifest.distroName !== DISTRO_NAME
      || !versionMissing && state.installedVersion !== manifest.version
      || !digestMissing && state.installedRuntimeSha256 !== manifest.runtime?.sha256
      || !transactionMissing && !SHA256_PATTERN.test(state.installedRuntimeTransactionId)) {
    reject('adoption_manifest');
  }

  let probe;
  let status;
  try {
    const probeResult = await manager.runControl('probe', { timeoutMs: 20_000 });
    probe = JSON.parse(probeResult.stdout);
    status = await manager.status();
  } catch {
    reject('adoption_runtime_identity');
  }
  if (!applianceIdentityMatches(probe, EXPECTED_SERVICE, EXPECTED_RUNTIME, manifest.version)
      || !applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, manifest.version)
      || status.runtimeSha256 !== manifest.runtime.sha256
      || !SHA256_PATTERN.test(status.runtimeTransactionId || '')
      || !digestMissing && status.runtimeSha256 !== state.installedRuntimeSha256
      || !transactionMissing && status.runtimeTransactionId !== state.installedRuntimeTransactionId
      || status.credentialsConfigured !== false
      || status.updateInProgress !== false
      || Boolean(status.pendingTransactionId)
      || status.configured !== true
      || !['running', 'starting'].includes(status.state)) {
    reject('adoption_runtime_identity');
  }

  let active;
  try {
    await manager.prepareLocalEndpoint({ expectedVersion: manifest.version });
    active = await manager.activeJobs();
  } catch {
    reject('adoption_active_jobs');
  }
  if (active.length !== 0) reject('adoption_active_jobs');
  const derivedStateUpdate = {};
  if (versionMissing) derivedStateUpdate.installedVersion = manifest.version;
  if (digestMissing) derivedStateUpdate.installedRuntimeSha256 = status.runtimeSha256;
  if (transactionMissing) derivedStateUpdate.installedRuntimeTransactionId = status.runtimeTransactionId;
  return {
    state,
    manifest,
    status,
    registration: registered,
    derivedStateUpdate,
  };
}

async function writeDistroOwnerNoReplace(manager, ownerId) {
  if (!SHA256_PATTERN.test(ownerId)) reject('adoption_commit');
  const transaction = crypto.randomBytes(16).toString('hex');
  const temporary = `${DISTRO_STATE_DIRECTORY}/.desktop-owner-id-${transaction}.tmp`;
  const payload = Buffer.from(ownerId, 'ascii');
  let published = false;
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/mkdir', '-p', DISTRO_STATE_DIRECTORY,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/chmod', '0700', DISTRO_STATE_DIRECTORY,
    ], { timeoutMs: 15_000 });
    await assertDistroMarkerAbsent(manager);
    await manager.runner.input(await manager.resolveWslExecutable(), [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/dd', `of=${temporary}`, 'bs=64', 'count=1', 'conv=fsync', 'status=none',
    ], payload, {
      timeoutMs: 20_000,
      maxInputBytes: 64,
      secrets: [ownerId],
      env: manager.processEnvironment,
    });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/chmod', '0600', temporary,
    ], { timeoutMs: 15_000 });
    await assertDistroMarkerAbsent(manager);
    // link(2) publishes without replacing a marker created by another actor.
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/ln', '--', temporary, DISTRO_OWNERSHIP_PATH,
    ], { timeoutMs: 15_000, secrets: [ownerId] });
    published = true;
    const marker = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/cat', DISTRO_OWNERSHIP_PATH,
    ], { timeoutMs: 15_000, secrets: [ownerId] });
    const mode = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/stat', '-c', '%a:%U', '--', DISTRO_OWNERSHIP_PATH,
    ], { timeoutMs: 15_000 });
    if (marker.stdout !== '[REDACTED]' && marker.stdout !== ownerId) reject('adoption_commit');
    if (mode.stdout.trim() !== '600:root') reject('adoption_commit');
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('adoption_commit');
  } finally {
    payload.fill(0);
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/rm', '-f', '--', temporary,
    ], { timeoutMs: 15_000 }).catch(() => {});
  }
  return published;
}

async function writeHostOwnerNoReplace(manager, ownerId) {
  if (!SHA256_PATTERN.test(ownerId)) reject('adoption_commit');
  const temporary = `${manager.ownershipPath}.${process.pid}.${crypto.randomBytes(12).toString('hex')}.tmp`;
  const payload = Buffer.from(ownerId, 'ascii');
  let handle;
  let published = false;
  let committed = false;
  try {
    await fsp.mkdir(path.dirname(manager.ownershipPath), { recursive: true });
    try {
      await fsp.lstat(manager.ownershipPath);
      reject('adoption_host_marker');
    } catch (error) {
      if (error instanceof WslSmokeError) throw error;
      if (error.code !== 'ENOENT') reject('adoption_host_marker');
    }
    handle = await fsp.open(temporary, 'wx', 0o600);
    await handle.write(payload, 0, payload.length, 0);
    await handle.sync();
    await handle.close();
    handle = null;
    // Hard-link publication is atomic and never replaces a host marker that
    // appears between the absence check and commit.
    await fsp.link(temporary, manager.ownershipPath);
    published = true;
    await fsp.unlink(temporary);
    const info = await fsp.lstat(manager.ownershipPath);
    if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1) reject('adoption_commit');
    committed = true;
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('adoption_commit');
  } finally {
    payload.fill(0);
    if (handle) await handle.close().catch(() => {});
    await fsp.unlink(temporary).catch(() => {});
    if (published && !committed) {
      try {
        if (await fsp.readFile(manager.ownershipPath, 'utf8') === ownerId) await fsp.unlink(manager.ownershipPath);
      } catch { /* The caller will fail closed on any remaining marker. */ }
    }
  }
}

async function rollbackOwner(manager, ownerId, stateBefore, unlinkHost = (filename) => fsp.unlink(filename)) {
  let rollbackOK = true;
  try {
    const current = await manager.readState();
    if (current.ownership?.ownerId === ownerId) {
      await manager.writeState({ ownership: stateBefore.ownership ?? null });
    }
  } catch { rollbackOK = false; }
  try {
    const marker = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/cat', DISTRO_OWNERSHIP_PATH,
    ], { timeoutMs: 15_000, secrets: [ownerId] });
    if (marker.stdout === '[REDACTED]' || marker.stdout === ownerId) {
      await manager.runWsl([
        '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
        '/usr/bin/rm', '-f', '--', DISTRO_OWNERSHIP_PATH,
      ], { timeoutMs: 15_000 });
    } else {
      rollbackOK = false;
    }
  } catch { /* A missing marker is already rolled back. */ }
  try {
    if (await manager.readOwnershipId() === ownerId) await unlinkHost(manager.ownershipPath);
  } catch { rollbackOK = false; }
  if (!rollbackOK) throw new WslSmokeError('local_poc_adoption_failed', 'adoption_rollback');
}

async function rollbackDerivedState(manager, derivedStateUpdate, stateBefore) {
  const keys = Object.keys(derivedStateUpdate || {});
  if (keys.length === 0) return;
  const current = await manager.readState();
  if (keys.some((key) => current[key] !== derivedStateUpdate[key])) {
    throw new WslSmokeError('local_poc_adoption_failed', 'adoption_rollback');
  }
  const restore = {};
  for (const key of keys) restore[key] = stateBefore[key] ?? null;
  await manager.writeState(restore);
}

async function adoptLocalPocCore(manager, {
  credentialsPresent = false,
  now = () => new Date().toISOString(),
  hostOwnerWriter = writeHostOwnerNoReplace,
  hostOwnerUnlink,
} = {}) {
  let candidate = await validateCandidate(manager, { credentialsPresent });
  const stateBefore = candidate.state;
  const derivedStateUpdate = candidate.derivedStateUpdate;
  let stateDerived = false;
  let ownerId = '';
  let commitStarted = false;
  try {
    if (Object.keys(derivedStateUpdate).length > 0) {
      await manager.writeState(derivedStateUpdate);
      stateDerived = true;
      candidate = await validateCandidate(manager, { credentialsPresent });
      if (Object.keys(candidate.derivedStateUpdate).length > 0) reject('adoption_manifest');
    }
    ownerId = crypto.randomBytes(32).toString('hex');
    await hostOwnerWriter(manager, ownerId);
    commitStarted = true;
    if (!SHA256_PATTERN.test(ownerId)) reject('adoption_commit');
    // Revalidate every external identity after the host file becomes the
    // no-replace commit lock and immediately before publishing the WSL marker.
    await validateCandidate(manager, { credentialsPresent, expectedHostOwner: ownerId });
    await writeDistroOwnerNoReplace(manager, ownerId);
    await manager.writeState({
      ownership: {
        schemaVersion: 1,
        ownerId,
        distroName: DISTRO_NAME,
        installDir: manager.installDir,
        createdAt: now(),
        adoptedLocalPoc: true,
      },
      installDir: manager.installDir,
    });
    await manager.verifyDistroOwnership({ expectedVersion: candidate.manifest.version });
    const finalStatus = await manager.status();
    if (!applianceIdentityMatches(finalStatus, EXPECTED_SERVICE, EXPECTED_RUNTIME, candidate.manifest.version)
        || finalStatus.runtimeSha256 !== candidate.manifest.runtime.sha256
        || finalStatus.runtimeTransactionId !== candidate.state.installedRuntimeTransactionId) {
      reject('adoption_commit');
    }
    return {
      adopted: true,
      identity: safeIdentity(finalStatus),
      registrationVerified: true,
      credentialsConfigured: false,
      activeJobs: 0,
      stateIdentityDerived: stateDerived,
    };
  } catch (error) {
    let rollbackFailed = false;
    if (commitStarted && ownerId) {
      try { await rollbackOwner(manager, ownerId, stateBefore, hostOwnerUnlink); } catch { rollbackFailed = true; }
    }
    if (stateDerived) {
      try { await rollbackDerivedState(manager, derivedStateUpdate, stateBefore); } catch { rollbackFailed = true; }
    }
    if (rollbackFailed) throw new WslSmokeError('local_poc_adoption_failed', 'adoption_rollback');
    if (error instanceof WslSmokeError) throw error;
    throw new WslSmokeError('local_poc_adoption_failed', 'adoption_commit');
  }
}

async function adoptLocalPocWhileLocked(manager, migrationLock, options = {}) {
  assertLocalPocMigrationLock(migrationLock);
  return adoptLocalPocCore(manager, options);
}

async function adoptLocalPoc(manager, options = {}) {
  const migrationLocker = options.migrationLocker
    || manager.localPocMigrationLocker
    || withLocalPocMigrationLock;
  const coreOptions = { ...options };
  delete coreOptions.migrationLocker;
  return migrationLocker(manager, () => adoptLocalPocCore(manager, coreOptions));
}

module.exports = {
  DISTRO_OWNERSHIP_PATH,
  adoptLocalPoc,
  adoptLocalPocWhileLocked,
  assertDistroMarkerAbsent,
  parseRegistrationIdentity,
  registrationIdentity,
  sameWindowsPath,
  validateCandidate,
  rollbackDerivedState,
  writeDistroOwnerNoReplace,
  writeHostOwnerNoReplace,
};

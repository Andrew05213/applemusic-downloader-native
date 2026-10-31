'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const {
  adoptLocalPoc,
  parseRegistrationIdentity,
  validateCandidate,
  writeHostOwnerNoReplace,
} = require('../../electron/local-poc-adoption');
const { WslSmokeError } = require('../../electron/wsl-smoke');
const { createManifest } = require('./fixtures/appliance-manifest-document');

const OWNER = 'e'.repeat(64);
const TRANSACTION = '1'.repeat(64);
const INSTALL_DIR = 'C:\\Users\\测试 用户\\AppData\\Local\\AppleMusicDownloader\\wsl';

function adoptionManager(overrides = {}) {
  const manifest = createManifest();
  const shared = {
    hostOwner: '',
    marker: '',
    temporaryOwner: '',
    active: [],
    lockHeld: false,
    lockAcquisitions: 0,
    runtimeStatus: {
      service: 'applemusic-downloader',
      runtime: 'wsl-appliance',
      version: manifest.version,
      runtimeSha256: manifest.runtime.sha256,
      runtimeTransactionId: TRANSACTION,
      credentialsConfigured: false,
      updateInProgress: false,
      configured: true,
      state: 'running',
    },
    state: {
      schemaVersion: 1,
      distroName: 'AppleMusicDownloader',
      installDir: INSTALL_DIR,
      manifest,
      installedVersion: manifest.version,
      installedRuntimeSha256: manifest.runtime.sha256,
      installedRuntimeTransactionId: TRANSACTION,
      installTransaction: null,
      updateTransaction: null,
      pendingManifest: null,
    },
  };
  const status = () => structuredClone(shared.runtimeStatus);
  const manager = {
    installDir: INSTALL_DIR,
    ownershipPath: 'C:\\host\\ownership.id',
    windowsProbeExe: 'C:\\Program Files\\Apple Music Downloader\\amdl-windows-probe.exe',
    processEnvironment: { PATH: 'safe' },
    localPocMigrationLocker: async (_manager, operation) => {
      assert.equal(shared.lockHeld, false);
      shared.lockAcquisitions += 1;
      shared.lockHeld = true;
      try { return await operation({ testLock: true }); } finally { shared.lockHeld = false; }
    },
    runner: {
      exec: async (_command, args) => {
        assert.deepEqual(args, ['wsl-appliance-identity']);
        return { stdout: JSON.stringify({ version: 1, distributionName: 'AppleMusicDownloader', basePath: INSTALL_DIR, wslVersion: 2 }) };
      },
      input: async (_command, args, input) => {
        assert.equal(args.some((value) => value === 'credential-stdin' || value === 'update-stdin'), false);
        shared.temporaryOwner = input.toString('ascii');
        return { stdout: '', stderr: '' };
      },
    },
    readState: async () => structuredClone(shared.state),
    readOwnershipId: async () => shared.hostOwner,
    registeredDistro: async () => ({ name: 'AppleMusicDownloader', version: 2 }),
    canonicalizeWindowsPathWithHelper: async () => ({ canonicalPath: INSTALL_DIR, fileSystem: 'NTFS' }),
    runControl: async (command) => {
      assert.equal(command, 'probe');
      return { stdout: JSON.stringify(status()), stderr: '' };
    },
    status: async () => status(),
    prepareLocalEndpoint: async () => {},
    activeJobs: async () => shared.active,
    createHostOwnershipRecord: async () => { shared.hostOwner = OWNER; return OWNER; },
    resolveWslExecutable: async () => 'C:\\Windows\\System32\\wsl.exe',
    writeState: async (update) => { shared.state = { ...shared.state, ...structuredClone(update) }; return shared.state; },
    verifyDistroOwnership: async () => {
      if (!shared.hostOwner || shared.marker !== shared.hostOwner || shared.state.ownership?.ownerId !== shared.hostOwner) throw new Error('ownership mismatch');
      return { appliance: status() };
    },
    runWsl: async (args) => {
      const executable = args.find((value) => typeof value === 'string' && value.startsWith('/usr/bin/'));
      if (executable === '/usr/bin/test') {
        if (shared.marker) throw new Error('marker exists');
        return { stdout: '', stderr: '' };
      }
      if (executable === '/usr/bin/ln') {
        if (shared.marker) throw new Error('no replace');
        shared.marker = shared.temporaryOwner;
        return { stdout: '', stderr: '' };
      }
      if (executable === '/usr/bin/cat') {
        if (!shared.marker) throw new Error('missing marker');
        return { stdout: shared.marker, stderr: '' };
      }
      if (executable === '/usr/bin/stat') return { stdout: '600:root\n', stderr: '' };
      if (executable === '/usr/bin/rm') {
        const target = args.at(-1);
        if (target.endsWith('/desktop-owner-id')) shared.marker = '';
        else shared.temporaryOwner = '';
        return { stdout: '', stderr: '' };
      }
      return { stdout: '', stderr: '' };
    },
    ...overrides,
  };
  return { manager, shared, manifest };
}

test('registration identity parser rejects an ambiguous or wrong WSL identity', () => {
  assert.deepEqual(parseRegistrationIdentity(JSON.stringify({
    version: 1,
    distributionName: 'AppleMusicDownloader',
    basePath: INSTALL_DIR,
    wslVersion: 2,
  })).basePath, INSTALL_DIR);
  for (const value of [
    {},
    { version: 1, distributionName: 'Ubuntu', basePath: INSTALL_DIR, wslVersion: 2 },
    { version: 1, distributionName: 'AppleMusicDownloader', basePath: INSTALL_DIR, wslVersion: 1 },
  ]) {
    assert.throws(() => parseRegistrationIdentity(JSON.stringify(value)), (error) => error.code === 'local_poc_adoption_rejected');
  }
});

test('host ownership publication is atomic and never replaces an existing marker', async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-adopt-owner-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const ownershipPath = path.join(directory, 'ownership.id');
  const manager = { ownershipPath };
  await writeHostOwnerNoReplace(manager, OWNER);
  assert.equal(fs.readFileSync(ownershipPath, 'utf8'), OWNER);
  await assert.rejects(
    writeHostOwnerNoReplace(manager, 'f'.repeat(64)),
    (error) => error.code === 'local_poc_adoption_rejected',
  );
  assert.equal(fs.readFileSync(ownershipPath, 'utf8'), OWNER);
  assert.equal(fs.readdirSync(directory).filter((name) => name.includes('.tmp')).length, 0);
});

test('local PoC adoption validates complete identity then publishes host, distro and state ownership', async () => {
  const { manager, shared } = adoptionManager();
  let generatedOwner = '';
  const result = await adoptLocalPoc(manager, {
    now: () => '2026-07-30T00:00:00.000Z',
    hostOwnerWriter: async (_manager, ownerId) => {
      assert.equal(shared.lockHeld, true);
      generatedOwner = ownerId;
      shared.hostOwner = ownerId;
    },
  });
  assert.equal(result.adopted, true);
  assert.match(generatedOwner, /^[a-f0-9]{64}$/);
  assert.equal(shared.hostOwner, generatedOwner);
  assert.equal(shared.marker, generatedOwner);
  assert.equal(shared.state.ownership.ownerId, generatedOwner);
  assert.equal(shared.state.ownership.adoptedLocalPoc, true);
  assert.equal(result.stateIdentityDerived, false);
  assert.equal(shared.lockAcquisitions, 1);
  assert.equal(shared.lockHeld, false);
  assert.doesNotMatch(JSON.stringify(result), new RegExp(generatedOwner));
});

test('explicit local PoC adoption safely derives missing persisted runtime identity from verified supervisor status', async () => {
  const { manager, shared, manifest } = adoptionManager();
  delete shared.state.installedVersion;
  delete shared.state.installedRuntimeSha256;
  delete shared.state.installedRuntimeTransactionId;
  let generatedOwner = '';
  const result = await adoptLocalPoc(manager, {
    hostOwnerWriter: async (_manager, ownerId) => { generatedOwner = ownerId; shared.hostOwner = ownerId; },
  });
  assert.equal(result.adopted, true);
  assert.equal(result.stateIdentityDerived, true);
  assert.equal(shared.state.installedVersion, manifest.version);
  assert.equal(shared.state.installedRuntimeSha256, manifest.runtime.sha256);
  assert.equal(shared.state.installedRuntimeTransactionId, TRANSACTION);
  assert.equal(shared.state.ownership.ownerId, generatedOwner);
  assert.doesNotMatch(JSON.stringify(result), new RegExp(generatedOwner));
});

test('runtime identity derivation rejects missing manifest and every persisted/supervisor mismatch', async () => {
  const mutations = [
    ({ shared }) => { shared.state.manifest = null; },
    ({ shared }) => { shared.state.manifest.runtime.sha256 = 'a'.repeat(64); },
    ({ shared }) => { shared.state.installedVersion = '9.9.9'; },
    ({ shared }) => { shared.state.installedRuntimeSha256 = 'a'.repeat(64); },
    ({ shared }) => { shared.state.installedRuntimeTransactionId = '2'.repeat(64); },
    ({ shared }) => { shared.runtimeStatus.version = '9.9.9'; },
    ({ shared }) => { shared.runtimeStatus.runtimeSha256 = 'a'.repeat(64); },
    ({ shared }) => { shared.runtimeStatus.runtimeTransactionId = 'invalid'; },
  ];
  for (const mutate of mutations) {
    const fixture = adoptionManager();
    mutate(fixture);
    await assert.rejects(
      validateCandidate(fixture.manager),
      (error) => error instanceof WslSmokeError && error.code === 'local_poc_adoption_rejected',
    );
    assert.equal(fixture.shared.hostOwner, '');
    assert.equal(fixture.shared.marker, '');
  }
});

test('adoption rejects credentials, transactions, active jobs and BasePath mismatch before owner creation', async () => {
  for (const mutate of [
    ({ options }) => { options.credentialsPresent = true; },
    ({ shared }) => { shared.state.updateTransaction = { id: 'pending' }; },
    ({ shared }) => { shared.active = [{ id: 'queued', status: 'queued' }]; },
    ({ manager }) => {
      manager.runner.exec = async () => ({ stdout: JSON.stringify({
        version: 1,
        distributionName: 'AppleMusicDownloader',
        basePath: 'C:\\Foreign\\wsl',
        wslVersion: 2,
      }) });
    },
  ]) {
    const fixture = adoptionManager();
    const options = { credentialsPresent: false };
    mutate({ ...fixture, options });
    await assert.rejects(
      validateCandidate(fixture.manager, options),
      (error) => error instanceof WslSmokeError && error.code === 'local_poc_adoption_rejected',
    );
    assert.equal(fixture.shared.hostOwner, '');
    assert.equal(fixture.shared.marker, '');
  }
});

test('adoption commit failure rolls back only its own host and distro markers', async () => {
  const fixture = adoptionManager();
  delete fixture.shared.state.installedRuntimeSha256;
  delete fixture.shared.state.installedRuntimeTransactionId;
  let first = true;
  fixture.manager.writeState = async (update) => {
    if (first && update.ownership?.ownerId) {
      first = false;
      throw new Error('synthetic state commit failure');
    }
    fixture.shared.state = { ...fixture.shared.state, ...structuredClone(update) };
    return fixture.shared.state;
  };
  // The production rollback unlinks a real host file. This mock represents
  // that fixed ownership path without exposing or deleting any user file.
  fixture.manager.readOwnershipId = async () => fixture.shared.hostOwner;
  await assert.rejects(
    adoptLocalPoc(fixture.manager, {
      hostOwnerWriter: async (_manager, ownerId) => { fixture.shared.hostOwner = ownerId; },
      hostOwnerUnlink: async () => { fixture.shared.hostOwner = ''; },
    }),
    (error) => error.code === 'local_poc_adoption_rejected' || error.code === 'local_poc_adoption_failed',
  );
  assert.equal(fixture.shared.marker, '');
  assert.equal(fixture.shared.hostOwner, '');
  assert.equal(fixture.shared.state.installedRuntimeSha256, null);
  assert.equal(fixture.shared.state.installedRuntimeTransactionId, null);
});

'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { Readable } = require('node:stream');

const { createManifest } = require('./fixtures/appliance-manifest-document');
const {
  CredentialSentinelProcessAudit,
  CredentialSentinelSmokeError,
  StreamingSecretScanner,
  createCredentialSentinel,
  publicCredentialSmokeError,
  runCredentialSentinelSmoke,
  scanReadable,
  wipeCredentialBundle,
} = require('../../electron/credential-sentinel-smoke');
const {
  EXECUTE_ARGUMENT,
  loadCredentialSmokeTargetManifest,
  parseArguments,
} = require('../../electron/run-credential-sentinel-smoke');

const TRUSTED_WSL = 'C:\\Windows\\System32\\wsl.exe';
const DISTRO = 'AppleMusicDownloader';
const TRANSACTION_ID = '1'.repeat(64);
const STATIC_SENTINEL = 'AMDL-CREDENTIAL-SENTINEL-0123456789abcdef0123456789abcdef0123456789abcdef';

function staticCredentialBundle() {
  const credentials = {
    appleEmail: `${STATIC_SENTINEL}@example.invalid`,
    applePassword: `${STATIC_SENTINEL}-apple-password`,
    mediaUserToken: `${STATIC_SENTINEL}-media-user-token`,
    authorizationToken: `${STATIC_SENTINEL}-authorization-token`,
  };
  return {
    base: STATIC_SENTINEL,
    credentials,
    needles: [STATIC_SENTINEL, ...Object.values(credentials)].map((value) => Buffer.from(value, 'utf8')),
  };
}

function createHarness({
  activeJobs = [],
  cleanupFailure = false,
  credentialStore = false,
  runtimeSha256 = '',
} = {}) {
  const manifest = createManifest();
  const expectedRuntimeSha256 = runtimeSha256 || manifest.runtime.sha256;
  const calls = [];
  let configuredCredentials = false;
  let running = true;
  let credentialInputObserved = false;
  const delegate = {
    exec: async (command, args, options) => {
      calls.push({ kind: 'exec', command, args: [...args], env: { ...(options.env || {}) } });
      return { stdout: '', stderr: '' };
    },
    input: async (command, args, input, options) => {
      const document = JSON.parse(Buffer.from(input).toString('utf8'));
      credentialInputObserved = args.at(-1) === 'credential-stdin'
        && Object.values(document.credentials || {}).every((value) => value.includes(STATIC_SENTINEL));
      calls.push({ kind: 'stdin', command, args: [...args], env: { ...(options.env || {}) } });
      return { stdout: '{"ok":true}', stderr: '' };
    },
    spawn: () => ({ stdout: Readable.from([]), stderr: Readable.from([]), once() {}, kill() {} }),
  };
  const audit = new CredentialSentinelProcessAudit(delegate);
  const state = {
    schemaVersion: 1,
    distroName: DISTRO,
    manifest,
    installedVersion: manifest.version,
    installedRuntimeSha256: expectedRuntimeSha256,
    installedRuntimeTransactionId: TRANSACTION_ID,
    installTransaction: null,
    updateTransaction: null,
    pendingManifest: null,
  };
  const status = () => ({
    service: 'applemusic-downloader',
    runtime: 'wsl-appliance',
    version: manifest.version,
    runtimeSha256: expectedRuntimeSha256,
    runtimeTransactionId: TRANSACTION_ID,
    updateInProgress: false,
    pendingTransactionId: '',
    configured: true,
    credentialsConfigured: configuredCredentials,
    state: running ? 'running' : 'stopped',
    pid: running ? 1200 : 0,
    supervisorPid: 1100,
  });
  const manager = {
    runtimeCredentials: null,
    readState: async () => state,
    registeredDistro: async () => ({ name: DISTRO, version: 2 }),
    verifyDistroOwnership: async () => ({
      appliance: {
        service: 'applemusic-downloader',
        runtime: 'wsl-appliance',
        version: manifest.version,
        state: 'available',
      },
    }),
    status: async () => status(),
    prepareLocalEndpoint: async () => {},
    activeJobs: async () => activeJobs,
    start: async (credentials) => {
      if (credentials !== undefined) {
        const payload = Buffer.from(JSON.stringify({ schemaVersion: 1, credentials }), 'utf8');
        try {
          await audit.input(
            TRUSTED_WSL,
            ['--distribution', DISTRO, '--exec', '/usr/local/bin/amdl-appliance-control', 'credential-stdin'],
            payload,
            { env: { PATH: 'C:\\Windows\\System32' }, secrets: Object.values(credentials) },
          );
          configuredCredentials = true;
          manager.runtimeCredentials = { ...credentials };
        } finally {
          payload.fill(0);
        }
      }
      running = true;
      return status();
    },
    stop: async () => {
      if (cleanupFailure) throw new Error('mock cleanup failed');
      await audit.exec(TRUSTED_WSL, ['--terminate', DISTRO], { env: { PATH: 'C:\\Windows\\System32' } });
      configuredCredentials = false;
      running = false;
      return { stopped: true };
    },
  };
  return {
    audit,
    calls,
    credentialInputObserved: () => credentialInputObserved,
    credentialsPresent: async () => credentialStore,
    manager,
    manifest,
  };
}

function successfulAuditors() {
  return {
    liveRuntimeAuditor: async () => ({
      processes: { processCount: 3, wrapperFDObserved: true },
      persistence: { scannedDatabaseFiles: 3 },
    }),
    persistenceAuditor: async () => ({ scannedDatabaseFiles: 3 }),
    apiAuditor: async () => ({ errorResponse: true, jobsResponse: true, sseTriggered: true }),
    artifactScanner: async () => ({ files: 4, decompressedArchives: 2 }),
  };
}

test('credential smoke sends all sentinels only through credential-stdin and restarts without credentials', async () => {
  const harness = createHarness();
  const result = await runCredentialSentinelSmoke({
    manager: harness.manager,
    targetManifest: harness.manifest,
    audit: harness.audit,
    credentialsPresent: harness.credentialsPresent,
    credentialFactory: staticCredentialBundle,
    ...successfulAuditors(),
  });

  assert.equal(result.ok, true);
  assert.equal(result.checks.stdinOnlyCredentialDelivery, true);
  assert.equal(result.cleanup.stoppedAndTerminated, true);
  assert.equal(result.cleanup.restartedWithoutCredentials, true);
  assert.equal(result.cleanup.credentialsConfigured, false);
  assert.equal(harness.credentialInputObserved(), true);
  assert.equal(harness.calls.filter((call) => call.kind === 'stdin').length, 1);
  assert.equal(harness.calls.filter((call) => call.args.includes('--terminate')).length, 1);
  assert.equal(JSON.stringify(harness.calls).includes(STATIC_SENTINEL), false);
  assert.equal(JSON.stringify(result).includes(STATIC_SENTINEL), false);
  assert.equal(harness.manager.runtimeCredentials, null);
});

test('process audit rejects sentinels in argv, environment, non-credential stdin and child output', async () => {
  const cleanDelegate = {
    exec: async () => ({ stdout: '', stderr: '' }),
    input: async () => ({ stdout: '', stderr: '' }),
    spawn: () => ({ once() {}, unref() {} }),
  };
  const bundle = staticCredentialBundle();
  const audit = new CredentialSentinelProcessAudit(cleanDelegate);
  audit.arm(bundle);
  await assert.rejects(
    audit.exec(TRUSTED_WSL, ['--list', STATIC_SENTINEL], { env: { PATH: 'safe' } }),
    (error) => error instanceof CredentialSentinelSmokeError && error.code === 'credential_leak',
  );
  await assert.rejects(
    audit.exec(TRUSTED_WSL, ['--list'], { env: { PATH: STATIC_SENTINEL } }),
    (error) => error instanceof CredentialSentinelSmokeError && error.code === 'credential_leak',
  );
  await assert.rejects(
    audit.input(TRUSTED_WSL, ['--exec', '/usr/bin/dd'], Buffer.from(STATIC_SENTINEL), { env: { PATH: 'safe' } }),
    (error) => error instanceof CredentialSentinelSmokeError && error.code === 'credential_leak',
  );

  const leakingOutputAudit = new CredentialSentinelProcessAudit({
    ...cleanDelegate,
    exec: async () => ({ stdout: `prefix ${STATIC_SENTINEL}`, stderr: '' }),
  });
  leakingOutputAudit.arm(bundle);
  await assert.rejects(
    leakingOutputAudit.exec(TRUSTED_WSL, ['--list'], { env: { PATH: 'safe' } }),
    (error) => error.code === 'credential_leak' && error.location === 'windows_child_output',
  );
  audit.disarm();
  leakingOutputAudit.disarm();
  wipeCredentialBundle(bundle);
});

test('streaming scanner detects a sentinel split across chunk boundaries without echoing it', async () => {
  const needle = Buffer.from(STATIC_SENTINEL, 'utf8');
  const split = Math.floor(needle.length / 2);
  await assert.rejects(
    scanReadable(Readable.from([
      Buffer.concat([Buffer.from('prefix-'), needle.subarray(0, split)]),
      Buffer.concat([needle.subarray(split), Buffer.from('-suffix')]),
    ]), [needle], 'host_artifact'),
    (error) => {
      assert.equal(error.code, 'credential_leak');
      assert.equal(error.location, 'host_artifact');
      assert.equal(error.message.includes(STATIC_SENTINEL), false);
      return true;
    },
  );

  const cleanScanner = new StreamingSecretScanner([needle], 'host_artifact');
  cleanScanner.push(Buffer.from('clean-first-half'));
  cleanScanner.push(Buffer.from('clean-second-half'));
  cleanScanner.close();
  needle.fill(0);
});

test('generated audit needles also detect UTF-16LE and base64 credential persistence', () => {
  const bundle = createCredentialSentinel(() => Buffer.alloc(24, 0x5a));
  const utf16 = Buffer.from(bundle.credentials.mediaUserToken, 'utf16le');
  const base64 = Buffer.from(Buffer.from(bundle.credentials.authorizationToken, 'utf8').toString('base64'), 'ascii');
  try {
    assert.throws(
      () => {
        const scanner = new StreamingSecretScanner(bundle.needles, 'wsl_jobs_db');
        try { scanner.push(utf16); } finally { scanner.close(); }
      },
      (error) => error.code === 'credential_leak' && error.location === 'wsl_jobs_db',
    );
    assert.throws(
      () => {
        const scanner = new StreamingSecretScanner(bundle.needles, 'host_artifact');
        try { scanner.push(base64); } finally { scanner.close(); }
      },
      (error) => error.code === 'credential_leak' && error.location === 'host_artifact',
    );
  } finally {
    utf16.fill(0);
    base64.fill(0);
    wipeCredentialBundle(bundle);
  }
});

test('cleanup failure overrides live success and prevents artifact success reporting', async () => {
  const harness = createHarness({ cleanupFailure: true });
  let artifactScanCalled = false;
  await assert.rejects(
    runCredentialSentinelSmoke({
      manager: harness.manager,
      targetManifest: harness.manifest,
      audit: harness.audit,
      credentialsPresent: harness.credentialsPresent,
      credentialFactory: staticCredentialBundle,
      ...successfulAuditors(),
      artifactScanner: async () => {
        artifactScanCalled = true;
        return { files: 0, decompressedArchives: 0 };
      },
    }),
    (error) => error instanceof CredentialSentinelSmokeError
      && error.code === 'cleanup_failed'
      && error.location === 'cleanup',
  );
  assert.equal(artifactScanCalled, false);
  assert.equal(harness.manager.runtimeCredentials, null);
});

test('old runtime, active jobs or a credential store block delivery before credential-stdin', async () => {
  const oldRuntime = createHarness({ runtimeSha256: 'a'.repeat(64) });
  await assert.rejects(
    runCredentialSentinelSmoke({
      manager: oldRuntime.manager,
      targetManifest: oldRuntime.manifest,
      audit: oldRuntime.audit,
      credentialsPresent: oldRuntime.credentialsPresent,
      credentialFactory: staticCredentialBundle,
      ...successfulAuditors(),
    }),
    (error) => error.code === 'precondition_failed' && error.location === 'runtime_identity',
  );
  assert.equal(oldRuntime.calls.length, 0);

  const busy = createHarness({ activeJobs: [{ id: 'queued', status: 'queued' }] });
  await assert.rejects(
    runCredentialSentinelSmoke({
      manager: busy.manager,
      targetManifest: busy.manifest,
      audit: busy.audit,
      credentialsPresent: busy.credentialsPresent,
      credentialFactory: staticCredentialBundle,
      ...successfulAuditors(),
    }),
    (error) => error.code === 'precondition_failed' && error.location === 'runtime_jobs',
  );
  assert.equal(busy.calls.length, 0);

  const stored = createHarness({ credentialStore: true });
  await assert.rejects(
    runCredentialSentinelSmoke({
      manager: stored.manager,
      targetManifest: stored.manifest,
      audit: stored.audit,
      credentialsPresent: stored.credentialsPresent,
      credentialFactory: staticCredentialBundle,
      ...successfulAuditors(),
    }),
    (error) => error.code === 'precondition_failed' && error.location === 'credentials_store',
  );
  assert.equal(stored.calls.length, 0);
});

test('public errors and the CLI confirmation gate never reveal a sentinel', () => {
  assert.equal(parseArguments([EXECUTE_ARGUMENT]), 'execute');
  assert.equal(parseArguments(['--help']), 'help');
  assert.throws(
    () => parseArguments([]),
    (error) => error.code === 'precondition_failed' && error.location === 'arguments',
  );
  const publicError = publicCredentialSmokeError(new CredentialSentinelSmokeError(
    'credential_leak',
    STATIC_SENTINEL,
    'wsl_jobs_db',
  ));
  assert.equal(JSON.stringify(publicError).includes(STATIC_SENTINEL), false);
  assert.equal(publicError.step, 'unknown');
  // Prove that callers cannot accidentally make an arbitrary secret location
  // public either.
  const sanitized = publicCredentialSmokeError(new CredentialSentinelSmokeError(
    'credential_leak',
    'scan',
    STATIC_SENTINEL,
  ));
  assert.equal(JSON.stringify(sanitized).includes(STATIC_SENTINEL), false);
});

test('credential smoke target uses the versioned dist manifest and never a stale generated fallback', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-credential-target-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const generatedDirectory = path.join(root, 'electron', 'generated');
  const distDirectory = path.join(root, 'appliance', 'dist');
  fs.mkdirSync(generatedDirectory, { recursive: true });
  fs.mkdirSync(distDirectory, { recursive: true });
  const stale = createManifest({
    runtime: { ...createManifest().runtime, sha256: 'c'.repeat(64) },
  });
  const target = createManifest({ version: '1.3.3' });
  fs.writeFileSync(path.join(generatedDirectory, 'appliance-manifest.json'), JSON.stringify(stale));
  fs.writeFileSync(
    path.join(distDirectory, 'applemusic-downloader-wsl-manifest-1.3.3-amd64.json'),
    JSON.stringify(target),
  );

  const selected = loadCredentialSmokeTargetManifest(root);
  assert.equal(selected.runtime.sha256, target.runtime.sha256);
  assert.notEqual(selected.runtime.sha256, stale.runtime.sha256);

  fs.rmSync(path.join(distDirectory, 'applemusic-downloader-wsl-manifest-1.3.3-amd64.json'));
  assert.throws(
    () => loadCredentialSmokeTargetManifest(root),
    (error) => error.code === 'precondition_failed'
      && error.step === 'target_manifest'
      && error.location === 'runtime_identity',
  );
});

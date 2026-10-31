'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');
const zlib = require('node:zlib');

const { validateApplianceManifest } = require('./appliance-manifest');
const {
  EXPECTED_RUNTIME,
  EXPECTED_SERVICE,
  applianceIdentityMatches,
} = require('./wsl-manager');

const DISTRO_NAME = 'AppleMusicDownloader';
const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const ACTIVE_STATES = new Set(['queued', 'running', 'cancelling']);
const SENSITIVE_ENVIRONMENT_KEY = /^(?:APPLE_(?:EMAIL|PASSWORD)|MEDIA_USER_TOKEN|AUTHORIZATION_TOKEN|AMDL_.*(?:TOKEN|PASSWORD|SECRET))$/i;
const FORBIDDEN_ARGUMENTS = new Set(['--unregister', '--shutdown', '--import', '--export', 'update-stdin']);
const CREDENTIAL_FIELDS = Object.freeze([
  'appleEmail',
  'applePassword',
  'mediaUserToken',
  'authorizationToken',
]);
const PUBLIC_LOCATIONS = new Set([
  'arguments',
  'credentials_store',
  'host_artifact',
  'host_artifact_compressed',
  'localhost_api_error',
  'localhost_api_jobs',
  'localhost_api_sse',
  'process_audit',
  'runtime_identity',
  'runtime_jobs',
  'runtime_status',
  'windows_child_output',
  'wsl_diagnostics',
  'wsl_journal',
  'wsl_jobs_db',
  'wsl_jobs_db_shm',
  'wsl_jobs_db_wal',
  'wsl_proc_backend_cmdline',
  'wsl_proc_backend_environ',
  'wsl_proc_supervisor_cmdline',
  'wsl_proc_supervisor_environ',
  'wsl_proc_wrapper_cmdline',
  'wsl_proc_wrapper_environ',
  'cleanup',
]);

const ERROR_TEXT = Object.freeze({
  credential_leak: ['凭据 sentinel 审计发现秘密出现在禁止位置。', '保留脱敏位置并修复泄漏；不要保存或发布本次产物。'],
  precondition_failed: ['凭据 sentinel smoke 的安全前置条件不满足。', '先完成目标 runtime 原地更新，并确认所有权、版本和空闲队列。'],
  process_audit_failed: ['Windows 子进程未遵守 stdin-only 凭据边界。', '检查 WslManager 的 argv、环境和 credential-stdin 调用。'],
  runtime_audit_failed: ['WSL runtime 凭据审计未完成。', '查看脱敏位置，修复后重新构建 runtime。'],
  cleanup_failed: ['凭据 sentinel smoke 未能确认安全清理。', '不要继续发布；先停止专用发行版并确认无凭据重启。'],
  credential_smoke_failed: ['凭据 sentinel smoke 未通过。', '根据脱敏位置修复后重试。'],
});

class CredentialSentinelSmokeError extends Error {
  constructor(code, step, location = '') {
    const publicCode = Object.prototype.hasOwnProperty.call(ERROR_TEXT, code) ? code : 'credential_smoke_failed';
    const text = ERROR_TEXT[publicCode];
    super(text[0]);
    this.name = 'CredentialSentinelSmokeError';
    this.code = publicCode;
    this.step = typeof step === 'string' && /^[a-z][a-z0-9_]{0,63}$/.test(step) ? step : 'unknown';
    this.action = text[1];
    this.location = PUBLIC_LOCATIONS.has(location) ? location : '';
  }
}

function smokeError(code, step, location = '') {
  return new CredentialSentinelSmokeError(code, step, location);
}

function publicCredentialSmokeError(error) {
  const normalized = error instanceof CredentialSentinelSmokeError
    ? error
    : smokeError('credential_smoke_failed', 'unknown');
  return {
    code: normalized.code,
    message: normalized.message,
    action: normalized.action,
    step: normalized.step,
    ...(normalized.location ? { location: normalized.location } : {}),
  };
}

function asNeedleBuffer(value) {
  if (Buffer.isBuffer(value)) return Buffer.from(value);
  if (typeof value !== 'string') throw new TypeError('Secret needles must be strings or Buffers');
  return Buffer.from(value, 'utf8');
}

function normalizeNeedles(values) {
  const unique = [];
  const seen = new Set();
  for (const value of values || []) {
    const needle = asNeedleBuffer(value);
    if (needle.length === 0 || needle.length > 4096) {
      needle.fill(0);
      throw new TypeError('Secret needles must contain between 1 and 4096 bytes');
    }
    const key = needle.toString('base64');
    if (seen.has(key)) {
      needle.fill(0);
      continue;
    }
    seen.add(key);
    unique.push(needle);
  }
  if (unique.length === 0) throw new TypeError('At least one secret needle is required');
  return unique;
}

function wipeBuffers(values) {
  for (const value of values || []) {
    if (Buffer.isBuffer(value)) value.fill(0);
  }
}

function bufferContainsNeedle(value, needles) {
  const source = Buffer.isBuffer(value) ? value : Buffer.from(String(value || ''), 'utf8');
  try {
    return needles.some((needle) => source.includes(needle));
  } finally {
    if (!Buffer.isBuffer(value)) source.fill(0);
  }
}

class StreamingSecretScanner {
  constructor(needles, location) {
    this.needles = needles;
    this.location = PUBLIC_LOCATIONS.has(location) ? location : 'host_artifact';
    this.maximumNeedleLength = Math.max(...needles.map((needle) => needle.length));
    this.carry = Buffer.alloc(0);
    this.closed = false;
  }

  push(chunk) {
    if (this.closed) throw new Error('Secret scanner is already closed');
    const current = Buffer.isBuffer(chunk) ? Buffer.from(chunk) : Buffer.from(chunk || '');
    const window = Buffer.concat([this.carry, current]);
    this.carry.fill(0);
    current.fill(0);
    try {
      if (this.needles.some((needle) => window.includes(needle))) {
        throw smokeError('credential_leak', 'scan', this.location);
      }
      const overlap = Math.min(Math.max(0, this.maximumNeedleLength - 1), window.length);
      this.carry = overlap === 0 ? Buffer.alloc(0) : Buffer.from(window.subarray(window.length - overlap));
    } finally {
      window.fill(0);
    }
  }

  close() {
    this.closed = true;
    this.carry.fill(0);
    this.carry = Buffer.alloc(0);
  }
}

async function scanReadable(readable, needles, location, { captureLimit = 0 } = {}) {
  const scanner = new StreamingSecretScanner(needles, location);
  const captured = [];
  let capturedBytes = 0;
  try {
    for await (const chunk of readable) {
      scanner.push(chunk);
      if (captureLimit > 0) {
        capturedBytes += chunk.length;
        if (capturedBytes > captureLimit) throw smokeError('runtime_audit_failed', 'scan_limit', location);
        captured.push(Buffer.from(chunk));
      }
    }
    return captureLimit > 0 ? Buffer.concat(captured) : null;
  } finally {
    scanner.close();
    // Buffer.concat returns an independent result. Always erase the per-chunk
    // copies, including when iteration or a size check failed part-way through.
    wipeBuffers(captured);
  }
}

function scanBuffer(value, needles, location) {
  const scanner = new StreamingSecretScanner(needles, location);
  try {
    scanner.push(value);
  } finally {
    scanner.close();
  }
}

function createCredentialSentinel(randomBytes = crypto.randomBytes) {
  const entropy = randomBytes(24);
  if (!Buffer.isBuffer(entropy) || entropy.length !== 24) throw new TypeError('Sentinel entropy source returned invalid data');
  const base = `AMDL-CREDENTIAL-SENTINEL-${entropy.toString('hex')}`;
  entropy.fill(0);
  const credentials = {
    appleEmail: `${base}@example.invalid`,
    applePassword: `${base}-apple-password`,
    mediaUserToken: `${base}-media-user-token`,
    authorizationToken: `${base}-authorization-token`,
  };
  let envelope = JSON.stringify({ schemaVersion: 1, credentials });
  const encodedNeedles = [];
  for (const value of [base, ...Object.values(credentials), envelope]) {
    const utf8 = Buffer.from(value, 'utf8');
    encodedNeedles.push(
      utf8,
      Buffer.from(value, 'utf16le'),
      Buffer.from(utf8.toString('base64'), 'ascii'),
    );
  }
  const needles = normalizeNeedles(encodedNeedles);
  wipeBuffers(encodedNeedles);
  envelope = '';
  return {
    base,
    credentials,
    needles,
  };
}

function wipeCredentialBundle(bundle) {
  if (!bundle) return;
  if (bundle.credentials) {
    for (const field of CREDENTIAL_FIELDS) bundle.credentials[field] = '';
  }
  bundle.base = '';
  wipeBuffers(bundle.needles);
  bundle.needles = [];
}

function exactCredentialEnvelope(input, expectedCredentials) {
  const source = Buffer.from(input);
  let parsed;
  try {
    parsed = JSON.parse(source.toString('utf8'));
  } catch {
    source.fill(0);
    return false;
  }
  try {
    if (!parsed || parsed.schemaVersion !== 1 || !parsed.credentials || Array.isArray(parsed.credentials)) return false;
    if (Object.keys(parsed).sort().join(',') !== 'credentials,schemaVersion') return false;
    if (Object.keys(parsed.credentials).sort().join(',') !== [...CREDENTIAL_FIELDS].sort().join(',')) return false;
    return CREDENTIAL_FIELDS.every((field) => parsed.credentials[field] === expectedCredentials[field]);
  } finally {
    source.fill(0);
    if (parsed?.credentials) {
      for (const field of CREDENTIAL_FIELDS) parsed.credentials[field] = '';
    }
  }
}

class CredentialSentinelProcessAudit {
  constructor(delegate) {
    if (!delegate || typeof delegate.exec !== 'function' || typeof delegate.input !== 'function' || typeof delegate.spawn !== 'function') {
      throw new TypeError('CredentialSentinelProcessAudit requires exec, input and spawn functions');
    }
    this.delegate = delegate;
    this.needles = [];
    this.expectedCredentials = null;
    this.armed = false;
    this.cleanup = false;
    this.records = [];
    this.credentialDocuments = 0;
    this.terminateCommands = 0;
  }

  arm(bundle) {
    if (this.armed) throw smokeError('process_audit_failed', 'arm', 'process_audit');
    this.needles = normalizeNeedles(bundle.needles);
    this.expectedCredentials = { ...bundle.credentials };
    this.armed = true;
  }

  beginCleanup() {
    this.cleanup = true;
  }

  disarm() {
    wipeBuffers(this.needles);
    this.needles = [];
    if (this.expectedCredentials) {
      for (const field of CREDENTIAL_FIELDS) this.expectedCredentials[field] = '';
    }
    this.expectedCredentials = null;
    this.armed = false;
    this.cleanup = false;
  }

  assertNoSecret(value, location = 'process_audit') {
    if (this.armed && bufferContainsNeedle(value, this.needles)) {
      throw smokeError('credential_leak', 'process_audit', location);
    }
  }

  observe(kind, command, args, options = {}, input) {
    if (typeof command !== 'string' || !Array.isArray(args)) throw smokeError('process_audit_failed', 'arguments', 'process_audit');
    const binary = path.win32.basename(command).toLowerCase();
    if (!['wsl.exe', 'amdl-windows-probe.exe'].includes(binary)) {
      throw smokeError('process_audit_failed', 'binary', 'process_audit');
    }
    if (args.some((argument) => FORBIDDEN_ARGUMENTS.has(String(argument).toLowerCase()))) {
      throw smokeError('process_audit_failed', 'forbidden_command', 'process_audit');
    }
    if (args.includes('--terminate')) {
      if (!this.cleanup || args.length !== 2 || args[1] !== DISTRO_NAME) {
        throw smokeError('process_audit_failed', 'terminate', 'process_audit');
      }
      this.terminateCommands += 1;
    }
    const environment = options.env === undefined ? process.env : options.env;
    if (Object.keys(environment || {}).some((key) => SENSITIVE_ENVIRONMENT_KEY.test(key))) {
      throw smokeError('process_audit_failed', 'environment', 'process_audit');
    }
    if (this.armed) {
      this.assertNoSecret(command);
      this.assertNoSecret(args.join('\u0000'));
      this.assertNoSecret(Object.entries(environment || {}).map(([key, value]) => `${key}=${value}`).join('\u0000'));
    }

    if (kind === 'stdin') {
      const operation = String(args.at(-1) || '').toLowerCase();
      const inputContainsSecret = this.armed && bufferContainsNeedle(input, this.needles);
      if (operation === 'credential-stdin') {
        if (!this.armed || !exactCredentialEnvelope(input, this.expectedCredentials)) {
          throw smokeError('process_audit_failed', 'credential_document', 'process_audit');
        }
        const redactionSecrets = new Set((options.secrets || []).map((value) => String(value)));
        if (!CREDENTIAL_FIELDS.every((field) => redactionSecrets.has(this.expectedCredentials[field]))) {
          throw smokeError('process_audit_failed', 'credential_redaction', 'process_audit');
        }
        this.credentialDocuments += 1;
      } else if (inputContainsSecret) {
        throw smokeError('credential_leak', 'stdin_route', 'process_audit');
      }
    }
    this.records.push({ kind, binary });
  }

  inspectResult(result) {
    if (!this.armed) return;
    for (const value of [result?.stdout, result?.stderr, result?.message]) {
      if (value !== undefined) this.assertNoSecret(value, 'windows_child_output');
    }
  }

  async exec(command, args, options = {}) {
    this.observe('exec', command, args, options);
    try {
      const result = await this.delegate.exec(command, args, options);
      this.inspectResult(result);
      return result;
    } catch (error) {
      this.inspectResult(error);
      throw error;
    }
  }

  async input(command, args, input, options = {}) {
    this.observe('stdin', command, args, options, input);
    try {
      const result = await this.delegate.input(command, args, input, options);
      this.inspectResult(result);
      return result;
    } catch (error) {
      this.inspectResult(error);
      throw error;
    }
  }

  spawn(command, args, options = {}) {
    this.observe('spawn', command, args, options);
    return this.delegate.spawn(command, args, options);
  }

  summary() {
    return {
      commandCount: this.records.length,
      credentialDocuments: this.credentialDocuments,
      terminateCommands: this.terminateCommands,
      argvSecretMatches: 0,
      environmentSecretMatches: 0,
    };
  }
}

function assertTargetStatus(status, state, manifest, step) {
  if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, manifest.version)
      || status.runtimeSha256 !== manifest.runtime.sha256
      || status.runtimeTransactionId !== state.installedRuntimeTransactionId
      || status.updateInProgress !== false
      || Boolean(status.pendingTransactionId)
      || status.configured !== true
      || status.state !== 'running') {
    throw smokeError('precondition_failed', step, 'runtime_identity');
  }
}

async function verifyCredentialSentinelPreconditions(manager, targetManifest, { credentialsPresent = false } = {}) {
  let manifest;
  try {
    manifest = validateApplianceManifest(targetManifest);
  } catch {
    throw smokeError('precondition_failed', 'target_manifest', 'runtime_identity');
  }
  const state = await manager.readState();
  let installedManifest;
  try {
    installedManifest = validateApplianceManifest(state.manifest);
  } catch {
    throw smokeError('precondition_failed', 'installed_manifest', 'runtime_identity');
  }
  if (manifest.distroName !== DISTRO_NAME
      || manifest.version !== state.installedVersion
      || installedManifest.version !== manifest.version
      || installedManifest.runtime.sha256 !== manifest.runtime.sha256
      || state.installedRuntimeSha256 !== manifest.runtime.sha256
      || !SHA256_PATTERN.test(state.installedRuntimeTransactionId || '')
      || state.installTransaction != null
      || state.updateTransaction != null
      || state.pendingManifest != null
      || credentialsPresent) {
    throw smokeError('precondition_failed', 'host_state', credentialsPresent ? 'credentials_store' : 'runtime_identity');
  }
  const distro = await manager.registeredDistro();
  if (!distro || distro.name !== DISTRO_NAME || distro.version !== 2) {
    throw smokeError('precondition_failed', 'distro', 'runtime_identity');
  }
  let owned;
  try {
    owned = await manager.verifyDistroOwnership({ expectedVersion: manifest.version });
  } catch {
    throw smokeError('precondition_failed', 'ownership', 'runtime_identity');
  }
  if (!applianceIdentityMatches(owned.appliance, EXPECTED_SERVICE, EXPECTED_RUNTIME, manifest.version)) {
    throw smokeError('precondition_failed', 'owned_runtime', 'runtime_identity');
  }
  const status = await manager.status();
  assertTargetStatus(status, state, manifest, 'status');
  if (status.credentialsConfigured !== false) {
    throw smokeError('precondition_failed', 'credentials', 'runtime_status');
  }
  try {
    await manager.prepareLocalEndpoint({ expectedVersion: manifest.version });
    const active = await manager.activeJobs();
    if (!Array.isArray(active) || active.length !== 0) {
      throw smokeError('precondition_failed', 'active_jobs', 'runtime_jobs');
    }
  } catch (error) {
    if (error instanceof CredentialSentinelSmokeError) throw error;
    throw smokeError('precondition_failed', 'active_jobs', 'runtime_jobs');
  }
  return { manifest, state, status };
}

function artifactLocation(filename) {
  const name = path.basename(filename).toLowerCase();
  if (name.includes('rootfs')) return 'host_artifact';
  if (name.includes('runtime')) return 'host_artifact';
  return 'host_artifact';
}

async function discoverHostArtifacts(repositoryRoot, { additionalRoots = [] } = {}) {
  const roots = [
    path.join(repositoryRoot, 'appliance', 'dist'),
    path.join(repositoryRoot, 'dist'),
    ...additionalRoots,
  ];
  const matches = [];
  const accepted = /(?:applemusic-downloader-(?:wsl-rootfs|wsl-runtime|runtime)-.*\.tar\.gz|applemusicdownloader-(?:offline-)?setup(?:\.[^.]+)?\.exe|app\.asar|appliance-manifest\.json|applemusic-downloader-wsl-manifest-.*\.json)$/i;
  async function visit(directory) {
    let entries;
    try {
      entries = await fsp.readdir(directory, { withFileTypes: true });
    } catch (error) {
      if (error.code === 'ENOENT') return;
      throw error;
    }
    for (const entry of entries.sort((left, right) => left.name.localeCompare(right.name))) {
      const filename = path.join(directory, entry.name);
      const info = await fsp.lstat(filename);
      if (info.isSymbolicLink()) continue;
      if (info.isDirectory()) await visit(filename);
      else if (info.isFile() && accepted.test(entry.name)) matches.push(filename);
    }
  }
  for (const root of roots) await visit(root);
  return [...new Set(matches)];
}

async function scanHostArtifacts(artifactPaths, needles) {
  let files = 0;
  let decompressedArchives = 0;
  for (const filename of artifactPaths || []) {
    const info = await fsp.lstat(filename);
    if (!info.isFile() || info.isSymbolicLink()) throw smokeError('runtime_audit_failed', 'artifact_identity', 'host_artifact');
    await scanReadable(fs.createReadStream(filename), needles, artifactLocation(filename));
    files += 1;
    if (filename.toLowerCase().endsWith('.tar.gz')) {
      const gunzip = zlib.createGunzip();
      const input = fs.createReadStream(filename);
      input.on('error', (error) => gunzip.destroy(error));
      input.pipe(gunzip);
      await scanReadable(gunzip, needles, 'host_artifact_compressed');
      decompressedArchives += 1;
    }
  }
  return { files, decompressedArchives };
}

async function spawnWslAndScan(manager, command, args, needles, location, { captureLimit = 0 } = {}) {
  const executable = await manager.resolveWslExecutable();
  const commandArgs = [
    '--distribution', DISTRO_NAME,
    '--user', 'root',
    '--exec', command,
    ...args,
  ];
  let child;
  try {
    child = manager.runner.spawn(executable, commandArgs, {
      windowsHide: true,
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
      env: manager.processEnvironment,
    });
  } catch {
    throw smokeError('runtime_audit_failed', 'wsl_spawn', location);
  }
  if (!child?.stdout || !child?.stderr || typeof child.once !== 'function') {
    child?.kill?.();
    throw smokeError('runtime_audit_failed', 'wsl_spawn', location);
  }
  const close = new Promise((resolve, reject) => {
    child.once('error', () => reject(smokeError('runtime_audit_failed', 'wsl_process', location)));
    child.once('close', (code) => {
      if (code === 0) resolve();
      else reject(smokeError('runtime_audit_failed', 'wsl_process', location));
    });
  });
  let captured;
  try {
    [captured] = await Promise.all([
      scanReadable(child.stdout, needles, location, { captureLimit }),
      scanReadable(child.stderr, needles, location),
      close,
    ]);
    return captured;
  } catch (error) {
    child.kill?.();
    throw error;
  }
}

function validPID(value) {
  const number = Number(value);
  return Number.isSafeInteger(number) && number > 1 && number <= 4_194_304 ? number : 0;
}

async function scanProcFile(manager, pid, leaf, needles, location) {
  const normalizedPID = validPID(pid);
  if (!normalizedPID || !['cmdline', 'environ'].includes(leaf)) {
    throw smokeError('runtime_audit_failed', 'proc_identity', location);
  }
  return spawnWslAndScan(
    manager,
    '/usr/bin/cat',
    [`/proc/${normalizedPID}/${leaf}`],
    needles,
    location,
    { captureLimit: 256 * 1024 },
  );
}

async function wrapperPIDs(manager) {
  try {
    const result = await manager.runWsl([
      '--distribution', DISTRO_NAME,
      '--user', 'root',
      '--exec', '/usr/bin/pgrep', '-x', 'wrapper',
    ], { timeoutMs: 10_000 });
    return String(result.stdout || '').split(/\s+/).map(validPID).filter(Boolean);
  } catch (error) {
    if (error instanceof CredentialSentinelSmokeError) throw error;
    return [];
  }
}

function includesPublicMarker(buffer, marker) {
  return Boolean(buffer && buffer.includes(Buffer.from(marker, 'utf8')));
}

async function auditWslProcesses(manager, needles, status) {
  const backendPID = validPID(status.pid);
  const supervisorPID = validPID(status.supervisorPid);
  if (!backendPID || !supervisorPID) throw smokeError('runtime_audit_failed', 'process_ids', 'runtime_status');
  let processCount = 0;
  let wrapperFDObserved = false;
  const scanTarget = async (pid, kind) => {
    let commandLine;
    let environment;
    const commandLocation = `wsl_proc_${kind}_cmdline`;
    const environmentLocation = `wsl_proc_${kind}_environ`;
    try {
      commandLine = await scanProcFile(manager, pid, 'cmdline', needles, commandLocation);
      environment = await scanProcFile(manager, pid, 'environ', needles, environmentLocation);
      if (kind === 'wrapper'
          && includesPublicMarker(commandLine, '--login-fd\u0000')
          && includesPublicMarker(commandLine, '\u00003\u0000')) {
        wrapperFDObserved = true;
      }
      processCount += 1;
    } finally {
      commandLine?.fill(0);
      environment?.fill(0);
    }
  };
  await scanTarget(supervisorPID, 'supervisor');
  await scanTarget(backendPID, 'backend');

  // Invalid synthetic login data can make Wrapper exit and enter its bounded
  // restart backoff between pgrep and /proc reads. Retry only transient
  // process-read failures; a detected credential leak is never retried or
  // downgraded.
  for (let attempt = 0; attempt < 20 && !wrapperFDObserved; attempt += 1) {
    const wrappers = await wrapperPIDs(manager);
    for (const pid of wrappers) {
      try {
        await scanTarget(pid, 'wrapper');
      } catch (error) {
        if (!(error instanceof CredentialSentinelSmokeError
            && error.code === 'runtime_audit_failed'
            && error.step === 'wsl_process')) throw error;
        // The process may have exited after pgrep; obtain a fresh PID below.
      }
      if (wrapperFDObserved) break;
    }
    if (!wrapperFDObserved) await manager.sleep(250);
  }
  if (!wrapperFDObserved) throw smokeError('runtime_audit_failed', 'wrapper_fd', 'wsl_proc_wrapper_cmdline');
  return { processCount, wrapperFDObserved };
}

async function wslFileExists(manager, filename) {
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME,
      '--user', 'root',
      '--exec', '/usr/bin/test', '-f', filename,
    ], { timeoutMs: 10_000 });
    return true;
  } catch (error) {
    if (error instanceof CredentialSentinelSmokeError) throw error;
    return false;
  }
}

async function auditWslPersistence(manager, needles) {
  const diagnostics = await manager.diagnostics();
  scanBuffer(diagnostics, needles, 'wsl_diagnostics');
  await spawnWslAndScan(manager, '/usr/bin/journalctl', [
    '--no-pager', '--unit', 'amdl-appliance.service', '--lines', '200', '--output', 'cat',
  ], needles, 'wsl_journal');
  const databaseFiles = [
    ['/var/lib/applemusic-downloader/data/jobs.db', 'wsl_jobs_db', true],
    ['/var/lib/applemusic-downloader/data/jobs.db-wal', 'wsl_jobs_db_wal', false],
    ['/var/lib/applemusic-downloader/data/jobs.db-shm', 'wsl_jobs_db_shm', false],
  ];
  let scannedDatabaseFiles = 0;
  for (const [filename, location, required] of databaseFiles) {
    const exists = await wslFileExists(manager, filename);
    if (!exists && required) throw smokeError('runtime_audit_failed', 'database_missing', location);
    if (!exists) continue;
    await spawnWslAndScan(manager, '/usr/bin/cat', [filename], needles, location);
    scannedDatabaseFiles += 1;
  }
  return { diagnostics: true, journal: true, scannedDatabaseFiles };
}

async function auditLiveWslRuntime(manager, needles, expectedRuntime) {
  const status = await manager.status();
  if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, expectedRuntime.version)
      || status.runtimeSha256 !== expectedRuntime.runtimeSha256
      || status.runtimeTransactionId !== expectedRuntime.runtimeTransactionId
      || status.credentialsConfigured !== true
      || status.state !== 'running') {
    throw smokeError('runtime_audit_failed', 'credential_status', 'runtime_status');
  }
  const processes = await auditWslProcesses(manager, needles, status);
  const persistence = await auditWslPersistence(manager, needles);
  return { processes, persistence };
}

function scanResponse(response, needles, location) {
  scanBuffer(response?.body || '', needles, location);
  scanBuffer(JSON.stringify(response?.payload || null), needles, location);
}

function streamLocalResponse(url, headers, needles, location, { timeoutMs = 2_000, maxBytes = 1024 * 1024 } = {}) {
  return new Promise((resolve, reject) => {
    let parsed;
    try {
      parsed = new URL(url);
    } catch {
      reject(smokeError('runtime_audit_failed', 'api_url', location));
      return;
    }
    if (parsed.protocol !== 'http:' || parsed.hostname !== '127.0.0.1') {
      reject(smokeError('runtime_audit_failed', 'api_url', location));
      return;
    }
    const scanner = new StreamingSecretScanner(needles, location);
    let bytes = 0;
    let settled = false;
    let responseStarted = false;
    let responseStatus = 0;
    const finish = (error, result) => {
      if (settled) return;
      settled = true;
      scanner.close();
      clearTimeout(timer);
      if (error) reject(error);
      else resolve(result);
    };
    const request = http.get(parsed, { headers }, (response) => {
      responseStarted = true;
      responseStatus = response.statusCode || 0;
      try {
        scanBuffer(JSON.stringify(response.headers || {}), needles, location);
      } catch (error) {
        request.destroy();
        finish(error);
        return;
      }
      response.on('data', (chunk) => {
        if (settled) return;
        try {
          bytes += chunk.length;
          if (bytes > maxBytes) throw smokeError('runtime_audit_failed', 'api_limit', location);
          scanner.push(chunk);
        } catch (error) {
          request.destroy();
          finish(error);
        }
      });
      response.on('end', () => finish(null, { statusCode: response.statusCode || 0, bytes, timedOut: false }));
      response.on('aborted', () => finish(smokeError('runtime_audit_failed', 'api_stream', location)));
      response.on('error', () => finish(smokeError('runtime_audit_failed', 'api_stream', location)));
    });
    request.on('error', () => finish(smokeError('runtime_audit_failed', 'api_stream', location)));
    const timer = setTimeout(() => {
      finish(null, { statusCode: responseStarted ? responseStatus : 0, bytes, timedOut: true });
      request.destroy();
    }, timeoutMs);
  });
}

async function auditLocalAPI(manager, needles) {
  if (!manager.serviceBaseUrl) throw smokeError('runtime_audit_failed', 'api_endpoint', 'localhost_api_error');
  const headers = manager.desktopProxyRequestHeaders();
  const errorResponse = await manager.request(`${manager.serviceBaseUrl}/api/v1/jobs/__amdl_credential_smoke_missing__`, {
    timeoutMs: 5_000,
    headers,
  });
  scanResponse(errorResponse, needles, 'localhost_api_error');
  if (errorResponse.statusCode !== 404) throw smokeError('runtime_audit_failed', 'api_error_status', 'localhost_api_error');

  const listResponse = await manager.request(`${manager.serviceBaseUrl}/api/v1/jobs?limit=100`, {
    timeoutMs: 5_000,
    headers,
  });
  scanResponse(listResponse, needles, 'localhost_api_jobs');
  if (listResponse.statusCode !== 200) throw smokeError('runtime_audit_failed', 'api_jobs_status', 'localhost_api_jobs');
  const jobs = Array.isArray(listResponse.payload) ? listResponse.payload : listResponse.payload?.jobs || [];
  const terminal = jobs.find((job) => typeof job?.id === 'string'
    && /^[0-9A-Za-z_-]{1,128}$/.test(job.id)
    && !ACTIVE_STATES.has(job.status));
  let sseTriggered = false;
  if (terminal) {
    const result = await streamLocalResponse(
      `${manager.serviceBaseUrl}/api/v1/jobs/${encodeURIComponent(terminal.id)}/events`,
      { ...headers, 'Last-Event-ID': '0', Accept: 'text/event-stream' },
      needles,
      'localhost_api_sse',
    );
    if (result.statusCode !== 200) throw smokeError('runtime_audit_failed', 'api_sse_status', 'localhost_api_sse');
    sseTriggered = true;
  }
  return { errorResponse: true, jobsResponse: true, sseTriggered };
}

async function cleanupCredentialRuntime(manager, expectedRuntime, needles, audit, {
  credentialsPresent,
  persistenceAuditor = auditWslPersistence,
} = {}) {
  const failures = [];
  audit?.beginCleanup?.();
  manager.runtimeCredentials = null;
  try {
    const active = await manager.activeJobs();
    if (!Array.isArray(active) || active.length !== 0) failures.push('active');
  } catch {
    failures.push('active');
  }
  try {
    await manager.stop();
  } catch {
    failures.push('stop');
  }
  if (!failures.includes('stop')) {
    try {
      await manager.start(undefined, { expectedVersion: expectedRuntime.version });
      const status = await manager.status();
      if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, expectedRuntime.version)
          || status.runtimeSha256 !== expectedRuntime.runtimeSha256
          || status.runtimeTransactionId !== expectedRuntime.runtimeTransactionId
          || status.credentialsConfigured !== false) failures.push('credential_state');
      const active = await manager.activeJobs();
      if (!Array.isArray(active) || active.length !== 0) failures.push('active_after_restart');
      await persistenceAuditor(manager, needles);
    } catch {
      failures.push('restart');
    }
  }
  try {
    if (await credentialsPresent()) failures.push('credentials_store');
  } catch {
    failures.push('credentials_store');
  }
  manager.runtimeCredentials = null;
  if (failures.length > 0 || audit?.summary?.().terminateCommands !== 1) {
    throw smokeError('cleanup_failed', 'cleanup', 'cleanup');
  }
  return { stoppedAndTerminated: true, restartedWithoutCredentials: true, credentialsConfigured: false };
}

async function runCredentialSentinelSmoke({
  manager,
  targetManifest,
  audit,
  credentialsPresent = async () => false,
  credentialFactory = createCredentialSentinel,
  liveRuntimeAuditor = auditLiveWslRuntime,
  persistenceAuditor = auditWslPersistence,
  apiAuditor = auditLocalAPI,
  artifactPaths = [],
  artifactScanner = scanHostArtifacts,
} = {}) {
  if (!manager || !audit) throw new TypeError('manager and audit are required');
  if (await credentialsPresent()) throw smokeError('precondition_failed', 'credentials_store', 'credentials_store');
  const initial = await verifyCredentialSentinelPreconditions(manager, targetManifest, { credentialsPresent: false });
  const bundle = credentialFactory();
  let attempted = false;
  let primaryError = null;
  let live = null;
  let api = null;
  let cleanup = null;
  let artifacts = null;
  try {
    audit.arm(bundle);
    const storeAppeared = await credentialsPresent();
    await verifyCredentialSentinelPreconditions(manager, targetManifest, { credentialsPresent: storeAppeared });
    attempted = true;
    const started = await manager.start(bundle.credentials, { expectedVersion: initial.manifest.version });
    if (!applianceIdentityMatches(started, EXPECTED_SERVICE, EXPECTED_RUNTIME, initial.manifest.version)
        || started.runtimeSha256 !== initial.manifest.runtime.sha256
        || started.runtimeTransactionId !== initial.state.installedRuntimeTransactionId
        || started.credentialsConfigured !== true
        || audit.summary().credentialDocuments !== 1) {
      throw smokeError('runtime_audit_failed', 'credential_start', 'runtime_status');
    }
    const expectedRuntime = {
      version: initial.manifest.version,
      runtimeSha256: initial.manifest.runtime.sha256,
      runtimeTransactionId: initial.state.installedRuntimeTransactionId,
    };
    live = await liveRuntimeAuditor(manager, bundle.needles, expectedRuntime);
    api = await apiAuditor(manager, bundle.needles);
  } catch (error) {
    primaryError = error instanceof CredentialSentinelSmokeError
      ? error
      : smokeError('credential_smoke_failed', 'live');
  } finally {
    if (attempted) {
      try {
        cleanup = await cleanupCredentialRuntime(manager, {
          version: initial.manifest.version,
          runtimeSha256: initial.manifest.runtime.sha256,
          runtimeTransactionId: initial.state.installedRuntimeTransactionId,
        }, bundle.needles, audit, {
          credentialsPresent,
          persistenceAuditor,
        });
      } catch (error) {
        primaryError = error instanceof CredentialSentinelSmokeError
          ? error
          : smokeError('cleanup_failed', 'cleanup', 'cleanup');
      }
    }
  }
  try {
    if (!primaryError) artifacts = await artifactScanner(artifactPaths, bundle.needles);
  } catch (error) {
    primaryError = error instanceof CredentialSentinelSmokeError
      ? error
      : smokeError('runtime_audit_failed', 'artifacts', 'host_artifact');
  } finally {
    wipeCredentialBundle(bundle);
    audit.disarm?.();
  }
  if (primaryError) throw primaryError;
  const finalAudit = audit.summary();
  if (finalAudit.credentialDocuments !== 1 || finalAudit.terminateCommands !== 1) {
    throw smokeError('process_audit_failed', 'final_audit', 'process_audit');
  }
  return {
    schemaVersion: 1,
    smoke: 'applemusic-downloader-credential-sentinel',
    ok: true,
    checks: {
      ownedTargetRuntime: true,
      zeroActiveJobs: true,
      stdinOnlyCredentialDelivery: true,
      inheritedWrapperFDObserved: live?.processes?.wrapperFDObserved === true,
      processArgvAndEnvironmentClean: true,
      diagnosticsLogsAndDatabaseClean: true,
      localAPIErrorClean: api?.errorResponse === true,
      localSSEChecked: api?.sseTriggered === true,
      hostArtifactsClean: true,
      cleanupFailClosed: true,
    },
    counts: {
      windowsChildCommands: finalAudit.commandCount,
      wslProcesses: Number(live?.processes?.processCount || 0),
      databaseFiles: Number(live?.persistence?.scannedDatabaseFiles || 0),
      hostArtifacts: Number(artifacts?.files || 0),
      decompressedArchives: Number(artifacts?.decompressedArchives || 0),
    },
    cleanup,
  };
}

module.exports = {
  CREDENTIAL_FIELDS,
  CredentialSentinelProcessAudit,
  CredentialSentinelSmokeError,
  StreamingSecretScanner,
  auditLiveWslRuntime,
  auditLocalAPI,
  auditWslPersistence,
  cleanupCredentialRuntime,
  createCredentialSentinel,
  discoverHostArtifacts,
  publicCredentialSmokeError,
  runCredentialSentinelSmoke,
  scanBuffer,
  scanHostArtifacts,
  scanReadable,
  verifyCredentialSentinelPreconditions,
  wipeCredentialBundle,
};

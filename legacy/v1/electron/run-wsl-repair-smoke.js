#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { fileURLToPath } = require('node:url');

const packageManifest = require('./legacy-desktop-package.json');
const {
  validateApplianceManifest,
  verifyArtifactFile,
} = require('./appliance-manifest');
const { ProcessRunner } = require('./process-runner');
const { defaultWindowsHelperPath } = require('./windows-capabilities');
const {
  EXPECTED_RUNTIME,
  EXPECTED_SERVICE,
  WslManager,
  applianceIdentityMatches,
  identityRuntimeSha256,
  identityRuntimeTransactionId,
  manifestSha256,
} = require('./wsl-manager');
const {
  loadInteropRecoveryDescriptor,
} = require('./wslinterop-recovery');
const {
  AuditedProcessRunner,
  safeIdentity,
  safeStatus,
} = require('./wsl-smoke');
const {
  cleanupLegacyWrapperMounts,
  quiesceLegacyAppliance,
} = require('./local-poc-bootstrap');
const {
  assertLocalPocMigrationLock,
  withLocalPocMigrationLock,
} = require('./local-poc-migration-lock');

const DISTRO_NAME = 'AppleMusicDownloader';
const EXECUTE_ARGUMENT = '--execute-private-runtime';
const QUIESCE_ARGUMENT = '--quiesce-legacy-mounts';
const MANIFEST_ARGUMENT = '--manifest';
const REPAIR_STATE_FIELD = 'localPocRepair';
const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const RUNNING_STATES = new Set(['running', 'starting']);
const HOST_TRANSACTION_FIELDS = [
  'installTransaction',
  'updateTransaction',
  'pendingManifest',
  'localPocBootstrap',
];
const ALLOWED_STDIN_CONTROLS = new Set(['configure-stdin', 'update-stdin']);
const REPAIR_PHASES = new Set(['prepared', 'terminating', 'quiesced', 'submitted', 'target_verified']);
const SAFE_UPSTREAM_ERROR_CODES = new Set([
  'local_poc_migration_lock_failed',
  'local_poc_migration_locked',
  'distro_corrupt',
  'distro_name_conflict',
  'active_jobs',
  'update_failed',
]);
const SAFE_ERROR_TYPES = new Set(['TypeError', 'WslSmokeError', 'WslManagerError']);

const ERROR_TEXT = Object.freeze({
  invalid_arguments: ['repair smoke 参数无效。', '使用 --manifest 指向受控 dist manifest；执行时另加 --execute-private-runtime。'],
  invalid_target: ['repair smoke 目标产物未通过校验。', '重新构建并校验 local-poc Appliance 产物。'],
  host_state: ['Windows Appliance 状态不满足 repair 前置条件。', '保留现有数据，先处理未完成事务或状态不一致。'],
  credentials_present: ['检测到桌面凭据文件，repair smoke 已拒绝继续。', '仅在无真实凭据的专用测试 Appliance 上运行。'],
  ownership_mismatch: ['专用 WSL2 发行版所有权或身份不匹配。', '不要 unregister；保留数据并检查 ownership 记录。'],
  runtime_state: ['WSL Appliance 运行时状态不满足 repair 前置条件。', '检查 supervisor、更新事务和当前 runtime 身份。'],
  active_jobs: ['存在活动任务，repair smoke 已拒绝更新。', '等待任务结束或取消后重新执行 preflight。'],
  repair_not_required: ['当前 runtime 已与目标 manifest 一致。', '不要重复执行 repair；运行普通 check-only smoke 即可。'],
  repair_failed: ['WSL Appliance repair 未能安全完成。', '保留 host recovery state，不要重复 update 或手工 unregister。'],
  repair_recovery_required: ['检测到未完成的 legacy mount repair，已停止以避免重复 terminate 或 update。', '保留发行版和 host marker，检查脱敏状态后按同一显式 repair 入口恢复。'],
  postcondition_failed: ['repair 后身份或持久状态校验失败。', '保留发行版并检查脱敏诊断；不要再次更新。'],
  audit_failed: ['repair 进程审计未通过。', '检查是否出现凭据、terminate、unregister、shutdown 或重复 update。'],
});

class RepairSmokeError extends Error {
  constructor(code, step) {
    const text = ERROR_TEXT[code] || ERROR_TEXT.repair_failed;
    super(text[0]);
    this.name = 'RepairSmokeError';
    this.code = code;
    this.step = step;
    this.action = text[1];
  }
}

class RepairAuditedProcessRunner extends AuditedProcessRunner {
  constructor(delegate, { allowTerminate = false } = {}) {
    super(delegate, { allowUpdate: true, allowTerminate });
    this.stdinControls = [];
    this.terminateCommands = 0;
  }

  observe(kind, command, args, options = {}) {
    if (args.includes('--terminate')) {
      if (this.terminateCommands >= 1) fail('audit_failed', 'process_audit');
      this.terminateCommands += 1;
    }
    return super.observe(kind, command, args, options);
  }

  rejectStdinControlWithoutInput(args) {
    if (args.some((argument) => String(argument).toLowerCase().endsWith('-stdin'))) {
      fail('audit_failed', 'process_audit');
    }
  }

  exec(command, args, options = {}) {
    this.rejectStdinControlWithoutInput(args);
    return super.exec(command, args, options);
  }

  input(command, args, input, options = {}) {
    const operation = String(args.at(-1) || '').toLowerCase();
    if (!ALLOWED_STDIN_CONTROLS.has(operation)) fail('audit_failed', 'process_audit');
    this.stdinControls.push(operation);
    return super.input(command, args, input, options);
  }

  spawn(command, args, options = {}) {
    this.rejectStdinControlWithoutInput(args);
    return super.spawn(command, args, options);
  }

  summary() {
    const summary = super.summary();
    return {
      ...summary,
      configureControlDocumentsObserved: this.stdinControls.filter((value) => value === 'configure-stdin').length,
      updateControlDocumentsObserved: this.stdinControls.filter((value) => value === 'update-stdin').length,
      terminateCommandsObserved: this.terminateCommands,
    };
  }
}

function fail(code, step) {
  throw new RepairSmokeError(code, step);
}

function publicRepairError(error) {
  const knownUpstream = SAFE_UPSTREAM_ERROR_CODES.has(error?.code)
    && typeof error?.step === 'string' && /^[a-z0-9_]{1,64}$/.test(error.step);
  const normalized = error instanceof RepairSmokeError
    ? error
    : new RepairSmokeError(
      error?.code === 'local_poc_migration_locked' ? 'repair_recovery_required' : 'repair_failed',
      knownUpstream ? error.step : 'unknown',
    );
  const result = {
    code: normalized.code,
    message: normalized.message,
    action: normalized.action,
    step: normalized.step,
  };
  if (knownUpstream) result.upstreamCode = error.code;
  if (!knownUpstream && SAFE_ERROR_TYPES.has(error?.name)) {
    result.errorType = error.name;
    const frame = String(error?.stack || '').match(/[\\/](run-wsl-repair-smoke|local-poc-migration-lock)\.js:(\d+):(\d+)/);
    if (frame) result.debugFrame = `${frame[1]}.js:${frame[2]}:${frame[3]}`;
  }
  return result;
}

function usage() {
  return [
    'Usage:',
    '  node electron/run-wsl-repair-smoke.js --manifest <versioned local-poc manifest>',
    `  node electron/run-wsl-repair-smoke.js --manifest <manifest> ${EXECUTE_ARGUMENT}`,
    `  node electron/run-wsl-repair-smoke.js --manifest <manifest> ${EXECUTE_ARGUMENT} ${QUIESCE_ARGUMENT}`,
    '',
    '默认只执行 preflight。仅 --quiesce-legacy-mounts 显式路径允许最多一次 terminate；始终禁止凭据、unregister 和 shutdown。',
  ].join('\n');
}

function parseArguments(argv) {
  if (argv.length === 1 && argv[0] === '--help') {
    return { help: true, execute: false, quiesceLegacyMounts: false, manifestPath: '' };
  }
  let manifestPath = '';
  let execute = false;
  let quiesceLegacyMounts = false;
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === MANIFEST_ARGUMENT && !manifestPath && index + 1 < argv.length
        && !argv[index + 1].startsWith('--')) {
      manifestPath = argv[index + 1];
      index += 1;
      continue;
    }
    if (argument === EXECUTE_ARGUMENT && !execute) {
      execute = true;
      continue;
    }
    if (argument === QUIESCE_ARGUMENT && !quiesceLegacyMounts) {
      quiesceLegacyMounts = true;
      continue;
    }
    fail('invalid_arguments', 'arguments');
  }
  if (!manifestPath) fail('invalid_arguments', 'arguments');
  if (quiesceLegacyMounts && !execute) fail('invalid_arguments', 'arguments');
  return { help: false, execute, quiesceLegacyMounts, manifestPath };
}

function samePath(left, right) {
  return path.normalize(path.resolve(left)).toLowerCase() === path.normalize(path.resolve(right)).toLowerCase();
}

async function regularFile(filename, maximumBytes = Number.MAX_SAFE_INTEGER) {
  const info = await fsp.lstat(filename);
  if (!info.isFile() || info.isSymbolicLink() || info.size <= 0 || info.size > maximumBytes) {
    fail('invalid_target', 'target_file');
  }
  return info;
}

async function loadRepairTargetManifest(repositoryRoot, manifestPath, {
  artifactVerifier = verifyArtifactFile,
} = {}) {
  const distDirectory = path.join(repositoryRoot, 'appliance', 'dist');
  const expectedManifest = path.join(
    distDirectory,
    `applemusic-downloader-wsl-manifest-${packageManifest.version}-amd64.json`,
  );
  try {
    const distInfo = await fsp.lstat(distDirectory);
    if (!distInfo.isDirectory() || distInfo.isSymbolicLink() || !samePath(manifestPath, expectedManifest)) {
      fail('invalid_target', 'target_manifest');
    }
    await regularFile(expectedManifest, 1024 * 1024);
    const manifestRealPath = await fsp.realpath(expectedManifest);
    if (!samePath(manifestRealPath, expectedManifest)) fail('invalid_target', 'target_manifest');
    const manifest = validateApplianceManifest(JSON.parse(await fsp.readFile(expectedManifest, 'utf8')));
    if (manifest.version !== packageManifest.version || manifest.channel !== 'local-poc'
        || manifest.distroName !== DISTRO_NAME || manifest.architecture !== 'amd64'
        || manifest.releasePolicy.publicReleaseAllowed !== false) {
      fail('invalid_target', 'target_manifest');
    }
    let artifactsVerified = 0;
    for (const [label, artifact] of [['rootfs', manifest.rootfs], ['runtime', manifest.runtime]]) {
      const parsed = new URL(artifact.url);
      if (parsed.protocol !== 'file:') fail('invalid_target', `${label}_artifact`);
      const artifactPath = fileURLToPath(parsed);
      const expectedArtifact = path.join(distDirectory, artifact.file);
      if (!samePath(artifactPath, expectedArtifact)) fail('invalid_target', `${label}_artifact`);
      await regularFile(expectedArtifact);
      const artifactRealPath = await fsp.realpath(expectedArtifact);
      if (!samePath(artifactRealPath, expectedArtifact)) fail('invalid_target', `${label}_artifact`);
      await artifactVerifier(artifact, expectedArtifact);
      artifactsVerified += 1;
    }
    return { manifest, artifactsVerified };
  } catch (error) {
    if (error instanceof RepairSmokeError) throw error;
    fail('invalid_target', 'target_manifest');
  }
}

async function credentialsFilePresent(appDataDir) {
  try {
    await fsp.lstat(path.join(appDataDir, 'credentials.bin'));
    return true;
  } catch (error) {
    if (error.code === 'ENOENT') return false;
    fail('credentials_present', 'credentials_file');
  }
}

function validateHostState(state) {
  let installedManifest;
  try { installedManifest = validateApplianceManifest(state?.manifest); } catch { fail('host_state', 'installed_manifest'); }
  const transactionsClear = HOST_TRANSACTION_FIELDS.every((field) => state[field] == null);
  if (state.schemaVersion !== 1 || state.distroName !== DISTRO_NAME || !transactionsClear
      || state.installedVersion !== installedManifest.version
      || state.installedRuntimeSha256 !== installedManifest.runtime.sha256
      || !SHA256_PATTERN.test(state.installedRuntimeTransactionId || '')
      || typeof state.downloadDir !== 'string' || !path.win32.isAbsolute(state.downloadDir)) {
    fail('host_state', 'host_state');
  }
  return installedManifest;
}

function assertRuntimeStatus(status, state, installedManifest, step) {
  if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, installedManifest.version)
      || identityRuntimeSha256(status) !== installedManifest.runtime.sha256
      || identityRuntimeTransactionId(status) !== state.installedRuntimeTransactionId
      || status.updateInProgress !== false || Boolean(status.pendingTransactionId)
      || status.configured !== true || status.credentialsConfigured !== false
      || !Number.isInteger(status.supervisorPid) || status.supervisorPid <= 0
      || ![...RUNNING_STATES, 'stopped'].includes(status.state)) {
    fail(step === 'post_status' ? 'postcondition_failed' : 'runtime_state', step);
  }
}

// A repair may only operate on the exact installed release. In particular,
// the historical 1.2.13 bridge is disabled because its immutable supervisor
// reopens Windows pathnames after desktop verification.
function isPermittedRepairVersionTransition(sourceManifest, targetManifest) {
  return sourceManifest?.version === targetManifest?.version;
}

function assertAudit(audit, {
  execute,
  quiesceLegacyMounts = false,
  expectedUpdateCount = execute ? 1 : 0,
}) {
  if (!audit || typeof audit.summary !== 'function') fail('audit_failed', 'process_audit');
  const summary = audit.summary();
  if (summary.dockerCommandsObserved !== 0 || summary.forbiddenCommandsObserved !== 0
      || summary.sensitiveEnvironmentKeysObserved !== 0
      || summary.updateCommandsObserved !== expectedUpdateCount
      || summary.updateControlDocumentsObserved !== expectedUpdateCount
      || summary.terminateCommandsObserved < 0
      || summary.terminateCommandsObserved > (quiesceLegacyMounts ? 1 : 0)
      || summary.configureControlDocumentsObserved < 0
      || summary.configureControlDocumentsObserved > (execute ? (quiesceLegacyMounts ? 2 : 1) : 0)
      || summary.stdinOnlyControlDocuments
        !== summary.configureControlDocumentsObserved + summary.updateControlDocumentsObserved) {
    fail('audit_failed', 'process_audit');
  }
  return summary;
}

async function verifyIdleIfRunning(manager, status, version) {
  if (!RUNNING_STATES.has(status.state)) return 'deferred_until_execute';
  try {
    await manager.prepareLocalEndpoint({ expectedVersion: version });
    await manager.waitForReady({ timeoutMs: 180_000, expectedVersion: version });
    const active = await manager.activeJobs();
    if (!Array.isArray(active) || active.length !== 0) fail('active_jobs', 'active_jobs');
    return 'idle';
  } catch (error) {
    if (error instanceof RepairSmokeError) throw error;
    fail('runtime_state', 'queue_probe');
  }
}

// This is deliberately a supervisor service stop, not WslManager.stop(). The
// latter also terminates the distro, which is forbidden for the private
// recovery path. The status and persisted identity are re-read immediately so
// an update never follows a stale stop acknowledgement.
async function stopBackendForRecovery(manager, { failureCode = 'runtime_state' } = {}) {
  if (!manager || typeof manager.runControl !== 'function' || typeof manager.status !== 'function') {
    fail(failureCode, 'service_stop');
  }
  try {
    await manager.runControl('stop', { timeoutMs: 60_000 });
  } catch (error) {
    if (error?.controlCode === 'active_jobs' || error?.code === 'active_jobs') fail('active_jobs', 'service_stop');
    fail(failureCode, 'service_stop');
  }
  const state = await manager.readState().catch(() => fail(failureCode, 'service_stop_state'));
  const manifest = validateHostState(state);
  const status = await manager.status().catch(() => fail(failureCode, 'service_stop_status'));
  assertRuntimeStatus(status, state, manifest, 'post_status');
  if (status.state !== 'stopped') fail(failureCode, 'service_stop_status');
  return { state, manifest, status };
}

function repairMarkerDocument(state, sourceManifest, targetManifest, transactionId) {
  const ownerId = state?.ownership?.ownerId;
  if (!SHA256_PATTERN.test(transactionId || '') || !SHA256_PATTERN.test(ownerId || '')) {
    fail('repair_failed', 'repair_marker');
  }
  if (sourceManifest.version !== targetManifest.version
      || transactionId === state.installedRuntimeTransactionId) {
    fail('repair_failed', 'repair_marker');
  }
  return {
    schemaVersion: 1,
    phase: 'prepared',
    ownerId,
    sourceVersion: sourceManifest.version,
    sourceRuntimeSha256: sourceManifest.runtime.sha256,
    sourceRuntimeTransactionId: state.installedRuntimeTransactionId,
    sourceManifestSha256: manifestSha256(sourceManifest),
    targetVersion: targetManifest.version,
    targetRuntimeSha256: targetManifest.runtime.sha256,
    targetRuntimeTransactionId: transactionId,
    targetManifestSha256: manifestSha256(targetManifest),
  };
}

function repairMarkerTargetsManifest(marker, targetManifest) {
  return marker.targetVersion === targetManifest.version
    && marker.targetRuntimeSha256 === targetManifest.runtime.sha256
    && marker.targetManifestSha256 === manifestSha256(targetManifest);
}

function validateRepairMarker(value, targetManifest = null) {
  const fields = [
    'schemaVersion', 'phase',
    'ownerId',
    'sourceVersion', 'sourceRuntimeSha256', 'sourceRuntimeTransactionId', 'sourceManifestSha256',
    'targetVersion', 'targetRuntimeSha256', 'targetRuntimeTransactionId', 'targetManifestSha256',
  ];
  if (!value || typeof value !== 'object' || Array.isArray(value)
      || Object.keys(value).sort().join(',') !== fields.sort().join(',')
      || value.schemaVersion !== 1 || !REPAIR_PHASES.has(value.phase)
      || !SHA256_PATTERN.test(value.ownerId || '')
      || !SHA256_PATTERN.test(value.sourceRuntimeSha256 || '')
      || !SHA256_PATTERN.test(value.sourceRuntimeTransactionId || '')
      || !SHA256_PATTERN.test(value.sourceManifestSha256 || '')
      || !SHA256_PATTERN.test(value.targetRuntimeTransactionId || '')
      || value.targetRuntimeTransactionId === value.sourceRuntimeTransactionId) {
    fail('repair_recovery_required', 'repair_marker');
  }
  if (targetManifest && !repairMarkerTargetsManifest(value, targetManifest)) {
    fail('repair_recovery_required', 'repair_marker');
  }
  return { ...value };
}

function assertRepairHostBase(state, marker) {
  if (!state || state.schemaVersion !== 1 || state.distroName !== DISTRO_NAME
      || state.installTransaction != null || state.localPocBootstrap != null
      || state.ownership?.ownerId !== marker.ownerId
      || typeof state.downloadDir !== 'string' || !path.win32.isAbsolute(state.downloadDir)
      || JSON.stringify(state[REPAIR_STATE_FIELD]) !== JSON.stringify(marker)) {
    fail('repair_recovery_required', 'repair_host_state');
  }
}

function assertRepairSourceState(state, marker) {
  assertRepairHostBase(state, marker);
  let manifest;
  try { manifest = validateApplianceManifest(state.manifest); } catch { fail('repair_recovery_required', 'repair_source'); }
  if (state.updateTransaction != null || state.pendingManifest != null
      || state.installedVersion !== marker.sourceVersion
      || state.installedRuntimeSha256 !== marker.sourceRuntimeSha256
      || state.installedRuntimeTransactionId !== marker.sourceRuntimeTransactionId
      || manifestSha256(manifest) !== marker.sourceManifestSha256) {
    fail('repair_recovery_required', 'repair_source');
  }
  return manifest;
}

function assertRepairTargetState(state, marker, targetManifest) {
  assertRepairHostBase(state, marker);
  let manifest;
  try { manifest = validateApplianceManifest(state.manifest); } catch { fail('postcondition_failed', 'repair_target'); }
  if (state.updateTransaction != null || state.pendingManifest != null
      || state.installedVersion !== marker.targetVersion
      || state.installedRuntimeSha256 !== marker.targetRuntimeSha256
      || state.installedRuntimeTransactionId !== marker.targetRuntimeTransactionId
      || manifestSha256(manifest) !== marker.targetManifestSha256
      || manifestSha256(manifest) !== manifestSha256(targetManifest)) {
    fail('postcondition_failed', 'repair_target');
  }
  return manifest;
}

function assertRepairPendingState(state, marker) {
  assertRepairHostBase(state, marker);
  let sourceManifest;
  let targetManifest;
  try {
    sourceManifest = validateApplianceManifest(state.manifest);
    targetManifest = validateApplianceManifest(state.pendingManifest);
  } catch {
    fail('repair_recovery_required', 'repair_pending');
  }
  const transaction = state.updateTransaction;
  if (!transaction || typeof transaction !== 'object' || Array.isArray(transaction)
      || transaction.schemaVersion !== 2 || transaction.id !== marker.targetRuntimeTransactionId
      || transaction.mode !== 'repair'
      || transaction.fromVersion !== marker.sourceVersion
      || transaction.toVersion !== marker.targetVersion
      || transaction.fromRuntimeSha256 !== marker.sourceRuntimeSha256
      || transaction.toRuntimeSha256 !== marker.targetRuntimeSha256
      || transaction.fromRuntimeTransactionId !== marker.sourceRuntimeTransactionId
      || transaction.fromManifestSha256 !== marker.sourceManifestSha256
      || transaction.toManifestSha256 !== marker.targetManifestSha256
      || typeof transaction.startedAt !== 'string' || transaction.startedAt.length < 1
      || state.installedVersion !== marker.sourceVersion
      || state.installedRuntimeSha256 !== marker.sourceRuntimeSha256
      || state.installedRuntimeTransactionId !== marker.sourceRuntimeTransactionId
      || manifestSha256(sourceManifest) !== marker.sourceManifestSha256
      || manifestSha256(targetManifest) !== marker.targetManifestSha256) {
    fail('repair_recovery_required', 'repair_pending');
  }
  return { sourceManifest, targetManifest, transaction };
}

async function verifyRepairRuntime(manager, marker, expectedOwnerId, { target = false } = {}) {
  const version = target ? marker.targetVersion : marker.sourceVersion;
  const runtimeSha256 = target ? marker.targetRuntimeSha256 : marker.sourceRuntimeSha256;
  const runtimeTransactionId = target
    ? marker.targetRuntimeTransactionId
    : marker.sourceRuntimeTransactionId;
  const state = await manager.readState();
  if (target) assertRepairTargetState(state, marker, state.manifest);
  else assertRepairSourceState(state, marker);
  const distro = await manager.registeredDistro();
  if (!distro || distro.name !== DISTRO_NAME || distro.version !== 2) fail('ownership_mismatch', 'repair_distro');
  let ownership;
  try { ownership = await manager.verifyDistroOwnership({ expectedVersion: version }); } catch {
    fail('ownership_mismatch', 'repair_ownership');
  }
  const boundOwnerId = expectedOwnerId || marker.ownerId;
  if (ownership.ownerId !== boundOwnerId
      || !applianceIdentityMatches(ownership.appliance, EXPECTED_SERVICE, EXPECTED_RUNTIME, version)) {
    fail('ownership_mismatch', 'repair_ownership');
  }
  const status = await manager.status().catch(() => fail('runtime_state', 'repair_status'));
  if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, version)
      || identityRuntimeSha256(status) !== runtimeSha256
      || identityRuntimeTransactionId(status) !== runtimeTransactionId
      || status.updateInProgress !== false || Boolean(status.pendingTransactionId)
      || status.configured !== true || status.credentialsConfigured !== false
      || !['running', 'starting', 'stopped'].includes(status.state)) {
    fail(target ? 'postcondition_failed' : 'runtime_state', 'repair_status');
  }
  return { state, status, ownerId: ownership.ownerId };
}

async function ensureRepairQueueIdle(manager, marker, expectedOwnerId, beforeMutation = async () => {}) {
  let proof = await verifyRepairRuntime(manager, marker, expectedOwnerId);
  if (!RUNNING_STATES.has(proof.status.state)) {
    await beforeMutation();
    await manager.start(undefined, { expectedVersion: marker.sourceVersion });
    proof = await verifyRepairRuntime(manager, marker, expectedOwnerId);
  }
  try {
    await beforeMutation();
    await manager.prepareLocalEndpoint({ expectedVersion: marker.sourceVersion });
    await manager.waitForReady({ timeoutMs: 180_000, expectedVersion: marker.sourceVersion });
    const active = await manager.activeJobs();
    if (!Array.isArray(active) || active.length !== 0) fail('active_jobs', 'repair_queue');
  } catch (error) {
    if (error instanceof RepairSmokeError) throw error;
    fail('runtime_state', 'repair_queue');
  }
}

async function ensureRepairTargetReady(manager, marker, ownerId, beforeMutation = async () => {}) {
  let proof = await verifyRepairRuntime(manager, marker, ownerId, { target: true });
  if (!RUNNING_STATES.has(proof.status.state)) {
    await beforeMutation();
    await manager.start(undefined, { expectedVersion: marker.targetVersion });
    proof = await verifyRepairRuntime(manager, marker, ownerId, { target: true });
  }
  try {
    await beforeMutation();
    await manager.prepareLocalEndpoint({ expectedVersion: marker.targetVersion });
    await manager.waitForReady({ timeoutMs: 180_000, expectedVersion: marker.targetVersion });
    const active = await manager.activeJobs();
    if (!Array.isArray(active) || active.length !== 0) fail('postcondition_failed', 'active_jobs');
  } catch (error) {
    if (error instanceof RepairSmokeError) throw error;
    fail('postcondition_failed', 'repair_health');
  }
  return proof;
}

async function verifyRepairPendingRuntime(manager, marker) {
  const state = await manager.readState();
  assertRepairPendingState(state, marker);
  const distro = await manager.registeredDistro();
  if (!distro || distro.name !== DISTRO_NAME || distro.version !== 2) {
    fail('ownership_mismatch', 'repair_distro');
  }
  let ownership;
  try { ownership = await manager.verifyDistroOwnership({ expectedVersion: marker.sourceVersion }); } catch {
    fail('ownership_mismatch', 'repair_ownership');
  }
  const status = await manager.status().catch(() => fail('repair_recovery_required', 'repair_status'));
  const sourceIdentity = identityRuntimeSha256(status) === marker.sourceRuntimeSha256
    && identityRuntimeTransactionId(status) === marker.sourceRuntimeTransactionId;
  const targetIdentity = identityRuntimeSha256(status) === marker.targetRuntimeSha256
    && identityRuntimeTransactionId(status) === marker.targetRuntimeTransactionId;
  if (ownership.ownerId !== marker.ownerId
      || !applianceIdentityMatches(ownership.appliance, EXPECTED_SERVICE, EXPECTED_RUNTIME, marker.sourceVersion)
      || !applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, marker.sourceVersion)
      || (!sourceIdentity && !targetIdentity)
      || status.configured !== true || status.credentialsConfigured !== false
      || !['running', 'starting', 'stopped'].includes(status.state)
      || (status.updateInProgress === true
        && status.pendingTransactionId !== marker.targetRuntimeTransactionId)
      || (status.updateInProgress === false
        && Boolean(status.pendingTransactionId))) {
    fail('repair_recovery_required', 'repair_pending_runtime');
  }
  return { state, status, ownerId: ownership.ownerId };
}

async function reconcileRepairPendingTransaction(manager, marker, beforeMutation) {
  const proof = await verifyRepairPendingRuntime(manager, marker);
  if (typeof manager.reconcileUpdateState !== 'function') {
    fail('repair_recovery_required', 'repair_reconcile');
  }
  let reconciliation;
  try {
    await beforeMutation();
    reconciliation = await manager.reconcileUpdateState(proof.status);
  } catch {
    fail('repair_recovery_required', 'repair_reconcile');
  }
  if (reconciliation?.outcome !== 'committed') {
    fail('repair_recovery_required', 'repair_reconcile');
  }
  return ensureRepairTargetReady(manager, marker, proof.ownerId, beforeMutation);
}

function assertNoRepairReplayReceipt(status, marker) {
  const lastTransactionId = String(status?.lastUpdateTransactionId || '');
  const lastOutcome = String(status?.lastUpdateOutcome || '');
  if (!lastTransactionId) return;
  if (!SHA256_PATTERN.test(lastTransactionId)
      || lastTransactionId === marker.targetRuntimeTransactionId
      || lastTransactionId !== marker.sourceRuntimeTransactionId
      || lastOutcome !== 'committed') {
    fail('repair_recovery_required', 'repair_terminal_receipt');
  }
}

async function resumeQuiesceWithoutTerminate(
  manager,
  marker,
  ownerId,
  mountCleaner,
  beforeMutation = async () => {},
) {
  let proof = await verifyRepairRuntime(manager, marker, ownerId);
  if (!RUNNING_STATES.has(proof.status.state)) {
    await beforeMutation();
    await manager.start(undefined, { expectedVersion: marker.sourceVersion });
    proof = await verifyRepairRuntime(manager, marker, ownerId);
  }
  try {
    await beforeMutation();
    await manager.prepareLocalEndpoint({ expectedVersion: marker.sourceVersion });
    await manager.waitForReady({ timeoutMs: 180_000, expectedVersion: marker.sourceVersion });
    const active = await manager.activeJobs();
    if (!Array.isArray(active) || active.length !== 0) fail('active_jobs', 'repair_queue');
  } catch (error) {
    if (error instanceof RepairSmokeError) throw error;
    fail('repair_recovery_required', 'repair_queue');
  }
  await beforeMutation();
  try { await manager.ensureKeeper(); } catch { fail('repair_recovery_required', 'repair_keeper'); }
  await manager.waitForSupervisorReady({ expectedVersion: marker.sourceVersion, timeoutMs: 180_000 })
    .catch(() => fail('repair_recovery_required', 'repair_supervisor'));
  proof = await verifyRepairRuntime(manager, marker, ownerId);
  if (RUNNING_STATES.has(proof.status.state)) {
    await beforeMutation();
    const stopped = JSON.parse((await manager.runControl('stop', { timeoutMs: 60_000 })).stdout);
    const status = stopped?.status || stopped;
    if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, marker.sourceVersion)
        || status.state !== 'stopped' || status.credentialsConfigured !== false) {
      fail('repair_recovery_required', 'repair_stop');
    }
  }
  await closeManagerProxy(manager);
  proof = await verifyRepairRuntime(manager, marker, ownerId);
  if (proof.status.state !== 'stopped') fail('repair_recovery_required', 'repair_stop');
  await beforeMutation();
  await mountCleaner(manager, marker.sourceVersion);
  return proof.status;
}

async function runQuiescedRepairSmoke({
  manager,
  target,
  artifactsVerified,
  audit,
  credentialsPresent,
  migrationLocker,
  migrationLockVerifier,
  quiescer,
  mountCleaner,
  transactionIdFactory,
}) {
  return migrationLocker(manager, async (migrationLock) => {
    const beforeMutation = async () => migrationLockVerifier(migrationLock);
    await beforeMutation();
    if (await credentialsPresent()) fail('credentials_present', 'credentials_file');
    let state = await manager.readState();
    let marker;
    if (state[REPAIR_STATE_FIELD] == null) {
      let sourceManifest;
      try { sourceManifest = validateHostState(state); } catch { fail('host_state', 'repair_source'); }
      marker = repairMarkerDocument(state, sourceManifest, target, transactionIdFactory());
      await beforeMutation();
      state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
    } else {
      marker = validateRepairMarker(state[REPAIR_STATE_FIELD]);
      if (!repairMarkerTargetsManifest(marker, target) && marker.phase !== 'target_verified') {
        fail('repair_recovery_required', 'repair_marker');
      }
    }

    if (marker.phase === 'target_verified') {
      const previousTarget = await ensureRepairTargetReady(manager, marker, marker.ownerId, beforeMutation);
      if (repairMarkerTargetsManifest(marker, target)) {
        const auditSummary = assertAudit(audit, {
          execute: true, quiesceLegacyMounts: true, expectedUpdateCount: 0,
        });
        return {
          schemaVersion: 1,
          smoke: 'applemusic-downloader-wsl-repair',
          mode: 'execute-private-runtime-quiesced',
          ok: true,
          target: safeIdentity(previousTarget.status),
          checks: {
            recoveredWithoutReplay: true,
            durableTerminalReceipt: true,
            digestChanged: marker.sourceRuntimeSha256 !== marker.targetRuntimeSha256,
          },
          audit: auditSummary,
        };
      }
      state = await manager.readState();
      let sourceManifest;
      try { sourceManifest = assertRepairTargetState(state, marker, state.manifest); } catch {
        fail('repair_recovery_required', 'repair_terminal_receipt');
      }
      marker = repairMarkerDocument(state, sourceManifest, target, transactionIdFactory());
      await beforeMutation();
      state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
    }

    state = await manager.readState();
    const targetStateVisible = state.updateTransaction == null && state.pendingManifest == null
      && state.installedVersion === marker.targetVersion
      && state.installedRuntimeSha256 === marker.targetRuntimeSha256
      && state.installedRuntimeTransactionId === marker.targetRuntimeTransactionId;
    if (targetStateVisible) {
      if (!['quiesced', 'submitted'].includes(marker.phase)) {
        fail('repair_recovery_required', 'repair_update_outcome');
      }
      assertRepairTargetState(state, marker, target);
      const targetProof = await ensureRepairTargetReady(manager, marker, marker.ownerId, beforeMutation);
      if (await credentialsPresent()) fail('postcondition_failed', 'credentials_file');
      const auditSummary = assertAudit(audit, {
        execute: true, quiesceLegacyMounts: true, expectedUpdateCount: 0,
      });
      marker = { ...marker, phase: 'target_verified' };
      await beforeMutation();
      state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
      assertRepairTargetState(state, marker, target);
      return {
        schemaVersion: 1,
        smoke: 'applemusic-downloader-wsl-repair',
        mode: 'execute-private-runtime-quiesced',
        ok: true,
        target: safeIdentity(targetProof.status),
        checks: {
          recoveredWithoutReplay: true,
          durableTerminalReceipt: true,
          digestChanged: marker.sourceRuntimeSha256 !== marker.targetRuntimeSha256,
        },
        audit: auditSummary,
      };
    }

    if (state.updateTransaction != null || state.pendingManifest != null) {
      if (!['quiesced', 'submitted'].includes(marker.phase)) {
        fail('repair_recovery_required', 'repair_update_transaction');
      }
      const targetProof = await reconcileRepairPendingTransaction(manager, marker, beforeMutation);
      if (await credentialsPresent()) fail('postcondition_failed', 'credentials_file');
      const auditSummary = assertAudit(audit, {
        execute: true, quiesceLegacyMounts: true, expectedUpdateCount: 0,
      });
      marker = { ...marker, phase: 'target_verified' };
      await beforeMutation();
      state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
      assertRepairTargetState(state, marker, target);
      return {
        schemaVersion: 1,
        smoke: 'applemusic-downloader-wsl-repair',
        mode: 'execute-private-runtime-quiesced',
        ok: true,
        target: safeIdentity(targetProof.status),
        checks: {
          recoveredWithoutReplay: true,
          durableTerminalReceipt: true,
          reconciledDurableUpdateTransaction: true,
          digestChanged: marker.sourceRuntimeSha256 !== marker.targetRuntimeSha256,
        },
        audit: auditSummary,
      };
    }

    let sourceProof = await verifyRepairRuntime(manager, marker, null);
    assertNoRepairReplayReceipt(sourceProof.status, marker);
    if (marker.phase === 'submitted') {
      marker = { ...marker, phase: 'quiesced' };
      await beforeMutation();
      state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
      assertRepairSourceState(state, marker);
      sourceProof = await verifyRepairRuntime(manager, marker, sourceProof.ownerId);
    }
    const ownerId = sourceProof.ownerId;
    if (marker.phase === 'prepared') {
      await ensureRepairQueueIdle(manager, marker, ownerId, beforeMutation);
      await quiescer(manager, marker.sourceVersion, {
        mountCleaner,
        ownershipVerifier: async () => {
          await beforeMutation();
          await verifyRepairRuntime(manager, marker, ownerId);
        },
        beforeTerminate: async () => {
          await beforeMutation();
          marker = { ...marker, phase: 'terminating' };
          await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
        },
      });
      await beforeMutation();
      marker = { ...marker, phase: 'quiesced' };
      state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
    } else if (marker.phase === 'terminating') {
      await resumeQuiesceWithoutTerminate(manager, marker, ownerId, mountCleaner, beforeMutation);
      await beforeMutation();
      marker = { ...marker, phase: 'quiesced' };
      state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
    } else if (marker.phase === 'quiesced') {
      await resumeQuiesceWithoutTerminate(manager, marker, ownerId, mountCleaner, beforeMutation);
    }

    state = await manager.readState();
    assertRepairSourceState(state, marker);
    const submitProof = await verifyRepairRuntime(manager, marker, ownerId);
    if (submitProof.status.state !== 'stopped') fail('repair_recovery_required', 'repair_submit_state');
    await beforeMutation();
    try {
      await manager.update(target, {
        force: true,
        repair: true,
        transactionId: marker.targetRuntimeTransactionId,
        beforeSubmit: async () => {
          await beforeMutation();
          const current = await verifyRepairRuntime(manager, marker, ownerId);
          if (current.status.state !== 'stopped') fail('repair_recovery_required', 'repair_submit_state');
        },
      });
    } catch (error) {
      if (error?.code === 'active_jobs') fail('active_jobs', 'repair_queue_check');
      fail('repair_failed', 'repair_update');
    }

    state = await manager.readState();
    assertRepairTargetState(state, marker, target);
    const targetProof = await ensureRepairTargetReady(manager, marker, ownerId, beforeMutation);
    if (await credentialsPresent()) fail('postcondition_failed', 'credentials_file');
    const auditSummary = assertAudit(audit, {
      execute: true, quiesceLegacyMounts: true, expectedUpdateCount: 1,
    });
    marker = { ...marker, phase: 'target_verified' };
    await beforeMutation();
    state = await manager.writeState({ [REPAIR_STATE_FIELD]: marker });
    assertRepairTargetState(state, marker, target);
    return {
      schemaVersion: 1,
      smoke: 'applemusic-downloader-wsl-repair',
      mode: 'execute-private-runtime-quiesced',
      ok: true,
      target: safeIdentity(targetProof.status),
      checks: {
        repairAttemptedOnce: true,
        quiescedLegacyMounts: true,
        backendStoppedBeforeUpdate: true,
        digestChanged: marker.sourceRuntimeSha256 !== marker.targetRuntimeSha256,
        ownershipPreserved: true,
        durableTerminalReceipt: true,
        activeJobs: 0,
      },
      audit: auditSummary,
    };
  });
}

async function runRepairSmoke({
  manager,
  targetManifest,
  artifactsVerified = 2,
  execute = false,
  quiesceLegacyMounts = false,
  audit,
  credentialsPresent = async () => false,
  migrationLocker = withLocalPocMigrationLock,
  migrationLockVerifier = assertLocalPocMigrationLock,
  quiescer = quiesceLegacyAppliance,
  mountCleaner = cleanupLegacyWrapperMounts,
  transactionIdFactory = () => crypto.randomBytes(32).toString('hex'),
} = {}) {
  if (!manager) throw new TypeError('manager is required');
  let target;
  try { target = validateApplianceManifest(targetManifest); } catch { fail('invalid_target', 'target_manifest'); }
  if (target.channel !== 'local-poc' || target.releasePolicy.publicReleaseAllowed !== false) {
    fail('invalid_target', 'target_manifest');
  }
  if (quiesceLegacyMounts) {
    if (!execute) fail('invalid_arguments', 'arguments');
    return runQuiescedRepairSmoke({
      manager,
      target,
      artifactsVerified,
      audit,
      credentialsPresent,
      migrationLocker,
      migrationLockVerifier,
      quiescer,
      mountCleaner,
      transactionIdFactory,
    });
  }
  if (await credentialsPresent()) fail('credentials_present', 'credentials_file');
  const state = await manager.readState();
  if (state?.[REPAIR_STATE_FIELD] != null) fail('repair_recovery_required', 'repair_marker');
  const installedManifest = validateHostState(state);

  const distro = await manager.registeredDistro();
  if (!distro || distro.name !== DISTRO_NAME || distro.version !== 2) fail('ownership_mismatch', 'distro');
  let ownership;
  try { ownership = await manager.verifyDistroOwnership({ expectedVersion: installedManifest.version }); } catch {
    fail('ownership_mismatch', 'ownership');
  }
  if (!applianceIdentityMatches(ownership?.appliance, EXPECTED_SERVICE, EXPECTED_RUNTIME, installedManifest.version)) {
    fail('ownership_mismatch', 'ownership');
  }
  const initialStatus = await manager.status().catch(() => fail('runtime_state', 'status'));
  assertRuntimeStatus(initialStatus, state, installedManifest, 'status');
  if (!isPermittedRepairVersionTransition(installedManifest, target)) {
    fail('invalid_target', 'target_version');
  }
  const queueCheck = await verifyIdleIfRunning(manager, initialStatus, installedManifest.version);
  const requiresRepair = installedManifest.runtime.sha256 !== target.runtime.sha256;

  if (!execute) {
    const auditSummary = assertAudit(audit, { execute: false });
    return {
      schemaVersion: 1,
      smoke: 'applemusic-downloader-wsl-repair',
      mode: 'preflight',
      ok: true,
      target: safeIdentity({ service: EXPECTED_SERVICE, runtime: EXPECTED_RUNTIME, version: target.version }),
      checks: {
        manifestVerified: true,
        artifactsVerified,
        credentialsAbsent: true,
        hostTransactionsClear: true,
        ownershipVerified: true,
        distroVersion: 2,
        initialStatus: safeStatus(initialStatus),
        queueCheck,
        repairRequired: requiresRepair,
        digestChanged: requiresRepair,
      },
      audit: auditSummary,
    };
  }
  try { await manager.repair(target); } catch (error) {
    if (error?.code === 'active_jobs') fail('active_jobs', 'repair_queue_check');
    fail('repair_failed', 'repair');
  }

  const postState = await manager.readState();
  if (HOST_TRANSACTION_FIELDS.some((field) => postState[field] != null)
      || postState.installedVersion !== target.version
      || postState.installedRuntimeSha256 !== target.runtime.sha256
      || !SHA256_PATTERN.test(postState.installedRuntimeTransactionId || '')
      || manifestSha256(postState.manifest) !== manifestSha256(target)
      || postState.downloadDir !== state.downloadDir) {
    fail('postcondition_failed', 'host_state');
  }
  let postOwnership;
  try { postOwnership = await manager.verifyDistroOwnership({ expectedVersion: target.version }); } catch {
    fail('postcondition_failed', 'ownership');
  }
  if (postOwnership.ownerId !== ownership.ownerId) fail('postcondition_failed', 'ownership');
  let postStatus = await manager.status().catch(() => fail('postcondition_failed', 'post_status'));
  assertRuntimeStatus(postStatus, postState, target, 'post_status');
  if (await credentialsPresent()) fail('postcondition_failed', 'credentials_file');
  let healthVerified = false;
  try {
    await manager.prepareLocalEndpoint({ expectedVersion: target.version });
    await manager.waitForReady({ timeoutMs: 180_000, expectedVersion: target.version });
    const active = await manager.activeJobs();
    if (!Array.isArray(active) || active.length !== 0) fail('postcondition_failed', 'active_jobs');
    healthVerified = true;
  } catch (error) {
    if (error instanceof RepairSmokeError) throw error;
    fail('postcondition_failed', 'health');
  }
  const auditSummary = assertAudit(audit, { execute: true });
  return {
    schemaVersion: 1,
    smoke: 'applemusic-downloader-wsl-repair',
    mode: 'execute-private-runtime',
    ok: true,
    target: safeIdentity(postStatus),
    checks: {
      repairAttemptedOnce: true,
      digestChanged: requiresRepair,
      credentialsAbsent: true,
      hostTransactionsClear: true,
      ownershipPreserved: true,
      runtimeIdentityVerified: true,
      healthVerified,
      activeJobs: 0,
    },
    audit: auditSummary,
  };
}

async function readHostState(statePath) {
  try { return JSON.parse(await fsp.readFile(statePath, 'utf8')); } catch { fail('host_state', 'host_state'); }
}

async function closeManagerProxy(manager) {
  if (!manager) return;
  if (typeof manager.discardLocalProxy === 'function') {
    await manager.discardLocalProxy({ clearPersistedEndpoint: true });
    return;
  }
  if (!manager.proxy) return;
  await manager.proxy.stop();
  manager.proxy = null;
  manager.proxyLocalPort = null;
  manager.proxyBackendPort = null;
  manager.serviceBaseUrl = '';
}

function createRepairWslManager(options, audit, ManagerClass = WslManager) {
  if (!audit || typeof audit.spawn !== 'function') throw new TypeError('repair audit is required');
  return new ManagerClass({
    ...options,
    runner: audit,
    spawn: (command, args, spawnOptions) => audit.spawn(command, args, spawnOptions),
  });
}

async function main(argv = process.argv.slice(2)) {
  const parsed = parseArguments(argv);
  if (parsed.help) {
    process.stdout.write(`${usage()}\n`);
    return 0;
  }
  const repositoryRoot = path.resolve(__dirname, '..');
  const appDataDir = path.win32.join(process.env.LOCALAPPDATA || '', DISTRO_NAME);
  if (!process.env.LOCALAPPDATA || !path.win32.isAbsolute(appDataDir)) fail('host_state', 'app_data');
  const statePath = path.win32.join(appDataDir, 'appliance-state.json');
  const ownershipPath = path.win32.join(appDataDir, 'ownership.id');
  const state = await readHostState(statePath);
  const { manifest, artifactsVerified } = await loadRepairTargetManifest(repositoryRoot, parsed.manifestPath);
  let helperIdentity;
  try {
    helperIdentity = loadInteropRecoveryDescriptor({
      expectedVersion: manifest.version,
      repositoryRoot,
      packaged: false,
    });
  } catch {
    fail('invalid_target', 'interop_recovery');
  }
  const baseRunner = new ProcessRunner();
  const audit = new RepairAuditedProcessRunner(baseRunner, { allowTerminate: parsed.quiesceLegacyMounts });
  const manager = createRepairWslManager({
    appDataDir,
    statePath,
    installDir: path.win32.join(appDataDir, 'wsl'),
    ownershipPath,
    downloadDir: state.downloadDir,
    protectedDownloadRoots: [appDataDir],
    windowsProbeExe: defaultWindowsHelperPath({ appPath: repositoryRoot, resourcesPath: '' }),
    windowsHelperBindingProvider: () => {
      const binding = helperIdentity?.windowsHelper;
      if (!binding) throw new Error('windows helper binding is unavailable');
      return binding;
    },
  }, audit);
  try {
    const result = await runRepairSmoke({
      manager,
      targetManifest: manifest,
      artifactsVerified,
      execute: parsed.execute,
      quiesceLegacyMounts: parsed.quiesceLegacyMounts,
      audit,
      credentialsPresent: () => credentialsFilePresent(appDataDir),
    });
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } finally {
    await closeManagerProxy(manager);
  }
}

if (require.main === module) {
  let auditSummary = null;
  main().then((code) => {
    if (process.exitCode === undefined) process.exitCode = code;
  }).catch((error) => {
    process.stdout.write(`${JSON.stringify({
      schemaVersion: 1,
      smoke: 'applemusic-downloader-wsl-repair',
      ok: false,
      error: publicRepairError(error),
      ...(auditSummary ? { audit: auditSummary } : {}),
    }, null, 2)}\n`);
    process.exitCode = 2;
  });
}

module.exports = {
  EXECUTE_ARGUMENT,
  MANIFEST_ARGUMENT,
  QUIESCE_ARGUMENT,
  REPAIR_STATE_FIELD,
  RepairAuditedProcessRunner,
  RepairSmokeError,
  createRepairWslManager,
  credentialsFilePresent,
  isPermittedRepairVersionTransition,
  loadRepairTargetManifest,
  parseArguments,
  publicRepairError,
  runRepairSmoke,
  stopBackendForRecovery,
  validateHostState,
};

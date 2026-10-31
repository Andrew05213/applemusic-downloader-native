'use strict';

const path = require('node:path');

const {
  EXPECTED_RUNTIME,
  EXPECTED_SERVICE,
  applianceIdentityMatches,
  diagnosticBuildIdentity,
  identityRuntimeSha256,
  identityRuntimeTransactionId,
} = require('./wsl-manager');
const { validateApplianceManifest } = require('./appliance-manifest');

const RUNNING_STATES = new Set(['running', 'starting']);
const FORBIDDEN_WSL_ARGUMENTS = new Set(['--unregister', '--shutdown', 'credential-stdin']);
const SENSITIVE_ENVIRONMENT_KEY = /^(?:APPLE_(?:EMAIL|PASSWORD)|MEDIA_USER_TOKEN|AUTHORIZATION_TOKEN|AMDL_.*(?:TOKEN|PASSWORD|SECRET))$/i;
const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const NETWORK_PREFLIGHT_READY_V1 = Object.freeze({
  schemaVersion: 1,
  path: 'windows_stdio_bridge',
  state: 'ready',
  stage: 'complete',
  windowsPath: 'reachable',
  wslDirectPath: 'unknown',
  proxyPropagated: true,
});
const NETWORK_PREFLIGHT_READY = Object.freeze({
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

const ERROR_TEXT = Object.freeze({
  distro_missing: ['未找到 AppleMusicDownloader 专用 WSL2 发行版。', '请先通过桌面安装向导安装 Appliance。'],
  owner_identity_mismatch: ['发行版所有者身份不匹配，smoke 已拒绝继续。', '请由桌面应用执行安全修复；不要手工 unregister。'],
  identity_mismatch: ['Appliance 服务身份不符合预期。', '请保留现有数据并通过桌面应用执行修复。'],
  active_jobs: ['存在活动下载任务，生命周期 smoke 已拒绝停止服务。', '等待任务结束后再运行生命周期 smoke。'],
  credentials_present: ['检测到桌面凭据存储，生命周期 smoke 已拒绝重启 WSL。', '仅在无真实凭据的测试 Appliance 上运行生命周期 smoke。'],
  path_probe_mismatch: ['已登记下载目录不同时包含中文和空格。', '为真实 smoke 预先选择一个包含中文和空格的测试下载目录。'],
  backend_state_mismatch: ['Appliance 生命周期状态与预期不一致。', '查看桌面诊断后重试。'],
  health_identity_mismatch: ['本机健康接口身份不符合预期。', '检查端口占用或 Appliance 运行时是否损坏。'],
  unsafe_process: ['smoke 拒绝执行不在允许列表内的外部进程或控制命令。', '检查 smoke 入口及 WslManager 是否出现越权命令。'],
  cleanup_failed: ['smoke 未能恢复运行前的服务状态。', '使用桌面应用查看状态；不要手工 unregister。'],
  wrapper_mount_residue: ['Wrapper 停止后仍检测到运行时挂载残留。', '保留发行版并查看诊断；不要继续更新或手工卸载挂载。'],
  local_poc_adoption_rejected: ['本地 PoC 不满足严格接管条件，未写入所有权。', '保留现有发行版，由开发者核对注册路径、运行时身份、事务与任务状态。'],
  local_poc_adoption_failed: ['本地 PoC 接管未能安全提交。', '保留现有发行版并检查 host/WSL marker；不要 unregister。'],
  local_poc_bootstrap_rejected: ['旧本地 PoC 不满足严格身份引导条件，未写入 release metadata。', '核对 manifest、安装树、BasePath、凭据和任务状态后重试。'],
  local_poc_bootstrap_failed: ['旧本地 PoC 身份引导未能安全提交。', '保留发行版并检查 release metadata；不要 unregister。'],
  local_poc_migration_locked: ['另一个本地 PoC 接管或身份引导进程正在运行。', '等待当前操作结束后重试；不要同时启动多个迁移入口。'],
  local_poc_migration_lock_failed: ['本地 PoC 迁移独占锁未能安全保持。', '停止并保留现有状态，重新运行同一迁移入口；不要 unregister。'],
  target_manifest_mismatch: ['所选最终 Appliance manifest 与已安装运行时身份不匹配。', '保留现有发行版，完成受控更新后重新运行 check-only smoke。'],
  network_preflight_state: ['服务未停止，check-only smoke 不会替换当前网络代次。', '等待服务停止后，使用同一最终 manifest 重新运行 check-only smoke。'],
  network_preflight_failed: ['WSL Appliance 无凭据网络预检未通过。', '检查 Windows 网络或代理后重试；无需重新输入密码或验证码。'],
  smoke_failed: ['WSL Appliance smoke 未通过。', '查看失败步骤并使用桌面诊断定位问题。'],
});

class WslSmokeError extends Error {
  constructor(code, step, details = {}) {
    const text = ERROR_TEXT[code] || ERROR_TEXT.smoke_failed;
    super(text[0]);
    this.name = 'WslSmokeError';
    this.code = code;
    this.step = step;
    this.action = text[1];
    this.details = details;
  }
}

function toSmokeError(error, code, step, details = {}) {
  if (error instanceof WslSmokeError) return error;
  return new WslSmokeError(code, step, details);
}

function publicSmokeError(error) {
  const normalized = error instanceof WslSmokeError
    ? error
    : new WslSmokeError('smoke_failed', 'unknown');
  return {
    code: normalized.code,
    message: normalized.message,
    action: normalized.action,
    step: normalized.step,
    ...(normalized.details && Object.keys(normalized.details).length > 0
      ? { details: normalized.details }
      : {}),
  };
}

function safeIdentity(value) {
  return {
    service: value?.service === EXPECTED_SERVICE ? EXPECTED_SERVICE : 'unexpected',
    runtime: value?.runtime === EXPECTED_RUNTIME ? EXPECTED_RUNTIME : 'unexpected',
    version: typeof value?.version === 'string' && /^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$/.test(value.version)
      ? value.version
      : 'unknown',
  };
}

function safeStatus(value) {
  return {
    ...safeIdentity(value),
    state: typeof value?.state === 'string' && /^[a-z_-]{1,32}$/.test(value.state) ? value.state : 'unknown',
    configured: value?.configured === true,
    updateInProgress: value?.updateInProgress === true,
  };
}

function versionAtLeast(version, major, minor, patch) {
  const match = /^(\d+)\.(\d+)\.(\d+)/.exec(String(version || ''));
  if (!match) return false;
  const actual = [Number(match[1]), Number(match[2]), Number(match[3])];
  const required = [major, minor, patch];
  for (let index = 0; index < required.length; index += 1) {
    if (actual[index] !== required[index]) return actual[index] > required[index];
  }
  return true;
}

function safeNetworkPreflight(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)
      || value.path !== NETWORK_PREFLIGHT_READY.path
      || value.state !== NETWORK_PREFLIGHT_READY.state
      || value.stage !== NETWORK_PREFLIGHT_READY.stage
      || value.windowsPath !== NETWORK_PREFLIGHT_READY.windowsPath
      || value.wslDirectPath !== NETWORK_PREFLIGHT_READY.wslDirectPath
      || value.proxyPropagated !== NETWORK_PREFLIGHT_READY.proxyPropagated) return null;
  if (value.schemaVersion === NETWORK_PREFLIGHT_READY_V1.schemaVersion) {
    return { ...NETWORK_PREFLIGHT_READY_V1 };
  }
  if (value.schemaVersion !== NETWORK_PREFLIGHT_READY.schemaVersion
      || value.caSource !== NETWORK_PREFLIGHT_READY.caSource
      || !Number.isInteger(value.curlCode) || value.curlCode < 0 || value.curlCode > 99
      || !Number.isInteger(value.sslVerifyResult) || value.sslVerifyResult < 0 || value.sslVerifyResult > 65535
      || !SHA256_PATTERN.test(value.generationId || '')) return null;
  return {
    ...NETWORK_PREFLIGHT_READY,
    curlCode: value.curlCode,
    sslVerifyResult: value.sslVerifyResult,
  };
}

function targetManifest(value) {
  try {
    const manifest = validateApplianceManifest(value);
    if (!manifest.productVersion || !manifest.applianceBuildId
        || !SHA256_PATTERN.test(manifest.runtime.sha256)
        || !SHA256_PATTERN.test(manifest.inputs.androidMainBuiltSha256)
        || (versionAtLeast(manifest.version, 1, 3, 5)
          && !SHA256_PATTERN.test(manifest.inputs.androidNetworkPreflightBuiltSha256))
        || !SHA256_PATTERN.test(manifest.inputs.rendererSha256)) {
      throw new Error('build identity is incomplete');
    }
    return manifest;
  } catch {
    throw new WslSmokeError('target_manifest_mismatch', 'target_manifest');
  }
}

function targetManifestMatchesState(state, manifest) {
  let installed;
  try { installed = validateApplianceManifest(state?.manifest); } catch {
    throw new WslSmokeError('target_manifest_mismatch', 'installed_manifest');
  }
  const expected = targetManifest(manifest);
  if (state?.installedVersion !== expected.version
      || state?.installedRuntimeSha256 !== expected.runtime.sha256
      || !SHA256_PATTERN.test(state?.installedRuntimeTransactionId || '')
      || installed.version !== expected.version
      || installed.productVersion !== expected.productVersion
      || installed.applianceBuildId !== expected.applianceBuildId
      || installed.runtime.sha256 !== expected.runtime.sha256
      || installed.inputs.androidMainBuiltSha256 !== expected.inputs.androidMainBuiltSha256
      || installed.inputs.androidNetworkPreflightBuiltSha256 !== expected.inputs.androidNetworkPreflightBuiltSha256
      || installed.inputs.rendererSha256 !== expected.inputs.rendererSha256) {
    throw new WslSmokeError('target_manifest_mismatch', 'installed_manifest');
  }
  return expected;
}

async function finalBuildIdentity(manager, status, manifest) {
  let helperSha256;
  let bootstrapSha256;
  let recovery;
  try {
    helperSha256 = await manager.fileSha256(manager.windowsProbeExe);
    bootstrapSha256 = await manager.fileSha256(manager.interopBootstrapExe);
    recovery = typeof manager.interopRecoveryIdentityProvider === 'function'
      ? await manager.interopRecoveryIdentityProvider()
      : null;
  } catch {
    throw new WslSmokeError('target_manifest_mismatch', 'windows_helper');
  }
  if (!SHA256_PATTERN.test(helperSha256 || '')
      || status?.windowsHelperSha256 !== helperSha256
      || identityRuntimeSha256(status) !== manifest.runtime.sha256
      || !identityRuntimeTransactionId(status)) {
    throw new WslSmokeError('target_manifest_mismatch', 'final_identity');
  }
  try {
    return diagnosticBuildIdentity(manifest, status, helperSha256, {
      bootstrapSha256,
      recovery,
    });
  } catch {
    throw new WslSmokeError('target_manifest_mismatch', 'final_identity');
  }
}

function assertIdentity(value, expectedVersion, step) {
  if (!applianceIdentityMatches(value, EXPECTED_SERVICE, EXPECTED_RUNTIME, expectedVersion)) {
    throw new WslSmokeError('identity_mismatch', step);
  }
}

function isInternationalPath(value) {
  return typeof value === 'string' && /\s/u.test(value) && /[^\x00-\x7f]/u.test(value);
}

function pathSummary(windowsPath, wslPath, canonicalIdentity = {}) {
  const driveMatch = /^\/mnt\/([a-z])(?:\/|$)/i.exec(wslPath || '');
  return {
    containsWhitespace: /\s/u.test(windowsPath),
    containsNonASCII: /[^\x00-\x7f]/u.test(windowsPath),
    windowsPathLength: windowsPath.length,
    wslPathLength: String(wslPath || '').length,
    wslDrive: driveMatch ? driveMatch[1].toLowerCase() : '',
    fileSystem: ['NTFS', 'ReFS'].includes(canonicalIdentity.fileSystem) ? canonicalIdentity.fileSystem : 'unknown',
  };
}

class AuditedProcessRunner {
  constructor(delegate, { allowTerminate = false, allowUpdate = false, networkOnly = false } = {}) {
    if (!delegate || typeof delegate.exec !== 'function' || typeof delegate.input !== 'function' || typeof delegate.spawn !== 'function') {
      throw new TypeError('AuditedProcessRunner requires exec, input and spawn functions');
    }
    this.delegate = delegate;
    this.allowTerminate = allowTerminate;
    this.allowUpdate = allowUpdate;
    this.networkOnly = networkOnly;
    this.records = [];
  }

  observe(kind, command, args, options = {}) {
    if (typeof command !== 'string' || !Array.isArray(args)) {
      throw new WslSmokeError('unsafe_process', 'process_audit');
    }
    const binary = path.win32.basename(command).toLowerCase();
    if (!['wsl.exe', 'amdl-windows-probe.exe'].includes(binary)) {
      throw new WslSmokeError('unsafe_process', 'process_audit', { binary: 'unexpected' });
    }
    const forbidden = args.find((argument) => FORBIDDEN_WSL_ARGUMENTS.has(String(argument).toLowerCase()));
    const normalizedArgs = args.map((argument) => String(argument).toLowerCase());
    const networkOnlyForbidden = new Set([
      'credential-stdin',
      'configure',
      'configure-stdin',
      'start',
      'start-stdin',
      'backend-start',
      'update-stdin',
      '--terminate',
      '--unregister',
      '--shutdown',
    ]);
    if (forbidden || (!this.allowTerminate && normalizedArgs.includes('--terminate'))
        || (!this.allowUpdate && normalizedArgs.includes('update-stdin'))
        || (this.networkOnly && normalizedArgs.some((argument) => networkOnlyForbidden.has(argument)))) {
      throw new WslSmokeError('unsafe_process', 'process_audit', { operation: 'forbidden' });
    }
    const sensitiveEnvironmentKeys = Object.keys(options.env || {}).filter((key) => SENSITIVE_ENVIRONMENT_KEY.test(key));
    if (sensitiveEnvironmentKeys.length > 0) {
      throw new WslSmokeError('unsafe_process', 'process_audit', { environment: 'contains_sensitive_keys' });
    }
    this.records.push({ kind, binary, update: args.includes('update-stdin') });
  }

  exec(command, args, options = {}) {
    this.observe('exec', command, args, options);
    return this.delegate.exec(command, args, options);
  }

  input(command, args, input, options = {}) {
    this.observe('stdin', command, args, options);
    return this.delegate.input(command, args, input, options);
  }

  spawn(command, args, options = {}) {
    this.observe('spawn', command, args, options);
    return this.delegate.spawn(command, args, options);
  }

  summary() {
    const binaries = {};
    for (const record of this.records) binaries[record.binary] = (binaries[record.binary] || 0) + 1;
    return {
      commandCount: this.records.length,
      binaries,
      dockerCommandsObserved: 0,
      forbiddenCommandsObserved: 0,
      updateCommandsObserved: this.records.filter((record) => record.update).length,
      sensitiveEnvironmentKeysObserved: 0,
      stdinOnlyControlDocuments: this.records.filter((record) => record.kind === 'stdin').length,
    };
  }
}

async function verifyOwnedAppliance(manager, expectedVersion = '') {
  const distro = await manager.registeredDistro();
  if (!distro) throw new WslSmokeError('distro_missing', 'ownership');
  if (distro.version !== 2) throw new WslSmokeError('owner_identity_mismatch', 'ownership');
  try {
    const owned = await manager.verifyDistroOwnership({ expectedVersion });
    assertIdentity(owned.appliance, expectedVersion, 'ownership');
    return { distroVersion: distro.version, identity: safeIdentity(owned.appliance) };
  } catch (error) {
    throw toSmokeError(error, 'owner_identity_mismatch', 'ownership');
  }
}

async function probeDownloadPath(manager, { requireInternationalPath = true } = {}) {
  const windowsPath = manager.downloadDir;
  if (requireInternationalPath && !isInternationalPath(windowsPath)) {
    throw new WslSmokeError('path_probe_mismatch', 'download_path');
  }
  let canonical;
  let wslPath;
  try {
    canonical = await manager.canonicalDownloadPathOutsideRoots(windowsPath);
    wslPath = await manager.windowsPathToWsl(canonical.canonicalPath);
  } catch (error) {
    throw toSmokeError(error, 'smoke_failed', 'download_path');
  }
  return pathSummary(canonical.canonicalPath, wslPath, canonical);
}

async function probeHealth(manager, expectedVersion) {
  try {
    await manager.prepareLocalEndpoint({ expectedVersion });
    const health = await manager.waitForReady({ timeoutMs: 180_000, expectedVersion });
    assertIdentity(health, expectedVersion, 'health');
    return safeIdentity(health);
  } catch (error) {
    throw toSmokeError(error, 'health_identity_mismatch', 'health');
  }
}

async function activeJobCount(manager) {
  const jobs = await manager.activeJobs();
  return jobs.length;
}

async function runStoppedNetworkPreflight(manager, state, manifest, { cleanup = false } = {}) {
  const target = targetManifestMatchesState(state, manifest);
  let before;
  try {
    before = await manager.status();
  } catch {
    throw new WslSmokeError('smoke_failed', 'network_preflight_status');
  }
  if (!applianceIdentityMatches(before, EXPECTED_SERVICE, EXPECTED_RUNTIME, target.version)) {
    throw new WslSmokeError('target_manifest_mismatch', 'network_preflight_status');
  }
  if (before.state !== 'stopped') {
    throw new WslSmokeError('network_preflight_state', 'network_preflight');
  }
  let result;
  let primaryError = null;
  let cleanupResult = null;
  try {
    try {
      await manager.prepareNetworkForStart({ expectedVersion: target.version });
    } catch {
      throw new WslSmokeError('network_preflight_failed', 'network_preflight');
    }
    let after;
    try {
      after = await manager.status();
    } catch {
      throw new WslSmokeError('smoke_failed', 'network_preflight_status');
    }
    if (!applianceIdentityMatches(after, EXPECTED_SERVICE, EXPECTED_RUNTIME, target.version)) {
      throw new WslSmokeError('target_manifest_mismatch', 'network_preflight_status');
    }
    const network = safeNetworkPreflight(after.network);
    if (!network) throw new WslSmokeError('network_preflight_failed', 'network_preflight_status');
    result = {
      buildIdentity: await finalBuildIdentity(manager, after, target),
      network,
    };
  } catch (error) {
    primaryError = error instanceof WslSmokeError
      ? error
      : toSmokeError(error, 'smoke_failed', 'network_preflight');
    throw primaryError;
  } finally {
    if (cleanup) {
      try {
        const cleanup = await manager.cleanupNetworkBridge({ expectedVersion: target.version });
        const stopped = cleanup?.status || cleanup;
        if (stopped?.state !== 'stopped'
            || !applianceIdentityMatches(stopped, EXPECTED_SERVICE, EXPECTED_RUNTIME, target.version)
            || stopped?.network?.state !== 'unprepared') {
          throw new Error('network-only cleanup did not leave the expected stopped Appliance');
        }
        cleanupResult = { stopped: true, brokerReleased: cleanup?.cleaned === true };
      } catch (error) {
        throw new WslSmokeError('cleanup_failed', 'network_preflight_cleanup', primaryError ? { preflightFailed: true } : {});
      }
    }
  }
  return {
    ...result,
    ...(cleanupResult ? { cleanup: cleanupResult } : {}),
  };
}

async function runNetworkOnlySmoke(manager, state, manifest) {
  if (!manifest) throw new WslSmokeError('target_manifest_mismatch', 'target_manifest');
  const target = targetManifestMatchesState(state, manifest);
  const ownership = await verifyOwnedAppliance(manager, target.version);
  let before;
  try {
    before = await manager.status();
  } catch (error) {
    throw toSmokeError(error, 'smoke_failed', 'network_only_status');
  }
  if (!applianceIdentityMatches(before, EXPECTED_SERVICE, EXPECTED_RUNTIME, target.version)
      || before.state !== 'stopped') {
    throw new WslSmokeError('network_preflight_state', 'network_only_status');
  }
  // Bind the installed release's non-sensitive component identities before the
  // bridge can be prepared. This includes the v1.3.5 Android CA and probe digests.
  await finalBuildIdentity(manager, before, target);
  const preflight = await runStoppedNetworkPreflight(manager, state, target, { cleanup: true });
  if (preflight.network.schemaVersion !== NETWORK_PREFLIGHT_READY.schemaVersion) {
    throw new WslSmokeError('network_preflight_failed', 'network_only_preflight');
  }
  return {
    schemaVersion: 1,
    smoke: 'applemusic-downloader-wsl-appliance',
    mode: 'network-only',
    ok: true,
    checks: {
      ownerIdentity: true,
      stoppedState: true,
      manifestBoundFinalIdentity: true,
      brokerAndNativePreflight: true,
      cleanup: preflight.cleanup?.stopped === true,
      noCredentialAccess: true,
      noCredentialDelivery: true,
      backendStartAttempted: false,
      updateAttempted: false,
    },
    ownership,
    initial: { status: safeStatus(before) },
    target: {
      version: target.version,
      applianceBuildId: target.applianceBuildId,
      runtimeSha256: target.runtime.sha256,
    },
    finalBuildIdentity: preflight.buildIdentity,
    networkPreflight: preflight.network,
    cleanup: preflight.cleanup,
  };
}

async function inspectOwnedManager(manager, { expectedVersion = '', requireInternationalPath = true } = {}) {
  const ownership = await verifyOwnedAppliance(manager, expectedVersion);
  let probe;
  try {
    probe = await manager.probe({ throwOnError: true });
  } catch (error) {
    throw toSmokeError(error, 'smoke_failed', 'probe');
  }
  const applianceVersion = expectedVersion || probe?.distro?.appliance?.version || '';
  assertIdentity(probe?.distro?.appliance, applianceVersion, 'probe');
  let status;
  try {
    status = await manager.status();
  } catch (error) {
    throw toSmokeError(error, 'smoke_failed', 'status');
  }
  assertIdentity(status, applianceVersion, 'status');
  const downloadPath = await probeDownloadPath(manager, { requireInternationalPath });
  let health = null;
  let activeJobs = null;
  if (RUNNING_STATES.has(status.state)) {
    health = await probeHealth(manager, applianceVersion);
    activeJobs = await activeJobCount(manager);
  }
  return {
    expectedVersion: applianceVersion,
    ownership,
    probe: {
      windows: {
        architecture: probe?.windows?.architecture === 'x64' ? 'x64' : 'unknown',
        virtualizationEnabled: probe?.windows?.virtualizationEnabled !== false,
      },
      wsl: { installed: probe?.wsl?.installed === true, distroVersion: probe?.distro?.version || 0 },
    },
    status: safeStatus(status),
    health,
    activeJobs,
    downloadPath,
  };
}

async function restoreInitialState(manager, initialRunning, expectedVersion) {
  const status = await manager.status();
  const running = RUNNING_STATES.has(status.state);
  if (initialRunning === running) return;
  if (initialRunning) {
    await manager.start(undefined, { expectedVersion });
    return;
  }
  if (running) {
    const active = await activeJobCount(manager);
    if (active > 0) throw new WslSmokeError('cleanup_failed', 'restore', { activeJobs: active });
    await manager.stop();
  }
}

async function runLifecycleSmoke(manager, initial, options) {
  if (options.credentialsPresent) throw new WslSmokeError('credentials_present', 'lifecycle_precondition');
  const expectedVersion = initial.expectedVersion;
  const initialRunning = RUNNING_STATES.has(initial.status.state);
  let currentManager = manager;
  let stateChanged = false;
  try {
    await currentManager.start(undefined, { expectedVersion });
    stateChanged = !initialRunning;
    const firstHealth = await probeHealth(currentManager, expectedVersion);
    const beforeStop = await activeJobCount(currentManager);
    if (beforeStop > 0) throw new WslSmokeError('active_jobs', 'explicit_stop', { activeJobs: beforeStop });

    await currentManager.stop();
    stateChanged = true;
    const stopped = await currentManager.status();
    if (RUNNING_STATES.has(stopped.state)) throw new WslSmokeError('backend_state_mismatch', 'explicit_stop');
    if (typeof options.mountInspector !== 'function') throw new WslSmokeError('smoke_failed', 'wrapper_mounts');
    const firstMountCheck = await options.mountInspector(currentManager, expectedVersion, 'explicit_stop_mounts');

    if (typeof options.detachedStarter !== 'function' || typeof options.managerFactory !== 'function') {
      throw new WslSmokeError('smoke_failed', 'manager_exit');
    }
    await options.detachedStarter();
    currentManager = await options.managerFactory({ allowTerminate: true });
    await verifyOwnedAppliance(currentManager, expectedVersion);
    const detachedStatus = await currentManager.status();
    if (!RUNNING_STATES.has(detachedStatus.state)) throw new WslSmokeError('backend_state_mismatch', 'manager_exit');
    const detachedHealth = await probeHealth(currentManager, expectedVersion);

    const afterDetachedStart = await activeJobCount(currentManager);
    if (afterDetachedStart > 0) throw new WslSmokeError('active_jobs', 'restart', { activeJobs: afterDetachedStart });
    await currentManager.stop();
    const stoppedAgain = await currentManager.status();
    if (RUNNING_STATES.has(stoppedAgain.state)) throw new WslSmokeError('backend_state_mismatch', 'restart');
    const detachedMountCheck = await options.mountInspector(currentManager, expectedVersion, 'detached_stop_mounts');
    await currentManager.start(undefined, { expectedVersion });
    const restartHealth = await probeHealth(currentManager, expectedVersion);

    return {
      initialState: initial.status.state,
      firstStartHealth: firstHealth,
      explicitStop: true,
      managerExitedWithoutStoppingBackend: true,
      detachedHealth,
      restartHealth,
      wrapperMountsAfterStops: [firstMountCheck, detachedMountCheck],
      downloadAtomicProbe: true,
    };
  } finally {
    if (stateChanged) {
      try {
        await restoreInitialState(currentManager, initialRunning, expectedVersion);
      } catch (error) {
        if (!options.primaryError) throw toSmokeError(error, 'cleanup_failed', 'restore');
      }
    }
  }
}

async function runWslSmoke({
  manager,
  managerFactory,
  detachedStarter,
  lifecycle = false,
  credentialsPresent = false,
  expectedManifest = null,
  networkOnly = false,
  requireInternationalPath = true,
  mountInspector,
} = {}) {
  if (!manager) throw new TypeError('manager is required');
  const state = await manager.readState();
  const selectedManifest = expectedManifest === null ? null : targetManifest(expectedManifest);
  if (networkOnly) return runNetworkOnlySmoke(manager, state, selectedManifest);
  const expectedVersion = selectedManifest?.version
    || (typeof state.installedVersion === 'string' ? state.installedVersion : '');
  const initial = await inspectOwnedManager(manager, { expectedVersion, requireInternationalPath });
  const result = {
    schemaVersion: 1,
    smoke: 'applemusic-downloader-wsl-appliance',
    mode: lifecycle ? 'lifecycle' : 'check-only',
    ok: true,
    checks: {
      ownerIdentity: true,
      probeAndStatus: true,
      localhostHealthIdentity: initial.health !== null,
      internationalDownloadPath: initial.downloadPath.containsWhitespace && initial.downloadPath.containsNonASCII,
      dockerIndependent: true,
      noCredentialDelivery: true,
      updateAttempted: false,
    },
    initial,
  };
  if (selectedManifest) {
    const preflight = await runStoppedNetworkPreflight(manager, state, selectedManifest);
    result.target = {
      version: selectedManifest.version,
      applianceBuildId: selectedManifest.applianceBuildId,
      runtimeSha256: selectedManifest.runtime.sha256,
    };
    result.finalBuildIdentity = preflight.buildIdentity;
    result.networkPreflight = preflight.network;
    result.checks.manifestBoundFinalIdentity = true;
    result.checks.stoppedStateNetworkPreflight = true;
  }
  if (lifecycle) {
    result.lifecycle = await runLifecycleSmoke(manager, initial, {
      credentialsPresent,
      detachedStarter,
      managerFactory,
      mountInspector,
    });
    result.checks.localhostHealthIdentity = true;
    result.checks.explicitStopAndRestart = true;
    result.checks.managerExitKeepsBackend = true;
    result.checks.atomicWindowsPathProbe = true;
    result.checks.wrapperMountResidueZero = true;
  }
  return result;
}

module.exports = {
  AuditedProcessRunner,
  ERROR_TEXT,
  WslSmokeError,
  isInternationalPath,
  pathSummary,
  publicSmokeError,
  runWslSmoke,
  runStoppedNetworkPreflight,
  safeIdentity,
  safeNetworkPreflight,
  safeStatus,
  targetManifestMatchesState,
  verifyOwnedAppliance,
};

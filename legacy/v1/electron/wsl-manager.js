'use strict';

const crypto = require('node:crypto');
const { AsyncLocalStorage } = require('node:async_hooks');
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const http = require('node:http');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { isDeepStrictEqual } = require('node:util');
const {
  DISTRO_NAME,
  artifactCacheName,
  downloadAndVerifyArtifact,
  sha256File,
  validateApplianceManifest,
  verifyArtifactFile,
} = require('./appliance-manifest');
const { CONTROL_PATH, WslLoopbackProxy } = require('./loopback-proxy');
const { ProcessRunner, redactSecrets } = require('./process-runner');
const { WINDOWS_SUPPORT_MESSAGE, WINDOWS_SUPPORT_ACTION, supportedWindowsBuild, windowsBuildFromRelease } = require('./windows-support');
const {
  BOOTSTRAP_FILE,
  requiresInteropRecoveryBinding,
  validateInteropRecoveryDescriptor,
  validateWindowsHelperBinding,
  verifyWindowsHelperBinding,
} = require('./wslinterop-recovery');

const ACTIVE_JOB_STATES = new Set(['queued', 'running', 'cancelling']);
const CONTROL_COMMANDS = new Set([
  'probe',
  'status',
  'configure-stdin',
  'credential-stdin',
  'network-prepare-stdin',
  'network-preflight',
  'network-cleanup-stdin',
  'start',
  'stop',
  'update-stdin',
  'diagnostics',
  'proxy-stdio',
]);
const DEFAULT_PORT = 8080;
const MAX_DISTINCT_PROXY_PORT_ATTEMPTS = 3;
const DEFAULT_TIMEOUT_MS = 180_000;
// The supervisor's bounded readiness phase is 180 seconds and the Go control
// client owns a five-minute socket deadline.  The Windows process timeout must
// be strictly outside both so it can receive the Go client's final result.
const SUPERVISOR_UPDATE_READY_TIMEOUT_MS = 180_000;
const GO_UPDATE_CLIENT_TIMEOUT_MS = 300_000;
const UPDATE_TIMEOUT_MS = 360_000;
// A freshly imported WSL2 rootfs may spend well over a minute completing its
// first systemd boot (machine-id setup, tmpfiles and socket activation).  Keep
// the cold-start contract aligned with the desktop's documented 180-second
// readiness window so a healthy appliance is not reported as import_failed.
const SUPERVISOR_READY_TIMEOUT_MS = 180_000;
const EXPECTED_SERVICE = 'applemusic-downloader';
const EXPECTED_RUNTIME = 'wsl-appliance';
const KEEPALIVE_PATH = '/opt/applemusic-downloader/current/amdl-appliance-keepalive';
const CONTROLLED_DOWNLOAD_MOUNT = '/srv/applemusic-downloader/downloads';
const DISTRO_OWNERSHIP_PATH = '/var/lib/applemusic-downloader/desktop-owner-id';
const HOST_OWNERSHIP_FILENAME = 'ownership.id';
const DESKTOP_PROXY_HEADER = 'X-AMDL-Desktop-Token';
const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const PATH_OBJECT_ID_PATTERN = /^[a-f0-9]{8}:[a-f0-9]{16}$/;
const OWNER_ID_PATTERN = /^[a-f0-9]{64}$/;
const NETWORK_BRIDGE_PATH = 'windows_stdio_bridge';
const NETWORK_PREFLIGHT_FAILURE_STAGES = new Set([
  'helper_identity',
  'helper_launch',
  'helper_protocol',
  'proxy_resolution',
  'native_probe_invalid',
  'native_startup',
  'dns',
  'tcp_connect',
  'tls',
  'ca_bundle_missing',
  'ca_bundle_invalid',
  'url_bag_fetch',
  'url_bag_parse',
  'url_bag_key_lookup',
]);
const NETWORK_CA_SOURCE = 'bundled_v1';
const NETWORK_TLS_CURL_CODES = new Set([35, 51, 58, 59, 60, 66, 80, 82, 83, 90, 91]);
const SENSITIVE_ENVIRONMENT_KEY = /^(?:APPLE_(?:EMAIL|PASSWORD)|MEDIA_USER_TOKEN|AUTHORIZATION_TOKEN|AMDL_.*(?:TOKEN|PASSWORD|SECRET))$/i;
const BLOCKED_PROCESS_ENVIRONMENT_KEY = /^(?:HTTP_PROXY|HTTPS_PROXY|ALL_PROXY|NO_PROXY|WSL_INTEROP|WSLENV)$/i;
const WINDOWS_LOCK_COMMANDS = Object.freeze({
  lifecycle: 'hold-wsl-lifecycle-lock',
  state: 'hold-wsl-state-lock',
});
const MAX_LOCK_OUTPUT_BYTES = 4096;
const MAX_UNREGISTER_JOURNAL_BYTES = 2 * 1024 * 1024;
const STATE_LOCK_TIMEOUT_MS = 30_000;
const LIFECYCLE_LOCK_TIMEOUT_MS = 15 * 60_000;
const lifecycleLockContext = new AsyncLocalStorage();
const lifecycleOperationTails = new Map();
const stateWriteTails = new Map();

const ERROR_MESSAGES = Object.freeze({
  unsupported_windows: [WINDOWS_SUPPORT_MESSAGE, WINDOWS_SUPPORT_ACTION],
  unsupported_arch: ['当前 CPU 架构不受支持。', '请使用 Windows x64 设备。'],
  virtualization_disabled: ['CPU 虚拟化未启用。', '请在 BIOS/UEFI 中启用 Intel VT-x 或 AMD-V，然后重新启动电脑。'],
  wsl_missing: ['尚未安装 WSL。', '请在安装向导中选择“安装 WSL”，完成后按提示重启。'],
  wsl_update_required: ['WSL 版本过旧或不可用。', '请在安装向导中选择“更新 WSL”，完成后重试。'],
  wsl1_only: ['专用发行版不是 WSL2。', '请将 AppleMusicDownloader 发行版转换为 WSL2，或在向导中执行修复。'],
  reboot_required: ['Windows 需要重新启动才能继续。', '请保存工作并重启，登录后安装向导会自动继续。'],
  distro_name_conflict: ['已存在同名但不属于本应用的 WSL 发行版。', '请备份并重命名该发行版，或联系支持人员处理。'],
  orphaned_install_state: ['检测到专用 WSL 发行版已丢失，但宿主安装状态或数据仍然存在。', '应用不会覆盖残留目录；请保留数据并查看诊断信息，确认目录缺失后再重试修复。'],
  distro_corrupt: ['专用 WSL 发行版不完整或已损坏。', '请在桌面应用中执行“修复服务”；不要手工 unregister，以免丢失任务数据。'],
  port_in_use: ['本机服务端口被占用。', '请关闭占用程序；应用也可改用自动选择的本机端口。'],
  path_unavailable: ['所选下载目录不可用。', '请选择本机 C:、D:、E: 等 NTFS/ReFS 磁盘上的绝对路径，并确认当前用户有写入权限。'],
  path_change_active_jobs: ['当前有下载任务，不能切换目录。', '请等待任务完成或取消任务后重试；应用不会修改当前目录。'],
  path_change_failed_recovered: ['下载目录切换失败，已恢复原目录和服务。', '请确认目标目录可写后重试；现有任务和服务已保留。'],
  path_change_recovery_failed: ['下载目录切换和自动恢复均未完成。', '应用已停止以保护现有任务数据，请执行“修复服务”后重试。'],
  download_failed: ['WSL Appliance 下载失败。', '请检查网络连接后重试。'],
  checksum_mismatch: ['WSL Appliance 校验失败。', '下载文件可能损坏或被替换，请删除缓存后重新下载。'],
  import_failed: ['专用 WSL 发行版导入失败。', '请确认磁盘空间充足，并在诊断信息中查看 WSL 错误。'],
  health_timeout: ['后端服务未能在 180 秒内就绪。', '请查看发行版状态和最近 200 行日志，然后执行修复。'],
  credential_delivery_failed: ['登录信息未能安全交给 WSL 服务。', '请重新保存登录信息；应用不会把凭据写入 .env 或命令行。'],
  network_preflight_failed: ['WSL Appliance 无法通过受支持的网络路径连接 Apple 认证服务。', '请查看脱敏诊断中的失败阶段后重试；无需重新输入密码或验证码。'],
  runtime_reinstall_required: ['当前 WSL Appliance 不能原地升级到此版本。', '应用不会修改现有发行版或数据。请保留数据并在新的专用 Appliance 中完成安装。'],
  active_jobs: ['仍有下载任务正在运行。', '请等待任务完成或明确取消任务后再更新服务。'],
  update_failed: ['后端更新失败，已保留原有任务数据。', '请查看日志并重试；若仍失败，请执行“修复服务”。'],
  state_conflict: ['桌面端状态已被其他操作更新。', '请重试当前操作；应用会保留已落盘的更新事务和恢复标记。'],
  windows_lock_timeout: ['另一个桌面操作仍在进行。', '请等待当前操作完成后重试；应用不会覆盖任务或更新状态。'],
  windows_lock_failed: ['桌面服务协调锁不可用。', '请查看诊断信息并重启应用；不要手工注销 AppleMusicDownloader 发行版。'],
  windows_lock_protocol: ['桌面服务协调协议异常。', '请执行“修复运行时”并查看最近 200 行脱敏日志。'],
  windows_lock_lost: ['桌面服务协调锁意外断开。', '请查看诊断信息后重试；已落盘的事务会保留。'],
  windows_lock_release_failed: ['桌面服务协调锁未能安全释放。', '请关闭重复启动的桌面进程后重试；不要手工注销发行版。'],
});

const PUBLIC_WINDOWS_LOCK_ERRORS = new Set([
  'windows_lock_timeout',
  'windows_lock_failed',
  'windows_lock_protocol',
  'windows_lock_lost',
  'windows_lock_release_failed',
]);

class WslManagerError extends Error {
  constructor(code, details = {}) {
    const [message, action] = ERROR_MESSAGES[code] || ['WSL Appliance 操作失败。', '请查看诊断信息后重试。'];
    super(message);
    this.name = 'WslManagerError';
    this.code = code;
    this.action = action;
    this.details = details;
  }

  toJSON() {
    return { code: this.code, message: this.message, action: this.action, details: this.details };
  }
}

function managerError(code, cause, secrets = []) {
  if (cause instanceof WslManagerError) return cause;
  const reason = cause && cause.message ? redactSecrets(cause.message, secrets) : redactSecrets(String(cause || ''), secrets);
  const details = reason ? { reason } : {};
  if (typeof cause?.controlCode === 'string') details.controlCode = cause.controlCode;
  if (typeof cause?.controlAction === 'string') details.controlAction = redactSecrets(cause.controlAction, secrets);
  return new WslManagerError(code, details);
}

function classifyWslFailure(cause, fallback) {
  const message = String(cause && cause.message || cause || '').toLowerCase();
  if (/0x80370102|hcs_e_hyperv_not_installed|virtualization|virtualisation|虚拟化|虚擬化/.test(message)) {
    return 'virtualization_disabled';
  }
  if (/0x800701bc|kernel component update|wsl.*update|更新.*wsl/.test(message)) return 'wsl_update_required';
  if (/0x8007012e|0x80070bc2|restart|reboot|重新启动|重新啟動/.test(message)) return 'reboot_required';
  return fallback;
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function processLocalKey(value) {
  return path.resolve(value).toLowerCase();
}

async function serializeProcessLocal(tails, key, operation) {
  const previous = tails.get(key) || Promise.resolve();
  let releaseTurn;
  const turn = new Promise((resolve) => { releaseTurn = resolve; });
  const tail = previous.catch(() => {}).then(() => turn);
  tails.set(key, tail);
  await previous.catch(() => {});
  try {
    return await operation();
  } finally {
    releaseTurn();
    if (tails.get(key) === tail) tails.delete(key);
  }
}

function windowsLockError(code, step) {
  const error = new Error(`Windows Appliance ${step} failed`);
  error.name = 'WindowsProcessLockError';
  error.code = code;
  error.step = step;
  return error;
}

function createLockCloseState(child) {
  const state = { closed: false, exitCode: null, signal: null, spawnError: null };
  state.promise = new Promise((resolve) => {
    const finish = () => {
      if (state.closed) return;
      state.closed = true;
      resolve(state);
    };
    child.once('error', (error) => {
      state.spawnError = error;
      finish();
    });
    child.once('close', (exitCode, signal) => {
      state.exitCode = exitCode;
      state.signal = signal;
      finish();
    });
  });
  return state;
}

class WindowsProcessLock {
  constructor(kind, child, closeState, verifyHelperBinding) {
    this.kind = kind;
    this.child = child;
    this.closeState = closeState;
    this.verifyHelperBinding = verifyHelperBinding;
    this.releasing = false;
    this.released = false;
  }

  assertHeld() {
    if (this.releasing || this.released || this.closeState.closed) {
      throw windowsLockError('windows_lock_lost', `${this.kind}_lock_lost`);
    }
    return this;
  }

  async release({ timeoutMs = 15_000 } = {}) {
    if (this.released) return;
    this.assertHeld();
    this.releasing = true;
    this.child.stdin.end();
    let timer;
    const timeout = new Promise((_, reject) => {
      timer = setTimeout(
        () => reject(windowsLockError('windows_lock_release_failed', `${this.kind}_lock_release`)),
        timeoutMs,
      );
    });
    try {
      const closed = await Promise.race([this.closeState.promise, timeout]);
      if (closed.spawnError || closed.exitCode !== 0 || closed.signal) {
        throw windowsLockError('windows_lock_release_failed', `${this.kind}_lock_release`);
      }
      try {
        await this.verifyHelperBinding();
      } catch {
        throw windowsLockError('windows_lock_release_failed', `${this.kind}_lock_helper_identity`);
      }
      this.released = true;
    } catch (error) {
      if (typeof this.child.kill === 'function') this.child.kill();
      throw error;
    } finally {
      if (timer) clearTimeout(timer);
    }
  }
}

async function acquireWindowsProcessLockAttempt(manager, kind) {
  const command = WINDOWS_LOCK_COMMANDS[kind];
  if (!command || typeof manager?.spawnTrustedWindowsHelper !== 'function') {
    throw windowsLockError('windows_lock_failed', `${kind}_lock_spawn`);
  }
  let child;
  let binding;
  try {
    const launched = await manager.spawnTrustedWindowsHelper([command], {
      windowsHide: true,
      shell: false,
      stdio: ['pipe', 'pipe', 'pipe'],
      env: manager.processEnvironment,
    });
    child = launched.child;
    binding = launched.binding;
  } catch {
    throw windowsLockError('windows_lock_failed', `${kind}_lock_spawn`);
  }
  if (!child?.stdin || !child?.stdout || !child?.stderr || typeof child.once !== 'function') {
    if (typeof child?.kill === 'function') child.kill();
    throw windowsLockError('windows_lock_failed', `${kind}_lock_spawn`);
  }

  const closeState = createLockCloseState(child);
  return new Promise((resolve, reject) => {
    let settled = false;
    let stdout = Buffer.alloc(0);
    let stderrBytes = 0;
    let timer;
    const finish = (error, lock) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      child.stdout.removeListener('data', onStdout);
      child.stderr.removeListener('data', onStderr);
      stdout.fill(0);
      if (error) {
        child.stdin.end();
        reject(error);
      } else {
        resolve(lock);
      }
    };
    const abort = (step) => {
      if (typeof child.kill === 'function') child.kill();
      finish(windowsLockError('windows_lock_failed', step));
    };
    const onStderr = (chunk) => {
      stderrBytes += Buffer.byteLength(chunk);
      if (stderrBytes > MAX_LOCK_OUTPUT_BYTES) abort(`${kind}_lock_protocol`);
    };
    child.stdin.on('error', () => {
      if (!settled) abort(`${kind}_lock_stdin`);
    });
    const onStdout = (chunk) => {
      stdout = Buffer.concat([stdout, Buffer.from(chunk)]);
      if (stdout.length > MAX_LOCK_OUTPUT_BYTES) {
        abort(`${kind}_lock_protocol`);
        return;
      }
      const newline = stdout.indexOf(0x0a);
      if (newline < 0) return;
      if (stdout.subarray(newline + 1).toString('utf8').trim() !== '') {
        abort(`${kind}_lock_protocol`);
        return;
      }
      let ready;
      try { ready = JSON.parse(stdout.subarray(0, newline).toString('utf8')); } catch {
        abort(`${kind}_lock_protocol`);
        return;
      }
      const keys = Object.keys(ready || {}).sort();
      const expected = ready?.acquired === true ? ['acquired', 'version'] : ['acquired', 'reason', 'version'];
      if (ready?.version !== 1
          || typeof ready.acquired !== 'boolean'
          || JSON.stringify(keys) !== JSON.stringify(expected.sort())
          || ready.acquired === false && ready.reason !== 'busy') {
        abort(`${kind}_lock_protocol`);
        return;
      }
      if (!ready.acquired) {
        finish(windowsLockError('windows_lock_busy', `${kind}_lock_busy`));
      } else if (closeState.closed) {
        finish(windowsLockError('windows_lock_lost', `${kind}_lock_lost`));
      } else {
        finish(null, new WindowsProcessLock(
          kind,
          child,
          closeState,
          () => manager.assertWindowsHelperBinding(binding),
        ));
      }
    };
    child.stdout.on('data', onStdout);
    child.stderr.on('data', onStderr);
    closeState.promise.then(() => {
      if (!settled) finish(windowsLockError('windows_lock_failed', `${kind}_lock_spawn`));
    });
    timer = setTimeout(() => abort(`${kind}_lock_protocol_timeout`), 15_000);
  });
}

async function acquireWindowsProcessLock(manager, kind, { timeoutMs } = {}) {
  if (!WINDOWS_LOCK_COMMANDS[kind]) throw new TypeError(`Unsupported Windows process lock: ${kind}`);
  const effectiveTimeout = timeoutMs || (kind === 'state' ? STATE_LOCK_TIMEOUT_MS : LIFECYCLE_LOCK_TIMEOUT_MS);
  const deadline = Date.now() + effectiveTimeout;
  do {
    try {
      return await acquireWindowsProcessLockAttempt(manager, kind);
    } catch (error) {
      if (error?.code !== 'windows_lock_busy') throw error;
      if (Date.now() >= deadline) throw windowsLockError('windows_lock_timeout', `${kind}_lock_timeout`);
      await delay(Math.min(50, Math.max(1, deadline - Date.now())));
    }
  } while (Date.now() <= deadline);
  throw windowsLockError('windows_lock_timeout', `${kind}_lock_timeout`);
}

async function syncDirectoryBestEffort(directory) {
  let handle;
  try {
    handle = await fsp.open(directory, 'r');
    await handle.sync();
  } catch (error) {
    if (!['EACCES', 'EISDIR', 'EINVAL', 'ENOTSUP', 'EPERM'].includes(error?.code)) throw error;
  } finally {
    await handle?.close().catch(() => {});
  }
}

function listenOnLoopback(port) {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.unref();
    server.once('error', reject);
    server.listen({ host: '127.0.0.1', port, exclusive: true }, () => {
      const address = server.address();
      const selected = address && typeof address === 'object' ? address.port : 0;
      server.close((error) => {
        if (error) reject(error);
        else resolve(selected);
      });
    });
  });
}

async function selectAvailableLoopbackPort(preferredPort) {
  try {
    return await listenOnLoopback(preferredPort);
  } catch (error) {
    if (error?.code !== 'EADDRINUSE' && error?.code !== 'EACCES') throw error;
    return listenOnLoopback(0);
  }
}

function validPort(value) {
  return Number.isInteger(value) && value >= 1 && value <= 65535;
}

// The v1.2.15 CA repair is intentionally fresh-install only. A pre-1.2.15
// supervisor cannot pass its own network gate, so attempting update-stdin
// would leave the user with a misleading, non-recovering transaction.
function requiresFreshApplianceInstall(version) {
  const match = /^1\.2\.(\d+)(?:-[0-9A-Za-z.-]+)?$/.exec(String(version || ''));
  return match !== null && Number(match[1]) >= 15;
}

function assertFreshApplianceSameVersion(state, targetVersion) {
  if (!requiresFreshApplianceInstall(targetVersion)) return;
  let installedManifest;
  try {
    installedManifest = validateApplianceManifest(state?.manifest);
  } catch {
    throw new WslManagerError('runtime_reinstall_required');
  }
  if (state?.installedVersion !== targetVersion || installedManifest.version !== targetVersion) {
    throw new WslManagerError('runtime_reinstall_required');
  }
}

function assertFreshApplianceObservedVersion(state, probe, targetVersion) {
  if (!requiresFreshApplianceInstall(targetVersion)) return;
  const candidates = [
    state?.installedVersion,
    state?.manifest?.version,
    probe?.distro?.appliance?.version,
    probe?.distro?.supervisor?.version,
    probe?.distro?.supervisor?.status?.version,
  ];
  if (candidates.some((value) => typeof value === 'string' && value && value !== targetVersion)) {
    throw new WslManagerError('runtime_reinstall_required');
  }
}

function requiresNetworkDiagnosticsV2(version) {
  const match = /^(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?$/.exec(String(version || ''));
  if (!match) return false;
  const actual = [Number(match[1]), Number(match[2]), Number(match[3])];
  if (actual[0] !== 1) return actual[0] > 1;
  if (actual[1] !== 2) return actual[1] > 2;
  return actual[2] >= 15;
}

function requiresStandaloneNetworkPreflightIdentity(version) {
  const match = /^(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?$/.exec(String(version || ''));
  if (!match) return false;
  const actual = [Number(match[1]), Number(match[2]), Number(match[3])];
  const required = [1, 3, 5];
  for (let index = 0; index < required.length; index += 1) {
    if (actual[index] !== required[index]) return actual[index] > required[index];
  }
  return true;
}

function expectedNetworkSchemaVersion(expectedVersion) {
  return requiresNetworkDiagnosticsV2(expectedVersion) ? 2 : 1;
}

function networkBaseKeys(schemaVersion) {
  const keys = [
    'generationId',
    'path',
    'proxyPropagated',
    'schemaVersion',
    'stage',
    'state',
    'windowsPath',
    'wslDirectPath',
  ];
  if (schemaVersion === 2) keys.push('caSource', 'curlCode', 'sslVerifyResult');
  return keys;
}

function exactNetworkKeys(network, keys) {
  return JSON.stringify(Object.keys(network).sort()) === JSON.stringify([...keys].sort());
}

function networkStatusForGeneration(status, generationId, {
  state,
  stage,
  windowsPath,
  wslDirectPath,
  proxyPropagated,
  expectedVersion = '',
}) {
  const network = status?.network;
  const schemaVersion = expectedNetworkSchemaVersion(expectedVersion);
  if (!network || typeof network !== 'object' || Array.isArray(network)
      || network.schemaVersion !== schemaVersion
      || network.path !== NETWORK_BRIDGE_PATH
      || network.generationId !== generationId
      || network.state !== state
      || network.stage !== stage
      || network.windowsPath !== windowsPath
      || network.wslDirectPath !== wslDirectPath
      || network.proxyPropagated !== proxyPropagated) return null;
  if (schemaVersion === 2 && network.caSource !== NETWORK_CA_SOURCE) return null;
  if (schemaVersion === 2
      && (!Number.isInteger(network.curlCode) || network.curlCode < 0 || network.curlCode > 99
        || !Number.isInteger(network.sslVerifyResult)
        || network.sslVerifyResult < 0 || network.sslVerifyResult > 65535)) return null;
  if (!exactNetworkKeys(network, networkBaseKeys(schemaVersion))) return null;
  return network;
}

function unpreparedNetworkStatus(status, { expectedVersion = '' } = {}) {
  const network = status?.network;
  const schemaVersion = expectedNetworkSchemaVersion(expectedVersion);
  const expectedKeys = schemaVersion === 2
    ? ['caSource', 'curlCode', 'path', 'proxyPropagated', 'schemaVersion', 'sslVerifyResult', 'state', 'windowsPath', 'wslDirectPath']
    : ['path', 'proxyPropagated', 'schemaVersion', 'state', 'windowsPath', 'wslDirectPath'];
  if (!network || typeof network !== 'object' || Array.isArray(network)
      || network.schemaVersion !== schemaVersion
      || network.path !== NETWORK_BRIDGE_PATH
      || network.state !== 'unprepared'
      || network.windowsPath !== 'unknown'
      || network.wslDirectPath !== 'unknown'
      || network.proxyPropagated !== false
      || !exactNetworkKeys(network, expectedKeys)) return null;
  if (schemaVersion === 2
      && (network.caSource !== NETWORK_CA_SOURCE || network.curlCode !== 0 || network.sslVerifyResult !== 0)) return null;
  return network;
}

function readyNetworkGeneration(status, {
  expectedService,
  expectedRuntime,
  expectedVersion,
}) {
  const network = status?.network;
  const generationId = network?.generationId;
  return applianceIdentityMatches(status, expectedService, expectedRuntime, expectedVersion)
    && OWNER_ID_PATTERN.test(generationId || '')
    && networkStatusForGeneration(status, generationId, {
      state: 'ready',
      stage: 'complete',
      windowsPath: 'reachable',
      wslDirectPath: 'unknown',
      proxyPropagated: true,
      expectedVersion,
    })
    ? generationId
    : '';
}

function networkFailureDetailsForGeneration(status, generationId, {
  expectedService,
  expectedRuntime,
  expectedVersion,
}) {
  const network = status?.network;
  const schemaVersion = expectedNetworkSchemaVersion(expectedVersion);
  if (!applianceIdentityMatches(status, expectedService, expectedRuntime, expectedVersion)
      || !network
      || typeof network !== 'object'
      || Array.isArray(network)
      || network.schemaVersion !== schemaVersion
      || network.path !== NETWORK_BRIDGE_PATH
      || network.generationId !== generationId
      || network.state !== 'failed'
      || !['reachable', 'unreachable'].includes(network.windowsPath)
      || network.wslDirectPath !== 'unknown'
      || network.proxyPropagated !== false
      || !NETWORK_PREFLIGHT_FAILURE_STAGES.has(network.stage)) {
    return null;
  }
  if (schemaVersion === 1) {
    return exactNetworkKeys(network, networkBaseKeys(1)) ? { stage: network.stage } : null;
  }
  if (network.caSource !== NETWORK_CA_SOURCE) return null;
  if (!Number.isInteger(network.curlCode) || network.curlCode < 0 || network.curlCode > 99
      || !Number.isInteger(network.sslVerifyResult)
      || network.sslVerifyResult < 0 || network.sslVerifyResult > 65535) return null;
  const details = { stage: network.stage, caSource: network.caSource };
  const expectedKeys = networkBaseKeys(2);
  if (network.stage === 'ca_bundle_invalid' && network.curlCode === 77) {
    details.curlCode = 77;
  } else if (network.stage === 'tls' && Number.isInteger(network.curlCode)
      && NETWORK_TLS_CURL_CODES.has(network.curlCode)) {
    details.curlCode = network.curlCode;
    if (Number.isInteger(network.sslVerifyResult)
        && network.sslVerifyResult >= 0 && network.sslVerifyResult <= 65535) {
      details.sslVerifyResult = network.sslVerifyResult;
    }
  }
  return exactNetworkKeys(network, expectedKeys) ? details : null;
}

function networkFailureStageForGeneration(status, generationId, identity) {
  return networkFailureDetailsForGeneration(status, generationId, identity)?.stage || '';
}

function networkFailureDetails(error, generationId, identity) {
  return networkFailureDetailsForGeneration(error?.controlStatus, generationId, identity);
}

function networkFailureStage(error, generationId, identity) {
  return networkFailureDetails(error, generationId, identity)?.stage || '';
}

function isSafeWslDrivePath(value) {
  if (typeof value !== 'string' || value.includes('\u0000') || value.includes('\n') || value.includes('\r')) return false;
  if (!/^\/mnt\/[a-z](?:\/|$)/i.test(value) || path.posix.normalize(value) !== value) return false;
  return !value.split('/').some((segment) => segment === '.' || segment === '..');
}

function isSafeWslExecutablePath(value) {
  return typeof value === 'string'
    && !/[\u0000-\u001F\u007F\\]/.test(value)
    && isSafeWslDrivePath(value)
    && /^\/mnt\/[a-z]\/.+\.exe$/.test(value);
}

function safeWindowsLeaf(value) {
  if (typeof value !== 'string' || !value || value === '.' || value === '..') return false;
  if (/[<>:"/\\|?*\u0000-\u001F]/.test(value) || /[. ]$/.test(value)) return false;
  return !/^(?:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?$/i.test(value);
}

function tailLines(value, maximum = 200) {
  return String(value || '').split(/\r?\n/).slice(-maximum).join('\n');
}

function validateWindowsDownloadPath(value) {
  if (typeof value !== 'string' || !value.trim() || value.includes('\u0000')) throw new WslManagerError('path_unavailable');
  const candidate = value.trim();
  if (/^(?:\\\\|\/\/|\\\\[?.]\\)/.test(candidate)) throw new WslManagerError('path_unavailable');
  if (!/^[A-Za-z]:[\\/]/.test(candidate) || !path.win32.isAbsolute(candidate)) throw new WslManagerError('path_unavailable');
  const normalized = path.win32.normalize(candidate);
  if (/^[A-Za-z]:\\?$/.test(normalized)) throw new WslManagerError('path_unavailable');
  return normalized;
}

function windowsPathIsWithin(parent, candidate) {
  const relative = path.win32.relative(parent.toLowerCase(), candidate.toLowerCase());
  return relative === '' || (
    relative !== '..'
    && !relative.startsWith(`..${path.win32.sep}`)
    && !path.win32.isAbsolute(relative)
  );
}

function validateDownloadPathOutsideRoots(value, protectedRoots = []) {
  const normalized = validateWindowsDownloadPath(value);
  for (const root of protectedRoots) {
    const protectedRoot = validateWindowsDownloadPath(root);
    if (windowsPathIsWithin(protectedRoot, normalized) || windowsPathIsWithin(normalized, protectedRoot)) {
      throw new WslManagerError('path_unavailable', {
        reason: 'Download path overlaps protected application data or installation files',
      });
    }
  }
  return normalized;
}

function manifestSha256(manifest) {
  const normalized = validateApplianceManifest(manifest);
  return crypto.createHash('sha256').update(JSON.stringify(normalized), 'utf8').digest('hex');
}

function normalizeWindowsFinalPath(value) {
  if (typeof value !== 'string') throw new WslManagerError('path_unavailable');
  const withoutDevicePrefix = value.startsWith('\\\\?\\') ? value.slice(4) : value;
  return validateWindowsDownloadPath(withoutDevicePrefix);
}

function defaultWindowsProbePath(options = {}) {
  const candidates = [];
  if (options.resourcesPath) candidates.push(path.join(options.resourcesPath, 'bin', 'amdl-windows-probe.exe'));
  if (process.resourcesPath) candidates.push(path.join(process.resourcesPath, 'bin', 'amdl-windows-probe.exe'));
  candidates.push(path.join(__dirname, 'bin', 'amdl-windows-probe.exe'));
  const selected = candidates.find((candidate) => fs.existsSync(candidate)) || candidates.at(-1);
  return path.resolve(selected);
}

function validateTrustedSystemExecutable(value, executableName, systemDirectory) {
  if (typeof value !== 'string' || typeof systemDirectory !== 'string') {
    throw new WslManagerError('wsl_update_required', { reason: 'Windows system executable identity is missing' });
  }
  const executable = path.win32.normalize(value);
  const system = path.win32.normalize(systemDirectory);
  if (!path.win32.isAbsolute(executable) || !path.win32.isAbsolute(system)
      || path.win32.basename(executable).toLowerCase() !== executableName.toLowerCase()
      || path.win32.dirname(executable).toLowerCase() !== system.toLowerCase()) {
    throw new WslManagerError('wsl_update_required', { reason: `Windows ${executableName} did not resolve inside the native system directory` });
  }
  if (process.platform === 'win32') {
    let executableInfo;
    let systemInfo;
    try {
      executableInfo = fs.lstatSync(executable);
      systemInfo = fs.lstatSync(system);
    } catch (error) {
      throw managerError('wsl_missing', error);
    }
    if (!executableInfo.isFile() || executableInfo.isSymbolicLink()
        || !systemInfo.isDirectory() || systemInfo.isSymbolicLink()) {
      throw new WslManagerError('wsl_update_required', { reason: `Windows ${executableName} or its system directory is not a regular trusted object` });
    }
    const finalExecutable = fs.realpathSync.native(executable);
    const finalSystem = fs.realpathSync.native(system);
    if (path.win32.dirname(finalExecutable).toLowerCase() !== finalSystem.toLowerCase()
        || path.win32.basename(finalExecutable).toLowerCase() !== executableName.toLowerCase()) {
      throw new WslManagerError('wsl_update_required', { reason: `Windows ${executableName} final path escaped the native system directory` });
    }
    return finalExecutable;
  }
  return executable;
}

function normalizePathProbeResult(value) {
  if (!value || value.version !== 1 || value.reparsePoint !== false || value.shortPathAlias !== false) {
    throw new WslManagerError('path_unavailable', { reason: 'Windows path helper rejected a reparse point or short-path alias' });
  }
  const canonicalPath = normalizeWindowsFinalPath(value.canonicalPath);
  const fileSystem = String(value.fileSystem || '').toUpperCase();
  if (!['NTFS', 'REFS'].includes(fileSystem)) {
    throw new WslManagerError('path_unavailable', { reason: 'Download directory must be on NTFS or ReFS' });
  }
  const normalized = { canonicalPath, fileSystem };
  if (PATH_OBJECT_ID_PATTERN.test(value.objectId || '')) normalized.objectId = value.objectId;
  return normalized;
}

function normalizeFileProbeResult(value) {
  if (value?.objectType !== 'file') {
    throw new WslManagerError('path_unavailable', { reason: 'Windows file helper did not return a regular file identity' });
  }
  return normalizePathProbeResult(value);
}

function normalizeRegistrationIdentity(value) {
  if (!value || value.version !== 1 || value.distributionName !== DISTRO_NAME || value.wslVersion !== 2
      || typeof value.basePath !== 'string' || !path.win32.isAbsolute(value.basePath)) {
    throw new WslManagerError('distro_name_conflict', { reason: 'Windows WSL registration identity is invalid' });
  }
  return {
    version: 1,
    distributionName: DISTRO_NAME,
    basePath: normalizeWindowsFinalPath(value.basePath),
    wslVersion: 2,
  };
}

function normalizeRegistrationPresence(value) {
  if (!value || value.version !== 1 || value.distributionName !== DISTRO_NAME
      || typeof value.registered !== 'boolean'
      || !Number.isSafeInteger(value.matchCount) || value.matchCount < 0) {
    throw new WslManagerError('orphaned_install_state', {
      reason: 'Windows WSL registration presence result is invalid',
    });
  }
  if (!value.registered && value.matchCount === 0) {
    return {
      version: 1,
      distributionName: DISTRO_NAME,
      registered: false,
      matchCount: 0,
    };
  }
  if (value.registered && value.matchCount > 0) {
    return {
      version: 1,
      distributionName: DISTRO_NAME,
      registered: true,
      matchCount: value.matchCount,
    };
  }
  throw new WslManagerError('orphaned_install_state', {
    reason: 'Windows WSL registration presence result is inconsistent',
  });
}

function sameWindowsPath(left, right) {
  return typeof left === 'string' && typeof right === 'string'
    && path.win32.normalize(left).toLowerCase() === path.win32.normalize(right).toLowerCase();
}

function validOwnershipRecord(state, ownerId, expectedInstallDir = '') {
  const ownership = state?.ownership;
  return Boolean(
    OWNER_ID_PATTERN.test(ownerId)
    && ownership
    && ownership.schemaVersion === 1
    && ownership.ownerId === ownerId
    && ownership.distroName === DISTRO_NAME
    && typeof ownership.installDir === 'string'
    && typeof state.installDir === 'string'
    && sameWindowsPath(ownership.installDir, state.installDir)
    && (!expectedInstallDir
      || (sameWindowsPath(ownership.installDir, expectedInstallDir)
        && sameWindowsPath(state.installDir, expectedInstallDir))),
  );
}

function validUnregisterJournal(journal, expectedInstallDir = '') {
  const snapshot = journal?.state;
  const revision = snapshot?.stateRevision;
  return Boolean(
    journal
    && journal.schemaVersion === 1
    && (journal.kind === undefined || journal.kind === 'post_unregister_cleanup')
    && journal.distroName === DISTRO_NAME
    && OWNER_ID_PATTERN.test(journal.ownerId)
    // Journals written before the phased transaction used the same durable
    // snapshot shape but had no phase. They can only have been written after
    // unregister, so retain a safe migration path for an interrupted local
    // 1.2.8 cleanup instead of leaving it permanently unrecoverable.
    && (journal.phase === undefined || journal.phase === 'prepared' || journal.phase === 'unregistered')
    && snapshot
    && snapshot.schemaVersion === 1
    && snapshot.distroName === DISTRO_NAME
    && (revision === undefined || (Number.isSafeInteger(revision) && revision >= 0))
    && validOwnershipRecord(snapshot, journal.ownerId, expectedInstallDir),
  );
}

function sameUnregisterJournalState(snapshot, candidate) {
  if (!snapshot || !candidate) return false;
  const snapshotRevision = Number.isSafeInteger(snapshot.stateRevision) ? snapshot.stateRevision : 0;
  const candidateRevision = Number.isSafeInteger(candidate.stateRevision) ? candidate.stateRevision : 0;
  if (candidateRevision !== snapshotRevision && candidateRevision !== snapshotRevision + 1) return false;
  const expected = { ...snapshot };
  const actual = { ...candidate };
  delete expected.stateRevision;
  delete actual.stateRevision;
  return isDeepStrictEqual(actual, expected);
}

function validInstallTransaction(transaction, manifest, ownershipId, expectedInstallDir = '') {
  return Boolean(
    transaction
    && transaction.schemaVersion === 1
    && OWNER_ID_PATTERN.test(transaction.id)
    && OWNER_ID_PATTERN.test(ownershipId)
    && transaction.ownershipId === ownershipId
    && transaction.distroName === DISTRO_NAME
    && transaction.version === manifest.version
    && transaction.manifestSha256 === manifestSha256(manifest)
    && transaction.rootfsSha256 === manifest.rootfs.sha256
    && transaction.runtimeSha256 === manifest.runtime.sha256
    && typeof transaction.installDir === 'string'
    && (!expectedInstallDir || sameWindowsPath(transaction.installDir, expectedInstallDir))
    && (transaction.installDirObjectId === undefined
      || PATH_OBJECT_ID_PATTERN.test(transaction.installDirObjectId))
    && ['prepared', 'imported', 'initializing', 'failed'].includes(transaction.phase),
  );
}

const DOWNLOAD_PATH_TRANSACTION_PHASES = new Set([
  'prepared',
  'stopped',
  'target_configured',
  'rollback_required',
]);

function validDownloadPathIdentity(identity, { requireWslPath = false } = {}) {
  if (!identity || typeof identity !== 'object' || Array.isArray(identity)) return false;
  let windowsPath;
  try {
    windowsPath = validateWindowsDownloadPath(identity.windowsPath);
  } catch {
    return false;
  }
  if (windowsPath !== identity.windowsPath
      || !['NTFS', 'REFS'].includes(String(identity.fileSystem || '').toUpperCase())) return false;
  if (identity.objectId !== undefined && !PATH_OBJECT_ID_PATTERN.test(identity.objectId)) return false;
  if (identity.wslPath !== undefined
      && identity.wslPath !== ''
      && !isSafeWslDrivePath(identity.wslPath)) return false;
  return !requireWslPath || isSafeWslDrivePath(identity.wslPath || '');
}

function validDownloadPathTransaction(transaction, expectedInstallDir = '') {
  if (transaction === undefined || transaction === null) return true;
  if (!transaction || typeof transaction !== 'object' || Array.isArray(transaction)
      || transaction.schemaVersion !== 1
      || transaction.kind !== 'download_path_change'
      || !OWNER_ID_PATTERN.test(transaction.id)
      || transaction.distroName !== DISTRO_NAME
      || typeof transaction.expectedVersion !== 'string'
      || transaction.expectedVersion.length > 64
      || typeof transaction.wasRunning !== 'boolean'
      || !DOWNLOAD_PATH_TRANSACTION_PHASES.has(transaction.phase)
      || !validDownloadPathIdentity(transaction.target, { requireWslPath: transaction.phase === 'target_configured' })
      || (transaction.previous !== null
        && !validDownloadPathIdentity(transaction.previous, { requireWslPath: false }))) {
    return false;
  }
  if (expectedInstallDir !== undefined && expectedInstallDir !== '') {
    try {
      if (!sameWindowsPath(transaction.installDir, expectedInstallDir)) return false;
    } catch {
      return false;
    }
  }
  return typeof transaction.installDir === 'string'
    && (() => {
      try { return sameWindowsPath(validateWindowsDownloadPath(transaction.installDir), expectedInstallDir || transaction.installDir); } catch { return false; }
    })();
}

function sameDownloadPathTransactionIdentity(transaction) {
  const previous = transaction?.previous;
  const target = transaction?.target;
  if (!previous || !target
      || !validDownloadPathIdentity(previous, { requireWslPath: true })
      || !validDownloadPathIdentity(target, { requireWslPath: false })) return false;
  return sameWindowsPath(previous.windowsPath, target.windowsPath)
    && String(previous.fileSystem).toUpperCase() === String(target.fileSystem).toUpperCase()
    && previous.objectId !== undefined
    && previous.objectId !== ''
    && previous.objectId === target.objectId;
}

function identityRuntimeSha256(identity) {
  for (const field of ['runtimeSha256', 'runtimeBundleSha256', 'buildSha256']) {
    const value = identity?.[field];
    if (typeof value === 'string' && SHA256_PATTERN.test(value)) return value;
  }
  return '';
}

function identityRuntimeTransactionId(identity) {
  return typeof identity?.runtimeTransactionId === 'string' && OWNER_ID_PATTERN.test(identity.runtimeTransactionId)
    ? identity.runtimeTransactionId
    : '';
}

function diagnosticBuildIdentity(manifestValue, status, windowsHelperSha256, recoveryIdentity = null) {
  const manifest = validateApplianceManifest(manifestValue);
  if (!manifest.productVersion || !manifest.applianceBuildId
      || !SHA256_PATTERN.test(manifest.inputs?.androidMainBuiltSha256 || '')
      || (requiresStandaloneNetworkPreflightIdentity(manifest.version)
        && !SHA256_PATTERN.test(manifest.inputs?.androidNetworkPreflightBuiltSha256 || ''))
      || (requiresNetworkDiagnosticsV2(manifest.version)
        && (manifest.inputs?.androidCaBundlePath !== '/usr/local/android/cert.pem'
          || !SHA256_PATTERN.test(manifest.inputs?.androidCaBundleSha256 || '')))
      || !SHA256_PATTERN.test(manifest.inputs?.rendererSha256 || '')
      || !SHA256_PATTERN.test(windowsHelperSha256 || '')) {
    throw new TypeError('Desktop build identity is unavailable');
  }
  const runtimeSha256 = identityRuntimeSha256(status);
  const runtimeTransactionId = identityRuntimeTransactionId(status);
  const expected = {
    productVersion: manifest.productVersion,
    applianceBuildId: manifest.applianceBuildId,
    runtimeSha256: manifest.runtime.sha256,
    androidMainBuiltSha256: manifest.inputs.androidMainBuiltSha256,
    ...(requiresStandaloneNetworkPreflightIdentity(manifest.version)
      ? { androidNetworkPreflightBuiltSha256: manifest.inputs.androidNetworkPreflightBuiltSha256 }
      : {}),
    ...(requiresNetworkDiagnosticsV2(manifest.version)
      ? {
        androidCaBundlePath: manifest.inputs.androidCaBundlePath,
        androidCaBundleSha256: manifest.inputs.androidCaBundleSha256,
      }
      : {}),
    rendererSha256: manifest.inputs.rendererSha256,
    windowsHelperSha256,
  };
  if (requiresInteropRecoveryBinding(manifest.version)) {
    let descriptor;
    try {
      descriptor = validateInteropRecoveryDescriptor(recoveryIdentity?.recovery?.descriptor, {
        expectedVersion: manifest.version,
      });
    } catch {
      throw new TypeError('Desktop build identity recovery descriptor identity is unavailable');
    }
    if (!SHA256_PATTERN.test(recoveryIdentity?.bootstrapSha256 || '')
        || !SHA256_PATTERN.test(recoveryIdentity?.recovery?.sha256 || '')) {
      throw new TypeError('Desktop build identity recovery descriptor identity is unavailable');
    }
    if (descriptor.windowsHelper.sha256 !== windowsHelperSha256) {
      throw new TypeError('Desktop build identity recovery Windows helper SHA-256 mismatch');
    }
    if (descriptor.bootstrap.sha256 !== recoveryIdentity.bootstrapSha256) {
      throw new TypeError('Desktop build identity recovery bootstrap SHA-256 mismatch');
    }
    expected.wslInteropBootstrapSha256 = recoveryIdentity.bootstrapSha256;
    expected.wslInteropRecoveryDescriptorSha256 = recoveryIdentity.recovery.sha256;
  }
  for (const [field, value] of Object.entries(expected)) {
    if (field === 'wslInteropBootstrapSha256' || field === 'wslInteropRecoveryDescriptorSha256') continue;
    let observed = field === 'runtimeSha256' ? runtimeSha256 : status?.[field];
    if (field === 'windowsHelperSha256' && !observed
        && ['stopped', 'unprepared'].includes(status?.network?.state)) {
      observed = windowsHelperSha256;
    }
    if (observed !== value) throw new TypeError(`Desktop build identity ${field} mismatch`);
  }
  if (!runtimeTransactionId) throw new TypeError('Desktop build identity runtime transaction is unavailable');
  const canonical = JSON.stringify({ schemaVersion: 1, ...expected });
  return {
    ...expected,
    desktopBuildId: crypto.createHash('sha256').update(canonical, 'utf8').digest('hex'),
    runtimeTransactionId,
  };
}

function classifyUpdateRuntimeIdentity(appliance, transaction) {
  const actualVersion = String(appliance?.version || '');
  const actualRuntimeSha256 = identityRuntimeSha256(appliance);
  const actualRuntimeTransactionId = identityRuntimeTransactionId(appliance);
  if (actualVersion === transaction.toVersion
      && actualRuntimeSha256 === transaction.toRuntimeSha256
      && actualRuntimeTransactionId === transaction.id) {
    return 'committed';
  }
  if (actualVersion === transaction.fromVersion
      && actualRuntimeSha256 === transaction.fromRuntimeSha256
      && actualRuntimeTransactionId === transaction.fromRuntimeTransactionId) {
    return 'rolled_back';
  }
  return 'unknown';
}

function validateUpdateTransaction(transaction) {
  if (!transaction || transaction.schemaVersion !== 2 || !OWNER_ID_PATTERN.test(transaction.id)
      || !['update', 'repair'].includes(transaction.mode)
      || typeof transaction.fromVersion !== 'string' || typeof transaction.toVersion !== 'string'
      || !SHA256_PATTERN.test(transaction.fromRuntimeSha256)
      || !SHA256_PATTERN.test(transaction.toRuntimeSha256)
      || !OWNER_ID_PATTERN.test(transaction.fromRuntimeTransactionId)
      || !SHA256_PATTERN.test(transaction.fromManifestSha256)
      || !SHA256_PATTERN.test(transaction.toManifestSha256)) {
    throw new WslManagerError('distro_corrupt', { reason: 'Interrupted update transaction is invalid' });
  }
  return transaction;
}

function validatePersistedUpdateBindings(state, transaction) {
  let sourceManifest;
  let targetManifest;
  try {
    sourceManifest = validateApplianceManifest(state.manifest);
    targetManifest = validateApplianceManifest(state.pendingManifest);
  } catch {
    throw new WslManagerError('distro_corrupt', {
      reason: 'Interrupted update manifest bindings are unavailable or invalid',
      transactionId: transaction.id,
    });
  }
  const matches = manifestSha256(sourceManifest) === transaction.fromManifestSha256
    && manifestSha256(targetManifest) === transaction.toManifestSha256
    && sourceManifest.version === transaction.fromVersion
    && sourceManifest.runtime.sha256 === transaction.fromRuntimeSha256
    && targetManifest.version === transaction.toVersion
    && targetManifest.runtime.sha256 === transaction.toRuntimeSha256
    && state.installedVersion === transaction.fromVersion
    && state.installedRuntimeSha256 === transaction.fromRuntimeSha256
    && state.installedRuntimeTransactionId === transaction.fromRuntimeTransactionId;
  if (!matches) {
    throw new WslManagerError('distro_corrupt', {
      reason: 'Interrupted update host state does not match its persisted manifest/runtime bindings',
      transactionId: transaction.id,
    });
  }
  return { sourceManifest, targetManifest };
}

function parseDistroList(value) {
  const rows = [];
  for (const raw of String(value || '').split(/\r?\n/)) {
    const line = raw.replace(/\u0000/g, '').trim();
    if (!line || /NAME\s+STATE\s+VERSION/i.test(line)) continue;
    const normalized = line.replace(/^\*\s*/, '');
    const match = normalized.match(/^(.*?)\s+\S+\s+([12])\s*$/);
    if (match) rows.push({ name: match[1].trim(), version: Number(match[2]), raw: normalized });
  }
  return rows;
}

function parseJsonOutput(output, label) {
  try { return JSON.parse(String(output || '').trim()); } catch { throw new Error(`${label} returned invalid JSON`); }
}

function parseControlStatus(output, label) {
  const response = parseJsonOutput(output, label);
  if (response && typeof response === 'object' && response.status && typeof response.status === 'object') {
    if (response.ok === false) {
      const error = new Error(response.error?.message || `${label} failed`);
      error.code = response.error?.code;
      error.action = response.error?.action;
      throw error;
    }
    return response.status;
  }
  return response;
}

function controlErrorFromProcess(error, label) {
  for (const candidate of [error?.stdout, error?.message]) {
    if (typeof candidate !== 'string' || !candidate.trim().startsWith('{')) continue;
    try {
      const response = JSON.parse(candidate.trim());
      if (response?.ok === false && response.error && typeof response.error.message === 'string') {
        const parsed = new Error(response.error.message || `${label} failed`);
        parsed.name = 'ApplianceControlError';
        parsed.controlCode = typeof response.error.code === 'string' ? response.error.code : 'control_failed';
        parsed.controlAction = typeof response.error.action === 'string' ? response.error.action : '';
        parsed.controlStatus = response.status && typeof response.status === 'object' ? response.status : null;
        return parsed;
      }
    } catch { /* Try the next representation or preserve the process error. */ }
  }
  return error;
}

function requestJson(url, options = {}) {
  return new Promise((resolve, reject) => {
    const parsed = new URL(url);
    if (parsed.protocol !== 'http:' || parsed.hostname !== '127.0.0.1') {
      reject(new Error('Only the IPv4 loopback endpoint is allowed'));
      return;
    }
    const request = http.get(parsed, {
      timeout: options.timeoutMs || 5_000,
      headers: options.headers || {},
    }, (response) => {
      const chunks = [];
      let length = 0;
      response.on('data', (chunk) => {
        length += chunk.length;
        if (length > (options.maxBytes || 1024 * 1024)) {
          request.destroy(new Error('Local API response is too large'));
          return;
        }
        chunks.push(Buffer.from(chunk));
      });
      response.on('end', () => {
        const body = Buffer.concat(chunks).toString('utf8');
        let payload = null;
        try { payload = body ? JSON.parse(body) : null; } catch { /* Preserve non-JSON body for identity rejection. */ }
        resolve({ statusCode: response.statusCode || 0, payload, body });
      });
    });
    request.on('timeout', () => request.destroy(new Error('Local API request timed out')));
    request.on('error', reject);
  });
}

function applianceIdentityMatches(identity, service = EXPECTED_SERVICE, runtime = EXPECTED_RUNTIME, expectedVersion = '') {
  return Boolean(
    identity
    && identity.service === service
    && identity.runtime === runtime
    && (!expectedVersion || identity.version === expectedVersion),
  );
}

function identityMismatchReason(identity, expectedVersion = '') {
  if (!identity || typeof identity !== 'object') return 'appliance identity is missing';
  if (identity.service !== EXPECTED_SERVICE || identity.runtime !== EXPECTED_RUNTIME) return 'unexpected appliance service identity';
  if (expectedVersion && identity.version !== expectedVersion) {
    return `appliance version ${String(identity.version || '<missing>')} does not match ${expectedVersion}`;
  }
  return '';
}

function lifecycleTargetCompletedSinceIntent(intentState, currentState, manifest) {
  const targetRuntimeSha256 = manifest?.runtime?.sha256;
  if (!SHA256_PATTERN.test(targetRuntimeSha256 || '') || currentState?.updateTransaction) return false;
  const intentTransactionId = intentState?.installedRuntimeTransactionId;
  const currentTransactionId = currentState?.installedRuntimeTransactionId;
  return OWNER_ID_PATTERN.test(intentTransactionId || '')
    && OWNER_ID_PATTERN.test(currentTransactionId || '')
    && intentTransactionId !== currentTransactionId
    && currentState.installedVersion === manifest.version
    && currentState.installedRuntimeSha256 === targetRuntimeSha256
    && currentState.manifest?.runtime?.sha256 === targetRuntimeSha256;
}

function healthIdentityMatches(response, service = EXPECTED_SERVICE, runtime = EXPECTED_RUNTIME, expectedVersion = '') {
  return Boolean(
    response
    && response.payload
    && applianceIdentityMatches(response.payload, service, runtime, expectedVersion),
  );
}

function normalizeCredentials(credentials) {
  if (!credentials || typeof credentials !== 'object' || Array.isArray(credentials)) throw new TypeError('Credentials must be an object');
  const limits = {
    appleEmail: 320,
    applePassword: 4096,
    mediaUserToken: 32768,
    authorizationToken: 32768,
    storefront: 16,
  };
  const normalized = {};
  for (const [key, limit] of Object.entries(limits)) {
    const value = credentials[key];
    if (value === undefined || value === null || value === '') continue;
    if (typeof value !== 'string' || value.length > limit || value.includes('\u0000')) throw new TypeError(`Invalid credential field: ${key}`);
    normalized[key] = value;
  }
  if (normalized.storefront && !/^[a-z]{2}$/i.test(normalized.storefront)) throw new TypeError('Invalid credential field: storefront');
  return normalized;
}

function normalizeCredentialDelivery(credentials) {
  let normalized;
  try {
    normalized = normalizeCredentials(credentials);
  } catch {
    throw new WslManagerError('credential_delivery_failed');
  }
  const payloadBytes = Buffer.byteLength(JSON.stringify({
    schemaVersion: 1,
    credentials: normalized,
  }), 'utf8');
  if (payloadBytes > 64 * 1024) {
    throw new WslManagerError('credential_delivery_failed', {
      reason: 'Credential document is too large',
    });
  }
  return normalized;
}

function sanitizeProcessEnvironment(environment = process.env) {
  const sanitized = {};
  for (const [key, value] of Object.entries(environment || {})) {
    if (!SENSITIVE_ENVIRONMENT_KEY.test(key)
        && !BLOCKED_PROCESS_ENVIRONMENT_KEY.test(key)
        && key.toLowerCase() !== 'wsl_utf8') {
      sanitized[key] = value;
    }
  }
  sanitized.WSL_UTF8 = '1';
  return sanitized;
}

class WslManager {
  constructor(options = {}) {
    this.onUpgradeProgress = typeof options.onUpgradeProgress === 'function' ? options.onUpgradeProgress : null;
    this.distroName = options.distroName || DISTRO_NAME;
    if (this.distroName !== DISTRO_NAME) throw new TypeError(`distroName must be ${DISTRO_NAME}`);
    this.controlPath = options.controlPath || CONTROL_PATH;
    this.platform = options.platform || process.platform;
    this.arch = options.arch || os.arch();
    this.release = options.release || os.release();
    this.runner = options.runner || new ProcessRunner({ execFile: options.execFile, spawn: options.spawn });
    this.processEnvironment = sanitizeProcessEnvironment(options.processEnvironment || process.env);
    this.windowsProbeExe = options.windowsProbeExe || defaultWindowsProbePath({ resourcesPath: options.resourcesPath });
    if (!path.win32.isAbsolute(this.windowsProbeExe)) throw new TypeError('windowsProbeExe must be an absolute path');
    this.interopBootstrapExe = options.interopBootstrapExe || path.win32.join(path.win32.dirname(this.windowsProbeExe), BOOTSTRAP_FILE);
    if (!path.win32.isAbsolute(this.interopBootstrapExe)) throw new TypeError('interopBootstrapExe must be an absolute path');
    if (options.interopRecoveryIdentityProvider !== undefined && typeof options.interopRecoveryIdentityProvider !== 'function') {
      throw new TypeError('interopRecoveryIdentityProvider must be a function');
    }
    this.interopRecoveryIdentityProvider = options.interopRecoveryIdentityProvider || null;
    if (options.windowsHelperBindingProvider !== undefined && typeof options.windowsHelperBindingProvider !== 'function') {
      throw new TypeError('windowsHelperBindingProvider must be a function');
    }
    this.windowsHelperBindingProvider = options.windowsHelperBindingProvider || null;
    this.wslExe = options.wslExe || null;
    if (this.wslExe && !path.win32.isAbsolute(this.wslExe)) throw new TypeError('wslExe must be an absolute trusted path');
    this.systemDirectory = options.systemDirectory || '';
    this.request = options.request || requestJson;
    this.sleep = options.sleep || delay;
    this.now = options.now || Date.now;
    this.systemInfo = options.systemInfo || (() => this.defaultSystemInfo());
    this.downloadArtifact = options.downloadArtifact || downloadAndVerifyArtifact;
    this.verifyArtifact = options.verifyArtifact || verifyArtifactFile;
    this.processLockProvider = options.processLockProvider
      || ((kind) => acquireWindowsProcessLock(this, kind));
    if (typeof this.processLockProvider !== 'function') throw new TypeError('processLockProvider must be a function');
    this.credentialProvider = typeof options.credentialProvider === 'function' ? options.credentialProvider : null;
    this.runtimeCredentials = null;
    if (options.fileSha256 !== undefined && typeof options.fileSha256 !== 'function') {
      throw new TypeError('fileSha256 must be a function');
    }
    this.fileSha256 = options.fileSha256 || sha256File;
    if (options.networkGenerationProvider !== undefined
        && typeof options.networkGenerationProvider !== 'function') {
      throw new TypeError('networkGenerationProvider must be a function');
    }
    this.networkGenerationProvider = options.networkGenerationProvider
      || (() => crypto.randomBytes(32).toString('hex'));
    this.desktopProxyToken = crypto.randomBytes(32).toString('hex');
    this.proxyFactory = options.proxyFactory || (() => new WslLoopbackProxy({
	  windowsProbeExe: this.windowsProbeExe,
      rendererDirectory: path.resolve(__dirname, '..', 'src', 'renderer'),
      windowsHelperBindingProvider: () => this.resolveWindowsHelperBinding(),
      spawn: options.spawn,
      processEnvironment: this.processEnvironment,
      authorizeRequest: ({ headers } = {}) => this.authorizeDesktopProxyRequest(headers),
    }));
    this.appDataDir = options.appDataDir || path.join(process.env.LOCALAPPDATA || process.cwd(), 'AppleMusicDownloader');
    this.installDir = options.installDir || path.join(this.appDataDir, 'wsl');
    this.artifactDir = options.artifactDir || path.join(this.appDataDir, 'artifacts');
    // SetupController owns desktop-state.json.  Appliance release metadata is
    // intentionally kept separate so either component can update atomically.
    this.statePath = options.statePath || path.join(this.appDataDir, 'appliance-state.json');
    this.stateBackupPath = `${this.statePath}.unregister-backup`;
    this.ownershipPath = options.ownershipPath || path.join(this.appDataDir, HOST_OWNERSHIP_FILENAME);
    const defaultMusic = options.musicDir || path.join(process.env.USERPROFILE || process.cwd(), 'Music');
    this.protectedDownloadRoots = (options.protectedDownloadRoots || []).map(validateWindowsDownloadPath);
    this.pathCanonicalizer = options.pathCanonicalizer
      ? async (value) => normalizePathProbeResult(await options.pathCanonicalizer(value))
      : (value) => this.canonicalizeWindowsPathWithHelper(value);
    this.registrationIdentityProvider = typeof options.registrationIdentityProvider === 'function'
      ? options.registrationIdentityProvider
      : () => this.registrationIdentityWithHelper();
    this.registrationPresenceProvider = typeof options.registrationPresenceProvider === 'function'
      ? options.registrationPresenceProvider
      : () => this.registrationPresenceWithHelper();
    this.downloadDir = validateWindowsDownloadPath(options.downloadDir || path.join(defaultMusic, 'Apple Music Downloader'));
    this.backendPort = validPort(options.backendPort) ? options.backendPort : (options.port || DEFAULT_PORT);
    if (!validPort(this.backendPort)) throw new TypeError('backendPort must be a valid TCP port');
    // Retain the old property for library consumers, but keep the backend
    // listener distinct from the dynamic Windows proxy endpoint.
    this.preferredPort = this.backendPort;
    this.selectBackendPort = options.selectBackendPort || selectAvailableLoopbackPort;
    this.keeperLauncher = options.keeperLauncher || ((command, args, spawnOptions) => this.runner.spawn(command, args, spawnOptions));
    this.serviceBaseUrl = '';
    this.expectedService = options.expectedService || EXPECTED_SERVICE;
    this.expectedRuntime = options.expectedRuntime || EXPECTED_RUNTIME;
    this.proxy = null;
    this.proxyLocalPort = null;
    this.proxyBackendPort = null;
    // A failed proxy stop leaves the listener/helper ownership in this
    // manager.  Keep an explicit latch so a later lifecycle operation cannot
    // start or reconfigure WSL while that old process tree is still live.
    this.proxyStopPending = false;
    // The stop promise is published before invoking proxy.stop().  This is a
    // process-local admission barrier: concurrent lifecycle/readiness paths
    // must await the exact helper tree teardown instead of starting a new
    // listener while the old one is still being reaped.
    this.proxyStopPromise = null;
    this.proxyStopOwner = null;
    this.endpointPreparation = null;
    this.keeperProcess = null;
  }

  async resolveWindowsHelperBinding() {
    if (!this.windowsHelperBindingProvider) {
      throw new Error('The packaged Windows helper binding is unavailable');
    }
    return validateWindowsHelperBinding(await this.windowsHelperBindingProvider());
  }

  async assertWindowsHelperBinding(binding = null) {
    const expected = binding || await this.resolveWindowsHelperBinding();
    verifyWindowsHelperBinding(this.windowsProbeExe, expected);
    return expected;
  }

  async runTrustedWindowsHelper(args, options = {}) {
    const binding = await this.assertWindowsHelperBinding();
    let result;
    let failure;
    try {
      result = await this.runner.exec(this.windowsProbeExe, args, options);
    } catch (error) {
      failure = error;
    }
    // A helper replacement while a fixed action is in flight must turn the
    // action into a trust failure rather than becoming the next accepted hash.
    await this.assertWindowsHelperBinding(binding);
    if (failure) throw failure;
    return result;
  }

  async spawnTrustedWindowsHelper(args, options = {}) {
    const binding = await this.assertWindowsHelperBinding();
    let child;
    try {
      child = this.runner.spawn(this.windowsProbeExe, args, options);
    } catch (error) {
      throw error;
    }
    try {
      await this.assertWindowsHelperBinding(binding);
    } catch (error) {
      if (typeof child?.kill === 'function') child.kill();
      throw error;
    }
    return { child, binding };
  }

  async defaultSystemInfo() {
    // The packaged Windows API helper is injected by the main process through
    // systemInfo.  Keep this fallback non-blocking and shell-free so library
    // consumers can still inspect WSL without invoking a command interpreter.
    return { virtualizationEnabled: null, rebootRequired: false };
  }

  desktopProxyRequestHeaders() {
    return { [DESKTOP_PROXY_HEADER]: this.desktopProxyToken };
  }

  authorizeDesktopProxyRequest(headers) {
    if (!headers || typeof headers !== 'object') return false;
    let candidate = '';
    for (const [name, value] of Object.entries(headers)) {
      if (name.toLowerCase() !== DESKTOP_PROXY_HEADER.toLowerCase()) continue;
      candidate = Array.isArray(value) ? value[0] : value;
      break;
    }
    if (typeof candidate !== 'string' || !OWNER_ID_PATTERN.test(candidate)) return false;
    const actual = Buffer.from(candidate, 'ascii');
    const expected = Buffer.from(this.desktopProxyToken, 'ascii');
    try {
      return actual.length === expected.length && crypto.timingSafeEqual(actual, expected);
    } finally {
      actual.fill(0);
      expected.fill(0);
    }
  }

  hasProxyForBackend(port) {
    return Boolean(this.proxy && this.proxyBackendPort === port);
  }

  hasReusableProxyForBackend(port) {
    return this.hasProxyForBackend(port)
      && validPort(this.proxyLocalPort)
      && this.proxyLocalPort !== port
      && this.serviceBaseUrl === `http://127.0.0.1:${this.proxyLocalPort}`;
  }

  async ensureProxyStopConfirmed() {
    if (!this.proxyStopPending) return;
    if (!this.proxy) {
      // Losing the object reference is not evidence that its helper tree
      // exited.  Keep the latch set and fail closed rather than allowing a
      // replacement backend/listener to overlap an unknown owner.
      throw new WslManagerError('port_in_use', {
        reason: 'A previous local WSL proxy stop has no retained ownership handle',
      });
    }
    // discardLocalProxy retries the exact owned proxy.  If confirmation still
    // fails it preserves the object and rethrows a stable port_in_use error.
    await this.discardLocalProxy({ clearPersistedEndpoint: true });
  }

  async discardLocalProxy({ clearPersistedEndpoint = false } = {}) {
    const proxy = this.proxy;
    this.proxyLocalPort = null;
    this.proxyBackendPort = null;
    this.serviceBaseUrl = '';

    if (!proxy) {
      if (this.proxyStopPending) {
        throw new WslManagerError('port_in_use', {
          reason: 'A previous local WSL proxy stop has no retained ownership handle',
        });
      }
      if (clearPersistedEndpoint) await this.writeState({ localPort: null, usesLoopbackProxy: false });
      return;
    }

    // Set the latch synchronously, before even scheduling proxy.stop().  A
    // caller that arrives during an unresolved stop therefore fails closed or
    // joins the same promise; it cannot allocate a replacement endpoint.
    this.proxyStopPending = true;
    let stopPromise = this.proxyStopPromise;
    if (!stopPromise) {
      this.proxyStopOwner = proxy;
      stopPromise = Promise.resolve().then(() => proxy.stop());
      this.proxyStopPromise = stopPromise;
    } else if (this.proxyStopOwner !== proxy) {
      throw new WslManagerError('port_in_use', {
        reason: 'A different local WSL proxy stop is already in progress',
      });
    }

    let stopError = null;
    try {
      await stopPromise;
    } catch (error) {
      stopError = error;
    } finally {
      if (this.proxyStopPromise === stopPromise) {
        this.proxyStopPromise = null;
        this.proxyStopOwner = null;
      }
    }
    // A helper whose exit was not confirmed must remain owned by this manager.
    // Its endpoint metadata is cleared so it cannot be reused, while every
    // later preparation is forced to retry stop instead of creating overlap.
    if (stopError) {
      if (this.proxy === proxy) this.proxy = proxy;
      this.proxyStopPending = true;
    } else if (this.proxy === proxy) {
      this.proxy = null;
      this.proxyStopPending = false;
    }
    if (clearPersistedEndpoint) await this.writeState({ localPort: null, usesLoopbackProxy: false });
    if (stopError) throw managerError('port_in_use', stopError);
  }

  async windowsHelperPathToWsl(windowsPath) {
    const result = await this.runWsl([
      '--distribution', this.distroName,
      '--exec', '/usr/bin/wslpath', '-a', '-u', '--', windowsPath,
    ], { timeoutMs: 15_000 });
    const converted = result.stdout.trim();
    if (!isSafeWslExecutablePath(converted)) {
      throw new Error('wslpath returned an unsafe Windows helper path');
    }
    return converted;
  }

  async preflightNetworkGeneration(generationId, { expectedVersion = '' } = {}) {
    const identity = {
      expectedService: this.expectedService,
      expectedRuntime: this.expectedRuntime,
      expectedVersion,
    };
    let failureDetails = null;
    try {
      if (!OWNER_ID_PATTERN.test(generationId || '')) {
        throw new Error('Network generation is not a canonical 256-bit identifier');
      }
      const result = await this.runControl('network-preflight', { timeoutMs: 30_000 });
      const preflight = parseControlStatus(result.stdout, 'Appliance network preflight');
      if (!applianceIdentityMatches(preflight, this.expectedService, this.expectedRuntime, expectedVersion)
          || !networkStatusForGeneration(preflight, generationId, {
            state: 'ready',
            stage: 'complete',
            windowsPath: 'reachable',
            wslDirectPath: 'unknown',
            proxyPropagated: true,
            expectedVersion,
          })) {
        failureDetails = networkFailureDetailsForGeneration(preflight, generationId, identity);
        throw new Error('Appliance network preflight did not prove the current helper generation');
      }
      return preflight.network;
    } catch (error) {
      failureDetails = failureDetails || networkFailureDetails(error, generationId, identity);
      throw new WslManagerError('network_preflight_failed', failureDetails || {});
    }
  }

  async confirmCABundleRepairAdmission({ expectedVersion, caRepairIdentity, generationId, helperSha256 }) {
    try {
      if (!caRepairIdentity || !OWNER_ID_PATTERN.test(generationId || '')
          || !SHA256_PATTERN.test(helperSha256 || '')) return false;
      const binding = await this.assertWindowsHelperBinding();
      if (binding.sha256 !== helperSha256) return false;
      const result = await this.runControl('status', { timeoutMs: 20_000 });
      const status = parseControlStatus(result.stdout, 'CA repair admission');
      const details = networkFailureDetailsForGeneration(status, generationId, {
        expectedService: this.expectedService, expectedRuntime: this.expectedRuntime, expectedVersion,
      });
      if (!['ca_bundle_missing', 'ca_bundle_invalid'].includes(details?.stage)
          || status.state !== 'stopped' || status.windowsHelperSha256 !== helperSha256
          || status.version !== expectedVersion || status.productVersion !== expectedVersion) return false;
      const fields = ['runtimeSha256', 'runtimeTransactionId', 'applianceBuildId',
        'androidMainBuiltSha256', 'androidNetworkPreflightBuiltSha256', 'androidCaBundleSha256', 'rendererSha256'];
      return fields.every(field => SHA256_PATTERN.test(caRepairIdentity[field] || '')
        && status[field] === caRepairIdentity[field])
        && status.androidCaBundlePath === caRepairIdentity.androidCaBundlePath
        && status.androidCaBundlePath === '/usr/local/android/cert.pem';
    } catch { return false; }
  }

  async prepareNetworkForStart({ expectedVersion = '', caRepairIdentity } = {}) {
    let generationId = '';
    try {
      generationId = await this.networkGenerationProvider();
      if (!OWNER_ID_PATTERN.test(generationId || '')) {
        throw new Error('Network generation is not a canonical 256-bit identifier');
      }

      const fixedHelperPath = path.win32.normalize(this.windowsProbeExe);
      const boundHelper = await this.assertWindowsHelperBinding();
      const firstIdentity = await this.canonicalizeWindowsFileWithHelper(fixedHelperPath);
      if (!sameWindowsPath(firstIdentity.canonicalPath, fixedHelperPath)
          || !PATH_OBJECT_ID_PATTERN.test(firstIdentity.objectId || '')
          || path.win32.basename(firstIdentity.canonicalPath).toLowerCase() !== boundHelper.file) {
        throw new Error('Windows network helper identity is not the fixed regular executable');
      }

      const helperSha256 = boundHelper.sha256;

      const secondIdentity = await this.canonicalizeWindowsFileWithHelper(fixedHelperPath);
      if (!sameWindowsPath(secondIdentity.canonicalPath, fixedHelperPath)
          || !sameWindowsPath(secondIdentity.canonicalPath, firstIdentity.canonicalPath)
          || secondIdentity.objectId !== firstIdentity.objectId) {
        throw new Error('Windows network helper identity changed while it was being hashed');
      }
      await this.assertWindowsHelperBinding(boundHelper);

      const helperWslPath = await this.windowsHelperPathToWsl(secondIdentity.canonicalPath);
      const payload = Buffer.from(JSON.stringify({
        schemaVersion: 1,
        generationId,
        helperPath: helperWslPath,
        helperSha256,
      }), 'utf8');
      let prepared;
      try {
        const result = await this.runControl('network-prepare-stdin', {
          input: payload,
          timeoutMs: 30_000,
          maxInputBytes: 1024,
        });
        prepared = parseControlStatus(result.stdout, 'Appliance network preparation');
      } finally {
        payload.fill(0);
      }
      if (!applianceIdentityMatches(prepared, this.expectedService, this.expectedRuntime, expectedVersion)
          || !networkStatusForGeneration(prepared, generationId, {
            state: 'prepared',
            stage: 'prepared',
            windowsPath: 'unknown',
            wslDirectPath: 'unknown',
            proxyPropagated: false,
            expectedVersion,
          })) {
        throw new Error('Appliance network preparation did not bind the current helper generation');
      }

      try {
        return await this.preflightNetworkGeneration(generationId, { expectedVersion });
      } catch (error) {
        const admission = { expectedVersion, caRepairIdentity, generationId, helperSha256 };
        if (caRepairIdentity && error instanceof WslManagerError
            && error.code === 'network_preflight_failed'
            && ['ca_bundle_missing', 'ca_bundle_invalid'].includes(error.details?.stage)
            && await this.confirmCABundleRepairAdmission(admission)) {
          // Not network-ready: only the supervisor's same-version CA repair
          // exception may accept this generation, and preflight the replacement.
          return { caRepairAdmission: admission };
        }
        throw error;
      }
    } catch (error) {
      if (error instanceof WslManagerError && error.code === 'network_preflight_failed') throw error;
      throw new WslManagerError('network_preflight_failed');
    }
  }

  async cleanupNetworkBridge(options = {}) {
    return this.withLifecycleLock(() => this.cleanupNetworkBridgeUnlocked(options));
  }

  async cleanupNetworkBridgeUnlocked({ expectedVersion = '' } = {}) {
    const state = await this.readState();
    const targetVersion = expectedVersion || state.installedVersion || '';
    if (typeof targetVersion !== 'string' || !targetVersion) {
      throw new WslManagerError('network_preflight_failed');
    }
    try {
      await this.verifyDistroOwnership({ expectedVersion: targetVersion });
    } catch (error) {
      throw managerError('network_preflight_failed', error);
    }

    let before;
    try {
      const result = await this.runControl('status', { timeoutMs: 20_000 });
      before = parseControlStatus(result.stdout, 'Appliance status');
    } catch (error) {
      throw managerError('network_preflight_failed', error);
    }
    if (!applianceIdentityMatches(before, this.expectedService, this.expectedRuntime, targetVersion)
        || before.state !== 'stopped') {
      throw new WslManagerError('network_preflight_failed');
    }
    // A preparation failure can happen before a broker is created. Treat that
    // already-unprepared state as an idempotent no-op; no control operation is
    // needed and, crucially, no lifecycle stop is implied.
    if (unpreparedNetworkStatus(before, { expectedVersion: targetVersion })) {
      return { cleaned: false, status: before };
    }
    const generationId = before.network?.generationId;
    if (!OWNER_ID_PATTERN.test(generationId || '')
        || !['prepared', 'ready', 'failed'].includes(before.network?.state)
        || before.network?.path !== NETWORK_BRIDGE_PATH) {
      throw new WslManagerError('network_preflight_failed');
    }
    const payload = Buffer.from(JSON.stringify({
      schemaVersion: 1,
      generationId,
      version: targetVersion,
    }), 'utf8');
    let after;
    try {
      const result = await this.runControl('network-cleanup-stdin', {
        input: payload,
        timeoutMs: 30_000,
        maxInputBytes: 1024,
      });
      after = parseControlStatus(result.stdout, 'Appliance network cleanup');
    } catch (error) {
      throw managerError('network_preflight_failed', error);
    } finally {
      payload.fill(0);
    }
    if (!applianceIdentityMatches(after, this.expectedService, this.expectedRuntime, targetVersion)
        || after.state !== 'stopped'
        || !unpreparedNetworkStatus(after, { expectedVersion: targetVersion })) {
      throw new WslManagerError('network_preflight_failed');
    }
    return { cleaned: true, status: after };
  }

  async resolveWslExecutable() {
    if (this.wslExe) return this.wslExe;
    let result;
    try {
      result = await this.runTrustedWindowsHelper(['system-paths'], {
        timeoutMs: 15_000,
        env: this.processEnvironment,
      });
    } catch (error) {
      throw managerError('wsl_missing', error);
    }
    const identity = parseJsonOutput(result.stdout, 'Windows system path probe');
    if (identity?.version !== 1) {
      throw new WslManagerError('wsl_update_required', { reason: 'Windows system path probe schema is not supported' });
    }
    this.systemDirectory = identity.systemDirectory;
    this.wslExe = validateTrustedSystemExecutable(identity.wslPath, 'wsl.exe', identity.systemDirectory);
    return this.wslExe;
  }

  async runWsl(args, options = {}) {
    const executable = await this.resolveWslExecutable();
    return this.runner.exec(executable, args, { ...options, env: this.processEnvironment });
  }

  async canonicalizeWindowsPathWithHelper(value) {
    const normalized = validateWindowsDownloadPath(value);
    let result;
    try {
      result = await this.runTrustedWindowsHelper(['canonicalize-path', normalized], {
        timeoutMs: 15_000,
        env: this.processEnvironment,
      });
    } catch (error) {
      throw managerError('path_unavailable', error);
    }
    return normalizePathProbeResult(parseJsonOutput(result.stdout, 'Windows path probe'));
  }

  async canonicalizeWindowsFileWithHelper(value) {
    const normalized = validateWindowsDownloadPath(value);
    let result;
    try {
      result = await this.runTrustedWindowsHelper(['canonicalize-file', normalized], {
        timeoutMs: 15_000,
        env: this.processEnvironment,
      });
    } catch (error) {
      throw managerError('path_unavailable', error);
    }
    return normalizeFileProbeResult(parseJsonOutput(result.stdout, 'Windows file probe'));
  }

  async registrationIdentityWithHelper() {
    let result;
    try {
      result = await this.runTrustedWindowsHelper(['wsl-appliance-identity'], {
        timeoutMs: 15_000,
        env: this.processEnvironment,
      });
    } catch (error) {
      throw managerError('distro_name_conflict', error);
    }
    return normalizeRegistrationIdentity(parseJsonOutput(result.stdout, 'Windows WSL registration identity'));
  }

  async registrationPresenceWithHelper() {
    let result;
    try {
      result = await this.runTrustedWindowsHelper(['wsl-appliance-presence'], {
        timeoutMs: 15_000,
        env: this.processEnvironment,
      });
      return normalizeRegistrationPresence(parseJsonOutput(result.stdout, 'Windows WSL registration presence'));
    } catch (error) {
      if (error instanceof WslManagerError) throw error;
      throw managerError('orphaned_install_state', error);
    }
  }

  async canonicalDownloadPathOutsideRoots(value) {
    const lexical = validateDownloadPathOutsideRoots(value, this.protectedDownloadRoots);
    const selected = await this.pathCanonicalizer(lexical);
    for (const protectedRoot of this.protectedDownloadRoots) {
      let protectedIdentity;
      try {
        protectedIdentity = await this.pathCanonicalizer(protectedRoot);
      } catch (error) {
        throw managerError('path_unavailable', error);
      }
      if (windowsPathIsWithin(protectedIdentity.canonicalPath, selected.canonicalPath)
          || windowsPathIsWithin(selected.canonicalPath, protectedIdentity.canonicalPath)) {
        throw new WslManagerError('path_unavailable', {
          reason: 'Download path final identity overlaps protected application data or installation files',
        });
      }
    }
    return selected;
  }

  async ensureKeeper() {
    if (this.keeperProcess && this.keeperProcess.exitCode === null && !this.keeperProcess.killed) return;
    try {
      await this.runWsl([
        '--distribution', this.distroName,
        '--exec', '/usr/bin/test', '-x', KEEPALIVE_PATH,
      ], { timeoutMs: 20_000 });
    } catch (error) {
      throw new WslManagerError('distro_corrupt', {
        reason: 'Installed runtime has no executable WSL keepalive helper; repair or force-update the runtime',
      });
    }

    const executable = await this.resolveWslExecutable();
    const args = ['--distribution', this.distroName, '--exec', KEEPALIVE_PATH];
    let child;
    try {
      child = this.keeperLauncher(executable, args, {
        windowsHide: true,
        shell: false,
        detached: true,
        stdio: 'ignore',
        env: this.processEnvironment,
      });
      if (!child || typeof child.unref !== 'function') throw new Error('Keepalive launcher returned no child process');
    } catch (error) {
      throw managerError('distro_corrupt', error);
    }
    this.keeperProcess = child;
    const clear = () => {
      if (this.keeperProcess === child) this.keeperProcess = null;
    };
    child.once?.('error', clear);
    child.once?.('exit', clear);
    child.unref();
  }

  async runControl(command, options = {}) {
    if (!CONTROL_COMMANDS.has(command)) throw new TypeError(`Unsupported appliance control command: ${command}`);
    const args = ['--distribution', this.distroName, '--exec', this.controlPath, command];
    try {
      if (options.input !== undefined) {
        return await this.runner.input(await this.resolveWslExecutable(), args, options.input, {
          timeoutMs: options.timeoutMs || 60_000,
          maxInputBytes: options.maxInputBytes || 64 * 1024,
          secrets: options.secrets || [],
          env: this.processEnvironment,
        });
      }
      return await this.runWsl(args, { timeoutMs: options.timeoutMs || 60_000, secrets: options.secrets || [] });
    } catch (error) {
      throw controlErrorFromProcess(error, `Appliance ${command}`);
    }
  }

  async readUnregisterJournal() {
    let journalStat;
    try {
      journalStat = await fsp.lstat(this.stateBackupPath);
    } catch (error) {
      if (error?.code === 'ENOENT') return null;
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance recovery journal is unreadable' });
    }
    if (!journalStat.isFile() || journalStat.size < 1 || journalStat.size > MAX_UNREGISTER_JOURNAL_BYTES) {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance recovery journal is invalid' });
    }
    let journal;
    try {
      journal = JSON.parse(await fsp.readFile(this.stateBackupPath, 'utf8'));
    } catch {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance recovery journal is unreadable' });
    }
    if (!validUnregisterJournal(journal, this.installDir)) {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance recovery journal is invalid' });
    }
    return journal;
  }

  async hasUnregisterJournal() {
    return Boolean(await this.readUnregisterJournal());
  }

  async readState({ allowUnregisterJournal = false } = {}) {
    if (!allowUnregisterJournal && await this.hasUnregisterJournal()) {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance cleanup recovery is incomplete' });
    }
    try {
      const document = JSON.parse(await fsp.readFile(this.statePath, 'utf8'));
      if (!document || document.schemaVersion !== 1 || document.distroName !== this.distroName) {
        throw new Error('State schema or distro identity is invalid');
      }
      if (document.stateRevision !== undefined
          && (!Number.isSafeInteger(document.stateRevision) || document.stateRevision < 0)) {
        throw new Error('State revision is invalid');
      }
      if (!validDownloadPathTransaction(document.downloadPathTransaction, this.installDir)) {
        throw new Error('Download path transaction is invalid');
      }
      return document;
    } catch (error) {
      if (error.code === 'ENOENT') {
        return {};
      }
      if (error instanceof WslManagerError) throw error;
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance state is unreadable or invalid' });
    }
  }

  async acquireProcessLock(kind) {
    let lock;
    try {
      lock = await this.processLockProvider(kind, this);
    } catch (error) {
      if (PUBLIC_WINDOWS_LOCK_ERRORS.has(error?.code)) {
        throw new WslManagerError(error.code, { reason: redactSecrets(error.message || String(error), []) });
      }
      throw managerError('distro_corrupt', error);
    }
    if (!lock || typeof lock.assertHeld !== 'function' || typeof lock.release !== 'function') {
      throw new WslManagerError('windows_lock_failed', { reason: `${kind} lock provider returned an invalid handle` });
    }
    try {
      lock.assertHeld();
    } catch (error) {
      if (PUBLIC_WINDOWS_LOCK_ERRORS.has(error?.code)) {
        throw new WslManagerError(error.code, { reason: redactSecrets(error.message || String(error), []) });
      }
      throw managerError('distro_corrupt', error);
    }
    return lock;
  }

  async withLifecycleLock(operation) {
    if (typeof operation !== 'function') throw new TypeError('Lifecycle lock operation must be a function');
    const inherited = lifecycleLockContext.getStore();
    if (inherited?.distroName === this.distroName) {
      inherited.lock.assertHeld();
      return operation({ reentrant: true });
    }
    return serializeProcessLocal(lifecycleOperationTails, this.distroName.toLowerCase(), async () => {
      const lock = await this.acquireProcessLock('lifecycle');
      let operationError = null;
      let result;
      try {
        result = await lifecycleLockContext.run(
          { distroName: this.distroName, lock },
          () => operation({ reentrant: false }),
        );
        lock.assertHeld();
      } catch (error) {
        operationError = error;
      }
      try {
        await lock.release();
      } catch (releaseError) {
        throw managerError('distro_corrupt', releaseError);
      }
      if (operationError) throw operationError;
      return result;
    });
  }

  async writeState(update, options = {}) {
    if (!update || typeof update !== 'object' || Array.isArray(update)) {
      throw new TypeError('State update must be an object');
    }
    const key = processLocalKey(this.statePath);
    return serializeProcessLocal(stateWriteTails, key, async () => {
      const lock = await this.acquireProcessLock('state');
      let temporary = '';
      let result;
      let operationError = null;
      try {
        lock.assertHeld();
        const current = await this.readState();
        const currentRevision = Number.isSafeInteger(current.stateRevision) ? current.stateRevision : 0;
        const hasImplicitRevision = Object.prototype.hasOwnProperty.call(update, 'stateRevision');
        const expectedRevision = options.expectedRevision ?? (hasImplicitRevision ? update.stateRevision : undefined);
        if (expectedRevision !== undefined
            && (!Number.isSafeInteger(expectedRevision) || expectedRevision < 0 || expectedRevision !== currentRevision)) {
          throw new WslManagerError('state_conflict', { expectedRevision, actualRevision: currentRevision });
        }
        const patch = { ...update };
        delete patch.schemaVersion;
        delete patch.distroName;
        delete patch.stateRevision;
        const document = {
          ...current,
          ...patch,
          schemaVersion: 1,
          distroName: this.distroName,
          stateRevision: currentRevision + 1,
        };
        const directory = path.dirname(this.statePath);
        temporary = `${this.statePath}.${process.pid}.${crypto.randomBytes(12).toString('hex')}.tmp`;
        await fsp.mkdir(directory, { recursive: true });
        const handle = await fsp.open(temporary, 'wx', 0o600);
        try {
          await handle.writeFile(`${JSON.stringify(document, null, 2)}\n`, 'utf8');
          await handle.sync();
        } finally {
          await handle.close();
        }
        lock.assertHeld();
        await fsp.rename(temporary, this.statePath);
        temporary = '';
        await syncDirectoryBestEffort(directory);
        result = document;
      } catch (error) {
        operationError = error;
      } finally {
        if (temporary) await fsp.unlink(temporary).catch(() => {});
      }
      try {
        await lock.release();
      } catch (releaseError) {
        throw managerError('distro_corrupt', releaseError);
      }
      if (operationError) throw operationError;
      return result;
    });
  }

  async removeStateFile() {
    const key = processLocalKey(this.statePath);
    return serializeProcessLocal(stateWriteTails, key, async () => {
      const lock = await this.acquireProcessLock('state');
      let operationError = null;
      try {
        lock.assertHeld();
        await fsp.unlink(this.statePath).catch((error) => {
          if (error?.code !== 'ENOENT') throw error;
        });
        await syncDirectoryBestEffort(path.dirname(this.statePath));
      } catch (error) {
        operationError = error;
      }
      try {
        await lock.release();
      } catch (releaseError) {
        throw managerError('distro_corrupt', releaseError);
      }
      if (operationError) throw operationError;
    });
  }

  async stateFileExists() {
    try {
      const stateStat = await fsp.lstat(this.statePath);
      if (!stateStat.isFile()) {
        throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance state file is not a regular file' });
      }
      return true;
    } catch (error) {
      if (error?.code === 'ENOENT') return false;
      if (error instanceof WslManagerError) throw error;
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance state file is unreadable' });
    }
  }

  async writeJsonAtomicallyLocked(lock, destination, document) {
    lock.assertHeld();
    const directory = path.dirname(destination);
    const temporary = `${destination}.${process.pid}.${crypto.randomBytes(12).toString('hex')}.tmp`;
    try {
      await fsp.mkdir(directory, { recursive: true });
      const handle = await fsp.open(temporary, 'wx', 0o600);
      try {
        await handle.writeFile(`${JSON.stringify(document, null, 2)}\n`, 'utf8');
        await handle.sync();
      } finally {
        await handle.close();
      }
      lock.assertHeld();
      await fsp.rename(temporary, destination);
      await syncDirectoryBestEffort(directory);
    } finally {
      await fsp.unlink(temporary).catch(() => {});
    }
  }

  async prepareUnregisterJournal() {
    const key = processLocalKey(this.statePath);
    return serializeProcessLocal(stateWriteTails, key, async () => {
      const lock = await this.acquireProcessLock('state');
      let operationError = null;
      let result;
      try {
        lock.assertHeld();
        if (await this.readUnregisterJournal()) {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance cleanup recovery is already pending' });
        }
        if (!await this.stateFileExists()) {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance state is missing before unregister' });
        }
        const snapshot = await this.readState({ allowUnregisterJournal: true });
        const ownerId = await this.readOwnershipId();
        if (!validOwnershipRecord(snapshot, ownerId, this.installDir)) {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance cleanup ownership is no longer valid' });
        }
        const journal = {
          schemaVersion: 1,
          kind: 'post_unregister_cleanup',
          phase: 'prepared',
          distroName: this.distroName,
          ownerId,
          state: snapshot,
        };
        await this.writeJsonAtomicallyLocked(lock, this.stateBackupPath, journal);
        result = journal;
      } catch (error) {
        operationError = error;
      }
      try {
        await lock.release();
      } catch (releaseError) {
        throw managerError('distro_corrupt', releaseError);
      }
      if (operationError) {
        if (operationError instanceof WslManagerError) throw operationError;
        throw managerError('distro_corrupt', operationError);
      }
      return result;
    });
  }

  async markUnregisterJournalUnregistered() {
    const key = processLocalKey(this.statePath);
    return serializeProcessLocal(stateWriteTails, key, async () => {
      const lock = await this.acquireProcessLock('state');
      let operationError = null;
      try {
        lock.assertHeld();
        const journal = await this.readUnregisterJournal();
        if (!journal) {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance unregister recovery journal is missing' });
        }
        if (journal.phase === undefined || journal.phase === 'unregistered') return journal;
        if (journal.phase !== 'prepared') {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance unregister recovery phase is invalid' });
        }
        const promoted = {
          ...journal,
          kind: 'post_unregister_cleanup',
          phase: 'unregistered',
        };
        await this.writeJsonAtomicallyLocked(lock, this.stateBackupPath, promoted);
        return promoted;
      } catch (error) {
        operationError = error;
      } finally {
        try {
          await lock.release();
        } catch (releaseError) {
          throw managerError('distro_corrupt', releaseError);
        }
      }
      if (operationError) {
        if (operationError instanceof WslManagerError) throw operationError;
        throw managerError('distro_corrupt', operationError);
      }
      return null;
    });
  }

  async discardPreparedUnregisterJournal() {
    const key = processLocalKey(this.statePath);
    return serializeProcessLocal(stateWriteTails, key, async () => {
      const lock = await this.acquireProcessLock('state');
      let operationError = null;
      try {
        lock.assertHeld();
        const journal = await this.readUnregisterJournal();
        if (!journal || journal.phase !== 'prepared') {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance unregister recovery phase is invalid' });
        }
        if (!await this.stateFileExists()) {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance state disappeared during unregister recovery' });
        }
        const state = await this.readState({ allowUnregisterJournal: true });
        const ownerId = await this.readOwnershipId();
        if (ownerId !== journal.ownerId
            || !sameUnregisterJournalState(journal.state, state)
            || !validOwnershipRecord(state, ownerId, this.installDir)) {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance unregister recovery ownership changed unexpectedly' });
        }
        await fsp.unlink(this.stateBackupPath);
        await syncDirectoryBestEffort(path.dirname(this.statePath));
      } catch (error) {
        operationError = error;
      }
      try {
        await lock.release();
      } catch (releaseError) {
        throw managerError('distro_corrupt', releaseError);
      }
      if (operationError) {
        if (operationError instanceof WslManagerError) throw operationError;
        throw managerError('distro_corrupt', operationError);
      }
    });
  }

  async restoreUnregisterStateLocked(lock, state) {
    lock.assertHeld();
    if (await this.stateFileExists()) {
      throw new WslManagerError('state_conflict', { reason: 'State file was recreated before unregister cleanup restoration' });
    }
    const priorRevision = Number.isSafeInteger(state?.stateRevision) ? state.stateRevision : 0;
    if (priorRevision < 0 || priorRevision >= Number.MAX_SAFE_INTEGER) {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance recovery state revision is invalid' });
    }
    const restored = {
      ...state,
      schemaVersion: 1,
      distroName: this.distroName,
      // State removal is itself a committed transition. Never reuse the old
      // revision or let a stale compare-and-swap write succeed.
      stateRevision: priorRevision + 1,
    };
    await this.writeJsonAtomicallyLocked(lock, this.statePath, restored);
    return restored;
  }

  async reconcileUnregisteredJournalLocked(lock, journal) {
    lock.assertHeld();
    if (journal.phase === 'prepared') {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance unregister has not been proven complete' });
    }
    let state = null;
    if (await this.stateFileExists()) {
      state = await this.readState({ allowUnregisterJournal: true });
      if (!sameUnregisterJournalState(journal.state, state)) {
        throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance state no longer matches its unregister recovery journal' });
      }
    }
    let ownerId = await this.readOwnershipId();
    if (ownerId && ownerId !== journal.ownerId) {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance ownership no longer matches its unregister recovery journal' });
    }
    if (state && ownerId && !validOwnershipRecord(state, ownerId, this.installDir)) {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance recovery ownership is invalid' });
    }

    // If a marker deletion failed after state removal, restore the durable
    // snapshot first. The journal remains in place until both files are known
    // absent, so a process crash cannot turn a recoverable state into a dead
    // end on the next uninstall attempt.
    if (!state && ownerId) state = await this.restoreUnregisterStateLocked(lock, journal.state);

    if (!state && !ownerId) {
      await fsp.unlink(this.stateBackupPath);
      await syncDirectoryBestEffort(path.dirname(this.statePath));
      return true;
    }

    if (state && !ownerId) {
      await fsp.unlink(this.statePath);
      await syncDirectoryBestEffort(path.dirname(this.statePath));
      if (await this.readOwnershipId()) {
        throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance ownership appeared during unregister cleanup' });
      }
      await fsp.unlink(this.stateBackupPath);
      await syncDirectoryBestEffort(path.dirname(this.statePath));
      return true;
    }

    // At this point both state and marker are present and still bound to the
    // same journal. Delete state first; on a marker failure restore a newer
    // revision and deliberately keep the journal for a fresh process retry.
    await fsp.unlink(this.statePath);
    await syncDirectoryBestEffort(path.dirname(this.statePath));
    try {
      await fsp.unlink(this.ownershipPath);
    } catch (ownerRemovalError) {
      if (ownerRemovalError?.code !== 'ENOENT') {
        await this.restoreUnregisterStateLocked(lock, journal.state);
        throw ownerRemovalError;
      }
    }
    ownerId = await this.readOwnershipId();
    if (ownerId) {
      throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance ownership remained during unregister cleanup' });
    }
    await syncDirectoryBestEffort(path.dirname(this.statePath));
    await fsp.unlink(this.stateBackupPath);
    await syncDirectoryBestEffort(path.dirname(this.statePath));
    return true;
  }

  async cleanupUnregisterHostState() {
    const key = processLocalKey(this.statePath);
    return serializeProcessLocal(stateWriteTails, key, async () => {
      const lock = await this.acquireProcessLock('state');
      let operationError = null;
      let result;
      try {
        lock.assertHeld();
        const journal = await this.readUnregisterJournal();
        if (!journal) {
          throw new WslManagerError('distro_corrupt', { reason: 'Desktop Appliance unregister recovery journal is missing' });
        }
        result = await this.reconcileUnregisteredJournalLocked(lock, journal);
      } catch (error) {
        operationError = error;
      }
      try {
        await lock.release();
      } catch (releaseError) {
        throw managerError('distro_corrupt', releaseError);
      }
      if (operationError) {
        if (operationError instanceof WslManagerError) throw operationError;
        throw managerError('distro_corrupt', operationError);
      }
      return result;
    });
  }

  async readOwnershipId() {
    let value;
    try {
      value = await fsp.readFile(this.ownershipPath, 'utf8');
    } catch (error) {
      if (error.code === 'ENOENT') return '';
      throw new WslManagerError('distro_corrupt', { reason: 'Host ownership record is unreadable' });
    }
    if (!OWNER_ID_PATTERN.test(value)) {
      throw new WslManagerError('distro_corrupt', { reason: 'Host ownership record is invalid' });
    }
    return value;
  }

  async createHostOwnershipRecord() {
    const existing = await this.readOwnershipId();
    if (existing) return existing;
    const ownerId = crypto.randomBytes(32).toString('hex');
    const temporary = `${this.ownershipPath}.${process.pid}.${crypto.randomBytes(6).toString('hex')}.tmp`;
    await fsp.mkdir(path.dirname(this.ownershipPath), { recursive: true });
    try {
      await fsp.writeFile(temporary, ownerId, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
      await fsp.rename(temporary, this.ownershipPath);
    } finally {
      await fsp.unlink(temporary).catch(() => {});
    }
    return ownerId;
  }

  async writeDistroOwnership(ownerId) {
    if (!OWNER_ID_PATTERN.test(ownerId)) throw new WslManagerError('distro_corrupt', { reason: 'Ownership identifier is invalid' });
    const payload = Buffer.from(ownerId, 'ascii');
    try {
      await this.runWsl([
        '--distribution', this.distroName, '--user', 'root', '--exec',
        '/usr/bin/mkdir', '-p', '/var/lib/applemusic-downloader',
      ], { timeoutMs: 20_000 });
      await this.runWsl([
        '--distribution', this.distroName, '--user', 'root', '--exec',
        '/usr/bin/chmod', '0700', '/var/lib/applemusic-downloader',
      ], { timeoutMs: 20_000 });
      await this.runner.input(await this.resolveWslExecutable(), [
        '--distribution', this.distroName, '--user', 'root', '--exec',
        '/usr/bin/dd', `of=${DISTRO_OWNERSHIP_PATH}`, 'bs=64', 'count=1', 'status=none',
      ], payload, {
        timeoutMs: 20_000,
        maxInputBytes: 64,
        secrets: [ownerId],
        env: this.processEnvironment,
      });
      await this.runWsl([
        '--distribution', this.distroName, '--user', 'root', '--exec',
        '/usr/bin/chmod', '0600', DISTRO_OWNERSHIP_PATH,
      ], { timeoutMs: 20_000 });
    } catch (error) {
      throw managerError('distro_corrupt', error, [ownerId]);
    } finally {
      payload.fill(0);
    }
  }

  async verifyRegisteredDistroIdentity(expectedRegistration = null) {
    let registered;
    let expectedInstall;
    try {
      registered = normalizeRegistrationIdentity(await this.registrationIdentityProvider());
      expectedInstall = await this.pathCanonicalizer(this.installDir);
    } catch (error) {
      throw managerError('distro_name_conflict', error);
    }
    if (!sameWindowsPath(registered.basePath, expectedInstall.canonicalPath)) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'Registered WSL BasePath does not match the owned Appliance install directory',
      });
    }
    if (expectedRegistration
        && (!sameWindowsPath(registered.basePath, expectedRegistration.basePath)
          || registered.wslVersion !== expectedRegistration.wslVersion)) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'Registered WSL identity changed during the Appliance lifecycle transaction',
      });
    }
    return { registered, expectedInstall };
  }

  async verifyDistroOwnership({ expectedVersion = '', allowUnregisterJournal = false } = {}) {
    const ownerId = await this.readOwnershipId();
    const state = await this.readState({ allowUnregisterJournal });
    if (!validOwnershipRecord(state, ownerId, this.installDir)) {
      throw new WslManagerError('distro_name_conflict', { reason: 'Host ownership record does not match the registered appliance state' });
    }
    const { registered, expectedInstall } = await this.verifyRegisteredDistroIdentity();
    if (!sameWindowsPath(state.installDir, expectedInstall.canonicalPath)
        || !sameWindowsPath(state.ownership.installDir, expectedInstall.canonicalPath)) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'Registered WSL BasePath does not match the owned Appliance install directory',
      });
    }
    let marker;
    try {
      marker = await this.runWsl([
        '--distribution', this.distroName, '--user', 'root', '--exec',
        '/usr/bin/cat', DISTRO_OWNERSHIP_PATH,
      ], { timeoutMs: 20_000 });
    } catch (error) {
      throw managerError('distro_name_conflict', error, [ownerId]);
    }
    if (marker.stdout !== ownerId) {
      throw new WslManagerError('distro_name_conflict', { reason: 'WSL ownership marker does not match the host record' });
    }
    let appliance;
    try {
      const probe = await this.runControl('probe', { timeoutMs: 20_000 });
      appliance = parseControlStatus(probe.stdout, 'Appliance ownership probe');
    } catch (error) {
      // The host owner, registered BasePath and in-distro nonce already match.
      // A missing or broken control CLI is therefore owned corruption, not
      // evidence that an unrelated same-name distribution owns this record.
      throw managerError('distro_corrupt', error);
    }
    if (!applianceIdentityMatches(appliance, this.expectedService, this.expectedRuntime, expectedVersion)) {
      throw new WslManagerError('distro_name_conflict', { reason: identityMismatchReason(appliance, expectedVersion) });
    }
    return { ownerId, state, appliance, registration: registered };
  }

  async registeredDistro() {
    const result = await this.runWsl(['--list', '--verbose'], { timeoutMs: 15_000 });
    return parseDistroList(result.stdout).find((entry) => entry.name.toLowerCase() === this.distroName.toLowerCase()) || null;
  }

  async proveOrphanedHostState(state, ownerId) {
    if (state?.installTransaction) {
      throw new WslManagerError('distro_corrupt', {
        reason: 'An orphan absence proof cannot replace a pending install transaction',
      });
    }
    if (!validOwnershipRecord(state, ownerId, this.installDir)) {
      throw new WslManagerError('distro_corrupt', {
        reason: 'Orphaned host ownership is not bound to the expected Appliance install directory',
      });
    }

    let cliRegistration;
    try {
      cliRegistration = await this.registeredDistro();
    } catch (error) {
      throw managerError('orphaned_install_state', error);
    }
    if (cliRegistration) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'WSL still reports an Appliance distribution with the protected name',
      });
    }

    let registryPresence;
    try {
      registryPresence = normalizeRegistrationPresence(await this.registrationPresenceProvider());
    } catch (error) {
      if (error instanceof WslManagerError) throw error;
      throw managerError('orphaned_install_state', error);
    }
    if (registryPresence.registered) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'Windows registry still reports an Appliance distribution with the protected name',
      });
    }

    try {
      await fsp.lstat(this.installDir);
    } catch (error) {
      if (error?.code === 'ENOENT') {
        return {
          ownerId,
          expectedRevision: Number.isSafeInteger(state.stateRevision) ? state.stateRevision : 0,
        };
      }
      throw new WslManagerError('orphaned_install_state', {
        reason: 'The Appliance install directory could not be proven absent',
      });
    }
    throw new WslManagerError('orphaned_install_state', {
      reason: 'The Appliance install directory still exists and will not be overwritten',
    });
  }

  async pendingUnregisterDistroIsRegistered() {
    let cliRegistration;
    let registryPresence;
    try {
      cliRegistration = await this.registeredDistro();
      registryPresence = normalizeRegistrationPresence(await this.registrationPresenceProvider());
    } catch (error) {
      if (error instanceof WslManagerError) throw error;
      throw managerError('distro_corrupt', error);
    }
    if (Boolean(cliRegistration) !== registryPresence.registered) {
      throw new WslManagerError('distro_corrupt', {
        reason: 'WSL CLI and registry disagree while unregister recovery is pending',
      });
    }
    return registryPresence.registered;
  }

  async provePendingUnregisterInstallAbsent() {
    try {
      await fsp.lstat(this.installDir);
    } catch (error) {
      if (error?.code === 'ENOENT') return;
      throw new WslManagerError('orphaned_install_state', {
        reason: 'The Appliance install directory could not be proven absent during unregister recovery',
      });
    }
    throw new WslManagerError('orphaned_install_state', {
      reason: 'The Appliance install directory remains after unregister and will not be removed automatically',
    });
  }

  async resumePendingUnregisterHostCleanup({ expectedVersion = '' } = {}) {
    const journal = await this.readUnregisterJournal();
    if (!journal) return false;

    const registered = await this.pendingUnregisterDistroIsRegistered();
    if (journal.phase === 'prepared') {
      if (registered) {
        // A failed terminate/unregister leaves a prepared journal. Re-prove
        // exact ownership before removing that admission barrier; a same-name
        // replacement can never turn a recovery retry into a destructive one.
        await this.verifyDistroOwnership({ expectedVersion, allowUnregisterJournal: true });
        await this.discardPreparedUnregisterJournal();
        return false;
      }
      // `wsl.exe` can complete unregister immediately before a process crash
      // or a journal phase-write error. Both independent presence sources now
      // prove absence, so promote the already durable snapshot without issuing
      // another WSL command.
      await this.provePendingUnregisterInstallAbsent();
      await this.markUnregisterJournalUnregistered();
    } else if (registered) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'A WSL distribution reappeared while host unregister cleanup was pending',
      });
    } else {
      await this.provePendingUnregisterInstallAbsent();
    }
    // Presence was first sampled before the install-directory absence proof.
    // Sample it again immediately before deleting host state so an external
    // same-name import cannot be mistaken for the just-unregistered appliance.
    if (await this.pendingUnregisterDistroIsRegistered()) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'A WSL distribution reappeared before host unregister cleanup could begin',
      });
    }
    await this.cleanupUnregisterHostState();
    return true;
  }

  async unregisterVerifiedDistro(options = {}) {
    const inherited = lifecycleLockContext.getStore();
    if (inherited?.distroName === this.distroName) {
      inherited.lock.assertHeld();
      return this.unregisterVerifiedDistroUnlocked(options);
    }
    return this.withLifecycleLock(() => this.unregisterVerifiedDistroUnlocked(options));
  }

  async unregisterVerifiedDistroUnlocked({ expectedVersion = '', clearState = true } = {}) {
    // Never enter a destructive WSL transaction while an older Windows proxy
    // helper is still unconfirmed.  The retained object must be retried first.
    await this.ensureProxyStopConfirmed();
    if (clearState && await this.resumePendingUnregisterHostCleanup({ expectedVersion })) {
      this.keeperProcess = null;
      return true;
    }
    const initialOwnership = await this.verifyDistroOwnership({ expectedVersion });
    try {
      await this.runControl('stop', { timeoutMs: 60_000 });
    } catch (error) {
      throw managerError('distro_corrupt', error);
    }
    // Quiesce the Windows listener and confirm all helpers before rechecking
    // ownership or issuing any terminate/unregister command.  A failed proxy
    // stop intentionally leaves the proxy reference owned by this manager and
    // aborts the destructive operation.
    await this.discardLocalProxy({ clearPersistedEndpoint: true });
    // Re-check both the host/distro nonce and the immutable service/runtime
    // identity after the stop boundary.  Any failure prohibits unregister.
    const currentOwnership = await this.verifyDistroOwnership({ expectedVersion });
    if (currentOwnership.ownerId !== initialOwnership.ownerId
        || !sameWindowsPath(currentOwnership.registration.basePath, initialOwnership.registration.basePath)) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'Appliance ownership changed after stop and before unregister',
      });
    }
    // The snapshot must be durable before terminate/unregister. Otherwise an
    // I/O failure after WSL succeeds would leave host ownership with no safe
    // recovery path on the next process start.
    if (clearState) await this.prepareUnregisterJournal();
    // The durable journal itself is an await boundary. Re-prove the complete
    // host/registry/in-distro ownership tuple after it is written and before
    // the first name-only destructive WSL command.
    const destructiveOwnership = await this.verifyDistroOwnership({
      expectedVersion,
      allowUnregisterJournal: clearState,
    });
    if (destructiveOwnership.ownerId !== currentOwnership.ownerId
        || !sameWindowsPath(destructiveOwnership.registration.basePath, currentOwnership.registration.basePath)) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'Appliance ownership changed after unregister recovery was prepared',
      });
    }
    try {
      await this.runWsl(['--terminate', this.distroName], { timeoutMs: 60_000 });
      // Do not issue a --distribution command after terminate: it could start
      // systemd again. The Windows registration identity is sufficient to
      // reject a same-name replacement before the final unregister command.
      await this.verifyRegisteredDistroIdentity(destructiveOwnership.registration);
      await this.runWsl(['--unregister', this.distroName], { timeoutMs: DEFAULT_TIMEOUT_MS });
    } catch (error) {
      throw managerError('distro_corrupt', error);
    }
    this.keeperProcess = null;
    if (clearState) {
      try {
        await this.markUnregisterJournalUnregistered();
        await this.provePendingUnregisterInstallAbsent();
        if (await this.pendingUnregisterDistroIsRegistered()) {
          throw new WslManagerError('distro_name_conflict', {
            reason: 'A WSL distribution reappeared before host state cleanup after unregister',
          });
        }
        await this.cleanupUnregisterHostState();
      } catch (error) {
        throw error instanceof WslManagerError ? error : managerError('distro_corrupt', error);
      }
    }
    return true;
  }

  async restoreBackendPort(state = null) {
    return this.withLifecycleLock(() => this.restoreBackendPortUnlocked(state));
  }

  async restoreBackendPortUnlocked(state = null) {
    const persisted = state || await this.readState();
    if (validPort(persisted.backendPort)) {
      this.backendPort = persisted.backendPort;
      this.preferredPort = persisted.backendPort;
    }
    return this.backendPort;
  }

  async ensureBackendPort(options = {}) {
    return this.withLifecycleLock(() => this.ensureBackendPortUnlocked(options));
  }

  async ensureBackendPortUnlocked({ expectedVersion = '', preserveIfRunning = false } = {}) {
    await this.ensureProxyStopConfirmed();
    const state = await this.readState();
    const candidate = await this.restoreBackendPortUnlocked(state);
    // A manager-owned proxy is an in-process witness of the backend port it
    // bridges.  Prefer that identity over a stale persisted backendPort so a
    // repeated preparation cannot move a running WSL service merely because
    // its old endpoint metadata was incomplete.  prepareLocalEndpoint will
    // subsequently discard/rebuild any same-port or otherwise invalid proxy.
    if (this.proxy && validPort(this.proxyBackendPort) && this.proxyBackendPort !== candidate) {
      this.backendPort = this.proxyBackendPort;
      this.preferredPort = this.proxyBackendPort;
      await this.writeState({ backendPort: this.proxyBackendPort });
      return { port: this.proxyBackendPort, existingService: false, changed: true };
    }
    if (this.hasProxyForBackend(candidate)) {
      await this.writeState({ backendPort: candidate });
      return { port: candidate, existingService: false, changed: false };
    }
    const directUrl = `http://127.0.0.1:${candidate}`;
    try {
      const response = await this.request(`${directUrl}/api/v1/health/ready`, { timeoutMs: 2_000 });
      if (healthIdentityMatches(response, this.expectedService, this.expectedRuntime, expectedVersion)) {
        await this.writeState({ backendPort: candidate });
        return { port: candidate, existingService: true, changed: false };
      }
    } catch { /* A stopped backend normally refuses the health request. */ }

    // Never move a backend that the supervisor says is already running.  If
    // Windows localhost forwarding is unavailable, prepareLocalEndpoint will
    // bridge this fixed backend port through a separate local proxy port.
    if (preserveIfRunning) {
      await this.writeState({ backendPort: candidate });
      return { port: candidate, existingService: false, changed: false };
    }

    let selected;
    try {
      selected = await this.selectBackendPort(candidate);
    } catch (error) {
      throw managerError('port_in_use', error);
    }
    if (!validPort(selected)) throw new WslManagerError('port_in_use', { reason: 'Port selector returned an invalid port' });
    this.backendPort = selected;
    this.preferredPort = selected;
    await this.writeState({ backendPort: selected });
    return { port: selected, existingService: false, changed: selected !== candidate };
  }

  async reconcileUpdateState(appliance, { explicitTerminal = false } = {}) {
    const state = await this.readState();
    const transaction = state.updateTransaction;
    if (!transaction || typeof transaction !== 'object') return { outcome: 'none', state, transaction: null };
    validateUpdateTransaction(transaction);
    validatePersistedUpdateBindings(state, transaction);
    const fromVersion = String(transaction.fromVersion || '');
    const toVersion = String(transaction.toVersion || '');
    const actualVersion = String(appliance?.version || '');
    const actualRuntimeSha256 = identityRuntimeSha256(appliance);
    const actualRuntimeTransactionId = identityRuntimeTransactionId(appliance);
    if (!explicitTerminal && appliance?.updateInProgress === true) {
      if (appliance.pendingTransactionId && appliance.pendingTransactionId !== transaction.id) {
        throw new WslManagerError('distro_corrupt', {
          reason: 'Supervisor is processing a different update transaction',
          transactionId: transaction.id,
        });
      }
      return { outcome: 'in_progress', state, transaction };
    }
    if (!explicitTerminal && appliance?.updateInProgress !== false) {
      throw new WslManagerError('distro_corrupt', {
        reason: 'Supervisor cannot prove whether the interrupted update is still in progress',
        transactionId: transaction.id,
      });
    }
    const outcome = classifyUpdateRuntimeIdentity(appliance, transaction);
    if (outcome === 'committed') {
      const terminalMatches = appliance.lastUpdateTransactionId === transaction.id;
      if (!explicitTerminal && appliance.lastUpdateTransactionId
          && !(terminalMatches && appliance.lastUpdateOutcome === 'committed')) {
        throw new WslManagerError('distro_corrupt', {
          reason: 'Target runtime identity conflicts with the supervisor terminal update outcome',
          transactionId: transaction.id,
        });
      }
      const updated = await this.writeState({
        manifest: state.pendingManifest || state.manifest,
        installedVersion: toVersion,
        installedRuntimeSha256: actualRuntimeSha256,
        installedRuntimeTransactionId: actualRuntimeTransactionId,
        updateTransaction: null,
        pendingManifest: null,
      }, { expectedRevision: Number.isSafeInteger(state.stateRevision) ? state.stateRevision : 0 });
      return { outcome, state: updated, transaction };
    }
    if (outcome === 'rolled_back') {
      if (!(appliance.lastUpdateTransactionId === transaction.id
          && appliance.lastUpdateOutcome === 'rolled_back')) {
        throw new WslManagerError('distro_corrupt', {
          reason: 'Source runtime identity has no matching terminal rollback outcome',
          transactionId: transaction.id,
        });
      }
      const updated = await this.writeState({
        installedVersion: fromVersion,
        installedRuntimeSha256: actualRuntimeSha256,
        installedRuntimeTransactionId: actualRuntimeTransactionId,
        updateTransaction: null,
        pendingManifest: null,
      }, { expectedRevision: Number.isSafeInteger(state.stateRevision) ? state.stateRevision : 0 });
      return { outcome, state: updated, transaction };
    }
    throw new WslManagerError('distro_corrupt', {
      reason: 'Interrupted update runtime digest/transaction identity cannot be reconciled',
      transactionId: transaction.id,
      fromVersion,
      toVersion,
      actualVersion,
      runtimeIdentityAvailable: Boolean(actualRuntimeSha256 && actualRuntimeTransactionId),
    });
  }

  async waitForUpdateReconciliation(transaction, {
    timeoutMs = SUPERVISOR_UPDATE_READY_TIMEOUT_MS,
    intervalMs = 500,
  } = {}) {
    const deadline = this.now() + timeoutMs;
    let lastReason = '';
    while (this.now() <= deadline) {
      try {
        const remaining = Math.max(1, deadline - this.now());
        const result = await this.runControl('status', { timeoutMs: Math.min(10_000, remaining) });
        const status = parseControlStatus(result.stdout, 'Appliance status');
        if (!applianceIdentityMatches(status, this.expectedService, this.expectedRuntime)) {
          lastReason = identityMismatchReason(status, '');
        } else if (status.updateInProgress !== false) {
          if (status.updateInProgress === true
              && status.pendingTransactionId
              && status.pendingTransactionId !== transaction.id) {
            lastReason = 'Supervisor is processing a different runtime transaction';
          } else if (status.updateInProgress === true) {
            lastReason = `Supervisor update is still in progress${status.updatePhase ? ` (${status.updatePhase})` : ''}`;
          } else {
            // Older supervisors do not expose this non-optional field. Their
            // source identity can be observed for minutes while an update
            // goroutine continues after its client disconnects, so it is not
            // safe to infer rollback from identity stability alone.
            lastReason = 'Supervisor does not expose an authoritative update completion state';
          }
        } else {
          const outcome = classifyUpdateRuntimeIdentity(status, transaction);
          const terminalMatches = status.lastUpdateTransactionId === transaction.id;
          if (outcome === 'committed'
              && (!status.lastUpdateTransactionId
                || (terminalMatches && status.lastUpdateOutcome === 'committed'))) {
            // The target release metadata itself contains this unpredictable
            // transaction ID. It remains authoritative after the committing
            // supervisor restarts and loses its in-memory terminal outcome.
            return { outcome, status };
          }
          if (outcome === 'rolled_back'
              && terminalMatches
              && status.lastUpdateOutcome === 'rolled_back') {
            return { outcome, status };
          }
          if (outcome === 'rolled_back') {
            lastReason = 'Source runtime is visible but this transaction has no matching terminal rollback outcome';
          } else if (terminalMatches && status.lastUpdateOutcome === 'unresolved') {
            lastReason = 'Supervisor completed the transaction with an unresolved recovery marker';
          } else {
            lastReason = 'Runtime identity has not reached an authoritative update source or target outcome';
          }
        }
      } catch (error) {
        lastReason = error?.message || String(error);
      }
      if (this.now() <= deadline) await this.sleep(intervalMs);
    }
    throw new WslManagerError('update_failed', {
      reason: redactSecrets(lastReason, []),
      transactionId: transaction.id,
      recovery: 'retry_or_repair',
      updateStatePreserved: true,
    });
  }

  async reconcileFailedUpdateControl(error, transaction) {
    let status = error?.controlStatus;
    let outcome = status ? classifyUpdateRuntimeIdentity(status, transaction) : 'unknown';
    if (outcome === 'unknown') {
      try {
        ({ outcome, status } = await this.waitForUpdateReconciliation(transaction));
      } catch (reconcileError) {
        if (reconcileError instanceof WslManagerError) throw reconcileError;
        throw new WslManagerError('update_failed', {
          reason: redactSecrets(reconcileError?.message || String(reconcileError), []),
          transactionId: transaction.id,
          recovery: 'retry_or_repair',
          updateStatePreserved: true,
        });
      }
    }
    try {
      return await this.reconcileUpdateState(status, { explicitTerminal: Boolean(error?.controlStatus) });
    } catch (reconcileError) {
      throw new WslManagerError('update_failed', {
        reason: redactSecrets(reconcileError?.details?.reason || reconcileError?.message || String(reconcileError), []),
        transactionId: transaction.id,
        recovery: 'retry_or_repair',
        updateStatePreserved: true,
      });
    }
  }

  async credentialsForRuntimeRestart() {
    if (this.credentialProvider) {
      const provided = await this.credentialProvider();
      return normalizeCredentials(provided || {});
    }
    return this.runtimeCredentials ? { ...this.runtimeCredentials } : undefined;
  }

  runtimeRestartStartOptions(expectedVersion) {
    const options = { expectedVersion };
    if (this.credentialProvider || this.runtimeCredentials) {
      options.credentialProvider = () => this.credentialsForRuntimeRestart();
    }
    return options;
  }

  async waitForSupervisorControl({
    expectedVersion = '',
    previousPID = null,
    requireRestart = false,
    timeoutMs = SUPERVISOR_READY_TIMEOUT_MS,
    intervalMs = 500,
  } = {}) {
    const deadline = this.now() + timeoutMs;
    let lastReason = '';
    while (this.now() <= deadline) {
      try {
        const remaining = Math.max(1, deadline - this.now());
        const result = await this.runControl('status', { timeoutMs: Math.min(10_000, remaining) });
        const status = parseControlStatus(result.stdout, 'Appliance status');
        if (!applianceIdentityMatches(status, this.expectedService, this.expectedRuntime, expectedVersion)) {
          lastReason = identityMismatchReason(status, expectedVersion);
        } else if (!Number.isInteger(status.supervisorPid) || status.supervisorPid <= 0) {
          lastReason = 'supervisor PID is unavailable';
        } else if (requireRestart && Number.isInteger(previousPID) && previousPID > 0 && status.supervisorPid === previousPID) {
          lastReason = 'supervisor process has not changed';
        } else {
          return status;
        }
      } catch (error) {
        lastReason = error.message;
      }
      if (this.now() <= deadline) await this.sleep(intervalMs);
    }
    throw new WslManagerError('health_timeout', { timeoutMs, reason: redactSecrets(lastReason, []) });
  }

  waitForSupervisorReady(options = {}) {
    return this.waitForSupervisorControl(options);
  }

  waitForSupervisorRestart(previousPID, options = {}) {
    return this.waitForSupervisorControl({ ...options, previousPID, requireRestart: true });
  }

  async probe({ throwOnError = false } = {}) {
    try {
      return await this.probeOrThrow();
    } catch (error) {
      const normalized = error instanceof WslManagerError ? error : managerError('wsl_update_required', error);
      if (throwOnError) throw normalized;
      return { ok: false, error: normalized.toJSON() };
    }
  }

  async probeOrThrow() {
    if (this.platform !== 'win32') throw new WslManagerError('unsupported_windows');
    if (this.arch !== 'x64') throw new WslManagerError('unsupported_arch');
    if (!supportedWindowsBuild(windowsBuildFromRelease(this.release))) throw new WslManagerError('unsupported_windows');

    const system = await this.systemInfo();
    if (system && system.virtualizationEnabled === false) throw new WslManagerError('virtualization_disabled');
    if (system && system.rebootRequired === true) throw new WslManagerError('reboot_required');

    let version;
    try { version = await this.runWsl(['--version'], { timeoutMs: 15_000 }); } catch (error) {
      const reason = String(error && error.message || '').toLowerCase();
      const fallback = reason.includes('not recognized') || reason.includes('not found') || reason.includes('找不到') ? 'wsl_missing' : 'wsl_update_required';
      throw managerError(classifyWslFailure(error, fallback), error);
    }
    try { await this.runWsl(['--status'], { timeoutMs: 15_000 }); } catch (error) { throw managerError(classifyWslFailure(error, 'wsl_update_required'), error); }

    let list;
    try { list = await this.runWsl(['--list', '--verbose'], { timeoutMs: 15_000 }); } catch (error) { throw managerError(classifyWslFailure(error, 'wsl_update_required'), error); }
    const distro = parseDistroList(list.stdout).find((entry) => entry.name.toLowerCase() === this.distroName.toLowerCase());
    if (distro && distro.version !== 2) throw new WslManagerError('wsl1_only', { distro });

    let appliance = null;
    let supervisor = null;
    if (distro) {
      try {
        const ownership = await this.verifyDistroOwnership();
        appliance = ownership.appliance;
      } catch (error) {
        throw managerError('distro_name_conflict', error);
      }
      let supervisorSocketHealthy = false;
      try {
        const result = await this.runControl('status', { timeoutMs: 20_000 });
        supervisorSocketHealthy = true;
        const status = parseControlStatus(result.stdout, 'Appliance status');
        if (!applianceIdentityMatches(status, this.expectedService, this.expectedRuntime, appliance.version)) {
          throw new Error(identityMismatchReason(status, appliance.version));
        }
        if (!Number.isInteger(status.supervisorPid) || status.supervisorPid <= 0) throw new Error('Supervisor PID is unavailable');
        if (!identityRuntimeSha256(status) || !identityRuntimeTransactionId(status)) {
          throw new Error('Supervisor did not report a valid .amdl-release.json runtime digest and transaction identity');
        }
        supervisor = { socketHealthy: true, status };
        await this.reconcileUpdateState(status);
      } catch (error) {
        const normalized = managerError('distro_corrupt', error);
        normalized.details = {
          ...normalized.details,
          installationIdentity: { service: appliance.service, runtime: appliance.runtime, version: appliance.version },
          supervisorSocketHealthy,
        };
        throw normalized;
      }
    }

    return {
      ok: true,
      windows: { release: this.release, architecture: this.arch, virtualizationEnabled: system && system.virtualizationEnabled },
      wsl: { installed: true, version: version.stdout.trim() },
      distro: distro ? { installed: true, version: distro.version, appliance, supervisor } : { installed: false },
    };
  }

  async windowsPathToWsl(windowsPath) {
    const normalized = validateWindowsDownloadPath(windowsPath);
    try {
      const result = await this.runWsl([
        '--distribution', this.distroName,
        '--exec', '/usr/bin/wslpath', '-a', '-u', normalized,
      ], { timeoutMs: 15_000 });
      const converted = result.stdout.trim();
      if (isSafeWslDrivePath(converted)) return converted;
      if (converted !== CONTROLLED_DOWNLOAD_MOUNT) throw new Error('wslpath returned an unsafe path');

      // WSL may resolve an already configured Windows directory through its
      // bind mount alias.  Reconstruct it only from the unbound parent, and
      // only when both persisted identities match this exact registered path.
      const state = await this.readState();
      const registeredWindows = validateWindowsDownloadPath(state.downloadDir || '');
      if (registeredWindows.toLowerCase() !== normalized.toLowerCase()) {
        throw new Error('controlled download mount does not match the registered Windows directory');
      }
      const parent = path.win32.dirname(normalized);
      const leaf = path.win32.basename(normalized);
      validateWindowsDownloadPath(parent);
      if (!safeWindowsLeaf(leaf)) throw new Error('registered Windows download leaf is unsafe');
      const parentResult = await this.runWsl([
        '--distribution', this.distroName,
        '--exec', '/usr/bin/wslpath', '-a', '-u', parent,
      ], { timeoutMs: 15_000 });
      const convertedParent = parentResult.stdout.trim();
      if (!isSafeWslDrivePath(convertedParent)) throw new Error('wslpath returned an unsafe parent path');
      const reconstructed = path.posix.join(convertedParent, leaf);
      if (!isSafeWslDrivePath(reconstructed)
        || !isSafeWslDrivePath(state.downloadWslPath)
        || state.downloadWslPath !== reconstructed) {
        throw new Error('reconstructed download path does not match the registered WSL directory');
      }
      return reconstructed;
    } catch (error) {
      throw managerError('path_unavailable', error);
    }
  }

  async configureDownloadPath(windowsPath = this.downloadDir, options = {}) {
    return this.withLifecycleLock(() => this.configureDownloadPathUnlocked(windowsPath, options));
  }

  async restoreDownloadPathTransactionUnlocked(transaction, { expectedVersion = '' } = {}) {
    if (!validDownloadPathTransaction(transaction, this.installDir) || !transaction?.previous) return false;
    const previous = transaction.previous;
    let restored;
    try {
      const result = await this.runTrustedWindowsHelper([
        'configure-download-locked', previous.windowsPath, previous.objectId || '', String(this.backendPort),
      ], {
        timeoutMs: 60_000,
        env: this.processEnvironment,
      });
      const document = parseJsonOutput(result.stdout, 'Locked Windows download rollback');
      restored = {
        ...normalizePathProbeResult(document),
        wslPath: document.wslPath,
      };
      if (!sameWindowsPath(restored.canonicalPath, previous.windowsPath)
          || restored.fileSystem !== previous.fileSystem
          || (previous.objectId && restored.objectId !== previous.objectId)
          || !isSafeWslDrivePath(restored.wslPath)) {
        throw new Error('Locked Windows download rollback returned a different directory identity');
      }
      this.downloadDir = restored.canonicalPath;
      await this.writeState({
        downloadDir: restored.canonicalPath,
        downloadWslPath: restored.wslPath,
        downloadPathTransaction: { ...transaction, phase: 'rollback_required' },
      });
      if (transaction.wasRunning) {
        await this.startUnlocked(undefined, {
          expectedVersion,
          skipDownloadConfiguration: true,
          skipDownloadPathRecovery: true,
        });
      }
      await this.writeState({ downloadPathTransaction: null });
      return true;
    } catch {
      // Keep the transaction durable. A later configure/start can retry the
      // exact old identity, but no caller may silently adopt the target path.
      try {
        await this.writeState({
          downloadPathTransaction: { ...transaction, phase: 'rollback_required' },
        });
      } catch { /* Preserve the original recovery failure without raw I/O text. */ }
      return false;
    }
  }

  async recoverPendingDownloadPathTransactionUnlocked(state = null) {
    const current = state || await this.readState();
    const transaction = current.downloadPathTransaction;
    if (!transaction) return false;
    if (!validDownloadPathTransaction(transaction, this.installDir)) {
      throw new WslManagerError('path_change_recovery_failed');
    }
    // A prepared marker can be left behind immediately before the stop call.
    // If the supervisor is still running, no directory mutation was admitted;
    // clear only after an identity/status proof and leave the live service in
    // place. A stopped service must take the rollback path below.
    if (transaction.phase === 'prepared') {
      try {
        const result = await this.runControl('status', { timeoutMs: 20_000 });
        const status = parseControlStatus(result.stdout, 'Appliance status');
        if (status.state === 'running' || status.state === 'starting') {
          await this.writeState({ downloadPathTransaction: null });
          return true;
        }
      } catch { /* A stopped/unknown service is recovered through the old path. */ }

      // A same-identity marker records no host-side directory change: the
      // helper may not have been invoked yet, or it may have replayed the
      // exact existing mapping.  Re-prove that identity and clear only this
      // idempotent marker; do not ask the helper to reconfigure a stopped
      // service merely to recover an interrupted no-op.
      if (sameDownloadPathTransactionIdentity(transaction)) {
        try {
          const identity = await this.canonicalDownloadPathOutsideRoots(transaction.target.windowsPath);
          if (sameWindowsPath(identity.canonicalPath, transaction.target.windowsPath)
              && identity.fileSystem === transaction.target.fileSystem
              && identity.objectId === transaction.target.objectId
              && isSafeWslDrivePath(transaction.previous.wslPath || '')) {
            this.downloadDir = identity.canonicalPath;
            await this.writeState({
              downloadDir: identity.canonicalPath,
              downloadWslPath: transaction.previous.wslPath,
              downloadPathTransaction: null,
            });
            return true;
          }
        } catch { /* Fall through to the durable rollback path below. */ }
      }
    }
    const expectedVersion = transaction.expectedVersion || current.installedVersion || '';
    if (await this.restoreDownloadPathTransactionUnlocked(transaction, { expectedVersion })) return true;
    throw new WslManagerError('path_change_recovery_failed');
  }

  async configureDownloadPathUnlocked(windowsPath = this.downloadDir, options = {}) {
    const lexical = validateDownloadPathOutsideRoots(windowsPath, this.protectedDownloadRoots);
    let ownershipState = await this.readState();
    if (ownershipState.downloadPathTransaction) {
      await this.recoverPendingDownloadPathTransactionUnlocked(ownershipState);
      ownershipState = await this.readState();
    }
    await this.verifyDistroOwnership({ expectedVersion: options.expectedVersion || ownershipState.installedVersion || '' });
    try { fs.mkdirSync(lexical, { recursive: true }); } catch (error) { throw managerError('path_unavailable', error); }
    const firstIdentity = await this.canonicalDownloadPathOutsideRoots(lexical);
    const normalized = firstIdentity.canonicalPath;

    let expectedVersion = options.expectedVersion || ownershipState.installedVersion || '';
    let serviceRunning = false;
    try {
      const result = await this.runControl('status', { timeoutMs: 20_000 });
      const status = parseControlStatus(result.stdout, 'Appliance status');
      serviceRunning = status.state === 'running' || status.state === 'starting';
      if (!expectedVersion) expectedVersion = status.version || '';
    } catch { /* Installation can configure as soon as the supervisor socket appears. */ }
    if (options.ensureBackendPort !== false) {
      await this.ensureBackendPortUnlocked({ expectedVersion, preserveIfRunning: serviceRunning });
    }

    // Capture the old mapping only after the first target identity proof. If
    // the user selected the already registered path, retaining this identity
    // avoids a second canonicalizer call and preserves the existing TOCTOU
    // ownership test ordering.
    let previousIdentity = null;
    const previousPath = ownershipState.downloadDir || this.downloadDir;
    if (typeof previousPath === 'string' && previousPath.trim()) {
      if (sameWindowsPath(previousPath, normalized)) {
        previousIdentity = {
          windowsPath: normalized,
          fileSystem: firstIdentity.fileSystem,
          ...(firstIdentity.objectId ? { objectId: firstIdentity.objectId } : {}),
          ...(isSafeWslDrivePath(ownershipState.downloadWslPath || '') ? { wslPath: ownershipState.downloadWslPath } : {}),
        };
      } else {
        previousIdentity = await this.canonicalDownloadPathOutsideRoots(previousPath);
        previousIdentity = {
          windowsPath: previousIdentity.canonicalPath,
          fileSystem: previousIdentity.fileSystem,
          ...(previousIdentity.objectId ? { objectId: previousIdentity.objectId } : {}),
          ...(isSafeWslDrivePath(ownershipState.downloadWslPath || '') ? { wslPath: ownershipState.downloadWslPath } : {}),
        };
      }
    }

    // Re-selecting the exact registered directory is an idempotent identity
    // check, not a directory transition.  In particular, do not leave a
    // durable `prepared` marker around a helper invocation that can be
    // interrupted while the service is already stopped.  The persisted WSL
    // mapping is retained and the backend start path will verify the mount
    // again inside the appliance.
    if (previousIdentity
        && sameWindowsPath(previousIdentity.windowsPath, normalized)
        && previousIdentity.fileSystem === firstIdentity.fileSystem
        && previousIdentity.objectId === (firstIdentity.objectId || '')
        && isSafeWslDrivePath(previousIdentity.wslPath || '')) {
      this.downloadDir = normalized;
      await this.writeState({
        downloadDir: normalized,
        downloadWslPath: previousIdentity.wslPath,
        downloadPathTransaction: null,
      });
      return {
        windowsPath: normalized,
        wslPath: previousIdentity.wslPath,
        serviceRestarted: false,
      };
    }
    const transaction = {
      schemaVersion: 1,
      kind: 'download_path_change',
      id: crypto.randomBytes(32).toString('hex'),
      phase: 'prepared',
      distroName: this.distroName,
      installDir: this.installDir,
      expectedVersion,
      wasRunning: serviceRunning,
      previous: previousIdentity,
      target: {
        windowsPath: normalized,
        fileSystem: firstIdentity.fileSystem,
        ...(firstIdentity.objectId ? { objectId: firstIdentity.objectId } : {}),
      },
    };
    let stopAccepted = false;
    try {
      await this.verifyDistroOwnership({ expectedVersion });
      if (serviceRunning) {
        await this.writeState({ downloadPathTransaction: transaction });
        let stopResult;
        try {
          stopResult = await this.runControl('stop', { timeoutMs: 60_000 });
          if (stopResult?.stdout) parseControlStatus(stopResult.stdout, 'Appliance stop');
        } catch (error) {
          if (error?.controlCode === 'active_jobs' || error?.code === 'active_jobs') {
            await this.writeState({ downloadPathTransaction: null });
            throw new WslManagerError('path_change_active_jobs');
          }
          throw error;
        }
        stopAccepted = true;
        await this.discardLocalProxy({ clearPersistedEndpoint: true });
        await this.verifyDistroOwnership({ expectedVersion });
        await this.writeState({ downloadPathTransaction: { ...transaction, phase: 'stopped' } });
      } else {
        // Keep a durable marker even for a stopped service so a crash between
        // the helper and the state commit can be recovered without guessing.
        await this.writeState({ downloadPathTransaction: transaction });
      }

      const result = await this.runTrustedWindowsHelper([
        'configure-download-locked', normalized, firstIdentity.objectId || '', String(this.backendPort),
      ], {
        timeoutMs: 60_000,
        env: this.processEnvironment,
      });
      const document = parseJsonOutput(result.stdout, 'Locked Windows download configuration');
      const lockedResult = {
        ...normalizePathProbeResult(document),
        wslPath: document.wslPath,
      };
      if (!sameWindowsPath(lockedResult.canonicalPath, normalized)
          || lockedResult.fileSystem !== firstIdentity.fileSystem
          || (lockedResult.objectId || '') !== (firstIdentity.objectId || '')
          || !isSafeWslDrivePath(lockedResult.wslPath)) {
        throw new WslManagerError('path_unavailable', { reason: 'Locked download configuration returned a different directory identity' });
      }
      const committed = {
        ...transaction,
        phase: 'target_configured',
        target: { ...transaction.target, wslPath: lockedResult.wslPath },
      };
      this.downloadDir = lockedResult.canonicalPath;
      await this.writeState({
        downloadDir: lockedResult.canonicalPath,
        downloadWslPath: lockedResult.wslPath,
        downloadPathTransaction: committed,
      });
      if (serviceRunning) {
        await this.startUnlocked(undefined, {
          expectedVersion,
          skipDownloadConfiguration: true,
          skipDownloadPathRecovery: true,
        });
      }
      await this.writeState({ downloadPathTransaction: null });
      return {
        windowsPath: lockedResult.canonicalPath,
        wslPath: lockedResult.wslPath,
        serviceRestarted: serviceRunning,
      };
    } catch (error) {
      if (error instanceof WslManagerError && error.code === 'path_change_active_jobs') throw error;
      // Ownership is a hard trust boundary. Once it changes after the stop
      // admission, do not issue a helper rollback against a potentially
      // foreign distribution; retain the durable marker and surface the
      // original conflict for repair/reconciliation.
      if (error instanceof WslManagerError && error.code === 'distro_name_conflict') {
        try {
          await this.writeState({ downloadPathTransaction: { ...transaction, phase: stopAccepted ? 'stopped' : 'prepared' } });
        } catch { /* Keep the conflict fail-closed even if state I/O is unavailable. */ }
        throw error;
      }
      // Once stop was accepted, or after the target helper may have mutated
      // the mapping, restore the exact previous identity before surfacing a
      // stable error. If no previous mapping exists, fail closed.
      if (serviceRunning || stopAccepted) {
        const rollback = await this.restoreDownloadPathTransactionUnlocked({
          ...transaction,
          phase: 'rollback_required',
        }, { expectedVersion });
        if (rollback) throw new WslManagerError('path_change_failed_recovered');
        throw new WslManagerError('path_change_recovery_failed');
      }
      if (error instanceof WslManagerError && error.code === 'path_unavailable') throw error;
      throw managerError('path_unavailable', error);
    }
  }

  async cacheArtifact(kind, version, artifact) {
    const destination = path.join(this.artifactDir, artifactCacheName(kind, version, artifact));
    try {
      return await this.downloadArtifact(artifact, destination);
    } catch (error) {
      const message = String(error && error.message || '');
      throw managerError(/sha-?256|checksum|size mismatch/i.test(message) ? 'checksum_mismatch' : 'download_failed', error);
    }
  }

  async prepareInstallTransaction(manifest, { orphanProof = null } = {}) {
    const current = await this.readState();
    let ownerId = await this.readOwnershipId();
    if (current.installTransaction) {
      if (!validOwnershipRecord(current, ownerId, this.installDir)
          || !validInstallTransaction(current.installTransaction, manifest, ownerId, this.installDir)) {
        throw new WslManagerError('distro_corrupt', { reason: 'Pending install transaction does not match the selected Appliance' });
      }
      return current.installTransaction;
    }
    if (ownerId) {
      if (!orphanProof || orphanProof.ownerId !== ownerId) {
        throw new WslManagerError('distro_name_conflict', { reason: 'An unbound host ownership record already exists' });
      }
      const currentRevision = Number.isSafeInteger(current.stateRevision) ? current.stateRevision : 0;
      if (currentRevision !== orphanProof.expectedRevision) {
        throw new WslManagerError('state_conflict', {
          expectedRevision: orphanProof.expectedRevision,
          actualRevision: currentRevision,
        });
      }
      await this.proveOrphanedHostState(current, ownerId);
      const confirmedOwnerId = await this.readOwnershipId();
      if (confirmedOwnerId !== orphanProof.ownerId) {
        throw new WslManagerError('distro_corrupt', {
          reason: 'Host ownership changed while orphan recovery was being prepared',
        });
      }
      const startedAt = new Date().toISOString();
      const transaction = {
        schemaVersion: 1,
        id: crypto.randomBytes(32).toString('hex'),
        phase: 'prepared',
        ownershipId: ownerId,
        distroName: this.distroName,
        installDir: this.installDir,
        version: manifest.version,
        manifestSha256: manifestSha256(manifest),
        rootfsSha256: manifest.rootfs.sha256,
        runtimeSha256: manifest.runtime.sha256,
        startedAt,
      };
      const updated = await this.writeState({
        ownership: current.ownership,
        installDir: this.installDir,
        installTransaction: transaction,
        manifest: null,
        installedVersion: null,
        installedRuntimeSha256: null,
        installedRuntimeTransactionId: null,
        updateTransaction: null,
        pendingManifest: null,
        backendPort: null,
        localPort: null,
        usesLoopbackProxy: false,
        downloadWslPath: null,
        appliancePreserved: false,
      }, { expectedRevision: orphanProof.expectedRevision });
      return updated.installTransaction;
    }
    ownerId = await this.createHostOwnershipRecord();
    const startedAt = new Date().toISOString();
    const transaction = {
      schemaVersion: 1,
      id: crypto.randomBytes(32).toString('hex'),
      phase: 'prepared',
      ownershipId: ownerId,
      distroName: this.distroName,
      installDir: this.installDir,
      version: manifest.version,
      manifestSha256: manifestSha256(manifest),
      rootfsSha256: manifest.rootfs.sha256,
      runtimeSha256: manifest.runtime.sha256,
      startedAt,
    };
    await this.writeState({
      ownership: {
        schemaVersion: 1,
        ownerId,
        distroName: this.distroName,
        installDir: this.installDir,
        createdAt: startedAt,
      },
      installDir: this.installDir,
      installTransaction: transaction,
    });
    return transaction;
  }

  async setInstallTransactionPhase(transaction, phase, detail = {}) {
    const state = await this.readState();
    if (state.installTransaction?.id !== transaction.id
        || state.installTransaction?.ownershipId !== transaction.ownershipId) {
      throw new WslManagerError('distro_corrupt', { reason: 'Install transaction ownership changed unexpectedly' });
    }
    const next = { ...state.installTransaction, ...detail, phase };
    await this.writeState({ installTransaction: next });
    return next;
  }

  async claimInstallDirectory(transaction) {
    if (PATH_OBJECT_ID_PATTERN.test(transaction.installDirObjectId || '')) {
      let identity;
      try {
        identity = await this.pathCanonicalizer(this.installDir);
      } catch (error) {
        throw managerError('orphaned_install_state', error);
      }
      if (!sameWindowsPath(identity.canonicalPath, this.installDir)
          || identity.objectId !== transaction.installDirObjectId) {
        throw new WslManagerError('orphaned_install_state', {
          reason: 'The Appliance install directory identity changed after this transaction claimed it',
          transactionId: transaction.id,
        });
      }
      return transaction;
    }

    try {
      await fsp.mkdir(this.installDir, { recursive: false, mode: 0o700 });
    } catch (error) {
      if (error?.code === 'EEXIST') {
        throw new WslManagerError('orphaned_install_state', {
          reason: 'The Appliance install directory appeared after the absence proof and will not be reused',
          transactionId: transaction.id,
        });
      }
      throw managerError('orphaned_install_state', error);
    }

    let identity;
    try {
      identity = await this.pathCanonicalizer(this.installDir);
    } catch (error) {
      throw managerError('orphaned_install_state', error);
    }
    if (!sameWindowsPath(identity.canonicalPath, this.installDir)
        || !PATH_OBJECT_ID_PATTERN.test(identity.objectId || '')) {
      throw new WslManagerError('orphaned_install_state', {
        reason: 'The newly claimed Appliance install directory has no stable Windows object identity',
        transactionId: transaction.id,
      });
    }
    return this.setInstallTransactionPhase(transaction, transaction.phase, {
      installDirObjectId: identity.objectId,
      installDirClaimedAt: new Date().toISOString(),
    });
  }

  async rollbackFreshInstall(transaction) {
    const state = await this.readState();
    if (state.installTransaction?.id !== transaction.id
        || state.installTransaction?.ownershipId !== transaction.ownershipId) {
      throw new WslManagerError('distro_name_conflict', { reason: 'Fresh install transaction no longer owns the rollback target' });
    }
    await this.unregisterVerifiedDistro({ expectedVersion: transaction.version, clearState: true });
  }

  async resumeImportedInstall(manifest, transaction) {
    try {
      await this.verifyDistroOwnership({ expectedVersion: manifest.version });
      await this.setInstallTransactionPhase(transaction, 'initializing');
      // `probe` only proves that the CLI exists.  `status` crosses the Unix
      // socket and therefore proves that systemd started the real supervisor.
      const supervisor = await this.waitForSupervisorReady({ expectedVersion: manifest.version, timeoutMs: SUPERVISOR_READY_TIMEOUT_MS });
      const runtimeSha256 = identityRuntimeSha256(supervisor);
      const runtimeTransactionId = identityRuntimeTransactionId(supervisor);
      if (!runtimeSha256 || !runtimeTransactionId) {
        throw new WslManagerError('import_failed', { reason: 'Imported runtime has no valid .amdl-release.json digest and transaction identity' });
      }
      if (runtimeSha256 !== manifest.runtime.sha256) {
        throw new WslManagerError('import_failed', { reason: 'Imported runtime digest does not match the Appliance manifest' });
      }
      await this.configureDownloadPath(this.downloadDir, { expectedVersion: manifest.version });
      await this.writeState({
        manifest,
        installedVersion: manifest.version,
        installedRuntimeSha256: runtimeSha256,
        installedRuntimeTransactionId: runtimeTransactionId,
        installDir: this.installDir,
        installTransaction: null,
      });
      return { alreadyInstalled: false, recovered: transaction.phase !== 'prepared', status: await this.status() };
    } catch (error) {
      let rollbackError = null;
      try { await this.rollbackFreshInstall(transaction); } catch (caught) { rollbackError = caught; }
      if (rollbackError) {
        await this.setInstallTransactionPhase(transaction, 'failed', {
          failedAt: new Date().toISOString(),
          recovery: 'retry_or_repair',
        }).catch(() => {});
        throw new WslManagerError('import_failed', {
          reason: redactSecrets(error?.message || String(error), []),
          transactionId: transaction.id,
          safeRollbackBlocked: true,
          recovery: 'retry_or_repair',
        });
      }
      throw new WslManagerError('import_failed', {
        reason: redactSecrets(error?.message || String(error), []),
        transactionId: transaction.id,
        safeRollbackCompleted: true,
      });
    }
  }

  async install(manifest, options = {}) {
    return this.withLifecycleLock(() => this.installUnlocked(manifest, options));
  }

  async installUnlocked(manifest, options = {}) {
    const normalized = validateApplianceManifest(manifest);
    const stateBeforeProbe = await this.readState();
    if (requiresFreshApplianceInstall(normalized.version)
        && stateBeforeProbe.installedVersion
        && stateBeforeProbe.installedVersion !== normalized.version) {
      throw new WslManagerError('runtime_reinstall_required');
    }
    if (requiresFreshApplianceInstall(normalized.version) && stateBeforeProbe.manifest) {
      let persistedManifest;
      try { persistedManifest = validateApplianceManifest(stateBeforeProbe.manifest); } catch {
        throw new WslManagerError('runtime_reinstall_required');
      }
      if (persistedManifest.version !== normalized.version) {
        throw new WslManagerError('runtime_reinstall_required');
      }
    }
    await this.ensureProxyStopConfirmed();
    const ownerIdBeforeProbe = await this.readOwnershipId();
    const pending = stateBeforeProbe.installTransaction;
    const pendingIsValid = Boolean(pending)
      && validOwnershipRecord(stateBeforeProbe, ownerIdBeforeProbe, this.installDir)
      && validInstallTransaction(pending, normalized, ownerIdBeforeProbe, this.installDir);
    if (pending && !pendingIsValid) {
      throw new WslManagerError('distro_corrupt', {
        reason: 'Pending install transaction does not match the selected Appliance or install directory',
      });
    }
    const probeResult = await this.probe();
    // The persisted desktop record cannot override an observed Appliance
    // release. A target v1.2.15+ install must leave every conflicting distro
    // untouched rather than reaching configuration, backend start, or update.
    assertFreshApplianceObservedVersion(stateBeforeProbe, probeResult, normalized.version);
    let capability = probeResult;
    if (!probeResult.ok) {
      if (!pendingIsValid || !['distro_corrupt', 'import_failed'].includes(probeResult.error?.code)) {
        throw new WslManagerError(probeResult.error?.code || 'import_failed', probeResult.error?.details || {});
      }
      // A persisted pre-import transaction is the only setup recovery path.
      // Static ownership and service identity are revalidated below before any
      // mutation or rollback is allowed.
      capability = { ok: true, distro: { installed: true, recovering: true } };
    }
    if (capability.distro.installed) {
      if (pendingIsValid) {
        return this.resumeImportedInstall(normalized, pending);
      }
      const state = await this.readState();
      const installedVersion = state.installedVersion || capability.distro.appliance.version;
      if (!installedVersion) {
        throw new WslManagerError('distro_corrupt', { reason: 'Owned appliance has no recoverable installed version' });
      }
      if (installedVersion !== normalized.version) {
        if (requiresFreshApplianceInstall(normalized.version)) {
          throw new WslManagerError('runtime_reinstall_required');
        }
        if (!normalized.runtime) throw new WslManagerError('update_failed', { reason: 'Existing appliance requires a runtime update artifact' });
        return { alreadyInstalled: true, updated: true, status: await this.update(normalized) };
      }
      const supervisor = capability.distro.supervisor?.status || capability.distro.appliance;
      const runtimeSha256 = identityRuntimeSha256(supervisor);
      const runtimeTransactionId = identityRuntimeTransactionId(supervisor);
      if (!runtimeSha256 || !runtimeTransactionId) {
        throw new WslManagerError('distro_corrupt', { reason: 'Installed runtime has no valid .amdl-release.json digest and transaction identity' });
      }
      if (runtimeSha256 !== normalized.runtime.sha256) {
        throw new WslManagerError('distro_corrupt', { reason: 'Installed same-version runtime digest does not match the selected Appliance manifest; run repair' });
      }
      await this.writeState({
        manifest: normalized,
        installedVersion,
        installedRuntimeSha256: runtimeSha256,
        installedRuntimeTransactionId: runtimeTransactionId,
      });
      return { alreadyInstalled: true, updated: false, status: await this.status() };
    }

    let orphanProof = null;
    if (ownerIdBeforeProbe && !pending) {
      orphanProof = await this.proveOrphanedHostState(stateBeforeProbe, ownerIdBeforeProbe);
    }
    const rootfs = await this.cacheArtifact('rootfs', normalized.version, normalized.rootfs);
    let transaction = await this.prepareInstallTransaction(normalized, { orphanProof });
    transaction = await this.claimInstallDirectory(transaction);
    try {
      // Re-read the complete cached rootfs immediately before handing its path
      // to wsl.exe. The Linux runtime updater additionally snapshots its bundle
      // into an anonymous ext4 file because it controls the extraction reader.
      await this.verifyArtifact(normalized.rootfs, rootfs);
      await this.runTrustedWindowsHelper([
        'import-rootfs-locked', this.installDir, transaction.installDirObjectId, rootfs, normalized.rootfs.sha256,
      ], {
        timeoutMs: DEFAULT_TIMEOUT_MS,
        env: this.processEnvironment,
      });
    } catch (error) {
      await this.setInstallTransactionPhase(transaction, 'failed', {
        failedAt: new Date().toISOString(),
        recovery: 'retry_or_repair',
      }).catch(() => {});
      const fallback = /sha-?256|checksum|artifact.*size mismatch/i.test(error?.message || '')
        ? 'checksum_mismatch'
        : 'import_failed';
      throw managerError(classifyWslFailure(error, fallback), error);
    }
    try {
      await this.writeDistroOwnership(transaction.ownershipId);
      await this.setInstallTransactionPhase(transaction, 'imported', { importedAt: new Date().toISOString() });
    } catch (error) {
      await this.setInstallTransactionPhase(transaction, 'failed', {
        failedAt: new Date().toISOString(),
        recovery: 'retry_or_repair',
      }).catch(() => {});
      throw new WslManagerError('import_failed', {
        reason: redactSecrets(error?.message || String(error), []),
        transactionId: transaction.id,
        safeRollbackBlocked: true,
        recovery: 'retry_or_repair',
      });
    }
    return this.resumeImportedInstall(normalized, { ...transaction, phase: 'imported' });
  }

  async deliverCredentials(credentials, { expectedVersion = '' } = {}) {
    await this.verifyDistroOwnership({ expectedVersion });
    const normalized = normalizeCredentialDelivery(credentials);
    const secrets = Object.values(normalized);
    const payload = Buffer.from(JSON.stringify({ schemaVersion: 1, credentials: normalized }), 'utf8');
    if (payload.length > 64 * 1024) {
      payload.fill(0);
      throw new WslManagerError('credential_delivery_failed', { reason: 'Credential document is too large' });
    }
    try {
      await this.runControl('credential-stdin', { input: payload, secrets, maxInputBytes: 64 * 1024 });
      this.runtimeCredentials = { ...normalized };
    } catch (error) {
      throw managerError('credential_delivery_failed', error, secrets);
    } finally {
      payload.fill(0);
    }
  }

  async start(credentials, options = {}) {
    return this.withLifecycleLock(() => this.startUnlocked(credentials, options));
  }

  async startUnlocked(credentials, options = {}) {
    const deferredCredentialProvider = options.credentialProvider;
    if (deferredCredentialProvider !== undefined && typeof deferredCredentialProvider !== 'function') {
      throw new WslManagerError('credential_delivery_failed');
    }
    if (credentials !== undefined && deferredCredentialProvider) {
      throw new WslManagerError('credential_delivery_failed');
    }
    let deliveryCredentials = credentials === undefined
      ? undefined
      : normalizeCredentialDelivery(credentials);
    let credentialDeliveryRequested = deliveryCredentials !== undefined || Boolean(deferredCredentialProvider);
    validateDownloadPathOutsideRoots(this.downloadDir, this.protectedDownloadRoots);
    await this.canonicalDownloadPathOutsideRoots(this.downloadDir);
    let state = await this.readState();
    if (state.downloadPathTransaction && !options.skipDownloadPathRecovery) {
      await this.recoverPendingDownloadPathTransactionUnlocked(state);
      state = await this.readState();
    }
    // A previous stop may have quiesced the endpoint but failed to confirm a
    // helper exit.  Do not start/reconfigure a new backend generation until
    // that exact proxy ownership is confirmed or the operation fails closed.
    await this.ensureProxyStopConfirmed();
    const expectedVersion = options.expectedVersion || state.installedVersion || '';
    let initialOwnership;
    try {
      initialOwnership = await this.verifyDistroOwnership({ expectedVersion });
      await this.ensureKeeper();
      await this.waitForSupervisorReady({ expectedVersion, timeoutMs: SUPERVISOR_READY_TIMEOUT_MS });
      const currentResult = await this.runControl('status', { timeoutMs: 20_000 });
      const current = parseControlStatus(currentResult.stdout, 'Appliance status');
      if (!applianceIdentityMatches(current, this.expectedService, this.expectedRuntime, expectedVersion)) {
        throw new Error(identityMismatchReason(current, expectedVersion));
      }
      const alreadyRunning = current.state === 'running' || current.state === 'starting';
      // Only ordinary startup opts in; explicit credential saves/repairs still
      // restart an idle backend to apply the newly supplied credentials.
      if (alreadyRunning && options.reuseRunning === true && credentials === undefined) {
        credentialDeliveryRequested = false;
      }
      await this.ensureBackendPortUnlocked({ expectedVersion, preserveIfRunning: alreadyRunning });
      if (!alreadyRunning && !options.skipDownloadConfiguration) {
        await this.configureDownloadPath(this.downloadDir, { ensureBackendPort: false, expectedVersion });
      }
      if (credentialDeliveryRequested && alreadyRunning) {
        // A credential FD is consumed once when the backend is spawned. Prove
        // the queue is idle before stopping; an accepted supervisor stop also
        // closes its broker, so the replacement generation is prepared only
        // after that stop boundary.
        await this.prepareLocalEndpoint({ expectedVersion });
        let active;
        try { active = await this.activeJobs(); } catch (error) {
          throw managerError('credential_delivery_failed', error);
        }
        if (active.length > 0) {
          throw new WslManagerError('active_jobs', {
            count: active.length,
            credentialDeliveryDeferred: true,
          });
        }
        try {
          await this.runControl('stop', { timeoutMs: 60_000 });
        } catch (error) {
          if (error?.controlCode === 'active_jobs') {
            throw new WslManagerError('active_jobs', {
              controlCode: error.controlCode,
              controlAction: error.controlAction || '',
              credentialDeliveryDeferred: true,
            });
          }
          throw error;
        }
        await this.discardLocalProxy({ clearPersistedEndpoint: true });
        const currentOwnership = await this.verifyDistroOwnership({ expectedVersion });
        if (currentOwnership.ownerId !== initialOwnership.ownerId
            || !sameWindowsPath(currentOwnership.registration.basePath, initialOwnership.registration.basePath)) {
          throw new WslManagerError('distro_name_conflict', {
            reason: 'Appliance ownership changed during credential restart',
          });
        }
      }

      // Reconnecting to an already-running backend without new credentials is
      // not a backend start. Replacing its broker would invalidate the proxy
      // URL already inherited by that live process.
      if (!alreadyRunning || credentialDeliveryRequested) {
        await this.prepareNetworkForStart({ expectedVersion });
        if (deferredCredentialProvider) {
          const providerOwnership = await this.verifyDistroOwnership({ expectedVersion });
          if (providerOwnership.ownerId !== initialOwnership.ownerId
              || !sameWindowsPath(providerOwnership.registration.basePath, initialOwnership.registration.basePath)) {
            throw new WslManagerError('distro_name_conflict', {
              reason: 'Appliance ownership changed before deferred credential delivery',
            });
          }
          try {
            const provided = await deferredCredentialProvider();
            deliveryCredentials = provided === undefined
              ? undefined
              : normalizeCredentialDelivery(provided);
          } catch (error) {
            if (error instanceof WslManagerError && error.code === 'credential_delivery_failed') throw error;
            throw new WslManagerError('credential_delivery_failed');
          }
        }
        if (deliveryCredentials !== undefined) {
          await this.deliverCredentials(deliveryCredentials, { expectedVersion });
        }
        const startResult = await this.runControl('start', { timeoutMs: 60_000 });
        const started = parseControlStatus(startResult.stdout, 'Appliance start');
        if (!applianceIdentityMatches(started, this.expectedService, this.expectedRuntime, expectedVersion)) {
          throw new Error(identityMismatchReason(started, expectedVersion));
        }
      } else {
        const generationId = readyNetworkGeneration(current, {
          expectedService: this.expectedService,
          expectedRuntime: this.expectedRuntime,
          expectedVersion,
        });
        if (!generationId) throw new WslManagerError('network_preflight_failed');
        // The live backend already inherited this exact broker generation.
        // Starting another Wrapper-based probe would share and remount its
        // Android chroot paths, so reconnect only validates the cached,
        // generation-bound status and never launches a cross-mode probe.
      }
    } catch (error) {
      if (error instanceof WslManagerError) throw error;
      throw managerError('distro_corrupt', error);
    }
    await this.prepareLocalEndpoint({ expectedVersion });
    await this.waitForReady({ timeoutMs: DEFAULT_TIMEOUT_MS, expectedVersion });
    const status = await this.status();
    if (!applianceIdentityMatches(status, this.expectedService, this.expectedRuntime, expectedVersion)) {
      throw new WslManagerError('distro_corrupt', { reason: identityMismatchReason(status, expectedVersion) });
    }
    return status;
  }

  async stop() {
    return this.withLifecycleLock(() => this.stopUnlocked());
  }

  async stopUnlocked() {
    const state = await this.readState();
    const expectedVersion = state.installedVersion || '';
    // A failed prior proxy stop is a hard lifecycle boundary; retry it before
    // asking the supervisor to stop or terminate a new backend generation.
    await this.ensureProxyStopConfirmed();
    const initialOwnership = await this.verifyDistroOwnership({ expectedVersion });
    try {
      await this.runControl('stop', { timeoutMs: 60_000 });
    } catch (error) {
      if (error?.controlCode === 'active_jobs') {
        throw new WslManagerError('active_jobs', {
          controlCode: error.controlCode,
          controlAction: error.controlAction || '',
        });
      }
      throw managerError('distro_corrupt', error);
    }
    // Stop admission before the ownership recheck/WSL terminate boundary.  A
    // proxy stop failure is fail-closed: do not terminate WSL and retain the
    // proxy object so a later retry can confirm helper ownership.
    await this.discardLocalProxy({ clearPersistedEndpoint: true });
    const currentOwnership = await this.verifyDistroOwnership({ expectedVersion });
    if (currentOwnership.ownerId !== initialOwnership.ownerId
        || !sameWindowsPath(currentOwnership.registration.basePath, initialOwnership.registration.basePath)) {
      throw new WslManagerError('distro_name_conflict', {
        reason: 'Appliance ownership changed after stop and before WSL termination',
      });
    }
    try {
      await this.runWsl(['--terminate', this.distroName], { timeoutMs: 60_000 });
    } catch (error) {
      throw managerError('distro_corrupt', error);
    }
    this.keeperProcess = null;
    return { stopped: true };
  }

  async status() {
    try {
      await this.restoreBackendPort();
      const result = await this.runControl('status', { timeoutMs: 20_000 });
      const endpoint = this.hasReusableProxyForBackend(this.backendPort) ? new URL(this.serviceBaseUrl) : null;
      return {
        ...parseControlStatus(result.stdout, 'Appliance status'),
        serviceBaseUrl: endpoint ? this.serviceBaseUrl : '',
        port: endpoint ? Number(endpoint.port || 80) : 0,
        backendPort: this.backendPort,
        downloadDir: this.downloadDir,
      };
    } catch (error) {
      throw managerError('distro_corrupt', error);
    }
  }

  async update(manifest, options = {}) {
    const intentState = await this.readState();
    const normalized = validateApplianceManifest(manifest);
    assertFreshApplianceSameVersion(intentState, normalized.version);
    return this.withLifecycleLock(() => this.updateUnlocked(normalized, options, intentState));
  }

  async recordUpgradeProgress(phase, fromVersion, toVersion) {
    if (!['checking', 'artifacts', 'network', 'submitting', 'reconciling', 'starting', 'complete', 'failed'].includes(phase)) return;
    const safeVersion = value => /^\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(value || '') ? value : 'unknown';
    const progress = { phase, fromVersion: safeVersion(fromVersion), toVersion: safeVersion(toVersion) };
    // This bounded record contains no arbitrary control output or error text.
    await this.writeState({ upgradeProgress: progress });
    try { this.onUpgradeProgress?.(progress); } catch { /* UI delivery is not transaction authority. */ }
  }

  async updateUnlocked(manifest, options = {}, intentState = {}) {
    const normalized = validateApplianceManifest(manifest);
    if (!normalized.runtime) throw new WslManagerError('update_failed', { reason: 'Manifest has no runtime artifact' });
    const earlyState = await this.readState();
    assertFreshApplianceSameVersion(earlyState, normalized.version);
    // Force-mode updates skip the active-job preflight below; they must still
    // refuse to touch artifacts or WSL while an older proxy stop is pending.
    await this.ensureProxyStopConfirmed();
    if (options.beforeSubmit !== undefined && typeof options.beforeSubmit !== 'function') {
      throw new WslManagerError('update_failed', { reason: 'Update submission verifier is invalid' });
    }
    if (options.transactionId !== undefined && !OWNER_ID_PATTERN.test(options.transactionId || '')) {
      throw new WslManagerError('update_failed', { reason: 'Requested update transaction identity is invalid' });
    }
    const serializedState = await this.readState();
    const initialOwnership = await this.verifyDistroOwnership({ expectedVersion: serializedState.installedVersion || '' });
    if (lifecycleTargetCompletedSinceIntent(intentState, serializedState, normalized)) {
      return this.status();
    }
    if (serializedState.updateTransaction) {
      throw new WslManagerError('update_failed', {
        reason: 'A persisted update transaction must be reconciled before another update can start',
        transactionId: serializedState.updateTransaction.id,
        recovery: 'retry_or_repair',
        updateStatePreserved: true,
      });
    }
    const previousState = await this.readState();
    let previousManifest;
    try { previousManifest = validateApplianceManifest(previousState.manifest); } catch {
      throw new WslManagerError('update_failed', { reason: 'Installed manifest is unavailable; repair cannot establish a safe rollback identity' });
    }
    // The historical 1.2.13 bootstrap gave immutable supervisor code a
    // verified Windows pathname that it reopened later. A per-user install
    // cannot bind that reopen to the verified bytes, so every cross-version
    // transition from 1.2.13 is a fresh-install boundary.
    if (previousManifest.version === '1.2.13' && normalized.version !== previousManifest.version) {
      throw new WslManagerError('runtime_reinstall_required');
    }
    let previousSupervisor = {};
    try {
      const statusResult = await this.runControl('status', { timeoutMs: 20_000 });
      previousSupervisor = parseControlStatus(statusResult.stdout, 'Appliance status');
    } catch (error) {
      throw managerError('update_failed', error);
    }
    const previousRuntimeSha256 = identityRuntimeSha256(previousSupervisor)
      || previousState.installedRuntimeSha256
      || previousManifest.runtime.sha256;
    const previousRuntimeTransactionId = identityRuntimeTransactionId(previousSupervisor)
      || previousState.installedRuntimeTransactionId;
    if (!SHA256_PATTERN.test(previousRuntimeSha256) || !OWNER_ID_PATTERN.test(previousRuntimeTransactionId)) {
      throw new WslManagerError('update_failed', { reason: 'Installed Appliance did not report a verifiable runtime digest and transaction identity' });
    }
    if (previousRuntimeSha256 !== previousManifest.runtime.sha256) {
      throw new WslManagerError('update_failed', { reason: 'Installed runtime digest does not match the persisted manifest' });
    }
    const previousRunning = previousSupervisor.state === 'running' || previousSupervisor.state === 'starting';
    await this.recordUpgradeProgress('checking', previousManifest.version, normalized.version);
    if (!options.force && previousRunning) {
      let active;
      try { active = await this.activeJobs(); } catch (error) { throw managerError('update_failed', error); }
      if (active.length > 0) throw new WslManagerError('active_jobs', { count: active.length });
    }
    // A stopped backend cannot execute a job.  Do not resurrect an older
    // runtime merely to enumerate its durable queue; the old supervisor owns
    // the final maintenance/admission check when it receives update-stdin.
    // Modern supervisors require a live, generation-bound network bridge even
    // for update admission. Prepare against the source identity, before any
    // transaction is persisted; legacy supervisors do not understand this API.
    const sourceVersion = previousManifest.version;
    const sourceVersionParts = /^(\d+)\.(\d+)\.(\d+)$/.exec(sourceVersion);
    const supportsNetworkPreparation = sourceVersionParts && (
      Number(sourceVersionParts[1]) > 1 ||
      (Number(sourceVersionParts[1]) === 1 && (Number(sourceVersionParts[2]) > 2 ||
        (Number(sourceVersionParts[2]) === 2 && Number(sourceVersionParts[3]) >= 14))));
    let caRepairAdmission = null;
    if (!previousRunning && supportsNetworkPreparation) {
      await this.recordUpgradeProgress('network', sourceVersion, normalized.version);
      const caRepairIdentity = options.repair === true && sourceVersion === normalized.version
        && previousSupervisor.state === 'stopped' ? {
          runtimeSha256: previousRuntimeSha256,
          runtimeTransactionId: previousRuntimeTransactionId,
          applianceBuildId: previousManifest.applianceBuildId,
          ...Object.fromEntries(['androidMainBuiltSha256', 'androidNetworkPreflightBuiltSha256',
            'androidCaBundlePath', 'androidCaBundleSha256', 'rendererSha256']
            .map(field => [field, previousManifest.inputs[field]])),
        } : undefined;
      const network = await this.prepareNetworkForStart({ expectedVersion: sourceVersion, caRepairIdentity });
      caRepairAdmission = network?.caRepairAdmission || null;
    }
    const transactionId = options.transactionId || crypto.randomBytes(32).toString('hex');
    if (!OWNER_ID_PATTERN.test(transactionId)) {
      throw new WslManagerError('update_failed', { reason: 'Update transaction identity must be a 64-character lowercase hexadecimal value' });
    }
    if (transactionId === previousRuntimeTransactionId) {
      throw new WslManagerError('update_failed', { reason: 'Update transaction identity must differ from the installed runtime identity' });
    }
    await this.recordUpgradeProgress('artifacts', sourceVersion, normalized.version);
    const bundle = await this.cacheArtifact('runtime', normalized.version, normalized.runtime);
    const bundleWslPath = await this.windowsPathToWsl(bundle);
    const updateTransaction = {
      schemaVersion: 2,
      id: transactionId,
      mode: options.repair ? 'repair' : 'update',
      fromVersion: previousState.installedVersion || previousSupervisor.version || '',
      toVersion: normalized.version,
      fromRuntimeSha256: previousRuntimeSha256,
      toRuntimeSha256: normalized.runtime.sha256,
      fromRuntimeTransactionId: previousRuntimeTransactionId,
      fromManifestSha256: manifestSha256(previousManifest),
      toManifestSha256: manifestSha256(normalized),
      startedAt: new Date().toISOString(),
    };
    const payload = Buffer.from(JSON.stringify({
      schemaVersion: 1,
      mode: options.repair ? 'repair' : 'update',
      version: normalized.version,
      bundlePath: bundleWslPath,
      sha256: normalized.runtime.sha256,
      transactionId: updateTransaction.id,
    }), 'utf8');
    let committed = false;
    try {
      const currentOwnership = await this.verifyDistroOwnership({
        expectedVersion: previousState.installedVersion || previousSupervisor.version || '',
      });
      if (currentOwnership.ownerId !== initialOwnership.ownerId
          || !sameWindowsPath(currentOwnership.registration.basePath, initialOwnership.registration.basePath)) {
        throw new WslManagerError('distro_name_conflict', {
          reason: 'Appliance ownership changed while the update was being prepared',
        });
      }
      // Repair uses this hook to re-prove the immutable host/distro owner
      // immediately before the host transaction is persisted and update-stdin
      // becomes reachable. A failed proof leaves no pending update state.
      if (options.beforeSubmit) await options.beforeSubmit();
      // Quiesce the old Windows bridge before handing the update transaction
      // to the supervisor.  update-stdin owns the backend stop/activation
      // transaction, but it cannot close Electron listeners or reap their
      // helpers.  Keeping the old bridge alive here permits late renderer
      // requests to target a stopped or replacement backend generation.
      await this.discardLocalProxy({ clearPersistedEndpoint: true });
      const postQuiesceOwnership = await this.verifyDistroOwnership({
        expectedVersion: previousState.installedVersion || previousSupervisor.version || '',
      });
      if (postQuiesceOwnership.ownerId !== initialOwnership.ownerId
          || !sameWindowsPath(postQuiesceOwnership.registration.basePath, initialOwnership.registration.basePath)) {
        throw new WslManagerError('distro_name_conflict', {
          reason: 'Appliance ownership changed after the proxy quiesce boundary',
        });
      }
      if (caRepairAdmission && !await this.confirmCABundleRepairAdmission(caRepairAdmission)) {
        throw new WslManagerError('network_preflight_failed');
      }
      await this.writeState({
        pendingManifest: normalized,
        updateTransaction,
      });
      // update-stdin is the sole transaction owner: the supervisor performs
      // stop, activation, a 180-second backend health check, commit, and any
      // required rollback before this call returns.
      try {
        await this.recordUpgradeProgress('submitting', sourceVersion, normalized.version);
        await this.runControl('update-stdin', { input: payload, timeoutMs: UPDATE_TIMEOUT_MS, maxInputBytes: 16 * 1024 });
        committed = true;
      } catch (controlError) {
        await this.recordUpgradeProgress('reconciling', sourceVersion, normalized.version);
        // A lost wsl.exe/JSON response is not evidence of rollback: the
        // supervisor may already have durably committed and restarted. Keep
        // the host transaction until a fresh control connection reports the
        // exact source or target digest+transaction identity.
        const reconciliation = await this.reconcileFailedUpdateControl(controlError, updateTransaction);
        if (reconciliation.outcome === 'rolled_back') {
          if (previousRunning) {
            try {
              await this.start(undefined, this.runtimeRestartStartOptions(updateTransaction.fromVersion));
            } catch { /* Preserve the supervisor's update failure. */ }
          }
          throw controlError;
        }
        if (reconciliation.outcome !== 'committed') {
          throw new WslManagerError('update_failed', {
            reason: 'Update response was lost and the persisted runtime identity remains unresolved',
            transactionId: updateTransaction.id,
            recovery: 'retry_or_repair',
            updateStatePreserved: true,
          });
        }
        committed = true;
      }
      await this.waitForSupervisorRestart(previousSupervisor.supervisorPid, { expectedVersion: normalized.version });
      await this.recordUpgradeProgress('starting', sourceVersion, normalized.version);
      const status = await this.start(undefined, this.runtimeRestartStartOptions(normalized.version));
      if (identityRuntimeSha256(status) !== normalized.runtime.sha256
          || identityRuntimeTransactionId(status) !== updateTransaction.id) {
        throw new WslManagerError('update_failed', { reason: 'Restarted Appliance runtime identity does not match the committed update transaction' });
      }
      await this.writeState({
        manifest: normalized,
        installedVersion: normalized.version,
        installedRuntimeSha256: normalized.runtime.sha256,
        installedRuntimeTransactionId: updateTransaction.id,
        updateTransaction: null,
        pendingManifest: null,
      });
      await this.recordUpgradeProgress('complete', sourceVersion, normalized.version);
      return status;
    } catch (error) {
      // Never clear an uncommitted-looking transaction merely because the
      // Windows transport failed.  reconcileFailedUpdateControl is the only
      // pre-commit cleanup path and clears state only after an exact source
      // identity match.  A known committed runtime likewise remains intact if
      // later endpoint or credential restoration fails.
      void committed;
      throw managerError('update_failed', error);
    } finally {
      payload.fill(0);
    }
  }

  async repair(manifest = null) {
    const intentState = await this.readState();
    return this.withLifecycleLock(() => this.repairUnlocked(manifest, intentState));
  }

  async repairUnlocked(manifest = null, intentState = {}) {
    const state = await this.readState();
    const selectedManifest = manifest ? validateApplianceManifest(manifest) : state.manifest;
    if (!selectedManifest) throw new WslManagerError('distro_corrupt', { reason: 'No installed manifest is available for repair' });
    assertFreshApplianceSameVersion(state, selectedManifest.version);
    // Repair may start a stopped backend and submit an update transaction;
    // never do either while a prior proxy stop is still unconfirmed.
    await this.ensureProxyStopConfirmed();
    let registered;
    try {
      registered = await this.registeredDistro();
    } catch (error) {
      throw managerError('distro_corrupt', error);
    }
    if (!registered) {
      return this.installUnlocked(selectedManifest, { recover: true, source: 'repair' });
    }
    if (lifecycleTargetCompletedSinceIntent(intentState, state, selectedManifest)) {
      return this.status();
    }
    if (state.updateTransaction) {
      throw new WslManagerError('update_failed', {
        reason: 'A persisted update transaction must be reconciled before repair can submit another transaction',
        transactionId: state.updateTransaction.id,
        recovery: 'retry_or_repair',
        updateStatePreserved: true,
      });
    }
    const expectedVersion = state.installedVersion || '';
    let initialOwnership;
    try {
      initialOwnership = await this.verifyDistroOwnership({ expectedVersion });
    } catch (error) {
      throw managerError('distro_name_conflict', error);
    }
    let controlStatus;
    try {
      const result = await this.runControl('status', { timeoutMs: 20_000 });
      controlStatus = parseControlStatus(result.stdout, 'Appliance status');
    } catch (error) {
      // Without a trustworthy supervisor state we cannot prove the queue is
      // idle, and update-stdin would use the same unavailable control path.
      throw managerError('update_failed', error);
    }
    const verifiedVersion = expectedVersion || controlStatus?.version || '';
    const backendRunning = controlStatus?.state === 'running' || controlStatus?.state === 'starting';
    if (backendRunning) {
      await this.prepareLocalEndpoint();
      let active;
      try { active = await this.activeJobs(); } catch (error) { throw managerError('update_failed', error); }
      if (active.length > 0) throw new WslManagerError('active_jobs', { count: active.length });
    }
    // A stopped backend cannot execute work. Do not start a legacy runtime
    // without credentials merely to inspect its queue; update() preserves the
    // final supervisor maintenance/admission boundary for a running backend.
    // update() performs a second queue check immediately before opening the
    // supervisor transaction. Re-prove ownership after that check and all
    // artifact preparation so a same-name distro swap can never receive the
    // update document.
    return this.update(selectedManifest, {
      repair: true,
      beforeSubmit: async () => {
        const currentOwnership = await this.verifyDistroOwnership({ expectedVersion: verifiedVersion });
        if (currentOwnership.ownerId !== initialOwnership.ownerId) {
          throw new WslManagerError('distro_name_conflict', {
            reason: 'Appliance ownership changed while repair was being prepared',
          });
        }
      },
    });
  }

  async uninstall({ preserveData = true } = {}) {
    return this.withLifecycleLock(() => this.uninstallUnlocked({ preserveData }));
  }

  async uninstallUnlocked({ preserveData = true } = {}) {
    if (preserveData) {
      // A preserve-data uninstall is allowed to stop only a verified owned
      // appliance.  An older/unowned same-name distro is left entirely alone.
      await this.verifyDistroOwnership();
      await this.stop();
      await this.writeState({ appliancePreserved: true });
      return { preserved: true, unregistered: false };
    }
    try {
      await this.unregisterVerifiedDistro();
      return { preserved: false, unregistered: true };
    } catch (error) {
      if (error instanceof WslManagerError) throw error;
      throw managerError('distro_corrupt', error);
    }
  }

  async activeJobs() {
    return this.withLifecycleLock(() => this.activeJobsUnlocked());
  }

  async activeJobsUnlocked() {
    await this.restoreBackendPortUnlocked();
    if (!this.hasReusableProxyForBackend(this.backendPort)) {
      const state = await this.readState();
      await this.prepareLocalEndpoint({ expectedVersion: state.installedVersion || '' });
    }
    const response = await this.request(`${this.serviceBaseUrl}/api/v1/jobs?active=true&limit=100`, {
      timeoutMs: 5_000,
      headers: this.desktopProxyRequestHeaders(),
    });
    if (response.statusCode !== 200) throw new Error(`Job API returned HTTP ${response.statusCode}`);
    const payload = response.payload || parseJsonOutput(response.body, 'Job API');
    const jobs = Array.isArray(payload) ? payload : payload.jobs || [];
    return jobs.filter((job) => ACTIVE_JOB_STATES.has(job.status));
  }

  prepareLocalEndpoint(options = {}) {
    return this.withLifecycleLock(() => this.prepareLocalEndpointLocked(options));
  }

  prepareLocalEndpointLocked(options = {}) {
    if (this.endpointPreparation) return this.endpointPreparation;
    let tracked;
    // Endpoint publication and teardown are part of the Appliance lifecycle.
    // The shared promise must be created only after the lifecycle lock is
    // held. Publishing a promise that is still queued for that lock would let
    // a lock holder wait on its own queued work. The lock is reentrant, so
    // start/update callers can create or share the real preparation directly.
    tracked = this.prepareLocalEndpointUnlocked(options).finally(() => {
      if (this.endpointPreparation === tracked) this.endpointPreparation = null;
    });
    this.endpointPreparation = tracked;
    return tracked;
  }

  async verifyLocalEndpointHealth(expectedVersion, endpointUrl = this.serviceBaseUrl) {
    return this.verifyLocalEndpointHealthAt(endpointUrl, expectedVersion);
  }

  async verifyLocalEndpointHealthAt(endpointUrl, expectedVersion) {
    const endpointMatch = typeof endpointUrl === 'string'
      ? /^http:\/\/127\.0\.0\.1:(\d+)$/.exec(endpointUrl)
      : null;
    if (!endpointMatch || !validPort(Number(endpointMatch[1]))) {
      throw new WslManagerError('port_in_use', { reason: 'A private local proxy endpoint is not valid' });
    }
    let response;
    try {
      response = await this.request(`${endpointUrl}/api/v1/health/ready`, {
        timeoutMs: 2_000,
        headers: this.desktopProxyRequestHeaders(),
      });
    } catch (error) {
      // Endpoint preparation must prove health before publication.  A
      // transport failure is not a reusable endpoint and is surfaced as a
      // bounded health failure rather than being silently swallowed.
      throw managerError('health_timeout', error);
    }
    if (response?.statusCode !== 200) {
      throw new WslManagerError('health_timeout', {
        timeoutMs: 2_000,
        reason: `HTTP ${Number.isInteger(response?.statusCode) ? response.statusCode : 'unknown'}`,
      });
    }
    if (!healthIdentityMatches(response, this.expectedService, this.expectedRuntime, expectedVersion)) {
      throw new WslManagerError('distro_corrupt', {
        reason: identityMismatchReason(response.payload, expectedVersion),
      });
    }
    return response.payload;
  }

  async prepareLocalEndpointUnlocked({ expectedVersion = '' } = {}) {
    // Endpoint preparation is also called directly by activeJobs/repair.  A
    // retained proxy after an unconfirmed stop must be retried before any
    // backend probe or listener allocation, otherwise two generations can
    // overlap even though the old helper is still owned by this manager.
    await this.ensureProxyStopConfirmed();
    await this.restoreBackendPortUnlocked();
    // Bind this whole candidate to the backend observed at preparation start.
    // Every public backend-port mutation now holds the same lifecycle lock,
    // and this final snapshot guard prevents accidental cross-generation
    // publication if a future code path is added outside that boundary.
    const backendPort = this.backendPort;
    const directUrl = `http://127.0.0.1:${backendPort}`;
    // Direct localhost forwarding is only a liveness hint.  Job and file APIs
    // require the supervisor-injected control token, so the desktop must never
    // expose or reuse the static WSL listener as its service endpoint.
    if (!this.hasProxyForBackend(backendPort)) {
      try {
        await this.request(`${directUrl}/api/v1/health/ready`, { timeoutMs: 2_000 });
      } catch { /* A refused direct connection is expected when localhost forwarding is unavailable. */ }
    }

    await this.resolveWslExecutable();
    if (this.hasReusableProxyForBackend(backendPort)) {
      try {
        await this.verifyLocalEndpointHealth(expectedVersion);
      } catch (error) {
        await this.discardLocalProxy({ clearPersistedEndpoint: true });
        throw error;
      }
      return { proxied: true, port: this.proxyLocalPort, backendPort };
    }

    // A process-local proxy can outlive persisted state, and a persisted
    // endpoint can outlive an Electron process.  Clear either stale shape
    // before allocating a new listener so localPort never describes the WSL
    // backend listener itself.
    await this.discardLocalProxy({ clearPersistedEndpoint: true });

    for (let attempt = 0; attempt < MAX_DISTINCT_PROXY_PORT_ATTEMPTS; attempt += 1) {
      let port;
      try {
        if (!this.proxy) this.proxy = this.proxyFactory();
        port = await this.proxy.start(0);
      } catch (error) {
        await this.discardLocalProxy({ clearPersistedEndpoint: true });
        if (error?.code === 'EADDRINUSE' && attempt + 1 < MAX_DISTINCT_PROXY_PORT_ATTEMPTS) continue;
        throw managerError('port_in_use', error);
      }
      if (!validPort(port)) {
        await this.discardLocalProxy({ clearPersistedEndpoint: true });
        throw new WslManagerError('port_in_use', { reason: 'Windows proxy returned an invalid local port' });
      }
      if (port === backendPort) {
        await this.discardLocalProxy({ clearPersistedEndpoint: true });
        continue;
      }
      // Keep the candidate private until the authenticated health response
      // proves the service/runtime identity.  Publishing the URL or writing a
      // reusable localPort before this check lets a 503/transport failure
      // poison the next manager generation.
      const candidateUrl = `http://127.0.0.1:${port}`;
      try {
        await this.verifyLocalEndpointHealth(expectedVersion, candidateUrl);
        if (this.backendPort !== backendPort) {
          throw new WslManagerError('port_in_use', {
            reason: 'Backend port changed while a private local endpoint was being prepared',
          });
        }
        await this.writeState({ backendPort, localPort: port, usesLoopbackProxy: true });
      } catch (error) {
        // If listener teardown cannot be confirmed, retain it as the manager's
        // non-reusable pending proxy and surface the cleanup error. Never lose
        // that ownership merely because health/state publication failed first.
        await this.discardLocalProxy({ clearPersistedEndpoint: false });
        throw error;
      }
      this.proxyLocalPort = port;
      this.proxyBackendPort = backendPort;
      this.serviceBaseUrl = candidateUrl;
      return { proxied: true, port, backendPort };
    }
    throw new WslManagerError('port_in_use', {
      reason: `Windows proxy could not obtain a port distinct from WSL backend port after ${MAX_DISTINCT_PROXY_PORT_ATTEMPTS} attempts`,
    });
  }

  async waitForReady({ timeoutMs = DEFAULT_TIMEOUT_MS, intervalMs = 1_000, expectedVersion = '' } = {}) {
    if (!this.hasReusableProxyForBackend(this.backendPort)) {
      await this.discardLocalProxy({ clearPersistedEndpoint: true });
      throw new WslManagerError('port_in_use', { reason: 'A distinct local Windows proxy endpoint is not available' });
    }
    const deadline = this.now() + timeoutMs;
    let lastReason = '';
    while (this.now() <= deadline) {
      try {
        const response = await this.request(`${this.serviceBaseUrl}/api/v1/health/ready`, {
          timeoutMs: 5_000,
          headers: this.desktopProxyRequestHeaders(),
        });
        if (response.statusCode === 200 && healthIdentityMatches(response, this.expectedService, this.expectedRuntime, expectedVersion)) return response.payload;
        lastReason = response.statusCode === 200 ? identityMismatchReason(response.payload, expectedVersion) : `HTTP ${response.statusCode}`;
      } catch (error) {
        lastReason = error.message;
      }
      await this.sleep(intervalMs);
    }
    const healthFailure = new WslManagerError('health_timeout', {
      timeoutMs,
      reason: redactSecrets(lastReason, []),
    });
    // A terminal readiness failure invalidates the endpoint just as a
    // candidate-health failure does. Keep the proxy ownership if helper
    // shutdown cannot be confirmed; discardLocalProxy then reports
    // port_in_use instead of allowing a later generation to overlap it.
    await this.discardLocalProxy({ clearPersistedEndpoint: true });
    throw healthFailure;
  }

  async diagnostics(secrets = []) {
    const tasks = [
      this.runWsl(['--list', '--verbose'], { timeoutMs: 15_000, secrets }),
      this.runControl('status', { timeoutMs: 15_000, secrets }),
      this.runControl('diagnostics', { timeoutMs: 30_000, secrets }),
      this.readState(),
      this.fileSha256(this.windowsProbeExe),
      this.fileSha256(this.interopBootstrapExe),
      this.interopRecoveryIdentityProvider
        ? Promise.resolve().then(() => this.interopRecoveryIdentityProvider())
        : Promise.resolve(null),
    ];
    const results = await Promise.allSettled(tasks);
    const sections = results.slice(0, 3).map((result) => {
      if (result.status === 'fulfilled') return `${result.value.stdout}\n${result.value.stderr}`;
      return result.reason && result.reason.message ? result.reason.message : String(result.reason);
    });
    try {
      if (results[1].status !== 'fulfilled' || results[3].status !== 'fulfilled'
          || results[4].status !== 'fulfilled') throw new Error('build identity input unavailable');
      const status = parseControlStatus(results[1].value.stdout, 'Appliance status');
      const identity = diagnosticBuildIdentity(
        results[3].value?.manifest,
        status,
        results[4].value,
        {
          bootstrapSha256: results[5]?.status === 'fulfilled' ? results[5].value : '',
          recovery: results[6]?.status === 'fulfilled' ? results[6].value : null,
        },
      );
      sections.unshift(JSON.stringify({ buildIdentity: identity }));
    } catch { /* Legacy or mismatched identities remain absent rather than partially trusted. */ }
    if (this.proxy) sections.push(this.proxy.diagnostics());
    return redactSecrets(tailLines(sections.join('\n'), 200), secrets);
  }
}

module.exports = {
  DESKTOP_PROXY_HEADER,
  DEFAULT_PORT,
  DEFAULT_TIMEOUT_MS,
  GO_UPDATE_CLIENT_TIMEOUT_MS,
  SUPERVISOR_UPDATE_READY_TIMEOUT_MS,
  SUPERVISOR_READY_TIMEOUT_MS,
  UPDATE_TIMEOUT_MS,
  WINDOWS_LOCK_COMMANDS,
  ERROR_MESSAGES,
  EXPECTED_RUNTIME,
  EXPECTED_SERVICE,
  CONTROLLED_DOWNLOAD_MOUNT,
  KEEPALIVE_PATH,
  WslManager,
  WslManagerError,
  acquireWindowsProcessLock,
  applianceIdentityMatches,
  classifyWslFailure,
  healthIdentityMatches,
  identityRuntimeSha256,
  identityRuntimeTransactionId,
  classifyUpdateRuntimeIdentity,
  manifestSha256,
  normalizeCredentials,
  parseControlStatus,
  controlErrorFromProcess,
  diagnosticBuildIdentity,
  parseDistroList,
  requestJson,
  selectAvailableLoopbackPort,
  sanitizeProcessEnvironment,
  tailLines,
  validatePersistedUpdateBindings,
  validateUpdateTransaction,
  validateDownloadPathOutsideRoots,
  validateWindowsDownloadPath,
};

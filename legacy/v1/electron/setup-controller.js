'use strict';

const { EventEmitter } = require('node:events');

const SETUP_STEPS = Object.freeze({
  DETECTING: 'detecting',
  WSL_ACTION: 'wsl_action',
  REBOOT: 'reboot',
  DOWNLOADING: 'downloading',
  IMPORTING: 'importing',
  DIRECTORY: 'directory',
  CREDENTIALS: 'credentials',
  STARTING: 'starting',
  COMPLETE: 'complete',
  FAILED: 'failed',
});

const SECRET_KEYS = new Set(['applePassword', 'mediaUserToken', 'authorizationToken']);
const NETWORK_PREFLIGHT_STAGES = new Set([
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
const NETWORK_PREFLIGHT_FALLBACK = Object.freeze({
  message: 'WSL Appliance 无法通过受支持的网络路径连接 Apple 认证服务。',
  action: '请保持 Windows 代理或网络工具运行，然后重试；无需重新输入密码或验证码。',
});
const RUNTIME_REINSTALL_FALLBACK = Object.freeze({
  message: '当前 WSL Appliance 不能原地升级到此版本。',
  action: '应用不会修改现有发行版或数据。请保留数据并在新的专用 Appliance 中完成安装。',
});
const DOWNLOAD_PATH_CHANGE_FALLBACKS = Object.freeze({
  path_change_active_jobs: Object.freeze({
    message: '当前有下载任务，不能切换目录。',
    action: '请等待任务完成或取消任务后重试；应用不会修改当前目录。',
  }),
  path_change_failed_recovered: Object.freeze({
    message: '下载目录切换失败，已恢复原目录和服务。',
    action: '请确认目标目录可写后重试；现有任务和服务已保留。',
  }),
  path_change_recovery_failed: Object.freeze({
    message: '下载目录切换和自动恢复均未完成。',
    action: '应用已停止以保护现有任务数据，请执行“修复服务”后重试。',
  }),
});

function publicNetworkPreflightDetails(error) {
  const stage = error?.details?.stage;
  if (typeof stage !== 'string' || !NETWORK_PREFLIGHT_STAGES.has(stage)) return null;
  const result = { stage };
  const details = error?.details;
  if (details?.caSource !== NETWORK_CA_SOURCE) return result;
  if (stage === 'ca_bundle_missing') return { ...result, caSource: NETWORK_CA_SOURCE };
  if (stage === 'ca_bundle_invalid' && details.curlCode === 77) {
    return { ...result, caSource: NETWORK_CA_SOURCE, curlCode: 77 };
  }
  if (stage === 'tls' && Number.isInteger(details.curlCode)
      && NETWORK_TLS_CURL_CODES.has(details.curlCode)) {
    const safe = { ...result, caSource: NETWORK_CA_SOURCE, curlCode: details.curlCode };
    if (Number.isInteger(details.sslVerifyResult)
        && details.sslVerifyResult >= 0 && details.sslVerifyResult <= 65535) {
      safe.sslVerifyResult = details.sslVerifyResult;
    }
    return safe;
  }
  return result;
}

function publicError(error) {
  const code = typeof error?.code === 'string' ? error.code : 'setup_failed';
  const networkPreflight = code === 'network_preflight_failed';
  const downloadPathChange = DOWNLOAD_PATH_CHANGE_FALLBACKS[code] || null;
  const result = {
    code,
    message: code === 'runtime_reinstall_required'
      ? RUNTIME_REINSTALL_FALLBACK.message
      : networkPreflight
      ? NETWORK_PREFLIGHT_FALLBACK.message
      : downloadPathChange
      ? downloadPathChange.message
      : typeof error?.message === 'string' ? error.message : '设置未能完成。',
    action: code === 'runtime_reinstall_required'
      ? RUNTIME_REINSTALL_FALLBACK.action
      : networkPreflight
      ? NETWORK_PREFLIGHT_FALLBACK.action
      : downloadPathChange
      ? downloadPathChange.action
      : typeof error?.action === 'string' ? error.action : '请查看诊断信息后重试。',
  };
  const details = networkPreflight ? publicNetworkPreflightDetails(error) : null;
  if (details) result.details = details;
  return result;
}

function nonSecretProgress(detail) {
  const safe = {};
  for (const [key, value] of Object.entries(detail || {})) {
    if (SECRET_KEYS.has(key) || /password|token|secret|credential/i.test(key)) continue;
    if (typeof value === 'string' && value.length <= 4096) safe[key] = value;
    else if (typeof value === 'boolean' || typeof value === 'number' || value === null) safe[key] = value;
  }
  return safe;
}

class SetupController extends EventEmitter {
  constructor({ manager, manifestProvider, stateStore, credentialStore, installWsl, updateWsl, sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)) }) {
    super();
    this.manager = manager;
    this.manifestProvider = manifestProvider;
    this.stateStore = stateStore;
    this.credentialStore = credentialStore;
    this.installWsl = installWsl;
    this.updateWsl = updateWsl;
    this.sleep = sleep;
  }

  state() {
    return this.stateStore.load();
  }

  transition(step, detail = {}) {
    const state = this.stateStore.save({ setupStep: step, ...nonSecretProgress(detail) });
    this.emit('progress', { step, ...nonSecretProgress(detail) });
    return state;
  }

  complete(status, { downloadDir = '', applianceVersion = '' } = {}) {
    return this.transition(SETUP_STEPS.COMPLETE, {
      rebootRequired: false,
      resumeSetup: false,
      downloadDir: downloadDir || status?.downloadDir || '',
      localPort: null,
      applianceVersion: applianceVersion || status?.version || '',
      lastErrorCode: '',
      pendingCompletion: false,
    });
  }

  failEndpoint(code = 'endpoint_publication_failed') {
    return this.transition(SETUP_STEPS.FAILED, {
      lastErrorCode: code,
      localPort: null,
      pendingCompletion: false,
    });
  }

  async probe() {
    this.transition(SETUP_STEPS.DETECTING);
    const result = await this.manager.probe();
    if (result.ok) return result;
    const error = result.error || result;
    if (error.code === 'reboot_required') {
      this.transition(SETUP_STEPS.REBOOT, { rebootRequired: true, resumeSetup: true, lastErrorCode: error.code });
    } else {
      this.transition(SETUP_STEPS.WSL_ACTION, { lastErrorCode: error.code || 'setup_failed' });
    }
    return result;
  }

  async applyWslAction(code) {
    let operation;
    let action;
    if (code === 'wsl_missing' && typeof this.installWsl === 'function') {
      operation = 'install';
      action = this.installWsl;
    } else if (code === 'wsl_update_required' && typeof this.updateWsl === 'function') {
      operation = 'update';
      action = this.updateWsl;
    } else {
      const error = new Error('当前问题不能由安装向导自动修复。');
      error.code = code || 'setup_failed';
      error.action = '请按页面说明启用虚拟化或重新启动 Windows。';
      throw error;
    }

    this.transition(SETUP_STEPS.WSL_ACTION, {
      lastErrorCode: code,
      operation,
      visibleTerminal: true,
    });
    try {
      await action();
    } catch (error) {
      this.transition(SETUP_STEPS.FAILED, { lastErrorCode: error?.code || code });
      throw error;
    }
    this.transition(SETUP_STEPS.DETECTING, { operation, actionCompleted: true });

    let result;
    for (let attempt = 0; attempt < 60; attempt += 1) {
      result = await this.manager.probe();
      if (result.ok) return result;
      if (result.error?.code === 'reboot_required') {
        this.transition(SETUP_STEPS.REBOOT, { rebootRequired: true, resumeSetup: true, lastErrorCode: result.error.code });
        const error = Object.assign(new Error(result.error.message), result.error);
        throw error;
      }
      if (attempt < 59) await this.sleep(1000);
    }
    const error = Object.assign(new Error(result?.error?.message || 'WSL2 仍不可用。'), result?.error || {});
    throw error;
  }

  async begin({ downloadDir, credentials } = {}) {
    try {
      const probe = await this.probe();
      const recoveringInstall = !probe.ok && probe.error?.code === 'distro_corrupt';
      if (!probe.ok && !recoveringInstall) {
        return { ok: false, error: publicError(probe.error || probe), state: this.state() };
      }

      const manifest = await this.manifestProvider();
      const installedVersion = probe?.distro?.appliance?.version
        || probe?.distro?.supervisor?.status?.version
        || '';
      const updatingRuntime = probe.distro?.installed === true
        && installedVersion !== ''
        && installedVersion !== manifest.version;
      if (recoveringInstall || !probe.distro?.installed || updatingRuntime) {
        this.transition(SETUP_STEPS.DOWNLOADING, {
          applianceVersion: manifest.version,
          recoveringInstall,
          updatingRuntime,
        });
        this.transition(SETUP_STEPS.IMPORTING, {
          applianceVersion: manifest.version,
          recoveringInstall,
          updatingRuntime,
        });
        await this.manager.install(manifest, { recover: recoveringInstall });
      }

      this.transition(SETUP_STEPS.DIRECTORY);
      if (downloadDir) await this.manager.configureDownloadPath(downloadDir);

      this.transition(SETUP_STEPS.CREDENTIALS);
      if (credentials && Object.keys(credentials).length > 0) this.credentialStore.save(credentials);

      this.transition(SETUP_STEPS.STARTING);
      const status = await this.manager.start(undefined, {
        expectedVersion: manifest.version,
        credentialProvider: () => this.credentialStore.load(),
      });
      if (!status || status.version !== manifest.version) {
        const error = new Error('WSL Appliance runtime version does not match the selected desktop manifest.');
        error.code = 'runtime_version_mismatch';
        throw error;
      }
      const state = this.transition(SETUP_STEPS.STARTING, {
        rebootRequired: false,
        downloadDir: downloadDir || status.downloadDir || '',
        localPort: null,
        applianceVersion: status.version || manifest.version,
        lastErrorCode: '',
        pendingCompletion: true,
      });
      return { ok: true, status, state, pendingCompletion: true };
    } catch (error) {
      const exposed = publicError(error);
      this.transition(SETUP_STEPS.FAILED, { lastErrorCode: exposed.code });
      return { ok: false, error: exposed, state: this.state() };
    }
  }
}

module.exports = {
  NETWORK_PREFLIGHT_STAGES,
  SetupController,
  SETUP_STEPS,
  publicError,
  nonSecretProgress,
  publicNetworkPreflightDetails,
};

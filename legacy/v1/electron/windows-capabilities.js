'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFile } = require('node:child_process');
const { validateWindowsHelperBinding, verifyWindowsHelperBinding } = require('./wslinterop-recovery');
const { WINDOWS_SUPPORT_MESSAGE, WINDOWS_SUPPORT_ACTION, supportedWindowsBuild, windowsBuildFromRelease } = require('./windows-support');

const ERROR_TEXT = Object.freeze({
  unsupported_windows: [WINDOWS_SUPPORT_MESSAGE, WINDOWS_SUPPORT_ACTION],
  unsupported_arch: ['仅支持 x64 处理器。', '请使用 Windows 10/11 x64 设备；32 位和 ARM64 尚未支持。'],
  virtualization_disabled: ['固件虚拟化未启用。', '请在 BIOS/UEFI 中启用 Intel VT-x 或 AMD-V，然后重新启动 Windows。'],
  reboot_required: ['Windows 需要重新启动后才能继续。', '保存其他工作并点击“重新启动 Windows”；登录后向导会继续。'],
  helper_identity: ['Windows 功能助手身份验证失败。', '请重新安装 Apple Music Downloader 后重试；不要手工运行助手程序。'],
});

const ELEVATED_ACTION_TIMEOUT_MS = 30 * 60 * 1000;

function capabilityError(code, details = {}) {
  const [message, action] = ERROR_TEXT[code] || ['无法检测 Windows 功能。', '请重新启动应用并查看诊断信息。'];
  return { code, message, action, details };
}

function runFile(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    execFile(command, args, { shell: false, windowsHide: true, encoding: 'utf8', timeout: options.timeoutMs || 30000 }, (error, stdout, stderr) => {
      if (error) {
        error.stdout = stdout;
        error.stderr = stderr;
        reject(error);
        return;
      }
      resolve({ stdout, stderr });
    });
  });
}

class WindowsCapabilities {
  constructor({
    helperPath,
    helperBindingProvider,
    runner = runFile,
    platform = process.platform,
    arch = process.arch,
    release = os.release(),
  } = {}) {
    if (helperBindingProvider !== undefined && typeof helperBindingProvider !== 'function') {
      throw new TypeError('Windows helper binding provider must be a function');
    }
    this.helperPath = helperPath;
    this.helperBindingProvider = helperBindingProvider || null;
    this.runner = runner;
    this.platform = platform;
    this.arch = arch;
    this.release = release;
  }

  async resolveHelperBinding() {
    if (!this.helperBindingProvider) throw new Error('Windows helper binding is unavailable');
    return validateWindowsHelperBinding(await this.helperBindingProvider());
  }

  async verifyHelper(binding) {
    const expected = binding || await this.resolveHelperBinding();
    verifyWindowsHelperBinding(this.helperPath, expected);
    return expected;
  }

  async runTrustedHelper(args, options = {}) {
    const binding = await this.verifyHelper();
    const result = await this.runner(this.helperPath, args, options);
    await this.verifyHelper(binding);
    return result;
  }

  async probe() {
    if (this.platform !== 'win32') return { ok: false, error: capabilityError('unsupported_windows', { platform: this.platform }) };
    if (this.arch !== 'x64') return { ok: false, error: capabilityError('unsupported_arch', { arch: this.arch }) };
    const fallbackBuild = windowsBuildFromRelease(this.release);
    if (!supportedWindowsBuild(fallbackBuild)) return { ok: false, error: capabilityError('unsupported_windows', { build: fallbackBuild }) };
    if (!this.helperPath || !fs.existsSync(this.helperPath)) {
      return { ok: true, build: fallbackBuild, architecture: this.arch, virtualizationEnabled: null, rebootRequired: false };
    }
    let stdout;
    try {
      ({ stdout } = await this.runTrustedHelper(['probe']));
    } catch {
      return { ok: false, error: capabilityError('helper_identity') };
    }
    const result = JSON.parse(stdout);
    if (!supportedWindowsBuild(result?.build)) {
      return { ok: false, error: capabilityError('unsupported_windows', result) };
    }
    if (result.architecture !== 'amd64') {
      return { ok: false, error: capabilityError('unsupported_arch', result) };
    }
    if (!result.virtualizationEnabled) return { ok: false, error: capabilityError('virtualization_disabled', result) };
    if (result.rebootRequired) return { ok: false, error: capabilityError('reboot_required', result) };
    return { ok: true, ...result };
  }

  async installWsl() {
    return this.fixedAction('install-wsl');
  }

  async updateWsl() {
    return this.fixedAction('update-wsl');
  }

  async restartWindows() {
    return this.fixedAction('restart-windows');
  }

  async fixedAction(action) {
    if (!['install-wsl', 'update-wsl', 'restart-windows'].includes(action)) throw new Error('unsupported Windows helper action');
    if (!this.helperPath || !fs.existsSync(this.helperPath)) {
      if (action === 'restart-windows') throw new Error('Windows capability helper is unavailable');
      const installing = action === 'install-wsl';
      const error = new Error('安装程序缺少 Windows 功能助手。');
      error.code = installing ? 'wsl_missing' : 'wsl_update_required';
      error.action = '请重新安装 Apple Music Downloader 后重试；无需手工运行命令。';
      throw error;
    }
    try {
      return await this.runTrustedHelper([action], { timeoutMs: ELEVATED_ACTION_TIMEOUT_MS });
    } catch (cause) {
      if (action === 'restart-windows') throw cause;
      const installing = action === 'install-wsl';
      const diagnostic = `${cause?.stderr || ''}\n${cause?.message || ''}`;
      const cancelled = /elevation_cancelled|operation (?:was )?cancelled|error[_ ]cancelled|\b1223\b/i.test(diagnostic);
      const error = new Error(cancelled
        ? '管理员授权已取消，或管理员安装窗口已关闭。'
        : `${installing ? 'WSL2 安装' : 'WSL2 更新'}未能完成。`);
      error.code = installing ? 'wsl_missing' : 'wsl_update_required';
      error.action = `请点击“${installing ? '安装 WSL2' : '更新 WSL2'}”重试，并保持管理员终端窗口打开直到命令结束。`;
      throw error;
    }
  }
}

function defaultWindowsHelperPath({ resourcesPath = process.resourcesPath, appPath = path.resolve(__dirname, '..') } = {}) {
  const packaged = resourcesPath && path.join(resourcesPath, 'bin', 'amdl-windows-probe.exe');
  if (packaged && fs.existsSync(packaged)) return packaged;
  return path.join(appPath, 'electron', 'bin', 'amdl-windows-probe.exe');
}

module.exports = { WindowsCapabilities, capabilityError, defaultWindowsHelperPath, runFile, ELEVATED_ACTION_TIMEOUT_MS };

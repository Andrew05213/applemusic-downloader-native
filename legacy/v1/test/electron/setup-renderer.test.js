'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const setupScript = fs.readFileSync(path.resolve(__dirname, '../../src/main/setup.js'), 'utf8');

class FakeElement {
  constructor() {
    this.textContent = '';
    this.value = '';
    this.hidden = false;
    this.disabled = false;
    this.dataset = {};
    this.attributes = new Map();
    this.listeners = new Map();
    this.classList = { toggle() {} };
  }

  addEventListener(name, listener) { this.listeners.set(name, listener); }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name); }

  async dispatch(name) {
    const listener = this.listeners.get(name);
    if (listener) await listener({ preventDefault() {} });
  }
}

async function settle() {
  for (let index = 0; index < 3; index += 1) await new Promise((resolve) => setImmediate(resolve));
}

async function loadSetupRenderer(
  initialState,
  applyResult = { ok: true },
  diagnostic = '暂无诊断输出。',
  runtimeRepairResult = { ok: true },
) {
  const ids = [
    'setup-form', 'download-dir', 'begin-button', 'repair-button', 'restart-button',
    'diagnostics-button', 'service-repair-button', 'diagnostics-panel', 'diagnostics-output',
    'status-title', 'status-message', 'status-action', 'choose-directory',
    'apple-email', 'apple-password', 'media-token', 'authorization-token', 'storefront',
  ];
  const elements = Object.fromEntries(ids.map((id) => [id, new FakeElement()]));
  elements['diagnostics-panel'].hidden = true;
  elements['service-repair-button'].hidden = true;
  const steps = ['detecting', 'downloading', 'directory', 'credentials', 'starting']
    .map((step) => Object.assign(new FakeElement(), { dataset: { step } }));
  let progressListener;
  let runtimeRepairCalls = 0;
  const context = {
    document: {
      getElementById: (id) => elements[id],
      querySelectorAll: () => steps,
    },
    window: {
      applianceSetup: {
        chooseDownloadDirectory: async () => null,
        applyWslAction: async () => applyResult,
        restartWindows: async () => ({}),
        getDiagnostics: async () => diagnostic,
        repairRuntime: async () => {
          runtimeRepairCalls += 1;
          if (runtimeRepairResult instanceof Error) throw runtimeRepairResult;
          return runtimeRepairResult;
        },
        begin: async () => ({ ok: false, error: { code: 'test' } }),
        onProgress: (listener) => { progressListener = listener; },
        getState: async () => initialState,
      },
    },
    console,
  };
  vm.runInNewContext(setupScript, context, { filename: 'setup.js' });
  await settle();
  return {
    elements,
    progress: (value) => progressListener(value),
    runtimeRepairCalls: () => runtimeRepairCalls,
  };
}

test('setup explains the WSL feature change before showing a retryable install button', async () => {
  const page = await loadSetupRenderer({
    error: {
      code: 'wsl_missing',
      message: '尚未安装 WSL2。',
      action: '点击安装后允许管理员授权。',
    },
  });

  assert.equal(page.elements['repair-button'].hidden, false);
  assert.equal(page.elements['repair-button'].textContent, '安装 WSL2');
  assert.match(page.elements['status-message'].textContent, /虚拟机平台.*可能需要重启/);
  assert.match(page.elements['status-action'].textContent, /管理员授权/);
});

test('setup shows visible-terminal progress and an explicit success result', async () => {
  const page = await loadSetupRenderer({ setupStep: 'detecting' });

  page.progress({ step: 'wsl_action', operation: 'install', visibleTerminal: true });
  assert.match(page.elements['status-title'].textContent, /正在启用 Windows 功能/);
  assert.match(page.elements['status-message'].textContent, /管理员终端/);
  assert.match(page.elements['status-action'].textContent, /请勿关闭/);

  page.elements['repair-button'].dataset.code = 'wsl_missing';
  await page.elements['repair-button'].dispatch('click');
  assert.match(page.elements['status-title'].textContent, /WSL2 安装命令已完成/);
  assert.match(page.elements['status-message'].textContent, /继续安装并启动/);
});

test('setup exposes sanitized appliance diagnostics inline and supports refresh', async () => {
  const page = await loadSetupRenderer(
    { setupStep: 'detecting' },
    { ok: true },
    'status: stopped\n[wrapper] credential=[REDACTED]',
  );

  assert.equal(page.elements['diagnostics-panel'].hidden, true);
  await page.elements['diagnostics-button'].dispatch('click');

  assert.equal(page.elements['diagnostics-panel'].hidden, false);
  assert.match(page.elements['diagnostics-output'].textContent, /status: stopped/);
  assert.match(page.elements['diagnostics-output'].textContent, /\[REDACTED\]/);
  assert.equal(page.elements['diagnostics-button'].textContent, '刷新诊断日志');
  assert.equal(page.elements['diagnostics-button'].getAttribute('aria-expanded'), 'true');
});

test('setup exposes a direct service repair action when the owned distro is corrupt', async () => {
  const page = await loadSetupRenderer({
    error: {
      code: 'distro_corrupt',
      message: '专用 WSL 发行版不完整或已损坏。',
      action: '请在桌面应用中执行“修复服务”。',
    },
  });

  assert.equal(page.elements['service-repair-button'].hidden, false);
  assert.equal(page.elements['service-repair-button'].textContent, '修复服务');
  await page.elements['service-repair-button'].dispatch('click');
  assert.equal(page.runtimeRepairCalls(), 1);
});

test('setup never offers service repair for a same-name distro that is not owned by this app', async () => {
  const page = await loadSetupRenderer({
    error: {
      code: 'distro_name_conflict',
      message: '已存在同名但不属于本应用的 WSL 发行版。',
      action: '请联系支持人员处理。',
    },
  });

  assert.equal(page.elements['service-repair-button'].hidden, true);
});

test('setup does not offer a dead-end service repair for WSL1 or orphaned install state', async () => {
  for (const code of ['wsl1_only', 'orphaned_install_state', 'update_failed']) {
    const page = await loadSetupRenderer({
      error: { code, message: '不可直接修复。', action: '请按诊断说明处理。' },
    });
    assert.equal(page.elements['service-repair-button'].hidden, true, code);
  }
});

test('setup renders download path transition outcomes without falling back to a C-drive restriction', async () => {
  const cases = [
    ['path_change_active_jobs', /当前有下载任务，不能切换目录/, false],
    ['path_change_failed_recovered', /已恢复原目录和服务/, false],
    ['path_change_recovery_failed', /自动恢复均未完成/, true],
  ];
  for (const [code, expected, repairVisible] of cases) {
    const page = await loadSetupRenderer({
      error: { code, message: '所选下载目录不可用。', action: 'raw helper path error' },
    });
    assert.match(page.elements['status-message'].textContent, expected);
    assert.doesNotMatch(page.elements['status-action'].textContent, /C:|E:|helper path/i);
    assert.equal(page.elements['service-repair-button'].hidden, !repairVisible, code);
  }
});

test('setup does not render raw IPC failures from the service repair action', async () => {
  const secret = 'credential-secret-sentinel';
  const page = await loadSetupRenderer(
    {
      error: {
        code: 'distro_corrupt',
        message: '专用 WSL 发行版不完整或已损坏。',
        action: '请执行修复。',
      },
    },
    { ok: true },
    '暂无诊断输出。',
    new Error(secret),
  );

  await page.elements['service-repair-button'].dispatch('click');
  assert.equal(page.runtimeRepairCalls(), 1);
  assert.match(page.elements['status-title'].textContent, /修复未能完成/);
  assert.doesNotMatch(page.elements['status-title'].textContent, new RegExp(secret));
  assert.doesNotMatch(page.elements['status-action'].textContent, new RegExp(secret));
});

test('setup maps only trusted network-preflight stages to fixed Chinese remediation categories', async () => {
  const cases = [
    ['helper_identity', /网络桥接未能启动/, /重新启动 Apple Music Downloader/],
    ['helper_launch', /网络桥接未能启动/, /重新启动 Apple Music Downloader/],
    ['helper_protocol', /网络桥接未能启动/, /重新启动 Apple Music Downloader/],
    ['proxy_resolution', /代理或网络连接失败/, /检查 Windows 网络和代理/],
    ['dns', /代理或网络连接失败/, /检查 Windows 网络和代理/],
    ['tcp_connect', /代理或网络连接失败/, /检查 Windows 网络和代理/],
    ['ca_bundle_missing', /证书库不可用/, /修复或重新安装 WSL Appliance/],
    ['ca_bundle_invalid', /证书库不可用/, /修复或重新安装 WSL Appliance/],
    ['native_probe_invalid', /Appliance 网络预检组件不可用/, /修复或重新安装 WSL Appliance/],
    ['native_startup', /Appliance 网络预检组件不可用/, /修复或重新安装 WSL Appliance/],
    ['tls', /安全连接失败/, /代理证书设置/],
    ['url_bag_fetch', /认证服务地址不可用/, /网络或代理/],
    ['url_bag_parse', /认证服务地址不可用/, /网络或代理/],
    ['url_bag_key_lookup', /认证服务地址不可用/, /网络或代理/],
  ];
  for (const [stage, title, action] of cases) {
    const page = await loadSetupRenderer({
      error: {
        code: 'network_preflight_failed',
        message: 'raw upstream message',
        action: 'raw upstream action',
        details: { stage },
      },
    });
    assert.match(page.elements['status-title'].textContent, title, stage);
    assert.match(page.elements['status-action'].textContent, action, stage);
    assert.match(page.elements['status-action'].textContent, /无需重新输入密码或验证码/);
  }
});

test('setup fails closed for an untrusted network-preflight stage and never renders raw details', async () => {
  const sentinel = 'unsafe-stage-sentinel';
  const page = await loadSetupRenderer({
    error: {
      code: 'network_preflight_failed',
      message: sentinel,
      action: sentinel,
      details: { stage: `dns-${sentinel}`, path: `C:\\${sentinel}` },
    },
  });

  assert.match(page.elements['status-title'].textContent, /网络预检未通过/);
  assert.match(page.elements['status-action'].textContent, /脱敏诊断中的失败阶段/);
  assert.doesNotMatch(page.elements['status-title'].textContent, new RegExp(sentinel));
  assert.doesNotMatch(page.elements['status-message'].textContent, new RegExp(sentinel));
  assert.doesNotMatch(page.elements['status-action'].textContent, new RegExp(sentinel));
});

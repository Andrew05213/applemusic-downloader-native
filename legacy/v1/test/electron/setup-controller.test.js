'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const {
  NETWORK_PREFLIGHT_STAGES,
  SetupController,
  publicError,
} = require('../../electron/setup-controller');

function memoryStateStore() {
  let state = { version: 1 };
  return {
    load: () => ({ ...state }),
    save: (next) => { state = { ...state, ...next }; return { ...state }; },
  };
}

test('setup imports a missing distro, configures a Unicode path and starts with encrypted-store credentials', async () => {
  const calls = [];
  const secret = 'SENTINEL-setup-password';
  let stored = {};
  const manager = {
    probe: async () => ({ ok: true, distro: { installed: false } }),
    install: async (manifest) => calls.push(['install', manifest.version]),
    configureDownloadPath: async (value) => calls.push(['path', value]),
    start: async (credentials, options) => {
      assert.equal(credentials, undefined);
      const provided = await options.credentialProvider();
      calls.push(['start', provided.applePassword]);
      return { port: 8080, version: '1.2.0' };
    },
  };
  const controller = new SetupController({
    manager,
    manifestProvider: async () => ({ version: '1.2.0' }),
    stateStore: memoryStateStore(),
    credentialStore: {
      save: (value) => { stored = { ...value }; },
      load: () => ({ ...stored }),
    },
  });

  const result = await controller.begin({
    downloadDir: 'C:\\Users\\张三\\音乐\\Apple Music Downloader',
    credentials: { appleEmail: 'person@example.test', applePassword: secret },
  });

  assert.equal(result.ok, true);
  assert.deepEqual(calls.map((entry) => entry[0]), ['install', 'path', 'start']);
  assert.equal(JSON.stringify(result).includes(secret), false);
  assert.equal(JSON.stringify(controller.state()).includes(secret), false);
});

test('setup routes an installed version mismatch through update before configure or start', async () => {
  const calls = [];
  let enteredUpdate;
  const entered = new Promise((resolve) => { enteredUpdate = resolve; });
  let finishUpdate;
  const update = new Promise((resolve) => { finishUpdate = resolve; });
  const controller = new SetupController({
    manager: {
      probe: async () => ({ ok: true, distro: { installed: true, appliance: { version: '1.2.13' } } }),
      install: async (manifest) => {
        calls.push(['update', manifest.version]);
        enteredUpdate();
        await update;
        calls.push(['updated']);
      },
      configureDownloadPath: async (value) => calls.push(['path', value]),
      start: async () => {
        calls.push(['start']);
        return { port: 8080, version: '1.2.14' };
      },
    },
    manifestProvider: async () => ({ version: '1.2.14' }),
    stateStore: memoryStateStore(),
    credentialStore: { load: () => ({}), save() {} },
  });

  const pending = controller.begin({ downloadDir: 'C:\\Users\\Test User\\Music\\Apple Music Downloader' });
  await entered;
  assert.deepEqual(calls, [['update', '1.2.14']]);
  finishUpdate();

  const result = await pending;
  assert.equal(result.ok, true);
  assert.deepEqual(calls.map(([operation]) => operation), ['update', 'updated', 'path', 'start']);
});

test('setup does not read encrypted-store credentials when network preflight fails', async () => {
  let credentialLoads = 0;
  let startCredentials;
  let startOptions;
  const manager = {
    probe: async () => ({ ok: true, distro: { installed: true, appliance: { version: '1.2.14' } } }),
    configureDownloadPath: async () => {},
    start: async (credentials, options) => {
      startCredentials = credentials;
      startOptions = options;
      throw Object.assign(new Error('untrusted preflight failure'), {
        code: 'network_preflight_failed',
        details: { stage: 'dns' },
      });
    },
  };
  const controller = new SetupController({
    manager,
    manifestProvider: async () => ({ version: '1.2.14' }),
    stateStore: memoryStateStore(),
    credentialStore: {
      save() {},
      load: () => { credentialLoads += 1; return { applePassword: 'must-not-be-read' }; },
    },
  });

  const result = await controller.begin({});

  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'network_preflight_failed');
  assert.equal(startCredentials, undefined);
  assert.equal(typeof startOptions?.credentialProvider, 'function');
  assert.equal(credentialLoads, 0);
});

test('setup surfaces a stable Chinese recovery action without persisting error details', async () => {
  const controller = new SetupController({
    manager: {
      probe: async () => ({
        ok: false,
        error: { code: 'virtualization_disabled', message: '固件虚拟化未启用。', action: '请在 BIOS/UEFI 中启用虚拟化。', details: 'internal' },
      }),
    },
    manifestProvider: async () => { throw new Error('must not download'); },
    stateStore: memoryStateStore(),
    credentialStore: { load: () => ({}), save() {} },
  });

  const result = await controller.begin({});
  assert.equal(result.ok, false);
  assert.equal(result.error.code, 'virtualization_disabled');
  assert.match(result.error.action, /BIOS\/UEFI/);
  assert.equal(JSON.stringify(controller.state()).includes('internal'), false);
});

test('setup resumes a persisted import transaction through the manager recovery path', async () => {
  const calls = [];
  const controller = new SetupController({
    manager: {
      probe: async () => ({
        ok: false,
        error: { code: 'distro_corrupt', message: '发行版初始化中断。', action: '请重试安装。' },
      }),
      install: async (_manifest, options) => calls.push(['install', options]),
      configureDownloadPath: async () => {},
      start: async () => ({ port: 43123, version: '1.2.0' }),
    },
    manifestProvider: async () => ({ version: '1.2.0' }),
    stateStore: memoryStateStore(),
    credentialStore: { load: () => ({}), save() {} },
  });

  const result = await controller.begin({});
  assert.equal(result.ok, true);
  assert.deepEqual(calls, [['install', { recover: true }]]);
  assert.equal(result.state.setupStep, 'starting');
  assert.equal(result.pendingCompletion, true);
});

test('WSL install announces a visible administrator terminal before waiting and preserves a retryable failure', async () => {
  let releaseInstall;
  const install = new Promise((resolve) => { releaseInstall = resolve; });
  const progress = [];
  const controller = new SetupController({
    manager: { probe: async () => ({ ok: true, distro: { installed: false } }) },
    manifestProvider: async () => ({ version: '1.2.0' }),
    stateStore: memoryStateStore(),
    credentialStore: { load: () => ({}), save() {} },
    installWsl: async () => install,
  });
  controller.on('progress', (event) => progress.push(event));

  const pending = controller.applyWslAction('wsl_missing');
  await Promise.resolve();
  assert.deepEqual(progress[0], {
    step: 'wsl_action',
    lastErrorCode: 'wsl_missing',
    operation: 'install',
    visibleTerminal: true,
  });
  releaseInstall();
  assert.equal((await pending).ok, true);

  const failure = Object.assign(new Error('管理员授权已取消或安装窗口已关闭。'), {
    code: 'wsl_missing',
    action: '请点击“安装 WSL2”重试。',
  });
  controller.installWsl = async () => { throw failure; };
  await assert.rejects(controller.applyWslAction('wsl_missing'), (error) => error === failure);
  assert.equal(controller.state().setupStep, 'failed');
  assert.equal(controller.state().lastErrorCode, 'wsl_missing');
});

test('setup exposes only a trusted network-preflight stage and replaces untrusted text with fixed copy', () => {
  for (const stage of NETWORK_PREFLIGHT_STAGES) {
    assert.deepEqual(publicError({
      code: 'network_preflight_failed',
      message: 'untrusted upstream message',
      action: 'untrusted upstream action',
      details: { stage, stderr: 'untrusted upstream stderr' },
    }), {
      code: 'network_preflight_failed',
      message: 'WSL Appliance 无法通过受支持的网络路径连接 Apple 认证服务。',
      action: '请保持 Windows 代理或网络工具运行，然后重试；无需重新输入密码或验证码。',
      details: { stage },
    });
  }

  for (const details of [
    { stage: 'complete' },
    { stage: 'helper_launch\nunsafe' },
    { stage: 8 },
    { stderr: 'sentinel' },
    null,
  ]) {
    const exposed = publicError({
      code: 'network_preflight_failed',
      message: 'raw sentinel message',
      action: 'raw sentinel action',
      details,
    });
    assert.equal(Object.hasOwn(exposed, 'details'), false);
    assert.doesNotMatch(JSON.stringify(exposed), /raw sentinel|secret|sentinel/i);
  }

  const mixedDetails = publicError({
    code: 'network_preflight_failed',
    details: { stage: 'dns', path: 'C:\\secret', stderr: 'sentinel' },
  });
  assert.deepEqual(mixedDetails.details, { stage: 'dns' });
  assert.doesNotMatch(JSON.stringify(mixedDetails), /secret|sentinel/i);

  assert.equal(Object.hasOwn(publicError({
    code: 'setup_failed',
    details: { stage: 'dns' },
  }), 'details'), false);
});

test('setup exposes CA/TLS diagnostics only when their schema-v2 pairing is complete', () => {
  assert.deepEqual(publicError({
    code: 'network_preflight_failed',
    details: { stage: 'ca_bundle_invalid', caSource: 'bundled_v1', curlCode: 77 },
  }).details, { stage: 'ca_bundle_invalid', caSource: 'bundled_v1', curlCode: 77 });
  assert.deepEqual(publicError({
    code: 'network_preflight_failed',
    details: { stage: 'tls', caSource: 'bundled_v1', curlCode: 60, sslVerifyResult: 20 },
  }).details, { stage: 'tls', caSource: 'bundled_v1', curlCode: 60, sslVerifyResult: 20 });
  assert.deepEqual(publicError({
    code: 'network_preflight_failed',
    details: { stage: 'tls', caSource: 'spoofed', curlCode: 60, sslVerifyResult: 20, path: 'C:\\secret' },
  }).details, { stage: 'tls' });
  assert.deepEqual(publicError({
    code: 'runtime_reinstall_required',
    message: 'untrusted',
    action: 'untrusted',
  }), {
    code: 'runtime_reinstall_required',
    message: '当前 WSL Appliance 不能原地升级到此版本。',
    action: '应用不会修改现有发行版或数据。请保留数据并在新的专用 Appliance 中完成安装。',
  });
});

test('setup exposes native probe failures with fixed non-sensitive guidance only', () => {
  for (const stage of ['native_probe_invalid', 'native_startup']) {
    const exposed = publicError({
      code: 'network_preflight_failed',
      message: 'sensitive loader detail sentinel',
      action: 'sensitive path sentinel',
      details: { stage, stderr: 'sensitive stderr sentinel' },
    });
    assert.deepEqual(exposed, {
      code: 'network_preflight_failed',
      message: 'WSL Appliance 无法通过受支持的网络路径连接 Apple 认证服务。',
      action: '请保持 Windows 代理或网络工具运行，然后重试；无需重新输入密码或验证码。',
      details: { stage },
    });
    assert.doesNotMatch(JSON.stringify(exposed), /sensitive|stderr|loader/i);
  }
});

test('setup maps download path transition failures to fixed non-path-leaking copy', () => {
  const cases = [
    ['path_change_active_jobs', '当前有下载任务，不能切换目录。', '请等待任务完成或取消任务后重试；应用不会修改当前目录。'],
    ['path_change_failed_recovered', '下载目录切换失败，已恢复原目录和服务。', '请确认目标目录可写后重试；现有任务和服务已保留。'],
    ['path_change_recovery_failed', '下载目录切换和自动恢复均未完成。', '应用已停止以保护现有任务数据，请执行“修复服务”后重试。'],
  ];
  for (const [code, message, action] of cases) {
    const exposed = publicError({
      code,
      message: 'raw path C:\\secret\\sentinel',
      action: 'raw helper error',
      details: { path: 'E:\\private', stderr: 'sentinel' },
    });
    assert.deepEqual(exposed, { code, message, action });
    assert.doesNotMatch(JSON.stringify(exposed), /secret|sentinel|E:\\private/i);
  }
});

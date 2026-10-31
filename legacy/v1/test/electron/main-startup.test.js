'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');
const path = require('node:path');

const mainModulePath = path.resolve(__dirname, '..', '..', 'src', 'main', 'main.js');

async function settle() {
  for (let index = 0; index < 8; index += 1) await new Promise((resolve) => setImmediate(resolve));
}

test('Electron opens the browser-first local shell without blocking on the WSL2 setup wizard', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  const loadedFiles = [];
  let resolveReady;
  let startCalls = 0;
  let credentialReads = 0;
  let exportDialogs = 0;
  const startupOrder = [];
  const ipcHandlers = new Map();
  let directorySelection = { canceled: true, filePaths: [] };
  const configuredDirectories = [];
  const savedStates = [];
  let directoryError = null;

  class MockBrowserWindow {
    constructor() {
      MockBrowserWindow.latest = this;
      this.webContents = { setWindowOpenHandler() {}, on() {}, send() {}, mainFrame: { url: '' }, getURL() { return this.mainFrame.url; } };
    }
    once(_event, callback) { callback(); }
    on() {}
    show() {}
    focus() {}
    removeMenu() {}
    isDestroyed() { return false; }
    async loadURL(url) { loadedFiles.push(url); this.webContents.mainFrame.url = url; }
    async loadFile(filename) { loadedFiles.push(filename); }
    static getAllWindows() { return []; }
  }

  class MockWslManager {
    constructor() { this.downloadDir = 'C:\\Users\\测试\\Music\\Apple Music Downloader'; }
    async probe() {
      return { ok: false, error: { code: 'wsl_missing', message: '尚未安装 WSL。', action: '使用向导安装。' } };
    }
    async start() { startCalls += 1; }
    async configureDownloadPath(directory) {
      configuredDirectories.push(directory);
      if (directoryError) throw directoryError;
      this.downloadDir = directory;
      return { windowsPath: directory, serviceRestarted: false };
    }
  }
  class MockWslManagerError extends Error {
    constructor(code, details) { super('网络预检失败'); this.code = code; this.action = '请重试'; this.details = details; }
  }

  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            getVersion: () => require('../../electron/legacy-desktop-package.json').version,
            requestSingleInstanceLock: () => { startupOrder.push('single-instance-lock'); return true; },
            getPath: (name) => {
              startupOrder.push(`get-path:${name}`);
              return name === 'music' ? 'C:\\Users\\测试\\Music' : 'C:\\test-user-data';
            },
            whenReady: () => new Promise((resolve) => { resolveReady = resolve; }),
            on() {},
            quit() {},
            setLoginItemSettings() {},
          },
          BrowserWindow: MockBrowserWindow,
          Menu: { buildFromTemplate: () => ({}), setApplicationMenu() {} },
          dialog: { showOpenDialog: async () => directorySelection, showMessageBox: async () => ({ response: 0 }), showSaveDialog: async () => { exportDialogs += 1; return { canceled: true }; } },
          ipcMain: { handle: (name, handler) => ipcHandlers.set(name, handler) },
          safeStorage: {},
        };
      }
      if (request === '../../electron/credentials') {
        return { CredentialStore: class { load() { credentialReads += 1; return {}; } save() {} } };
      }
      if (request === '../../electron/desktop-state') {
        return { DesktopStateStore: class { load() { return {}; } save(value) { savedStates.push(value); return value; } } };
      }
      if (request === '../../electron/windows-capabilities') {
        return { WindowsCapabilities: class {}, defaultWindowsHelperPath: () => 'probe.exe' };
      }
      if (request === '../../electron/wsl-manager') return { WslManager: MockWslManager, WslManagerError: MockWslManagerError };
      if (request === '../../electron/setup-controller') {
        return { ...originalLoad.call(this, request, parent, isMain), SetupController: class { on() {} } };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const mainModule = require(mainModulePath);
    assert.deepEqual(mainModule.enabledLoginItemSettings(['--autostart']), {
      openAtLogin: true,
      name: 'AppleMusicDownloader',
      args: ['--autostart'],
    });
    assert.equal(mainModule.packagedProductVersion(), require('../../electron/legacy-desktop-package.json').version);
    assert.equal(mainModule.safeRuntimeVersion('1.3.5'), '1.3.5');
    assert.equal(mainModule.safeRuntimeVersion('1.3.0-beta.1'), '1.3.0-beta.1');
    assert.equal(mainModule.safeRuntimeVersion('1.3.0.1'), 'unknown');
    resolveReady();
    await settle();

    assert.equal(startupOrder[0], 'single-instance-lock');
    assert.equal(startCalls, 0);
    assert.equal(loadedFiles.length, 1);
    assert.match(loadedFiles[0], /renderer\/index\.html#browse$/);
    assert.equal(loadedFiles.some((entry) => /setup\.html$/.test(entry)), false);
    assert.equal(mainModule.isExactOfflineShellURL(loadedFiles[0]), true);
    assert.equal(mainModule.isExactOfflineShellURL(loadedFiles[0].replace('#browse', '#evil')), false);
    const owner = MockBrowserWindow.latest;
    const trustedEvent = { sender: owner.webContents, senderFrame: owner.webContents.mainFrame };
    const chooseDirectory = ipcHandlers.get('desktop:choose-download-location');
    const readsBeforeDirectory = credentialReads;
    assert.deepEqual(await chooseDirectory(trustedEvent), { ok: true, cancelled: true });
    assert.deepEqual(configuredDirectories, []);
    directorySelection = { canceled: false, filePaths: ['E:\\音乐 下载'] };
    assert.deepEqual(await chooseDirectory(trustedEvent), { ok: true });
    assert.deepEqual(configuredDirectories, ['E:\\音乐 下载']);
    assert.equal(savedStates.at(-1).downloadDir, 'E:\\音乐 下载');
    directoryError = new MockWslManagerError('path_change_active_jobs');
    directorySelection = { canceled: false, filePaths: ['I:\\其他目录'] };
    const stateCount = savedStates.length;
    assert.equal((await chooseDirectory(trustedEvent)).error.code, 'path_change_active_jobs');
    assert.equal(savedStates.slice(stateCount).some((state) => state.downloadDir === 'I:\\其他目录'), false);
    assert.equal(credentialReads, readsBeforeDirectory, 'directory changes must not request credentials');
    assert.equal(loadedFiles.some((entry) => /setup\.html$/.test(entry)), false, 'directory chooser is independent of installation wizard');
    const readsBeforeExport = credentialReads;
    assert.deepEqual(await ipcHandlers.get('desktop:export-diagnostics')(trustedEvent, ['CKC error.']), { ok: true, cancelled: true });
    assert.equal(credentialReads, readsBeforeExport, 'export cancellation must not read credentials');
    assert.equal(exportDialogs, 1);
    await assert.rejects(() => ipcHandlers.get('desktop:export-diagnostics')({ ...trustedEvent, senderFrame: { url: loadedFiles[0] } }), /IPC request is not allowed/);
    await assert.rejects(() => ipcHandlers.get('desktop:export-diagnostics')({ ...trustedEvent, sender: { mainFrame: owner.webContents.mainFrame } }), /IPC request is not allowed/);
    assert.equal(exportDialogs, 1, 'untrusted frames cannot open a file dialog');
    assert.deepEqual(await ipcHandlers.get('desktop:music-current')(trustedEvent), { ok: true, url: '' });
    await assert.rejects(() => ipcHandlers.get('desktop:music-current')({ ...trustedEvent, senderFrame: { url: loadedFiles[0] } }), /IPC request is not allowed/);
    assert.equal(typeof ipcHandlers.get('setup:get-diagnostics'), 'function');
    assert.equal(typeof ipcHandlers.get('setup:repair-runtime'), 'function');
    assert.equal(typeof ipcHandlers.get('desktop:repair-runtime'), 'function');
    for (const channel of [
      'desktop:choose-download-location',
      'desktop:open-download-settings',
      'desktop:open-credentials',
      'desktop:start-service',
      'desktop:show-diagnostics',
      'desktop:export-diagnostics',
      'desktop:stop-service',
      'desktop:playback-open-login',
      'desktop:playback-play',
      'desktop:playback-stop',
    ]) {
      assert.equal(typeof ipcHandlers.get(channel), 'function', channel);
      await assert.rejects(() => ipcHandlers.get(channel)({}), /IPC request is not allowed from this page/);
    }
    for (const channel of ['desktop:music-open', 'desktop:music-close', 'desktop:music-back', 'desktop:music-forward', 'desktop:music-reload', 'desktop:music-current', 'desktop:music-bounds']) {
      assert.equal(typeof ipcHandlers.get(channel), 'function', channel);
      await assert.rejects(() => ipcHandlers.get(channel)({}), /IPC request is not allowed from this page/);
    }
    await assert.rejects(
      () => ipcHandlers.get('setup:repair-runtime')({}),
      /IPC request is not allowed from this page/,
    );
    assert.match(mainModule.errorDocument({ code: 'health_timeout', message: '服务未就绪', action: '请查看诊断。' }), /打开诊断与修复/);
    assert.match(mainModule.errorDocument({ code: 'health_timeout', message: '服务未就绪', action: '请查看诊断。' }), /applemusic-downloader\.invalid\/repair-runtime/);
    const repairPromise = mainModule.repairRuntime({ confirm: true, showWindow: false });
    assert.equal(typeof repairPromise?.then, 'function', 'repair should return a recoverable promise');
    assert.deepEqual(await repairPromise, { ok: false, cancelled: true });
    assert.match(mainModule.errorDocument({ code: 'network_preflight_failed', details: { stage: 'tls' } }, 'expected_version=1.4.3'), /network_preflight_stage=tls/);
    const unknownStagePage = mainModule.errorDocument({ code: 'network_preflight_failed', details: { stage: 'SENTINEL_STAGE' } });
    assert.match(unknownStagePage, /network_preflight_stage=unknown/);
    assert.doesNotMatch(unknownStagePage, /SENTINEL_STAGE/);
    assert.doesNotMatch(mainModule.errorDocument({ code: 'health_timeout', details: { stage: 'tls' } }), /network_preflight_stage=/);
    assert.deepEqual(mainModule.publicRepairError(new MockWslManagerError('network_preflight_failed', { stage: 'tls' })).details, { stage: 'tls' });
    assert.deepEqual(mainModule.publicRepairError(new MockWslManagerError('network_preflight_failed', { stage: 'SENTINEL_STAGE' })).details, { stage: 'unknown' });
    assert.deepEqual(mainModule.publicOfflineServiceState({ code: 'network_preflight_failed', details: { stage: 'tls' } }), {
      state: 'unavailable', code: 'network_preflight_failed', stage: 'tls',
    });
    assert.deepEqual(mainModule.publicOfflineServiceState({ code: 'network_preflight_failed', details: { stage: 'SENTINEL_STAGE' } }), {
      state: 'unavailable', code: 'network_preflight_failed', stage: 'unknown',
    });
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('a process that loses the single-instance lock quits before resolving shared paths or registering startup work', () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  const calls = [];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => { calls.push('lock'); return false; },
            getPath: () => { calls.push('getPath'); return 'must-not-be-read'; },
            whenReady: () => { calls.push('whenReady'); return Promise.resolve(); },
            on: () => calls.push('on'),
            quit: () => calls.push('quit'),
          },
          BrowserWindow: class {},
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const mainModule = require(mainModulePath);
    assert.equal(mainModule.singleInstanceAcquired, false);
    assert.deepEqual(calls, ['lock', 'quit']);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('a normal second launch focuses the existing window while duplicate autostart remains silent', () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  const handlers = new Map();
  const focusCalls = [];
  const existingWindow = {
    isDestroyed: () => false,
    isMinimized: () => true,
    restore: () => focusCalls.push('restore'),
    show: () => focusCalls.push('show'),
    focus: () => focusCalls.push('focus'),
  };
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        class MockBrowserWindow {}
        MockBrowserWindow.getAllWindows = () => [existingWindow];
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on: (name, handler) => handlers.set(name, handler),
            isReady: () => true,
            quit() {},
          },
          BrowserWindow: MockBrowserWindow,
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    require(mainModulePath);
    assert.equal(typeof handlers.get('second-instance'), 'function');
    handlers.get('second-instance')({}, ['AppleMusicDownloader.exe', '--autostart']);
    assert.deepEqual(focusCalls, []);
    handlers.get('second-instance')({}, ['AppleMusicDownloader.exe']);
    assert.deepEqual(focusCalls, ['restore', 'show', 'focus']);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('explicit stop helper cancels each active job and waits for terminal state', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  let now = 0;
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };
    const { cancelActiveJobsAndWait } = require(mainModulePath);
    const requests = [];
    let activePolls = 0;
    const manager = {
      serviceBaseUrl: 'http://127.0.0.1:54321',
      desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': 'a'.repeat(64) }),
      async activeJobs() {
        activePolls += 1;
        return activePolls === 1 ? [{ id: 'still-cancelling', status: 'cancelling' }] : [];
      },
    };
    await cancelActiveJobsAndWait(manager, [{ id: 'job/一' }, { id: 'job-two' }], {
      request: async (url, options) => { requests.push({ url, options }); return { statusCode: 200 }; },
      now: () => now,
      sleep: async (milliseconds) => { now += milliseconds; },
      intervalMs: 10,
      timeoutMs: 100,
    });

    assert.equal(requests.length, 3);
    assert.ok(requests.every(({ options }) => options.method === 'POST'));
    assert.ok(requests.every(({ options }) => options.headers['X-AMDL-Desktop-Token'] === 'a'.repeat(64)));
    assert.match(requests[0].url, /\/api\/v1\/jobs\/job%2F%E4%B8%80\/cancel$/);
    assert.match(requests[2].url, /\/api\/v1\/jobs\/still-cancelling\/cancel$/);
    assert.equal(activePolls, 2);

    const queuedBatches = [
      Array.from({ length: 100 }, (_, index) => ({ id: `queued-${index}`, status: 'queued' })),
      Array.from({ length: 100 }, (_, index) => ({ id: `queued-${index + 100}`, status: 'queued' })),
      Array.from({ length: 5 }, (_, index) => ({ id: `queued-${index + 200}`, status: 'queued' })),
    ];
    let queuedPolls = 0;
    const queuedRequests = [];
    await cancelActiveJobsAndWait({
      serviceBaseUrl: manager.serviceBaseUrl,
      desktopProxyRequestHeaders: manager.desktopProxyRequestHeaders,
      activeJobs: async () => queuedBatches[queuedPolls += 1] || [],
    }, queuedBatches[0], {
      request: async (url) => { queuedRequests.push(url); return { statusCode: 200 }; },
      now: () => now,
      sleep: async (milliseconds) => { now += milliseconds; },
      intervalMs: 10,
      timeoutMs: 100,
    });
    assert.equal(queuedRequests.length, 205);
    assert.equal(new Set(queuedRequests).size, 205);
    assert.equal(queuedPolls, 3);

    await assert.rejects(
      cancelActiveJobsAndWait({
        serviceBaseUrl: manager.serviceBaseUrl,
        desktopProxyRequestHeaders: manager.desktopProxyRequestHeaders,
        activeJobs: async () => [{ id: 'stuck', status: 'cancelling' }],
      }, [], {
        request: async () => ({ statusCode: 200 }),
        now: () => now,
        sleep: async (milliseconds) => { now += milliseconds; },
        intervalMs: 10,
        timeoutMs: 10,
      }),
      (error) => error.code === 'cancel_timeout' && /服务仍保持运行/.test(error.action),
    );
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('desktop webRequest injects only for the exact owned origin and strips redirect leakage', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };
    const {
      diagnosticSecretValues,
      desktopNetworkRequestHeaders,
      installDesktopProxyRequestInterceptor,
      isArmedErrorRepairNavigation,
      isManagerServiceRequest,
      mainServiceFrameBelongsToWindow,
      localAPIRequest,
      loadManagerService,
      managerNavigationOptions,
      publicRepairError,
    } = require(mainModulePath);
    const token = 'b'.repeat(64);
    const targetManager = {
      serviceBaseUrl: 'http://127.0.0.1:54321',
      desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': token }),
    };

    assert.equal(isArmedErrorRepairNavigation(
      'https://applemusic-downloader.invalid/repair-runtime',
      'data:text/html,error-page',
      'data:text/html,error-page',
    ), true);
    assert.equal(isArmedErrorRepairNavigation(
      'https://applemusic-downloader.invalid/repair-runtime',
      'https://example.test/navigated-away',
      'data:text/html,error-page',
    ), false);

    const serviceMainFrame = { url: 'http://127.0.0.1:54321/' };
    const serviceWebContents = {
      id: 71,
      mainFrame: serviceMainFrame,
      getURL: () => serviceMainFrame.url,
    };
    const serviceWindow = {
      webContents: serviceWebContents,
      isDestroyed: () => false,
    };
    assert.equal(mainServiceFrameBelongsToWindow({ sender: serviceWebContents, senderFrame: serviceMainFrame }, serviceWindow, targetManager), true);
    for (const rejected of [
      { sender: { id: 71 }, senderFrame: serviceMainFrame },
      { sender: serviceWebContents, senderFrame: { url: serviceMainFrame.url } },
      { sender: serviceWebContents },
      { sender: serviceWebContents, senderFrame: serviceMainFrame, currentURL: 'https://example.test/' },
    ]) {
      if (rejected.currentURL) serviceWebContents.getURL = () => rejected.currentURL;
      assert.equal(mainServiceFrameBelongsToWindow(rejected, serviceWindow, targetManager), false);
      serviceWebContents.getURL = () => serviceMainFrame.url;
    }

    const safeFailure = publicRepairError(Object.assign(new Error('credential-secret-sentinel'), { code: 'unexpected', action: 'raw action' }));
    assert.equal(safeFailure.code, 'repair_failed');
    assert.equal(safeFailure.message.includes('credential-secret-sentinel'), false);
    assert.equal(safeFailure.action.includes('raw action'), false);

    assert.deepEqual(managerNavigationOptions(targetManager), {
      extraHeaders: `X-AMDL-Desktop-Token: ${token}\n`,
    });
    const navigationCalls = [];
    await loadManagerService({
      loadURL: async (...args) => navigationCalls.push(args),
    }, targetManager);
    assert.deepEqual(navigationCalls, [[
      targetManager.serviceBaseUrl,
      { extraHeaders: `X-AMDL-Desktop-Token: ${token}\n` },
    ]]);
    assert.deepEqual(diagnosticSecretValues({
      appleEmail: 'person@example.test',
      applePassword: 'password-secret',
      mediaUserToken: 'media-secret',
      authorizationToken: 'authorization-secret',
      storefront: 'us',
    }), [
      'person@example.test',
      'password-secret',
      'media-secret',
      'authorization-secret',
    ]);

    assert.equal(isManagerServiceRequest('http://127.0.0.1:54321/api/v1/jobs', targetManager), true);
    for (const rejected of [
      'http://127.0.0.1:54322/api/v1/jobs',
      'http://localhost:54321/api/v1/jobs',
      'http://user@127.0.0.1:54321/api/v1/jobs',
      'https://127.0.0.1:54321/api/v1/jobs',
    ]) assert.equal(isManagerServiceRequest(rejected, targetManager), false);

    const ownedMainFrame = { url: 'http://127.0.0.1:54321/' };
    const ownedWebContents = {
      id: 41,
      mainFrame: ownedMainFrame,
      getURL: () => ownedMainFrame.url,
    };
    const allowed = desktopNetworkRequestHeaders({
      webContentsId: 41,
      webContents: ownedWebContents,
      frame: ownedMainFrame,
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: { Accept: 'application/json' },
    }, targetManager, ownedWebContents);
    assert.deepEqual(allowed, { Accept: 'application/json', 'X-AMDL-Desktop-Token': token });

    const allowedWithoutOptionalID = desktopNetworkRequestHeaders({
      webContents: ownedWebContents,
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: { Accept: 'application/json' },
    }, targetManager, ownedWebContents);
    assert.deepEqual(allowedWithoutOptionalID, { Accept: 'application/json' });

    const allowedWithObjectFallback = desktopNetworkRequestHeaders({
      webContents: ownedWebContents,
      frame: ownedMainFrame,
      resourceType: 'xhr',
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: { Accept: 'application/json' },
    }, targetManager, ownedWebContents);
    assert.deepEqual(allowedWithObjectFallback, {
      Accept: 'application/json',
      'X-AMDL-Desktop-Token': token,
    });

    const allowedWithoutOptionalIDOrWebContents = desktopNetworkRequestHeaders({
      frame: { top: ownedMainFrame },
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: { Accept: 'application/json' },
    }, targetManager, ownedWebContents);
    assert.deepEqual(allowedWithoutOptionalIDOrWebContents, { Accept: 'application/json' });

    const rejectedConflictingID = desktopNetworkRequestHeaders({
      webContentsId: 99,
      webContents: ownedWebContents,
      frame: { top: ownedMainFrame },
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: { 'X-AMDL-Desktop-Token': token },
    }, targetManager, ownedWebContents);
    assert.deepEqual(rejectedConflictingID, {});

    const rejectedForeignWebContents = desktopNetworkRequestHeaders({
      webContents: { id: 41 },
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: { 'X-AMDL-Desktop-Token': token },
    }, targetManager, ownedWebContents);
    assert.deepEqual(rejectedForeignWebContents, {});

    for (const rejected of [
      { webContentsId: '41', webContents: ownedWebContents },
      { webContentsId: 41, webContents: { id: 41 } },
      { webContentsId: 41, resourceType: 'subFrame', frame: {} },
      { webContentsId: 41, frame: null },
      { webContentsId: 41, frame: { url: 'data:text/html,error' } },
      { webContentsId: 41, frame: { url: 'https://example.test/' } },
    ]) {
      assert.deepEqual(desktopNetworkRequestHeaders({
        ...rejected,
        url: 'http://127.0.0.1:54321/api/v1/jobs',
        requestHeaders: { 'X-AMDL-Desktop-Token': token },
      }, targetManager, ownedWebContents), {});
    }

    const initialMainFrame = { url: '' };
    ownedWebContents.mainFrame = initialMainFrame;
    ownedWebContents.getURL = () => '';
    const allowedInitialNavigation = desktopNetworkRequestHeaders({
      webContentsId: 41,
      webContents: ownedWebContents,
      frame: initialMainFrame,
      resourceType: 'mainFrame',
      url: 'http://127.0.0.1:54321/',
      requestHeaders: { 'X-AMDL-Desktop-Token': token },
    }, targetManager, ownedWebContents);
    assert.deepEqual(allowedInitialNavigation, { 'X-AMDL-Desktop-Token': token });
    for (const rejectedNavigation of [
      { requestHeaders: {} },
      { requestHeaders: { 'X-AMDL-Desktop-Token': '0'.repeat(64) } },
      { requestHeaders: { 'X-AMDL-Desktop-Token': token }, url: 'http://127.0.0.1:54321/api/v1/jobs' },
      { requestHeaders: { 'X-AMDL-Desktop-Token': token }, frame: { url: '' } },
      { requestHeaders: { 'X-AMDL-Desktop-Token': token }, webContentsId: 99 },
    ]) {
      assert.deepEqual(desktopNetworkRequestHeaders({
        webContentsId: 41,
        webContents: ownedWebContents,
        frame: initialMainFrame,
        resourceType: 'mainFrame',
        url: 'http://127.0.0.1:54321/',
        ...rejectedNavigation,
      }, targetManager, ownedWebContents), {});
    }

    ownedWebContents.mainFrame = ownedMainFrame;
    ownedWebContents.getURL = () => ownedMainFrame.url;

    for (const downloadPath of [
      `/api/v1/files/${'a'.repeat(32)}`,
      `/api/v1/jobs/${'b'.repeat(32)}/archive`,
    ]) {
      assert.deepEqual(desktopNetworkRequestHeaders({
        webContentsId: 41,
        webContents: ownedWebContents,
        frame: ownedMainFrame,
        resourceType: 'mainFrame',
        method: 'GET',
        url: `http://127.0.0.1:54321${downloadPath}`,
        requestHeaders: { Accept: 'application/octet-stream' },
      }, targetManager, ownedWebContents), {
        Accept: 'application/octet-stream',
        'X-AMDL-Desktop-Token': token,
      });
    }
    for (const rejectedDownload of [
      { method: 'POST', url: `http://127.0.0.1:54321/api/v1/files/${'a'.repeat(32)}` },
      { method: 'GET', url: `http://127.0.0.1:54321/api/v1/files/${'a'.repeat(31)}` },
      { method: 'GET', url: `http://127.0.0.1:54321/api/v1/jobs/${'b'.repeat(32)}` },
      { method: 'GET', url: `http://127.0.0.1:54321/api/v1/jobs/${'b'.repeat(32)}/archive?extra=1` },
      { method: 'GET', url: `http://127.0.0.1:54321/api/v1/files/${'a'.repeat(32)}`, frame: {} },
      { method: 'GET', url: `http://127.0.0.1:54321/api/v1/files/${'a'.repeat(32)}`, webContentsId: 99 },
    ]) {
      assert.deepEqual(desktopNetworkRequestHeaders({
        webContentsId: 41,
        webContents: ownedWebContents,
        frame: ownedMainFrame,
        resourceType: 'mainFrame',
        requestHeaders: { 'X-AMDL-Desktop-Token': token },
        ...rejectedDownload,
      }, targetManager, ownedWebContents), {});
    }

    const allowedMainFrame = desktopNetworkRequestHeaders({
      webContentsId: 41,
      webContents: ownedWebContents,
      frame: ownedMainFrame,
      resourceType: 'xhr',
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: {},
    }, targetManager, ownedWebContents);
    assert.equal(allowedMainFrame['X-AMDL-Desktop-Token'], token);

    ownedWebContents.getURL = () => 'https://example.test/navigated-away';
    const rejectedAfterNavigation = desktopNetworkRequestHeaders({
      webContentsId: 41,
      webContents: ownedWebContents,
      frame: ownedMainFrame,
      resourceType: 'xhr',
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: {},
    }, targetManager, ownedWebContents);
    assert.deepEqual(rejectedAfterNavigation, {});

    for (const details of [
      { webContentsId: 41, url: 'http://127.0.0.1:54322/redirected' },
      { webContentsId: 41, url: 'https://example.test/redirected' },
      { webContentsId: 99, url: 'http://127.0.0.1:54321/api/v1/jobs' },
    ]) {
      const stripped = desktopNetworkRequestHeaders({
        ...details,
        requestHeaders: { Accept: '*/*', 'x-amdl-desktop-token': token },
      }, targetManager, 41);
      assert.deepEqual(stripped, { Accept: '*/*' });
    }

    const registrations = [];
    const webRequest = {
      onBeforeSendHeaders(filter, listener) {
        if (filter === null) registrations.push({ removed: true });
        else registrations.push({ filter, listener });
      },
    };
    const firstMainFrame = { url: 'http://127.0.0.1:54321/' };
    const firstWindow = {
      webContents: {
        id: 41,
        mainFrame: firstMainFrame,
        getURL: () => firstMainFrame.url,
        session: { webRequest },
      },
    };
    const firstDispose = installDesktopProxyRequestInterceptor(firstWindow, () => targetManager);
    const firstRegistration = registrations[0];
    assert.deepEqual(firstRegistration.filter, { urls: ['<all_urls>'] });
    let callbackResult;
    firstRegistration.listener({
      webContentsId: 41,
      webContents: firstWindow.webContents,
      frame: firstMainFrame,
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: {},
    }, (result) => { callbackResult = result; });
    assert.equal(callbackResult.requestHeaders['X-AMDL-Desktop-Token'], token);
    firstRegistration.listener({
      webContentsId: 41,
      url: 'https://example.test/after-redirect',
      requestHeaders: { 'X-AMDL-Desktop-Token': token },
    }, (result) => { callbackResult = result; });
    assert.equal(Object.keys(callbackResult.requestHeaders).length, 0);
    firstRegistration.listener({
      webContentsId: 41,
      webContents: firstWindow.webContents,
      frame: { url: 'data:text/html,error' },
      url: 'http://127.0.0.1:54321/api/v1/jobs',
      requestHeaders: { 'X-AMDL-Desktop-Token': token },
    }, (result) => { callbackResult = result; });
    assert.equal(Object.keys(callbackResult.requestHeaders).length, 0);

    assert.equal(installDesktopProxyRequestInterceptor(firstWindow, () => targetManager), firstDispose);
    assert.equal(registrations.length, 1, 'repeated install must not add another listener');
    const secondDispose = installDesktopProxyRequestInterceptor(
      { webContents: { id: 42, session: { webRequest } } },
      () => targetManager,
    );
    assert.deepEqual(registrations.map((entry) => Boolean(entry.removed)), [false, true, false]);
    firstDispose();
    assert.equal(registrations.length, 3, 'stale disposer must not remove the replacement listener');
    secondDispose();
    assert.equal(registrations.at(-1).removed, true);

    const requestCalls = [];
    const response = new (require('node:events').EventEmitter)();
    response.statusCode = 204;
    const request = new (require('node:events').EventEmitter)();
    request.end = () => {
      requestCalls[0].callback(response);
      response.emit('end');
    };
    request.destroy = () => {};
    return localAPIRequest('http://127.0.0.1:54321/api/v1/jobs/job/cancel', {
      method: 'POST',
      headers: { 'X-AMDL-Desktop-Token': token },
      requestImpl: (url, options, callback) => {
        requestCalls.push({ url, options, callback });
        return request;
      },
    }).then((result) => {
      assert.equal(result.statusCode, 204);
      assert.equal(requestCalls[0].options.headers['X-AMDL-Desktop-Token'], token);
    });
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('endpoint publication commits a rotated proxy endpoint and waits for the authenticated top-level navigation', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const { publishServiceEndpoint } = require(mainModulePath);
    const token = 'c'.repeat(64);
    const targetManager = {
      serviceBaseUrl: 'http://127.0.0.1:54342',
      desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': token }),
    };
    const saved = [];
    const readiness = [];
    const navigationCalls = [];
    let resolveLoad;
    let navigationStarted;
    const navigationStartedPromise = new Promise((resolve) => { navigationStarted = resolve; });
    const navigationBarrier = new Promise((resolve) => { resolveLoad = resolve; });
    const window = {
      loadURL: async (...args) => {
        navigationCalls.push(args);
        navigationStarted();
        await navigationBarrier;
      },
    };

    let settled = false;
    const publication = publishServiceEndpoint({
      port: 54342,
      backendPort: 8080,
      serviceBaseUrl: 'http://127.0.0.1:54342',
    }, {
      manager: targetManager,
      desktopState: { save: (next) => saved.push(next) },
      showWindow: true,
      createMainWindow: () => window,
      setServiceReady: (value) => readiness.push(value),
      state: { pendingCredentialDelivery: false },
    }).then((result) => {
      settled = true;
      return result;
    });

    await navigationStartedPromise;
    assert.equal(settled, false, 'the caller must wait for the new top-level document');
    assert.deepEqual(saved, [{ pendingCredentialDelivery: false, localPort: 54342 }]);
    assert.deepEqual(readiness, [true]);
    assert.deepEqual(navigationCalls, [[
      'http://127.0.0.1:54342',
      { extraHeaders: `X-AMDL-Desktop-Token: ${token}\n` },
    ]]);

    resolveLoad();
    assert.deepEqual(await publication, {
      published: true,
      reconnected: true,
      error: null,
      port: 54342,
    });
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('endpoint publication rejects a stale running snapshot when the live supervisor is stopped', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const { publishServiceEndpoint } = require(mainModulePath);
    const token = 'e'.repeat(64);
    const generation = 'f'.repeat(64);
    const candidate = {
      version: '1.3.3',
      state: 'running',
      pid: 1201,
      supervisorPid: 1200,
      port: 54343,
      backendPort: 8080,
      serviceBaseUrl: 'http://127.0.0.1:54343',
      network: {
        schemaVersion: 1,
        path: 'windows_stdio_bridge',
        state: 'ready',
        stage: 'complete',
        windowsPath: 'reachable',
        wslDirectPath: 'unknown',
        proxyPropagated: true,
        generationId: generation,
      },
    };
    const targetManager = {
      serviceBaseUrl: candidate.serviceBaseUrl,
      desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': token }),
      status: async () => ({
        ...candidate,
        state: 'stopped',
        pid: 0,
        serviceBaseUrl: '',
        port: 0,
        network: { ...candidate.network, state: 'unprepared', proxyPropagated: false },
      }),
    };
    const state = [];
    const result = await publishServiceEndpoint(candidate, {
      manager: targetManager,
      desktopState: { save: (next) => state.push(next) },
      expectedVersion: '1.3.3',
      requireLiveStatus: true,
      showWindow: false,
    });
    assert.equal(result.published, false);
    assert.deepEqual(state, [{ localPort: null }]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('endpoint publication requires the same live runtime and network generation before publishing', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const { publishServiceEndpoint } = require(mainModulePath);
    const generation = '1'.repeat(64);
    const candidate = {
      version: '1.3.3',
      state: 'running',
      pid: 2201,
      supervisorPid: 2200,
      port: 54344,
      backendPort: 8080,
      serviceBaseUrl: 'http://127.0.0.1:54344',
      network: {
        schemaVersion: 1,
        path: 'windows_stdio_bridge',
        state: 'ready',
        stage: 'complete',
        windowsPath: 'reachable',
        wslDirectPath: 'unknown',
        proxyPropagated: true,
        generationId: generation,
      },
    };
    const writes = [];
    const result = await publishServiceEndpoint(candidate, {
      manager: {
        serviceBaseUrl: candidate.serviceBaseUrl,
        desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': '1'.repeat(64) }),
        status: async () => ({ ...candidate, network: { ...candidate.network } }),
      },
      desktopState: { save: (next) => writes.push(next) },
      expectedVersion: candidate.version,
      requireLiveStatus: true,
      showWindow: false,
    });
    assert.equal(result.published, true);
    assert.deepEqual(writes, [{ localPort: candidate.port }]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('endpoint publication rejects an old runtime identity before state or navigation', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };
    const { publishServiceEndpoint } = require(mainModulePath);
    const state = [];
    const readiness = [];
    let navigated = false;
    const result = await publishServiceEndpoint({
      version: '1.2.9',
      port: 54342,
      backendPort: 8080,
      serviceBaseUrl: 'http://127.0.0.1:54342',
    }, {
      expectedVersion: '1.2.10',
      manager: {
        serviceBaseUrl: 'http://127.0.0.1:54342',
        desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': 'f'.repeat(64) }),
      },
      desktopState: { save: (next) => state.push(next) },
      setServiceReady: (value) => readiness.push(value),
      showWindow: true,
      createMainWindow: () => {
        navigated = true;
        return { loadURL: async () => {} };
      },
    });
    assert.equal(result.published, false);
    assert.equal(navigated, false);
    assert.deepEqual(state, [{ localPort: null }]);
    assert.deepEqual(readiness, [false]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('invalidating a published endpoint immediately strips token access from its old main frame', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: { requestSingleInstanceLock: () => true, whenReady: () => new Promise(() => {}), on() {}, quit() {} },
          BrowserWindow: { getAllWindows: () => [] }, Menu: {}, dialog: {}, ipcMain: {}, safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const {
      desktopNetworkRequestHeaders,
      invalidateServiceEndpoint,
      loadManagerService,
      mainServiceFrameBelongsToWindow,
      publishServiceEndpoint,
    } = require(mainModulePath);
    const token = 'd'.repeat(64);
    const targetManager = {
      serviceBaseUrl: 'http://127.0.0.1:54342',
      desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': token }),
    };
    const frame = { url: 'http://127.0.0.1:54342/' };
    const contents = { id: 42, mainFrame: frame, getURL: () => frame.url };
    const window = { webContents: contents, isDestroyed: () => false, loadURL: async () => {} };
    const stateWrites = [];
    const readiness = [];
    const request = {
      webContentsId: 42,
      webContents: contents,
      frame,
      resourceType: 'xhr',
      url: 'http://127.0.0.1:54342/api/v1/capabilities',
      requestHeaders: { Accept: 'application/json' },
    };

    const publication = await publishServiceEndpoint({
      version: '1.2.10',
      port: 54342,
      backendPort: 8080,
      serviceBaseUrl: targetManager.serviceBaseUrl,
    }, {
      expectedVersion: '1.2.10',
      manager: targetManager,
      desktopState: { save: (next) => stateWrites.push(next) },
      setServiceReady: (value) => readiness.push(value),
    });
    assert.equal(publication.published, true);
    assert.equal(desktopNetworkRequestHeaders(request, targetManager, contents, { requirePublished: true })['X-AMDL-Desktop-Token'], token);
    assert.equal(mainServiceFrameBelongsToWindow({ sender: contents, senderFrame: frame }, window, targetManager, { requirePublished: true }), true);

    await invalidateServiceEndpoint({
      manager: targetManager,
      desktopState: { save: (next) => stateWrites.push(next) },
      setServiceReady: (value) => readiness.push(value),
    });

    assert.deepEqual(desktopNetworkRequestHeaders(request, targetManager, contents, { requirePublished: true }), { Accept: 'application/json' });
    assert.equal(mainServiceFrameBelongsToWindow({ sender: contents, senderFrame: frame }, window, targetManager, { requirePublished: true }), false);
    await assert.rejects(loadManagerService(window, targetManager, { requirePublished: true }), /endpoint is unavailable/);
    assert.deepEqual(stateWrites, [{ localPort: 54342 }, { localPort: null }]);
    assert.deepEqual(readiness, [true, false]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('credential delivery does not publish a stale endpoint and active jobs leave the current endpoint untouched', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const { publishServiceEndpoint, saveCredentialsAndReconnect } = require(mainModulePath);
    const staleState = [];
    const staleReadiness = [];
    const staleResult = await publishServiceEndpoint({
      port: 54342,
      backendPort: 8080,
      serviceBaseUrl: 'http://127.0.0.1:54341',
    }, {
      manager: {
        serviceBaseUrl: 'http://127.0.0.1:54342',
        desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': 'd'.repeat(64) }),
      },
      desktopState: { save: (next) => staleState.push(next) },
      showWindow: true,
      createMainWindow: () => { throw new Error('a mismatched endpoint must never be navigated'); },
      setServiceReady: (value) => staleReadiness.push(value),
    });
    assert.deepEqual(staleResult, {
      published: false,
      reconnected: false,
      error: {
        code: 'endpoint_publication_failed',
        message: '本机服务端点未能安全发布。',
        action: '请从“服务”菜单重新连接，或运行诊断与修复。',
      },
      port: null,
    });
    assert.deepEqual(staleState, [{ localPort: null }]);
    assert.deepEqual(staleReadiness, [false]);

    const stateWriteFailure = [];
    const stateWriteReadiness = [];
    const stateWriteResult = await publishServiceEndpoint({
      port: 54342,
      backendPort: 8080,
      serviceBaseUrl: 'http://127.0.0.1:54342',
    }, {
      manager: {
        serviceBaseUrl: 'http://127.0.0.1:54342',
        desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': 'd'.repeat(64) }),
      },
      desktopState: {
        save: (next) => {
          stateWriteFailure.push(next);
          if (next.localPort === 54342) throw new Error('state write failed');
        },
      },
      showWindow: true,
      createMainWindow: () => { throw new Error('state write failure must not navigate'); },
      setServiceReady: (value) => stateWriteReadiness.push(value),
    });
    assert.equal(stateWriteResult.published, false);
    assert.equal(stateWriteResult.reconnected, false);
    assert.deepEqual(stateWriteFailure, [{ localPort: 54342 }, { localPort: null }]);
    assert.deepEqual(stateWriteReadiness, [false]);

    const reconnectFailureState = [];
    const reconnectFailureReadiness = [];
    const reconnectFailure = await publishServiceEndpoint({
      port: 54342,
      backendPort: 8080,
      serviceBaseUrl: 'http://127.0.0.1:54342',
    }, {
      manager: {
        serviceBaseUrl: 'http://127.0.0.1:54342',
        desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': 'd'.repeat(64) }),
      },
      desktopState: { save: (next) => reconnectFailureState.push(next) },
      showWindow: true,
      createMainWindow: () => ({ loadURL: async () => { throw new Error('rotated endpoint temporarily unavailable'); } }),
      setServiceReady: (value) => reconnectFailureReadiness.push(value),
    });
    assert.deepEqual(reconnectFailure, {
      published: true,
      reconnected: false,
      error: {
        code: 'endpoint_reconnect_failed',
        message: '本机服务已应用新端点，但主界面未能重新连接。',
        action: '请从“服务”菜单重新连接，或运行诊断与修复。',
      },
      port: 54342,
    });
    assert.deepEqual(reconnectFailureState, [{ localPort: 54342 }]);
    assert.deepEqual(reconnectFailureReadiness, [true]);

    const savedCredentials = [];
    const stateWrites = [];
    let startCalls = 0;
    let navigationCalls = 0;
    const result = await saveCredentialsAndReconnect({ appleEmail: 'new@example.test' }, {
      manager: {
        serviceBaseUrl: 'http://127.0.0.1:54341',
        activeJobs: async () => [{ id: 'still-running' }],
        start: async () => { startCalls += 1; },
      },
      credentialStore: {
        load: () => ({ appleEmail: 'old@example.test', applePassword: 'secret-sentinel' }),
        save: (next) => savedCredentials.push(next),
      },
      desktopState: { save: (next) => stateWrites.push(next) },
      isServiceReady: () => true,
      setServiceReady: () => { throw new Error('active jobs must not change readiness'); },
      createMainWindow: () => { navigationCalls += 1; },
    });
    assert.deepEqual(result, {
      ok: true,
      saved: true,
      applied: false,
      pending: true,
      reconnected: false,
    });
    assert.equal(startCalls, 0);
    assert.equal(navigationCalls, 0);
    assert.deepEqual(stateWrites, [{ pendingCredentialDelivery: true }]);
    assert.deepEqual(savedCredentials, [{ appleEmail: 'new@example.test', applePassword: 'secret-sentinel' }]);

    const rotationStateWrites = [];
    const rotationStarts = [];
    const rotationNavigation = [];
    let resolveRotationNavigation;
    let rotationNavigationStarted;
    const rotationStarted = new Promise((resolve) => { rotationNavigationStarted = resolve; });
    const rotationBarrier = new Promise((resolve) => { resolveRotationNavigation = resolve; });
    const rotatingManager = {
      serviceBaseUrl: 'http://127.0.0.1:54341',
      desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': 'e'.repeat(64) }),
      activeJobs: async () => [],
      start: async (next) => {
        rotationStarts.push(next);
        rotatingManager.serviceBaseUrl = 'http://127.0.0.1:54342';
        return {
          port: 54342,
          backendPort: 8080,
          serviceBaseUrl: 'http://127.0.0.1:54342',
        };
      },
    };
    let rotationSettled = false;
    const rotation = saveCredentialsAndReconnect({ appleEmail: 'new@example.test' }, {
      manager: rotatingManager,
      credentialStore: {
        load: () => ({ appleEmail: 'old@example.test', applePassword: 'secret-sentinel' }),
        save() {},
      },
      desktopState: { save: (next) => rotationStateWrites.push(next) },
      isServiceReady: () => true,
      setServiceReady() {},
      createMainWindow: () => ({
        loadURL: async (...args) => {
          rotationNavigation.push(args);
          rotationNavigationStarted();
          await rotationBarrier;
        },
      }),
      showStartupError: async () => { throw new Error('a healthy rotated endpoint must not show an error page'); },
    }).then((value) => {
      rotationSettled = true;
      return value;
    });
    await rotationStarted;
    assert.equal(rotationSettled, false);
    assert.deepEqual(rotationStarts, [{ appleEmail: 'new@example.test', applePassword: 'secret-sentinel' }]);
    assert.deepEqual(rotationStateWrites, [{ pendingCredentialDelivery: false, localPort: 54342 }]);
    assert.deepEqual(rotationNavigation, [[
      'http://127.0.0.1:54342',
      { extraHeaders: `X-AMDL-Desktop-Token: ${'e'.repeat(64)}\n` },
    ]]);
    resolveRotationNavigation();
    assert.deepEqual(await rotation, {
      ok: true,
      saved: true,
      applied: true,
      pending: false,
      reconnected: true,
    });

    const retainedStateWrites = [];
    const retainedReadiness = [];
    let healthChecks = 0;
    await assert.rejects(
      saveCredentialsAndReconnect({ appleEmail: 'new@example.test' }, {
        manager: {
          serviceBaseUrl: 'http://127.0.0.1:54341',
          activeJobs: async () => [],
          start: async () => { throw Object.assign(new Error('restart failed'), { code: 'restart_failed' }); },
          status: async () => ({
            port: 54341,
            backendPort: 8080,
            version: '1.2.9',
            serviceBaseUrl: 'http://127.0.0.1:54341',
          }),
          verifyLocalEndpointHealth: async (version, url) => {
            healthChecks += 1;
            assert.equal(version, '1.2.9');
            assert.equal(url, 'http://127.0.0.1:54341');
          },
        },
        credentialStore: { load: () => ({}), save() {} },
        desktopState: { save: (next) => retainedStateWrites.push(next) },
        isServiceReady: () => true,
        setServiceReady: (value) => retainedReadiness.push(value),
      }),
      /restart failed/,
    );
    assert.equal(healthChecks, 0);
    assert.deepEqual(retainedStateWrites, [{ localPort: null }]);
    assert.deepEqual(retainedReadiness, [false]);

    const invalidatedStateWrites = [];
    const invalidatedReadiness = [];
    await assert.rejects(
      saveCredentialsAndReconnect({ appleEmail: 'new@example.test' }, {
        manager: {
          serviceBaseUrl: '',
          activeJobs: async () => [],
          start: async () => { throw Object.assign(new Error('restart failed'), { code: 'restart_failed' }); },
          status: async () => ({ port: 0, backendPort: 8080, serviceBaseUrl: '' }),
          verifyLocalEndpointHealth: async () => { throw new Error('must not verify an invalid endpoint'); },
        },
        credentialStore: { load: () => ({}), save() {} },
        desktopState: { save: (next) => invalidatedStateWrites.push(next) },
        isServiceReady: () => true,
        setServiceReady: (value) => invalidatedReadiness.push(value),
      }),
      /restart failed/,
    );
    assert.deepEqual(invalidatedStateWrites, [{ localPort: null }]);
    assert.deepEqual(invalidatedReadiness, [false]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('main service operations serialize every queued caller; nested lifecycle work uses named unlocked helpers', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: {
            requestSingleInstanceLock: () => true,
            whenReady: () => new Promise(() => {}),
            on() {},
            quit() {},
          },
          BrowserWindow: { getAllWindows: () => [] },
          Menu: {},
          dialog: {},
          ipcMain: {},
          safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    const { createServiceOperationQueue } = require(mainModulePath);
    const queue = createServiceOperationQueue();
    const events = [];
    let releaseFirst;
    let firstEntered;
    const firstEnteredPromise = new Promise((resolve) => { firstEntered = resolve; });
    const firstBarrier = new Promise((resolve) => { releaseFirst = resolve; });
    let nested;
    const first = queue.run(async () => {
      events.push('first:start');
      firstEntered();
      nested = queue.run(async () => { events.push('first:nested'); });
      await firstBarrier;
      events.push('first:end');
    });
    const second = queue.run(async () => { events.push('second'); });

    await firstEnteredPromise;
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(events, ['first:start']);
    releaseFirst();
    await Promise.all([first, second, nested]);
    assert.deepEqual(events, ['first:start', 'first:end', 'second', 'first:nested']);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('normal startup does not read stored credentials when network preflight fails', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: { requestSingleInstanceLock: () => true, whenReady: () => new Promise(() => {}), on() {}, quit() {} },
          BrowserWindow: { getAllWindows: () => [] }, Menu: {}, dialog: {}, ipcMain: {}, safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };
    const { startManagerWithDeferredCredentials } = require(mainModulePath);
    let credentialLoads = 0;
    const stateWrites = [];
    const preflightFailure = Object.assign(new Error('fixed preflight failure'), {
      code: 'network_preflight_failed',
      details: { stage: 'tls' },
    });
    const targetManager = {
      start: async (credentials, options) => {
        assert.equal(credentials, undefined);
        assert.equal(options.expectedVersion, '1.2.14');
        assert.equal(typeof options.credentialProvider, 'function');
        assert.equal(options.reuseRunning, true);
        throw preflightFailure;
      },
    };

    await assert.rejects(
      startManagerWithDeferredCredentials(targetManager, {
        credentialStore: {
          load: () => { credentialLoads += 1; return { applePassword: 'must-not-be-read' }; },
        },
        stateStore: { save: (next) => stateWrites.push(next) },
        expectedVersion: '1.2.14',
      }),
      (error) => error === preflightFailure,
    );

    assert.equal(credentialLoads, 0);
    assert.deepEqual(stateWrites, []);

    for (const pendingCredentialDelivery of [false, true]) {
      const status = { state: 'running' };
      const result = await startManagerWithDeferredCredentials({
        start: async (credentials, options) => {
          assert.equal(credentials, undefined);
          assert.equal(options.reuseRunning, !pendingCredentialDelivery);
          if (pendingCredentialDelivery) await options.credentialProvider();
          return status;
        },
      }, {
        credentialStore: { load: () => { credentialLoads += 1; return {}; } },
        stateStore: {
          load: () => ({ pendingCredentialDelivery }),
          save: (next) => stateWrites.push(next),
        },
      });
      assert.equal(result, status);
    }
    assert.equal(credentialLoads, 1, 'only pending changes resolve credentials');
    assert.deepEqual(stateWrites, [{ pendingCredentialDelivery: false }, { pendingCredentialDelivery: false }]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('credential restart holds the manager lifecycle lock through endpoint-state publication before navigating', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: { requestSingleInstanceLock: () => true, whenReady: () => new Promise(() => {}), on() {}, quit() {} },
          BrowserWindow: { getAllWindows: () => [] }, Menu: {}, dialog: {}, ipcMain: {}, safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };
    const { saveCredentialsAndReconnect } = require(mainModulePath);
    const events = [];
    const manager = {
      serviceBaseUrl: 'http://127.0.0.1:54341',
      desktopProxyRequestHeaders: () => ({ 'X-AMDL-Desktop-Token': 'e'.repeat(64) }),
      activeJobs: async () => [],
      withLifecycleLock: async (operation) => {
        events.push('lock:start');
        try { return await operation(); } finally { events.push('lock:end'); }
      },
      start: async () => {
        events.push('start');
        manager.serviceBaseUrl = 'http://127.0.0.1:54342';
        return { port: 54342, backendPort: 8080, serviceBaseUrl: manager.serviceBaseUrl, version: '1.2.10' };
      },
    };
    const result = await saveCredentialsAndReconnect({ appleEmail: 'new@example.test' }, {
      manager,
      credentialStore: { load: () => ({}), save: () => events.push('credential:save') },
      desktopState: { save: () => events.push('desktop:save') },
      isServiceReady: () => true,
      setServiceReady: () => events.push('ready'),
      createMainWindow: () => ({}),
      loadManagerService: async () => { events.push('navigate'); },
      showStartupError: async () => { throw new Error('unexpected error page'); },
    });

    assert.equal(result.reconnected, true);
    assert.deepEqual(events, [
      'credential:save', 'lock:start', 'start', 'desktop:save', 'ready', 'lock:end', 'navigate',
    ]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

test('runtime diagnostics require the current published endpoint and derive update state from the appliance state', async () => {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  try {
    delete require.cache[mainModulePath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          app: { requestSingleInstanceLock: () => true, whenReady: () => new Promise(() => {}), on() {}, quit() {} },
          BrowserWindow: { getAllWindows: () => [] }, Menu: {}, dialog: {}, ipcMain: {}, safeStorage: {},
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };
    const { safeRuntimeDiagnostics } = require(mainModulePath);
    const endpoint = {
      serviceBaseUrl: 'http://127.0.0.1:54342',
      version: '1.2.10',
      expectedVersion: '1.2.10',
    };
    const events = [];
    const currentManager = {
      serviceBaseUrl: endpoint.serviceBaseUrl,
      withLifecycleLock: async (operation) => {
        events.push('lock:start');
        try { return await operation(); } finally { events.push('lock:end'); }
      },
      readState: async () => ({ installedVersion: '1.2.9', pendingRuntimeVersion: '1.2.10' }),
      status: async () => ({
        port: 54342,
        backendPort: 8080,
        serviceBaseUrl: endpoint.serviceBaseUrl,
        version: '1.2.10',
      }),
      verifyLocalEndpointHealth: async (version, url) => {
        events.push(`health:${version}:${url}`);
      },
      diagnostics: async (secrets) => {
        events.push('diagnostics');
        assert.deepEqual(secrets, ['diagnostic-secret-sentinel']);
        return 'current-runtime-diagnostics';
      },
    };
    endpoint.manager = currentManager;

    assert.equal(await safeRuntimeDiagnostics({
      manager: currentManager,
      expectedVersion: '1.2.10',
      serviceReady: true,
      publishedEndpoint: endpoint,
      diagnosticSecrets: ['diagnostic-secret-sentinel'],
    }), 'current-runtime-diagnostics');
    assert.deepEqual(events, [
      'lock:start',
      'health:1.2.10:http://127.0.0.1:54342',
      'diagnostics',
      'lock:end',
    ]);

    let staleDiagnostics = 0;
    const staleManager = {
      ...currentManager,
      serviceBaseUrl: 'http://127.0.0.1:54343',
      diagnostics: async () => { staleDiagnostics += 1; return 'diagnostic-secret-sentinel'; },
    };
    const staleEndpoint = { ...endpoint, manager: staleManager };
    const staleResult = await safeRuntimeDiagnostics({
      manager: staleManager,
      expectedVersion: '1.2.10',
      serviceReady: true,
      publishedEndpoint: staleEndpoint,
      diagnosticSecrets: ['diagnostic-secret-sentinel'],
    });
    assert.equal(staleDiagnostics, 0);
    assert.match(staleResult, /installed_version=1\.2\.9/);
    assert.match(staleResult, /pending_runtime_version=1\.2\.10/);
    assert.match(staleResult, /endpoint_authorized=false/);
    assert.doesNotMatch(staleResult, /diagnostic-secret-sentinel/);

    let racedDiagnostics = 0;
    let endpointReplaced = false;
    const racedManager = {
      ...currentManager,
      serviceBaseUrl: endpoint.serviceBaseUrl,
      verifyLocalEndpointHealth: async () => { endpointReplaced = true; },
      diagnostics: async () => { racedDiagnostics += 1; return 'diagnostic-secret-sentinel'; },
    };
    const racedEndpoint = { ...endpoint, manager: racedManager };
    const replacementEndpoint = { ...racedEndpoint };
    const racedOptions = {
      manager: racedManager,
      expectedVersion: '1.2.10',
      serviceReady: true,
      diagnosticSecrets: ['diagnostic-secret-sentinel'],
    };
    Object.defineProperty(racedOptions, 'publishedEndpoint', {
      enumerable: true,
      get: () => (endpointReplaced ? replacementEndpoint : racedEndpoint),
    });
    const racedResult = await safeRuntimeDiagnostics(racedOptions);
    assert.equal(racedDiagnostics, 0);
    assert.match(racedResult, /endpoint_authorized=false/);
    assert.doesNotMatch(racedResult, /diagnostic-secret-sentinel/);
  } finally {
    Module._load = originalLoad;
    delete require.cache[mainModulePath];
    if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
  }
});

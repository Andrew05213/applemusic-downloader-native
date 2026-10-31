'use strict';

const { app, BrowserWindow, WebContentsView, Menu, dialog, ipcMain, safeStorage, components } = require('electron');

const APP_NAME = 'AppleMusicDownloader';
// Desktop/runtime identity is a semantic v1 version.  Keep this gate in
// lockstep with the Appliance manifest validator so a new minor release can
// be discovered and diagnosed before any WSL state transition.
const PRODUCT_VERSION_PATTERN = /^1\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/;
// The lock must be the first Electron state transition. In particular, a
// losing process must not resolve user-data paths or initialize WSL state.
const SINGLE_INSTANCE_ACQUIRED = typeof app.requestSingleInstanceLock !== 'function'
  || app.requestSingleInstanceLock();

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { CredentialStore } = require('../../electron/credentials');
const { createOfficialWebPlayer } = require('../../electron/official-web-player');
const { DesktopStateStore } = require('../../electron/desktop-state');
const { loadApplianceManifest } = require('../../electron/release-manifest');
const { SetupController, publicNetworkPreflightDetails } = require('../../electron/setup-controller');
const { createAppleMusicBrowser } = require('../../electron/apple-music-browser');
const { exportDiagnosticReport } = require('../../electron/diagnostic-export');
const { WindowsCapabilities, defaultWindowsHelperPath } = require('../../electron/windows-capabilities');
const { DESKTOP_PROXY_HEADER, WslManager, WslManagerError } = require('../../electron/wsl-manager');
const {
  loadInteropRecoveryDescriptorWithIdentity,
  requiresInteropRecoveryBinding,
} = require('../../electron/wslinterop-recovery');

const REPOSITORY_ROOT = path.resolve(__dirname, '..', '..');
const SETUP_PAGE_URL = pathToFileURL(path.join(__dirname, 'setup.html')).href;
const CREDENTIALS_PAGE_URL = pathToFileURL(path.join(__dirname, 'credentials.html')).href;
const OFFLINE_SHELL_URL = pathToFileURL(path.join(__dirname, '..', 'renderer', 'index.html')).href;
const ERROR_REPAIR_URL = 'https://applemusic-downloader.invalid/repair-runtime';
const AUTOSTART = process.argv.includes('--autostart');
const RESUME_SETUP = process.argv.includes('--resume-setup');

function enabledLoginItemSettings(args) {
  return { openAtLogin: true, name: APP_NAME, args };
}

let mainWindow = null;
let setupWindow = null;
let credentialsWindow = null;
let appleMusicBrowser = null;
let officialWebPlayer = null;
let manager = null;
let credentials = null;
let desktopState = null;
let setupController = null;
let windowsCapabilities = null;
let serviceReady = false;
let handlersInstalled = false;
let internalErrorURL = '';
let activeErrorPageURL = '';
let appDataDir = '';
let defaultDownloadDir = '';
let foregroundRequested = !AUTOSTART || RESUME_SETUP;
let initialLaunchPromise = null;
let desktopProxyWebRequestBinding = null;
let repairOperationPromise = null;
let publishedServiceEndpoint = null;
let packagedReleaseIdentity = null;
const serviceOperations = createServiceOperationQueue();

function initializeApplicationPaths() {
  if (appDataDir && defaultDownloadDir) return;
  appDataDir = path.join(process.env.LOCALAPPDATA || app.getPath('userData'), APP_NAME);
  defaultDownloadDir = path.join(app.getPath('music'), 'Apple Music Downloader');
}

function packagedProductVersion() {
  try {
    if (typeof app.getVersion === 'function') {
      const version = app.getVersion();
      if (typeof version === 'string' && PRODUCT_VERSION_PATTERN.test(version)) return version;
    }
  } catch { /* Fall through to the immutable package metadata. */ }
  try {
    const packageDocument = JSON.parse(fs.readFileSync(path.join(REPOSITORY_ROOT, 'package.json'), 'utf8'));
    if (typeof packageDocument.version === 'string' && PRODUCT_VERSION_PATTERN.test(packageDocument.version)) {
      return packageDocument.version;
    }
  } catch { /* The caller will fail closed without a release identity. */ }
  return '';
}

function loadPackagedReleaseIdentity(options = {}) {
  const expectedVersion = options.expectedVersion || packagedProductVersion();
  if (!expectedVersion) throw endpointPublicationException();
  const packaged = app.isPackaged === true;
  const manifest = options.manifest || loadApplianceManifest({
    explicitPath: packaged ? undefined : process.env.AMDL_APPLIANCE_MANIFEST,
    packaged,
    resourcesPath: process.resourcesPath,
    repositoryRoot: REPOSITORY_ROOT,
    expectedVersion,
  });
  if (!manifest || manifest.version !== expectedVersion) throw endpointPublicationException();
  let interopRecoveryIdentity = options.interopRecoveryIdentity || null;
  let interopRecovery = options.interopRecovery || interopRecoveryIdentity?.descriptor || null;
  if (!interopRecovery && requiresInteropRecoveryBinding(expectedVersion)) {
    interopRecoveryIdentity = loadInteropRecoveryDescriptorWithIdentity({
      expectedVersion,
      resourcesPath: process.resourcesPath,
      repositoryRoot: REPOSITORY_ROOT,
      packaged,
    });
    interopRecovery = interopRecoveryIdentity.descriptor;
  }
  const identity = Object.freeze({ version: manifest.version, manifest, interopRecovery, interopRecoveryIdentity });
  packagedReleaseIdentity = identity;
  return identity;
}

function escapeHtml(value) {
  return String(value || '').replace(/[&<>"']/g, (character) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[character]));
}

function errorDocument(error, diagnostic = '') {
  const message = escapeHtml(error?.message || error || '未知错误');
  const action = escapeHtml(error?.action || '请打开“服务 → 诊断与修复”后重试。');
  const code = escapeHtml(error?.code || 'startup_failed');
  const stage = error?.code === 'network_preflight_failed'
    ? publicNetworkPreflightDetails(error)?.stage || 'unknown'
    : '';
  const log = escapeHtml([diagnostic, stage ? `network_preflight_stage=${stage}` : ''].filter(Boolean).join('\n'));
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>Apple Music Downloader</title>
    <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">
    <style>body{font-family:"Segoe UI Variable","Microsoft YaHei UI",system-ui,sans-serif;max-width:780px;margin:8vh auto;padding:2rem;color:#172033;background:#f6f7fb}main{background:#fff;border:1px solid #dde2ec;border-radius:18px;padding:28px;box-shadow:0 18px 55px #25305214}h1{margin-top:0}code{color:#6941c6}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:48vh;overflow:auto;background:#f3f5f8;padding:1rem;border-radius:10px;color:#354158}.action{font-weight:650;color:#9b341d}.repair-link{display:inline-block;margin-top:1rem;padding:.7rem 1rem;border-radius:9px;background:#e42b58;color:#fff;text-decoration:none;font-weight:650}</style>
    </head><body><main><h1>本机服务未能启动</h1><p><code>${code}</code>：${message}</p><p class="action">${action}</p><p><a class="repair-link" href="${ERROR_REPAIR_URL}">打开诊断与修复</a></p>${log ? `<h2>最近诊断（已脱敏）</h2><pre>${log}</pre>` : ''}</main></body></html>`;
}

function isManagerServiceRequest(candidate, targetManager) {
  if (!targetManager?.serviceBaseUrl) return false;
  try {
    const allowed = new URL(targetManager.serviceBaseUrl);
    const actual = new URL(candidate);
    return allowed.protocol === 'http:'
      && allowed.hostname === '127.0.0.1'
      && allowed.username === ''
      && allowed.password === ''
      && (allowed.pathname === '' || allowed.pathname === '/')
      && allowed.search === ''
      && allowed.hash === ''
      && actual.protocol === 'http:'
      && actual.hostname === '127.0.0.1'
      && actual.username === ''
      && actual.password === ''
      && actual.origin === allowed.origin;
  } catch {
    return false;
  }
}

function isPublishedManagerServiceRequest(candidate, targetManager) {
  if (!publishedServiceEndpoint
      || publishedServiceEndpoint.manager !== targetManager
      || !isManagerServiceRequest(candidate, targetManager)) return false;
  try {
    return new URL(candidate).origin === publishedServiceEndpoint.serviceBaseUrl
      && publishedServiceEndpoint.version === publishedServiceEndpoint.expectedVersion;
  } catch {
    return false;
  }
}

function shouldRequirePublishedEndpoint(targetManager, options = {}) {
  return options.requirePublished === true || targetManager === manager;
}

function isArmedErrorRepairNavigation(candidate, currentURL, armedErrorURL) {
  return candidate === ERROR_REPAIR_URL
    && typeof armedErrorURL === 'string'
    && armedErrorURL.length > 0
    && currentURL === armedErrorURL;
}

function isManagerServiceRootRequest(candidate, targetManager, options = {}) {
  const requestCheck = shouldRequirePublishedEndpoint(targetManager, options)
    ? isPublishedManagerServiceRequest
    : isManagerServiceRequest;
  if (!requestCheck(candidate, targetManager)) return false;
  try {
    const actual = new URL(candidate);
    return actual.pathname === '/' && actual.search === '' && actual.hash === '';
  } catch {
    return false;
  }
}

function isManagerDownloadRequest(candidate, targetManager, options = {}) {
  const requestCheck = shouldRequirePublishedEndpoint(targetManager, options)
    ? isPublishedManagerServiceRequest
    : isManagerServiceRequest;
  if (!requestCheck(candidate, targetManager)) return false;
  try {
    const actual = new URL(candidate);
    if (actual.search !== '' || actual.hash !== '') return false;
    return /^\/api\/v1\/files\/[a-f0-9]{32}$/i.test(actual.pathname)
      || /^\/api\/v1\/jobs\/[a-f0-9]{32}\/archive$/i.test(actual.pathname);
  } catch {
    return false;
  }
}

function desktopProxyToken(targetManager) {
  const supplied = targetManager?.desktopProxyRequestHeaders?.();
  for (const [name, value] of Object.entries(supplied || {})) {
    if (name.toLowerCase() === DESKTOP_PROXY_HEADER.toLowerCase()
        && typeof value === 'string' && /^[a-f0-9]{64}$/.test(value)) return value;
  }
  return '';
}

function managerNavigationOptions(targetManager) {
  const token = desktopProxyToken(targetManager);
  if (!token) throw new Error('desktop proxy token is unavailable');
  // Electron supplies this only on the main-process initiated top-level load.
  // The webRequest hook below validates the same in-memory value, exact owner
  // main frame, and exact loopback root before preserving it.
  return { extraHeaders: `${DESKTOP_PROXY_HEADER}: ${token}\n` };
}

function publicEndpointPublicationError() {
  return {
    code: 'endpoint_publication_failed',
    message: '本机服务端点未能安全发布。',
    action: '请从“服务”菜单重新连接，或运行诊断与修复。',
  };
}

function publicEndpointReconnectError() {
  return {
    code: 'endpoint_reconnect_failed',
    message: '本机服务已应用新端点，但主界面未能重新连接。',
    action: '请从“服务”菜单重新连接，或运行诊断与修复。',
  };
}

function endpointPublicationException(descriptor = publicEndpointPublicationError()) {
  return Object.assign(new Error(descriptor.message), descriptor);
}

function validEndpointPort(value) {
  return Number.isInteger(value) && value >= 1 && value <= 65535;
}

function expectedServiceBaseUrl(port) {
  return `http://127.0.0.1:${port}`;
}

function validateServiceEndpoint(status, targetManager, expectedVersion = '') {
  const port = status?.port;
  const backendPort = status?.backendPort;
  const expected = validEndpointPort(port) ? expectedServiceBaseUrl(port) : '';
  const releaseVersion = expectedVersion || (targetManager === manager ? packagedReleaseIdentity?.version || '' : '');
  if (!expected
      || !validEndpointPort(backendPort)
      || port === backendPort
      || status?.serviceBaseUrl !== expected
      || targetManager?.serviceBaseUrl !== expected
      || (targetManager === manager && !releaseVersion)
      || (releaseVersion && status?.version !== releaseVersion)) {
    throw endpointPublicationException();
  }
  return { port, backendPort, serviceBaseUrl: expected, version: status?.version || releaseVersion };
}

function liveServiceRunMatches(candidate, live) {
  if (!candidate || typeof candidate !== 'object' || !live || typeof live !== 'object') return false;
  if (!['running', 'starting'].includes(candidate.state)) return false;
  if (!['running', 'starting'].includes(live.state)) return false;
  for (const field of ['version', 'pid', 'supervisorPid']) {
    const expected = candidate[field];
    if (expected === undefined || expected === null || expected === '' || expected === 0) continue;
    if (live[field] !== expected) return false;
  }
  for (const field of ['port', 'backendPort', 'serviceBaseUrl']) {
    if (candidate[field] !== undefined && candidate[field] !== null
        && candidate[field] !== '' && live[field] !== candidate[field]) return false;
  }
  const candidateNetwork = candidate.network;
  const liveNetwork = live.network;
  if (candidateNetwork && typeof candidateNetwork === 'object') {
    if (!liveNetwork || typeof liveNetwork !== 'object') return false;
    if (!candidateNetwork.generationId
        || liveNetwork.generationId !== candidateNetwork.generationId) return false;
    for (const field of ['schemaVersion', 'path', 'windowsPath', 'wslDirectPath']) {
      if (candidateNetwork[field] !== undefined && liveNetwork[field] !== candidateNetwork[field]) return false;
    }
    if (liveNetwork.state !== 'ready'
        || liveNetwork.stage !== 'complete'
        || liveNetwork.proxyPropagated !== true) return false;
  }
  return true;
}

async function verifyLiveServiceRun(candidate, targetManager, options = {}) {
  if (options.requireLiveStatus !== true) return;
  // The object returned by start/update must itself be a live-shaped status;
  // otherwise a later status call could accidentally bless a stale endpoint.
  if (!liveServiceRunMatches(candidate, candidate)) {
    throw endpointPublicationException();
  }
  if (!targetManager || typeof targetManager.status !== 'function') {
    throw endpointPublicationException();
  }
  let live;
  try {
    live = await targetManager.status();
  } catch {
    throw endpointPublicationException();
  }
  if (!liveServiceRunMatches(candidate, live)) throw endpointPublicationException();
}

function createServiceOperationQueue() {
  let tail = Promise.resolve();
  return {
    run(operation) {
      if (typeof operation !== 'function') return Promise.reject(new TypeError('Service operation must be a function'));
      // Public operations always join FIFO. Intentional nested lifecycle work
      // calls its named *Unlocked helper directly; inheriting an async context
      // here would let a future timer or callback bypass this boundary.
      const scheduled = tail.then(operation, operation);
      tail = scheduled.catch(() => {});
      return scheduled;
    },
  };
}

function runServiceOperation(operation) {
  return serviceOperations.run(operation);
}

async function withManagerLifecycle(targetManager, operation) {
  if (typeof operation !== 'function') throw new TypeError('Lifecycle operation must be a function');
  if (typeof targetManager?.withLifecycleLock === 'function') {
    return targetManager.withLifecycleLock(operation);
  }
  return operation();
}

function endpointDependencies(options = {}) {
  return {
    targetManager: options.manager === undefined ? manager : options.manager,
    stateStore: options.desktopState === undefined ? desktopState : options.desktopState,
    setReady: options.setServiceReady || ((value) => { serviceReady = value; }),
    createWindow: options.createMainWindow || createMainWindow,
    loadService: options.loadManagerService || loadManagerService,
  };
}

async function invalidateServiceEndpoint(options = {}) {
  const { targetManager, stateStore, setReady } = endpointDependencies(options);
  if (!targetManager || !publishedServiceEndpoint || publishedServiceEndpoint.manager === targetManager) {
    publishedServiceEndpoint = null;
  }
  setReady(false);
  try {
    if (!stateStore || typeof stateStore.save !== 'function') return false;
    stateStore.save({ ...(options.state || {}), localPort: null });
    return true;
  } catch {
    return false;
  }
}

async function reconnectPublishedServiceEndpoint(publication, options = {}) {
  if (!publication?.published || !options.showWindow) return publication;
  const { targetManager, createWindow, loadService } = endpointDependencies(options);
  try {
    await loadService(createWindow(), targetManager);
    return { ...publication, reconnected: true, error: null };
  } catch {
    // The verified new endpoint remains authoritative. Never restore or retry
    // the old BrowserWindow origin after a proxy-generation rotation.
    return { ...publication, reconnected: false, error: publicEndpointReconnectError() };
  }
}

async function publishServiceEndpoint(status, options = {}) {
  const {
    targetManager, stateStore, setReady, createWindow, loadService,
  } = endpointDependencies(options);
  let endpoint;
  try {
    endpoint = validateServiceEndpoint(status, targetManager, options.expectedVersion || '');
  } catch {
    await invalidateServiceEndpoint({
      desktopState: stateStore,
      manager: targetManager,
      setServiceReady: setReady,
      state: options.invalidState,
    });
    return {
      published: false,
      reconnected: false,
      error: publicEndpointPublicationError(),
      port: null,
    };
  }

  try {
    await verifyLiveServiceRun(status, targetManager, options);
  } catch {
    await invalidateServiceEndpoint({
      desktopState: stateStore,
      manager: targetManager,
      setServiceReady: setReady,
      state: options.invalidState,
    });
    return {
      published: false,
      reconnected: false,
      error: publicEndpointPublicationError(),
      port: null,
    };
  }

  try {
    if (!stateStore || typeof stateStore.save !== 'function') throw new Error('desktop state store is unavailable');
    stateStore.save({ ...(options.state || {}), localPort: endpoint.port });
  } catch {
    await invalidateServiceEndpoint({ manager: targetManager, desktopState: stateStore, setServiceReady: setReady });
    return {
      published: false,
      reconnected: false,
      error: publicEndpointPublicationError(),
      port: null,
    };
  }
  publishedServiceEndpoint = {
    manager: targetManager,
    serviceBaseUrl: endpoint.serviceBaseUrl,
    version: endpoint.version,
    expectedVersion: options.expectedVersion || endpoint.version,
  };
  setReady(true);
  return reconnectPublishedServiceEndpoint({
    published: true,
    reconnected: false,
    error: null,
    port: endpoint.port,
  }, options);
}

function diagnosticSecretValues(document = {}) {
  return ['appleEmail', 'applePassword', 'mediaUserToken', 'authorizationToken']
    .map((key) => document?.[key])
    .filter((value) => typeof value === 'string' && value.length > 0);
}

function allowedServiceURL(candidate) {
  return isPublishedManagerServiceRequest(candidate, manager);
}

function desktopRequestBelongsToWebContents(details, expectedWebContents, targetManager, options = {}) {
  const expectedId = expectedWebContents?.id;
  if (!Number.isInteger(expectedId)) return false;
  const hasWebContentsId = Object.prototype.hasOwnProperty.call(details || {}, 'webContentsId');
  if (hasWebContentsId && (!Number.isInteger(details.webContentsId) || details.webContentsId !== expectedId)) return false;
  if (!hasWebContentsId && details?.webContents !== expectedWebContents) return false;
  if (details?.webContents != null && details.webContents !== expectedWebContents) return false;
  if (details?.resourceType === 'mainFrame' || details?.resourceType === 'subFrame') return false;
  const ownerMainFrame = expectedWebContents?.mainFrame;
  if (!ownerMainFrame || !Object.prototype.hasOwnProperty.call(details || {}, 'frame')) return false;
  if (!details.frame || details.frame !== ownerMainFrame) return false;
  const requestCheck = shouldRequirePublishedEndpoint(targetManager, options)
    ? isPublishedManagerServiceRequest
    : isManagerServiceRequest;
  if (!requestCheck(details.frame.url, targetManager)) return false;
  let currentURL;
  try {
    currentURL = typeof expectedWebContents.getURL === 'function'
      ? expectedWebContents.getURL()
      : ownerMainFrame.url;
  } catch {
    return false;
  }
  if (!requestCheck(currentURL, targetManager)) return false;
  return true;
}

function desktopMainFrameNavigationBelongsToWebContents(details, expectedWebContents, targetManager, options = {}) {
  const expectedId = expectedWebContents?.id;
  if (!Number.isInteger(expectedId) || details?.resourceType !== 'mainFrame') return false;
  const hasWebContentsId = Object.prototype.hasOwnProperty.call(details || {}, 'webContentsId');
  if (hasWebContentsId && (!Number.isInteger(details.webContentsId) || details.webContentsId !== expectedId)) return false;
  if (!hasWebContentsId && details?.webContents !== expectedWebContents) return false;
  if (details?.webContents != null && details.webContents !== expectedWebContents) return false;
  const ownerMainFrame = expectedWebContents?.mainFrame;
  if (!ownerMainFrame || !Object.prototype.hasOwnProperty.call(details || {}, 'frame')) return false;
  if (!details.frame || details.frame !== ownerMainFrame) return false;
  return isManagerServiceRootRequest(details?.url, targetManager, options);
}

function desktopDownloadNavigationBelongsToWebContents(details, expectedWebContents, targetManager, options = {}) {
  const expectedId = expectedWebContents?.id;
  if (!Number.isInteger(expectedId) || details?.resourceType !== 'mainFrame' || details?.method !== 'GET') return false;
  const hasWebContentsId = Object.prototype.hasOwnProperty.call(details || {}, 'webContentsId');
  if (hasWebContentsId && (!Number.isInteger(details.webContentsId) || details.webContentsId !== expectedId)) return false;
  if (!hasWebContentsId && details?.webContents !== expectedWebContents) return false;
  if (details?.webContents != null && details.webContents !== expectedWebContents) return false;
  const ownerMainFrame = expectedWebContents?.mainFrame;
  if (!ownerMainFrame || !Object.prototype.hasOwnProperty.call(details || {}, 'frame')) return false;
  const requestCheck = shouldRequirePublishedEndpoint(targetManager, options)
    ? isPublishedManagerServiceRequest
    : isManagerServiceRequest;
  if (!details.frame || details.frame !== ownerMainFrame || !requestCheck(details.frame.url, targetManager)) return false;
  let currentURL;
  try {
    currentURL = typeof expectedWebContents.getURL === 'function'
      ? expectedWebContents.getURL()
      : ownerMainFrame.url;
  } catch {
    return false;
  }
  return requestCheck(currentURL, targetManager)
    && isManagerDownloadRequest(details?.url, targetManager, options);
}

function desktopNetworkRequestHeaders(details, targetManager, expectedWebContents, options = {}) {
  const requestHeaders = {};
  let providedDesktopToken = '';
  for (const [name, value] of Object.entries(details?.requestHeaders || {})) {
    if (name.toLowerCase() === DESKTOP_PROXY_HEADER.toLowerCase()) {
      providedDesktopToken = Array.isArray(value) ? value[0] : value;
    } else requestHeaders[name] = value;
  }
  const token = desktopProxyToken(targetManager);
  const trustedSubresource = desktopRequestBelongsToWebContents(details, expectedWebContents, targetManager, options);
  const trustedInitialNavigation = desktopMainFrameNavigationBelongsToWebContents(
    details, expectedWebContents, targetManager, options,
  ) && providedDesktopToken === token;
  const trustedDownloadNavigation = desktopDownloadNavigationBelongsToWebContents(
    details, expectedWebContents, targetManager, options,
  );
  const requestCheck = shouldRequirePublishedEndpoint(targetManager, options)
    ? isPublishedManagerServiceRequest
    : isManagerServiceRequest;
  if (!token || (!trustedSubresource && !trustedInitialNavigation && !trustedDownloadNavigation)
      || !requestCheck(details?.url, targetManager)) {
    return requestHeaders;
  }
  requestHeaders[DESKTOP_PROXY_HEADER] = token;
  return requestHeaders;
}

function installDesktopProxyRequestInterceptor(window, managerProvider = () => manager) {
  const targetWebContents = window?.webContents;
  const webRequest = targetWebContents?.session?.webRequest;
  const webContentsId = targetWebContents?.id;
  if (!webRequest?.onBeforeSendHeaders || !Number.isInteger(webContentsId)) return () => {};
  if (desktopProxyWebRequestBinding?.webRequest === webRequest
      && desktopProxyWebRequestBinding.webContents === targetWebContents) {
    return desktopProxyWebRequestBinding.dispose;
  }
  desktopProxyWebRequestBinding?.dispose();

  const listener = (details, callback) => {
    let requestHeaders;
    try {
      if (window?.isDestroyed?.()) throw new Error('desktop proxy window is destroyed');
      const targetManager = managerProvider();
      requestHeaders = desktopNetworkRequestHeaders(details, targetManager, targetWebContents, {
        requirePublished: targetManager === manager,
      });
    } catch {
      // Fail closed: preserve ordinary headers but remove any token inherited
      // across a redirect or supplied by an untrusted renderer request.
      requestHeaders = desktopNetworkRequestHeaders(details, null, targetWebContents, { requirePublished: true });
    }
    callback({ requestHeaders });
  };
  const binding = {
    webRequest,
    webContents: targetWebContents,
    webContentsId,
    disposed: false,
    dispose: null,
  };
  binding.dispose = () => {
    if (binding.disposed) return;
    binding.disposed = true;
    if (desktopProxyWebRequestBinding === binding) {
      webRequest.onBeforeSendHeaders(null);
      desktopProxyWebRequestBinding = null;
    }
  };
  webRequest.onBeforeSendHeaders({ urls: ['<all_urls>'] }, listener);
  desktopProxyWebRequestBinding = binding;
  return binding.dispose;
}

function focusExistingWindow() {
  const window = [mainWindow, setupWindow, credentialsWindow, ...(BrowserWindow.getAllWindows?.() || [])]
    .find((candidate) => candidate && !candidate.isDestroyed?.());
  if (!window) return false;
  if (window.isMinimized?.()) window.restore?.();
  window.show?.();
  window.focus?.();
  return true;
}

function isExactPrivilegedPageURL(candidate, expectedURL) {
  if (typeof candidate !== 'string' || typeof expectedURL !== 'string') return false;
  try {
    const actual = new URL(candidate);
    const expected = new URL(expectedURL);
    return candidate === expectedURL
      && expected.protocol === 'file:'
      && expected.host === ''
      && expected.search === ''
      && expected.hash === ''
      && actual.protocol === 'file:'
      && actual.host === ''
      && actual.search === ''
      && actual.hash === ''
      && actual.href === expected.href;
  } catch {
    return false;
  }
}

function isExactOfflineShellURL(candidate) {
  return typeof candidate === 'string'
    && ['#browse', '#tasks', '#settings'].some((hash) => candidate === OFFLINE_SHELL_URL + hash);
}

function publicOfflineServiceState(error) {
  if (error?.code !== 'network_preflight_failed') return { state: 'unavailable' };
  return {
    state: 'unavailable',
    code: 'network_preflight_failed',
    stage: publicNetworkPreflightDetails(error)?.stage || 'unknown',
  };
}

async function retainOfflineBrowse(error = null) {
  if (!mainWindow || mainWindow.isDestroyed?.()) return;
  if (!isExactOfflineShellURL(mainWindow.webContents?.getURL?.())) {
    await mainWindow.loadURL(OFFLINE_SHELL_URL + '#browse');
  }
  mainWindow.webContents.send?.('desktop:shell-state', publicOfflineServiceState(error));
}

function restrictWindowToPage(window, expectedURL) {
  window.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  const preventUnexpectedNavigation = (event, url) => {
    if (!isExactPrivilegedPageURL(url, expectedURL)) event.preventDefault();
  };
  window.webContents.on('will-navigate', preventUnexpectedNavigation);
  window.webContents.on('will-redirect', preventUnexpectedNavigation);
}

function handleSecondInstance(_event, argv = []) {
  // A duplicate login-item launch is expected during sign-in races and must
  // stay silent. A normal user launch promotes an in-flight autostart to a
  // foreground launch and focuses, rather than creating another manager.
  if (Array.isArray(argv) && argv.includes('--autostart') && !argv.includes('--resume-setup')) return;
  foregroundRequested = true;
  if (focusExistingWindow()) return;
  if (!app.isReady?.()) return;
  if (initialLaunchPromise && !serviceReady) {
    createMainWindow().loadURL(OFFLINE_SHELL_URL + '#browse').catch(showStartupError);
    return;
  }
  if (serviceReady && manager) {
    runServiceOperation(() => loadManagerService(createMainWindow())).catch(showStartupError);
    return;
  }
  if (!initialLaunchPromise) runInitialLaunch().catch(showStartupError);
}

function loadManagerService(window, targetManager = manager, options = {}) {
  if (!window?.loadURL || !targetManager?.serviceBaseUrl
      || (shouldRequirePublishedEndpoint(targetManager, options)
        && !isPublishedManagerServiceRequest(targetManager.serviceBaseUrl, targetManager))) {
    return Promise.reject(new Error('manager service endpoint is unavailable'));
  }
  activeErrorPageURL = '';
  return window.loadURL(targetManager.serviceBaseUrl, managerNavigationOptions(targetManager));
}

function installMainServiceNavigationGuard(window) {
  const preventUnexpectedNavigation = (event, url) => {
    let currentURL = '';
    try { currentURL = window.webContents.getURL?.() || ''; } catch { /* Treat an unreadable URL as untrusted. */ }
    if (isArmedErrorRepairNavigation(url, currentURL, activeErrorPageURL)) {
      event.preventDefault();
      repairRuntime({ confirm: true, showWindow: true })
        .catch((error) => showStartupError(publicRepairError(error)));
      return;
    }
    if (url !== internalErrorURL && !allowedServiceURL(url) && !isExactOfflineShellURL(url)) event.preventDefault();
  };
  window.webContents.on('will-navigate', preventUnexpectedNavigation);
  window.webContents.on('will-redirect', preventUnexpectedNavigation);
}

function createMainWindow() {
  if (mainWindow && !mainWindow.isDestroyed?.()) return mainWindow;
  mainWindow = new BrowserWindow({
    width: 1240,
    height: 840,
    minWidth: 900,
    minHeight: 640,
    show: false,
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      preload: path.join(__dirname, '../preload/preload.js'),
    },
  });
  const window = mainWindow;
  const disposeDesktopProxyInterceptor = installDesktopProxyRequestInterceptor(window);
  mainWindow.once('ready-to-show', () => mainWindow?.show());
  mainWindow.on?.('closed', () => {
    disposeDesktopProxyInterceptor();
    appleMusicBrowser?.destroy();
    officialWebPlayer?.destroy();
    officialWebPlayer = null;
    appleMusicBrowser = null;
    if (mainWindow === window) mainWindow = null;
  });
  mainWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  installMainServiceNavigationGuard(window);
  return mainWindow;
}

async function createSetupWindow(initialProgress) {
  if (setupWindow && !setupWindow.isDestroyed?.()) {
    setupWindow.show?.();
    setupWindow.focus?.();
    return setupWindow;
  }
  setupWindow = new BrowserWindow({
    width: 860,
    height: 860,
    minWidth: 680,
    minHeight: 680,
    show: false,
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      preload: path.join(__dirname, '../preload/setup-preload.js'),
    },
  });
  setupWindow.removeMenu?.();
  setupWindow.once('ready-to-show', () => {
    setupWindow?.show();
    if (initialProgress) setupWindow?.webContents.send?.('setup:progress', initialProgress);
  });
  setupWindow.on?.('closed', () => { setupWindow = null; });
  restrictWindowToPage(setupWindow, SETUP_PAGE_URL);
  await setupWindow.loadURL(SETUP_PAGE_URL);
  return setupWindow;
}

function safeRuntimeVersion(value) {
  return typeof value === 'string' && PRODUCT_VERSION_PATTERN.test(value) ? value : 'unknown';
}

function runtimeDiagnosticsEndpointIsCurrent(targetManager, endpoint, expectedVersion, ready) {
  return Boolean(
    ready
      && expectedVersion !== 'unknown'
      && endpoint
      && endpoint.manager === targetManager
      && endpoint.version === expectedVersion
      && endpoint.expectedVersion === expectedVersion
      && endpoint.serviceBaseUrl === targetManager?.serviceBaseUrl
      && isManagerServiceRequest(endpoint.serviceBaseUrl, targetManager),
  );
}

function diagnosticRedactions(options = {}) {
  if (Array.isArray(options.diagnosticSecrets)) {
    return options.diagnosticSecrets.filter((value) => typeof value === 'string' && value.length > 0);
  }
  try {
    return diagnosticSecretValues(options.credentialsDocument === undefined
      ? credentials?.load?.() || {}
      : options.credentialsDocument);
  } catch {
    return [];
  }
}

async function safeRuntimeDiagnostics(options = {}) {
  const targetManager = options.manager === undefined ? manager : options.manager;
  const currentEndpoint = () => (options.publishedEndpoint === undefined
    ? publishedServiceEndpoint
    : options.publishedEndpoint);
  const currentReady = () => (options.serviceReady === undefined ? serviceReady : options.serviceReady);
  const endpoint = currentEndpoint();
  const expectedVersion = safeRuntimeVersion(
    options.expectedVersion || packagedReleaseIdentity?.version || packagedProductVersion(),
  );
  let state = {};
  try {
    if (typeof targetManager?.readState === 'function') state = await targetManager.readState() || {};
  } catch { state = {}; }
  const installedVersion = safeRuntimeVersion(state.installedVersion);
  const pendingVersion = safeRuntimeVersion(state.pendingRuntimeVersion);
  let authorized = false;
  if (runtimeDiagnosticsEndpointIsCurrent(targetManager, endpoint, expectedVersion, currentReady())
      && typeof targetManager?.status === 'function'
      && typeof targetManager?.verifyLocalEndpointHealth === 'function'
      && typeof targetManager?.diagnostics === 'function') {
    try {
      const diagnostic = await withManagerLifecycle(targetManager, async () => {
        // A lifecycle transition can rotate or invalidate the local proxy
        // between the initial check and the lock acquisition.
        if (currentEndpoint() !== endpoint
            || !runtimeDiagnosticsEndpointIsCurrent(targetManager, endpoint, expectedVersion, currentReady())) return null;
        const status = await targetManager.status();
        const current = validateServiceEndpoint(status, targetManager, expectedVersion);
        if (current.serviceBaseUrl !== endpoint.serviceBaseUrl) return null;
        await targetManager.verifyLocalEndpointHealth(expectedVersion, current.serviceBaseUrl);
        // Health verification may overlap a proxy generation change. Do not
        // collect raw supervisor diagnostics unless the same authority remains
        // current at the instant immediately before the call.
        if (currentEndpoint() !== endpoint
            || !runtimeDiagnosticsEndpointIsCurrent(targetManager, endpoint, expectedVersion, currentReady())) return null;
        const result = await targetManager.diagnostics(diagnosticRedactions(options));
        return typeof result === 'string' ? result : null;
      });
      if (diagnostic !== null) {
        authorized = true;
        return diagnostic;
      }
    } catch { /* Fall through to the allowlisted identity-only record. */ }
  }
  const upgrade = state.upgradeProgress;
  const upgradeLines = upgrade && ['checking', 'artifacts', 'network', 'submitting', 'reconciling', 'starting', 'complete', 'failed'].includes(upgrade.phase)
    ? [`last_upgrade_phase=${upgrade.phase}`, `upgrade_from=${safeRuntimeVersion(upgrade.fromVersion)}`, `upgrade_to=${safeRuntimeVersion(upgrade.toVersion)}`]
    : [];
  return [
    `expected_version=${expectedVersion}`,
    `installed_version=${installedVersion}`,
    `pending_runtime_version=${pendingVersion}`,
    `endpoint_authorized=${authorized ? 'true' : 'false'}`,
    ...upgradeLines,
  ].join('\n');
}

async function showRuntimeUpdatePendingPage() {
  await showStartupError({
    code: 'runtime_update_pending',
    message: '等待任务结束后应用安全更新。',
    action: '当前旧运行时仍在后台保留；任务结束后可从“服务”菜单重新连接并应用更新。',
  });
}

async function showStartupError(error) {
  let diagnostic = '';
  try { diagnostic = await safeRuntimeDiagnostics(); } catch { diagnostic = ''; }
  const window = createMainWindow();
  activeErrorPageURL = `data:text/html;charset=utf-8,${encodeURIComponent(errorDocument(error, diagnostic))}`;
  internalErrorURL = activeErrorPageURL;
  try { await window.loadURL(internalErrorURL); } finally { internalErrorURL = ''; }
}

function systemInfoFromHelper() {
  return async () => {
    const result = await windowsCapabilities.probe();
    const persisted = desktopState?.load?.() || {};
    if (result.ok) {
      return {
        virtualizationEnabled: result.virtualizationEnabled === true ? true : null,
        rebootRequired: Boolean(persisted.resumeSetup && persisted.rebootRequired),
      };
    }
    // IsProcessorFeaturePresent and generic Windows reboot markers are known
    // to report false positives on hosts where WSL2 is already operational.
    // WslManager therefore performs real wsl.exe probes and classifies their
    // HCS error codes; only a setup restart marker created by this app blocks.
    return { virtualizationEnabled: null, rebootRequired: Boolean(persisted.resumeSetup && persisted.rebootRequired) };
  };
}

function initializeRuntime() {
  initializeApplicationPaths();
  credentials = new CredentialStore({
    credentialPath: path.join(appDataDir, 'credentials.bin'),
    safeStorage,
  });
  desktopState = new DesktopStateStore(path.join(appDataDir, 'desktop-state.json'));
  const windowsHelperBindingProvider = () => {
    const binding = loadPackagedReleaseIdentity().interopRecovery?.windowsHelper;
    if (!binding) throw new Error('The packaged Windows helper binding is unavailable');
    return binding;
  };
  windowsCapabilities = new WindowsCapabilities({
    helperPath: defaultWindowsHelperPath({ resourcesPath: process.resourcesPath, appPath: REPOSITORY_ROOT }),
    helperBindingProvider: windowsHelperBindingProvider,
  });
  manager = new WslManager({
    onUpgradeProgress: progress => setupWindow?.webContents.send?.('setup:progress', {
      step: 'updating', upgrade: progress,
    }),
    appDataDir,
    statePath: path.join(appDataDir, 'appliance-state.json'),
    musicDir: app.getPath('music'),
    downloadDir: desktopState.load().downloadDir || defaultDownloadDir,
    protectedDownloadRoots: [
      appDataDir,
      ...(app.isPackaged ? [path.dirname(process.execPath)] : []),
    ],
    systemInfo: systemInfoFromHelper(),
    credentialProvider: () => credentials.load(),
    windowsHelperBindingProvider,
    interopRecoveryIdentityProvider: () => loadPackagedReleaseIdentity().interopRecoveryIdentity,
  });
  setupController = new SetupController({
    manager,
    stateStore: desktopState,
    credentialStore: credentials,
    manifestProvider: async () => loadPackagedReleaseIdentity().manifest,
    installWsl: () => windowsCapabilities.installWsl(),
    updateWsl: () => windowsCapabilities.updateWsl(),
  });
  setupController.on('progress', (progress) => setupWindow?.webContents.send?.('setup:progress', progress));
}

async function startManagerWithDeferredCredentials(targetManager, {
  credentialStore,
  stateStore,
  expectedVersion = '',
}) {
  if (!targetManager || typeof targetManager.start !== 'function'
      || !credentialStore || typeof credentialStore.load !== 'function'
      || !stateStore || typeof stateStore.save !== 'function') {
    throw new Error('Deferred credential startup is unavailable');
  }
  try {
    const status = await targetManager.start(undefined, {
      expectedVersion,
      credentialProvider: () => credentialStore.load(),
      reuseRunning: stateStore.load?.().pendingCredentialDelivery !== true,
    });
    stateStore.save({ pendingCredentialDelivery: false });
    return status;
  } catch (error) {
    if (error?.code !== 'active_jobs') throw error;
    // The manager rejects an idle credential restart before resolving the
    // provider when the queue becomes active. Preserve the live backend and
    // reconnect without delivering credentials to that generation.
    stateStore.save({ pendingCredentialDelivery: true });
    return targetManager.start(undefined, { expectedVersion });
  }
}

function startService(options = {}) {
  return runServiceOperation(async () => {
    let result = await startServiceUnlocked(options);
    if (result?.publication?.published && options.showWindow !== false && !result.reconnected) {
      const publication = await reconnectPublishedServiceEndpoint(result.publication, { showWindow: true });
      if (!publication.reconnected) throw endpointPublicationException(publication.error || publicEndpointReconnectError());
      result = { ...result, reconnected: true, publication };
    }
    if (result?.pendingRuntimeUpdate && options.showWindow !== false
        && !isExactOfflineShellURL(mainWindow?.webContents?.getURL?.())) {
      await showRuntimeUpdatePendingPage();
    }
    return result;
  });
}

async function startServiceUnlocked({ showWindow = true, releaseIdentity = null } = {}) {
  const release = releaseIdentity || loadPackagedReleaseIdentity();
  const installedState = await manager.readState();
  if (installedState.installedVersion && installedState.installedVersion !== release.version) {
    const update = await applyRuntimeUpdateIfIdleUnlocked({ releaseIdentity: release, showWindow: false });
    if (update.reason === 'active_jobs') {
      return {
        pendingRuntimeUpdate: true,
        status: { version: installedState.installedVersion, downloadDir: manager.downloadDir },
        publication: { published: false, reconnected: false, error: null, port: null },
        reconnected: false,
      };
    }
    if (update.updated) {
      return {
        ...update.status,
        publication: update.publication,
        reconnected: update.reconnected,
      };
    }
  }
  const { status, publication: committedPublication } = await withManagerLifecycle(manager, async () => {
    let status;
    try {
      status = await startManagerWithDeferredCredentials(manager, {
        credentialStore: credentials,
        stateStore: desktopState,
        expectedVersion: release.version,
      });
    } catch (error) {
      await retainVerifiedEndpointAfterLifecycleFailure(manager);
      throw error;
    }
    const publication = await publishServiceEndpoint(status, {
      state: {
        setupStep: 'starting',
        pendingCompletion: true,
        downloadDir: status.downloadDir || manager.downloadDir,
        applianceVersion: status.version || '',
        rebootRequired: false,
        resumeSetup: false,
        lastErrorCode: '',
      },
      invalidState: {
        setupStep: 'failed',
        pendingCompletion: false,
        lastErrorCode: 'endpoint_publication_failed',
      },
      expectedVersion: release.version,
      requireLiveStatus: true,
      showWindow: false,
    });
    return { status, publication };
  });
  const publication = await reconnectPublishedServiceEndpoint(committedPublication, { showWindow });
  if (!publication.published || (showWindow && !publication.reconnected)) {
    if (publication.published) {
      try {
        desktopState.save({ setupStep: 'failed', pendingCompletion: false, lastErrorCode: 'endpoint_publication_failed', localPort: null });
      } catch { /* The endpoint remains fail-closed even if the diagnostic state cannot be written. */ }
    }
    throw endpointPublicationException(publication.error || publicEndpointPublicationError());
  }
  try {
    desktopState.save({
      setupStep: 'complete',
      pendingCompletion: false,
      downloadDir: status.downloadDir || manager.downloadDir,
      applianceVersion: status.version || release.version,
      rebootRequired: false,
      resumeSetup: false,
      lastErrorCode: '',
    });
  } catch {
    await invalidateServiceEndpoint({
      manager,
      state: { setupStep: 'failed', pendingCompletion: false, lastErrorCode: 'endpoint_publication_failed' },
    });
    throw endpointPublicationException();
  }
  if (typeof app.setLoginItemSettings === 'function') {
    app.setLoginItemSettings(enabledLoginItemSettings(['--autostart']));
  }
  return { ...status, reconnected: publication.reconnected, publication };
}

function applyRuntimeUpdateIfIdle(options = {}) {
  return runServiceOperation(() => applyRuntimeUpdateIfIdleUnlocked(options));
}

async function applyRuntimeUpdateIfIdleUnlocked({
  showWindow = Boolean(mainWindow && !mainWindow.isDestroyed?.()),
  releaseIdentity = null,
} = {}) {
  let release;
  try { release = releaseIdentity || loadPackagedReleaseIdentity(); } catch {
    return { updated: false, reason: 'manifest_unavailable' };
  }
  const manifest = release.manifest;
  const applianceState = await manager.readState();
  if (!applianceState.installedVersion || applianceState.installedVersion === manifest.version) {
    return { updated: false, reason: 'current' };
  }
  let committed;
  try {
    committed = await withManagerLifecycle(manager, async () => {
    let status;
    try {
      status = await manager.update(manifest, { interopRecovery: release.interopRecovery });
    } catch (error) {
      await invalidateServiceEndpoint({
        manager,
        state: { applianceVersion: applianceState.installedVersion, pendingRuntimeVersion: manifest.version },
      });
      throw error;
    }
    await manager.writeState({ pendingRuntimeVersion: '' });
    const publication = await publishServiceEndpoint(status, {
      state: { setupStep: 'starting', pendingCompletion: true, applianceVersion: manifest.version },
      invalidState: {
        setupStep: 'failed',
        pendingCompletion: false,
        applianceVersion: manifest.version,
        lastErrorCode: 'endpoint_publication_failed',
      },
      expectedVersion: release.version,
      requireLiveStatus: true,
      showWindow: false,
    });
    return { status, publication };
    });
  } catch (error) {
    if (error?.code === 'active_jobs') {
      await manager.writeState({ pendingRuntimeVersion: manifest.version });
      await invalidateServiceEndpoint({
        manager,
        state: { applianceVersion: applianceState.installedVersion, pendingRuntimeVersion: manifest.version },
      });
      return { updated: false, reason: 'active_jobs' };
    }
    throw error;
  }
  const { status, publication: committedPublication } = committed;
  const publication = await reconnectPublishedServiceEndpoint(committedPublication, { showWindow });
  if (!publication.published || (showWindow && !publication.reconnected)) {
    if (publication.published) {
      try {
        desktopState.save({ setupStep: 'failed', pendingCompletion: false, lastErrorCode: 'endpoint_publication_failed', localPort: null });
      } catch { /* Preserve fail-closed endpoint state even if diagnostics are unavailable. */ }
    }
    throw endpointPublicationException(publication.error || publicEndpointPublicationError());
  }
  desktopState.save({
    setupStep: 'complete',
    pendingCompletion: false,
    applianceVersion: manifest.version,
    rebootRequired: false,
    resumeSetup: false,
    lastErrorCode: '',
  });
  return { updated: true, status, reconnected: publication.reconnected, publication };
}

function publicRepairError(error) {
  if (error instanceof WslManagerError) {
    const result = { code: error.code, message: error.message, action: error.action };
    if (error.code === 'network_preflight_failed') {
      result.details = { stage: publicNetworkPreflightDetails(error)?.stage || 'unknown' };
    }
    return result;
  }
  return {
    code: 'repair_failed',
    message: 'WSL Appliance 修复未能完成。',
    action: '请打开“服务 → 诊断信息”查看脱敏日志后重试。',
  };
}

function repairRuntime({ confirm = true, showWindow = true } = {}) {
  if (repairOperationPromise) return repairOperationPromise;
  repairOperationPromise = runServiceOperation(async () => {
    if (!manager) throw new Error('WSL Appliance manager is unavailable');
    if (confirm) {
      const choice = await dialog.showMessageBox(mainWindow || setupWindow, {
        type: 'warning',
        buttons: ['取消', '修复服务'],
        defaultId: 0,
        cancelId: 0,
        title: '诊断与修复',
        message: '将修复专用 WSL2 运行时。',
        detail: '修复前必须没有活动下载任务；数据库、Wrapper 状态和下载目录会保留，不会注销发行版。是否继续？',
      });
      if (choice.response !== 1) return { ok: false, cancelled: true };
    }
    const release = loadPackagedReleaseIdentity();
    let status;
    try {
      status = await withManagerLifecycle(manager, async () => {
        await manager.repair(release.manifest);
        // Keep the historical credential restart after repair, but retain the
        // lifecycle lock through the final endpoint commit so no other
        // process can publish an intermediate proxy generation.
        return startServiceUnlocked({ showWindow: false, releaseIdentity: release });
      });
    } catch (error) {
      await retainVerifiedEndpointAfterLifecycleFailure(manager);
      throw error;
    }
    if (showWindow) {
      const publication = await reconnectPublishedServiceEndpoint(status.publication, { showWindow: true });
      if (!publication.reconnected) throw endpointPublicationException(publication.error);
      setupWindow?.close?.();
    }
    return { ok: true };
  }).finally(() => {
    repairOperationPromise = null;
  });
  return repairOperationPromise;
}

function localAPIRequest(url, {
  method = 'GET',
  timeoutMs = 5_000,
  maxBytes = 1024 * 1024,
  headers = {},
  requestImpl = http.request,
} = {}) {
  return new Promise((resolve, reject) => {
    let parsed;
    try { parsed = new URL(url); } catch (error) { reject(error); return; }
    if (parsed.protocol !== 'http:' || parsed.hostname !== '127.0.0.1') {
      reject(new Error('Only the IPv4 loopback API is allowed'));
      return;
    }
    const request = requestImpl(parsed, { method, timeout: timeoutMs, headers: { ...headers } }, (response) => {
      const chunks = [];
      let length = 0;
      response.on('data', (chunk) => {
        length += chunk.length;
        if (length > maxBytes) {
          request.destroy(new Error('Local API response is too large'));
          return;
        }
        chunks.push(Buffer.from(chunk));
      });
      response.on('end', () => resolve({
        statusCode: response.statusCode || 0,
        body: Buffer.concat(chunks).toString('utf8'),
      }));
    });
    request.on('timeout', () => request.destroy(new Error('Local API request timed out')));
    request.on('error', reject);
    request.end();
  });
}

function desktopActionError(code, message, action) {
  return Object.assign(new Error(message), { code, action });
}

async function cancelActiveJobsAndWait(targetManager, jobs, options = {}) {
  const request = options.request || localAPIRequest;
  const sleep = options.sleep || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)));
  const now = options.now || Date.now;
  const timeoutMs = options.timeoutMs || 30_000;
  const intervalMs = options.intervalMs || 250;
  let base;
  try { base = new URL(targetManager.serviceBaseUrl); } catch {
    throw desktopActionError('cancel_failed', '无法连接本机任务服务。', '请先从“服务”菜单重新连接，然后再停止服务。');
  }
  if (base.protocol !== 'http:' || base.hostname !== '127.0.0.1') {
    throw desktopActionError('cancel_failed', '任务服务不是受信任的本机地址。', '请运行诊断与修复，不要继续停止服务。');
  }
  let proxyHeaders;
  try { proxyHeaders = targetManager.desktopProxyRequestHeaders(); } catch {
    throw desktopActionError('cancel_failed', '本机任务服务认证不可用。', '服务仍保持运行；请重新连接或运行诊断与修复。');
  }
  const proxyToken = Object.entries(proxyHeaders || {})
    .find(([name]) => name.toLowerCase() === DESKTOP_PROXY_HEADER.toLowerCase())?.[1];
  if (typeof proxyToken !== 'string' || !/^[a-f0-9]{64}$/.test(proxyToken)) {
    throw desktopActionError('cancel_failed', '本机任务服务认证不可用。', '服务仍保持运行；请重新连接或运行诊断与修复。');
  }
  proxyHeaders = { [DESKTOP_PROXY_HEADER]: proxyToken };

  const deadline = now() + timeoutMs;
  const requested = new Set();
  let remaining = Array.isArray(jobs) ? jobs : [];
  while (true) {
    const identifiers = [...new Set(remaining
      .map((job) => job?.id)
      .filter((id) => typeof id === 'string' && id && !requested.has(id)))];
    for (const id of identifiers) {
      let response;
      try {
        response = await request(new URL(`/api/v1/jobs/${encodeURIComponent(id)}/cancel`, base).href, {
          method: 'POST',
          timeoutMs: 5_000,
          headers: proxyHeaders,
        });
      } catch {
        throw desktopActionError('cancel_failed', '请求取消下载任务时连接失败。', '服务仍保持运行；请检查任务状态后重试。');
      }
      // 404/409 are safe stale-list races: the subsequent active-job poll is
      // the authority for whether stopping is allowed.
      if (![200, 404, 409].includes(response.statusCode)) {
        throw desktopActionError('cancel_failed', `取消任务失败（HTTP ${response.statusCode}）。`, '服务仍保持运行；请检查任务状态后重试。');
      }
      requested.add(id);
    }

    try { remaining = await targetManager.activeJobs(); } catch {
      throw desktopActionError('cancel_failed', '无法确认任务是否已经停止。', '为避免损坏下载，服务仍保持运行；请重新连接后重试。');
    }
    if (remaining.length === 0) return;
    if (now() >= deadline) {
      throw desktopActionError('cancel_timeout', '部分任务未能在 30 秒内取消。', '服务仍保持运行；请等待任务进入“已取消”后再次停止。');
    }
    await sleep(intervalMs);
  }
}

async function retainVerifiedEndpointAfterLifecycleFailure(targetManager = manager, options = {}) {
  try {
    const expectedVersion = options.expectedVersion
      || (targetManager === manager ? packagedReleaseIdentity?.version || '' : '');
    if (!expectedVersion) throw new Error('release identity is unavailable');
    return await withManagerLifecycle(targetManager, async () => {
      if (!targetManager || typeof targetManager.status !== 'function'
          || typeof targetManager.verifyLocalEndpointHealth !== 'function') throw new Error('endpoint verification is unavailable');
      const status = await targetManager.status();
      const endpoint = validateServiceEndpoint(status, targetManager, expectedVersion);
      await targetManager.verifyLocalEndpointHealth(expectedVersion, endpoint.serviceBaseUrl);
      const publication = await publishServiceEndpoint(status, {
        manager: targetManager,
        desktopState: options.desktopState,
        setServiceReady: options.setServiceReady,
        expectedVersion,
        showWindow: false,
      });
      return publication.published;
    });
  } catch {
    await invalidateServiceEndpoint({
      manager: targetManager,
      desktopState: options.desktopState,
      setServiceReady: options.setServiceReady,
    });
    return false;
  }
}

function stopServiceWithConfirmation() {
  return runServiceOperation(stopServiceWithConfirmationUnlocked);
}

async function stopServiceWithConfirmationUnlocked() {
  if (!manager) return false;
  let activeJobs = [];
  try { activeJobs = await manager.activeJobs(); } catch { /* Backend may already be stopped. */ }
  if (activeJobs.length > 0) {
    const choice = await dialog.showMessageBox(mainWindow || setupWindow, {
      type: 'warning',
      buttons: ['保持服务运行', '取消任务并停止'],
      defaultId: 0,
      cancelId: 0,
      title: '仍有下载任务',
      message: `有 ${activeJobs.length} 个活动任务。`,
      detail: '继续后会先逐个请求取消并等待任务进入终态，然后才停止 WSL 服务。正式下载文件不会因此删除。',
    });
    if (choice.response !== 1) return false;
    await cancelActiveJobsAndWait(manager, activeJobs);
  }
  await withManagerLifecycle(manager, async () => {
    try {
      await manager.stop();
    } catch (error) {
      await retainVerifiedEndpointAfterLifecycleFailure(manager);
      throw error;
    }
    await invalidateServiceEndpoint();
  });
  await retainOfflineBrowse();
  return true;
}

function openCredentialsWindow() {
  if (credentialsWindow && !credentialsWindow.isDestroyed?.()) {
    credentialsWindow.show?.();
    credentialsWindow.focus?.();
    return credentialsWindow;
  }
  credentialsWindow = new BrowserWindow({
    width: 540,
    height: 560,
    resizable: false,
    parent: mainWindow || undefined,
    modal: Boolean(mainWindow),
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      preload: path.join(__dirname, '../preload/credentials-preload.js'),
    },
  });
  const window = credentialsWindow;
  window.removeMenu?.();
  window.on?.('closed', () => {
    if (credentialsWindow === window) credentialsWindow = null;
  });
  restrictWindowToPage(window, CREDENTIALS_PAGE_URL);
  Promise.resolve(window.loadURL(CREDENTIALS_PAGE_URL)).catch(showStartupError);
  return window;
}

async function showDiagnostics() {
  const diagnostic = await safeRuntimeDiagnostics();
  await dialog.showMessageBox(mainWindow || setupWindow, {
    type: 'info',
    title: 'WSL Appliance 诊断（已脱敏）',
    message: '发行版状态与最近 200 行日志',
    detail: diagnostic || '暂无诊断输出。',
  });
}

function installMenu() {
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    {
      label: '服务',
      submenu: [
        { label: '下载位置与设置…', click: () => createSetupWindow(desktopState.load()).catch(showStartupError) },
        { label: '配置凭据…', click: openCredentialsWindow },
        {
          label: '启动/重新连接',
          click: async () => {
            try { await startService({ showWindow: true }); } catch (error) {
              if (!serviceReady) await retainOfflineBrowse(error);
              else await showStartupError(error);
            }
          },
        },
        { label: '诊断信息…', click: () => showDiagnostics().catch(showStartupError) },
        { label: '修复运行时…', click: async () => {
          try { await repairRuntime({ confirm: true, showWindow: true }); }
          catch (error) { await showStartupError(publicRepairError(error)); }
        } },
        { type: 'separator' },
        { label: '停止服务', click: () => stopServiceWithConfirmation().catch(showStartupError) },
      ],
    },
  ]));
}

function assertTrustedFrame(event, expectedURL, expectedWindow) {
  const sender = event?.sender;
  const senderFrame = event?.senderFrame;
  const expectedContents = expectedWindow?.webContents;
  const trusted = expectedWindow
    && !expectedWindow.isDestroyed?.()
    && expectedContents
    && sender === expectedContents
    && senderFrame
    && senderFrame === sender.mainFrame
    && isExactPrivilegedPageURL(senderFrame.url, expectedURL)
    && isExactPrivilegedPageURL(sender.getURL?.(), expectedURL);
  if (!trusted) throw new Error('IPC request is not allowed from this page');
}

function mainServiceFrameBelongsToWindow(event, expectedWindow, targetManager, options = {}) {
  const sender = event?.sender;
  const senderFrame = event?.senderFrame;
  const expectedContents = expectedWindow?.webContents;
  let currentURL = '';
  try {
    currentURL = typeof sender?.getURL === 'function' ? sender.getURL() : senderFrame?.url || '';
  } catch {
    currentURL = '';
  }
  return Boolean(expectedWindow
    && !expectedWindow.isDestroyed?.()
    && expectedContents
    && sender === expectedContents
    && senderFrame
    && senderFrame === expectedContents.mainFrame
    && (shouldRequirePublishedEndpoint(targetManager, options)
      ? isPublishedManagerServiceRequest(senderFrame.url, targetManager)
      : isManagerServiceRequest(senderFrame.url, targetManager))
    && (shouldRequirePublishedEndpoint(targetManager, options)
      ? isPublishedManagerServiceRequest(currentURL, targetManager)
      : isManagerServiceRequest(currentURL, targetManager)));
}

function assertTrustedMainServiceFrame(event) {
  const trusted = serviceReady && mainServiceFrameBelongsToWindow(event, mainWindow, manager);
  if (!trusted) throw new Error('IPC request is not allowed from this page');
}

function offlineShellFrameBelongsToWindow(event) {
  const sender = event?.sender;
  const frame = event?.senderFrame;
  if (!mainWindow || mainWindow.isDestroyed?.() || sender !== mainWindow.webContents
      || !frame || frame !== sender.mainFrame || !isExactOfflineShellURL(frame.url)) return false;
  try { return isExactOfflineShellURL(sender.getURL()); } catch { return false; }
}

function assertTrustedMainShellFrame(event) {
  if (!offlineShellFrameBelongsToWindow(event)
      && !(serviceReady && mainServiceFrameBelongsToWindow(event, mainWindow, manager))) {
    throw new Error('IPC request is not allowed from this page');
  }
}

function normalizeCredentialUpdate(update, existing) {
  if (!update || typeof update !== 'object' || Array.isArray(update)) throw new TypeError('Credential update must be an object');
  const merged = { ...existing };
  if (typeof update.appleEmail === 'string') merged.appleEmail = update.appleEmail.trim();
  if (typeof update.storefront === 'string') merged.storefront = update.storefront.trim();
  for (const key of ['applePassword', 'mediaUserToken', 'authorizationToken']) {
    if (typeof update[key] === 'string' && update[key]) merged[key] = update[key];
  }
  return merged;
}

async function saveCredentialsAndReconnect(update, options = {}) {
  const targetManager = options.manager === undefined ? manager : options.manager;
  const credentialStore = options.credentialStore === undefined ? credentials : options.credentialStore;
  const stateStore = options.desktopState === undefined ? desktopState : options.desktopState;
  const isReady = options.isServiceReady || (() => serviceReady);
  const setReady = options.setServiceReady || ((value) => { serviceReady = value; });
  const showError = options.showStartupError || showStartupError;
  let release = null;
  if (targetManager === manager) {
    release = options.releaseIdentity || loadPackagedReleaseIdentity();
  }
  if (!targetManager || !credentialStore || typeof credentialStore.load !== 'function' || typeof credentialStore.save !== 'function') {
    throw new Error('Credential delivery is unavailable');
  }

  const merged = normalizeCredentialUpdate(update, credentialStore.load());
  credentialStore.save(merged);
  let active = [];
  if (isReady()) {
    try { active = await targetManager.activeJobs(); } catch { /* Apply on the next successful start. */ }
  }
  if (active.length > 0 || !isReady()) {
    stateStore.save({ pendingCredentialDelivery: true });
    return { ok: true, saved: true, applied: false, pending: true, reconnected: false };
  }

  let committedPublication;
  try {
    committedPublication = await withManagerLifecycle(targetManager, async () => {
      // Credential stdin updates supervisor memory; start(credentials) also
      // performs the required idle stop/start so the backend consumes a fresh
      // one-shot credential FD.
      const status = await targetManager.start(merged, release ? { expectedVersion: release.version } : undefined);
      return publishServiceEndpoint(status, {
        manager: targetManager,
        desktopState: stateStore,
        setServiceReady: setReady,
        state: { pendingCredentialDelivery: false },
        invalidState: { pendingCredentialDelivery: false },
        expectedVersion: release?.version || options.expectedVersion || '',
        showWindow: false,
      });
    });
  } catch (error) {
    if (error?.code !== 'active_jobs') {
      await retainVerifiedEndpointAfterLifecycleFailure(targetManager, {
        desktopState: stateStore,
        setServiceReady: setReady,
        expectedVersion: release?.version || options.expectedVersion || '',
      });
      throw error;
    }
    stateStore.save({ pendingCredentialDelivery: true });
    return { ok: true, saved: true, applied: false, pending: true, reconnected: false };
  }

  const publication = await reconnectPublishedServiceEndpoint(committedPublication, {
    manager: targetManager,
    createMainWindow: options.createMainWindow,
    loadManagerService: options.loadManagerService,
    showWindow: true,
  });
  if (publication.error) {
    try { await showError(endpointPublicationException(publication.error)); } catch { /* Preserve the applied credential result. */ }
  }
  const result = {
    ok: true,
    saved: true,
    applied: true,
    pending: false,
    reconnected: publication.reconnected,
  };
  if (publication.error) result.error = publication.error;
  return result;
}

function installIPCHandlers() {
  if (handlersInstalled) return;
  handlersInstalled = true;

  const getPlaybackPlayer = () => {
    if (!officialWebPlayer) officialWebPlayer = createOfficialWebPlayer({
      BrowserWindow, components,
      consent: async () => {
        const result = await dialog.showMessageBox(mainWindow, {
          type: 'question', buttons: ['同意并同步', '仅网页登录'], defaultId: 0, cancelId: 1,
          title: '同步 Apple Music 下载授权',
          message: '是否自动保存此应用官方网页会话中的 Media-User-Token？',
          detail: '只读取指定 Cookie 并通过现有凭据链加密保存，不读取密码或其他浏览器数据。网页登录、播放与下载授权分别判定。',
        });
        return result.response === 0;
      },
      deliver: pair => runServiceOperation(async () => {
        try {
          return await saveCredentialsAndReconnect(pair);
        } catch {
          desktopState.save({ pendingCredentialDelivery: true });
          throw new Error('playback_delivery_pending');
        }
      }),
    });
    return officialWebPlayer;
  };
  ipcMain.handle('desktop:playback-open-login', async event => {
    assertTrustedMainShellFrame(event);
    return getPlaybackPlayer().open();
  });
  ipcMain.handle('desktop:playback-play', async (event, item) => {
    assertTrustedMainShellFrame(event);
    try {
      return await getPlaybackPlayer().open(item);
    } catch {
      return { ok: false, code: 'invalid_playback_item' };
    }
  });
  ipcMain.handle('desktop:playback-stop', async event => {
    assertTrustedMainShellFrame(event);
    return officialWebPlayer ? officialWebPlayer.stop() : { ok: true };
  });

  let exportPending = false;
  ipcMain.handle('desktop:export-diagnostics', async (event, uiLogs) => {
    assertTrustedMainShellFrame(event);
    if (exportPending) return { ok: false, error: { code: 'diagnostic_export_busy', message: '日志导出正在进行。' } };
    exportPending = true;
    try {
      return await exportDiagnosticReport({ dialog, owner: mainWindow,
        collectRuntime: () => safeRuntimeDiagnostics({ diagnosticSecrets: [] }),
        writeFile: fs.promises.writeFile.bind(fs.promises), productVersion: packagedProductVersion(), uiLogs });
    } finally { exportPending = false; }
  });

  // Settings mirrors the native menu, not an arbitrary command bridge. Never
  // return window objects, diagnostics, paths or credential data to this page.
  const settingsActions = {
    'desktop:choose-download-location': async () => {
      const selected = await dialog.showOpenDialog(mainWindow, {
        title: '选择下载位置', defaultPath: manager.downloadDir,
        properties: ['openDirectory', 'createDirectory'],
      });
      if (selected.canceled || !selected.filePaths?.[0]) return { ok: true, cancelled: true };
      await runServiceOperation(() => withManagerLifecycle(manager, async () => {
        const result = await manager.configureDownloadPath(selected.filePaths[0]);
        desktopState.save({ downloadDir: result.windowsPath });
        if (result.serviceRestarted) {
          const status = await manager.status();
          const publication = await publishServiceEndpoint(status, {
            expectedVersion: packagedProductVersion(), requireLiveStatus: true, showWindow: false,
          });
          const reconnected = await reconnectPublishedServiceEndpoint(publication, { showWindow: true });
          if (!reconnected.published || !reconnected.reconnected) throw endpointPublicationException(reconnected.error || publicEndpointPublicationError());
        }
      }));
      return { ok: true };
    },
    'desktop:open-download-settings': async () => { await createSetupWindow(desktopState.load()); return { ok: true }; },
    'desktop:open-credentials': async () => { openCredentialsWindow(); return { ok: true }; },
    'desktop:start-service': async () => {
      const result = await startService({ showWindow: true });
      if (result?.pendingRuntimeUpdate) return {
        ok: false, error: { code: 'runtime_update_pending', message: '下载服务暂未就绪。', action: '请等待现有任务结束后重试。' },
      };
      return { ok: true };
    },
    'desktop:show-diagnostics': async () => { await showDiagnostics(); return { ok: true }; },
    'desktop:stop-service': async () => ({ ok: true, cancelled: !(await stopServiceWithConfirmation()) }),
  };
  for (const [channel, action] of Object.entries(settingsActions)) {
    ipcMain.handle(channel, async (event) => {
      assertTrustedMainShellFrame(event);
      try { return await action(); }
      catch (error) {
        if (!serviceReady) {
          try { await retainOfflineBrowse(error); } catch { /* Keep the fixed action error. */ }
        }
        return { ok: false, error: error instanceof WslManagerError
          ? { code: error.code, message: error.message, action: error.action }
          : { code: 'settings_action_failed', message: '操作未能完成。', action: '请查看诊断信息后重试。' } };
      }
    });
  }

  const musicActions = {
    'desktop:music-open': async () => {
      if (!mainWindow || !WebContentsView) return { ok: false, code: 'browser_unavailable' };
      if (!appleMusicBrowser) appleMusicBrowser = createAppleMusicBrowser({ owner: mainWindow, WebContentsView });
      const browser = appleMusicBrowser;
      await browser.open();
      if (appleMusicBrowser !== browser) return { ok: false, code: 'browser_unavailable' };
      return { ok: true, url: browser.currentURL() };
    },
    'desktop:music-close': () => { appleMusicBrowser?.hide(); return { ok: true }; },
    'desktop:music-back': () => { appleMusicBrowser?.back(); return { ok: true }; },
    'desktop:music-forward': () => { appleMusicBrowser?.forward(); return { ok: true }; },
    'desktop:music-reload': () => { appleMusicBrowser?.reload(); return { ok: true }; },
    'desktop:music-current': () => ({ ok: true, url: appleMusicBrowser?.currentURL() || '' }),
    'desktop:music-bounds': (bounds) => {
      if (!appleMusicBrowser) return { ok: false, code: 'browser_unavailable' };
      appleMusicBrowser.setBounds(bounds);
      return { ok: true };
    },
  };
  for (const [channel, action] of Object.entries(musicActions)) {
    ipcMain.handle(channel, async (event, bounds) => {
      assertTrustedMainShellFrame(event);
      try { return await action(bounds); }
      catch { return { ok: false, code: 'browser_unavailable' }; }
    });
  }

  ipcMain.handle('desktop:get-download-preferences', (event) => {
    assertTrustedMainShellFrame(event);
    const saved = desktopState.load();
    return {
      quality: ['aac-lc', 'alac', 'aac', 'atmos'].includes(saved.defaultQuality) ? saved.defaultQuality : 'aac-lc',
      existingPolicy: ['skip', 'overwrite', 'fail'].includes(saved.defaultExistingPolicy) ? saved.defaultExistingPolicy : 'skip',
    };
  });
  ipcMain.handle('desktop:set-download-preferences', (event, value) => {
    assertTrustedMainShellFrame(event);
    if (!value || typeof value !== 'object' || Array.isArray(value)
        || Object.keys(value).sort().join(',') !== 'existingPolicy,quality'
        || !['aac-lc', 'alac', 'aac', 'atmos'].includes(value.quality)
        || !['skip', 'overwrite', 'fail'].includes(value.existingPolicy)) {
      return { ok: false };
    }
    desktopState.save({ defaultQuality: value.quality, defaultExistingPolicy: value.existingPolicy });
    return { ok: true };
  });

  ipcMain.on?.('desktop:music-download-click', (event, generation) => {
    const url = appleMusicBrowser?.acceptDownloadIntent(event, generation) || '';
    if (!url || !mainWindow || mainWindow.isDestroyed?.()) return;
    const ownerContents = mainWindow.webContents;
    const ownerEvent = { sender: ownerContents, senderFrame: ownerContents?.mainFrame };
    if (!offlineShellFrameBelongsToWindow(ownerEvent)
        && !(serviceReady && mainServiceFrameBelongsToWindow(ownerEvent, mainWindow, manager))) return;
    ownerContents.send('desktop:music-download-intent', { url });
  });

  ipcMain.handle('desktop:repair-runtime', async (event) => {
    assertTrustedMainShellFrame(event);
    try {
      return await repairRuntime({ confirm: true, showWindow: true });
    } catch (error) {
      return { ok: false, error: publicRepairError(error) };
    }
  });

  ipcMain.handle('setup:repair-runtime', async (event) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    try {
      return await repairRuntime({ confirm: true, showWindow: true });
    } catch (error) {
      return { ok: false, error: publicRepairError(error) };
    }
  });

  ipcMain.handle('setup:get-state', (event) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    return { ...desktopState.load(), downloadDir: desktopState.load().downloadDir || defaultDownloadDir };
  });
  ipcMain.handle('setup:get-diagnostics', async (event) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    if (!manager) return '';
    return safeRuntimeDiagnostics();
  });
  ipcMain.handle('setup:choose-download-directory', async (event) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    const result = await dialog.showOpenDialog(setupWindow, {
      title: '选择 Apple Music 下载目录',
      defaultPath: desktopState.load().downloadDir || defaultDownloadDir,
      properties: ['openDirectory', 'createDirectory'],
    });
    return result.canceled ? null : result.filePaths[0];
  });
  ipcMain.handle('setup:begin', async (event, options) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    return runServiceOperation(async () => {
      const release = loadPackagedReleaseIdentity();
      const { result, committedPublication } = await withManagerLifecycle(manager, async () => {
        const result = await setupController.begin(options || {});
        if (!result.ok) return { result, committedPublication: null };
        const committedPublication = await publishServiceEndpoint(result.status, {
          manager,
          expectedVersion: release.version,
          requireLiveStatus: true,
          invalidState: {
            setupStep: 'failed',
            pendingCompletion: false,
            lastErrorCode: 'endpoint_publication_failed',
          },
          showWindow: false,
        });
        return { result, committedPublication };
      });
      if (!result.ok) return result;
      const publication = await reconnectPublishedServiceEndpoint(committedPublication, { showWindow: true });
      if (!publication.published || !publication.reconnected) {
        setupController.failEndpoint(publication.error?.code || 'endpoint_publication_failed');
        await showStartupError(endpointPublicationException(publication.error || publicEndpointPublicationError()));
        return {
          ...result,
          ok: false,
          error: publication.error || publicEndpointPublicationError(),
          state: setupController.state(),
          pendingCompletion: false,
        };
      }
      if (typeof app.setLoginItemSettings === 'function') app.setLoginItemSettings(enabledLoginItemSettings(['--autostart']));
      setupWindow?.close?.();
      const completedState = setupController.complete(result.status, {
        downloadDir: options?.downloadDir || '',
        applianceVersion: release.version,
      });
      return { ...result, state: completedState, pendingCompletion: false, reconnected: true };
    });
  });
  ipcMain.handle('setup:retry', async (event) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    return setupController.probe();
  });
  ipcMain.handle('setup:apply-wsl-action', async (event, code) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    try { return { ok: true, probe: await setupController.applyWslAction(code) }; } catch (error) { return { ok: false, error: { code: error.code, message: error.message, action: error.action } }; }
  });
  ipcMain.handle('setup:restart-windows', async (event) => {
    assertTrustedFrame(event, SETUP_PAGE_URL, setupWindow);
    const choice = await dialog.showMessageBox(setupWindow, {
      type: 'warning',
      buttons: ['取消', '重新启动 Windows'],
      defaultId: 0,
      cancelId: 0,
      message: '现在重新启动 Windows？',
      detail: '安装向导会在下次登录后自动继续。请先保存其他应用中的工作。',
    });
    if (choice.response !== 1) return { restarted: false };
    desktopState.save({ rebootRequired: true, resumeSetup: true, setupStep: 'reboot' });
    if (typeof app.setLoginItemSettings === 'function') app.setLoginItemSettings(enabledLoginItemSettings(['--resume-setup']));
    await windowsCapabilities.restartWindows();
    return { restarted: true };
  });

  ipcMain.handle('credentials:summary', (event) => {
    assertTrustedFrame(event, CREDENTIALS_PAGE_URL, credentialsWindow);
    const values = credentials.load();
    return {
      appleEmail: values.appleEmail || '',
      storefront: values.storefront || '',
      hasApplePassword: Boolean(values.applePassword),
      hasMediaUserToken: Boolean(values.mediaUserToken),
      hasAuthorizationToken: Boolean(values.authorizationToken),
      pendingDelivery: Boolean(desktopState.load().pendingCredentialDelivery),
    };
  });
  ipcMain.handle('credentials:save', async (event, update) => {
    assertTrustedFrame(event, CREDENTIALS_PAGE_URL, credentialsWindow);
    return runServiceOperation(() => saveCredentialsAndReconnect(update));
  });
}

async function initialLaunch() {
  initializeRuntime();
  installIPCHandlers();
  installMenu();

  // The public browser does not depend on WSL. Paint this exact, packaged
  // shell before probing or starting the appliance; successful publication
  // replaces it with the authenticated service page.
  if (foregroundRequested) await createMainWindow().loadURL(OFFLINE_SHELL_URL + '#browse');

  const probe = await manager.probe();
  if (!probe.ok || !probe.distro?.installed || RESUME_SETUP) {
    if (AUTOSTART && !RESUME_SETUP && !foregroundRequested) {
      app.quit?.();
      return;
    }
    await retainOfflineBrowse();
    return;
  }

  try {
    const started = await startService({ showWindow: false });
    if (started?.pendingRuntimeUpdate) {
      if (foregroundRequested) await retainOfflineBrowse();
      else app.quit?.();
      return;
    }
    if (foregroundRequested) await runServiceOperation(() => loadManagerService(createMainWindow()));
    else app.quit?.();
  } catch (error) {
    if (AUTOSTART && !foregroundRequested) {
      app.quit?.();
      return;
    }
    if (mainWindow && !mainWindow.isDestroyed?.()) await retainOfflineBrowse(error);
    else await showStartupError(error);
  }
}

function runInitialLaunch() {
  if (!initialLaunchPromise) {
    initialLaunchPromise = initialLaunch().finally(() => { initialLaunchPromise = null; });
  }
  return initialLaunchPromise;
}

if (!SINGLE_INSTANCE_ACQUIRED) {
  app.quit?.();
} else {
  app.on('second-instance', handleSecondInstance);
  app.whenReady().then(runInitialLaunch).catch((error) => showStartupError(error));

  app.on('activate', async () => {
    try {
      foregroundRequested = true;
      if (BrowserWindow.getAllWindows().length > 0) {
        focusExistingWindow();
        return;
      }
      if (!manager) {
        await runInitialLaunch();
        return;
      }
      if (serviceReady) await runServiceOperation(() => loadManagerService(createMainWindow()));
      else await runInitialLaunch();
    } catch (error) {
      await showStartupError(error);
    }
  });

  // Closing Electron must not stop systemd, the supervisor, or active jobs.
  app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') app.quit?.();
  });
}

module.exports = {
  CREDENTIALS_PAGE_URL,
  ERROR_REPAIR_URL,
  SETUP_PAGE_URL,
  allowedServiceURL,
  assertTrustedFrame,
  assertTrustedMainServiceFrame,
  assertTrustedMainShellFrame,
  isExactOfflineShellURL,
  cancelActiveJobsAndWait,
  createServiceOperationQueue,
  diagnosticSecretValues,
  desktopNetworkRequestHeaders,
  enabledLoginItemSettings,
  errorDocument,
  focusExistingWindow,
  handleSecondInstance,
  installDesktopProxyRequestInterceptor,
  isManagerServiceRequest,
  installMainServiceNavigationGuard,
  isArmedErrorRepairNavigation,
  mainServiceFrameBelongsToWindow,
  isExactPrivilegedPageURL,
  localAPIRequest,
  loadManagerService,
  managerNavigationOptions,
  normalizeCredentialUpdate,
  openCredentialsWindow,
  packagedProductVersion,
  invalidateServiceEndpoint,
  publishServiceEndpoint,
  publicRepairError,
  publicOfflineServiceState,
  repairRuntime,
  safeRuntimeDiagnostics,
  safeRuntimeVersion,
  saveCredentialsAndReconnect,
  startManagerWithDeferredCredentials,
  restrictWindowToPage,
  singleInstanceAcquired: SINGLE_INSTANCE_ACQUIRED,
};

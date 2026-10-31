'use strict';

// Browser/catalog scripts receive this narrow interface instead of reaching
// into the queue's mutable state or relying on global function declarations.
window.amdlRenderer = (({ jobData }) => {
const state = {
  capabilities: null,
  // Capabilities are a snapshot of the protected local bridge.  Keep the
  // last payload for diagnostics, but never present it as current while the
  // bridge is known to be unavailable.
  capabilitiesState: 'unknown',
  jobs: new Map(),
  jobCards: new Map(),
  pendingDeletes: new Set(),
  deleteErrors: new Map(),
  // Job/file IDs are immutable. Retain successful deletions for this page
  // session so in-flight reads and buffered SSE cannot resurrect them.
  deletedJobs: new Set(),
  deletedFiles: new Set(),
  filesByJob: new Map(),
  knownFiles: new Map(),
  eventSources: new Map(),
  lastEventIds: new Map(),
  queuePollTimer: null,
};
const QUEUE_REFRESH_INTERVAL_MS = 1_500;
const offlineShell = window.location.protocol === 'file:';
const NETWORK_PREFLIGHT_STAGE_CODES = new Set([
  'helper_identity', 'helper_launch', 'helper_protocol', 'proxy_resolution',
  'native_probe_invalid', 'native_startup', 'dns', 'tcp_connect', 'tls',
  'ca_bundle_missing', 'ca_bundle_invalid', 'url_bag_fetch',
  'url_bag_parse', 'url_bag_key_lookup',
]);
function knownNetworkPreflightStage(value) {
  return NETWORK_PREFLIGHT_STAGE_CODES.has(value) ? value : 'unknown';
}
// A capabilities read must be bounded. Without a deadline a single request that
// never settles keeps the in-flight handle forever, so the scheduler never arms
// its next tick and the last good snapshot stays on screen indefinitely.
const CAPABILITIES_REQUEST_TIMEOUT_MS = 8_000;
// Startup stages the backend is allowed to name, mirroring the manager's
// allowlist. A stage arrives over the bridge, so it is matched against this set
// and never rendered as free text.
const WRAPPER_STARTUP_STAGES = new Set([
  'launcher_start',
  'mounts_prepared',
  'chroot_complete',
  'fork_parent',
  'fork_child',
  'exec_main_begin',
  'main_enter',
  'login_fd_read_begin',
  'login_fd_eof',
  'login_document_valid',
  'init_begin',
  'init_complete',
  'init_ctx_begin',
  'init_ctx_complete',
  'login_begin',
]);

function knownStartupStage(stage) {
  return typeof stage === 'string' && WRAPPER_STARTUP_STAGES.has(stage) ? stage : '';
}

// The only signals the backend's stop path delivers, with the exit code each
// must produce (128 + signal number). This is an allowlist rather than a fault
// denylist: a denylist silently accepts anything unlisted, such as SIGUSR1 or a
// name this build does not know, and each of those would still let the UI vouch
// for a controlled cleanup it cannot account for.
const WRAPPER_WATCHDOG_SIGNAL_EXIT_CODES = new Map([
  ['SIGTERM', 143],
  ['SIGKILL', 137],
]);
const WRAPPER_WATCHDOG_PHASES = new Set([
  'authenticating',
  'authenticated_initializing',
]);

// watchdogCleanupConfirmed mirrors the backend's corroboration rule. Saying "our
// watchdog cleaned this up, it is not a crash" and suppressing the credential
// prompt are assertions about cause, so they require the whole record to agree,
// not just the reason string. An older backend that sends no initiator is
// treated as unattributed rather than assumed. The renderer re-derives this
// independently: a compromised or buggy backend must not be able to unlock the
// stronger copy just by setting one token.
function watchdogCleanupConfirmed(lastExit) {
  if (!lastExit || lastExit.terminationInitiator !== 'initialization_watchdog') return false;
  if (lastExit.reason !== 'initialization_timeout') return false;
  if (!(Number.isSafeInteger(lastExit.deadlineMs) && lastExit.deadlineMs > 0)) return false;
  // A record with no run generation cannot be tied to the run it claims to
  // describe, so its attribution is unverifiable.
  if (!(Number.isSafeInteger(lastExit.runGeneration) && lastExit.runGeneration > 0)) return false;
  if (!(Number.isSafeInteger(lastExit.currentAttempt) && lastExit.currentAttempt > 0)) return false;
  // attempt and currentAttempt are written from one attempt number; a
  // disagreement means the record was assembled from two different attempts.
  if (!(Number.isSafeInteger(lastExit.attempt) && lastExit.attempt > 0)) return false;
  if (lastExit.attempt !== lastExit.currentAttempt) return false;
  if (!WRAPPER_WATCHDOG_PHASES.has(lastExit.phase)) return false;
  if (lastExit.portReady !== false) return false;
  return watchdogSignalMatchesStop(lastExit);
}

// watchdogSignalMatchesStop requires the signal and exit code to agree. An absent
// signal is tolerated because the Wrapper can exit on its own as the deadline
// fires, but its code must still be a plausible process status.
function watchdogSignalMatchesStop(lastExit) {
  const { signal, exitCode } = lastExit;
  if (signal === undefined || signal === null || signal === '') {
    return exitCode === undefined || exitCode === null
      || (Number.isInteger(exitCode) && exitCode >= 0 && exitCode <= 255);
  }
  const expected = WRAPPER_WATCHDOG_SIGNAL_EXIT_CODES.get(signal);
  return expected !== undefined && exitCode === expected;
}

const LEGACY_BROWSER_DOWNLOAD_LIMIT = 128 * 1024 * 1024;
const LOCAL_PROXY_ERROR_STATUSES = new Set([502, 503, 504]);
// Trust contract for mapping a StoreServices code to a business message. The
// renderer independently re-checks every field of the derived schema-v2
// diagnostic so a bare integer, an unaudited binary, or a newer schema can
// never reach the constrained text. These constants must move in lockstep with
// the manager's derived diagnostic; a mismatch fails closed to the safe copy.
// STORESERVICES_AUDITED_SHA256 is a BUILD-TIME audited-identity claim, not a
// runtime measurement: it is the libstoreservicescore.so this UI was reviewed
// against. It is kept byte-identical to the Go manager constant and to
// appliance/versions.lock STORESERVICESCORE_BINARY_SHA256 by the mechanical gate
// in test/storeservices-fingerprint-consistency.test.js; the actual loaded
// library identity is enforced separately by the appliance build's locked-.so
// verification and the install-time runtime SHA.
const STORESERVICES_DIAGNOSTIC_SCHEMA_VERSION = 2;
const STORESERVICES_ERROR_CATEGORY = 'StoreServicesCore';
const STORESERVICES_AUDITED_SHA256 = '6238261363d7943f1a0a27116b52544831f3e295e8533a634641fe5b09d92c7e';
// The one code-8 mapping the renderer is allowed to translate: the audited
// generic-category URLBagKeyNotFound for the authenticateAccount Bag key.
const STORESERVICES_URLBAG_KEY_NOT_FOUND = {
  code: 8,
  hex: '0x00000008',
  errorName: 'URLBagKeyNotFound',
  urlBagKey: 'authenticateAccount',
};
const elements = {
  form: document.getElementById('job-form'), urls: document.getElementById('music-urls'), quality: document.getElementById('quality'),
  existingPolicy: document.getElementById('existing-policy'), qualityHelp: document.getElementById('quality-help'), submit: document.getElementById('download-btn'),
  wrapper: document.getElementById('wrapper-status'), downloader: document.getElementById('downloader-status'), twoFA: document.getElementById('twofa-section'),
  twoFACode: document.getElementById('twofa-code'), twoFASubmit: document.getElementById('twofa-submit'), jobs: document.getElementById('jobs'),
  jobsEmpty: document.getElementById('jobs-empty'), files: document.getElementById('files'), filesEmpty: document.getElementById('files-empty'),
  archive: document.getElementById('archive-link'), log: document.getElementById('log-container'), refresh: document.getElementById('refresh-btn'),
  repair: document.getElementById('repair-service-button'),
  readiness: document.getElementById('download-readiness'),
  defaultQuality: document.getElementById('default-quality'),
  defaultExistingPolicy: document.getElementById('default-existing-policy'),
  defaultDownloadStatus: document.getElementById('default-download-status'),
  downloadConcurrency: document.getElementById('download-concurrency'),
  downloadConcurrencySave: document.getElementById('download-concurrency-save'),
  downloadConcurrencyReset: document.getElementById('download-concurrency-reset'),
  downloadConcurrencyStatus: document.getElementById('download-concurrency-status'),
  desktopSettings: document.getElementById('desktop-settings'),
  desktopServiceSettings: document.getElementById('desktop-service-settings'),
  desktopSettingsBrowserNote: document.getElementById('desktop-settings-browser-note'),
  desktopSettingsStatus: document.getElementById('desktop-settings-status'),
};

const desktopSettingsActions = [
  ['settings-download-location', 'chooseDownloadLocation', '选择下载位置'],
  ['settings-startup', 'openDownloadSettings', '安装与启动设置'],
  ['settings-credentials', 'openCredentials', '配置凭据'],
  ['settings-playback-login', 'openPlaybackLogin', '打开 Apple 登录与播放'],
  ['settings-start-service', 'startService', '启动/重新连接'],
  ['settings-diagnostics', 'showDiagnostics', '查看诊断信息'],
  ['settings-export-logs', 'exportDiagnostics', '导出脱敏日志'],
  ['export-logs', 'exportDiagnostics', '导出脱敏日志'],
  ['settings-repair-runtime', 'repairRuntime', '修复运行时'],
  ['settings-stop-service', 'stopService', '停止服务'],
];
let desktopSettingsPending = false;

function updateDesktopSettingsControls() {
  let available = false;
  for (const [id, method] of desktopSettingsActions) {
    const supported = typeof window.desktopShell?.[method] === 'function';
    available ||= supported;
    const control = document.getElementById(id);
    if (control) control.disabled = !supported || desktopSettingsPending || repairInProgress;
    if (id === 'export-logs' && control) control.hidden = !supported;
  }
  if (elements.desktopSettings) elements.desktopSettings.hidden = !available;
  if (elements.desktopServiceSettings) elements.desktopServiceSettings.hidden = !available;
  if (elements.desktopSettingsBrowserNote) elements.desktopSettingsBrowserNote.hidden = available;
  if (elements.repair) elements.repair.disabled = desktopSettingsPending || repairInProgress;
}

function reportDesktopSettings(message) {
  if (elements.desktopSettingsStatus) elements.desktopSettingsStatus.textContent = message;
}

async function requestDesktopSettingsAction(method, label) {
  if (desktopSettingsPending || repairInProgress || typeof window.desktopShell?.[method] !== 'function') return;
  if (method === 'repairRuntime') return requestRuntimeRepair();
  desktopSettingsPending = true;
  updateDesktopSettingsControls();
  const exportStatusBeforeRequest = elements.desktopSettingsStatus?.textContent || '';
  reportDesktopSettings(`正在${label}…`);
  try {
    const result = method === 'exportDiagnostics'
      ? await window.desktopShell[method]([...elements.log.children].map((item) => item.textContent.slice(0, 2048))
        .concat([`Wrapper: ${elements.wrapper.textContent}`, `下载服务: ${elements.downloader.textContent}`, `界面状态: ${exportStatusBeforeRequest}`]).slice(-100))
      : await window.desktopShell[method]();
    if (result?.cancelled) { reportDesktopSettings(method === 'exportDiagnostics' ? '已取消日志导出。' : '已取消操作，服务保持原状。'); return; }
    if (!result?.ok) {
      reportDesktopSettings([result?.error?.message, result?.error?.action].filter(Boolean).join(' — ') || '操作未能完成，请查看诊断信息后重试。');
      return;
    }
    if (method === 'openPlaybackLogin') {
      const states = {saved:'用户 token 已加密保存并交付；不代表所有歌曲均可下载。',saved_pending:'用户 token 已加密保存，等待服务空闲后应用。',failed:'用户 token 同步失败，可重新打开此入口重试或手动填写。',not_found:'官方网页已打开，等待登录后的用户 token。',ambiguous:'检测到不明确的用户 token，未覆盖下载凭据。',not_enabled:'官方网页已打开，未启用下载授权同步。'};
      reportDesktopSettings(states[result.syncState] || '官方网页已打开；网页登录与下载授权分别判定。');
    } else reportDesktopSettings(`${label}：已完成。`);
    if (method === 'startService') await Promise.all([refreshCapabilitiesNow({ force: true }), loadJobs(), loadKnownFiles()]);
  } catch {
    // Do not expose raw IPC exceptions, paths or native error details.
    reportDesktopSettings('操作未能完成，请查看诊断信息后重试。');
  } finally {
    desktopSettingsPending = false;
    updateDesktopSettingsControls();
  }
}

// Archives are task-scoped. Keep the old global anchor inert for v1 DOM
// compatibility and expose downloads only from the matching task card.
elements.archive.href = '';
elements.archive.hidden = true;

function addLog(message, kind = 'info') {
  const item = document.createElement('div');
  item.className = `log-entry ${kind}`;
  item.textContent = `[${new Date().toLocaleTimeString()}] ${message}`;
  elements.log.append(item);
  while (elements.log.children.length > 100) elements.log.firstElementChild.remove();
  elements.log.scrollTop = elements.log.scrollHeight;
}

let repairInProgress = false;
let capabilityRequest = null;
let capabilityGeneration = 0;
let capabilityRefreshTimer = null;
let capabilityRecoveryLogged = false;

function desktopRepairAvailable() {
  return typeof window.desktopShell?.repairRuntime === 'function';
}

function updateRepairButton() {
  if (!elements.repair) return;
  elements.repair.hidden = !desktopRepairAvailable();
}

async function requestRuntimeRepair() {
  if (!desktopRepairAvailable() || repairInProgress || desktopSettingsPending) return;
  repairInProgress = true;
  updateDesktopSettingsControls();
  reportDesktopSettings('正在修复运行时…');
  // A repair can replace the backend while an older capabilities request is
  // still waiting on the previous listener.  Invalidate that generation
  // before crossing the repair boundary so its eventual response cannot
  // resurrect stale "online" state.
  invalidateCapabilities();
  elements.repair.disabled = true;
  elements.repair.textContent = '修复中…';
  try {
    const result = await window.desktopShell.repairRuntime();
    if (result?.cancelled) {
      reportDesktopSettings('已取消运行时修复。');
      addLog('已取消运行时修复。', 'warning');
      return;
    }
    if (!result?.ok) {
      const error = result?.error || {};
      const stage = error.code === 'network_preflight_failed'
        ? `（阶段：${knownNetworkPreflightStage(error.details?.stage)}）`
        : '';
      const message = [error.message, stage, error.action].filter(Boolean).join(' — ') || '运行时修复失败。';
      reportDesktopSettings(message);
      addLog(message, 'error');
      return;
    }
    addLog('运行时修复完成，正在重新连接服务。', 'success');
    reportDesktopSettings('运行时修复完成，正在重新连接服务。');
    await Promise.all([refreshCapabilitiesNow({ force: true }), loadJobs(), loadKnownFiles()]);
  } catch (error) {
    reportDesktopSettings('运行时修复未能完成，请查看诊断信息后重试。');
    addLog(`运行时修复失败：${error?.message || '未知错误'}`, 'error');
  } finally {
    repairInProgress = false;
    elements.repair.disabled = false;
    elements.repair.textContent = '诊断与修复';
    updateRepairButton();
    updateDesktopSettingsControls();
  }
}

let apiAccessKey = '';
let apiAccessKeyPromise = null;

async function requestLocalAPIAccessKey() {
  if (apiAccessKeyPromise) return apiAccessKeyPromise;
  apiAccessKeyPromise = (async () => {
    const value = window.prompt('请输入本地 API 访问密钥。Docker/CLI 用户可在 data/api-access-token 找到它，或在 .env 中设置 AMDL_API_TOKEN。');
    const accessKey = String(value || '').trim();
    if (!accessKey) throw new Error('未提供本地 API 访问密钥。');
    if (!/^[0-9a-f]{64}$/i.test(accessKey)) throw new Error('本地 API 访问密钥必须是 64 位十六进制字符。');
    apiAccessKey = accessKey;
    return accessKey;
  })();
  try {
    return await apiAccessKeyPromise;
  } finally {
    apiAccessKeyPromise = null;
  }
}

function apiRequestHeaders(headers = {}) {
  const result = { ...headers };
  if (apiAccessKey) result.Authorization = `Bearer ${apiAccessKey}`;
  else delete result.Authorization;
  return result;
}

async function authenticatedFetch(path, options = {}, allowAccessKeyRetry = true) {
  let response;
  try {
    response = await fetch(path, {
      ...options,
      // The API is bearer-only. Never send the legacy host-wide session cookie,
      // because localhost cookies are shared across ports.
      credentials: 'omit',
      headers: apiRequestHeaders(options.headers || {}),
    });
  } catch {
    // Do not surface browser/transport text: it can vary by platform and is
    // not a trustworthy service state. Callers that render capabilities use
    // this private marker to invalidate the old ready snapshot.
    const failure = new Error('本地 WSL 服务桥暂时不可用。');
    failure.transportFailure = true;
    throw failure;
  }
  if (response.status === 401 && allowAccessKeyRetry) {
    await requestLocalAPIAccessKey();
    return authenticatedFetch(path, options, false);
  }
  if (response.status === 401 && !allowAccessKeyRetry) apiAccessKey = '';
  return response;
}

async function api(path, options = {}, allowAccessKeyRetry = true) {
  const response = await authenticatedFetch(path, {
    ...options,
    headers: { Accept: 'application/json', ...(options.headers || {}) },
  }, allowAccessKeyRetry);
  const contentType = response.headers.get('content-type') || '';
  const data = contentType.includes('application/json') ? await response.json() : null;
  if (!response.ok) {
    const error = data && data.error ? data.error : { message: `请求失败（HTTP ${response.status}）` };
    const explanation = [error.message, error.action].filter(Boolean).join(' — ');
    const failure = new Error(explanation || `请求失败（HTTP ${response.status}）`);
    failure.status = response.status;
    if (typeof error.code === 'string' && /^[a-z_]{1,64}$/.test(error.code)) failure.code = error.code;
    throw failure;
  }
  return data;
}

async function downloadWithAccessKey(path, filename) {
  const safeFilename = String(filename || 'download').replace(/[\\/:*?"<>|]/g, '_');
  let fileHandle = null;
  if (typeof window.showSaveFilePicker === 'function') {
    // localhost is a secure context in Chromium. Ask for the destination while
    // still inside the click gesture, then stream the authenticated response
    // directly to disk instead of retaining a large archive in renderer RAM.
    fileHandle = await window.showSaveFilePicker({ suggestedName: safeFilename });
  }
  const response = await authenticatedFetch(path, { headers: { Accept: 'application/octet-stream, application/zip' } });
  if (!response.ok) {
    const contentType = response.headers.get('content-type') || '';
    const data = contentType.includes('application/json') ? await response.json().catch(() => null) : null;
    const details = data && data.error ? [data.error.message, data.error.action].filter(Boolean).join(' — ') : '';
    throw new Error(details || `下载失败（HTTP ${response.status}）。`);
  }
  if (fileHandle && response.body && typeof response.body.pipeTo === 'function') {
    const writable = await fileHandle.createWritable();
    await response.body.pipeTo(writable);
    return;
  }
  const rawContentLength = response.headers.get('content-length');
  const contentLength = rawContentLength ? Number(rawContentLength) : Number.NaN;
  if (!Number.isSafeInteger(contentLength) || contentLength < 0 || contentLength > LEGACY_BROWSER_DOWNLOAD_LIMIT) {
    if (response.body && typeof response.body.cancel === 'function') await response.body.cancel().catch(() => {});
    throw new Error('当前浏览器无法安全地流式保存此文件。请使用最新版 Chrome/Edge，或使用带 Bearer 请求头的 CLI 下载。');
  }
  const blob = await response.blob();
  const objectURL = URL.createObjectURL(blob);
  const temporary = document.createElement('a');
  temporary.href = objectURL;
  temporary.download = safeFilename;
  temporary.hidden = true;
  document.body.append(temporary);
  temporary.click();
  temporary.remove();
  window.setTimeout(() => URL.revokeObjectURL(objectURL), 0);
}

function authenticateDownloadLink(link, path, filename) {
  link.addEventListener('click', (event) => {
    // Electron injects its desktop capability in the main process, so its
    // native streaming download remains untouched. Browser users keep their
    // bearer only in this renderer's memory and therefore use fetch/blob.
    if (!apiAccessKey) return;
    event.preventDefault();
    downloadWithAccessKey(path, filename).catch((error) => {
      if (error && error.name === 'AbortError') return;
      addLog(error.message, 'error');
    });
  });
}

function jobId(job) { return job.id; }
function jobStatus(job) { return job.status || 'unknown'; }
function jobURL(job) { return job.url || 'Apple Music URL'; }
function jobTracks(job) {
  return Array.isArray(job.items) ? job.items : [];
}
function qualityText(quality) {
  return ({ 'aac-lc': 'AAC-LC 256kbps', alac: 'ALAC 无损', aac: 'AAC 256kbps', atmos: 'Dolby Atmos' })[quality] || quality || '默认音质';
}
function existingPolicyText(policy) {
  return ({ skip: '跳过已有文件', overwrite: '覆盖已有文件', fail: '已有文件时失败' })[policy] || policy || '跳过已有文件';
}
function statusText(status) {
  return ({ queued: '排队中', running: '下载中', cancelling: '正在取消', succeeded: '完成', partial: '部分完成', failed: '失败', cancelled: '已取消', interrupted: '已中断', skipped: '已跳过', unavailable: '不可用' })[status] || status;
}

function playlistFailureText(job) {
  const code = job && job.errorCode;
  return ({
    metadata_transient: '曲目信息获取失败，请稍后重试。',
    metadata_failed: '曲目信息获取失败，无法开始下载。',
    download_result_missing: '未能确认下载结果，请检查任务后重试。',
    playlist_auth_required: '个人歌单需要配置 Media-User-Token。',
    playlist_access_denied: '当前账号无权访问此个人歌单。',
    playlist_not_found: '找不到此歌单，或歌单已被删除。',
    playlist_pagination_failed: '歌单分页未能完整读取，请稍后重试。',
    playlist_no_downloadable_tracks: '歌单中没有可下载的曲目。',
    playlist_incomplete: '公开歌单页面的结构化数据不完整。',
    public_playlist_data_unavailable: '公开歌单页面的结构化数据不可用。',
    playlist_track_unavailable: '歌单中有曲目暂不可用。',
  })[code] || '';
}
function isActive(job) { return ['queued', 'running', 'cancelling'].includes(jobStatus(job)); }
function needsEventStream(job) { return ['running', 'cancelling'].includes(jobStatus(job)); }
function hasQueuedJobs() { return [...state.jobs.values()].some((job) => jobStatus(job) === 'queued'); }

function count(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
}

function progressText(job) {
  const total = count(job.total);
  if (!total) return '';
  const completed = count(job.succeeded) + count(job.failed) + count(job.skipped) + count(job.unavailable);
  return `已处理：${Math.min(total, completed)}/${total} · 成功下载：${count(job.succeeded)} · 失败：${count(job.failed)} · 已跳过：${count(job.skipped)} · 不可用：${count(job.unavailable)}`;
}

function canRetryJob(job) {
  if (queueNeedsRecovery()) return false;
  if (!['failed', 'partial', 'interrupted'].includes(jobStatus(job))) return false;
  const tracks = jobTracks(job);
  return !tracks.length || tracks.some((track) => ['failed', 'interrupted'].includes(track.status));
}

function trackFailureText(track) {
  const code = String(track.errorCode || '');
  const match = /^(metadata|manifest|media|credentials|output|lyrics|tagging|publish|validation)_(failed|transient)$/.exec(code);
  if (match) {
    const stage = { metadata: '曲目信息获取失败', manifest: '媒体清单获取失败', media: '媒体传输或解密失败', credentials: '缺少有效下载凭据', output: '无法准备目标文件', lyrics: '歌词文件写入失败', tagging: '媒体标签写入失败', publish: '媒体文件保存失败', validation: '媒体文件不完整或损坏' }[match[1]];
    return `${stage}${match[2] === 'transient' ? '，可重试' : ''}`;
  }
  return ({ track_unavailable: '没有支持的可播放资源', duplicate_track: '重复曲目，已跳过', download_result_missing: '完成状态未确认，请查看日志或重试' })[code] || (track.status === 'failed' ? '下载失败，请重试或查看脱敏诊断' : '');
}

function updateServiceStatus() {
  if (queueNeedsRecovery()) {
    elements.downloader.textContent = '任务状态保存失败，请检查磁盘并重启服务恢复';
    elements.downloader.className = 'status-value error';
    updateReadiness();
    return;
  }
  const active = [...state.jobs.values()].some(isActive);
  elements.downloader.textContent = active ? '队列处理中' : '就绪';
  elements.downloader.className = `status-value ${active ? 'busy' : 'ready'}`;
  updateReadiness();
}

function queueNeedsRecovery() { return state.capabilities?.queue?.state === 'error'; }

// Submission readiness is independent of the lamp's stronger login claim.
// Token-only AAC-LC can be admitted without a Wrapper login; neither fact
// guarantees a later license request will succeed.
function downloadSubmissionReady() {
  const quality = elements.defaultQuality.value;
  return state.capabilitiesState === 'ready'
    && state.capabilities?.queue?.state === 'ready'
    && state.capabilities?.jobExecutorConfigured === true
    && state.capabilities?.qualities?.[quality]?.available === true;
}

function updateReadiness() {
  const available = downloadSubmissionReady();
  const loginCompleted = state.capabilitiesState === 'ready'
    && state.capabilities?.wrapper?.status === 'online'
    && state.capabilities?.wrapper?.loginCompleted === true;
  const verified = available && loginCompleted;
  elements.readiness.classList.toggle('ready', verified);
  elements.readiness.classList.toggle('unverified', available && !verified);
  elements.readiness.classList.toggle('not-ready', !available);
  const label = verified ? '已登录 · 可以提交' : available ? '登录未确认' : '不可下载';
  elements.readiness.querySelector('.readiness-label').textContent = label;
  elements.readiness.setAttribute('aria-label', `${label}；打开设置与状态`);
  elements.readiness.title = verified ? '本次 Wrapper 登录已完成，可以提交任务；不保证会话持续有效或解密许可可用。'
    : available ? '凭据已配置，可提交所选音质的任务；尚无本次 Wrapper 登录完成证据。' : '当前无法提交所选音质的任务；打开设置与状态。';
  window.dispatchEvent(new CustomEvent('amdl:download-readiness', { detail: { ready: available } }));
}

async function loadDefaultDownloadPreferences() {
  let selected = null;
  try {
    const stored = JSON.parse(window.localStorage.getItem('amdl.downloadDefaults') || 'null');
    if (stored && typeof stored === 'object') selected = stored;
  } catch { /* Keep the safe initial defaults. */ }
  try {
    if (window.desktopShell?.getDownloadPreferences) selected = await window.desktopShell.getDownloadPreferences();
  } catch { /* Browser deployments and offline shell keep their local defaults. */ }
  if (['aac-lc', 'alac', 'aac', 'atmos'].includes(selected?.quality)) elements.defaultQuality.value = selected.quality;
  if (['skip', 'overwrite', 'fail'].includes(selected?.existingPolicy)) elements.defaultExistingPolicy.value = selected.existingPolicy;
  updateReadiness();
}

async function saveDefaultDownloadPreferences() {
  const selected = { quality: elements.defaultQuality.value, existingPolicy: elements.defaultExistingPolicy.value };
  updateReadiness();
  try { window.localStorage.setItem('amdl.downloadDefaults', JSON.stringify(selected)); } catch { /* Optional local fallback. */ }
  if (window.desktopShell?.setDownloadPreferences) {
    try {
      const result = await window.desktopShell.setDownloadPreferences(selected);
      elements.defaultDownloadStatus.textContent = result?.ok ? '一键下载默认值已保存。' : '默认值未能保存，请重试。';
    } catch { elements.defaultDownloadStatus.textContent = '默认值未能保存，请重试。'; }
  } else elements.defaultDownloadStatus.textContent = '一键下载默认值已保存。';
}

function renderDownloadConcurrencySetting(setting) {
  if (!elements.downloadConcurrency || !setting) return;
  const value = Number(setting.downloadConcurrency);
  const minimum = Number(setting.minDownloadConcurrency) || 1;
  const maximum = Number(setting.maxDownloadConcurrency) || 8;
  if (Number.isInteger(value) && value >= minimum && value <= maximum) {
    elements.downloadConcurrency.value = String(value);
  }
  elements.downloadConcurrency.min = String(minimum);
  elements.downloadConcurrency.max = String(maximum);
  if (elements.downloadConcurrencyStatus) {
    elements.downloadConcurrencyStatus.textContent = setting.overridden
      ? '已使用自定义并行数。'
      : '默认并行数：' + (Number(setting.defaultDownloadConcurrency) || 3) + '。';
  }
}

async function loadDownloadConcurrencySetting() {
  if (!elements.downloadConcurrency) return true;
  try {
    renderDownloadConcurrencySetting(await api('/api/v1/settings/download-concurrency', { cache: 'no-store' }));
    return true;
  } catch {
    if (elements.downloadConcurrencyStatus) elements.downloadConcurrencyStatus.textContent = '无法读取下载设置。';
    addLog('无法读取下载设置。', 'error');
    return false;
  }
}

async function saveDownloadConcurrencySetting() {
  if (!elements.downloadConcurrency) return;
  const value = Number(elements.downloadConcurrency.value);
  if (!Number.isInteger(value) || value < 1 || value > 8) {
    if (elements.downloadConcurrencyStatus) elements.downloadConcurrencyStatus.textContent = '请输入 1 到 8 之间的整数。';
    return;
  }
  elements.downloadConcurrencySave.disabled = true;
  try {
    const setting = await api('/api/v1/settings/download-concurrency', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ downloadConcurrency: value }),
    });
    renderDownloadConcurrencySetting(setting);
    addLog('已将默认并行下载数设置为 ' + value + '。', 'success');
  } catch {
    if (elements.downloadConcurrencyStatus) elements.downloadConcurrencyStatus.textContent = '保存下载设置失败。';
  } finally {
    elements.downloadConcurrencySave.disabled = false;
  }
}

async function resetDownloadConcurrencySetting() {
  if (!elements.downloadConcurrencyReset) return;
  elements.downloadConcurrencyReset.disabled = true;
  try {
    const setting = await api('/api/v1/settings/download-concurrency', { method: 'DELETE' });
    renderDownloadConcurrencySetting(setting);
    addLog('已恢复默认并行下载数。', 'success');
  } catch {
    if (elements.downloadConcurrencyStatus) elements.downloadConcurrencyStatus.textContent = '恢复默认设置失败。';
  } finally {
    elements.downloadConcurrencyReset.disabled = false;
  }
}

function qualityAvailable(quality) {
  if (state.capabilitiesState !== 'ready') return false;
  const capabilities = state.capabilities || {};
  const qualities = capabilities.qualities || capabilities.quality || {};
  const entry = qualities[quality];
  return entry === undefined || entry === true || entry.available === true;
}

// A complete non-secret preference snapshot; never merge arbitrary response keys.
const DOWNLOAD_PREFERENCE_FIELDS = {
  downloadRegion: 'string', metadataRegion: 'string', lyricsRegion: 'string', language: 'string',
  saveLyrics: 'boolean', embedLyrics: 'boolean', lyricsType: 'string', lyricsFormat: 'string',
  embedCover: 'boolean', coverFormat: 'string', coverSize: 'string',
  saveArtistCover: 'boolean', saveAnimatedArtwork: 'boolean', useSongInfoForPlaylist: 'boolean',
  alacMax: 'number', mvMax: 'number',
  songFileFormat: 'string',
};
let downloadPreferencesReady = false;
let downloadPreferencesBusy = false;
function validDownloadPreferenceValues(values) {
  if (!values || Object.keys(values).length !== Object.keys(DOWNLOAD_PREFERENCE_FIELDS).length ||
    Object.entries(DOWNLOAD_PREFERENCE_FIELDS).some(([key, type]) => typeof values[key] !== type)) return false;
  if (['downloadRegion', 'metadataRegion', 'lyricsRegion'].some((key) => values[key] !== '' && !/^[a-z]{2}$/.test(values[key]))) return false;
  return (values.language === '' || /^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$/.test(values.language)) &&
    ['lyrics', 'syllable-lyrics'].includes(values.lyricsType) && ['lrc', 'ttml'].includes(values.lyricsFormat) &&
    ['jpg', 'png', 'original'].includes(values.coverFormat) && /^([1-9][0-9]{0,3}|10000)x([1-9][0-9]{0,3}|10000)$/.test(values.coverSize) &&
    Number.isInteger(values.alacMax) && values.alacMax >= 8000 && values.alacMax <= 384000 &&
    Number.isInteger(values.mvMax) && values.mvMax >= 1 && values.mvMax <= 4320 && validSongFilenameTemplate(values.songFileFormat);
}
function validSongFilenameTemplate(value) {
  if (typeof value !== 'string' || !value.trim() || new TextEncoder().encode(value).length > 256 || /[\\/<>"|?*\x00-\x1f\x7f]/.test(value) || /\.m4a$/i.test(value) || ['.', '..'].includes(value)) return false;
  let valid = true;
  const allowed = new Set(['title', 'artist', 'album', 'album_artist', 'composer', 'genre', 'tracknum', 'disk', 'year', 'codec', 'song_id', 'album_id', 'artist_id', 'SongId', 'SongNumer', 'SongName', 'DiscNumber', 'TrackNumber', 'Quality', 'Tag', 'Codec']);
  const rest = value.replace(/\{([A-Za-z_]+)(?::(0?[1-9])d)?\}/g, (_token, name, width) => {
    if (!allowed.has(name) || (width && !['tracknum', 'disk', 'year'].includes(name))) valid = false;
    return '';
  });
  return valid && !/[{}:]/.test(rest);
}
const filenameBuilderHistory = [];
let filenameBuilderApplying = false;
function updateFilenameExample() {
  const input = document.getElementById('preference-songFileFormat');
  const output = document.getElementById('filename-example');
  if (!input || !output) return;
  if (!validSongFilenameTemplate(input.value)) { output.textContent = '请选择字段组合成有效模板；不要填写路径或扩展名。'; return; }
  const values = {title:'示例歌曲',artist:'示例艺人',album:'示例专辑',album_artist:'示例艺人',composer:'示例作曲者',genre:'流行',tracknum:'1',disk:'1',year:'2026',codec:'ALAC',song_id:'123456',album_id:'123000',artist_id:'120000',SongNumer:'01',SongName:'示例歌曲',SongId:'123456',DiscNumber:'1',TrackNumber:'1',Quality:'无损',Tag:'',Codec:'ALAC'};
  output.textContent = input.value.replace(/\{([A-Za-z_]+)(?::(0?[1-9])d)?\}/g, (_token,name,width) => width ? values[name].padStart(Number(width),'0') : values[name]).replace(/[\\/<>:"|?*]/g,'_').trim() + '（扩展名自动添加）';
}
function initializeFilenameBuilder() {
  const builder = document.getElementById('filename-builder');
  const input = document.getElementById('preference-songFileFormat');
  if (!builder || !input) return;
  const control = (parent,label,action,aria=label) => {
    const b=document.createElement('button');b.type='button';b.textContent=label;b.setAttribute('aria-label',aria);b.addEventListener('click',()=>{
      if (!document.getElementById('download-preferences-fields').disabled) action();
    });document.getElementById(parent).appendChild(b);return b;
  };
  function apply(value,start=value.length,end=start,remember=true) {
    if (new TextEncoder().encode(value).length>256) { preferenceMessage('文件名模板最多 256 字节，请先移除部分字段。'); return; }
    if (remember) { filenameBuilderHistory.push({value:input.value,start:input.selectionStart,end:input.selectionEnd});if(filenameBuilderHistory.length>20)filenameBuilderHistory.shift(); }
    input.value=value;input.focus();input.setSelectionRange(start,end);
    filenameBuilderApplying=true;input.dispatchEvent(new Event('input',{bubbles:true}));filenameBuilderApplying=false;
    undo.disabled=filenameBuilderHistory.length===0;
  }
  function insert(text) { const start=input.selectionStart??input.value.length,end=input.selectionEnd??start;apply(input.value.slice(0,start)+text+input.value.slice(end),start+text.length); }
  for (const [label,value] of [['序号 · 歌名','{tracknum:02d}. {title}'],['艺人 · 歌名','{artist} - {title}'],['碟号 · 序号 · 歌名','{disk}-{tracknum:02d}. {title}'],['只用歌名','{title}']]) control('filename-presets',label,()=>apply(value));
  for (const [label,value] of [['歌名','{title}'],['艺人','{artist}'],['专辑','{album}'],['专辑艺人','{album_artist}'],['序号（两位）','{tracknum:02d}'],['碟号','{disk}'],['年份','{year}'],['编码','{codec}'],['作曲者','{composer}'],['流派','{genre}'],['歌曲 ID','{song_id}'],['专辑 ID','{album_id}'],['艺人 ID','{artist_id}']]) control('filename-fields',label,()=>insert(value),'添加'+label);
  for (const [label,value] of [['空格',' '],['点号','. '],['横线',' - '],['下划线','_']]) control('filename-separators',label,()=>insert(value),'分隔符：'+label);
  const undo=control('filename-separators','撤销上一步',()=>{const old=filenameBuilderHistory.pop();if(old)apply(old.value,old.start,old.end,false);});undo.id='filename-undo';undo.disabled=true;
  control('filename-separators','清空',()=>apply(''));
  input.addEventListener('input',()=>{if(!filenameBuilderApplying){filenameBuilderHistory.length=0;undo.disabled=true;}updateFilenameExample();});
  updateFilenameExample();
}
function renderDownloadPreferences(result) {
  const values = result?.settings;
  if (!validDownloadPreferenceValues(values) || result.appliesTo !== 'next_job') {
    throw new Error('invalid preference receipt');
  }
  for (const [key, type] of Object.entries(DOWNLOAD_PREFERENCE_FIELDS)) {
    const input = document.getElementById('preference-' + key);
    if (!input) continue;
    if (type === 'boolean') input.checked = values[key];
    else {
      const value = String(values[key]);
      // Preserve a configured valid region/language/limit not in the presets.
      if (input.tagName === 'SELECT' && !Array.from(input.options || []).some((option) => option.value === value)) {
        const option = document.createElement('option'); option.value = value; option.textContent = value; input.appendChild(option);
      }
      input.value = value;
    }
  }
  downloadPreferencesReady = true;
  filenameBuilderHistory.length = 0;
  const undo = document.getElementById('filename-undo'); if (undo) undo.disabled = true;
  updateFilenameExample();
}
function preferenceMessage(message) {
  const status = document.getElementById('download-preferences-status');
  if (status) status.textContent = message;
}
async function loadDownloadPreferences() {
  const fields = document.getElementById('download-preferences-fields');
  if (!fields) return;
  fields.disabled = true;
  try {
    renderDownloadPreferences(await api('/api/v1/settings/download', { cache: 'no-store' }));
    fields.disabled = false;
    preferenceMessage('');
  } catch {
    downloadPreferencesReady = false;
    preferenceMessage('请连接服务；旧版后端需先更新。');
  }
}
async function changeDownloadPreferences(event, reset = false) {
  event?.preventDefault();
  if (offlineShell || !downloadPreferencesReady || downloadPreferencesBusy) return;
  const fields = document.getElementById('download-preferences-fields');
  const values = {};
  for (const [key, type] of Object.entries(DOWNLOAD_PREFERENCE_FIELDS)) {
    const input = document.getElementById('preference-' + key);
    values[key] = type === 'boolean' ? input.checked : type === 'number' ? Number(input.value) : input.value;
  }
  if (!reset && !validSongFilenameTemplate(values.songFileFormat)) {
    preferenceMessage('文件名模板无效：请使用下方列出的参数，不包含路径或扩展名。');
    return;
  }
  downloadPreferencesBusy = true; fields.disabled = true;
  try {
    const options = reset ? { method: 'DELETE' } : { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(values) };
    renderDownloadPreferences(await api('/api/v1/settings/download', options));
    preferenceMessage(reset ? '已恢复默认值，下一项开始时生效。' : '已保存，下一项开始时生效。');
  } catch {
    preferenceMessage('保存失败，请重试。');
  } finally { downloadPreferencesBusy = false; fields.disabled = false; }
}

function capabilityMessage(quality) {
  if (state.capabilitiesState === 'reconnecting') return '本地 WSL 服务桥正在重连。';
  const capabilities = state.capabilities || {};
  const qualities = capabilities.qualities || capabilities.quality || {};
  const entry = qualities[quality];
  if (!entry || entry === true || entry.available === true) return '';
  return entry.action || entry.reason || '当前配置不可用';
}

// storeServicesURLBagKeyMissing reports whether a sanitized exit diagnostic
// satisfies the full end-to-end trust contract for the one business message the
// renderer is allowed to bind: a trusted StoreServicesCore code 8 that decodes
// to URLBagKeyNotFound on the exact audited binary. Every clause must hold —
// trusted flag, canonical decimal/hex agreement, supported schema, generic
// StoreServicesCore category, the URLBagKeyNotFound name, the authenticateAccount
// key, and a byte-exact StoreServicesCore fingerprint. Anything missing, unknown,
// mismatched, or out of range fails closed so a bare integer, a forged code, or a
// different binary can never reach the mapped text.
function storeServicesURLBagKeyMissing(lastExit) {
  if (!lastExit || typeof lastExit !== 'object') return false;
  const authCode = lastExit.authCode;
  const mapping = STORESERVICES_URLBAG_KEY_NOT_FOUND;
  return lastExit.authCodeTrusted === true
    && lastExit.diagnosticSchemaVersion === STORESERVICES_DIAGNOSTIC_SCHEMA_VERSION
    && lastExit.errorCategory === STORESERVICES_ERROR_CATEGORY
    && lastExit.errorName === mapping.errorName
    && lastExit.urlBagKey === mapping.urlBagKey
    && lastExit.storeServicesCoreSha256 === STORESERVICES_AUDITED_SHA256
    && authCode === mapping.code
    && typeof lastExit.authCodeHex === 'string'
    && lastExit.authCodeHex === mapping.hex
    && lastExit.authCodeHex === `0x${authCode.toString(16).toUpperCase().padStart(8, '0')}`;
}

function renderCapabilities() {
  updateRepairButton();
  for (const option of elements.quality.options) option.disabled = !qualityAvailable(option.value);
  if (elements.quality.selectedOptions[0] && elements.quality.selectedOptions[0].disabled) {
    const next = [...elements.quality.options].find((option) => !option.disabled);
    if (next) elements.quality.value = next.value;
  }
  const wrapper = state.capabilities && (state.capabilities.wrapper || state.capabilities.wrapperStatus) || {};
  const wrapperStatus = typeof wrapper === 'string' ? wrapper : wrapper.status || 'unknown';
  const lastExit = typeof wrapper === 'object' && wrapper ? wrapper.lastExit : null;
  const crashSignature = lastExit && lastExit.signal === 'SIGSEGV' && lastExit.exitCode === 139 ? 'SIGSEGV/139' : '';
  const exitReason = typeof lastExit?.reason === 'string' ? lastExit.reason : '';
  const authCode = lastExit?.authCode;
  const authCodeHex = lastExit?.authCodeTrusted === true
    && Number.isSafeInteger(authCode)
    && authCode >= 0
    && authCode <= 0xFFFFFFFF
    && typeof lastExit?.authCodeHex === 'string'
    && /^0x[0-9A-F]{8}$/.test(lastExit.authCodeHex)
    && lastExit.authCodeHex === `0x${authCode.toString(16).toUpperCase().padStart(8, '0')}`
    ? lastExit.authCodeHex
    : '';
  const definitiveCredentialOutcome = ['store_services_auth_failed', 'authentication_failed', 'playback_lease_failed'].includes(exitReason);
  const hasUnsubmittedTwoFA = elements.twoFACode.value.trim().length > 0;
  const reconnecting = state.capabilitiesState === 'reconnecting';
  const preserveTwoFAInput = hasUnsubmittedTwoFA && !definitiveCredentialOutcome
    && (reconnecting || ['starting', 'error'].includes(wrapperStatus));
  const needsTwoFA = !reconnecting && (wrapperStatus === 'needs_2fa' || wrapper.needs2FA === true) || preserveTwoFAInput;
  const statusText = {
    online: '在线', ready: '在线', starting: '启动中', stopped: '认证已停止', needs_2fa: '需要两步验证', offline: '离线', unavailable: '未配置', error: '异常',
    crashed_post_auth: crashSignature ? `账号验证已完成，但 Wrapper 在初始化阶段崩溃（${crashSignature}）` : '账号验证已完成，但 Wrapper 在初始化阶段崩溃',
    reconnecting: '重连中',
  };
  const visibleStatus = reconnecting ? 'reconnecting' : wrapperStatus;
  let visibleText = statusText[visibleStatus] || visibleStatus;
  if (!reconnecting && ['online', 'ready'].includes(wrapperStatus)) {
    visibleText = wrapper.loginCompleted === true ? '已登录（本次启动已确认）' : '在线（登录未确认）';
  } else if (!reconnecting && wrapperStatus === 'starting' && wrapper.loginCompleted === true) {
    visibleText = '已登录，正在初始化';
  }
  if (!reconnecting && wrapperStatus === 'error') {
    if (exitReason === 'store_services_auth_failed') {
      if (storeServicesURLBagKeyMissing(lastExit)) {
        // Full trust contract satisfied: code 8 decodes to URLBagKeyNotFound on
        // the audited binary. The endpoint for authentication was never fetched,
        // so no password/2FA re-entry is warranted. The copy names no specific
        // sub-cause (DNS, fake-IP, proxy) because no same-stack stage was observed.
        visibleText = '无法获取 Apple 认证服务地址。请检查网络/代理或稍后重试；无需重新输入密码或验证码。';
      } else if (authCodeHex) {
        visibleText = `Apple 认证未完成（诊断代码 ${authCodeHex}，原因尚未映射）`;
      } else {
        visibleText = 'Apple 认证未完成；当前版本无法可靠读取原因';
      }
    } else if (exitReason === 'authentication_failed') {
      visibleText = 'Apple 认证未完成（原因尚未映射）';
    } else if (exitReason === 'playback_lease_failed') {
      visibleText = 'Wrapper 播放租约初始化未完成';
    } else if (exitReason === 'initialization_timeout') {
      const attemptCount = Number.isInteger(lastExit?.attempt) && lastExit.attempt > 0 ? lastExit.attempt : 0;
      const attemptText = attemptCount > 0 ? `（已尝试 ${attemptCount} 次）` : '';
      // A located stall and a stall before any marker are different findings, so
      // they must not read identically. The stage is matched against a fixed
      // list rather than echoed: it arrives over the bridge, and an unlisted
      // value would put untrusted text into the status line.
      // Naming this app's watchdog as the killer, and ruling out a crash, are
      // claims about cause. They are only made when the payload corroborates
      // them; otherwise the timeout is reported without attributing it.
      if (watchdogCleanupConfirmed(lastExit)) {
        // The stage is only evidence about where this attempt stalled if the
        // record it travels in is itself trustworthy. Showing it beside an
        // unverifiable termination would present the stall location as
        // established when the record cannot be tied to a run at all.
        const stage = knownStartupStage(lastExit?.lastStartupStage);
        const stageText = stage
          ? `最后到达的启动阶段为 ${stage}，请从该阶段继续排查。`
          : '本次没有观测到任何启动阶段标记，说明进程在首个标记前就已停滞。';
        visibleText = `Wrapper 初始化超时${attemptText}，本机看护程序已清理该进程；这不是崩溃。${stageText}`;
      } else {
        visibleText = `Wrapper 初始化超时${attemptText}，但终止原因无法确认，启动阶段诊断不可信。`;
      }
    }
  }
  // While the manager is retrying, the snapshot legitimately carries the current
  // attempt's status together with the previous attempt's terminal lastExit. A
  // bare "starting" would hide that an earlier attempt already timed out.
  if (!reconnecting && wrapperStatus === 'starting') {
    const currentAttempt = Number.isInteger(wrapper.currentAttempt) && wrapper.currentAttempt > 0
      ? wrapper.currentAttempt
      : 0;
    const totalAttempts = Number.isInteger(wrapper.totalAttempts) && wrapper.totalAttempts > 0
      ? wrapper.totalAttempts
      : 0;
    if (currentAttempt > 1) {
      const ofTotal = totalAttempts >= currentAttempt ? `/${totalAttempts}` : '';
      const previousTimedOut = exitReason === 'initialization_timeout' ? '上一次初始化超时，' : '';
      visibleText = `${previousTimedOut}正在进行第 ${currentAttempt}${ofTotal} 次初始化尝试`;
    }
  }
  elements.wrapper.textContent = visibleText;
  elements.wrapper.className = `status-value ${['online', 'ready'].includes(visibleStatus) ? 'online' : 'offline'}`;
  document.body.dataset.needsTwoFa = String(needsTwoFA);
  elements.twoFA.hidden = !needsTwoFA || document.body.dataset.view !== 'settings';
  elements.qualityHelp.textContent = capabilityMessage(elements.quality.value);
  updateReadiness();
}

function button(label, action, disabled = false, variant = '') {
  const result = document.createElement('button');
  result.type = 'button'; result.className = `secondary-button ${variant}`.trim(); result.textContent = label; result.disabled = disabled;
  result.addEventListener('click', action);
  return result;
}

function renderJobs() {
  const jobs = [...state.jobs.values()];
  elements.jobsEmpty.hidden = jobs.length > 0;
  for (const [id, view] of state.jobCards) {
    if (!state.jobs.has(id)) { view.card.remove(); state.jobCards.delete(id); }
  }
  let position = 0;
  for (const job of jobs) {
    const id = jobId(job); const status = jobStatus(job);
    const deleting = state.pendingDeletes.has(id);
    const deleteError = state.deleteErrors.get(id) || '';
    const signature = JSON.stringify([job, queueNeedsRecovery(), deleting, deleteError]);
    const previous = state.jobCards.get(id);
    if (previous && previous.signature === signature) {
      if (elements.jobs.children[position] !== previous.card) elements.jobs.insertBefore(previous.card, elements.jobs.children[position] || null);
      position += 1;
      continue;
    }
    const card = document.createElement('article'); card.className = `job-card ${status}`;
    const heading = document.createElement('div'); heading.className = 'job-heading';
    const title = document.createElement('a'); title.href = jobURL(job); title.target = '_blank'; title.rel = 'noreferrer'; title.textContent = jobURL(job); title.title = jobURL(job); title.ariaLabel = `${jobURL(job)}（在新窗口打开）`;
    const badge = document.createElement('span'); badge.className = `job-status ${status}`; badge.textContent = queueNeedsRecovery() && isActive(job) ? '结果待恢复' : statusText(status);
    heading.append(title, badge); card.append(heading);
    const policy = job.existingPolicy || 'skip';
    const shortID = String(id || '').slice(0, 8);
    const details = document.createElement('p'); details.className = 'hint'; details.textContent = `${qualityText(job.quality)} · ${existingPolicyText(policy)} · 任务 ${shortID}`; card.append(details);
    const playlistFailure = playlistFailureText(job);
    if (playlistFailure) {
      const failure = document.createElement('p'); failure.className = 'hint error-message'; failure.textContent = playlistFailure; card.append(failure);
    }
    if (deleteError) {
      const failure = document.createElement('p'); failure.className = 'hint error-message'; failure.role = 'alert'; failure.textContent = deleteError; card.append(failure);
    }
    const progress = progressText(job);
    if (progress) {
      const progressLine = document.createElement('p'); progressLine.className = 'job-progress'; progressLine.textContent = progress; card.append(progressLine);
      const progressBar = document.createElement('progress');
      const total = count(job.total);
      progressBar.className = 'job-progress-bar'; progressBar.max = total;
      progressBar.value = Math.min(total, count(job.succeeded) + count(job.failed) + count(job.skipped) + count(job.unavailable));
      progressBar.ariaLabel = progress; card.append(progressBar);
    }
    const tracks = jobTracks(job);
    if (tracks.length) {
      const list = document.createElement('ul'); list.className = 'track-list';
      tracks.forEach((track) => {
        const line = document.createElement('li');
        const failure = trackFailureText(track);
        const attempts = Number.isSafeInteger(track.attempt) && track.attempt > 0 ? ` · 尝试 ${track.attempt} 次` : '';
        line.textContent = `${track.name || track.sourceId || '曲目'} — ${statusText(track.status || 'queued')}${failure ? ` · ${failure}` : ''}${attempts}`;
        list.append(line);
      });
      card.append(list);
    }
    const controls = document.createElement('div'); controls.className = 'job-controls';
    if (isActive(job)) controls.append(button('取消', () => cancelJob(id), false, 'cancel-button'));
    if (canRetryJob(job)) controls.append(button('重试失败项', () => retryJob(id), deleting, 'retry-button'));
    if (!isActive(job) && count(job.succeeded) > 0 && !deleting) {
      const archive = document.createElement('a');
      archive.className = 'secondary-button job-download';
      archive.href = `/api/v1/jobs/${encodeURIComponent(id)}/archive`;
      archive.textContent = '下载此任务';
      authenticateDownloadLink(archive, archive.href, `AppleMusicDownloader-${shortID}.zip`);
      controls.append(archive);
    }
    if (!isActive(job)) {
      controls.append(button('仅删除任务（保留文件）', () => deleteJob(id, true), deleting));
      controls.append(button(deleting ? '正在删除…' : '删除任务和文件', () => deleteJob(id), deleting, 'danger-button'));
    }
    controls.append(button('刷新详情', () => loadJob(id), deleting));
    const controlsSignature = JSON.stringify([isActive(job), canRetryJob(job), count(job.succeeded) > 0, deleting]);
    let view;
    if (previous) {
      // Keep the card and unchanged controls attached, even when details or
      // another task's progress change between pointerdown and pointerup.
      if (previous.controlsSignature !== controlsSignature) previous.controls.replaceChildren(...controls.children);
      for (const child of [...previous.card.children]) {
        if (child !== previous.controls) child.remove();
      }
      for (const child of [...card.children]) previous.card.insertBefore(child, previous.controls);
      previous.card.className = card.className;
      view = { ...previous, signature, controlsSignature };
    } else {
      card.append(controls);
      view = { card, controls, signature, controlsSignature };
    }
    state.jobCards.set(id, view);
    if (elements.jobs.children[position] !== view.card) elements.jobs.insertBefore(view.card, elements.jobs.children[position] || null);
    position += 1;
  }
  updateServiceStatus();
}

function fileId(file) { return file.id; }
function renderFiles(files) {
  elements.files.replaceChildren();
  const valid = (files || []).filter((file) => fileId(file));
  elements.filesEmpty.hidden = valid.length > 0;
  for (const file of valid) {
    const row = document.createElement('div'); row.className = 'file-row';
    const name = document.createElement('span'); name.className = 'file-name'; name.textContent = file.name || '文件';
    const meta = document.createElement('span'); meta.className = 'file-size';
    const details = [];
    if (file.unmanaged) details.push('已有文件');
    if (file.size) details.push(`${Math.round(file.size / 1024)} KB`);
    meta.textContent = details.join(' · ');
    const link = document.createElement('a'); link.className = 'file-dl-btn'; link.href = `/api/v1/files/${encodeURIComponent(fileId(file))}`; link.textContent = '下载';
    authenticateDownloadLink(link, link.href, file.name || 'download');
    row.append(name, meta, link); elements.files.append(row);
  }
}

function mergeJob(id, update) {
  if (state.deletedJobs.has(id) || state.pendingDeletes.has(id)) return;
  const current = state.jobs.get(id) || { id };
  const next = { ...current, ...(update || {}), id };
  state.jobs.set(id, next);
  return next;
}

function setJobFiles(id, files) {
  if (state.deletedJobs.has(id)) return;
  const unique = new Map();
  for (const file of files || []) {
    const idValue = fileId(file);
    if (idValue) unique.set(idValue, { ...file, jobId: id });
  }
  state.filesByJob.set(id, [...unique.values()]);
  rememberKnownFiles([...unique.values()]);
}

function addJobFile(id, file) {
  if (state.deletedJobs.has(id) || !file) return;
  const idValue = fileId(file);
  if (!idValue) return;
  // File events are scoped by their EventSource URL. Persist that ownership
  // locally even when a compact SSE payload omits jobId, so later deletion
  // can remove the exact task's live-file entries from the global index.
  const ownedFile = { ...file, jobId: id };
  const files = new Map((state.filesByJob.get(id) || []).map((entry) => [fileId(entry), entry]));
  files.set(idValue, { ...(files.get(idValue) || {}), ...ownedFile });
  state.filesByJob.set(id, [...files.values()]);
  rememberKnownFiles([ownedFile]);
}

function rememberKnownFiles(files) {
  for (const file of files || []) {
    const id = fileId(file);
    if (!id || state.deletedFiles.has(id) || state.deletedJobs.has(file.jobId)) continue;
    state.knownFiles.set(id, { ...(state.knownFiles.get(id) || {}), ...file });
  }
}

function renderKnownFiles() {
  renderFiles([...state.knownFiles.values()]);
}

function rememberEventID(id, event) {
  if (state.deletedJobs.has(id)) return;
  if (event && event.lastEventId) state.lastEventIds.set(id, event.lastEventId);
}

function closeInactiveSubscriptions() {
  for (const [id, source] of state.eventSources) {
    const job = state.jobs.get(id);
    if (job && needsEventStream(job)) continue;
    source.close();
    state.eventSources.delete(id);
  }
}

function scheduleQueueRefresh() {
  // The backend is intentionally serial. A single bounded poll discovers the
  // next queued job becoming runnable without opening one long-lived SSE
  // connection per queued URL (a batch may contain up to 100 jobs).
  if (state.queuePollTimer !== null || !hasQueuedJobs()) return;
  state.queuePollTimer = window.setTimeout(async () => {
    state.queuePollTimer = null;
    if (!hasQueuedJobs()) return;
    await loadJobs();
    scheduleQueueRefresh();
  }, QUEUE_REFRESH_INTERVAL_MS);
}

function syncJobSubscriptions() {
  closeInactiveSubscriptions();
  for (const job of state.jobs.values()) {
    if (needsEventStream(job)) subscribe(jobId(job));
  }
  scheduleQueueRefresh();
}

function eventData(event) {
  if (!event || typeof event.data !== 'string') return event && event.data;
  try { return JSON.parse(event.data); }
  catch { return event.data; }
}

function itemKey(item) {
  return item && (item.id || item.sourceId);
}

function updateItem(id, item) {
  if (state.deletedJobs.has(id) || state.pendingDeletes.has(id)) return;
  if (!item || typeof item !== 'object') return;
  const current = state.jobs.get(id) || { id };
  const key = itemKey(item);
  if (!key) return;
  const tracks = jobTracks(current).slice();
  const index = tracks.findIndex((track) => itemKey(track) === key);
  if (index >= 0) tracks[index] = { ...tracks[index], ...item };
  else tracks.push(item);
  state.jobs.set(id, { ...current, items: tracks });
}

function handleStateEvent(id, payload) {
  const update = jobData.job(payload?.job || payload);
  mergeJob(id, update);
  syncJobSubscriptions();
  renderJobs();
}

function handleProgressEvent(id, payload) {
  const data = payload && typeof payload === 'object' ? (payload.progress || payload) : {};
  const update = {};
  for (const field of ['total', 'succeeded', 'failed', 'skipped', 'unavailable']) {
    if (Object.hasOwn(data, field)) update[field] = data[field];
  }
  mergeJob(id, update);
  renderJobs();
}

function handleItemEvent(id, payload) {
  updateItem(id, jobData.item(payload?.item || payload?.track || payload));
  renderJobs();
}

function handleFileEvent(id, payload) {
  const file = jobData.file(payload?.file || payload);
  addJobFile(id, file);
  renderKnownFiles();
}

function handleLogEvent(payload) {
  if (payload && typeof payload === 'object') {
    if (payload.message) addLog(payload.message, payload.level === 'error' ? 'error' : 'info');
    return;
  }
  if (payload) addLog(String(payload));
}

async function loadJob(id) {
  if (state.deletedJobs.has(id) || state.pendingDeletes.has(id)) return;
  try {
    const job = await api(`/api/v1/jobs/${encodeURIComponent(id)}`);
    if (state.deletedJobs.has(id)) return;
    mergeJob(id, jobData.job(job.job || job));
    syncJobSubscriptions();
    renderJobs();
    const files = await api(`/api/v1/jobs/${encodeURIComponent(id)}/files`).catch(() => []);
    setJobFiles(id, jobData.files(files));
    renderKnownFiles();
  } catch (error) { addLog(`读取任务失败：${error.message}`, 'error'); }
}

function handleMessageEvent(id, event) {
  if (state.deletedJobs.has(id)) return;
  rememberEventID(id, event);
  const payload = eventData(event);
  if (payload && typeof payload === 'object') {
    const job = jobData.job(payload.job || payload);
    if (job && jobId(job)) {
      mergeJob(id, job);
      syncJobSubscriptions();
      renderJobs();
    }
    handleLogEvent(payload);
  } else if (payload) addLog(String(payload));
}

function dispatchJobEvent(id, type, data, lastEventId) {
  const event = { data, lastEventId };
  switch (type) {
    case 'state':
      rememberEventID(id, event);
      handleStateEvent(id, eventData(event));
      break;
    case 'progress':
      rememberEventID(id, event);
      handleProgressEvent(id, eventData(event));
      break;
    case 'item':
      rememberEventID(id, event);
      handleItemEvent(id, eventData(event));
      break;
    case 'file':
      rememberEventID(id, event);
      handleFileEvent(id, eventData(event));
      break;
    case 'log':
      rememberEventID(id, event);
      handleLogEvent(eventData(event));
      break;
    default:
      handleMessageEvent(id, event);
  }
}

function parseEventBlock(block) {
  let type = 'message';
  let lastEventId = '';
  const data = [];
  for (const line of block.split(/\r\n|\r|\n/)) {
    if (!line || line.startsWith(':')) continue;
    const separator = line.indexOf(':');
    const field = separator < 0 ? line : line.slice(0, separator);
    let value = separator < 0 ? '' : line.slice(separator + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') type = value || 'message';
    else if (field === 'data') data.push(value);
    else if (field === 'id' && !value.includes('\u0000')) lastEventId = value;
  }
  if (!data.length) return null;
  return { type, data: data.join('\n'), lastEventId };
}

async function consumeEventStream(body, onEvent) {
  if (!body || typeof body.getReader !== 'function') throw new Error('事件流响应不可读取。');
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      while (true) {
        const delimiter = buffer.match(/\r\n\r\n|\n\n|\r\r/);
        if (!delimiter) break;
        const block = buffer.slice(0, delimiter.index);
        buffer = buffer.slice(delimiter.index + delimiter[0].length);
        const event = parseEventBlock(block);
        if (event) onEvent(event);
      }
    }
    buffer += decoder.decode();
  } finally {
    if (typeof reader.releaseLock === 'function') reader.releaseLock();
  }
}

function reconnectSubscription(id, source) {
  if (state.eventSources.get(id) !== source) return;
  source.close();
  state.eventSources.delete(id);
  if (!needsEventStream(state.jobs.get(id))) return;
  window.setTimeout(() => loadJob(id).then(() => {
    if (needsEventStream(state.jobs.get(id))) subscribe(id);
  }), 1_000);
}

function createFetchEventSource(id) {
  const controller = new AbortController();
  const source = {
    closed: false,
    close() {
      if (source.closed) return;
      source.closed = true;
      controller.abort();
    },
  };
  const lastId = state.lastEventIds.get(id);
  const headers = { Accept: 'text/event-stream' };
  if (lastId) headers['Last-Event-ID'] = lastId;
  Promise.resolve().then(async () => {
    const response = await authenticatedFetch(`/api/v1/jobs/${encodeURIComponent(id)}/events`, {
      cache: 'no-store',
      headers,
      signal: controller.signal,
    });
    if (!response.ok) throw new Error(`事件流连接失败（HTTP ${response.status}）。`);
    const contentType = response.headers.get('content-type') || '';
    if (!contentType.includes('text/event-stream')) throw new Error('事件流响应类型无效。');
    await consumeEventStream(response.body, (event) => dispatchJobEvent(id, event.type, event.data, event.lastEventId));
    if (!source.closed) reconnectSubscription(id, source);
  }).catch(() => {
    if (!source.closed) reconnectSubscription(id, source);
  });
  return source;
}

function subscribe(id) {
  if (!id || state.eventSources.has(id)) return;
  if (apiAccessKey) {
    const source = createFetchEventSource(id);
    state.eventSources.set(id, source);
    return;
  }
  const lastId = state.lastEventIds.get(id);
  const source = new EventSource(`/api/v1/jobs/${encodeURIComponent(id)}/events${lastId ? `?lastEventId=${encodeURIComponent(lastId)}` : ''}`);
  state.eventSources.set(id, source);
  const dispatch = (type, event) => dispatchJobEvent(id, type, event.data, event.lastEventId);
  for (const type of ['state', 'progress', 'item', 'file', 'log']) {
    source.addEventListener(type, (event) => dispatch(type, event));
  }
  source.onmessage = (event) => dispatch('message', event);
  source.onerror = () => reconnectSubscription(id, source);
}

async function loadJobs() {
  try {
    const payload = await api('/api/v1/jobs?limit=100');
    const pending = [...state.jobs].filter(([id]) => state.pendingDeletes.has(id));
    state.jobs.clear();
    jobData.jobs(payload).forEach((job) => { const id = jobId(job); if (id && !state.deletedJobs.has(id)) state.jobs.set(id, job); });
    // A list snapshot (even an empty one) cannot settle an in-flight DELETE.
    for (const [id, job] of pending) state.jobs.set(id, job);
    syncJobSubscriptions();
    renderJobs();
    return true;
  } catch (error) { addLog(`无法读取任务历史：${error.message}`, 'error'); }
  return false;
}

async function loadKnownFiles() {
  try {
    const payload = await api('/api/v1/files');
    state.knownFiles.clear();
    rememberKnownFiles(jobData.files(payload));
    renderKnownFiles();
    return true;
  } catch (error) { addLog(`无法读取已有文件：${error.message}`, 'error'); }
  return false;
}

// fetchCapabilitiesWithDeadline bounds one capabilities read. The abort fires
// through the same window.setTimeout the scheduler uses, and the timer is always
// cleared so a completed request cannot leave a pending abort behind.
async function fetchCapabilitiesWithDeadline() {
  const controller = new AbortController();
  let timedOut = false;
  const deadline = window.setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, CAPABILITIES_REQUEST_TIMEOUT_MS);
  try {
    return await api('/api/v1/capabilities', { cache: 'no-store', signal: controller.signal });
  } catch (error) {
    if (timedOut || error?.name === 'AbortError') {
      // Treat an expired deadline like any other bridge unavailability: it is a
      // local timing fact, never remote text.
      const failure = new Error('本地 WSL 服务桥暂时不可用。');
      failure.transportFailure = true;
      throw failure;
    }
    throw error;
  } finally {
    if (typeof window.clearTimeout === 'function') window.clearTimeout(deadline);
  }
}

async function loadCapabilities() {
  if (capabilityRequest) return capabilityRequest;
  const generation = ++capabilityGeneration;
  capabilityRequest = (async () => {
    try {
      const next = await fetchCapabilitiesWithDeadline();
      // A future caller may invalidate this generation while the request is
      // in flight.  Do not let an older response resurrect a stale bridge.
      if (generation !== capabilityGeneration) return false;
      const wasReconnecting = state.capabilitiesState === 'reconnecting';
      state.capabilities = next;
      state.capabilitiesState = 'ready';
      renderCapabilities();
      renderJobs();
      if (wasReconnecting && !capabilityRecoveryLogged) {
        capabilityRecoveryLogged = true;
        addLog('本地 WSL 服务桥已恢复。', 'success');
      }
      return true;
    } catch (error) {
      if (generation !== capabilityGeneration) return false;
      // Any failed read means the published snapshot is no longer current. The
      // old one describes the past, so it must stop being rendered as live
      // state regardless of which failure class produced it. Only the wording
      // differs: 502/503/504 and transport failures name the local bridge,
      // everything else (403, 500, malformed JSON) is reported without echoing
      // remote or parser text.
      state.capabilitiesState = 'reconnecting';
      capabilityRecoveryLogged = false;
      renderCapabilities();
      if (error.transportFailure || LOCAL_PROXY_ERROR_STATUSES.has(error.status)) {
        const status = LOCAL_PROXY_ERROR_STATUSES.has(error.status) ? `（HTTP ${error.status}）` : '';
        const message = `本地 WSL 服务桥暂时不可用${status}，正在自动重试。`;
        addLog(`无法读取服务能力：${message}`, 'error');
      } else {
        const status = Number.isInteger(error.status) ? `（HTTP ${error.status}）` : '';
        addLog(`无法读取服务能力${status}，当前状态已标记为未知，正在自动重试。`, 'error');
      }
      return false;
    } finally {
      if (capabilityRequest && generation === capabilityGeneration) capabilityRequest = null;
    }
  })();
  return capabilityRequest;
}

function invalidateCapabilities() {
  capabilityGeneration += 1;
  // The underlying fetch is intentionally allowed to finish; generation
  // checks make its result inert without requiring an abort signal in every
  // embedded renderer.  Clearing the handle permits the post-repair request
  // to start immediately, while the old finally block cannot clear a newer
  // request because its generation no longer matches.
  capabilityRequest = null;
  state.capabilitiesState = 'reconnecting';
  renderCapabilities();
}

function scheduleCapabilitiesRefresh() {
  if (capabilityRefreshTimer !== null) return;
  capabilityRefreshTimer = window.setTimeout(async () => {
    capabilityRefreshTimer = null;
    // Scheduler liveness must not depend on the outcome of one read. Each read
    // is deadline-bounded and swallows its own errors, but re-arming from a
    // finally block keeps polling alive even if a future change throws here.
    try {
      await loadCapabilities();
    } finally {
      scheduleCapabilitiesRefresh();
    }
  }, 1000);
}

async function refreshCapabilitiesNow({ force = false } = {}) {
  if (force) invalidateCapabilities();
  const result = await loadCapabilities();
  scheduleCapabilitiesRefresh();
  return result;
}

async function cancelJob(id) {
  try { await api(`/api/v1/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }); addLog('已请求取消任务'); await loadJob(id); }
  catch (error) { addLog(`取消失败：${error.message}`, 'error'); }
}

async function retryJob(id) {
  if (state.pendingDeletes.has(id) || state.deletedJobs.has(id)) return;
  try { await api(`/api/v1/jobs/${encodeURIComponent(id)}/retry`, { method: 'POST' }); addLog('已请求重试失败项'); await loadJob(id); }
  catch (error) { addLog(`重试失败：${error.message}`, 'error'); }
}

async function deleteJob(id, retainFiles = false) {
  if (state.pendingDeletes.has(id) || state.deletedJobs.has(id)) return;
  const confirmation = retainFiles
    ? '仅删除任务记录，保留已下载的文件。文件仍可在下载文件列表中查看。是否继续？'
    : '删除任务会永久删除该任务的下载文件，且无法恢复。是否继续？';
  if (!window.confirm(confirmation)) return;
  state.pendingDeletes.add(id);
  state.deleteErrors.delete(id);
  renderJobs();
  try {
    // Never fall back to the destructive endpoint, even on an older backend.
    await api(`/api/v1/jobs/${encodeURIComponent(id)}${retainFiles ? '/record' : ''}`, { method: 'DELETE' });
    state.deletedJobs.add(id);
    const source = state.eventSources.get(id);
    if (source) source.close();
    state.eventSources.delete(id);
    state.lastEventIds.delete(id);
    for (const file of state.filesByJob.get(id) || []) {
      if (retainFiles) state.knownFiles.set(fileId(file), { ...file, jobId: '', unmanaged: true });
      else state.deletedFiles.add(fileId(file));
    }
    state.filesByJob.delete(id);
    state.jobs.delete(id);
    for (const [fileID, file] of state.knownFiles) {
      if (file.jobId === id) {
        if (retainFiles) state.knownFiles.set(fileID, { ...file, jobId: '', unmanaged: true });
        else state.deletedFiles.add(fileID);
      }
      if (state.deletedFiles.has(fileID)) state.knownFiles.delete(fileID);
    }
    renderKnownFiles();
    addLog(retainFiles ? '已删除任务记录，下载文件已保留。' : '已删除任务及其下载文件。', 'success');
    if (retainFiles) await loadKnownFiles();
  } catch (error) {
    const message = retainFiles && [404, 405].includes(error.status)
      ? '当前后端尚不支持此操作，或任务已不存在。未执行删除文件操作；请更新后端或刷新列表后重试。'
      : `删除任务失败：${error.message}`;
    state.deleteErrors.set(id, message);
    addLog(message, 'error');
  } finally {
    state.pendingDeletes.delete(id);
    renderJobs();
  }
}

elements.quality.addEventListener('change', renderCapabilities);
elements.defaultQuality.addEventListener('change', saveDefaultDownloadPreferences);
elements.defaultExistingPolicy.addEventListener('change', saveDefaultDownloadPreferences);
elements.refresh.addEventListener('click', async () => { await refreshCapabilitiesNow(); await loadJobs(); await loadKnownFiles(); });
if (elements.repair) elements.repair.addEventListener('click', requestRuntimeRepair);
for (const [id, method, label] of desktopSettingsActions) {
  document.getElementById(id)?.addEventListener('click', () => requestDesktopSettingsAction(method, label));
}
updateDesktopSettingsControls();
if (elements.downloadConcurrencySave) elements.downloadConcurrencySave.addEventListener('click', saveDownloadConcurrencySetting);
if (elements.downloadConcurrencyReset) elements.downloadConcurrencyReset.addEventListener('click', resetDownloadConcurrencySetting);
document.getElementById('download-preferences-form')?.addEventListener('submit', changeDownloadPreferences);
document.getElementById('download-preferences-reset')?.addEventListener('click', (event) => changeDownloadPreferences(event, true));
initializeFilenameBuilder();
updateRepairButton();
elements.form.addEventListener('submit', async (event) => {
  event.preventDefault();
  if (offlineShell) { addLog('下载服务尚未连接，当前只能浏览 Apple Music。', 'warning'); return; }
  if (queueNeedsRecovery()) { addLog('任务状态保存失败，请检查磁盘并重启服务恢复。', 'error'); return; }
  const urls = elements.urls.value.split(/\r?\n/).map((url) => url.trim()).filter(Boolean);
  if (!urls.length || urls.length > 100) { addLog('请输入 1–100 个 Apple Music URL。', 'warning'); return; }
  if (!qualityAvailable(elements.quality.value)) { addLog(capabilityMessage(elements.quality.value) || '所选音质当前不可用。', 'warning'); return; }
  elements.submit.disabled = true;
  try {
    const payload = await api('/api/v1/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ urls, quality: elements.quality.value, existingPolicy: elements.existingPolicy.value, existing_policy: elements.existingPolicy.value }) });
    elements.urls.value = '';
    addLog(`已将 ${urls.length} 个 URL 加入下载队列。`, 'success');
    await loadJobs();
  } catch (error) { addLog(`创建任务失败：${error.message}`, 'error'); }
  finally { elements.submit.disabled = false; }
});
elements.twoFASubmit.addEventListener('click', async () => {
  if (offlineShell) return;
  const code = elements.twoFACode.value.trim();
  if (!/^\d{6}$/.test(code)) { addLog('请输入六位验证码。', 'warning'); return; }
  elements.twoFASubmit.disabled = true;
  try { await api('/api/v1/wrapper/2fa', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code }) }); elements.twoFACode.value = ''; addLog('验证码已提交。', 'success'); await refreshCapabilitiesNow(); }
  catch (error) { addLog(`验证码提交失败：${error.message}`, 'error'); }
  finally { elements.twoFASubmit.disabled = false; }
});

loadDefaultDownloadPreferences();
if (offlineShell) {
  elements.wrapper.textContent = '未连接';
  elements.downloader.textContent = '未连接';
  elements.submit.disabled = true;
  elements.refresh.disabled = true;
  elements.twoFASubmit.disabled = true;
  elements.downloadConcurrencySave.disabled = true;
  elements.downloadConcurrencyReset.disabled = true;
  elements.downloadConcurrencyStatus.textContent = '连接下载服务后可调整。';
  reportDesktopSettings('下载服务未连接。浏览 Apple Music 不受影响；首次使用请打开“下载位置与设置”向导。');
  addLog('下载服务未连接；仍可浏览公开 Apple Music 页面。');
  updateReadiness();
  window.desktopShell?.onServiceState?.((serviceState) => {
    if (serviceState?.code === 'network_preflight_failed') {
      const stage = knownNetworkPreflightStage(serviceState.stage);
      reportDesktopSettings(`网络预检未完成（network_preflight_failed，阶段：${stage}）。请检查网络或代理后重试。`);
    } else reportDesktopSettings('下载服务仍未就绪。请打开设置向导，或稍后重新连接。');
  });
} else {
  Promise.all([loadCapabilities(), loadJobs(), loadKnownFiles(), loadDownloadConcurrencySetting(), loadDownloadPreferences()]).then(([capabilitiesOK, jobsOK, filesOK]) => {
    if (capabilitiesOK && jobsOK && filesOK) addLog('已连接本机任务服务。');
    scheduleCapabilitiesRefresh();
  }).catch(() => {});
}

return Object.freeze({
  api, offlineShell, downloadSubmissionReady, queueNeedsRecovery,
  qualityAvailable, capabilityMessage, isActive, loadJobs,
  refreshCapabilities: loadCapabilities,
  jobs: Object.freeze({
    values: () => state.jobs.values(),
    acceptCreated: (value) => {
      const job = jobData.job(value);
      if (job.id) mergeJob(job.id, job);
    },
  }),
});
})({ jobData: window.amdlJobData });

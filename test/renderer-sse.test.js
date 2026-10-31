'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const JOBS_LIST_URL = '/api/v1/jobs?limit=100';

test('green readiness requires explicit current login evidence, not just an online port or token', async () => {
 for (const evidence of [undefined, false, 'true', true]) {
  const page = await loadRenderer({capabilities:{wrapper:{status:'online',loginCompleted:evidence},queue:{state:'ready'},jobExecutorConfigured:true,qualities:{'aac-lc':{available:true}}}});
  assert.equal(page.elements['download-readiness'].classList.contains('ready'), evidence === true);
  assert.match(page.elements['wrapper-status'].textContent, evidence === true ? /已登录/ : /登录未确认/);
 }
});

test('one click exports bounded recent UI logs and handles native cancellation', async () => {
 let received;
 const page = await loadRenderer({desktopShell:{exportDiagnostics:async(lines)=>{received=lines;return{ok:true,cancelled:true};}}});
 await dispatch(page.elements['export-logs'],'click');
 assert.ok(Array.isArray(received)); assert.ok(received.length <= 100);
 assert.match(page.elements['desktop-settings-status'].textContent,/已取消/);
 assert.equal(page.elements['export-logs'].disabled,false);
});

test('a stale bridge immediately removes green login readiness without blocking token-only submission', async () => {
 let unavailable=false;
 const page=await loadRenderer({fetchImpl:async(url)=>{
  if(url==='/api/v1/capabilities') return unavailable?errorResponse(503):jsonResponse({wrapper:{status:'online',loginCompleted:true},queue:{state:'ready'},jobExecutorConfigured:true,qualities:{'aac-lc':{available:true}}});
  if(url===JOBS_LIST_URL)return jsonResponse({jobs:[]});
  if(url==='/api/v1/files')return jsonResponse({files:[]});
  throw Error('unexpected fixture request');
 }});
 assert.equal(page.elements['download-readiness'].classList.contains('ready'),true);
 unavailable=true;await dispatch(page.elements['refresh-btn'],'click');
 assert.equal(page.elements['download-readiness'].classList.contains('ready'),false);
 assert.equal(page.elements['download-readiness'].classList.contains('not-ready'),true);
});

test('pending log export prevents repeated exports and recovers after a fixed failure', async()=>{
 let calls=0,release;
 const gate=new Promise(resolve=>{release=resolve;});
 const page=await loadRenderer({desktopShell:{exportDiagnostics:async()=>{calls++;return gate;}}});
 const pending=dispatch(page.elements['export-logs'],'click');await settle();
 assert.equal(page.elements['settings-export-logs'].disabled,true);
 await dispatch(page.elements['settings-export-logs'],'click');assert.equal(calls,1);
 release({ok:false,error:{message:'日志导出未完成。'}});await pending;
 assert.equal(page.elements['export-logs'].disabled,false);
 assert.match(page.elements['desktop-settings-status'].textContent,/未完成/);
});

test('metadata failures before any tracks have a fixed safe explanation',async()=>{
 const page=await loadRenderer({initialJob:{id:'job-1',status:'failed',total:0,errorCode:'metadata_transient',errorMessage:'sentinel-secret'}});
 assert.match(page.elements.jobs.textContent,/曲目信息获取失败/);
 assert.doesNotMatch(page.elements.jobs.textContent,/sentinel-secret/);
});

test('queue finalization failure is shown instead of a running download',async()=>{
 const page=await loadRenderer({capabilities:{wrapper:{status:'online'},qualities:{alac:{available:true}},queue:{state:'error',errorCode:'job_finalization_failed'}}});
 assert.match(page.elements.jobs.textContent,/结果待恢复/);
 assert.match(page.elements['downloader-status'].textContent,/状态保存失败/);
});

test('queue recovery blocks submission and retry actions',async()=>{
 const page=await loadRenderer({initialJob:{id:'job-1',status:'failed',total:1,failed:1,items:[{sourceId:'a',status:'failed'}]},capabilities:{wrapper:{status:'online'},qualities:{alac:{available:true}},queue:{state:'error',errorCode:'job_finalization_failed'}}});
 page.elements['music-urls'].value='https://music.apple.com/us/song/one/123';
 await dispatch(page.elements['job-form'],'submit',{preventDefault(){}});
 assert.equal(page.requests.filter((r)=>r.options.method==='POST').length,0);
 assert.doesNotMatch(page.elements.jobs.textContent,/重试失败项/);
});

test('download summary separates processed and success and explains safe failures', async () => {
 const page=await loadRenderer({initialJob:{id:'job-1',status:'partial',total:3,succeeded:1,failed:1,skipped:0,unavailable:1,items:[
  {sourceId:'a',name:'One',status:'succeeded'},
  {sourceId:'b',name:'Two',status:'failed',errorCode:'manifest_transient',errorMessage:'sentinel-secret',attempt:3},
  {sourceId:'c',name:'Three',status:'unavailable'},
 ]}});
 const text=page.elements.jobs.textContent;
 assert.match(text,/已处理：3\/3/);
 assert.match(text,/成功下载：1/);
 assert.match(text,/失败：1/);
 assert.match(text,/不可用：1/);
 assert.match(text,/媒体清单/);
 assert.match(text,/尝试 3 次/);
 assert.doesNotMatch(text,/sentinel-secret/);
});

test('no retry button when every resolved track is unavailable',async()=>{
 const page=await loadRenderer({initialJob:{id:'job-1',status:'failed',total:1,succeeded:0,failed:0,unavailable:1,items:[{sourceId:'a',status:'unavailable'}]}});
 assert.doesNotMatch(page.elements.jobs.textContent,/重试失败项/);
});

test('missing completion receipt does not claim the downloaded file is incomplete', async () => {
 const page=await loadRenderer({initialJob:{id:'job-receipt',status:'partial',total:2,succeeded:1,failed:1,items:[
  {sourceId:'a',name:'Complete',status:'succeeded'},
  {sourceId:'b',name:'Unconfirmed',status:'failed',errorCode:'download_result_missing'},
 ]}});
 assert.match(page.elements.jobs.textContent,/完成状态未确认/);
 assert.doesNotMatch(page.elements.jobs.textContent,/下载结果不完整/);
 assert.match(page.elements.jobs.textContent,/重试失败项/);
});

class FakeElement {
  constructor(tagName = 'div') {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.className = '';
    this.dataset = {};
    const classes = new Set();
    this.classList = {
      toggle: (name, enabled) => { if (enabled) classes.add(name); else classes.delete(name); return enabled; },
      contains: (name) => classes.has(name),
    };
    this.hidden = false;
    this.disabled = false;
    this.value = '';
    this.href = '';
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this._textContent = '';
    this._listeners = new Map();
  }

  set textContent(value) {
    this._textContent = String(value);
    this.children = [];
  }

  get textContent() {
    return this._textContent + this.children.map((child) => child.textContent).join('');
  }

  get firstElementChild() {
    return this.children[0] || null;
  }

  append(...children) {
    children.forEach((child) => this.appendChild(child));
  }

  appendChild(child) {
    child.remove();
    child.parentNode = this;
    this.children.push(child);
    return child;
  }

  replaceChildren(...children) {
    for (const child of this.children) child.parentNode = null;
    this.children = [];
    this._textContent = '';
    this.append(...children);
  }

  insertBefore(child, reference) {
    if (child === reference) return child;
    if (reference === null) return this.appendChild(child);
    assert.ok(this.children.includes(reference), 'insertBefore reference must be a child');
    child.remove();
    this.children.splice(this.children.indexOf(reference), 0, child);
    child.parentNode = this;
    return child;
  }

  remove() {
    if (!this.parentNode) return;
    const index = this.parentNode.children.indexOf(this);
    if (index >= 0) this.parentNode.children.splice(index, 1);
    this.parentNode = null;
  }

  addEventListener(type, listener) {
    const listeners = this._listeners.get(type) || [];
    listeners.push(listener);
    this._listeners.set(type, listeners);
  }

  querySelector() {
    if (!this._queryChild) this._queryChild = new FakeElement('span');
    return this._queryChild;
  }

  setAttribute() {}

  click() {}
}

function makeDocument() {
  const ids = [
    'job-form', 'music-urls', 'quality', 'existing-policy', 'quality-help', 'download-btn',
    'wrapper-status', 'downloader-status', 'twofa-section', 'twofa-code', 'twofa-submit',
    'jobs', 'jobs-empty', 'files', 'files-empty', 'archive-link', 'log-container', 'refresh-btn', 'repair-service-button',
    'desktop-settings', 'desktop-settings-browser-note', 'desktop-settings-status',
    'download-readiness', 'default-quality', 'default-existing-policy', 'default-download-status',
    'settings-download-location', 'settings-startup', 'settings-credentials', 'settings-start-service', 'settings-diagnostics', 'settings-export-logs', 'export-logs', 'settings-repair-runtime', 'settings-stop-service',
  ];
  const elements = Object.fromEntries(ids.map((id) => [id, new FakeElement()]));
  const quality = elements.quality;
  quality.options = ['aac-lc', 'alac', 'aac', 'atmos'].map((value) => ({ value, disabled: false }));
  quality.value = 'alac';
  Object.defineProperty(quality, 'selectedOptions', {
    get() { return this.options.filter((option) => option.value === this.value); },
  });
  elements['existing-policy'].value = 'skip';
  elements['default-quality'].value = 'aac-lc';
  elements['default-existing-policy'].value = 'skip';

  const body = new FakeElement('body');
  body.dataset.view = 'settings';
  return {
    elements,
    body,
    getElementById(id) { return elements[id] || null; },
    createElement(tagName) { return new FakeElement(tagName); },
  };
}

function jsonResponse(data) {
  return {
    ok: true,
    status: 200,
    headers: { get: () => 'application/json' },
    json: async () => data,
  };
}

function errorResponse(status) {
  return {
    ok: false,
    status,
    headers: { get: () => 'text/plain; charset=utf-8' },
    json: async () => null,
  };
}

function eventStreamResponse(body) {
  const bytes = new TextEncoder().encode(body);
  let delivered = false;
  return {
    ok: true,
    status: 200,
    headers: { get: (name) => name.toLowerCase() === 'content-type' ? 'text/event-stream; charset=utf-8' : '' },
    body: {
      getReader() {
        return {
          async read() {
            if (delivered) return { done: true, value: undefined };
            delivered = true;
            return { done: false, value: bytes };
          },
          releaseLock() {},
        };
      },
    },
  };
}

async function settle() {
  for (let index = 0; index < 4; index += 1) await new Promise((resolve) => setImmediate(resolve));
}

function findByTag(element, tagName) {
  if (element.tagName === tagName.toUpperCase()) return element;
  for (const child of element.children) {
    const match = findByTag(child, tagName);
    if (match) return match;
  }
  return null;
}

function findByText(element, tagName, text) {
  if (element.tagName === tagName.toUpperCase() && element._textContent === text) return element;
  for (const child of element.children) {
    const match = findByText(child, tagName, text);
    if (match) return match;
  }
  return null;
}

async function dispatch(element, type, event = {}) {
  const listeners = element._listeners.get(type) || [];
  for (const listener of listeners) {
    await listener({ preventDefault() {}, ...event });
  }
}

async function loadRenderer({ knownFiles = [], initialJob: providedInitialJob, capabilities: providedCapabilities, fetchImpl, confirmResult = true, promptResult = '', showSaveFilePicker, desktopShell = {} } = {}) {
  const document = makeDocument();
  const sources = [];
  const scheduled = [];
  const timers = new Map();
  let nextTimerId = 1;
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      this.closed = false;
      sources.push(this);
    }

    addEventListener(type, listener) {
      this.listeners.set(type, listener);
    }

    close() { this.closed = true; }

    emit(type, data, lastEventId) {
      const listener = this.listeners.get(type);
      if (listener) listener({ data: JSON.stringify(data), lastEventId });
    }
  }
  const initialJob = providedInitialJob || {
    id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
    status: 'running', total: 4, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
  };
  const capabilities = providedCapabilities || {
    wrapper: { status: 'online' },
    qualities: Object.fromEntries(['aac-lc', 'alac', 'aac', 'atmos'].map((quality) => [quality, { available: true }])),
  };
  const requests = [];
  const prompts = [];
  const objectURLs = [];
  const defaultFetch = async (url, options = {}) => {
    requests.push({ url, options });
    switch (url) {
      case '/api/v1/capabilities':
        return jsonResponse(capabilities);
      case JOBS_LIST_URL:
        return jsonResponse({ jobs: [initialJob] });
      case '/api/v1/files':
        return jsonResponse({ files: knownFiles });
      case '/api/v1/jobs/job-1':
        return jsonResponse(initialJob);
      case '/api/v1/jobs/job-1/files':
        return jsonResponse({ files: [] });
      default:
        throw new Error(`unexpected request: ${url}`);
    }
  };
  const fetch = fetchImpl || defaultFetch;
  const source = fs.readFileSync(path.join(__dirname, '..', 'web', 'renderer.js'), 'utf8');
  vm.runInNewContext(source, {
    document,
    fetch,
    EventSource: FakeEventSource,
    AbortController,
    TextDecoder,
    CustomEvent: class { constructor(type, options) { this.type = type; this.detail = options?.detail; } },
    URL: {
      createObjectURL(blob) { objectURLs.push(blob); return `blob:amdl-${objectURLs.length}`; },
      revokeObjectURL() {},
    },
    window: {
      amdlJobData: require('../web/job-data'),
      location: { protocol: 'http:' },
      localStorage: { getItem: () => null, setItem() {} },
      dispatchEvent() {},
      // Timers are modelled as a cancellable queue so a cleared timer cannot
      // still be fired by a test, mirroring real setTimeout/clearTimeout.
      setTimeout: (callback) => {
        timers.set(nextTimerId, callback);
        scheduled.push(callback);
        return nextTimerId++;
      },
      clearTimeout: (id) => {
        const callback = timers.get(id);
        if (callback === undefined) return;
        timers.delete(id);
        const index = scheduled.indexOf(callback);
        if (index !== -1) scheduled.splice(index, 1);
      },
      confirm: () => confirmResult,
      prompt: (message) => { prompts.push(message); return promptResult; },
      showSaveFilePicker,
      desktopShell,
    },
    console,
  }, { filename: 'renderer.js' });
  await settle();
  return { ...document, sources, scheduled, requests, prompts, objectURLs };
}

test('401 keeps one bearer access key in renderer memory without cookies or URL secrets', async () => {
  const accessKey = '0123456789abcdef'.repeat(4);
  const requests = [];
  const page = await loadRenderer({
    promptResult: accessKey,
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (options.headers.Authorization !== `Bearer ${accessKey}`) {
        return {
          ok: false,
          status: 401,
          headers: { get: () => 'application/json' },
          json: async () => ({ error: { code: 'api_auth_required', message: 'authentication required' } }),
        };
      }
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'stopped' }, qualities: {} });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });
  assert.equal(page.prompts.length, 1);
  assert.equal(requests.filter((request) => request.url === '/api/v1/session').length, 0);
  assert.equal(requests.some((request) => request.url.includes(accessKey)), false);
  assert.ok(requests.some((request) => request.options.headers.Authorization === `Bearer ${accessKey}`));
  assert.equal(requests.every((request) => request.options.credentials === 'omit'), true);
});

test('bearer-authenticated jobs consume SSE through a header-capable fetch stream', async () => {
  const accessKey = 'abcdef0123456789'.repeat(4);
  const job = {
    id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
    status: 'running', total: 2, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
  };
  const requests = [];
  const page = await loadRenderer({
    initialJob: job,
    promptResult: accessKey,
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (options.headers.Authorization !== `Bearer ${accessKey}`) {
        return {
          ok: false,
          status: 401,
          headers: { get: () => 'application/json' },
          json: async () => ({ error: { code: 'api_auth_required', message: 'authentication required' } }),
        };
      }
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [job] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      if (url === '/api/v1/jobs/job-1/events') {
        return eventStreamResponse([
          'id: 17',
          'event: progress',
          'data: {"total":2,"succeeded":2,"failed":0,"skipped":0,"unavailable":0}',
          '',
          'id: 18',
          'event: state',
          'data: {"status":"succeeded"}',
          '',
          '',
        ].join('\n'));
      }
      throw new Error(`unexpected request: ${url}`);
    },
  });
  await settle();

  assert.equal(page.prompts.length, 1);
  assert.equal(page.sources.length, 0, 'bearer mode must not use headerless EventSource');
  const streamRequest = requests.find((request) => request.url === '/api/v1/jobs/job-1/events');
  assert.ok(streamRequest, 'the running task opens a fetch-based event stream');
  assert.equal(streamRequest.options.headers.Authorization, `Bearer ${accessKey}`);
  assert.equal(streamRequest.options.credentials, 'omit');
  assert.equal(requests.some((request) => request.url.includes(accessKey)), false);
  assert.match(page.elements.jobs.textContent, /完成/);
  assert.match(page.elements.jobs.textContent, /已处理：2\/2/);
});

test('browser file downloads carry the in-memory bearer without putting it in the link', async () => {
  const accessKey = '13579bdf02468ace'.repeat(4);
  const file = { id: 'file-1', jobId: 'job-1', name: 'finished.m4a', size: 2048 };
  const requests = [];
  const page = await loadRenderer({
    promptResult: accessKey,
    initialJob: { id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 },
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (options.headers.Authorization !== `Bearer ${accessKey}`) {
        return {
          ok: false,
          status: 401,
          headers: { get: () => 'application/json' },
          json: async () => ({ error: { code: 'api_auth_required', message: 'authentication required' } }),
        };
      }
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [file] });
      if (url === '/api/v1/files/file-1') {
        return {
          ok: true,
          status: 200,
          headers: { get: (name) => name.toLowerCase() === 'content-length' ? '2048' : 'audio/mp4' },
          blob: async () => ({ size: 2048 }),
        };
      }
      throw new Error(`unexpected request: ${url}`);
    },
  });

  const link = findByTag(page.elements.files, 'a');
  assert.equal(link.href, '/api/v1/files/file-1');
  assert.equal(link.href.includes(accessKey), false);
  await dispatch(link, 'click');
  await settle();

  const download = requests.find((request) => request.url === '/api/v1/files/file-1');
  assert.ok(download, 'the opaque file endpoint is fetched after the authenticated click');
  assert.equal(download.options.headers.Authorization, `Bearer ${accessKey}`);
  assert.equal(download.options.credentials, 'omit');
  assert.equal(page.objectURLs.length, 1);
});

test('Chromium bearer downloads stream to the selected file without buffering a large blob', async () => {
  const accessKey = '2468ace013579bdf'.repeat(4);
  const file = { id: 'file-1', jobId: 'job-1', name: 'large-archive.zip', size: 8 * 1024 * 1024 * 1024 };
  const writable = { marker: 'selected-file' };
  let streamedTo = null;
  let blobCalled = false;
  const page = await loadRenderer({
    promptResult: accessKey,
    initialJob: { id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 },
    showSaveFilePicker: async ({ suggestedName }) => {
      assert.equal(suggestedName, 'large-archive.zip');
      return { createWritable: async () => writable };
    },
    fetchImpl: async (url, options = {}) => {
      if (options.headers.Authorization !== `Bearer ${accessKey}`) {
        return {
          ok: false,
          status: 401,
          headers: { get: () => 'application/json' },
          json: async () => ({ error: { code: 'api_auth_required', message: 'authentication required' } }),
        };
      }
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [file] });
      if (url === '/api/v1/files/file-1') {
        return {
          ok: true,
          status: 200,
          headers: { get: () => 'application/zip' },
          body: { pipeTo: async (target) => { streamedTo = target; } },
          blob: async () => { blobCalled = true; return {}; },
        };
      }
      throw new Error(`unexpected request: ${url}`);
    },
  });

  await dispatch(findByTag(page.elements.files, 'a'), 'click');
  await settle();

  assert.equal(streamedTo, writable);
  assert.equal(blobCalled, false, 'the multi-gigabyte response must not be materialized as a Blob');
  assert.equal(page.objectURLs.length, 0);
});

test('legacy browsers refuse an unbounded authenticated archive instead of risking renderer OOM', async () => {
  const accessKey = '369cf258be147ad0'.repeat(4);
  let responseCancelled = false;
  let blobCalled = false;
  const page = await loadRenderer({
    promptResult: accessKey,
    initialJob: { id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 },
    fetchImpl: async (url, options = {}) => {
      if (options.headers.Authorization !== `Bearer ${accessKey}`) {
        return {
          ok: false,
          status: 401,
          headers: { get: () => 'application/json' },
          json: async () => ({ error: { code: 'api_auth_required', message: 'authentication required' } }),
        };
      }
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [{ id: 'file-1', name: 'unbounded.zip' }] });
      if (url === '/api/v1/files/file-1') {
        return {
          ok: true,
          status: 200,
          headers: { get: () => '' },
          body: { cancel: async () => { responseCancelled = true; } },
          blob: async () => { blobCalled = true; return {}; },
        };
      }
      throw new Error(`unexpected request: ${url}`);
    },
  });

  await dispatch(findByTag(page.elements.files, 'a'), 'click');
  await settle();

  assert.equal(responseCancelled, true);
  assert.equal(blobCalled, false);
  assert.match(page.elements['log-container'].textContent, /Chrome\/Edge/);
});

test('an explicitly available quality has no unavailable-configuration hint', async () => {
  const page = await loadRenderer({
    capabilities: {
      wrapper: { status: 'online' },
      qualities: { alac: { available: true } },
    },
  });

  assert.equal(page.elements['quality-help'].textContent, '');
});

test('named job SSE events update status, progress, tracks, files, and logs', async () => {
  const page = await loadRenderer();
  assert.equal(page.sources.length, 1, 'the active job is subscribed once');

  const source = page.sources[0];
  source.emit('progress', { total: 4, succeeded: 2, failed: 1, skipped: 1, unavailable: 0 }, '41');
  source.emit('item', { id: 'item-1', jobId: 'job-1', sourceId: 'song-1', name: 'Night Drive', status: 'succeeded', attempt: 1 }, '42');
  source.emit('file', { id: 'file-1', name: 'Night Drive.m4a', size: 2048 }, '43');
  source.emit('log', { level: 'info', message: 'Night Drive downloaded' }, '44');
  source.emit('state', { status: 'succeeded' }, '45');

  assert.match(page.elements.jobs.textContent, /完成/);
  assert.match(page.elements.jobs.textContent, /已处理：4\/4/);
  assert.match(page.elements.jobs.textContent, /Night Drive — 完成/);
  assert.match(page.elements.files.textContent, /Night Drive\.m4a/);
  assert.equal(findByTag(page.elements.files, 'a').href, '/api/v1/files/file-1');
  assert.match(page.elements['log-container'].textContent, /Night Drive downloaded/);

  const scheduledBeforeError = page.scheduled.length;
  source.onerror();
  assert.equal(page.scheduled.length, scheduledBeforeError, 'a completed job does not schedule a named event-stream reconnect');
});

test('personal playlist failure codes render fixed safe guidance', async () => {
  const page = await loadRenderer({
    initialJob: {
      id: 'job-1',
      url: 'https://music.apple.com/us/library/playlist/p.abc',
      quality: 'alac',
      existingPolicy: 'skip',
      status: 'failed',
      errorCode: 'playlist_auth_required',
      errorMessage: 'Authorization: Bearer secret-token',
      total: 0,
      succeeded: 0,
      failed: 1,
      skipped: 0,
      unavailable: 0,
    },
  });
  const text = page.elements.jobs.textContent;
  assert.match(text, /个人歌单需要配置 Media-User-Token/);
  assert.doesNotMatch(text, /secret-token|Authorization/);
});

test('HTTP aliases and partial SSE updates share one item model without stale tracks', async () => {
  const page = await loadRenderer({ initialJob: {
    jobId: 'job-1', sourceUrl: 'https://music.apple.com/us/song/123', status: 'running',
    existing_policy: 'overwrite', tracks: [{ id: 'item-1', title: 'Original title', status: 'running' }],
  } });
  const source = page.sources[0];
  assert.match(page.elements.jobs.textContent, /Original title/);
  assert.match(page.elements.jobs.textContent, /覆盖已有文件/);
  source.emit('item', { track: { id: 'item-1', status: 'failed', error_code: 'media_transient' } }, 'legacy-item');
  assert.match(page.elements.jobs.textContent, /Original title — 失败 · 媒体传输或解密失败，可重试/);
  source.emit('state', { job: { items: [{ id: 'item-1', name: 'Refreshed title', status: 'succeeded' }] } }, 'snapshot');
  assert.match(page.elements.jobs.textContent, /Refreshed title — 完成/);
  assert.doesNotMatch(page.elements.jobs.textContent, /Original title/);
  source.emit('state', { status: 'running' }, 'partial');
  assert.match(page.elements.jobs.textContent, /Refreshed title/);
  source.emit('state', { items: [] }, 'empty');
  assert.doesNotMatch(page.elements.jobs.textContent, /Refreshed title/);
  source.emit('state', { tracks: [{ title: 'Legacy replacement', status: 'queued' }] }, 'legacy-empty');
  assert.match(page.elements.jobs.textContent, /Legacy replacement/);
});

test('legacy file aliases retain scoped ownership and cannot return through late events after deletion', async () => {
  const page = await loadRenderer({
    fetchImpl: async (url, options = {}) => {
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: {} });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ jobId: 'job-1', status: 'running' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      if (url === '/api/v1/jobs/job-1' && options.method === 'DELETE') return { ok: true, status: 204, headers: { get: () => '' } };
      throw new Error(`unexpected request: ${url}`);
    },
  });
  const source = page.sources[0];
  source.emit('file', { file: { fileId: 'file-1', filename: 'Legacy.m4a', managed: false } }, 'legacy-file');
  assert.match(page.elements.files.textContent, /Legacy\.m4a/);
  assert.match(page.elements.files.textContent, /已有文件/);
  assert.equal(findByTag(page.elements.files, 'a').href, '/api/v1/files/file-1');
  source.emit('state', { status: 'succeeded' }, 'terminal');
  await dispatch(findByText(page.elements.jobs, 'button', '删除任务和文件'), 'click');
  source.emit('state', { jobId: 'job-1', status: 'running' }, 'late-state');
  source.emit('file', { fileId: 'file-1', job_id: 'job-1', filename: 'Legacy.m4a' }, 'late-file');
  assert.equal(page.elements.jobs.children.length, 0);
  assert.equal(page.elements.files.children.length, 0);
});

test('incomplete public playlist failures render fixed guidance without upstream details', async () => {
  const page = await loadRenderer({
    initialJob: {
      id: 'job-incomplete',
      url: 'https://music.apple.com/us/playlist/public/pl.u-abc',
      status: 'failed',
      errorCode: 'playlist_incomplete',
      errorMessage: 'response body contains sentinel',
    },
  });
  const text = page.elements.jobs.textContent;
  assert.match(text, /公开歌单页面的结构化数据不完整/);
  assert.doesNotMatch(text, /sentinel/);
});

test('the existing-file index is shown through opaque v1 file links', async () => {
  const page = await loadRenderer({
    knownFiles: [{ id: 'unmanaged-1', name: 'already-downloaded.m4a', size: 4097, unmanaged: true }],
  });

  assert.match(page.elements.files.textContent, /already-downloaded\.m4a/);
  assert.match(page.elements.files.textContent, /已有文件/);
  assert.equal(findByTag(page.elements.files, 'a').href, '/api/v1/files/unmanaged-1');
});

test('a batch submission posts the v1 queue contract and renders every created job', async () => {
  const jobs = [];
  const requests = [];
  const capabilities = {
    wrapper: { status: 'online' },
    qualities: Object.fromEntries(['aac-lc', 'alac', 'aac', 'atmos'].map((quality) => [quality, { available: true }])),
  };
  const page = await loadRenderer({
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      if (url === '/api/v1/jobs' && options.method === 'POST') {
        const body = JSON.parse(options.body);
        jobs.splice(0, jobs.length, ...body.urls.map((value, index) => ({
          id: `job-${index + 1}`, url: value, quality: body.quality, existingPolicy: body.existingPolicy,
          status: 'queued', total: 0, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
        })));
        return jsonResponse({ jobs });
      }
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  page.elements['music-urls'].value = 'https://music.apple.com/us/album/first/1\nhttps://music.apple.com/us/album/second/2';
  page.elements.quality.value = 'aac';
  page.elements['existing-policy'].value = 'overwrite';
  await dispatch(page.elements['job-form'], 'submit');

  const request = requests.find((entry) => entry.url === '/api/v1/jobs' && entry.options.method === 'POST');
  assert.ok(request, 'the form uses the v1 job endpoint');
  assert.deepEqual(JSON.parse(request.options.body), {
    urls: ['https://music.apple.com/us/album/first/1', 'https://music.apple.com/us/album/second/2'],
    quality: 'aac',
    existingPolicy: 'overwrite',
    existing_policy: 'overwrite',
  });
  assert.equal(page.elements['music-urls'].value, '');
  assert.match(page.elements.jobs.textContent, /first/);
  assert.match(page.elements.jobs.textContent, /second/);
  assert.equal(page.sources.length, 0, 'queued jobs use one bounded queue poll instead of per-job event streams');
  assert.equal(page.scheduled.length, 2, 'one capability refresh and one shared queue poll are scheduled');
});

test('a 100 URL queue keeps one poll and only streams the single running job', async () => {
  let jobs = [];
  const requests = [];
  const capabilities = {
    wrapper: { status: 'online' },
    qualities: { alac: { available: true } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'unused', status: 'succeeded' },
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      if (url === '/api/v1/jobs' && options.method === 'POST') {
        const body = JSON.parse(options.body);
        jobs = body.urls.map((value, index) => ({ id: `batch-${index + 1}`, url: value, quality: 'alac', status: 'queued' }));
        return jsonResponse({ jobs });
      }
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs });
      if (url === '/api/v1/jobs/batch-1/cancel' && options.method === 'POST') {
        jobs[0] = { ...jobs[0], status: 'cancelled' };
        return jsonResponse({ job: jobs[0] });
      }
      if (url === '/api/v1/jobs/batch-1') return jsonResponse(jobs[0]);
      if (url === '/api/v1/jobs/batch-1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  page.elements['music-urls'].value = Array.from(
    { length: 100 },
    (_, index) => `https://music.apple.com/us/album/batch/${index + 1}`,
  ).join('\n');
  await dispatch(page.elements['job-form'], 'submit');

  assert.equal(page.sources.length, 0, 'queued batch must not consume 100 EventSource connections');
  assert.equal(page.scheduled.length, 2, 'the whole queued batch shares a single refresh timer');

  jobs[0] = { ...jobs[0], status: 'running' };
  const queueRefresh = page.scheduled[1];
  await queueRefresh();
  await settle();

  assert.equal(page.sources.length, 1, 'only the serial worker job receives an EventSource');
  assert.match(page.sources[0].url, /\/jobs\/batch-1\/events/);
  assert.ok(findByText(page.elements.jobs, 'button', '取消'), 'cancel remains reachable while 99 jobs are queued');

  await dispatch(findByText(page.elements.jobs, 'button', '取消'), 'click');
  assert.ok(requests.some((entry) => entry.url === '/api/v1/jobs/batch-1/cancel' && entry.options.method === 'POST'));
  assert.equal(page.sources[0].closed, true, 'the stream closes after cancellation reaches a terminal state');
});

test('queue controls do not offer an unsupported retry after cancellation', async () => {
  const job = {
    id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
    status: 'running', total: 1, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
  };
  const requests = [];
  const page = await loadRenderer({
    initialJob: job,
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [job] });
      if (url === '/api/v1/files' || url === '/api/v1/jobs/job-1/files') return jsonResponse({ files: [] });
      if (url === '/api/v1/jobs/job-1/cancel' && options.method === 'POST') {
        job.status = 'cancelled';
        return jsonResponse({ job });
      }
      if (url === '/api/v1/jobs/job-1') return jsonResponse(job);
      throw new Error(`unexpected request: ${url}`);
    },
  });

  await dispatch(findByText(page.elements.jobs, 'button', '取消'), 'click');
  assert.ok(requests.some((entry) => entry.url === '/api/v1/jobs/job-1/cancel' && entry.options.method === 'POST'));
  assert.match(page.elements.jobs.textContent, /已取消/);
  assert.equal(findByText(page.elements.jobs, 'button', '重试失败项'), null, 'cancelled jobs are not retryable through the v1 API');
  assert.equal(requests.some((entry) => entry.url === '/api/v1/jobs/job-1/retry'), false);
});

test('a completed task exposes its own archive download and can be removed from history', async () => {
  const job = {
    id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
    status: 'succeeded', total: 1, succeeded: 1, failed: 0, skipped: 0, unavailable: 0,
  };
  const ownedFile = { id: 'file-1', jobId: 'job-1', name: 'finished.m4a', size: 2048 };
  const requests = [];
  const page = await loadRenderer({
    initialJob: job,
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [job] });
      if (url === '/api/v1/files') return jsonResponse({ files: [ownedFile] });
      if (url === '/api/v1/jobs/job-1' && options.method !== 'DELETE') return jsonResponse(job);
      if (url === '/api/v1/jobs/job-1/files') return jsonResponse({ files: [ownedFile] });
      if (url === '/api/v1/jobs/job-1' && options.method === 'DELETE') return { ok: true, status: 204, headers: { get: () => '' } };
      throw new Error(`unexpected request: ${url}`);
    },
  });

  const archive = findByText(page.elements.jobs, 'a', '下载此任务');
  assert.ok(archive, 'a completed task has a task-scoped archive action');
  assert.equal(archive.href, '/api/v1/jobs/job-1/archive');

  await dispatch(findByText(page.elements.jobs, 'button', '刷新详情'), 'click');
  assert.equal(page.elements['archive-link'].hidden, true, 'global archive stays hidden because archives are task-scoped');

  const remove = findByText(page.elements.jobs, 'button', '删除任务和文件');
  assert.ok(remove, 'a completed task can be removed with its registered files');
  await dispatch(remove, 'click');

  assert.ok(requests.some((entry) => entry.url === '/api/v1/jobs/job-1' && entry.options.method === 'DELETE'));
  assert.equal(page.elements['jobs-empty'].hidden, false);
  assert.equal(page.elements['archive-link'].hidden, true, 'deleting a task cannot reveal a stale global archive link');
  assert.doesNotMatch(page.elements.files.textContent, /finished\.m4a/, 'deleting a task also removes its owned file from the UI index');
});

test('deleting a task removes a file received only through its SSE stream', async () => {
  const job = {
    id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
    status: 'running', total: 1, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
  };
  const requests = [];
  const page = await loadRenderer({
    initialJob: job,
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [job] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      if (url === '/api/v1/jobs/job-1' && options.method === 'DELETE') return { ok: true, status: 204, headers: { get: () => '' } };
      throw new Error(`unexpected request: ${url}`);
    },
  });

  const source = page.sources[0];
  source.emit('file', { id: 'file-1', name: 'streamed.m4a', size: 2048 }, 'file-1');
  source.emit('state', { status: 'succeeded' }, 'done-1');
  assert.match(page.elements.files.textContent, /streamed\.m4a/, 'the live SSE file is visible before deletion');

  await dispatch(findByText(page.elements.jobs, 'button', '删除任务和文件'), 'click');

  assert.ok(requests.some((entry) => entry.url === '/api/v1/jobs/job-1' && entry.options.method === 'DELETE'));
  assert.doesNotMatch(page.elements.files.textContent, /streamed\.m4a/, 'deleting the task removes its SSE-only file from the UI index');
});

test('declining destructive task deletion sends no DELETE request', async () => {
  const job = { id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 };
  const requests = [];
  const page = await loadRenderer({
    initialJob: job,
    confirmResult: false,
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [job] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  const remove = findByText(page.elements.jobs, 'button', '删除任务和文件');
  assert.ok(remove, 'terminal tasks expose the destructive deletion control');
  await dispatch(remove, 'click');

  assert.equal(requests.filter((entry) => entry.url === '/api/v1/jobs/job-1' && entry.options.method === 'DELETE').length, 0);
  assert.match(page.elements.jobs.textContent, /完成/);
});

function deletionBarrier() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

test('settings exposes all desktop service actions without passing credentials or arguments', async () => {
  const actions = {
    'settings-download-location': 'chooseDownloadLocation', 'settings-startup': 'openDownloadSettings', 'settings-credentials': 'openCredentials',
    'settings-start-service': 'startService', 'settings-diagnostics': 'showDiagnostics',
    'settings-repair-runtime': 'repairRuntime', 'settings-stop-service': 'stopService',
  };
  const calls = [];
  const desktopShell = Object.fromEntries(Object.values(actions).map((method) => [method, async (...args) => {
    calls.push({ method, args }); return { ok: true };
  }]));
  const page = await loadRenderer({ desktopShell });
  assert.equal(page.elements['desktop-settings'].hidden, false);
  assert.equal(page.elements['desktop-settings-browser-note'].hidden, true);
  for (const [id, method] of Object.entries(actions)) {
    await dispatch(page.elements[id], 'click');
    assert.deepEqual(calls.at(-1), { method, args: [] });
  }
});

test('browser settings hides desktop-only service actions', async () => {
  const page = await loadRenderer();
  assert.equal(page.elements['desktop-settings'].hidden, true);
  assert.equal(page.elements['desktop-settings-browser-note'].hidden, false);
});

test('desktop settings prevents duplicate actions and reports cancellation and failure inline', async () => {
  const gate = deletionBarrier(); let calls = 0;
  const page = await loadRenderer({ desktopShell: {
    stopService: async () => { calls += 1; return gate.promise; },
    openCredentials: async () => { throw new Error('raw-secret-sentinel'); },
  } });
  const pending = dispatch(page.elements['settings-stop-service'], 'click');
  await settle();
  try {
    assert.equal(page.elements['settings-stop-service'].disabled, true);
    await dispatch(page.elements['settings-stop-service'], 'click');
    assert.equal(calls, 1);
  } finally { gate.resolve({ ok: true, cancelled: true }); await pending; }
  assert.match(page.elements['desktop-settings-status'].textContent, /取消/);
  assert.equal(page.elements['settings-stop-service'].disabled, false);
  await dispatch(page.elements['settings-credentials'], 'click');
  assert.match(page.elements['desktop-settings-status'].textContent, /未能完成/);
  assert.doesNotMatch(page.elements['desktop-settings-status'].textContent, /raw-secret-sentinel/);
});

async function deletionFixture(intercept = () => undefined) {
  const job = { id: 'job-1', status: 'running', total: 1, succeeded: 0 };
  const file = { id: 'file-1', jobId: job.id, name: 'finished.m4a' };
  const requests = [];
  const page = await loadRenderer({ fetchImpl: async (url, options = {}) => {
    requests.push({ url, options });
    const overridden = intercept(url, options);
    if (overridden !== undefined) return overridden;
    if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: {} });
    if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ ...job }] });
    if (url === '/api/v1/files') return jsonResponse({ files: [file] });
    if (url === '/api/v1/jobs/job-1/files') return jsonResponse({ files: [{ id: file.id, name: file.name }] });
    if (url === '/api/v1/jobs/job-1') return options.method === 'DELETE'
      ? { ok: true, status: 204, headers: { get: () => '' } } : jsonResponse({ ...job });
    throw new Error(`unexpected request: ${url}`);
  } });
  const source = page.sources[0];
  job.status = 'succeeded'; job.succeeded = 1;
  source.emit('state', { ...job }, 'done');
  return { ...page, requests, source, job, file };
}

test('delete controls survive capability polling and refreshed job details', async () => {
  const page = await deletionFixture();
  const remove = findByText(page.elements.jobs, 'button', '删除任务和文件');
  await page.scheduled[0]();
  await dispatch(findByText(page.elements.jobs, 'button', '刷新详情'), 'click');
  page.source.emit('item', { id: 'track-1', name: 'Finished track', status: 'succeeded' }, 'late-item');
  assert.equal(findByText(page.elements.jobs, 'button', '删除任务和文件'), remove);
  assert.match(page.elements.jobs.textContent, /Finished track/);
});

test('slow deletion has immediate busy feedback and cannot send a second DELETE', async () => {
  const gate = deletionBarrier();
  const page = await deletionFixture((url, options) => options.method === 'DELETE' ? gate.promise : undefined);
  const remove = findByText(page.elements.jobs, 'button', '删除任务和文件');
  const pending = dispatch(remove, 'click');
  await settle();
  try {
    const busy = findByText(page.elements.jobs, 'button', '正在删除…');
    assert.ok(busy, 'deletion is visibly in progress');
    assert.equal(busy.disabled, true);
    await dispatch(remove, 'click'); // A queued click on the original node must also be ignored.
    assert.equal(page.requests.filter((r) => r.options.method === 'DELETE').length, 1);
    assert.equal(page.elements.jobs.children.length, 1, 'wait for server confirmation before hiding the task');
  } finally {
    gate.resolve({ ok: true, status: 204, headers: { get: () => '' } });
    await pending;
  }
  assert.equal(page.elements.jobs.children.length, 0);
});

test('failed deletion is visible on the card, preserves files and can be retried', async () => {
  let fail = true;
  const page = await deletionFixture((url, options) => options.method === 'DELETE' && fail ? errorResponse(409) : undefined);
  await dispatch(findByText(page.elements.jobs, 'button', '删除任务和文件'), 'click');
  assert.match(page.elements.jobs.textContent, /删除任务失败.*409/);
  assert.match(page.elements.files.textContent, /finished\.m4a/);
  const retry = findByText(page.elements.jobs, 'button', '删除任务和文件');
  assert.equal(retry.disabled, false);
  fail = false;
  await dispatch(retry, 'click');
  assert.equal(page.elements.jobs.children.length, 0);
  assert.equal(page.elements.files.children.length, 0);
});

test('pending deletion stays visible through stale events and empty history until acknowledged', async () => {
  const gate = deletionBarrier();
  let omitJob = false;
  const page = await deletionFixture((url, options) => {
    if (options.method === 'DELETE') return gate.promise;
    if (omitJob && url === JOBS_LIST_URL) return jsonResponse({ jobs: [] });
    return undefined;
  });
  const pending = dispatch(findByText(page.elements.jobs, 'button', '删除任务和文件'), 'click');
  await settle();
  try {
    page.source.emit('state', { status: 'running' }, 'buffered-state');
    assert.ok(findByText(page.elements.jobs, 'button', '正在删除…'));
    omitJob = true;
    await dispatch(page.elements['refresh-btn'], 'click');
    assert.ok(findByText(page.elements.jobs, 'button', '正在删除…'), 'a list response is not the DELETE acknowledgement');
  } finally {
    gate.resolve(errorResponse(409));
    await pending;
  }
  assert.match(page.elements.jobs.textContent, /删除任务失败.*409/);
  assert.ok(findByText(page.elements.jobs, 'button', '删除任务和文件'));
});

for (const stalePath of [JOBS_LIST_URL, '/api/v1/jobs/job-1', '/api/v1/jobs/job-1/files', '/api/v1/files']) {
  test(`successful deletion rejects a late response from ${stalePath}`, async () => {
    const gate = deletionBarrier(); const entered = deletionBarrier();
    let hold = false;
    const page = await deletionFixture((url, options) => {
      if (hold && url === stalePath && options.method !== 'DELETE') {
        entered.resolve();
        return gate.promise;
      }
      return undefined;
    });
    hold = true;
    const detail = stalePath.startsWith('/api/v1/jobs/job-1');
    const refreshing = dispatch(detail ? findByText(page.elements.jobs, 'button', '刷新详情') : page.elements['refresh-btn'], 'click');
    await entered.promise;
    await dispatch(findByText(page.elements.jobs, 'button', '删除任务和文件'), 'click');
    const payload = stalePath === JOBS_LIST_URL ? { jobs: [page.job] }
      : stalePath.endsWith('/files') ? { files: [page.file] } : page.job;
    gate.resolve(jsonResponse(payload));
    await refreshing;
    assert.equal(page.elements.jobs.children.length, 0, 'late reads cannot resurrect the deleted task');
    assert.equal(page.elements.files.children.length, 0, 'late reads cannot resurrect deleted files');
  });
}

test('successful deletion ignores buffered SSE state, progress, item, file and message events', async () => {
  const page = await deletionFixture();
  await dispatch(findByText(page.elements.jobs, 'button', '删除任务和文件'), 'click');
  page.source.emit('state', { ...page.job, status: 'running' }, 'old-state');
  page.source.emit('progress', { total: 1, succeeded: 1 }, 'old-progress');
  page.source.emit('item', { id: 'track-1', status: 'succeeded' }, 'old-item');
  page.source.emit('file', { id: 'late-file', name: 'late.m4a' }, 'old-file');
  page.source.onmessage({ data: JSON.stringify({ job: page.job }), lastEventId: 'old-message' });
  assert.equal(page.elements.jobs.children.length, 0);
  assert.equal(page.elements.files.children.length, 0);
  assert.equal(page.sources.length, 1, 'deleted jobs must not reopen an event stream');
});

test('refresh restores queue, capabilities, and the permanent file index', async () => {
  let capabilities = {
    wrapper: { status: 'online' },
    qualities: Object.fromEntries(['aac-lc', 'alac', 'aac', 'atmos'].map((quality) => [quality, { available: true }])),
  };
  let jobs = [{
    id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
    status: 'running', total: 1, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
  }];
  let files = [];
  const page = await loadRenderer({
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs });
      if (url === '/api/v1/files') return jsonResponse({ files });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  capabilities = {
    wrapper: { status: 'needs_2fa' },
    qualities: { 'aac-lc': { available: true }, alac: { available: false, action: '请先完成两步验证' }, aac: { available: true }, atmos: { available: false } },
  };
  jobs = [{ ...jobs[0], status: 'succeeded', succeeded: 1 }];
  files = [{ id: 'file-1', name: 'finished.m4a', size: 2048 }];
  await dispatch(page.elements['refresh-btn'], 'click');

  assert.equal(page.elements.quality.options.find((option) => option.value === 'alac').disabled, true);
  assert.equal(page.elements.quality.value, 'aac-lc', 'refresh chooses a currently available quality');
  assert.equal(page.elements['twofa-section'].hidden, false);
  assert.match(page.elements.jobs.textContent, /完成/);
  assert.match(page.elements.files.textContent, /finished\.m4a/);
});

test('two-factor UI rejects invalid codes and submits exactly six digits to the local API', async () => {
  let capabilities = {
    wrapper: { status: 'needs_2fa' },
    qualities: { 'aac-lc': { available: true }, alac: { available: false, action: '请先完成两步验证' } },
  };
  const requests = [];
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url, options = {}) => {
      requests.push({ url, options });
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      if (url === '/api/v1/wrapper/2fa' && options.method === 'POST') {
        capabilities = { wrapper: { status: 'online' }, qualities: { 'aac-lc': { available: true }, alac: { available: true } } };
        return jsonResponse({ ok: true });
      }
      throw new Error(`unexpected request: ${url}`);
    },
  });

  assert.equal(page.elements['twofa-section'].hidden, false);
  page.elements['twofa-code'].value = '12345';
  await dispatch(page.elements['twofa-submit'], 'click');
  assert.equal(requests.filter((entry) => entry.url === '/api/v1/wrapper/2fa').length, 0);

  page.elements['twofa-code'].value = '123456';
  await dispatch(page.elements['twofa-submit'], 'click');
  const request = requests.find((entry) => entry.url === '/api/v1/wrapper/2fa');
  assert.deepEqual(JSON.parse(request.options.body), { code: '123456' });
  assert.equal(page.elements['twofa-code'].value, '');
  assert.equal(page.elements['twofa-section'].hidden, true);
});

test('capability polling reveals a new two-factor challenge without manual refresh', async () => {
  let capabilities = {
    wrapper: { status: 'starting' },
    qualities: { alac: { available: false, action: '正在启动解密服务' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
	});

	assert.equal(page.elements['twofa-section'].hidden, true);
	assert.equal(page.elements['wrapper-status'].textContent, '启动中');
	capabilities = {
    wrapper: { status: 'needs_2fa' },
    qualities: { alac: { available: false, action: '请先完成两步验证' } },
  };
  const scheduledRefresh = page.scheduled.shift();
  if (scheduledRefresh) await scheduledRefresh();
  await settle();

  assert.equal(page.elements['wrapper-status'].textContent, '需要两步验证');
  assert.equal(page.elements['twofa-section'].hidden, false);
});

test('502/503/504 capability failures show actionable proxy wording and keep polling', async () => {
  for (const scenario of [
    { status: 502, wording: '本地 WSL 服务桥暂时不可用（HTTP 502），正在自动重试' },
    { status: 503, wording: '本地 WSL 服务桥暂时不可用（HTTP 503），正在自动重试' },
    { status: 504, wording: '本地 WSL 服务桥暂时不可用（HTTP 504），正在自动重试' },
  ]) {
    let capabilityRequests = 0;
    const page = await loadRenderer({
      initialJob: { id: 'job-1', status: 'succeeded' },
      fetchImpl: async (url) => {
        if (url === '/api/v1/capabilities') {
          capabilityRequests += 1;
          return errorResponse(scenario.status);
        }
        if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
        if (url === '/api/v1/files') return jsonResponse({ files: [] });
        throw new Error(`unexpected request: ${url}`);
      },
    });

    assert.match(page.elements['log-container'].textContent, new RegExp(scenario.wording));
    const scheduledBeforeRefresh = page.scheduled.length;
    const refresh = page.scheduled[0];
    assert.ok(refresh, 'capability polling timer must be scheduled after an initial failure');
    await refresh();
    await settle();
    assert.equal(capabilityRequests, 2);
    assert.ok(page.scheduled.length > scheduledBeforeRefresh, 'failed capability polling must schedule its next retry');
    assert.match(page.elements['log-container'].textContent, new RegExp(scenario.wording));
  }
});

test('initial capability or data failure never claims the task service is connected', async () => {
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return errorResponse(503);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  assert.doesNotMatch(page.elements['log-container'].textContent, /已连接本机任务服务/);
  assert.equal(page.elements['wrapper-status'].textContent, '重连中');
});

test('stale capability state is not shown as online and half-entered 2FA survives recovery', async () => {
  let capabilityAttempt = 0;
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') {
        capabilityAttempt += 1;
        if (capabilityAttempt === 1) return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
        if (capabilityAttempt === 2) return errorResponse(502);
        return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      }
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });
  page.elements['twofa-code'].value = '123';

  const refresh = page.scheduled.shift();
  assert.ok(refresh, 'capability retry timer must be present');
  await refresh();
  await settle();
  assert.equal(page.elements['wrapper-status'].textContent, '重连中');
  assert.equal(page.elements['twofa-section'].hidden, false);
  assert.equal(page.elements['twofa-code'].value, '123');

  const recovery = page.scheduled.shift();
  assert.ok(recovery, 'stale capability state must continue polling');
  await recovery();
  await settle();
  assert.equal(page.elements['wrapper-status'].textContent, '在线（登录未确认）');
  assert.equal((page.elements['log-container'].textContent.match(/本地 WSL 服务桥已恢复。/g) || []).length, 1);

  const secondRecovery = page.scheduled.shift();
  if (secondRecovery) await secondRecovery();
  await settle();
  assert.equal((page.elements['log-container'].textContent.match(/本地 WSL 服务桥已恢复。/g) || []).length, 1);
});

test('a capabilities fetch rejection invalidates an old ready snapshot and recovers once', async () => {
  let capabilityAttempt = 0;
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') {
        capabilityAttempt += 1;
        if (capabilityAttempt === 1 || capabilityAttempt === 3) {
          return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
        }
        throw new TypeError('transport sentinel must not reach renderer logs');
      }
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });
  page.elements['twofa-code'].value = '123';

  const rejectedRefresh = page.scheduled.shift();
  assert.ok(rejectedRefresh, 'capability retry timer must be present');
  await rejectedRefresh();
  await settle();
  assert.equal(page.elements['wrapper-status'].textContent, '重连中');
  assert.equal(page.elements['quality'].options[0].disabled, true);
  assert.equal(page.elements['twofa-code'].value, '123');
  assert.match(page.elements['log-container'].textContent, /本地 WSL 服务桥暂时不可用/);
  assert.doesNotMatch(page.elements['log-container'].textContent, /transport sentinel/);

  const recoveredRefresh = page.scheduled.shift();
  assert.ok(recoveredRefresh, 'transport rejection must continue polling');
  await recoveredRefresh();
  await settle();
  assert.equal(page.elements['wrapper-status'].textContent, '在线（登录未确认）');
  assert.equal((page.elements['log-container'].textContent.match(/本地 WSL 服务桥已恢复。/g) || []).length, 1);
});

test('manual capability refreshes share one in-flight request', async () => {
  let capabilityAttempt = 0;
  let release;
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') {
        capabilityAttempt += 1;
        if (capabilityAttempt === 2) {
          await new Promise((resolve) => { release = resolve; });
        }
        return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      }
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  const first = dispatch(page.elements['refresh-btn'], 'click');
  await settle();
  const second = dispatch(page.elements['refresh-btn'], 'click');
  await settle();
  assert.equal(capabilityAttempt, 2, 'the two refreshes must share the second request');
  release();
  await Promise.all([first, second]);
  assert.equal(capabilityAttempt, 2);
});

test('repair invalidates an in-flight capabilities generation before publishing the new response', async () => {
  let capabilityCalls = 0;
  let releaseOldCapabilities;
  const oldCapabilities = new Promise((resolve) => { releaseOldCapabilities = resolve; });
  const page = await loadRenderer({
    desktopShell: { repairRuntime: async () => ({ ok: true }) },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') {
        capabilityCalls += 1;
        if (capabilityCalls === 1) return oldCapabilities;
        return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      }
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  await dispatch(page.elements['repair-service-button'], 'click');
  assert.equal(capabilityCalls, 2, 'repair must issue a fresh capabilities request');
  assert.equal(page.elements['wrapper-status'].textContent, '在线（登录未确认）');

  // The old request resolves after repair. Its response must be ignored even
  // if it claims a different wrapper state.
  releaseOldCapabilities(jsonResponse({ wrapper: { status: 'error' }, qualities: {} }));
  await settle();
  assert.equal(page.elements['wrapper-status'].textContent, '在线（登录未确认）');
});

test('partial two-factor input survives transient wrapper status changes', async () => {
  let capabilities = {
    wrapper: { status: 'needs_2fa' },
    qualities: { alac: { available: false, action: '请先完成两步验证' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  page.elements['twofa-code'].value = '123';
  capabilities = {
    wrapper: { status: 'starting' },
    qualities: { alac: { available: false, action: '正在完成登录' } },
  };
  const startingRefresh = page.scheduled.shift();
  if (startingRefresh) await startingRefresh();
  await settle();

  assert.equal(page.elements['twofa-section'].hidden, false);
  assert.equal(page.elements['twofa-code'].value, '123');

  capabilities = {
    wrapper: { status: 'error' },
    qualities: { alac: { available: false, action: '登录暂时失败' } },
  };
  const errorRefresh = page.scheduled.shift();
  if (errorRefresh) await errorRefresh();
  await settle();

  assert.equal(page.elements['twofa-section'].hidden, false);
  assert.equal(page.elements['twofa-code'].value, '123');
});

test('post-auth Wrapper crash is shown without reopening two-factor input', async () => {
  const capabilities = {
    wrapper: {
      status: 'crashed_post_auth',
      lastExit: { exitCode: 139, signal: 'SIGSEGV', attempt: 3, phase: 'authenticated_initializing', portReady: false },
    },
    qualities: {
      alac: { available: false, action: 'Account verification completed, but Wrapper initialization crashed.' },
      aac: { available: false, action: 'Account verification completed, but Wrapper initialization crashed.' },
      'aac-lc': { available: false, action: 'Account verification completed, but Wrapper initialization crashed.' },
      atmos: { available: false, action: 'Account verification completed, but Wrapper initialization crashed.' },
    },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  assert.match(page.elements['wrapper-status'].textContent, /账号验证已完成.*SIGSEGV\/139/);
  assert.equal(page.elements['twofa-section'].hidden, true);
  assert.match(page.elements['quality-help'].textContent, /initialization crashed/i);
});

test('a definitive StoreServices auth failure is shown without reopening two-factor input', async () => {
  const capabilities = {
    wrapper: {
      status: 'error',
      lastExit: {
        exitCode: 1,
        attempt: 1,
        phase: 'authenticating',
        portReady: false,
        reason: 'store_services_auth_failed',
        authResponseType: 5,
        authCode: 2712847316,
        authCodeHex: '0xA1B2C3D4',
        authCodeTrusted: true,
      },
    },
    qualities: { alac: { available: false, action: '登录暂时失败' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  page.elements['twofa-code'].value = '123';
  const refresh = page.scheduled.shift();
  if (refresh) await refresh();
  await settle();

  assert.match(page.elements['wrapper-status'].textContent, /Apple 认证未完成.*0xA1B2C3D4/);
  assert.equal(page.elements['twofa-section'].hidden, true);
  assert.equal(page.elements['twofa-code'].value, '123');
  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /密码|代理|锁定|必须/);
});

test('an untrusted StoreServices code is hidden and uses the safe fallback', async () => {
  const capabilities = {
    wrapper: {
      status: 'error',
      lastExit: {
        exitCode: 1,
        attempt: 1,
        phase: 'authenticating',
        portReady: false,
        reason: 'store_services_auth_failed',
        authCode: 2123687976,
        authCodeHex: '0x7E94E828',
        authCodeTrusted: false,
      },
    },
    qualities: { alac: { available: false, action: '登录暂时失败' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  const refresh = page.scheduled.shift();
  if (refresh) await refresh();
  await settle();

  assert.match(page.elements['wrapper-status'].textContent, /当前版本无法可靠读取原因/);
  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /0x7E94E828|2123687976/);
});

test('a forged trusted StoreServices code is hidden when its decimal and hexadecimal forms disagree', async () => {
  const capabilities = {
    wrapper: {
      status: 'error',
      lastExit: {
        exitCode: 1,
        attempt: 1,
        phase: 'authenticating',
        portReady: false,
        reason: 'store_services_auth_failed',
        authCode: 2712847316,
        authCodeHex: '0x7E94E828',
        authCodeTrusted: true,
      },
    },
    qualities: { alac: { available: false, action: '登录暂时失败' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  const refresh = page.scheduled.shift();
  if (refresh) await refresh();
  await settle();

  assert.match(page.elements['wrapper-status'].textContent, /当前版本无法可靠读取原因/);
  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /0x7E94E828|0xA1B2C3D4|2712847316/);
});

test('a playback lease failure has its own neutral Wrapper status', async () => {
  const capabilities = {
    wrapper: {
      status: 'error',
      lastExit: {
        exitCode: 1,
        attempt: 3,
        phase: 'authenticating',
        portReady: false,
        reason: 'playback_lease_failed',
      },
    },
    qualities: { alac: { available: false, action: '登录暂时失败' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  assert.match(page.elements['wrapper-status'].textContent, /播放租约初始化未完成/);
  assert.equal(page.elements['twofa-section'].hidden, true);
});

test('desktop renderer exposes a visible repair entry and invokes the protected bridge', async () => {
  let calls = 0;
  const page = await loadRenderer({
    desktopShell: {
      repairRuntime: async () => { calls += 1; return { ok: true }; },
    },
  });

  assert.equal(page.elements['repair-service-button'].hidden, false);
  await dispatch(page.elements['repair-service-button'], 'click');
  assert.equal(calls, 1);
  assert.match(page.elements['log-container'].textContent, /修复完成/);
  assert.equal(page.elements['repair-service-button'].disabled, false);
});

test('browser renderer does not expose the desktop-only repair entry', async () => {
  const page = await loadRenderer();
  assert.equal(page.elements['repair-service-button'].hidden, true);
});

test('refresh closes stale SSE subscriptions after an active job becomes terminal', async () => {
  let jobs = [{
    id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
    status: 'running', total: 1, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
  }];
  const page = await loadRenderer({
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse({ wrapper: { status: 'online' }, qualities: { alac: { available: true } } });
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });
  const source = page.sources[0];

  jobs = [{ ...jobs[0], status: 'succeeded', succeeded: 1 }];
  await dispatch(page.elements['refresh-btn'], 'click');

  assert.equal(source.closed, true, 'a completed job must not retain a reconnecting event stream');
});

test('a terminal fallback SSE message also closes its event stream', async () => {
  const page = await loadRenderer();
  const source = page.sources[0];

  source.onmessage({ data: JSON.stringify({ id: 'job-1', status: 'cancelled' }), lastEventId: 'terminal-1' });

  assert.equal(source.closed, true, 'unnamed terminal events must not keep reconnecting');
});

// The audited fingerprint the manager compiles in and the renderer independently
// re-checks. A code-8 URLBagKeyNotFound diagnostic only maps to business copy
// when the full contract below is byte-exact.
const AUDITED_STORESERVICES_SHA256 =
  '6238261363d7943f1a0a27116b52544831f3e295e8533a634641fe5b09d92c7e';

function code8Diagnostic(overrides = {}) {
  return {
    exitCode: 1,
    attempt: 1,
    phase: 'authenticating',
    portReady: false,
    reason: 'store_services_auth_failed',
    authResponseType: 5,
    authCode: 8,
    authCodeHex: '0x00000008',
    authCodeTrusted: true,
    diagnosticSchemaVersion: 2,
    errorCategory: 'StoreServicesCore',
    errorName: 'URLBagKeyNotFound',
    urlBagKey: 'authenticateAccount',
    storeServicesCoreSha256: AUDITED_STORESERVICES_SHA256,
    ...overrides,
  };
}

function watchdogExit(overrides = {}) {
  return {
    signal: 'SIGTERM',
    exitCode: 143,
    reason: 'initialization_timeout',
    attempt: 3,
    phase: 'authenticating',
    portReady: false,
    terminationInitiator: 'initialization_watchdog',
    deadlineMs: 60000,
    runGeneration: 1,
    currentAttempt: 3,
    ...overrides,
  };
}

// Renders one already-sanitized wrapper snapshot (status plus whatever
// lifecycle fields the API published) and settles the first poll.
async function renderWithExit(wrapper) {
  const capabilities = {
    wrapper,
    qualities: { alac: { available: false, action: '当前不可用' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });
  const refresh = page.scheduled.shift();
  if (refresh) await refresh();
  await settle();
  return page;
}

async function renderWithCode8Exit(lastExit) {
  const capabilities = {
    wrapper: { status: 'error', lastExit },
    qualities: { alac: { available: false, action: '登录暂时失败' } },
  };
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url) => {
      if (url === '/api/v1/capabilities') return jsonResponse(capabilities);
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });
  const refresh = page.scheduled.shift();
  if (refresh) await refresh();
  await settle();
  return page;
}

test('a fully trusted code 8 URLBagKeyNotFound maps to the constrained network message', async () => {
  const page = await renderWithCode8Exit(code8Diagnostic());

  assert.match(page.elements['wrapper-status'].textContent, /无法获取 Apple 认证服务地址/);
  assert.match(page.elements['wrapper-status'].textContent, /无需重新输入密码或验证码/);
  // The copy must not name an unproven sub-cause or reopen credential entry.
  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /DNS|fake-?IP|无路由|防火墙/);
  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /0x00000008|诊断代码/);
  assert.equal(page.elements['twofa-section'].hidden, true);
});

test('code 8 from an unaudited StoreServicesCore fingerprint fails closed to the safe fallback', async () => {
  const page = await renderWithCode8Exit(code8Diagnostic({
    storeServicesCoreSha256: 'f'.repeat(64),
  }));

  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /无法获取 Apple 认证服务地址/);
  assert.match(page.elements['wrapper-status'].textContent, /Apple 认证未完成/);
  // The bare code is still allowed as an unmapped hint; the mapped copy is not.
  assert.match(page.elements['wrapper-status'].textContent, /0x00000008/);
});

test('code 8 under an unknown diagnostic schema fails closed to the safe fallback', async () => {
  const page = await renderWithCode8Exit(code8Diagnostic({ diagnosticSchemaVersion: 3 }));

  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /无法获取 Apple 认证服务地址/);
  assert.match(page.elements['wrapper-status'].textContent, /原因尚未映射/);
});

test('code 8 with a mismatched error category or name fails closed to the safe fallback', async () => {
  for (const overrides of [{ errorCategory: 'FairPlay' }, { errorName: 'SomethingElse' }, { urlBagKey: 'other' }]) {
    const page = await renderWithCode8Exit(code8Diagnostic(overrides));
    assert.doesNotMatch(page.elements['wrapper-status'].textContent, /无法获取 Apple 认证服务地址/, JSON.stringify(overrides));
  }
});

test('an untrusted code 8 never reaches the mapped network message', async () => {
  const page = await renderWithCode8Exit(code8Diagnostic({ authCodeTrusted: false }));

  assert.doesNotMatch(page.elements['wrapper-status'].textContent, /无法获取 Apple 认证服务地址/);
  assert.match(page.elements['wrapper-status'].textContent, /当前版本无法可靠读取原因/);
});

// A "starting" snapshot is only meaningful while it is current. Once capabilities
// stops returning a usable body, the last successful snapshot describes the past,
// so it must never keep being presented as live state. HTTP 403 is the real
// desktop failure mode (an old frame or endpoint generation loses its
// X-AMDL-Desktop-Token and the loopback proxy synthesizes 403), and 500 plus an
// invalid JSON body cover the other non-transport shapes.
test('a stale starting snapshot is invalidated by 403, 500, and invalid JSON while polling continues', async () => {
  for (const scenario of [
    { label: 'HTTP 403', makeResponse: () => errorResponse(403) },
    { label: 'HTTP 500', makeResponse: () => errorResponse(500) },
    {
      label: 'invalid JSON',
      makeResponse: () => ({
        ok: true,
        status: 200,
        headers: { get: () => 'application/json' },
        json: async () => { throw new SyntaxError('Unexpected token < in JSON at position 0'); },
      }),
    },
  ]) {
    let capabilityRequests = 0;
    const page = await loadRenderer({
      initialJob: { id: 'job-1', status: 'succeeded' },
      fetchImpl: async (url) => {
        if (url === '/api/v1/capabilities') {
          capabilityRequests += 1;
          // First poll succeeds and publishes a live "starting" snapshot.
          if (capabilityRequests === 1) {
            return jsonResponse({
              wrapper: { status: 'starting' },
              qualities: { alac: { available: false, action: '正在启动' } },
            });
          }
          return scenario.makeResponse();
        }
        if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
        if (url === '/api/v1/files') return jsonResponse({ files: [] });
        throw new Error(`unexpected request: ${url}`);
      },
    });

    // Half-entered 2FA must survive per the existing retention rule.
    page.elements['twofa-code'].value = '123';
    assert.equal(page.elements['wrapper-status'].textContent, '启动中', `${scenario.label}: precondition`);

    const scheduledBeforeFailure = page.scheduled.length;
    const refresh = page.scheduled.shift();
    assert.ok(refresh, `${scenario.label}: a polling timer must exist`);
    await refresh();
    await settle();

    assert.equal(capabilityRequests, 2, `${scenario.label}: the failing poll must have run`);
    assert.notEqual(
      page.elements['wrapper-status'].textContent,
      '启动中',
      `${scenario.label}: a failed capabilities read must not keep showing the old starting snapshot as current`,
    );
    assert.ok(
      page.scheduled.length > scheduledBeforeFailure - 1,
      `${scenario.label}: the scheduler must keep polling after a failure`,
    );
    assert.equal(page.elements['twofa-code'].value, '123', `${scenario.label}: half-entered 2FA must be retained`);
    // Raw proxy/transport/parser text must never reach the user-visible log.
    assert.doesNotMatch(
      page.elements['log-container'].textContent,
      /Unexpected token|X-AMDL-Desktop-Token|ECONNREFUSED|127\.0\.0\.1|localhost:\d+/,
      `${scenario.label}: raw proxy or parser text must not leak`,
    );
  }
});

// A capabilities request that never settles must not be able to stop the polling
// loop. Without a deadline the in-flight handle is never cleared, the scheduler
// never arms its next tick, and the last good snapshot stays on screen forever.
test('a capabilities request that never settles is aborted and does not freeze the scheduler', async () => {
  let capabilityRequests = 0;
  let hangingSignal = null;
  let releaseHanging = null;
  const page = await loadRenderer({
    initialJob: { id: 'job-1', status: 'succeeded' },
    fetchImpl: async (url, options = {}) => {
      if (url === '/api/v1/capabilities') {
        capabilityRequests += 1;
        if (capabilityRequests === 1) {
          return jsonResponse({
            wrapper: { status: 'starting' },
            qualities: { alac: { available: false, action: '正在启动' } },
          });
        }
        if (capabilityRequests === 2) {
          // Never resolves on its own. It may only end via an abort signal.
          hangingSignal = options.signal || null;
          return new Promise((resolve, reject) => {
            releaseHanging = resolve;
            if (options.signal) {
              options.signal.addEventListener('abort', () => {
                const aborted = new Error('The operation was aborted.');
                aborted.name = 'AbortError';
                reject(aborted);
              });
            }
          });
        }
        return jsonResponse({
          wrapper: { status: 'error', lastExit: { signal: 'SIGTERM', exitCode: 143, reason: 'initialization_timeout', attempt: 3 } },
          qualities: { alac: { available: false, action: '初始化超时' } },
        });
      }
      if (url === JOBS_LIST_URL) return jsonResponse({ jobs: [{ id: 'job-1', status: 'succeeded' }] });
      if (url === '/api/v1/files') return jsonResponse({ files: [] });
      throw new Error(`unexpected request: ${url}`);
    },
  });

  assert.equal(page.elements['wrapper-status'].textContent, '启动中', 'precondition: live starting snapshot');

  // Run the poll that hangs.
  const pollTimer = page.scheduled.shift();
  assert.ok(pollTimer, 'a polling timer must exist');
  const hangingPoll = pollTimer();
  await settle();
  assert.equal(capabilityRequests, 2, 'the hanging poll must have been issued');
  assert.ok(hangingSignal, 'the capabilities request must carry an abort signal so it can be bounded');

  // The deadline itself is scheduled through window.setTimeout, so firing the
  // pending callbacks is what a real timer expiry does here - no sleeping.
  for (const callback of page.scheduled.splice(0)) await callback();
  await settle();
  await hangingPoll;
  await settle();

  assert.equal(hangingSignal.aborted, true, 'the never-settling request must be aborted at its deadline');
  assert.notEqual(
    page.elements['wrapper-status'].textContent,
    '启动中',
    'an aborted capabilities read must not keep presenting the old starting snapshot as current',
  );

  // The in-flight handle must be cleared so a manual refresh starts a new
  // generation instead of re-awaiting the dead promise.
  const requestsBeforeManual = capabilityRequests;
  await dispatch(page.elements['refresh-btn'], 'click');
  await settle();
  assert.ok(
    capabilityRequests > requestsBeforeManual,
    'a manual refresh must create a new request generation after an aborted one',
  );

  // Late resolution of the abandoned promise must not resurrect old state or
  // clear the newer request.
  const statusAfterRecovery = page.elements['wrapper-status'].textContent;
  if (releaseHanging) {
    releaseHanging(jsonResponse({ wrapper: { status: 'starting' }, qualities: {} }));
    await settle();
  }
  assert.equal(
    page.elements['wrapper-status'].textContent,
    statusAfterRecovery,
    'a late response from the aborted generation must not resurrect the old snapshot',
  );
});

// SIGTERM/143 after reason=initialization_timeout is the manager's own watchdog
// reaping a Wrapper that never became ready. It is categorically not a native
// crash, and because authentication was never reached it gives no grounds to ask
// the user to re-enter a password or 2FA code.
test('an initialization timeout is explained as a watchdog cleanup, not a crash', async () => {
  const page = await renderWithExit({
    status: 'error',
    // The cause claim below is only made for a corroborated record, so this
    // payload carries every field the backend attaches when the watchdog fires.
    lastExit: watchdogExit(),
  });

  const text = page.elements['wrapper-status'].textContent;
  assert.match(text, /初始化/, 'the copy must say initialization');
  assert.match(text, /超时/, 'the copy must say it timed out');
  assert.match(text, /看护程序已清理/, 'the full watchdog tuple may identify the local terminator');
  assert.match(text, /这不是崩溃/, 'the full watchdog tuple may rule out a crash');
  assert.doesNotMatch(text, /SIGSEGV/, 'a watchdog SIGTERM must never be labelled SIGSEGV');
  // "崩溃" is only acceptable when explicitly negated ("这不是崩溃"); it must never
  // be asserted as what happened.
  assert.doesNotMatch(text, /(?<!这不是)崩溃/, 'a watchdog SIGTERM must not be presented as a crash');
  assert.doesNotMatch(text, /密码|验证码/, 'an initialization timeout must not ask for credentials again');
  assert.equal(text !== '异常', true, 'the generic error label is not an explanation');
  assert.equal(page.elements['twofa-section'].hidden, true, 'a timeout must not reopen two-factor entry');
});

// While the manager is retrying, the snapshot legitimately mixes the current
// attempt's status with the previous attempt's terminal lastExit. A static
// "starting" hides that a prior attempt already timed out.
test('a retry generation is shown as an attempt count rather than a static starting label', async () => {
  const page = await renderWithExit({
    status: 'starting',
    currentAttempt: 2,
    totalAttempts: 3,
    lastExit: { signal: 'SIGTERM', exitCode: 143, reason: 'initialization_timeout', attempt: 1 },
  });

  const text = page.elements['wrapper-status'].textContent;
  assert.match(text, /2/, 'the copy must name the current attempt');
  assert.notEqual(text, '启动中', 'a retry after a timeout must not read as a plain starting label');
  assert.doesNotMatch(text, /密码|验证码/, 'a retry must not ask for credentials');
});

// An initialization timeout with no observed startup marker and one that stalled
// at a named stage are different findings. Collapsing them into one message
// leaves the user, and anyone reading a support log, unable to tell whether the
// Wrapper ever started running.
test('an initialization timeout names the last startup stage that was actually reached', async () => {
  const stalled = await renderWithExit({
    status: 'error',
    lastExit: watchdogExit({
      lastStartupStage: 'login_fd_read_begin',
    }),
  });

  const stalledText = stalled.elements['wrapper-status'].textContent;
  assert.match(stalledText, /初始化/, 'the copy must still say initialization');
  assert.match(stalledText, /login_fd_read_begin/, 'the copy must name the observed stage');
  assert.doesNotMatch(stalledText, /SIGSEGV/, 'a watchdog SIGTERM must never be labelled SIGSEGV');
  assert.doesNotMatch(stalledText, /密码|验证码/, 'a stalled startup must not ask for credentials');

  // No marker at all is the more severe finding and must read differently.
  const silent = await renderWithExit({
    status: 'error',
    lastExit: watchdogExit(),
  });
  const silentText = silent.elements['wrapper-status'].textContent;
  assert.doesNotMatch(silentText, /login_fd_read_begin/, 'a stage must never be invented');
  assert.notEqual(
    silentText,
    stalledText,
    'a stall with no observed marker must not read identically to a located stall',
  );
});

// The stage token is attacker-influenced in the sense that it arrives over the
// bridge; the renderer must not paint an unrecognized value into the DOM.
test('an unrecognized startup stage is ignored rather than echoed', async () => {
  const page = await renderWithExit({
    status: 'error',
    lastExit: watchdogExit({
      lastStartupStage: '<img src=x onerror=alert(1)> password=hunter2',
    }),
  });

  const text = page.elements['wrapper-status'].textContent;
  assert.doesNotMatch(text, /hunter2/, 'an unlisted stage value must never be echoed');
  assert.doesNotMatch(text, /<img|onerror/, 'an unlisted stage value must never be echoed');
  assert.match(text, /初始化/, 'the copy must still explain the timeout');
});

// The watchdog copy makes two load-bearing promises: that this was a local
// cleanup rather than a crash, and that the user has nothing to re-enter.
// Asserting them from reason alone means a contradictory or older payload can
// make the UI vouch for something it cannot know.
test('the watchdog explanation is only asserted when the payload corroborates it', async () => {
  const corroborated = await renderWithExit({
    status: 'error',
    lastExit: watchdogExit({
      lastStartupStage: 'main_enter',
    }),
  });
  const corroboratedText = corroborated.elements['wrapper-status'].textContent;
  assert.match(corroboratedText, /看护程序/, 'a corroborated record may name the watchdog');
  assert.match(corroboratedText, /这不是崩溃/, 'a corroborated record may rule out a crash');
  assert.match(corroboratedText, /main_enter/, 'a corroborated record may name the stage');

  // A fault signal contradicts "not a crash". The UI must not vouch for either.
  const contradicted = await renderWithExit({
    status: 'error',
    lastExit: watchdogExit({
      signal: 'SIGSEGV',
      exitCode: 139,
    }),
  });
  const contradictedText = contradicted.elements['wrapper-status'].textContent;
  assert.doesNotMatch(contradictedText, /这不是崩溃/, 'a fault signal must not be described as "not a crash"');
  assert.doesNotMatch(contradictedText, /看护程序已清理/, 'a fault signal must not be attributed to the watchdog');
  assert.match(contradictedText, /初始化/, 'the copy must still say initialization');

  // An older backend sends no initiator. The timeout is still reportable, but
  // the cause is not attributable.
  const unattributed = await renderWithExit({
    status: 'error',
    lastExit: { signal: 'SIGTERM', exitCode: 143, reason: 'initialization_timeout', attempt: 3 },
  });
  const unattributedText = unattributed.elements['wrapper-status'].textContent;
  assert.doesNotMatch(unattributedText, /看护程序已清理/, 'an unattributed timeout must not name the watchdog as the killer');
  assert.match(unattributedText, /初始化/, 'the copy must still say initialization');
  assert.doesNotMatch(unattributedText, /密码|验证码/, 'an unattributed timeout still must not ask for credentials');

  // A spoofed initiator token must not unlock the strong copy.
  const spoofed = await renderWithExit({
    status: 'error',
    lastExit: watchdogExit({
      terminationInitiator: 'not_our_watchdog',
    }),
  });
  assert.doesNotMatch(
    spoofed.elements['wrapper-status'].textContent,
    /看护程序已清理/,
    'an unrecognized initiator token must not unlock the watchdog claim',
  );
});

// Reported bypasses of the renderer's watchdog check: a payload with no
// runGeneration, one whose attempt disagrees with currentAttempt, and one
// carrying a signal the stop path never sends. Each previously unlocked the copy
// that tells the user this was a local cleanup and that nothing needs re-entering.
test('the renderer rejects watchdog payloads that cannot be tied to a run or attempt', async () => {
  const corroborated = watchdogExit({
    lastStartupStage: 'main_enter',
  });

  const rejected = {
    missingRunGeneration: { ...corroborated, runGeneration: undefined },
    zeroRunGeneration: { ...corroborated, runGeneration: 0 },
    mismatchedAttempt: { ...corroborated, attempt: 2 },
    arbitrarySignal: { ...corroborated, signal: 'SIGUSR1', exitCode: 138 },
    arbitrarySignalAndCode: { ...corroborated, signal: 'SIGUSR1', exitCode: 999 },
    unknownSignalToken: { ...corroborated, signal: 'NOT_A_SIGNAL' },
    signalCodeDisagreement: { ...corroborated, signal: 'SIGKILL', exitCode: 143 },
    signalWithoutCode: { ...corroborated, exitCode: undefined },
    outOfRangeCodeWithoutSignal: { ...corroborated, signal: '', exitCode: 999 },
    missingPhase: { ...corroborated, phase: undefined },
    onlinePhase: { ...corroborated, phase: 'online' },
    unknownPhase: { ...corroborated, phase: 'future_phase' },
    missingPortReady: { ...corroborated, portReady: undefined },
    readyPort: { ...corroborated, portReady: true },
  };

  for (const [name, lastExit] of Object.entries(rejected)) {
    const page = await renderWithExit({ status: 'error', lastExit });
    const text = page.elements['wrapper-status'].textContent;
    assert.doesNotMatch(text, /看护程序已清理/, `${name} must not be attributed to the watchdog`);
    assert.doesNotMatch(text, /这不是崩溃/, `${name} must not be declared "not a crash"`);
    // An unverifiable record must not present a stall location as established.
    assert.doesNotMatch(text, /main_enter/, `${name} must not present its stage as evidence`);
    assert.match(text, /初始化超时/, `${name} must still report the timeout`);
    assert.doesNotMatch(text, /密码|验证码/, `${name} must still not ask for credentials`);
  }

  // The same record, fully corroborated, still unlocks the stronger copy.
  const accepted = await renderWithExit({ status: 'error', lastExit: corroborated });
  const acceptedText = accepted.elements['wrapper-status'].textContent;
  assert.match(acceptedText, /看护程序已清理/);
  assert.match(acceptedText, /main_enter/);

  // An escalated SIGKILL is legitimate when its code agrees.
  const escalated = await renderWithExit({
    status: 'error',
    lastExit: { ...corroborated, signal: 'SIGKILL', exitCode: 137 },
  });
  assert.match(escalated.elements['wrapper-status'].textContent, /看护程序已清理/);

  const authenticatedInitializing = await renderWithExit({
    status: 'error',
    lastExit: { ...corroborated, phase: 'authenticated_initializing' },
  });
  assert.match(authenticatedInitializing.elements['wrapper-status'].textContent, /看护程序已清理/);
});

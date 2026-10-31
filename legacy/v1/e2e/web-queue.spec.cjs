'use strict';

const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { once } = require('node:events');
const { test, expect } = require('@playwright/test');
const { WslManager } = require('../electron/wsl-manager');

const rendererDirectory = path.join(__dirname, '..', 'src', 'renderer');

const defaultDownloadSettings = {
  downloadRegion: '', metadataRegion: '', lyricsRegion: '', language: '',
  saveLyrics: true, embedLyrics: true, lyricsType: 'lyrics', lyricsFormat: 'lrc',
  embedCover: true, coverFormat: 'jpg', coverSize: '5000x5000',
  saveArtistCover: false, saveAnimatedArtwork: false, useSongInfoForPlaylist: false,
  alacMax: 192000, mvMax: 2160,
  songFileFormat: '{SongNumer}. {SongName}',
};

function response(route, payload, status = 200) {
  return route.fulfill({
    status,
    contentType: 'application/json; charset=utf-8',
    body: JSON.stringify(payload),
  });
}

function createBackend(overrides = {}) {
  return {
    capabilities: {
      wrapper: { status: 'online', loginCompleted: true },
      queue: { state: 'ready' },
      jobExecutorConfigured: true,
      qualities: Object.fromEntries(['aac-lc', 'alac', 'aac', 'atmos'].map((quality) => [quality, { available: true }])),
    },
    jobs: [],
    files: [],
    downloadConcurrency: 3,
    downloadSettings: { ...defaultDownloadSettings },
    calls: [],
    apiToken: '',
    sseEvents: [],
    jobCreateResponse: null,
    ...overrides,
  };
}

async function installMockAPI(page, backend) {
  if (backend.apiToken) {
    await page.addInitScript((accessKey) => {
      window.prompt = () => accessKey;
    }, backend.apiToken);
  }
  await page.addInitScript(() => {
    const sources = [];
    class MockEventSource {
      constructor(url) {
        this.url = url;
        this.listeners = new Map();
        this.closed = false;
        sources.push(this);
      }

      addEventListener(type, listener) {
        this.listeners.set(type, listener);
      }

      close() {
        this.closed = true;
      }

      emit(type, payload, lastEventId = '1') {
        const listener = this.listeners.get(type);
        if (listener) listener({ data: JSON.stringify(payload), lastEventId });
      }
    }

    window.EventSource = MockEventSource;
    window.__amdlMockSSE = {
      emit(index, type, payload, lastEventId) { sources[index].emit(type, payload, lastEventId); },
      count() { return sources.length; },
    };
  });

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request();
    const method = request.method();
    const requestURL = new URL(request.url());
    const pathname = requestURL.pathname;
    const body = request.postData() || '';
    const authorized = !backend.apiToken || request.headers().authorization === `Bearer ${backend.apiToken}`;
    backend.calls.push({ method, pathname, search: requestURL.search, body, authorized });

    if (!authorized) {
      return response(route, { error: { code: 'api_auth_required', message: 'authentication required' } }, 401);
    }

    if (method === 'GET' && pathname === '/api/v1/capabilities') return response(route, backend.capabilities);
	if (method === 'GET' && pathname === '/api/v1/catalog/session') return response(route, backend.catalogSession || { schemaVersion:1, state:'public', storefront:'us', configuredStorefront:'us', userTokenConfigured:false });
	if (method === 'GET' && pathname.startsWith('/api/v1/catalog/')) {
		if (backend.catalogFailure) return response(route, {error:{code:'catalog_unavailable',message:'SENTINEL_PRIVATE_BODY'}},502);
		if (pathname.endsWith('/detail')) return response(route, backend.catalogDetail || {schemaVersion:1,storefront:'us',groups:[],resource:backend.catalogAlbum,tracks:[],nextOffset:null});
		return response(route, {schemaVersion:1,storefront:requestURL.searchParams.get('region'),groups:backend.catalogGroups || [],nextOffset:null});
	}
    if (method === 'POST' && pathname === '/api/v1/browser-download/resolve') {
      if (backend.regionFailure) return response(route, { error: { code: 'browser_region_unavailable', message: 'SENTINEL_RAW_ERROR' } }, 422);
      if (backend.regionReceipt) return response(route, backend.regionReceipt);
      const original = JSON.parse(body).url;
      const resolved = backend.regionURL || original;
      return response(route, { schemaVersion: 1, url: resolved, sourceStorefront: new URL(original).pathname.split('/')[1], targetStorefront: new URL(resolved).pathname.split('/')[1], converted: resolved !== original });
    }
    if (pathname === '/api/v1/settings/download') {
      if (backend.preferencesUnsupported) return response(route, {}, 404);
      if (method === 'PUT') {
        if (backend.preferenceReceiptIncomplete) return response(route, { settings: {} });
        backend.downloadSettings = JSON.parse(body);
      }
      if (method === 'DELETE') backend.downloadSettings = { ...defaultDownloadSettings };
      return response(route, { settings: backend.downloadSettings, defaults: defaultDownloadSettings, overridden: method === 'PUT', appliesTo: 'next_job' });
    }
    if (pathname === '/api/v1/settings/download-concurrency') {
      if (method === 'GET') {
        return response(route, {
          downloadConcurrency: backend.downloadConcurrency,
          defaultDownloadConcurrency: 3,
          minDownloadConcurrency: 1,
          maxDownloadConcurrency: 8,
          overridden: backend.downloadConcurrency !== 3,
        });
      }
      if (method === 'PUT') {
        backend.downloadConcurrency = JSON.parse(body).downloadConcurrency;
        return response(route, {
          downloadConcurrency: backend.downloadConcurrency,
          defaultDownloadConcurrency: 3,
          minDownloadConcurrency: 1,
          maxDownloadConcurrency: 8,
          overridden: true,
        });
      }
      if (method === 'DELETE') {
        backend.downloadConcurrency = 3;
        return response(route, {
          downloadConcurrency: 3,
          defaultDownloadConcurrency: 3,
          minDownloadConcurrency: 1,
          maxDownloadConcurrency: 8,
          overridden: false,
        });
      }
    }
    if (method === 'GET' && pathname === '/api/v1/jobs') {
      const explicitLimit = requestURL.searchParams.get('limit');
      const limit = explicitLimit ? Number(explicitLimit) : 50;
      const ordered = explicitLimit ? backend.jobs : [...backend.jobs].reverse();
      return response(route, { jobs: ordered.slice(0, limit) });
    }
    if (method === 'GET' && pathname === '/api/v1/files') return response(route, { files: backend.files });

    if (method === 'POST' && pathname === '/api/v1/jobs') {
      if (backend.jobCreateResponse) return response(route, backend.jobCreateResponse, 202);
      const requestBody = JSON.parse(body);
      const created = requestBody.urls.map((url, index) => ({
        id: `job-${backend.jobs.length + index + 1}`,
        url,
        quality: requestBody.quality,
        existingPolicy: requestBody.existingPolicy,
        status: 'queued',
        total: 0,
        succeeded: 0,
        failed: 0,
        skipped: 0,
        unavailable: 0,
      }));
      backend.jobs.push(...created);
      return response(route, { jobs: created }, 202);
    }

    if (method === 'POST' && pathname === '/api/v1/wrapper/2fa') {
      backend.capabilities = {
        ...backend.capabilities,
        wrapper: { status: 'online' },
      };
      return response(route, { ok: true });
    }

    const eventsMatch = pathname.match(/^\/api\/v1\/jobs\/([^/]+)\/events$/);
    if (method === 'GET' && eventsMatch) {
      const event = backend.sseEvents.shift();
      const bodyText = event ? [
        `id: ${event.id || '1'}`,
        `event: ${event.type || 'message'}`,
        `data: ${JSON.stringify(event.data)}`,
        '',
        '',
      ].join('\n') : ': keepalive\n\n';
      return route.fulfill({ status: 200, contentType: 'text/event-stream; charset=utf-8', body: bodyText });
    }

    const jobMatch = pathname.match(/^\/api\/v1\/jobs\/([^/]+)(?:\/(cancel|retry|files))?$/);
    if (jobMatch) {
      const [, id, action] = jobMatch;
      const job = backend.jobs.find((candidate) => candidate.id === id);
      if (!job) return response(route, { error: { code: 'not_found', message: 'job not found' } }, 404);
      if (method === 'GET' && !action) return response(route, job);
      if (method === 'DELETE' && !action) {
        backend.jobs = backend.jobs.filter((candidate) => candidate.id !== id);
        backend.files = backend.files.filter((file) => file.jobId !== id);
        return route.fulfill({ status: 204 });
      }
      if (method === 'GET' && action === 'files') return response(route, { files: backend.files.filter((file) => file.jobId === id) });
      if (method === 'POST' && action === 'cancel') {
        job.status = 'cancelled';
        return response(route, { job });
      }
      if (method === 'POST' && action === 'retry') {
        job.status = 'running';
        return response(route, { job });
      }
    }

    return response(route, { error: { code: 'unexpected', message: `${method} ${pathname}` } }, 500);
  });
}

async function startWebUI() {
  const server = http.createServer((request, reply) => {
    const pathname = new URL(request.url, 'http://127.0.0.1').pathname;
    const filename = pathname === '/' ? 'index.html' : pathname.slice(1);
    if (!['index.html', 'job-data.js', 'renderer.js', 'browse.js', 'catalog.js', 'playback.js', 'styles.css', 'client.css', 'client-navigation.js'].includes(filename)) {
      reply.writeHead(404).end();
      return;
    }
    const extension = path.extname(filename);
    const contentType = extension === '.js' ? 'application/javascript' : extension === '.css' ? 'text/css' : 'text/html';
    reply.writeHead(200, { 'content-type': `${contentType}; charset=utf-8` });
    reply.end(fs.readFileSync(path.join(rendererDirectory, filename)));
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const { port } = server.address();
  return {
    url: `http://127.0.0.1:${port}`,
    close: () => new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve()))),
  };
}

let webUI;

test('repeated Enter search responds and only displays the latest keyword', async ({page}) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.route('**/api/v1/catalog/search?*', async route => {
    const term = new URL(route.request().url()).searchParams.get('term');
    await response(route, {schemaVersion:1,storefront:'us',groups:[{type:'albums',items:[{id:'123',type:'albums',title:term,artist:'Fixture',url:'https://music.apple.com/us/album/123',downloadable:true}],nextOffset:null}],nextOffset:null});
  });
  await page.goto(webUI.url);
  await expect(page.locator('#catalog-account')).toContainText('公开目录模式');
  await page.locator('#catalog-search-input').fill('first');
  await page.getByRole('button',{name:'搜索目录',exact:true}).click();
  await expect(page.locator('#catalog-results')).toContainText('first');
  await page.locator('#catalog-search-input').fill('second');
  await page.locator('#catalog-search-input').press('Enter');
  await expect(page.locator('#catalog-results')).toContainText('second');
  await page.locator('#catalog-search-input').fill('third');
  await page.locator('#catalog-search-input').press('Enter');
  await expect(page.locator('#catalog-results')).toContainText('third');
  await expect(page.locator('#catalog-results')).not.toContainText('second');
});

test('home recommendations and private playlists browse, paginate and queue original library identity',async({page},testInfo)=>{
  const playlist={id:'p.private',type:'library-playlists',title:'我的私有歌单',artist:'',url:'https://music.apple.com/us/library/playlist/p.private',downloadable:true};
  const album={id:'123',type:'albums',title:'为你推荐',artist:'Fixture',url:'https://music.apple.com/us/album/123',downloadable:true};
  const backend=createBackend({catalogSession:{schemaVersion:1,state:'verified',storefront:'us',configuredStorefront:'cn',accountStorefront:'us',userTokenConfigured:true}});
  await installMockAPI(page,backend);
  await page.route('**/api/v1/catalog/home?*',route=>response(route,{schemaVersion:1,storefront:'us',feed:'personal',groups:[{type:'albums',title:'Made for You',recommendationId:'rec-1',items:[album],nextOffset:25}],nextOffset:25}));
  await page.route('**/api/v1/catalog/recommendation?*',route=>response(route,{schemaVersion:1,storefront:'us',feed:'personal',groups:[{type:'albums',title:'More for You',recommendationId:'rec-1',items:[{...album,id:'124',url:'https://music.apple.com/us/album/124',title:'推荐下一页'}],nextOffset:null}],nextOffset:null}));
  await page.route('**/api/v1/catalog/library?*',route=>{
    const offset=new URL(route.request().url()).searchParams.get('offset');
    return response(route,{schemaVersion:1,storefront:'us',groups:[{type:'library-playlists',items:[offset==='25'?{...playlist,id:'p.second',title:'第二页歌单',url:'https://music.apple.com/us/library/playlist/p.second'}:playlist],nextOffset:null}],nextOffset:offset==='25'?null:25});
  });
  await page.route('**/api/v1/catalog/library-detail?*',route=>{
    const offset=new URL(route.request().url()).searchParams.get('offset');
    const track={id:'456',sourceId:'i.catalog',type:'songs',title:'目录曲目',artist:'Fixture',url:'https://music.apple.com/us/song/456',downloadable:true};
    return response(route,{schemaVersion:1,storefront:'us',groups:[],...(offset==='0'?{resource:playlist}:{}),tracks:offset==='0'?[track,{id:'',sourceId:'i.upload',type:'songs',title:'用户上传',artist:'Fixture',url:'',downloadable:false,unavailableReason:'catalog_resource_unavailable'}]:[{...track,id:'457',url:'https://music.apple.com/us/song/457',title:'下一页曲目'}],nextOffset:offset==='0'?100:null});
  });
  await page.goto(webUI.url);
  await expect(page.locator('#catalog-page-title')).toHaveText('主页');
  await expect(page.locator('#catalog-results')).toContainText('Made for You');
  await page.getByRole('button',{name:'查看更多专辑',exact:true}).click();
  await expect(page.locator('#catalog-results')).toContainText('推荐下一页');
  await expect(page.getByRole('button',{name:'加载更多推荐',exact:true})).toBeVisible();
  await expect(page.locator('#catalog-region')).toBeDisabled();
  await page.getByRole('button',{name:'我的歌单',exact:true}).click();
  await expect(page.getByRole('button',{name:'查看 我的私有歌单',exact:true})).toBeVisible();
  await page.getByRole('button',{name:'加载更多歌单',exact:true}).click();
  await expect(page.locator('#catalog-results')).toContainText('第二页歌单');
  await expect(page.getByRole('button',{name:'加载更多歌单',exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'查看 我的私有歌单',exact:true}).click();
  await expect(page.locator('.catalog-tracks li')).toHaveCount(2);
  await expect(page.locator('.catalog-tracks li').nth(1)).toContainText('没有可下载的 Apple Music 目录资源');
  await expect(page.locator('.catalog-tracks li').nth(1).getByRole('button',{name:'此地区不可用',exact:true})).toBeDisabled();
  await page.getByRole('button',{name:'加载更多曲目',exact:true}).click();
  await expect(page.locator('.catalog-tracks li')).toHaveCount(3);
  await page.getByRole('button',{name:'下载整个歌单',exact:true}).click();
  await expect(page.locator('#catalog-status')).toContainText('已加入下载队列');
  const post=backend.calls.find(c=>c.method==='POST'&&c.pathname==='/api/v1/jobs');
  expect(JSON.parse(post.body).urls).toEqual([playlist.url]);
  expect(backend.calls.some(c=>c.pathname==='/api/v1/browser-download/resolve')).toBe(false);
  await page.getByRole('button',{name:'下载整个歌单',exact:true}).click();
  expect(backend.calls.filter(c=>c.method==='POST'&&c.pathname==='/api/v1/jobs')).toHaveLength(1);
  for(const width of [1280,390]){await page.setViewportSize({width,height:820});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.screenshot({path:testInfo.outputPath('private-playlist-'+width+'.png')});}
});

test('native catalog bounds stalled requests and recovers on the next Enter',async({page})=>{
  const backend=createBackend();await installMockAPI(page,backend);
  await page.addInitScript(()=>{
    const original=window.setTimeout;
    window.setTimeout=(callback,delay,...args)=>{
      if(delay===15000) window.__fireCatalogTimeout=()=>callback(...args);
      return original(callback,delay,...args);
    };
  });
  let release;const barrier=new Promise(resolve=>release=resolve);let pending=false;
  await page.route('**/api/v1/catalog/search?*',async route=>{
    if(new URL(route.request().url()).searchParams.get('term')==='stalled'){pending=true;await barrier;}
    try{await response(route,{schemaVersion:1,storefront:'us',groups:[],nextOffset:null});}catch{/* Aborted request. */}
  });
  await page.goto(webUI.url);await expect(page.locator('#catalog-account')).toContainText('公开目录模式');
  await page.locator('#catalog-search-input').fill('stalled');await page.locator('#catalog-search-input').press('Enter');
  await expect.poll(()=>pending).toBe(true);await page.evaluate(()=>window.__fireCatalogTimeout());
  await expect(page.locator('#catalog-status')).toContainText('请求超时');
  await page.locator('#catalog-search-input').fill('retry');await page.locator('#catalog-search-input').press('Enter');
  await expect(page.locator('#catalog-status')).toBeEmpty();release();
  await expect(page.locator('#catalog-home')).toHaveAttribute('aria-pressed','false');
});

test('personal library response recovers the actual account region after unverified session',async({page})=>{
  const backend=createBackend({catalogSession:{schemaVersion:1,state:'unverified',storefront:'cn',configuredStorefront:'cn',userTokenConfigured:true}});
  await installMockAPI(page,backend);
  await page.route('**/api/v1/catalog/home?*',route=>response(route,{schemaVersion:1,storefront:'us',feed:'personal',groups:[],nextOffset:null}));
  await page.route('**/api/v1/catalog/library?*',route=>response(route,{schemaVersion:1,storefront:'us',feed:'personal',groups:[{type:'library-playlists',items:[{id:'p.fixture',type:'library-playlists',title:'账号地区歌单',artist:'',url:'https://music.apple.com/us/library/playlist/p.fixture',downloadable:true}],nextOffset:null}],nextOffset:null}));
  await page.goto(webUI.url);await expect(page.locator('#catalog-account')).toContainText('暂未验证');
  await page.getByRole('button',{name:'我的歌单',exact:true}).click();
  await expect(page.locator('#catalog-results')).toContainText('账号地区歌单');
  await expect(page.locator('#catalog-region')).toHaveValue('us');
  await expect(page.locator('#catalog-status')).toBeEmpty();
});

test('catalog token format failure has a fixed non-network message and recovers on Enter', async ({page}) => {
  const backend = createBackend(); await installMockAPI(page, backend);
  let fail = true;
  await page.route('**/api/v1/catalog/search?*', route => fail
    ? response(route, {error:{code:'catalog_token_format_changed', message:'SENTINEL_RAW_BODY'}},503)
    : response(route,{schemaVersion:1,storefront:'us',groups:[],nextOffset:null}));
  await page.goto(webUI.url);
  await page.locator('#catalog-search-input').fill('fixture');
  await page.locator('#catalog-search-input').press('Enter');
  await expect(page.locator('#catalog-status')).toContainText('未能从 Apple 公开网页识别');
  await expect(page.locator('#catalog-status')).not.toContainText('SENTINEL');
  await expect(page.locator('#catalog-status')).not.toContainText('检查网络');
  fail = false; await page.locator('#catalog-search-input').press('Enter');
  await expect(page.locator('#catalog-status')).toBeEmpty();
});

test('submitted search reports fixed timeout errors and retries on Enter without automatic search on edits',async({page})=>{
  const backend=createBackend();await installMockAPI(page,backend);
  let fail=true;let searches=0;
  await page.route('**/api/v1/catalog/search?*',route=>{
    searches++;
    return fail?response(route,{error:{code:'catalog_timeout',message:'SENTINEL_RAW_BODY'}},504):response(route,{schemaVersion:1,storefront:'us',groups:[],nextOffset:null});
  });
  await page.goto(webUI.url);await expect(page.locator('#catalog-account')).toContainText('公开目录模式');
  await page.locator('#catalog-search-input').fill('fixture');expect(searches).toBe(0);
  expect(await page.locator('#catalog-search-input').evaluate(input=>input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',isComposing:true,bubbles:true,cancelable:true})))).toBe(false);
  expect(searches).toBe(0);
  await page.locator('#catalog-search-input').press('Enter');await expect(page.locator('#catalog-status')).toContainText('请求超时');
  await expect(page.locator('#catalog-status')).not.toContainText('SENTINEL');
  fail=false;await page.locator('#catalog-search-input').press('Enter');await expect(page.locator('#catalog-status')).toBeEmpty();expect(searches).toBe(2);
  await page.route('**/api/v1/catalog/library?*',route=>response(route,{error:{code:'catalog_auth_required',message:'SENTINEL_USER'}},403));
  await page.getByRole('button',{name:'我的歌单',exact:true}).click();
  await expect(page.locator('#catalog-status')).toContainText('需要 Media-User-Token');await expect(page.locator('#catalog-status')).not.toContainText('SENTINEL');
});

test('native catalog defaults to the verified US account and queues its actual regional album ID', async ({page}) => {
  const album={id:'6791462937',type:'albums',title:'Genshin Impact',artist:'HOYO-MiX',artwork:'',url:'https://music.apple.com/us/album/6791462937',downloadable:true,trackCount:92};
  const backend=createBackend({catalogAlbum:album,catalogSession:{schemaVersion:1,state:'verified',storefront:'us',configuredStorefront:'cn',accountStorefront:'us',userTokenConfigured:true},catalogGroups:[{type:'albums',items:[album],nextOffset:null}]});
  await installMockAPI(page,backend); await page.goto(webUI.url);
  await expect(page.locator('#catalog-panel')).toBeVisible(); await expect(page.locator('#embedded-browser-viewport')).toBeHidden();
  await expect(page.locator('#catalog-account')).toContainText('账号 API 已验证 · US');
  await page.locator('#catalog-search-input').fill('HOYO-MiX'); await page.getByRole('button',{name:'搜索目录',exact:true}).click();
  await page.locator('#catalog-results').getByRole('button',{name:'查看 Genshin Impact',exact:true}).click();
  await expect(page.locator('#catalog-detail')).toContainText('92 首');
  await expect(page.locator('#download-readiness')).toHaveClass(/ready/);
  await page.getByRole('button',{name:'下载整个专辑',exact:true}).click();
  await expect(page.locator('#catalog-status')).toContainText('已加入下载队列');
  expect(JSON.parse(backend.calls.find(c=>c.method==='POST'&&c.pathname==='/api/v1/jobs').body).urls).toEqual([album.url]);
  expect(backend.calls.some(c=>c.pathname==='/api/v1/browser-download/resolve')).toBe(false);
  expect(backend.calls.find(c=>c.pathname==='/api/v1/catalog/search').search).toContain('region=us');
});

test('native catalog keeps invalid token and upstream errors distinct without leaking response text', async ({page}) => {
  const backend=createBackend({catalogSession:{schemaVersion:1,state:'invalid',storefront:'us',configuredStorefront:'us',userTokenConfigured:true},catalogFailure:true});
  await installMockAPI(page,backend);await page.goto(webUI.url);
  await expect(page.locator('#catalog-account')).toContainText('Token 无效');
  await expect(page.locator('#catalog-status')).toContainText('目录暂时不可用');
  await expect(page.locator('#catalog-status')).not.toContainText('SENTINEL');
  expect(backend.calls.some(c=>c.method==='POST')).toBe(false);
});

test('native catalog paginates tracks, retries failures and never treats API verification as playback login', async ({page},testInfo) => {
  const album={id:'123',type:'albums',title:'Fixture album',artist:'Artist',url:'https://music.apple.com/us/album/123',downloadable:true,trackCount:102};
  const track=(id,downloadable=true)=>({id,type:'songs',title:'Track '+id,artist:'Artist',url:'https://music.apple.com/us/song/'+id,downloadable});
  const backend=createBackend({catalogGroups:[{type:'albums',items:[album]}],catalogDetail:{schemaVersion:1,storefront:'us',groups:[],resource:album,tracks:[track('456'),track('457',false)],nextOffset:100},
    catalogSession:{schemaVersion:1,state:'verified',storefront:'us',configuredStorefront:'cn',accountStorefront:'us',userTokenConfigured:true},jobCreateResponse:{jobs:[]}});
  backend.capabilities.wrapper.loginCompleted=false;
  await installMockAPI(page,backend);
  let failPage=true;
  await page.route('**/api/v1/catalog/detail?*',async route=>{
    if(new URL(route.request().url()).searchParams.get('offset')!=='100')return route.fallback();
    if(failPage)return response(route,{error:{message:'SENTINEL'}},502);
    return response(route,{schemaVersion:1,storefront:'us',groups:[],tracks:[track('458')],nextOffset:null});
  });
  await page.goto(webUI.url);
  await expect(page.locator('#catalog-account')).toContainText('API 已验证');
  await expect(page.locator('#download-readiness')).toHaveClass(/unverified/);
  await page.getByRole('button',{name:'查看 Fixture album',exact:true}).click();
  await expect(page.locator('.catalog-tracks li')).toHaveCount(2);
  await expect(page.locator('.catalog-tracks').getByRole('button',{name:'此地区不可用'})).toBeDisabled();
  await page.getByRole('button',{name:'加载更多曲目',exact:true}).click();
  await expect(page.locator('#catalog-status')).toContainText('暂时不可用');
  await expect(page.getByRole('button',{name:'加载更多曲目',exact:true})).toBeEnabled();
  failPage=false;
  await page.getByRole('button',{name:'加载更多曲目',exact:true}).click();
  await expect(page.locator('.catalog-tracks li')).toHaveCount(3);
  await page.getByRole('button',{name:'下载整个专辑',exact:true}).click();
  await expect(page.locator('#catalog-status')).toContainText('队列回执不完整');
  expect(JSON.parse(backend.calls.find(c=>c.method==='POST'&&c.pathname==='/api/v1/jobs').body).urls).toEqual([album.url]);
  for(const width of [1280,390]){
    await page.setViewportSize({width,height:820});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:testInfo.outputPath('native-detail-'+width+'.png')});
  }
});

for (const outcome of ['full','interaction','failure','throw','stop-failed','invalid-success','busy','superseded']) {
  test('unified playback uses one button and handles '+outcome,async({page})=>{
    const song={id:'456',type:'songs',title:'Unified song',artist:'Fixture',url:'https://music.apple.com/us/song/456',downloadable:true,previewUrl:'https://audio-ssl.mzstatic.com/fixture.m4a'};
    const backend=createBackend({catalogGroups:[{type:'songs',items:[song]}]});
    await installMockAPI(page,backend);
    await page.addInitScript(outcome=>{
      window.playFixture={full:0,stop:0,audio:0};
      window.desktopShell={
        playCatalogTrack:async()=>{window.playFixture.full++;if(outcome==='throw')throw Error('private-sentinel');return outcome==='full'?{ok:true,playing:true}:outcome==='interaction'?{ok:true,playing:false,needsInteraction:true}:outcome==='invalid-success'?{ok:false,playing:true}:{ok:false,code:outcome==='busy'?'playback_busy':outcome==='superseded'?'playback_superseded':'drm_not_ready'};},
        stopCatalogPlayback:async()=>{window.playFixture.stop++;return {ok:outcome!=='stop-failed'};}
      };
      const create=document.createElement.bind(document);
      document.createElement=(tag,...args)=>{
        const el=create(tag,...args);
        if(tag==='audio'){
          window.playFixture.audio++;let paused=true;
          Object.defineProperty(el,'paused',{get:()=>paused});
          el.play=async()=>{paused=false;el.dispatchEvent(new Event('playing'));};
          el.pause=()=>{paused=true;el.dispatchEvent(new Event('pause'));};el.load=()=>{};
        }return el;
      };
    },outcome);
    await page.goto(webUI.url);
    const card=page.locator('.catalog-song-card');
    await expect(card.locator('button')).toHaveCount(3); // detail, download, one play
    await card.getByRole('button',{name:'播放 Unified song',exact:true}).click();
    await expect.poll(()=>page.evaluate(()=>window.playFixture.full)).toBe(1);
    if(outcome==='stop-failed'){
      await expect(page.locator('#catalog-status')).toContainText('未能停止');
      expect(await page.evaluate(()=>window.playFixture.audio)).toBe(0);
    }else if(['busy','superseded'].includes(outcome)){
      await expect(page.locator('#playback-status')).toHaveText('播放请求已取消，请重试。');
      expect(await page.evaluate(()=>window.playFixture.audio)).toBe(0);
      expect(await page.evaluate(()=>window.playFixture.stop)).toBe(0);
    }else{
      await expect(page.locator('#playback-status')).toHaveText(outcome==='full'?'正在播放 · Apple Music':'正在试听 · Apple Music');
      expect(await page.evaluate(()=>window.playFixture.audio)).toBe(outcome==='full'?0:1);
      await card.getByRole('button',{name:'暂停 Unified song',exact:true}).click();
      await expect(page.locator('#playback-status')).toHaveText('已暂停');
    }
    await expect(page.locator('body')).not.toContainText('private-sentinel');
    expect(backend.jobs).toHaveLength(0);
    await page.setViewportSize({width:390,height:820});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  });
}

test('unified playback without a preview still attempts full and reports unavailable accurately',async({page})=>{
  const song={id:'456',type:'songs',title:'No preview',artist:'Fixture',url:'https://music.apple.com/us/song/456',downloadable:true};
  const backend=createBackend({catalogGroups:[{type:'songs',items:[song]}]});await installMockAPI(page,backend);
  await page.addInitScript(()=>{window.fullAvailable=true;window.desktopShell={playCatalogTrack:async()=>({ok:window.fullAvailable,playing:window.fullAvailable}),stopCatalogPlayback:async()=>({ok:true})};});
  await page.goto(webUI.url);
  await page.getByRole('button',{name:'播放 No preview',exact:true}).click();
  await expect(page.locator('#playback-status')).toHaveText('正在播放 · Apple Music');
  await page.getByRole('button',{name:'停止播放',exact:true}).click();
  await page.evaluate(()=>window.fullAvailable=false);
  await page.getByRole('button',{name:'播放 No preview',exact:true}).click();
  await expect(page.locator('#catalog-status')).toContainText('也没有可用试听片段');
  expect(await page.locator('#playback-media audio').count()).toBe(0);
  await expect(page.getByRole('button',{name:'播放 No preview',exact:true})).toBeEnabled();
});

test('unified playback ignores a full receipt after leaving browse and blocks duplicate attempts',async({page})=>{
  const song={id:'456',type:'songs',title:'Pending song',artist:'Fixture',url:'https://music.apple.com/us/song/456',downloadable:true,previewUrl:'https://audio-ssl.mzstatic.com/fixture.m4a'};
  const backend=createBackend({catalogGroups:[{type:'songs',items:[song]}]});await installMockAPI(page,backend);
  await page.addInitScript(()=>{
    window.playFixture={full:0,stop:0};
    window.desktopShell={playCatalogTrack:()=>{window.playFixture.full++;return new Promise(resolve=>window.resolveFull=resolve);},stopCatalogPlayback:async()=>{window.playFixture.stop++;return {ok:true};}};
  });
  await page.goto(webUI.url);
  const play=page.getByRole('button',{name:'播放 Pending song',exact:true});
  await play.click();await expect(play).toBeDisabled();
  expect(await page.evaluate(()=>window.playFixture.full)).toBe(1);
  await page.getByRole('link',{name:'设置与状态',exact:true}).click();
  await page.evaluate(()=>window.resolveFull({ok:false}));
  await expect(page.locator('#playback-player')).toBeHidden();
  expect(await page.locator('#playback-media audio').count()).toBe(0);
  await expect.poll(()=>page.evaluate(()=>window.playFixture.stop)).toBeGreaterThan(0);
});

test('catalog public preview plays, pauses, replaces and cleans up without queueing',async({page},testInfo)=>{
  const album={id:'123',type:'albums',title:'Preview album',artist:'Artist',url:'https://music.apple.com/us/album/123',downloadable:true};
  const track=(id,previewUrl)=>({id,type:'songs',title:'Preview '+id,artist:'Artist',url:'https://music.apple.com/us/song/'+id,downloadable:false,previewUrl});
  const backend=createBackend({catalogGroups:[{type:'albums',items:[album]}],catalogDetail:{schemaVersion:1,storefront:'us',groups:[],resource:album,tracks:[track('456','https://audio-ssl.mzstatic.com/456.m4a'),track('457','https://audio-ssl.itunes.apple.com/457.m4a'),track('458','https://evil.invalid/private')],nextOffset:null}});
  await installMockAPI(page,backend);
  await page.addInitScript(()=>{
    window.previewAudios=[];
    const originalCreate=document.createElement.bind(document);
    document.createElement=function(tag,...args){
      const el=originalCreate(tag,...args);
      if(tag==='audio'){
        window.previewAudios.push(el);let paused=true;
        Object.defineProperty(el,'paused',{get:()=>paused});
        el.play=()=>{paused=false;el.dispatchEvent(new Event('playing'));return window.previewAudios.length===1 ? new Promise((resolve,reject)=>{window.rejectOldPreview=reject;}) : Promise.resolve();};
        el.pause=()=>{paused=true;el.dispatchEvent(new Event('pause'));};el.load=()=>{};
      }return el;
    };
  });
  await page.goto(webUI.url);await page.getByRole('button',{name:'查看 Preview album',exact:true}).click();
  await expect(page.getByRole('button',{name:'播放 Preview 458',exact:true})).toBeDisabled();
  await expect(page.locator('.catalog-track-heading')).not.toContainText('试听');
  await expect(page.locator('.catalog-tracks li').first().locator('button')).toHaveCount(2);
  await page.getByRole('button',{name:'播放 Preview 456',exact:true}).click();
  await expect(page.locator('#playback-status')).toHaveText('正在试听 · Apple Music');
  await expect(page.getByRole('button',{name:'暂停 Preview 456',exact:true})).toHaveAttribute('aria-pressed','true');
  await page.getByRole('button',{name:'暂停 Preview 456',exact:true}).click();
  await expect(page.locator('#playback-status')).toHaveText('已暂停');
  await page.evaluate(()=>window.rejectOldPreview(new DOMException('fixture pause','AbortError')));
  await expect(page.locator('#playback-status')).toHaveText('已暂停');
  await page.getByRole('button',{name:'播放 Preview 456',exact:true}).click();
  await page.getByRole('button',{name:'播放 Preview 457',exact:true}).click();
  expect(await page.evaluate(()=>({oldSrc:previewAudios[0].getAttribute('src'),cors:previewAudios[1].crossOrigin}))).toEqual({oldSrc:null,cors:'anonymous'});
  await page.evaluate(()=>previewAudios[0].dispatchEvent(new Event('error')));
  await page.evaluate(()=>window.rejectOldPreview(new Error('fixture stale play')));
  await expect(page.locator('#playback-title')).toHaveText('Preview 457 · Artist');
  await expect(page.locator('#playback-status')).toHaveText('正在试听 · Apple Music');
  await page.evaluate(()=>previewAudios[1].dispatchEvent(new Event('ended')));
  await expect(page.locator('#playback-status')).toHaveText('试听结束');
  await page.evaluate(()=>previewAudios[1].dispatchEvent(new Event('error')));
  await expect(page.locator('#playback-status')).toHaveText('试听暂时不可用，请稍后重试。');
  await page.setViewportSize({width:390,height:820});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('preview-390.png')});
  await page.getByRole('link',{name:'设置与状态',exact:true}).click();
  await expect(page.locator('#playback-player')).toBeHidden();
  expect(await page.evaluate(()=>previewAudios.every(a=>!a.getAttribute('src')))).toBe(true);
  expect(backend.calls.filter(c=>c.pathname==='/api/v1/jobs'&&c.method==='POST')).toHaveLength(0);
});

test('catalog preview streams only a generated public audio fixture with anonymous credentials',async({page})=>{
  const song={id:'456',type:'songs',title:'Generated preview',artist:'Fixture',url:'https://music.apple.com/us/song/456',downloadable:true,previewUrl:'https://audio-ssl.mzstatic.com/fixture.wav'};
  const backend=createBackend({catalogGroups:[{type:'songs',items:[song]}]});
  await installMockAPI(page,backend);
  let requested=false;
  await page.route('https://audio-ssl.mzstatic.com/fixture.wav',route=>{
    const headers=route.request().headers();
    expect(headers.authorization).toBeUndefined();expect(headers.cookie).toBeUndefined();expect(headers['media-user-token']).toBeUndefined();
    requested=true;
    // Locally generated one-second silence: never fetch Apple media.
    const wav=Buffer.alloc(44+16000);wav.write('RIFF');wav.writeUInt32LE(wav.length-8,4);wav.write('WAVEfmt ',8);wav.writeUInt32LE(16,16);wav.writeUInt16LE(1,20);wav.writeUInt16LE(1,22);wav.writeUInt32LE(8000,24);wav.writeUInt32LE(16000,28);wav.writeUInt16LE(2,32);wav.writeUInt16LE(16,34);wav.write('data',36);wav.writeUInt32LE(16000,40);
    return route.fulfill({status:200,contentType:'audio/wav',headers:{'Access-Control-Allow-Origin':'*'},body:wav});
  });
  await page.goto(webUI.url);
  expect(requested).toBe(false);
  await page.getByRole('button',{name:'播放 Generated preview',exact:true}).click();
  await expect(page.locator('#playback-status')).toHaveText('试听结束');
  expect(requested).toBe(true);
  await page.getByRole('button',{name:'停止播放',exact:true}).click();
  await expect(page.locator('#playback-player')).toBeHidden();
  expect(backend.jobs).toHaveLength(0);
});

test('native catalog rejects stale search results and closes website fallback when returning',async({page})=>{
  const item=(id,title)=>({id,type:'albums',title,artist:'Fixture',url:'https://music.apple.com/us/album/'+id,downloadable:true});
  const backend=createBackend();
  await installMockAPI(page,backend);
  await page.addInitScript(()=>{window.__opens=0;window.__closes=0;window.desktopShell={openAppleMusic:async()=>{window.__opens++;return{ok:true};},closeAppleMusic:async()=>{window.__closes++;return{ok:true};},setAppleMusicBounds:async()=>({ok:true})};});
  let release;const barrier=new Promise(resolve=>release=resolve);let oldPending=false;
  await page.route('**/api/v1/catalog/search?*',async route=>{
    const old=new URL(route.request().url()).searchParams.get('term')==='old';
    if(old){oldPending=true;await barrier;}
    try{await response(route,{schemaVersion:1,storefront:'us',groups:[{type:'albums',items:[item(old?'111':'222',old?'OLD':'CURRENT')]}],nextOffset:null});}catch{/* An aborted old request is expected. */}
  });
  await page.goto(webUI.url);
  await expect(page.locator('#catalog-account')).toContainText('公开目录模式');
  expect(await page.evaluate(()=>window.__opens)).toBe(0);
  expect(backend.calls.filter(c=>c.pathname==='/api/v1/catalog/session')).toHaveLength(1);
  await page.locator('#catalog-search-input').fill('old');await page.locator('#catalog-search-input').press('Enter');
  await expect.poll(()=>oldPending).toBe(true);
  await page.locator('#catalog-search-input').fill('new');await page.locator('#catalog-search-input').press('Enter');
  await expect(page.locator('#catalog-results')).toContainText('CURRENT');
  release();
  await page.getByRole('button',{name:'打开官方网页',exact:true}).click();
  await expect.poll(()=>page.evaluate(()=>window.__opens)).toBe(1);
  await page.getByRole('button',{name:'返回原生目录',exact:true}).click();
  await expect(page.locator('#catalog-results')).toContainText('CURRENT');
  await expect(page.locator('#catalog-results')).not.toContainText('OLD');
  await expect.poll(()=>page.evaluate(()=>window.__closes)).toBeGreaterThan(0);
});

test('native catalog search pagination retires the consumed page control',async({page})=>{
  const item=(id)=>({id,type:'albums',title:'Album '+id,artist:'Fixture',url:'https://music.apple.com/us/album/'+id,downloadable:true});
  const backend=createBackend();await installMockAPI(page,backend);
  let pages=0;
  await page.route('**/api/v1/catalog/search?*',async route=>{
    const offset=new URL(route.request().url()).searchParams.get('offset');
    if(offset==='25')pages++;
    await response(route,{schemaVersion:1,storefront:'us',groups:[{type:'albums',items:[item(offset==='25'?'222':'111')],nextOffset:offset==='25'?null:25}],nextOffset:null});
  });
  await page.goto(webUI.url);await expect(page.locator('#catalog-account')).toContainText('公开目录模式');
  await page.locator('#catalog-search-input').fill('fixture');await page.getByRole('button',{name:'搜索目录',exact:true}).click();
  await page.getByRole('button',{name:'查看更多专辑',exact:true}).click();
  await expect(page.locator('.catalog-card')).toHaveCount(2);
  await expect(page.getByRole('button',{name:'查看更多专辑',exact:true})).toHaveCount(0);
  expect(pages).toBe(1);
});

for (const colorScheme of ['light','dark']) {
  test('Windows-style native catalog retains functional album hierarchy ('+colorScheme+')',async({page},testInfo)=>{
    await page.emulateMedia({colorScheme});
    const cover='https://is1-ssl.mzstatic.com/fixture/cover.jpg';
    await page.route('https://is1-ssl.mzstatic.com/fixture/cover.jpg',route=>route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="500" height="500"><defs><linearGradient id="a" x2="1" y2="1"><stop stop-color="#9bb6b9"/><stop offset="1" stop-color="#1b4155"/></linearGradient></defs><rect width="500" height="500" fill="url(#a)"/><circle cx="250" cy="242" r="115" fill="none" stroke="#c9dfe2" stroke-width="2"/><path d="M90 350 245 130 405 350Z" fill="none" stroke="#d2dfca" stroke-width="2"/><text x="250" y="435" text-anchor="middle" fill="#e9eeee" font-family="sans-serif" font-size="25">CATALOG FIXTURE</text></svg>'}));
    const album={id:'123',type:'albums',title:'原神 · 珍珠之歌 — The Shimmering Voyage',artist:'HOYO-MiX',artwork:cover,url:'https://music.apple.com/us/album/123',downloadable:true,trackCount:92};
    const tracks=['白首苍茫来','降入深冥','Night Voyage','失色的流岚'].map((title,index)=>({id:String(456+index),type:'songs',title,artist:'HOYO-MiX',artwork:cover,url:'https://music.apple.com/us/song/'+(456+index),downloadable:index!==3,duration:193000,trackNumber:index+1,discNumber:1}));
    const backend=createBackend({catalogGroups:[{type:'albums',items:[album]},{type:'songs',items:tracks}],catalogDetail:{schemaVersion:1,storefront:'us',groups:[],resource:album,tracks,nextOffset:null}});
    await installMockAPI(page,backend);await page.setViewportSize({width:1280,height:850});await page.goto(webUI.url);
    await expect(page.locator('#catalog-toolbar')).toBeVisible();
    await expect(page.locator('#catalog-back')).toBeDisabled();
    await expect(page.locator('.catalog-card')).toHaveCount(5);
    await page.screenshot({path:testInfo.outputPath('native-home-'+colorScheme+'.png')});
    await page.getByRole('button',{name:'查看 '+album.title,exact:true}).click();
    await expect(page.locator('#catalog-panel')).toHaveAttribute('data-page','detail');
    await expect(page.locator('#catalog-search-form')).toBeHidden();
    await expect(page.locator('.catalog-track-heading')).toContainText('歌曲');
    await expect(page.locator('.catalog-track-heading')).toContainText('艺人');
    await expect(page.locator('#catalog-status')).toBeEmpty();
    await expect(page.locator('#catalog-panel')).not.toContainText('下载使用设置中的默认音质与文件策略');
    await expect(page.locator('.catalog-track .catalog-download svg')).toHaveCount(4);
    await expect(page.locator('.catalog-tracks').getByRole('button',{name:'此地区不可用',exact:true})).toBeDisabled();
    const desktopCover=await page.locator('.catalog-detail-header > .catalog-artwork').boundingBox();
    expect(desktopCover.width).toBeGreaterThanOrEqual(240);
    expect((await page.locator('.catalog-track').first().boundingBox()).height).toBeLessThanOrEqual(68);
    for(const width of [1280,390]){
      await page.setViewportSize({width,height:850});
      await page.locator('#catalog-panel').evaluate(element=>element.scrollTop=0);
      expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
      expect(await page.locator('#catalog-panel').evaluate(element=>element.scrollWidth<=element.clientWidth)).toBe(true);
      await expect(page.getByRole('button',{name:'下载整个专辑',exact:true})).toBeVisible();
      await expect(page.getByRole('button',{name:'打开官方网页',exact:true})).toBeVisible();
      await page.screenshot({path:testInfo.outputPath('native-album-'+colorScheme+'-'+width+'.png')});
    }
    await page.getByRole('button',{name:'下载整个专辑',exact:true}).click();
    await expect(page.locator('#catalog-status')).toContainText('已加入下载队列');
    expect(JSON.parse(backend.calls.find(c=>c.method==='POST'&&c.pathname==='/api/v1/jobs').body).urls).toEqual([album.url]);
    await page.locator('#catalog-back').click();
    await expect(page.locator('#catalog-search-form')).toBeVisible();
    await expect(page.locator('#catalog-back')).toBeDisabled();
  });
}

test('settings hierarchy groups preferences, account and diagnostics with ordinary concurrency control', async ({ page }, testInfo) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.addInitScript(() => {
    window.desktopShell = { openCredentials: async () => ({ ok: true }) };
  });
  await page.goto(`${webUI.url}/#settings`);
  await expect(page.locator('.settings-card > h2')).toHaveText(['下载偏好', '本机与账户', '服务与诊断']);
  await expect(page.locator('#download-settings-title')).toHaveText('下载偏好');
  await expect(page.locator('label[for="download-concurrency"]')).toBeVisible();
  await expect(page.locator('#download-preferences-card h3')).toHaveText(['任务与默认选项', '地区与歌词', '封面与质量', '跨区元数据匹配', '文件名']);
  await expect(page.locator('#service-settings-card #wrapper-status')).toBeVisible();
  await expect(page.locator('#service-settings-card #log-container')).toBeHidden();
  await page.locator('#service-settings-card summary').click();
  await expect(page.locator('#service-settings-card #log-container')).toBeVisible();
  await page.locator('#download-concurrency').fill('4');
  await page.locator('#download-concurrency').press('Tab');
  await expect(page.locator('#download-concurrency-save')).toBeFocused();
  await page.locator('#download-concurrency-save').click();
  await expect(page.locator('#download-concurrency-status')).toContainText('已使用自定义并行数');
  expect(backend.downloadConcurrency).toBe(4);
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme });
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      const sizes = await page.locator('#download-preferences-card').evaluate(card => ({
        title: parseFloat(getComputedStyle(card.querySelector('h2')).fontSize),
        group: parseFloat(getComputedStyle(card.querySelector('h3')).fontSize),
        label: parseFloat(getComputedStyle(card.querySelector('label[for="download-concurrency"]')).fontSize),
      }));
      expect(sizes.title).toBeGreaterThan(sizes.group);
      expect(sizes.group).toBeGreaterThan(sizes.label);
      await page.screenshot({ path: testInfo.outputPath(`settings-hierarchy-${colorScheme}-${width}.png`), fullPage: true });
    }
  }
  await page.getByRole('link', { name: '浏览', exact: true }).click();
  await expect(page.locator('#service-settings-card')).toBeHidden();
});

test('settings connection cards adapt to three, two and one columns', async ({ page }) => {
  await installMockAPI(page, createBackend());
  await page.goto(`${webUI.url}/#settings`);
  const grid = page.locator('.status-grid');
  await expect(grid).toBeVisible();
  // Test the available container width rather than a particular window size.
  for (const [width, columns] of [[740, 3], [510, 2], [240, 1]]) {
    await grid.evaluate((element, size) => { element.style.width = `${size}px`; element.style.maxWidth = 'none'; }, width);
    expect(await grid.evaluate((element) => getComputedStyle(element).gridTemplateColumns.split(' ').length)).toBe(columns);
  }
  await grid.evaluate((element) => { element.style.width = ''; element.style.maxWidth = ''; });
  await page.setViewportSize({ width: 390, height: 820 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test('filename click builder selects presets, inserts at cursor, previews, undoes and saves',async({page})=>{
  const backend=createBackend();await installMockAPI(page,backend);await page.goto(`${webUI.url}/#settings`);
  const builder=page.locator('#filename-builder');const input=page.locator('#preference-songFileFormat');
  await builder.getByRole('button',{name:'序号 · 歌名',exact:true}).click();
  await expect(input).toHaveValue('{tracknum:02d}. {title}');
  await expect(page.locator('#filename-example')).toContainText('01. 示例歌曲');
  await input.evaluate(el=>{el.focus();el.setSelectionRange(el.value.length,el.value.length);});
  await builder.getByRole('button',{name:'分隔符：横线',exact:true}).click();
  await builder.getByRole('button',{name:'添加艺人',exact:true}).click();
  await expect(input).toHaveValue('{tracknum:02d}. {title} - {artist}');
  await builder.getByRole('button',{name:'撤销上一步',exact:true}).click();
  await expect(input).toHaveValue('{tracknum:02d}. {title} - ');
  await builder.getByRole('button',{name:'撤销上一步',exact:true}).click();
  await expect(input).toHaveValue('{tracknum:02d}. {title}');
  await page.getByRole('button',{name:'保存下载偏好',exact:true}).click();
  await expect(page.locator('#download-preferences-status')).toHaveText('已保存，下一项开始时生效。');
  expect(backend.downloadSettings.songFileFormat).toBe('{tracknum:02d}. {title}');
  await expect(builder.getByRole('button',{name:'撤销上一步',exact:true})).toBeDisabled();
  await page.reload();await expect(input).toHaveValue('{tracknum:02d}. {title}');
  await expect(page.locator('#filename-example')).toContainText('01. 示例歌曲');
  await input.fill('{title}');await input.evaluate(el=>el.setSelectionRange(0,el.value.length));
  await builder.getByRole('button',{name:'添加专辑',exact:true}).click();await expect(input).toHaveValue('{album}');
  await page.setViewportSize({width:390,height:900});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('regional, lyrics and artwork preferences save, reload and reset without credentials', async ({ page }, testInfo) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#settings`);
  const fields = page.locator('#download-preferences-fields');
  await expect(fields).toBeEnabled();
  const buttonFonts = await page.locator('.preferences-actions button').evaluateAll(buttons => buttons.map(button => {
    const s = getComputedStyle(button); return {family:s.fontFamily,size:s.fontSize,weight:s.fontWeight,line:s.lineHeight,height:s.minHeight};
  }));
  expect(buttonFonts[0]).toEqual(buttonFonts[1]);
  await page.locator('#preference-downloadRegion').selectOption('us');
  await page.locator('#preference-metadataRegion').selectOption('cn');
  await page.locator('#preference-lyricsRegion').selectOption('cn');
  await page.locator('#preference-language').selectOption('zh-Hans-CN');
  await page.locator('#preference-saveLyrics').uncheck();
  await page.locator('#preference-coverSize').selectOption('1000x1000');
  await page.locator('#preference-songFileFormat').fill('{disk}-{tracknum:02d}. {title}');
  await page.getByRole('button', { name: '保存下载偏好', exact: true }).click();
  await expect(page.locator('#download-preferences-status')).toHaveText('已保存，下一项开始时生效。');
  const save = backend.calls.find((call) => call.pathname === '/api/v1/settings/download' && call.method === 'PUT');
  const payload = JSON.parse(save.body);
  expect(payload).toEqual({ ...defaultDownloadSettings, downloadRegion: 'us', metadataRegion: 'cn', lyricsRegion: 'cn', language: 'zh-Hans-CN', saveLyrics: false, coverSize: '1000x1000', songFileFormat: '{disk}-{tracknum:02d}. {title}' });
  expect(save.body).not.toMatch(/token|password|cookie/i);
  await page.reload();
  await expect(page.locator('#preference-metadataRegion')).toHaveValue('cn');
  await expect(page.locator('#preference-saveLyrics')).not.toBeChecked();
  await expect(page.locator('#preference-songFileFormat')).toHaveValue('{disk}-{tracknum:02d}. {title}');
  await page.getByRole('button', { name: '恢复配置默认值', exact: true }).click();
  await expect(page.locator('#preference-downloadRegion')).toHaveValue('');
  await expect(page.locator('#preference-saveLyrics')).toBeChecked();
  await expect(page.locator('#preference-songFileFormat')).toHaveValue(defaultDownloadSettings.songFileFormat);
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    await page.screenshot({ path: testInfo.outputPath(`preferences-${width}.png`), fullPage: true });
  }
});

test('filename templates reject paths and unsupported placeholders without saving', async ({ page }) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#settings`);
  await expect(page.locator('#download-preferences-fields')).toBeEnabled();
  for (const value of ['../{title}', '{token}', '{title:02d}']) {
    await page.locator('#preference-songFileFormat').fill(value);
    await page.getByRole('button', { name: '保存下载偏好', exact: true }).click();
    await expect(page.locator('#download-preferences-status')).toContainText('文件名模板无效');
  }
  expect(backend.calls.filter((call) => call.pathname === '/api/v1/settings/download' && call.method === 'PUT')).toHaveLength(0);
});

test('old backend and incomplete preference receipts cannot claim settings were saved', async ({ page }) => {
  const backend = createBackend({ preferencesUnsupported: true });
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#settings`);
  await expect(page.locator('#download-preferences-save')).toBeDisabled();
  await expect(page.locator('#download-preferences-status')).toContainText('旧版后端');
  backend.preferencesUnsupported = false;
  backend.preferenceReceiptIncomplete = true;
  await page.reload();
  await expect(page.locator('#download-preferences-fields')).toBeEnabled();
  await page.getByRole('button', { name: '保存下载偏好', exact: true }).click();
  await expect(page.locator('#download-preferences-status')).toHaveText('保存失败，请重试。');
});

for (const colorScheme of ['light', 'dark']) {
  test(`browser chrome uses muted icons and four top-left horizontal controls (${colorScheme})`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    await installMockAPI(page, createBackend());
    await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
    const toolbar = page.locator('#browse-web-panel .browse-web-toolbar');
    await expect(toolbar).toBeVisible();
    await expect(toolbar.locator('button')).toHaveCount(5);
    await expect(page.locator('.client-nav .browse-web-toolbar')).toHaveCount(0);
    await expect(page.locator('#sidebar-toggle svg')).toHaveCount(1);
    const background = await page.locator('#sidebar-toggle').evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(background).toBe('rgba(0, 0, 0, 0)');
    const buttonIDs = ['browse-web-back', 'browse-web-forward', 'browse-web-reload', 'browse-current-download'];
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 820 });
      const bar = await toolbar.boundingBox();
      const nav = await page.locator('.client-nav').boundingBox();
      const viewport = await page.locator('#embedded-browser-viewport').boundingBox();
      expect(bar.x).toBe(nav.width);
      expect(bar.y).toBe(0);
      expect(bar.height).toBe(48);
      expect(viewport.y).toBe(bar.y + bar.height);
      expect(viewport.height).toBe(820 - bar.height);
      let previous = null;
      for (const id of buttonIDs) {
        const button = page.locator(`#${id}`);
        await expect(button.locator('svg')).toHaveCount(1);
        const box = await button.boundingBox();
        expect(box.y).toBe(6.5);
        expect(box.width).toBe(34);
        if (previous) expect(box.x).toBe(previous.x + previous.width + 4);
        previous = box;
      }
      await page.screenshot({ path: testInfo.outputPath(`chrome-${colorScheme}-${width}.png`) });
    }
    await page.getByRole('link', { name: '设置与状态', exact: true }).click();
    await expect(toolbar).toBeHidden();
  });
}

test('offline-to-connected desktop retains bundled full-bleed layout even with an old WSL UI', async ({ page }) => {
  // Use the production manager factory and real TCP listener, not the test's
  // static web server. No WSL/control operation or credential provider is used.
  let helperCalls = 0;
  const manager = new WslManager({
    windowsProbeExe: 'C:\\fixture\\amdl-windows-probe.exe',
    appDataDir: 'C:\\fixture\\app-data',
    downloadDir: 'C:\\fixture\\downloads',
    processEnvironment: {},
    spawn: () => { helperCalls += 1; throw new Error('old backend HTML requested'); },
  });
  const proxy = manager.proxyFactory();
  const port = await proxy.start();
  try {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.addInitScript(() => {
      window.desktopShell = {
        openAppleMusic: async () => ({ ok: true }),
        setAppleMusicBounds: async (bounds) => { window.__browserBounds = bounds; return { ok: true }; },
        closeAppleMusic: async () => ({ ok: true }),
      };
    });
    await installMockAPI(page, createBackend());
    await page.route('**/favicon.ico', (route) => route.fulfill({ status: 204 }));
    await page.goto(pathToFileURL(path.join(rendererDirectory, 'index.html')).href + '#browse');
    await expect(page.locator('.client-nav')).toHaveClass(/collapsed/);
    await page.setExtraHTTPHeaders(manager.desktopProxyRequestHeaders());
    await page.goto(`http://127.0.0.1:${port}/#browse`);
    await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
    await expect(page.locator('#download-readiness')).toHaveClass(/ready/);
    await expect(page.locator('.client-nav')).toHaveClass(/collapsed/);
    await expect(page.locator('#browse-heading')).toHaveCount(0);
    await expect(page.locator('#browse-web-address')).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__browserBounds)).toEqual({ x: 68, y: 48, width: 1212, height: 752 });
    await page.getByRole('button', { name: '展开侧边栏' }).click();
    await expect.poll(() => page.evaluate(() => window.__browserBounds)).toEqual({ x: 200, y: 48, width: 1080, height: 752 });
    await page.getByRole('link', { name: '下载任务', exact: true }).click();
    await expect(page.locator('#job-form')).toBeVisible();
    await page.getByRole('link', { name: '设置与状态', exact: true }).click();
    await expect(page.locator('#service-status-title')).toBeVisible();
    expect(helperCalls).toBe(0);
  } finally { await proxy.stop(); }
});

test('the client opens on a browser-first three-section shell with collapsible navigation', async ({ page }) => {
  await page.addInitScript(() => window.localStorage.setItem('amdl.sidebarCollapsed', '0'));
  await installMockAPI(page, createBackend({ capabilities: {
    wrapper: { status: 'online', loginCompleted: true }, queue: { state: 'ready' }, jobExecutorConfigured: true,
    qualities: { 'aac-lc': { available: true } },
  } }));
  await page.goto(webUI.url);
  await expect(page.locator('#browse-section')).toBeVisible();
  await expect(page.locator('.client-nav a[data-view]')).toHaveCount(3);
  await expect(page.locator('#browse-search-form')).toHaveCount(0);
  await expect(page.locator('#service-status-title')).toBeHidden();
  await expect(page.getByRole('link', { name: /已登录.*可以提交/ })).toHaveAttribute('href', '#settings');
  await expect(page.locator('.client-nav')).toHaveClass(/collapsed/);
  await page.getByRole('button', { name: '展开侧边栏' }).click();
  await expect(page.locator('.client-nav')).not.toHaveClass(/collapsed/);
  await page.reload();
  await expect(page.locator('.client-nav')).not.toHaveClass(/collapsed/);
  await page.getByRole('button', { name: '收起侧边栏' }).click();
  await page.getByRole('link', { name: '设置与状态', exact: true }).click();
  await expect(page.locator('#service-status-title')).toBeVisible();
  await expect(page.locator('#log-container')).toBeAttached();
  await page.getByRole('link', { name: '下载任务' }).click();
  await expect(page.locator('.files-section')).toBeVisible();
});

test('settings exposes startup login evidence and one-click sanitized log export', async ({page})=>{
 await page.addInitScript(()=>{
  window.__exports=[];
  window.desktopShell={exportDiagnostics:async(lines)=>{window.__exports.push(lines);return{ok:true,cancelled:window.__cancelExport===true};}};
 });
 const backend=createBackend();backend.capabilities.wrapper.loginCompleted=false;
 await installMockAPI(page,backend);await page.goto(webUI.url);
 await expect(page.locator('#download-readiness')).toHaveClass(/unverified/);
 await page.getByRole('link',{name:'设置与状态',exact:true}).click();
 await expect(page.locator('#wrapper-status')).toContainText('登录未确认');
 await page.locator('#settings-export-logs').click();
 await expect(page.locator('#desktop-settings-status')).toContainText('已完成');
 expect((await page.evaluate(()=>window.__exports[0])).length).toBeLessThanOrEqual(100);
 backend.capabilities.wrapper.loginCompleted=true;
 await page.evaluate(()=>document.getElementById('refresh-btn').click());
 await expect(page.locator('#download-readiness')).toHaveClass(/ready/);
 await expect(page.locator('#wrapper-status')).toContainText('已登录');
 await page.evaluate(()=>{window.__cancelExport=true;});
 await page.locator('#export-logs').click();
 await expect(page.locator('#desktop-settings-status')).toContainText('已取消日志导出');
 await page.setViewportSize({width:390,height:844});
 await expect(page.locator('#settings-export-logs')).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
});

test('packaged offline shell permits browsing and setup but never reads or writes the task API', async ({ page }) => {
  const apiRequests = [];
  page.on('request', (request) => { if (request.url().includes('/api/v1/')) apiRequests.push(request.url()); });
  await page.addInitScript(() => {
    window.__setupOpens = 0;
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }),
      setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }),
      currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/us/song/demo/123' }),
      openDownloadSettings: async () => { window.__setupOpens += 1; return { ok: true }; },
      onServiceState: (callback) => { window.__serviceStateCallback = callback; return () => {}; },
      repairRuntime: async () => ({ ok: false, error: {
        code: 'network_preflight_failed', message: '网络预检失败', action: '请重试', details: { stage: window.__repairStage || 'tls' },
      } }),
    };
  });
  await page.goto(pathToFileURL(path.join(rendererDirectory, 'index.html')).href + '#browse');
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await expect(page.locator('#browse-section')).toBeVisible();
  await expect(page.locator('#download-readiness')).toHaveClass(/not-ready/);
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('当前无法提交下载');
  await page.getByRole('link', { name: '设置与状态', exact: true }).click();
  await page.evaluate(() => window.__serviceStateCallback({ state: 'unavailable', code: 'network_preflight_failed', stage: 'tls' }));
  await expect(page.locator('#desktop-settings-status')).toContainText('tls');
  await page.evaluate(() => window.__serviceStateCallback({ state: 'unavailable', code: 'network_preflight_failed', stage: 'SENTINEL_STAGE' }));
  await expect(page.locator('#desktop-settings-status')).not.toContainText('SENTINEL_STAGE');
  await page.getByRole('button', { name: '安装与启动设置' }).click();
  await expect.poll(() => page.evaluate(() => window.__setupOpens)).toBe(1);
  await page.getByRole('button', { name: '修复运行时' }).click();
  await expect(page.locator('#desktop-settings-status')).toContainText('tls');
  await page.evaluate(() => { window.__repairStage = 'SENTINEL_STAGE'; });
  await page.getByRole('button', { name: '修复运行时' }).click();
  await expect(page.locator('#desktop-settings-status')).not.toContainText('SENTINEL_STAGE');
  expect(apiRequests).toEqual([]);
});

test('current Apple Music page queues once with saved defaults and only confirms a complete receipt', async ({ page }) => {
  await page.addInitScript(() => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }),
      setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }),
      currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/us/album/demo/202?i=101' }),
    };
  });
  const backend = createBackend({ capabilities: {
    wrapper: { status: 'online' }, queue: { state: 'ready' }, jobExecutorConfigured: true,
    qualities: Object.fromEntries(['aac-lc', 'alac', 'aac', 'atmos'].map((quality) => [quality, { available: true }])),
  } });
  await installMockAPI(page, backend);
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await page.getByRole('link', { name: '设置与状态', exact: true }).click();
  await page.locator('#default-quality').selectOption('alac');
  await page.locator('#default-existing-policy').selectOption('fail');
  await page.getByRole('link', { name: '浏览', exact: true }).click();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('已加入下载队列');
  await expect(page.locator('#browse-current-download [data-download-feedback]')).toHaveText('✓');
  const post = backend.calls.find((call) => call.pathname === '/api/v1/jobs' && call.method === 'POST');
  expect(JSON.parse(post.body)).toMatchObject({ urls: ['https://music.apple.com/us/album/demo/202?i=101'], quality: 'alac', existingPolicy: 'fail' });
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('已在队列中');
  expect(backend.calls.filter((call) => call.pathname === '/api/v1/jobs' && call.method === 'POST')).toHaveLength(1);
  await page.getByRole('link', { name: '设置与状态', exact: true }).click();
  await page.locator('#default-quality').selectOption('aac-lc');
  await page.getByRole('link', { name: '浏览', exact: true }).click();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect.poll(() => backend.calls.filter((call) => call.pathname === '/api/v1/jobs' && call.method === 'POST').length).toBe(2);
  for (const job of backend.jobs) job.status = 'succeeded';
  await page.getByRole('link', { name: '下载任务' }).click();
  await page.locator('#refresh-btn').click();
  await page.getByRole('link', { name: '设置与状态', exact: true }).click();
  await page.locator('#default-quality').selectOption('alac');
  await page.getByRole('link', { name: '浏览', exact: true }).click();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect.poll(() => backend.calls.filter((call) => call.pathname === '/api/v1/jobs' && call.method === 'POST').length).toBe(3);
  await page.reload();
  await page.getByRole('link', { name: '设置与状态', exact: true }).click();
  await expect(page.locator('#default-quality')).toHaveValue('alac');
  await expect(page.locator('#default-existing-policy')).toHaveValue('fail');
});

test('website download adapter still rejects private library URLs',async({page})=>{
  await page.addInitScript(()=>{
    window.desktopShell={openAppleMusic:async()=>({ok:true}),setAppleMusicBounds:async()=>({ok:true}),closeAppleMusic:async()=>({ok:true}),currentAppleMusicURL:async()=>({ok:true,url:'https://music.apple.com/us/library/playlist/p.private'})};
  });
  const backend=createBackend();await installMockAPI(page,backend);await page.goto(webUI.url);
  await page.getByRole('button',{name:'打开官方网页',exact:true}).click();
  await page.getByRole('button',{name:'下载当前页面',exact:true}).click();
  await expect(page.locator('#browse-web-status')).toContainText('当前页面');
  expect(backend.calls.filter(call=>call.method==='POST')).toHaveLength(0);
});

test('browser CN selected track resolves before queueing and deduplicates the US resource', async ({ page }) => {
  const original = 'https://music.apple.com/cn/album/%E6%8C%AA%E5%BE%B7%E5%8D%A1%E8%8E%B1/1845873509?i=1845873514';
  await page.addInitScript((url) => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }), setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }), currentAppleMusicURL: async () => ({ ok: true, url }),
      onAppleMusicDownloadIntent: (listener) => { window.__downloadIntent = listener; },
    };
  }, original);
  const backend = createBackend({ regionURL: 'https://music.apple.com/us/song/999' });
  await installMockAPI(page, backend);
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('已加入下载队列');
  const mutations = backend.calls.filter((call) => call.method === 'POST');
  expect(mutations.map((call) => call.pathname)).toEqual(['/api/v1/browser-download/resolve', '/api/v1/jobs']);
  expect(JSON.parse(mutations[0].body)).toEqual({ url: original });
  expect(JSON.parse(mutations[1].body).urls).toEqual(['https://music.apple.com/us/song/999']);
  await page.evaluate((url) => window.__downloadIntent({ url }), original);
  await expect(page.locator('#browse-web-status')).toContainText('已在队列中');
  expect(backend.jobs).toHaveLength(1);
});

test('failed or incomplete region conversion never enqueues or leaks raw errors', async ({ page }) => {
  await page.addInitScript(() => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }), setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }), currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/cn/album/demo/123' }),
    };
  });
  const backend = createBackend({ regionFailure: true });
  await installMockAPI(page, backend); await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('对应资源');
  await expect(page.locator('#browse-web-status')).not.toContainText('SENTINEL');
  backend.regionFailure = false; backend.regionReceipt = { url: 'https://music.apple.com/us/album/999' };
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('转换回执不完整');
  expect(backend.calls.some((call) => call.method === 'POST' && call.pathname === '/api/v1/jobs')).toBe(false);
});

test('region resolution pending blocks repeated clicks and rechecks service readiness before enqueue', async ({ page }) => {
  await page.addInitScript(() => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }), setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }), currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/cn/album/demo/123' }),
    };
  });
  const backend = createBackend(); await installMockAPI(page, backend);
  let release; const gate = new Promise((resolve) => { release = resolve; });
  let pending = 0;
  await page.route('**/api/v1/browser-download/resolve', async (route) => { pending++; await gate; await route.fallback(); });
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  const button = page.getByRole('button', { name: '下载当前页面' });
  await button.click(); await expect.poll(() => pending).toBe(1);
  await button.click(); expect(pending).toBe(1);
  backend.capabilities.queue.state = 'maintenance';
  await page.evaluate(() => window.amdlRenderer.refreshCapabilities());
  release();
  await expect(page.locator('#browse-web-status')).toContainText('服务状态已变化');
  expect(backend.calls.some((call) => call.method === 'POST' && call.pathname === '/api/v1/jobs')).toBe(false);
});

test('a rapid second current-page click cannot create another pending job', async ({ page }) => {
  await page.addInitScript(() => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }), setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }),
      currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/us/song/demo/101' }),
    };
  });
  const backend = createBackend();
  backend.capabilities.wrapper.loginCompleted = false;
  await installMockAPI(page, backend);
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  await page.route('**/api/v1/jobs', async (route) => {
    if (route.request().method() === 'POST') await gate;
    await route.fallback();
  });
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await expect(page.locator('#download-readiness')).toHaveClass(/unverified/);
  const download = page.getByRole('button', { name: '下载当前页面' });
  await download.click();
  await expect(page.locator('#browse-web-status')).toContainText('正在加入下载队列');
  await expect(page.locator('#browse-current-download [data-download-feedback]')).toHaveText('…');
  // Feedback belongs to the structured result, independently of translated copy.
  await page.locator('#browse-web-status').evaluate(element => { element.textContent = 'Fixture replacement copy'; });
  await expect(page.locator('#browse-current-download [data-download-feedback]')).toHaveText('…');
  await download.click();
  release();
  await expect(page.locator('#browse-web-status')).toContainText('已加入下载队列');
  expect(backend.calls.filter((call) => call.pathname === '/api/v1/jobs' && call.method === 'POST')).toHaveLength(1);
});

test('host fallback rejects an unsupported current-page URL without submitting a job', async ({ page }) => {
  await page.addInitScript(() => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }), setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }),
      currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/us/album/demo/not-a-catalog-id' }),
    };
  });
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('不是可下载');
  await expect(page.locator('#browse-current-download [data-download-feedback]')).toHaveText('!');
  expect(backend.calls.some((call) => call.pathname === '/api/v1/jobs' && call.method === 'POST')).toBe(false);
});

test('browse replaces catalog search and fits desktop and 390px layouts', async ({ page }, testInfo) => {
  await installMockAPI(page, createBackend());
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await expect(page.locator('#browse-search-form')).toHaveCount(0);
  await expect(page.locator('#embedded-browser-viewport')).toBeVisible();
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const nav = await page.locator('.client-nav').boundingBox();
    const viewport = await page.locator('#embedded-browser-viewport').boundingBox();
    expect(nav.x).toBe(0);
    expect(nav.y).toBe(0);
    expect(nav.height).toBe(900);
    expect(viewport.x).toBe(nav.width);
    expect(viewport.y).toBe(48);
    expect(viewport.width).toBe(width - nav.width);
    expect(viewport.height).toBe(852);
    await expect(page.locator('.hero')).toBeHidden();
    await expect(page.locator('footer')).toBeHidden();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`browse-${width}.png`), fullPage: true });
  }
});

test('desktop browse tab controls an isolated Apple Music page without exposing a login form', async ({ page }) => {
  const calls = [];
  await page.addInitScript(() => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }),
      setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }),
      currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/us/song/303' }),
    };
  });
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await expect(page.locator('#embedded-browser-viewport')).toBeVisible();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('已加入下载队列');
  calls.push(...backend.calls.filter((call) => call.method === 'POST' && call.pathname === '/api/v1/jobs'));
  expect(JSON.parse(calls[0].body).urls).toEqual(['https://music.apple.com/us/song/303']);
  await expect(page.locator('#browse-section input[type="password"]')).toHaveCount(0);
});

test('switching away while the embedded page opens closes the late view', async ({ page }) => {
  await page.addInitScript(() => {
    window.__browserOpens = 0;
    window.__browserCloses = 0;
    window.desktopShell = {
      openAppleMusic: () => { window.__browserOpens += 1; return new Promise((resolve) => { window.__finishBrowserOpen = resolve; }); },
      setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => { window.__browserCloses += 1; return { ok: true }; },
    };
  });
  await installMockAPI(page, createBackend());
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await expect.poll(() => page.evaluate(() => window.__browserOpens)).toBe(1);
  await page.getByRole('link', { name: '下载任务' }).click();
  await page.evaluate(() => window.__finishBrowserOpen({ ok: true }));
  await expect.poll(() => page.evaluate(() => window.__browserCloses)).toBe(1);
});

test('expanding the sidebar while the page loads uses the final browser bounds', async ({ page }) => {
  await page.addInitScript(() => {
    window.__browserBounds = [];
    window.desktopShell = {
      openAppleMusic: () => new Promise((resolve) => { window.__finishBrowserOpen = resolve; }),
      setAppleMusicBounds: async (bounds) => { window.__browserBounds.push(bounds); return { ok: true }; },
      closeAppleMusic: async () => ({ ok: true }),
    };
  });
  await installMockAPI(page, createBackend());
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await expect.poll(() => page.evaluate(() => typeof window.__finishBrowserOpen)).toBe('function');
  await page.getByRole('button', { name: '展开侧边栏' }).click();
  const viewport = await page.locator('#embedded-browser-viewport').boundingBox();
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.evaluate(() => window.__finishBrowserOpen({ ok: true }));
  await expect.poll(() => page.evaluate(() => window.__browserBounds[0])).toEqual({
    x: viewport.x, y: viewport.y, width: viewport.width, height: viewport.height,
  });
});

test('browse does not report queue success when the server returns an incomplete receipt', async ({ page }) => {
  await page.addInitScript(() => {
    window.desktopShell = {
      openAppleMusic: async () => ({ ok: true }), setAppleMusicBounds: async () => ({ ok: true }),
      closeAppleMusic: async () => ({ ok: true }),
      currentAppleMusicURL: async () => ({ ok: true, url: 'https://music.apple.com/us/song/101' }),
    };
  });
  const backend = createBackend({ jobCreateResponse: { jobs: [] } });
  await installMockAPI(page, backend);
  await page.goto(webUI.url);
  await page.getByRole('button', { name: '打开官方网页', exact: true }).click();
  await page.getByRole('button', { name: '下载当前页面' }).click();
  await expect(page.locator('#browse-web-status')).toContainText('队列回执不完整');
  await expect(page.locator('#browse-web-status')).not.toContainText('已加入');
});

test('record-only deletion preserves files across refresh and requires confirmation', async ({ page }) => {
  const backend = createBackend({ jobs: [{ id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 }],
    files: [{ id: 'file-1', jobId: 'job-1', name: 'retained.m4a', size: 42 }] });
  await installMockAPI(page, backend);
  await page.route('**/api/v1/jobs/job-1/record', async (route) => {
    backend.calls.push({ pathname: '/api/v1/jobs/job-1/record', method: route.request().method() });
    backend.jobs = [];
    backend.files = backend.files.map(file => ({ ...file, jobId: '', unmanaged: true }));
    return route.fulfill({ status: 204 });
  });
  await page.goto(`${webUI.url}/#tasks`);
  const remove = page.getByRole('button', { name: '仅删除任务（保留文件）', exact: true });
  await expect(remove).toBeVisible();
  page.once('dialog', dialog => dialog.dismiss());
  await remove.click();
  expect(backend.calls.filter(call => call.method === 'DELETE')).toHaveLength(0);
  page.once('dialog', async dialog => { expect(dialog.message()).toContain('保留'); await dialog.accept(); });
  await remove.click();
  await expect(page.locator('.job-card')).toHaveCount(0);
  await expect(page.locator('#files')).toContainText('retained.m4a');
  await page.reload();
  await expect(page.locator('#files')).toContainText('retained.m4a');
  expect(backend.files).toHaveLength(1);
  expect(backend.calls.filter(call => call.method === 'DELETE').map(call => call.pathname)).toEqual(['/api/v1/jobs/job-1/record']);
});

test('record-only deletion never falls back to file deletion on an old backend', async ({ page }) => {
  const backend = createBackend({ jobs: [{ id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 }] });
  await installMockAPI(page, backend);
  await page.route('**/api/v1/jobs/job-1/record', async (route) => response(route, { error: { message: 'unsupported' } }, 404));
  await page.goto(`${webUI.url}/#tasks`);
  page.on('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: '仅删除任务（保留文件）', exact: true }).click();
  await expect(page.locator('.job-card')).toContainText('后端');
  await expect(page.locator('.job-card')).toHaveCount(1);
  expect(backend.calls.filter(call => call.method === 'DELETE')).toHaveLength(0);
});

test('delete click survives a refresh between pointer down and pointer up', async ({ page }) => {
  const backend = createBackend({ jobs: [
    { id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 },
    { id: 'job-2', status: 'running', total: 2, succeeded: 0 },
  ] });
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#tasks`);
  const remove = page.getByRole('button', { name: '删除任务和文件', exact: true });
  await expect(remove).toBeVisible();
  await remove.scrollIntoViewIfNeeded();
  await expect(remove).toBeInViewport();
  let confirmations = 0;
  page.on('dialog', async (dialog) => { confirmations += 1; await dialog.accept(); });
  const box = await remove.boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.evaluate(async () => {
    await window.amdlRenderer.refreshCapabilities();
    window.__amdlMockSSE.emit(0, 'progress', { total: 2, succeeded: 1 }, 'during-click');
  });
  await page.mouse.up();
  await expect(remove).toHaveCount(0);
  expect(confirmations).toBe(1);
  expect(backend.calls.filter((call) => call.method === 'DELETE')).toHaveLength(1);
  await expect(page.locator('.job-card')).toHaveCount(1);
});

test('slow delete is disabled until acknowledged and failure is shown on its card', async ({ page }) => {
  const backend = createBackend({ jobs: [{ id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 }] });
  await installMockAPI(page, backend);
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let deletes = 0;
  await page.route('**/api/v1/jobs/job-1', async (route) => {
    if (route.request().method() !== 'DELETE') return route.fallback();
    deletes += 1;
    await gate;
    return response(route, { error: { message: '文件正被使用，请关闭占用文件后重试。' } }, 409);
  });
  page.on('dialog', (dialog) => dialog.accept());
  await page.goto(`${webUI.url}/#tasks`);
  await page.getByRole('button', { name: '删除任务和文件', exact: true }).click();
  try {
    await expect(page.getByRole('button', { name: '正在删除…', exact: true })).toBeDisabled();
    await page.evaluate(() => window.amdlRenderer.refreshCapabilities());
    await expect(page.getByRole('button', { name: '正在删除…', exact: true })).toBeDisabled();
    await expect(page.locator('.job-card')).toHaveCount(1);
    expect(deletes).toBe(1);
  } finally { release(); }
  await expect(page.locator('.job-card')).toContainText('文件正被使用');
  await expect(page.getByRole('button', { name: '删除任务和文件', exact: true })).toBeEnabled();
});

test.beforeAll(async () => {
  webUI = await startWebUI();
});

test.afterAll(async () => {
  await webUI.close();
});

test('queue submission, progress events, and browser refresh use the persistent v1 job API', async ({ page }) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#tasks`);

  await page.locator('#music-urls').fill('https://music.apple.com/us/album/first/1\nhttps://music.apple.com/us/album/second/2');
  await page.selectOption('#quality', 'aac');
  await page.selectOption('#existing-policy', 'overwrite');
  await page.getByRole('button', { name: '加入队列' }).click();

  const createCall = backend.calls.find((call) => call.method === 'POST' && call.pathname === '/api/v1/jobs');
  expect(JSON.parse(createCall.body)).toMatchObject({
    urls: ['https://music.apple.com/us/album/first/1', 'https://music.apple.com/us/album/second/2'],
    quality: 'aac',
    existingPolicy: 'overwrite',
  });
  await expect(page.locator('#jobs')).toContainText('first');
  await expect(page.locator('#jobs')).toContainText('second');

  expect(await page.evaluate(() => window.__amdlMockSSE.count())).toBe(0);
  backend.jobs[0].status = 'running';
  await expect.poll(() => page.evaluate(() => window.__amdlMockSSE.count()), { timeout: 5_000 }).toBe(1);
  await page.evaluate(() => window.__amdlMockSSE.emit(0, 'progress', { total: 2, succeeded: 1, failed: 0, skipped: 0, unavailable: 0 }, 'progress-1'));
  await expect(page.locator('#jobs')).toContainText('已处理：1/2');

  backend.jobs[0].total = 2;
  backend.jobs[0].succeeded = 1;
  await page.reload();
  await expect(page.locator('#jobs')).toContainText('first');
  await expect(page.locator('#jobs')).toContainText('已处理：1/2');
});

test('the existing URL input accepts a personal library playlist without a browser login flow', async ({ page }) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#tasks`);

  await expect(page.locator('.url-field .field-description')).toContainText('个人资料库歌单');
  await page.locator('#music-urls').fill('https://music.apple.com/us/library/playlist/my-mix/p.abc-123');
  await page.getByRole('button', { name: '加入队列' }).click();

  const createCall = backend.calls.find((call) => call.method === 'POST' && call.pathname === '/api/v1/jobs');
  expect(JSON.parse(createCall.body).urls).toEqual(['https://music.apple.com/us/library/playlist/my-mix/p.abc-123']);
  // Official playback login lives in Settings; manual playlist submission
  // must still neither add nor require a login control in its own form.
  expect(await page.locator('#job-form input, #job-form button').filter({ hasText: '登录' }).count()).toBe(0);
});

test('download concurrency setting is adjustable and resettable', async ({ page }) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#settings`);

  await expect(page.locator('#download-concurrency')).toHaveValue('3');
  await page.locator('#download-concurrency').fill('5');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.locator('#download-concurrency')).toHaveValue('5');
  expect(backend.downloadConcurrency).toBe(5);

  await page.getByRole('button', { name: '恢复默认' }).click();
  await expect(page.locator('#download-concurrency')).toHaveValue('3');
  expect(backend.downloadConcurrency).toBe(3);
});

test('desktop settings includes every service menu action and preserves settings layout', async ({ page }, testInfo) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.addInitScript(() => {
    window.__desktopCalls = [];
    window.desktopShell = Object.fromEntries([
      'chooseDownloadLocation', 'openDownloadSettings', 'openCredentials', 'startService', 'showDiagnostics', 'repairRuntime', 'stopService',
    ].map((method) => [method, async (...args) => {
      window.__desktopCalls.push({ method, args });
      return method === 'stopService' ? { ok: true, cancelled: true } : { ok: true };
    }]));
  });
  await page.goto(webUI.url);
  await expect(page.locator('#desktop-settings')).toBeHidden();
  await page.getByRole('link', { name: '设置与状态', exact: true }).click();
  await expect(page.locator('#desktop-settings')).toBeVisible();
  await expect(page.locator('#desktop-settings-browser-note')).toBeHidden();
  const actions = [
    ['选择下载位置', 'chooseDownloadLocation'], ['安装与启动设置', 'openDownloadSettings'], ['配置凭据', 'openCredentials'],
    ['启动/重新连接', 'startService'], ['查看诊断信息', 'showDiagnostics'],
    ['修复运行时', 'repairRuntime'], ['停止服务', 'stopService'],
  ];
  for (const [label, method] of actions) {
    await page.locator('.settings-section').getByRole('button', { name: label, exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__desktopCalls.at(-1))).toEqual({ method, args: [] });
  }
  await expect(page.locator('#desktop-settings-status')).toContainText('已取消');
  expect(await page.locator('#desktop-settings input[type=password]').count()).toBe(0);
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme });
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`settings-${colorScheme}-${width}.png`), fullPage: true });
    }
  }
});

test('browser settings does not advertise unavailable desktop controls', async ({ page }) => {
  await installMockAPI(page, createBackend());
  await page.goto(`${webUI.url}/#settings`);
  await expect(page.locator('#desktop-settings')).toBeHidden();
  await expect(page.locator('#desktop-service-settings')).toBeHidden();
  await expect(page.locator('#desktop-settings-browser-note')).toContainText('桌面客户端');
  await expect(page.locator('#download-concurrency')).toBeVisible();
});

test('a 100 URL batch keeps browser connections available for queue refresh and cancellation', async ({ page }) => {
  const backend = createBackend();
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#tasks`);

  const urls = Array.from(
    { length: 100 },
    (_, index) => `https://music.apple.com/us/album/browser-batch/${index + 1}`,
  );
  await page.locator('#music-urls').fill(urls.join('\n'));
  await page.getByRole('button', { name: '加入队列' }).click();

  await expect(page.locator('.job-card')).toHaveCount(100);
  expect(backend.calls.some((call) => call.method === 'GET' && call.pathname === '/api/v1/jobs' && call.search === '?limit=100')).toBe(true);
  expect(await page.evaluate(() => window.__amdlMockSSE.count())).toBe(0);

  backend.jobs[0].status = 'running';
  await expect.poll(() => page.evaluate(() => window.__amdlMockSSE.count()), { timeout: 5_000 }).toBe(1);
  await expect(page.locator('.job-card').first()).toContainText('下载中');

  await page.locator('.job-card').first().getByRole('button', { name: '取消' }).click();
  await expect(page.locator('.job-card').first()).toContainText('已取消');
  expect(backend.calls.filter((call) => call.pathname === '/api/v1/jobs/job-1/cancel' && call.method === 'POST')).toHaveLength(1);
});

test('queue controls keep cancelled jobs terminal and retry failed jobs without creating a second URL', async ({ page }) => {
  const backend = createBackend({
    jobs: [{
      id: 'job-1', url: 'https://music.apple.com/us/album/example/1', quality: 'alac', existingPolicy: 'skip',
      status: 'running', total: 1, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
    }],
  });
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#tasks`);

  await page.getByRole('button', { name: '取消' }).click();
  await expect(page.locator('#jobs')).toContainText('已取消');
  await expect(page.getByRole('button', { name: '重试失败项' })).toHaveCount(0);

  backend.jobs[0].status = 'failed';
  backend.jobs[0].failed = 1;
  await page.reload();
  await page.getByRole('button', { name: '重试失败项' }).click();
  await expect(page.locator('#jobs')).toContainText('下载中');

  expect(backend.calls.filter((call) => call.pathname === '/api/v1/jobs/job-1/cancel' && call.method === 'POST')).toHaveLength(1);
  expect(backend.calls.filter((call) => call.pathname === '/api/v1/jobs/job-1/retry' && call.method === 'POST')).toHaveLength(1);
  expect(backend.calls.filter((call) => call.pathname === '/api/v1/jobs' && call.method === 'POST')).toHaveLength(0);
});

test('browser bearer stays out of cookies and URLs while authenticating fetch-stream SSE', async ({ page, context }) => {
  const apiToken = '0123456789abcdef'.repeat(4);
  const backend = createBackend({
    apiToken,
    jobs: [{
      id: 'job-1', url: 'https://music.apple.com/us/album/authenticated/1', quality: 'alac', existingPolicy: 'skip',
      status: 'running', total: 1, succeeded: 0, failed: 0, skipped: 0, unavailable: 0,
    }],
    sseEvents: [
      { id: '7', type: 'state', data: { id: 'job-1', status: 'succeeded', total: 1, succeeded: 1 } },
    ],
  });
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#tasks`);

  await expect(page.locator('#jobs')).toContainText('完成');
  const streamCall = backend.calls.find((call) => call.pathname === '/api/v1/jobs/job-1/events');
  expect(streamCall && streamCall.authorized).toBe(true);
  expect(backend.calls.some((call) => call.pathname === '/api/v1/session')).toBe(false);
  expect(backend.calls.some((call) => call.pathname.includes(apiToken) || call.search.includes(apiToken))).toBe(false);
  expect((await context.cookies()).some((cookie) => cookie.name === 'amdl_session')).toBe(false);
});

test('the local 2FA form validates six digits and updates capabilities after submission', async ({ page }) => {
  const backend = createBackend({
    capabilities: {
      wrapper: { status: 'needs_2fa' },
      qualities: { 'aac-lc': { available: true }, alac: { available: false, action: '请先完成两步验证' } },
    },
  });
  await installMockAPI(page, backend);
  await page.goto(`${webUI.url}/#settings`);

  await expect(page.locator('#twofa-section')).toBeVisible();
  await page.locator('#twofa-code').fill('12345');
  await page.getByRole('button', { name: '提交' }).click();
  expect(backend.calls.filter((call) => call.pathname === '/api/v1/wrapper/2fa')).toHaveLength(0);

  await page.locator('#twofa-code').fill('123456');
  await page.getByRole('button', { name: '提交' }).click();
  const twoFACall = backend.calls.find((call) => call.pathname === '/api/v1/wrapper/2fa');
  expect(JSON.parse(twoFACall.body)).toEqual({ code: '123456' });
  await expect(page.locator('#twofa-section')).toBeHidden();
});

for (const appearance of ['light', 'dark']) {
  test(`client navigation preserves form state and fits desktop/mobile (${appearance})`, async ({ page }, testInfo) => {
    const backend = createBackend({ capabilities: {
      wrapper: { status: 'needs_2fa' }, qualities: { 'aac-lc': { available: true } },
    } });
    await installMockAPI(page, backend);
    await page.emulateMedia({ colorScheme: appearance });
    await page.goto(`${webUI.url}/#tasks`);
    await page.locator('#music-urls').fill('https://music.apple.com/us/playlist/example/pl.u-test');
    await page.getByRole('link', { name: '设置与状态', exact: true }).click();
    await page.locator('#twofa-code').fill('123');
    await expect(page.locator('#download-concurrency')).toBeVisible();
    await expect(page.locator('#music-urls')).toBeHidden();
    await page.getByRole('link', { name: '下载任务', exact: true }).click();
    await expect(page.locator('#music-urls')).toHaveValue('https://music.apple.com/us/playlist/example/pl.u-test');
    await expect(page.locator('#twofa-code')).toHaveValue('123');
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`client-${appearance}-${width}.png`), fullPage: true });
    }
  });
}

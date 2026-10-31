'use strict';

(({ renderer, queueCatalog, playback }) => {
  const { api, offlineShell, downloadSubmissionReady, queueNeedsRecovery } = renderer;
  const panel = document.getElementById('catalog-panel');
  const results = document.getElementById('catalog-results');
  const detail = document.getElementById('catalog-detail');
  const status = document.getElementById('catalog-status');
  const account = document.getElementById('catalog-account');
  const region = document.getElementById('catalog-region');
  const input = document.getElementById('catalog-search-input');
  const names = { songs: '歌曲', albums: '专辑', playlists: '歌单', 'library-playlists':'个人歌单', 'music-videos': '音乐视频' };
  const state = { generation: 0, abort: null, initialization: null, loaded: false, dataLoaded: false, kind: '', term: '', current: null, pending: false, view:'home', accountStorefront:'' };
  const nativeVisible = () => document.body.dataset.view === 'browse' && document.body.dataset.browseMode === 'native';
  const validRegion = (value) => typeof value === 'string' && /^[a-z]{2}$/.test(value);
  const node = (tag, text, className) => { const item = document.createElement(tag); if (text !== undefined) item.textContent = text; if (className) item.className = className; return item; };
  function selectRegion(value) {
    if (![...region.options].some(option => option.value === value)) {const option = node('option',value.toUpperCase());option.value=value;region.append(option);}
    region.value=value;
  }
  function button(text, action, className = 'secondary-button') { const result = node('button', text, className); result.type = 'button'; result.addEventListener('click', action); return result; }
  function icon(name) {
    const paths = { download:'M12 3v12m-4-4 4 4 4-4M5 17v4h14v-4', back:'m14 5-7 7 7 7', refresh:'M20 10a8 8 0 1 0-1 7M20 4v6h-6', external:'M14 4h6v6M20 4 10 14M10 4H4v16h16v-6' };
    const svg = document.createElementNS('http://www.w3.org/2000/svg','svg');
    svg.setAttribute('viewBox','0 0 24 24'); svg.setAttribute('class','chrome-icon'); svg.setAttribute('aria-hidden','true');
    const path = document.createElementNS(svg.namespaceURI,'path'); path.setAttribute('d',paths[name]); svg.append(path); return svg;
  }
  function pageMode(mode) {
    panel.dataset.page = mode;
    document.getElementById('catalog-back').disabled = mode !== 'detail';
  }
  function artwork(url) {
    let safe = false;
    try { const u = new URL(url); safe = u.protocol === 'https:' && !u.username && !u.password && !u.port && u.hostname.endsWith('.mzstatic.com'); } catch { /* No image is valid. */ }
    if (!safe) return node('div', '♪', 'catalog-artwork');
    const image = node('img', undefined, 'catalog-artwork'); image.src = url; image.alt = ''; image.loading = 'lazy'; image.referrerPolicy = 'no-referrer'; return image;
  }
  function validItem(item) {
    if (!item || !Object.hasOwn(names, item.type) || typeof item.id !== 'string' || typeof item.title !== 'string' || typeof item.artist !== 'string' || typeof item.downloadable !== 'boolean') return false;
    if (item.unavailableReason === 'catalog_resource_unavailable') return ['songs','music-videos'].includes(item.type) && item.id === '' && item.url === '' && !item.downloadable && typeof item.sourceId === 'string' && item.sourceId.length > 0 && item.sourceId.length <= 512;
    if (item.type === 'library-playlists') return /^p\.[A-Za-z0-9_-]{1,510}$/.test(item.id) && item.url === `https://music.apple.com/${region.value}/library/playlist/${item.id}`;
    const kind = { songs:'song', albums:'album', playlists:'playlist', 'music-videos':'music-video' }[item.type];
    if (item.type === 'playlists' ? !/^pl\.[A-Za-z0-9_-]{1,508}$/.test(item.id) : !/^[1-9]\d{0,19}$/.test(item.id)) return false;
    return item.url === `https://music.apple.com/${region.value}/${kind}/${item.id}`;
  }
  function validate(data) {
    if (data?.schemaVersion !== 1 || data.storefront !== region.value || !Array.isArray(data.groups)
        || data.groups.some((group) => !Object.hasOwn(names, group.type) || !Array.isArray(group.items) || group.items.some((item) => !validItem(item)) || group.title !== undefined && (typeof group.title !== 'string' || group.title.length > 512) || group.recommendationId !== undefined && !/^[A-Za-z0-9_-]{1,256}$/.test(group.recommendationId))
        || data.resource && !validItem(data.resource) || data.tracks && (!Array.isArray(data.tracks) || data.tracks.some((item) => !validItem(item)))) throw new Error('invalid_catalog_receipt');
    for (const offset of [data.nextOffset, ...data.groups.map((group) => group.nextOffset)]) {
      if (offset !== null && offset !== undefined && (!Number.isSafeInteger(offset) || offset < 1 || offset > 100000)) throw new Error('invalid_catalog_receipt');
    }
    return data;
  }
  function readiness() {
    for (const control of panel.querySelectorAll('[data-catalog-download]')) control.disabled = state.pending || control.dataset.available !== 'true' || !downloadSubmissionReady() || queueNeedsRecovery();
  }
  async function queue(item) {
    if (state.pending || !validItem(item) || !item.downloadable) return;
    state.pending = true; readiness(); status.textContent = '正在加入下载队列…';
    try {
      const result = await queueCatalog(item.url);
      status.textContent = result.message;
    }
    catch { status.textContent = '加入队列失败，请检查服务状态后重试。'; }
    finally { state.pending = false; readiness(); }
  }
  function download(item, label = '下载', compact = false) {
    const text = item.downloadable ? label : '此地区不可用';
    const control = button('', () => queue(item), 'catalog-download' + (compact ? ' catalog-download-icon' : ''));
    control.setAttribute('aria-label',text); control.title = item.downloadable ? label + ' · ' + item.title : text;
    control.append(icon('download'),node('span',text,compact ? 'visually-hidden' : ''));
    control.dataset.catalogDownload = ''; control.dataset.available = String(item.downloadable); return control;
  }
  function setMode(mode) {
    ++state.generation; state.abort?.abort(); state.initialization = null;
    document.body.dataset.browseMode = mode;
    panel.hidden = mode !== 'native'; document.getElementById('browse-web-panel').hidden = mode !== 'web';
    window.dispatchEvent(new CustomEvent('amdl:browse-mode', { detail:{mode} }));
    if (mode === 'native') resume();
  }
  async function request(path, render, append = false) {
    const generation = ++state.generation;
    state.abort?.abort(); const abort = new AbortController(); state.abort = abort;
    status.textContent = '正在读取目录…';
    let timedOut = false;
    const timer = setTimeout(() => {timedOut = true; abort.abort();}, 15000);
    try {
      const receipt = await api(path, { signal:abort.signal });
      if (generation !== state.generation || !nativeVisible()) return;
      // Only account-scoped endpoints may confirm a personal storefront. A
      // late response must never change the next search's selected region.
      if (/^\/api\/v1\/catalog\/(?:home|library|library-detail|recommendation)\?/.test(path) && receipt?.feed === 'personal' && validRegion(receipt.storefront)) {
        selectRegion(receipt.storefront);state.accountStorefront=receipt.storefront;
      }
      const data = validate(receipt);
      render(data, append); state.dataLoaded = true; readiness();
      status.textContent = '';
      return true;
    } catch (error) {
      const messages = {
        catalog_auth_required:'个人歌单需要 Media-User-Token，请在“设置与状态”配置已有登录凭据。',
        catalog_auth_invalid:'账号 API 授权无效或无权访问个人内容，请在“设置与状态”更新 Media-User-Token。',
        catalog_token_unavailable:'目录 API token 获取失败，请检查网络后按 Enter 重试；无需重新输入密码。',
        catalog_token_format_changed:'未能从 Apple 公开网页识别有效目录 token，请按 Enter 重试或更新客户端；无需重新输入密码。',
        catalog_api_rejected:'Apple 目录 API 拒绝了请求，请稍后重试；这不代表 Apple 账号登录失败。',
        catalog_timeout:'目录请求超时，请检查网络后按 Enter 重试。',
        catalog_schema_invalid:'Apple 目录返回的数据不完整，未加载部分结果，请重试。',
        catalog_pagination_invalid:'目录分页异常，未加载部分结果，请刷新后重试。',
      };
      if (generation === state.generation && nativeVisible()) status.textContent = timedOut ? messages.catalog_timeout : messages[error.code] || (error.status === 404
        ? '资源不存在或后端尚不支持原生目录，请检查地区或修复运行时。'
        : '目录暂时不可用，请检查服务与网络后重试；也可打开官方网页。');
    } finally { clearTimeout(timer); }
  }
  function card(item) {
    const wrapper = node('article', undefined, 'catalog-card' + (item.type === 'songs' ? ' catalog-song-card' : ''));
    const open = button('', () => openDetail(item), 'catalog-card-open'); open.setAttribute('aria-label', '查看 ' + (item.title || '未命名资源'));
    open.append(artwork(item.artwork), node('p', item.title || '未命名资源', 'catalog-card-title'), node('p', item.artist, 'catalog-card-artist'));
    wrapper.append(open, download(item, '下载', true));
    if (item.type === 'songs') wrapper.append(playback.button(item));
    return wrapper;
  }
  function renderGroups(data, append = false, relationshipPage = false) {
    pageMode('catalog');
    detail.hidden = true; state.current = null;
    if (!append) results.replaceChildren();
    let count = 0;
    for (const group of data.groups) {
      if (!group.items.length && !group.nextOffset) continue;
      count += group.items.length;
      const section = node('section', undefined, 'catalog-group'); const grid = node('div', undefined, 'catalog-cards' + (group.type === 'songs' ? ' catalog-song-grid' : ''));
      section.append(node('h3', group.title || names[group.type])); for (const item of group.items) grid.append(card(item)); section.append(grid);
      if (group.nextOffset) section.append(button('查看更多' + names[group.type], async (event) => {
        const control = event.currentTarget; control.disabled = true;
        const completed = group.recommendationId
          ? await request('/api/v1/catalog/recommendation?' + new URLSearchParams({region:region.value,id:group.recommendationId,offset:String(group.nextOffset)}),(data,append)=>renderGroups(data,append,true),true)
          : await search(group.type, group.nextOffset, true);
        if (completed) control.remove();
        else if (control.isConnected) control.disabled = false;
      }));
      results.append(section);
    }
    if (!count && !append) results.append(node('p', state.view === 'library' ? '资料库中没有歌单。' : state.view === 'home' ? '暂无推荐内容，可使用搜索或排行榜。' : '当前地区没有匹配结果，请尝试其他关键词。', 'hint'));
    playback.sync();
    if (!relationshipPage) results.querySelector('[data-catalog-list-more]')?.remove();
    if (!relationshipPage && data.nextOffset) {
      const more = button('加载更多' + (state.view === 'library' ? '歌单' : '推荐'), async event => {
        const control = event.currentTarget;control.disabled = true;
        const completed = await feed(state.view,data.nextOffset,true);
        if (completed) control.remove();else if(control.isConnected) control.disabled = false;
      });more.dataset.catalogListMore = '';results.append(more);
    }
  }
  function detailPath(item, offset = 0) {
    return item.type === 'library-playlists'
      ? '/api/v1/catalog/library-detail?' + new URLSearchParams({region:region.value,id:item.id,offset:String(offset)})
      : '/api/v1/catalog/detail?' + new URLSearchParams({region:region.value,type:item.type,id:item.id,offset:String(offset)});
  }
  function renderDetail(data, append = false) {
    if (!append) {
      if (!data.resource) throw new Error('invalid_catalog_receipt');
      detail.replaceChildren(); results.replaceChildren(); detail.hidden = false; state.current = data.resource; pageMode('detail');
      const header = node('div', undefined, 'catalog-detail-header'); const copy = node('div', undefined, 'catalog-detail-copy');
      copy.append(node('p',names[data.resource.type],'catalog-resource-kind'),node('h2', data.resource.title), node('p', data.resource.artist,'catalog-resource-artist'), node('p', data.resource.trackCount ? `${data.resource.trackCount} 首 · ${region.value.toUpperCase()}` : region.value.toUpperCase(),'catalog-resource-meta'));
      const actions = node('div',undefined,'catalog-detail-actions');
      actions.append(download(data.resource, data.resource.type === 'albums' ? '下载整个专辑' : ['playlists','library-playlists'].includes(data.resource.type) ? '下载整个歌单' : '下载' + names[data.resource.type]));
      if (data.resource.type === 'songs') actions.append(playback.button(data.resource));
      copy.append(actions);
      header.append(artwork(data.resource.artwork), copy); detail.append(header);
      if (data.resource.type === 'albums' || data.resource.type === 'playlists' || data.resource.type === 'library-playlists') {
        const heading = node('div',undefined,'catalog-track-heading');
        heading.setAttribute('aria-hidden','true');
        heading.append(node('span','#'),node('span','歌曲'),node('span','艺人','catalog-table-artist'),node('span','时长','catalog-duration'),node('span','播放'),node('span','下载'));
        detail.append(heading);
      }
      detail.append(node('ol', undefined, 'catalog-tracks'));
    }
    const tracks = detail.querySelector('.catalog-tracks');
    for (const item of data.tracks || []) {
      const row = node('li', undefined, 'catalog-track'); const info = node('div', undefined, 'catalog-track-info');
      info.append(node('span', item.title), node('small', item.artist,'catalog-mobile-artist'));
      if (!item.downloadable) info.append(node('small',item.unavailableReason ? '没有可下载的 Apple Music 目录资源' : '此地区不可用','catalog-unavailable'));
      const seconds = Math.max(0, Math.floor((item.duration || 0) / 1000));
      row.append(node('span', item.discNumber ? `${item.discNumber}-${item.trackNumber || tracks.children.length + 1}` : String(tracks.children.length + 1), 'catalog-track-number'), info,
        node('span',item.artist,'catalog-table-artist'),
        node('span', seconds ? `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2,'0')}` : '—', 'catalog-duration'), playback.button(item), download(item,'下载',true)); tracks.append(row);
    }
    playback.sync();
    detail.querySelector('[data-catalog-more]')?.remove();
    if (data.nextOffset) {
      const more = button('加载更多曲目', async (event) => {
        const control = event.currentTarget; control.disabled = true;
        await request(detailPath(state.current, data.nextOffset), renderDetail, true);
        if (control.isConnected) control.disabled = false;
      }); more.dataset.catalogMore = ''; detail.append(more);
    }
  }
  function openDetail(item) { state.current = item; request(detailPath(item), renderDetail); }
  function feed(view = state.view, offset = 0, append = false) {
    state.view = view;state.term = '';input.value = '';pageMode('catalog');
    region.disabled = view === 'library' || view === 'home' && !!state.accountStorefront;
    if (region.disabled && state.accountStorefront) region.value = state.accountStorefront;
    document.getElementById('catalog-page-title').textContent = ({home:'主页',charts:'排行榜',library:'我的歌单'})[view];
    for (const [id,name] of [['catalog-home','home'],['catalog-charts','charts'],['catalog-library','library']]) document.getElementById(id).setAttribute('aria-pressed',String(name === view));
    const query = new URLSearchParams({region:region.value});if (view !== 'charts') query.set('offset',String(offset));
    return request('/api/v1/catalog/' + view + '?' + query,renderGroups,append);
  }
  function charts() { return feed('charts'); }
  function search(kind = state.kind, offset = 0, append = false) {
    if (!append) state.term = input.value.trim();
    if (!state.term) return feed('home');
    state.view = 'search';region.disabled = false;pageMode('catalog');
    for (const id of ['catalog-home','catalog-charts','catalog-library']) document.getElementById(id).setAttribute('aria-pressed','false');
    document.getElementById('catalog-page-title').textContent = '搜索';
    return request('/api/v1/catalog/search?' + new URLSearchParams({region:region.value,type:kind,term:state.term,offset:String(offset)}), renderGroups, append);
  }
  async function initialize(force = false) {
    if (!nativeVisible() || state.initialization !== null && !force) return;
    if (offlineShell) {
      account.textContent = '账号 API 未验证 · 服务未连接';
      status.textContent = '连接本地服务后可使用原生目录；当前可以打开官方网页浏览。'; return;
    }
    const generation = ++state.generation; state.abort?.abort(); const abort = new AbortController(); state.abort = abort;
    state.initialization = generation;
    account.textContent = '正在确认账号 API 状态…';
    const timer = setTimeout(()=>abort.abort(),15000);
    try {
      const session = await api('/api/v1/catalog/session', {signal:abort.signal});
      if (generation !== state.generation || !nativeVisible()) return;
      if (session?.schemaVersion !== 1 || !validRegion(session.storefront) || !validRegion(session.configuredStorefront)
          || typeof session.userTokenConfigured !== 'boolean' || !['public','verified','invalid','unverified'].includes(session.state)
          || session.state === 'verified' && (!session.userTokenConfigured || !validRegion(session.accountStorefront))) throw new Error('invalid_catalog_session');
      if (!state.loaded) selectRegion(session.storefront);
      account.textContent = session.state === 'verified' ? `账号 API 已验证 · ${session.accountStorefront.toUpperCase()}（不代表播放/解密许可）`
        : session.state === 'invalid' ? 'Media-User-Token 无效；仍可浏览公开目录，请到设置与状态更新凭据。'
          : session.state === 'unverified' ? '账号 API 暂未验证；可尝试公开目录，不代表账号已登录。'
            : '公开目录模式 · 未配置 Media-User-Token。';
      state.accountStorefront = session.state === 'verified' ? session.accountStorefront : '';
      state.loaded = true;
      if (state.current) request(detailPath(state.current),renderDetail);else if (state.term) search();else feed();
    } catch {
      if (generation === state.generation && nativeVisible()) { account.textContent = '账号 API 暂未验证'; status.textContent = '原生目录服务未连接或需要更新，请重试；也可打开官方网页。'; }
    } finally {
      clearTimeout(timer);
      if (state.initialization === generation) state.initialization = null;
    }
  }
  function resume() { if (!state.loaded) initialize(); else if (!state.dataLoaded) feed(); }
  document.getElementById('catalog-back').append(icon('back'));
  document.getElementById('catalog-back').addEventListener('click',() => state.term ? search() : feed());
  document.getElementById('catalog-refresh').append(icon('refresh'));
  document.getElementById('catalog-open-web').append(icon('external'));
  document.getElementById('catalog-search-form').addEventListener('submit',(event) => { event.preventDefault(); search(); });
  input.addEventListener('keydown',event=>{if(event.key==='Enter' && (event.isComposing || event.keyCode===229)) event.preventDefault();});
  document.getElementById('catalog-refresh').addEventListener('click',() => initialize(true));
  document.getElementById('catalog-home').addEventListener('click',() => feed('home'));
  document.getElementById('catalog-charts').addEventListener('click',charts);
  document.getElementById('catalog-library').addEventListener('click',() => feed('library'));
  region.addEventListener('change',() => state.term ? search() : feed());
  for (const tab of panel.querySelectorAll('[data-catalog-type]')) tab.addEventListener('click',() => {
    state.kind = tab.dataset.catalogType;
    for (const item of panel.querySelectorAll('[data-catalog-type]')) item.setAttribute('aria-pressed',String(item===tab));
    search();
  });
  document.getElementById('catalog-open-web').addEventListener('click',() => setMode('web'));
  document.getElementById('browse-return-native').addEventListener('click',() => setMode('native'));
  window.addEventListener('amdl:download-readiness',readiness);
  window.addEventListener('amdl:view-changed',() => { if (nativeVisible()) resume(); else { ++state.generation; state.abort?.abort(); state.initialization = null; } });
  requestAnimationFrame(resume);
})({ renderer: window.amdlRenderer, queueCatalog: window.amdlBrowse.queueCatalog, playback: window.catalogPlayback });

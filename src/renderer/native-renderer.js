'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const desktop = window.nativeDesktop;
  const api = (path, method = 'GET', body) => desktop.api(path, { method, body });
  const state = { view: 'browse', session: 'checking', canDownload:false, gateGeneration:0, generation: 0, feed: 'home', detail: null, browseSnapshot:null, preferences: null, pending: new Set(), playing: '', playbackGeneration: 0, playbackItem:null, previewGeneration:0, previewPromise:null };
  let collectionPlayRequest = 0, segmentPlayer = null, playbackStopBarrier = Promise.resolve();
  const releaseStream = () => { segmentPlayer?.dispose(); segmentPlayer = null; };
  const viewHistory = [], detailHistory = [];
  let searching = false, sidebarLibraryRegion = '', sidebarLibraryRequest = 0;
  const sessionNames = { checking: '正在恢复会话', needs_login: '未登录', authenticating: '正在登录', needs_2fa: '等待验证码', authenticated: '已验证登录', unverified: '会话暂未验证', runtime_unavailable: '本机服务不可用' };
  const statusNames = { queued: '排队中', running: '下载中', cancelling: '正在取消', succeeded: '完成', partial: '部分完成', failed: '失败', cancelled: '已取消', interrupted: '已中断', skipped: '已跳过', unavailable: '不可用' };
  const notice = text => { $('notice').textContent = text; };
  const node = (tag, text, className) => { const value = document.createElement(tag); if (text !== undefined) value.textContent = text; if (className) value.className = className; return value; };
  const button = (text, action, label = text) => { const value = node('button', text); value.type = 'button'; value.setAttribute('aria-label', label); value.onclick = () => Promise.resolve().then(action).catch(() => notice('操作暂未完成，请检查状态后重试。')); return value; };
  function image(url) { const value = node('img', undefined, 'artwork'); value.alt = ''; value.loading = 'lazy'; if (/^https:\/\/[^/]+\.mzstatic\.com\//.test(url || '')) value.src = desktop.artworkURL ? desktop.artworkURL(url) : url; return value; }
  function observe(session) {
    const previousSession=state.session;
    state.session = sessionNames[session?.state] ? session.state : 'runtime_unavailable';
    $('account-state').textContent = sessionNames[state.session]; if(state.session !== 'authenticated') state.canDownload=false; $('session-light').dataset.state = state.canDownload ? 'authenticated' : state.session==='authenticated'?'unverified':state.session;
    $('session-light').title = sessionNames[state.session] + ' · 打开登录与状态';
    $('sidebar-session').textContent = sessionNames[state.session];
    if (state.session === 'needs_login' && previousSession !== 'needs_login') { sidebarLibraryRegion=''; ++sidebarLibraryRequest; $('sidebar-playlists').replaceChildren(); }
    if (session?.region && /^[a-z]{2}$/.test(session.region) && !$('region').dataset.selected) {
      if (![...$('region').options].some(option => option.value === session.region)) { const option = node('option', session.region.toUpperCase()); option.value = session.region; $('region').append(option); }
      $('region').value = session.region;
    }
    if(state.session==='authenticated' && sidebarLibraryRegion!==$('region').value)void loadSidebarLibrary();
    for (const control of document.querySelectorAll('[data-download]')) control.disabled = !state.canDownload || control.dataset.available !== 'true';
    if(session?.region) void gate();
    if(state.session==='authenticated'&&previousSession!=='authenticated'){jobNameAttempts.clear();if(state.view==='tasks')void resolveJobNames();}
  }
  async function gate(){const generation=++state.gateGeneration;let ready=false;try{const caps=await api('/api/v1/capabilities');ready=state.session==='authenticated'&&caps.queue?.state==='ready'&&caps.jobExecutorConfigured===true&&caps.qualities?.[$('quality').value]?.available===true;}catch{}if(generation!==state.gateGeneration)return;state.canDownload=ready;$('session-light').dataset.state=ready?'authenticated':state.session==='authenticated'?'unverified':state.session;$('session-light').title=ready?'可提交当前音质的下载任务 · 不保证单曲最终成功':'当前尚不可提交下载 · 打开设置与状态';for(const control of document.querySelectorAll('[data-download]'))control.disabled=!ready||control.dataset.available!=='true';}
  function updateNavigation() {
    document.querySelector('.native-toolbar').hidden = state.view==='browse'&&!!state.detail;
    $('browse-back').disabled = !((state.view==='browse' && (state.detail || searching)) || viewHistory.length);
    for(const control of document.querySelectorAll('[data-feed]')) { const active=state.view==='browse'&&!searching&&control.dataset.feed===state.feed;control.classList.toggle('selected',active);if(active)control.setAttribute('aria-current','page');else control.removeAttribute('aria-current'); }
    $('page-title').textContent = state.view==='browse' ? searching?'搜索':({home:'主页',charts:'排行榜',library:'我的歌单'}[state.feed]||'主页') : {tasks:'下载任务',settings:'设置与状态'}[state.view];
    for(const control of document.querySelectorAll('.sidebar-playlist')) {if(state.view==='browse'&&state.detail?.id===control.dataset.id)control.setAttribute('aria-current','page');else control.removeAttribute('aria-current');}
  }
  function view(name, remember = true) { if(remember&&name!==state.view){viewHistory.push(state.view);if(viewHistory.length>20)viewHistory.shift();}state.view = name; for (const value of ['browse', 'tasks', 'settings']) { $(value + '-view').hidden = value !== name; for (const control of document.querySelectorAll('[data-view="' + value + '"]')) { if (value === name) control.setAttribute('aria-current', 'page'); else control.removeAttribute('aria-current'); } }updateNavigation();if (name === 'tasks') void jobs(); if (name === 'settings') void settings(); }
  async function loadSidebarLibrary(force = false) {
    if(state.session!=='authenticated')return;
    const region=$('region').value;if(!force&&sidebarLibraryRegion===region)return;sidebarLibraryRegion=region;const request=++sidebarLibraryRequest;
    try {const data=await api('/api/v1/catalog/library?'+new URLSearchParams({region,offset:'0'}));if(request!==sidebarLibraryRequest||state.session!=='authenticated'||region!==$('region').value)return;const list=$('sidebar-playlists');list.replaceChildren();for(const item of(data.groups||[]).flatMap(group=>group.items||[]).filter(item=>item.type==='library-playlists').slice(0,25)){const control=button('',()=>{view('browse');return detail(item);},'查看 '+item.title+' 详情');control.className='sidebar-playlist';control.dataset.id=item.id;control.append(image(item.artwork),node('span',item.title));list.append(control);}updateNavigation();}catch{/* Library failure never blocks the main catalog. */}
  }
  function detailPath(item, region=$('region').value) { const library=item.type==='library-playlists';return '/api/v1/catalog/'+(library?'library-detail':'detail')+'?'+new URLSearchParams({region,id:item.id,...(library?{}:{type:item.type}),offset:'0'}); }
  let prefetchActive=0;const prefetched=new Set();
  function preloadOnIntent(control, path) { if(!desktop.prefetchCatalog)return;let timer;const cancel=()=>{clearTimeout(timer);};const schedule=()=>{cancel();timer=setTimeout(()=>{const target=typeof path==='function'?path():path;if(prefetchActive>=2||prefetched.has(target)||navigator.connection?.saveData)return;prefetched.add(target);if(prefetched.size>256)prefetched.delete(prefetched.values().next().value);prefetchActive++;Promise.resolve(desktop.prefetchCatalog(target)).catch(()=>prefetched.delete(target)).finally(()=>prefetchActive--);},150);};control.addEventListener('pointerenter',schedule);control.addEventListener('focus',schedule);control.addEventListener('pointerleave',cancel);control.addEventListener('blur',cancel);control.addEventListener('click',cancel); }
  async function queue(url, title = '') {
    if (state.session !== 'authenticated') { await desktop.openLogin(); return; }
    if (!state.canDownload) {notice('下载服务或所选音质尚未就绪，请稍后重试。');return;}
    const key = url + '|' + $('quality').value + '|' + $('existing').value; if (state.pending.has(key)) return; state.pending.add(key);
    notice('正在加入下载队列…');
    try {
      let canonical = url;
      if (!/\/library\/playlists?\//.test(url)) canonical = (await api('/api/v1/browser-download/resolve', 'POST', { url })).url;
      const result = await api('/api/v1/jobs', 'POST', { urls: [canonical], ...(title ? { titles: [title] } : {}), quality: $('quality').value, existingPolicy: $('existing').value });
      if (!Array.isArray(result.jobs) || result.jobs.length !== 1 || !result.jobs[0].id || result.jobs[0].url !== canonical) throw new Error('incomplete_receipt');
      if (title) rememberJobName(canonical, title);
      notice('已加入下载队列。');
    } catch { notice('未能加入任务，请检查登录、地区与服务状态。'); }
    finally { state.pending.delete(key); }
  }
  function download(item, text = '↓') { const control = button(text, () => queue(item.url, item.title), '下载 ' + item.title); control.dataset.download = ''; control.dataset.available = String(item.downloadable && item.type !== 'music-videos'); control.disabled = !state.canDownload || control.dataset.available !== 'true'; return control; }
  function safePreview(url) { try { const value = new URL(url); return value.protocol === 'https:' && !value.username && !value.password && !value.port && !value.hash && (value.hostname.endsWith('.mzstatic.com') || value.hostname === 'audio-ssl.itunes.apple.com') ? value.href : ''; } catch { return ''; } }
  async function stopPlayback(invalidate = true) { if(invalidate) { ++state.playbackGeneration; ++collectionPlayRequest; } releaseStream(); state.playing = ''; state.playbackItem=null; $('audio').pause(); $('audio').removeAttribute('src'); $('audio').load(); $('player').hidden = true; playbackStopBarrier = Promise.all([playbackStopBarrier,api('/api/native/playback', 'DELETE').catch(() => {})]); await playbackStopBarrier; }
  function preview(item,generation){
    if(state.previewGeneration===generation)return state.previewPromise;
    state.previewGeneration=generation;
    state.previewPromise=(async()=>{if(generation!==state.playbackGeneration)return;releaseStream();const url=safePreview(item.previewUrl);if(!url){$('audio').pause();$('audio').removeAttribute('src');$('audio').load();$('player-status').textContent='完整播放与试听均不可用';return;}$('audio').dataset.mode='试听';$('audio').src=url;try{await $('audio').play();if(generation===state.playbackGeneration)$('player-status').textContent='试听 · 非完整歌曲';}catch{if(generation===state.playbackGeneration)$('player-status').textContent='试听暂不可用';}})();
    return state.previewPromise;
  }
  async function play(item) {
    ++collectionPlayRequest;
    if (state.playing === item.id && $('audio').src) { if ($('audio').paused) await $('audio').play(); else $('audio').pause(); return; }
    const generation = ++state.playbackGeneration; await stopPlayback(false); if(generation !== state.playbackGeneration) return; state.playing = item.id; state.playbackItem=item;
    $('player').hidden = false; $('player-title').textContent = item.title + (item.artist ? ' · ' + item.artist : ''); $('player-status').textContent = '正在准备完整播放…';
    let full = false;
    if (state.session === 'authenticated') {
      try {
        const result = await api('/api/native/playback', 'POST', { id: item.id, region: $('region').value });
        if (generation !== state.playbackGeneration) return;
        if (result.ok === true && result.mode === 'stream') {
          const current = () => generation === state.playbackGeneration && state.previewGeneration !== generation;
          segmentPlayer = new NativeSegmentPlayer($('audio'), result, desktop, () => { if(current())void preview(item,generation); }, text => { if(current())$('player-status').textContent=text; });
          $('audio').dataset.mode='完整播放'; await segmentPlayer.start(); full = true;
        } else if (result.ok === true && result.mode === 'full' && /^http:\/\/127\.0\.0\.1:\d+\/api\/native\/playback\/[a-f0-9]{32}$/.test(result.mediaURL)) { $('audio').dataset.mode='完整播放'; $('audio').src = result.mediaURL; await $('audio').play(); full = true; }
      } catch { /* The single play action may fall back only to a real preview. */ }
    }
    if (generation !== state.playbackGeneration) return;
    if (!full||state.previewGeneration===generation) {await preview(item,generation);return;}
    $('player-status').textContent = '完整播放';
  }
  function personalMixGroup(group, path) {
    const route = new URL(path, 'http://fixture').pathname;
    if (!['/api/v1/catalog/home', '/api/v1/catalog/recommendation'].includes(route) || group.type !== 'playlists') return false;
    return (group.items?.length > 0 && group.items.every(item => item.playlistType === 'personal-mix')) || /^(?:playlists made for you|made for you|专属推荐歌单|專屬推薦歌單|为你打造的歌单|為你打造的播放列表)$/i.test((group.title || '').trim());
  }
  async function playCollection(item, control) {
    const request = ++collectionPlayRequest, region = $('region').value;
    control.disabled = true; control.setAttribute('aria-busy', 'true');
    try {
      const data = await api('/api/v1/catalog/detail?' + new URLSearchParams({region, id:item.id, type:item.type, offset:'0'}));
      if (request !== collectionPlayRequest || region !== $('region').value) return;
      const first = (data.tracks || []).find(track => track.type === 'songs' && !track.unavailableReason);
      if (!first) { notice('此歌单暂无可播放的歌曲，请进入详情查看。'); return; }
      await play(first);
    } catch { if (request === collectionPlayRequest) notice('暂时无法播放此歌单，请进入详情重试。'); }
    finally { control.disabled = false; control.removeAttribute('aria-busy'); }
  }
  function personalMixCard(item) {
    const value = node('article', undefined, 'catalog-card personal-mix-card artwork-missing');
    const cover = button('', () => detail(item), '查看 ' + item.title + ' 详情');
    cover.className = 'catalog-artwork-action personal-mix-cover';
    preloadOnIntent(cover,()=>detailPath(item));
    const backdrop = image(item.artwork); backdrop.className = 'personal-mix-backdrop';
    const artwork = image(item.artwork); artwork.className = 'personal-mix-artwork';
    artwork.onload = () => value.classList.remove('artwork-missing');
    artwork.onerror = () => value.classList.add('artwork-missing');
    cover.append(backdrop, artwork, node('h3', item.title, 'personal-mix-title'));
    const summary = node('p', item.description || item.artist || 'Apple Music 为你精选', 'personal-mix-summary');
    const playButton = button('▶', () => playCollection(item, playButton), '播放 ' + item.title);
    playButton.className = 'personal-mix-play';
    value.append(cover, summary, playButton);
    return value;
  }
  function card(item, personalMix = false, related = false) {
    if (personalMix && item.type === 'playlists') return personalMixCard(item);
    const value = node('article', undefined, item.type==='artists'?'catalog-card artist-card':'catalog-card');
    {
      const label = '查看 ' + item.title + ' 详情';
      const cover = button('', () => detail(item), label); cover.className = 'catalog-artwork-action'; cover.append(image(item.artwork));
      const title = node('h3'); const link = button(item.title, () => detail(item), label); link.className = 'catalog-title-action'; title.append(link);
      preloadOnIntent(cover,()=>detailPath(item));preloadOnIntent(link,()=>detailPath(item));
      value.append(cover, title);
    }
    value.append(node('p',item.type==='artists'?'艺人':related&&/^\d{4}/.test(item.releaseDate||'')?item.releaseDate.slice(0,4)+'年':item.artist));
    if (item.type === 'songs') {
      const actions = node('div', undefined, 'card-actions');
      actions.append(button('▶', () => play(item), '播放 ' + item.title), download(item));
      value.append(actions);
    }
    return value;
  }
  function track(item, index) { const row = node('div', undefined, 'track-row'); row.append(node('span', String(index + 1), 'muted'), node('span', item.title), node('span', item.artist, 'muted artist'), node('span', item.duration ? Math.floor(item.duration / 60000) + ':' + String(Math.floor(item.duration / 1000) % 60).padStart(2, '0') : '', 'muted duration'), button('▶', () => play(item), '播放 ' + item.title), download(item)); return row; }
  function searchSongRow(item, subtitle=item.artist) {
    const row=node('article',undefined,'search-song-row');row.setAttribute('role','listitem');
    const label='查看 '+item.title+' 详情';
    const cover=button('',()=>detail(item),label);cover.className='catalog-artwork-action search-song-cover';cover.append(image(item.artwork));
    const title=node('h3');const link=button(item.title,()=>detail(item),label);link.className='catalog-title-action';link.title=item.title;title.append(link);
    const copy=node('div',undefined,'search-song-copy');const mobileArtist=node('p',subtitle,'search-song-mobile-artist');copy.append(title,mobileArtist);
    const artist=node('span',item.artist,'search-song-artist');artist.title=item.artist||'';
    const duration=node('span',item.duration>0?Math.floor(item.duration/60000)+':'+String(Math.floor(item.duration/1000)%60).padStart(2,'0'):'—','search-song-duration');
    const playButton=button('▶',()=>play(item),'播放 '+item.title);playButton.className='search-song-play';
    const downloadButton=download(item);downloadButton.className='search-song-download';
    preloadOnIntent(cover,()=>detailPath(item));preloadOnIntent(link,()=>detailPath(item));
    row.append(cover,copy,artist,duration,playButton,downloadButton);return row;
  }
  function searchBestCard(item){
    const control=button('',()=>detail(item),'查看最佳结果 '+item.title+' 详情');control.className='search-best-card'+(item.type==='artists'?' artist-result':'');
    const artwork=image(item.artwork);const copy=node('span',undefined,'search-best-copy');
    const type={artists:'艺人',albums:'专辑',songs:'歌曲',playlists:'歌单','music-videos':'音乐视频'}[item.type]||'作品';
    copy.append(node('strong',item.title),node('span',type+(item.type!=='artists'&&item.artist?' · '+item.artist:'')));
    control.title=item.title;control.append(artwork,copy);preloadOnIntent(control,()=>detailPath(item));return control;
  }
  function searchGroups(data){
    const order=['albums','songs','artists','playlists','music-videos'];
    const priority=kind=>kind==='artists'&&data.bestResults?.[0]?.type==='artists'?-1:order.indexOf(kind)<0?99:order.indexOf(kind);
    return [...(data.groups||[])].sort((a,b)=>priority(a.type)-priority(b.type));
  }
  function detailArtist(item){
    const line=node('p',undefined,'detail-artist');
    const valid=ref=>/^[1-9][0-9]{0,19}$/.test(ref.id||'');
    const refs=(item.artists||[]).filter(valid);
    if(!refs.length&&valid({id:item.artistId}))refs.push({id:item.artistId,name:item.relatedArtist||''});
    if(!refs.length){line.textContent=item.artist||'';return line;}
    if(!refs.some(ref=>ref.name)){line.append(document.createTextNode((item.artist||'')+' '));}
    refs.forEach((ref,index)=>{if(index)line.append(document.createTextNode('、'));const artist={id:ref.id,type:'artists',title:ref.name||'相关艺人'};const link=button(artist.title,()=>detail(artist),'查看艺人 '+artist.title+' 主页');link.className='detail-artist-link';preloadOnIntent(link,()=>detailPath(artist));line.append(link);});
    return line;
  }
  function artistHome(content,data){
    const item=data.resource,home=data.artistHome||{},songs=(home.topSongs||[]).slice(0,9),latest=home.latestRelease;
    const hero=node('section',undefined,'detail-hero artist-hero');const portrait=node('div',undefined,'artist-portrait');portrait.append(image(item.artwork));
    const title=node('div',undefined,'artist-title-row');const first=songs.find(song=>!song.unavailableReason);let playButton;
    playButton=button('▶',()=>first?play(first):playCollection(latest,playButton),'播放 '+item.title);playButton.className='artist-play';playButton.disabled=!first&&!latest;
    title.append(playButton,node('h1',item.title));hero.append(portrait,title);content.append(hero);
    if(latest||songs.length){
      const overview=node('div',undefined,'artist-overview');if(!latest)overview.classList.add('no-latest');
      if(latest){const section=node('section',undefined,'artist-latest');section.setAttribute('aria-label','最新发布');section.append(node('h2','最新发布'));const release=node('div',undefined,'artist-release-card');const cover=button('',()=>detail(latest),'查看 '+latest.title+' 详情');cover.className='catalog-artwork-action';cover.append(image(latest.artwork));const copy=node('div',undefined,'artist-release-copy');
        if(/^\d{4}-\d{2}-\d{2}/.test(latest.releaseDate||'')){const date=new Date(latest.releaseDate.slice(0,10)+'T00:00:00Z');if(!Number.isNaN(date.getTime()))copy.append(node('p',new Intl.DateTimeFormat('zh-CN',{year:'numeric',month:'long',day:'numeric',timeZone:'UTC'}).format(date),'artist-release-date'));}
        const heading=node('h3');const link=button(latest.title,()=>detail(latest),'查看 '+latest.title+' 详情');link.className='catalog-title-action';heading.append(link);copy.append(heading);if(latest.trackCount)copy.append(node('p',latest.trackCount+' 首歌曲'));copy.append(download(latest,'↓ 下载'));preloadOnIntent(cover,()=>detailPath(latest));preloadOnIntent(link,()=>detailPath(latest));release.append(cover,copy);section.append(release);overview.append(section);}
      if(songs.length){const section=node('section',undefined,'artist-top-songs');section.setAttribute('aria-label','歌曲排行');section.append(node('h2','歌曲排行'));const list=node('div',undefined,'search-song-list');list.setAttribute('role','list');list.setAttribute('aria-label','歌曲排行');for(const song of songs)list.append(searchSongRow(song,[song.albumTitle,/^\d{4}/.test(song.releaseDate||'')?song.releaseDate.slice(0,4)+'年':''].filter(Boolean).join(' · ')||song.artist));section.append(list);overview.append(section);}
      content.append(overview);
    }
  }
  function detailTraits(item) {const parts=[...(item.genres||[]).slice(0,1)];if(/^\d{4}/.test(item.releaseDate||''))parts.push(item.releaseDate.slice(0,4)+'年');const variants=item.audioVariants||[];if(variants.includes('hi-res-lossless'))parts.push('高解析度无损');else if(variants.includes('lossless'))parts.push('无损');if(variants.includes('dolby-atmos'))parts.push('Dolby Atmos');if(item.contentRating==='explicit')parts.push('E');return parts.join(' · ');}
  function drawDetailMetadata(content) { const item=content.querySelector('.detail-hero')?.resource;if(!item)return;const lines=[];if(/^\d{4}-\d{2}-\d{2}/.test(item.releaseDate||'')){const date=new Date(item.releaseDate.slice(0,10)+'T00:00:00Z');if(!Number.isNaN(date.getTime()))lines.push(new Intl.DateTimeFormat('zh-CN',{year:'numeric',month:'long',day:'numeric',timeZone:'UTC'}).format(date));}const list=content.querySelector('.track-list');const count=list.childElementCount,total=item.trackCount||count;const minutes=Math.floor([...list.children].reduce((sum,row)=>sum+Number(row.dataset.duration||0),0)/60000);if(total)lines.push(total+' 首歌曲'+(count>=total&&minutes?' · '+minutes+' 分钟':''));if(item.copyright)lines.push(item.copyright);if(item.recordLabel&&!(item.copyright||'').includes(item.recordLabel))lines.push(item.recordLabel);content.querySelector('.detail-metadata').textContent=lines.join('\n'); }
  async function relatedWorks(item, slot, generation) {
    if(!item.artistId){slot.hidden=true;return;}slot.hidden=false;const title='更多'+(item.relatedArtist||item.artist||'同音乐人')+'的作品';
    try{const data=await api('/api/v1/catalog/artist-albums?'+new URLSearchParams({region:$('region').value,id:item.artistId,offset:'0'}));if(generation!==state.generation||!slot.isConnected)return;const seen=new Set([item.id]),items=(data.groups||[]).flatMap(group=>group.items||[]).filter(value=>{if(value.type!=='albums'||seen.has(value.id))return false;seen.add(value.id);return true;}).slice(0,12);slot.replaceChildren();if(!items.length){slot.hidden=true;return;}slot.append(node('h2',title));const grid=node('div',undefined,'catalog-grid related-grid');for(const value of items)grid.append(card(value,false,true));slot.append(grid);}catch{if(generation!==state.generation||!slot.isConnected)return;slot.replaceChildren(node('h2',title));const feedback=node('div',undefined,'related-feedback');feedback.append(node('span','其他作品暂时无法载入'),button('重试',()=>relatedWorks(item,slot,generation)));slot.append(feedback);}
  }
  async function browse(path, append = false) {
    if(!append)state.catalogPath=path;
    const generation = ++state.generation; notice('正在读取目录…');
    try {
      const data = await api(path); if (generation !== state.generation) return;
      const content = $('catalog-content'); if (!append) content.replaceChildren();
      const searchPage=new URL(path,'http://fixture').pathname==='/api/v1/catalog/search';
      if (data.resource&&data.resource.type!=='artists') {
        if (!append) { const hero = node('div', undefined, 'detail-hero');hero.resource=data.resource; const info = node('div',undefined,'detail-info'); info.append(node('h1', data.resource.title), detailArtist(data.resource),node('p',detailTraits(data.resource),'detail-traits')); const actions = node('div', undefined, 'card-actions'); if (data.tracks?.length) actions.append(button('▶ 播放', () => play(data.tracks.find(item=>!item.unavailableReason)||data.tracks[0]))); actions.append(download(data.resource, '下载全部')); info.append(actions); hero.append(image(data.resource.artwork), info);const related=node('section',undefined,'detail-related');related.hidden=true;content.append(hero, node('div', undefined, 'track-list'),node('p','','detail-metadata'),related);void relatedWorks(data.resource,related,generation); }
        const list = content.querySelector('.track-list'); const start = list.childElementCount; for (const [index, item] of (data.tracks || []).entries()){const row=track(item,start+index);row.dataset.duration=String(Math.max(0,item.duration||0));list.append(row);}drawDetailMetadata(content);
      } else {
        if(data.resource?.type==='artists'&&!append)artistHome(content,data);
        if(searchPage&&!append&&data.bestResults?.length){const section=node('section',undefined,'search-best');section.setAttribute('aria-label','最佳结果');section.append(node('h2','最佳结果'));const grid=node('div',undefined,'search-best-grid');for(const item of data.bestResults.slice(0,8))grid.append(searchBestCard(item));section.append(grid);content.append(section);}
        for (const group of searchPage?searchGroups(data):data.groups||[]) {
        const personalMix = personalMixGroup(group, path);
        const songSearch=searchPage&&group.type==='songs';
        if(searchPage&&!group.items?.length)continue;
        let grid=append&&searchPage?[...content.querySelectorAll('[data-search-kind]')].find(value=>value.dataset.searchKind===group.type):null;
        if(!grid){
          const title=personalMix ? '专属推荐歌单' : group.title || ({ songs: '歌曲', artists:'艺人', albums: '专辑', playlists: '歌单', 'library-playlists': '我的歌单' }[group.type] || '推荐');
          content.append(node('h2',title,personalMix?'personal-mixes-heading':undefined));
          const artistPage=state.detail?.type==='artists';
          grid=node('div',undefined,songSearch?'search-song-list':personalMix?'catalog-grid personal-mixes':searchPage||artistPage?'catalog-grid search-collection-grid'+(group.type==='artists'?' search-artists':''):'catalog-grid');
          if(searchPage)grid.dataset.searchKind=group.type;
          if(songSearch){grid.setAttribute('role','list');grid.setAttribute('aria-label',title);}
          if(personalMix){grid.tabIndex=0;grid.setAttribute('role','region');grid.setAttribute('aria-label','专属推荐歌单');}
          content.append(grid);
        }
        for (const item of group.items || []) grid.append(songSearch?searchSongRow(item):card(item,personalMix));
        if(Number.isInteger(group.nextOffset)&&!Number.isInteger(data.nextOffset)){
          const next=new URL(path,'http://fixture');next.searchParams.set('offset',String(group.nextOffset));
          if(group.recommendationId){next.pathname='/api/v1/catalog/recommendation';next.searchParams.delete('term');next.searchParams.delete('type');next.searchParams.set('id',group.recommendationId);}
          else if(next.pathname.endsWith('/search'))next.searchParams.set('type',group.type);
          const more=button('加载更多',async()=>{more.disabled=true;const requestGeneration=state.generation+1;const loaded=await browse(next.pathname+next.search,true);if(requestGeneration!==state.generation)return;if(loaded)more.remove();else more.disabled=false;},'加载更多 '+(group.title||group.type));more.dataset.catalogPage='';content.append(more);
        }
        }
      }
      const next = data.nextOffset; $('catalog-more').hidden = !Number.isInteger(next); $('catalog-more').onclick = () => { const url = new URL(path, 'http://fixture'); url.searchParams.set('offset', String(next)); void browse(url.pathname + url.search, true); };
      notice((data.resource||data.tracks?.length || data.groups?.some(group => group.items?.length)) ? '' : '暂无内容。'); observe({ state: state.session }); return true;
    } catch { if (generation === state.generation) notice('目录暂不可用，请检查登录与网络后重试。'); return false; }
  }
  function discardDetail() { state.detail = null; state.browseSnapshot = null;detailHistory.length=0;updateNavigation(); }
  function feed(mode) { searching=false;discardDetail(); state.feed = mode;updateNavigation(); return browse('/api/v1/catalog/' + mode + '?' + new URLSearchParams({ region: $('region').value, ...(mode === 'charts' ? {} : {offset: '0'}) })); }
  function takeSnapshot(){const content=document.createDocumentFragment();const scrolls=[...$('catalog-content').querySelectorAll('.catalog-grid,.search-song-list')].map(element=>({element,x:element.scrollLeft,y:element.scrollTop}));const snapshot={content,scrolls,detail:state.detail,region:$('region').value,moreHidden:$('catalog-more').hidden,moreAction:$('catalog-more').onclick,notice:$('notice').textContent,trigger:document.activeElement,x:window.scrollX,y:window.scrollY};content.append(...$('catalog-content').childNodes);return snapshot;}
  function detail(item, replace=false) {
    const entering = !state.detail;
    if (entering) {
      state.browseSnapshot = takeSnapshot();
    } else if(!replace&&(state.detail.id!==item.id||state.detail.type!==item.type)){detailHistory.push(takeSnapshot());if(detailHistory.length>10)detailHistory.shift();}
    state.detail = item;updateNavigation(); $('catalog-more').hidden = true; $('catalog-more').onclick = null;
    if (entering||!replace) { $('browse-back').focus({preventScroll:true}); window.scrollTo(0, 0); }
    return browse(detailPath(item));
  }
  function closeDetail() {
    if (!state.detail) return;
    ++state.generation;
    const snapshot = detailHistory.length?detailHistory.pop():state.browseSnapshot;
    if(snapshot?.detail)state.detail=snapshot.detail;else discardDetail();updateNavigation();
    if (!snapshot || snapshot.region !== $('region').value) { void feed(state.feed); return; }
    $('catalog-content').replaceChildren(snapshot.content); $('catalog-more').hidden = snapshot.moreHidden; $('catalog-more').onclick = snapshot.moreAction;
    for(const scroll of snapshot.scrolls||[]){scroll.element.scrollLeft=scroll.x;scroll.element.scrollTop=scroll.y;}
    for (const control of document.querySelectorAll('[data-catalog-page]')) control.disabled = false;
    notice(snapshot.notice === '正在读取目录…' ? '' : snapshot.notice); observe({state:state.session});
    if (snapshot.trigger?.isConnected) snapshot.trigger.focus({preventScroll:true});
    window.scrollTo(snapshot.x, snapshot.y);
    const hero=$('catalog-content').querySelector('.detail-hero'),related=$('catalog-content').querySelector('.detail-related');if(hero?.resource?.artistId&&related&&!related.childElementCount)void relatedWorks(hero.resource,related,state.generation);
  }
  function goBack(){if(state.view==='browse'&&state.detail){closeDetail();return;}if(viewHistory.length){view(viewHistory.pop(),false);return;}if(searching)void feed(state.feed);}
  const jobCards = new Map(), jobOperations = new Map(), removedJobs = new Set();
  const jobNames = new Map(), jobNameAttempts = new Set(); let resolvingJobName = false;
  try { for (const [url,title] of Object.entries(JSON.parse(localStorage.getItem('native-v2-job-names') || '{}'))) if (typeof title === 'string') jobNames.set(url,title.slice(0,300)); } catch {}
  function rememberJobName(url, title) {
    if (typeof title !== 'string' || !title.trim()) return;
    jobNames.set(url,title.trim().slice(0,300));
    while(jobNames.size>256)jobNames.delete(jobNames.keys().next().value);
    try { localStorage.setItem('native-v2-job-names',JSON.stringify(Object.fromEntries(jobNames))); } catch {}
  }
  function jobResource(job) {
    try {
      const url = new URL(job.url), parts = url.pathname.split('/').filter(Boolean), selected = url.searchParams.get('i');
      const library = parts[1] === 'library';
      const type = selected ? 'songs' : library ? 'library-playlists' : ({album:'albums',song:'songs',playlist:'playlists','music-video':'music-videos'})[parts[1]];
      const id = selected || parts.at(-1);
      if (type && /^(?:\d+|(?:pl\.|p\.)[a-zA-Z0-9.-]+)$/.test(id) && /^[a-z]{2}$/i.test(parts[0])) return {region:parts[0],id,type,library};
    } catch {}
    return null;
  }
  function jobHeading(job) {
    if(job.title)return job.title;
    if(jobNames.has(job.url))return jobNames.get(job.url);
    const resource = jobResource(job);
    if(resource?.type==='songs'){const item=job.items?.find(item=>item.sourceId===resource.id);if(item?.name)return item.name;}
    const kind = resource ? {albums:'专辑',songs:'歌曲',playlists:'歌单','library-playlists':'私人歌单','music-videos':'音乐视频'}[resource.type] : '下载任务';
    return kind + (resource ? ' · ' + resource.id : '');
  }
  async function resolveJobNames() {
    if(resolvingJobName)return;resolvingJobName=true;
    try {
      while(state.view==='tasks'){
        const entry=[...jobCards.values()].find(card=>!card.job.title&&!jobNames.has(card.job.url)&&!jobNameAttempts.has(card.job.url)&&jobResource(card.job));
        if(!entry)break;
        const job=entry.job,resource=jobResource(job);jobNameAttempts.add(job.url);
        try {
          const result=await api('/api/v1/catalog/'+(resource.library?'library-detail':'detail')+'?'+new URLSearchParams({region:resource.region,id:resource.id,...(resource.library?{}:{type:resource.type}),offset:'0'}));
          if(result.resource?.title){rememberJobName(job.url,result.resource.title);for(const card of jobCards.values())if(card.job.url===job.url)card.querySelector('h3').textContent=jobHeading(card.job);}
        } catch { /* Do not block deletion or repeatedly retry catalog on every poll. */ }
      }
    } finally {resolvingJobName=false;}
  }
  const downloadFailures = {
    license_failed:'解密许可获取失败。请重新验证会话，并检查该资源的地区与订阅权限。',
    session_rejected:'授权被拒绝（HTTP 401）。请到设置中重新验证或登录。',
    resource_forbidden:'资源访问被拒绝（HTTP 403）。请检查账号地区、订阅和资源权限。',
    resource_not_found:'当前地区找不到此资源（HTTP 404）。请切换下载地区或选择对应版本。',
    catalog_token_failed:'目录 Token 获取失败。请检查服务与网络后重试，无需反复输入验证码。',
    disk_full:'下载盘空间不足。请清理空间或更换下载位置后重试。',
    permission_denied:'无法写入文件。请检查下载目录权限、只读属性和文件占用。',
    decryption_failed:'媒体解密失败。请导出诊断，并检查解密服务后重试。',
    download_result_missing:'下载结果未确认完整。请检查文件并重试失败项，不等同于文件一定不存在。',
    credentials_failed:'下载凭据不可用。请到设置中检查登录与服务状态。',
    metadata_failed:'目录元数据解析失败。请检查地区和网络后重试。',
    manifest_failed:'媒体地址解析失败。请检查地区、资源可用性和网络。',
    manifest_queue_timeout_transient:'媒体地址请求排队超时，请稍后重试。',
    manifest_empty_transient:'媒体服务暂未返回播放地址，已自动重试，请稍后再试。',
    manifest_response_invalid:'媒体地址服务响应异常，请重试或导出诊断。',
    quality_unavailable:'这首歌曲暂不提供所选音质，请尝试其他音质。',
    media_failed:'媒体传输或解密失败，现有记录未提供更细原因。请导出诊断后重试。',
    output_failed:'无法准备输出文件。请检查下载位置、空间与文件占用。',
    lyrics_failed:'歌词文件写入失败。请检查下载目录权限与歌词设置。',
    tagging_failed:'音频标签写入失败。请检查文件占用和元数据设置。',
    muxing_failed:'兼容音频封装失败，文件未发布。请导出诊断查看原因，勿将临时文件视为下载完成。',
    publish_failed:'文件保存或重命名失败。请检查文件占用与目录权限。',
    validation_failed:'音频完整性校验失败。请重试此曲，勿将未校验文件视为成功。',
    track_unavailable:'此曲在当前地区没有可下载版本。',
    playlist_access_denied:'无权访问此歌单。请检查账号与歌单权限。',
    playlist_auth_required:'私人歌单需要有效登录。请重新验证会话。',
    playlist_not_found:'歌单不存在，或在当前地区不可用。',
    playlist_pagination_failed:'歌单曲目未能完整获取。请检查网络后重试。'
  };
  function downloadFailure(value) {
    const code=value.errorCode||'',message=String(value.errorMessage||'');
    if(downloadFailures[code])return downloadFailures[code];
    const stage={metadata:'目录解析',manifest:'媒体地址解析',media:'媒体传输'}[code.split('_')[0]]||'下载';
    if(/_timeout_transient$/.test(code))return stage+'超时。请检查网络或本机服务后重试。';
    if(/_dns_transient$/.test(code))return stage+'无法解析服务器地址。请检查 DNS 或代理设置。';
    if(/_connection_transient$/.test(code))return stage+'连接被拒绝或中断。请检查服务与网络。';
    if(/_transient$/.test(code))return /HTTP 429/.test(message)?'上游请求过于频繁（HTTP 429）。请稍后重试。':/HTTP 5xx/.test(message)?'上游服务暂不可用（HTTP 5xx）。请稍后重试。':stage+'遇到临时网络或上游错误，请稍后重试。';
    return message ? '失败原因：'+message.slice(0,400) : '未获得明确失败原因，请导出诊断以便排查。';
  }
  function bytes(value) {const units=['B','KiB','MiB','GiB'];let unit=0;while(value>=1024&&unit<3){value/=1024;unit++;}return value.toFixed(unit?1:0)+' '+units[unit];}
  const jobProgressMemory=new Map();
  function freshTransfer(value){return value&&Number.isFinite(value.at)&&Date.now()-value.at<=4000&&value.at-Date.now()<=2000;}
  function activeTransferItems(job){
    const terminal=new Set((job.items||[]).filter(item=>['succeeded','failed','skipped','unavailable','cancelled','interrupted'].includes(item.status)).map(item=>item.sourceId));
    const seen=new Set();return (Array.isArray(job.transfer?.items)?job.transfer.items:[]).filter(item=>{
      if(typeof item.sourceId!=='string'||seen.has(item.sourceId)||terminal.has(item.sourceId)||!Number.isFinite(item.progress)||item.progress<0||item.progress>.99)return false;
      seen.add(item.sourceId);return true;
    });
  }
  function plannedTransferTotals(job){
    const t=job.transfer?.totals,valid=value=>Number.isSafeInteger(value)&&value>=0;
    if(!freshTransfer(job.transfer)||!t||!valid(t.items)||t.items<1||t.items>100000||!valid(t.unknownItems)||t.unknownItems>t.items||
      !valid(t.bytes)||!valid(t.totalBytes)||!valid(t.processedBytes)||t.bytes>t.totalBytes||t.processedBytes>t.totalBytes||
      typeof t.estimated!=='boolean'||(t.unknownItems>0&&!t.estimated)||!Number.isFinite(t.progress)||t.progress<0||t.progress>.99)return null;
    return t;
  }
  function jobProgress(job){
    const budget=plannedTransferTotals(job);
    const total=Math.max(Number.isFinite(job.total)?Math.max(0,job.total):0,budget?.items||0);
    const done=Math.min(total,['succeeded','failed','skipped','unavailable'].reduce((sum,key)=>sum+(Number.isFinite(job[key])?Math.max(0,job[key]):0),0));
    const active=['running','cancelling'].includes(job.status),fresh=freshTransfer(job.transfer),items=fresh?activeTransferItems(job):[];
    const identity=String(job.attempt||0)+'/'+String(job.startedAt||'')+'/'+total;
    let previous=jobProgressMemory.get(job.id);if(previous?.identity!==identity)previous=null;
    let value=done+items.reduce((sum,item)=>sum+item.progress,0),weighted=!!budget||!!items.length||!!previous?.weighted;
    // A selective retry plan describes this attempt, not prior completed tracks.
    // Retain the existing track-unit fallback when those scopes differ.
    if(active&&budget&&budget.items===total&&budget.totalBytes>0&&budget.unknownItems===0)value=total*budget.progress;
    if(active){value=Math.min(value,total*.99);if(fresh&&previous)value=Math.max(value,Math.min(previous.value,total*.99));}
    value=Math.max(0,Math.min(total,value));
    const unknown=budget?budget.unknownItems>0:items.some(item=>!(Number.isFinite(item.totalBytes)&&item.totalBytes>0)&&item.progress===0);
    const known=total>0&&!unknown&&!(active&&weighted&&!fresh);
    if(!active)jobProgressMemory.delete(job.id);else if(fresh){jobProgressMemory.set(job.id,{identity,value,weighted});if(jobProgressMemory.size>256)jobProgressMemory.delete(jobProgressMemory.keys().next().value);}
    return {total,done,value,known,weighted,percent:total?(value/total*100).toFixed(1).replace(/\.0$/,''):''};
  }
  function applyJobProgress(progress,p){
    progress.setAttribute('aria-label','下载与处理进度');progress.max=p.total||1;if(p.known)progress.value=p.value;else progress.removeAttribute('value');
    progress.setAttribute('aria-valuetext',p.known?(p.weighted?'预估进度 '+p.percent+'%，':'')+'已处理 '+p.done+' 首，共 '+p.total+' 首':'等待有效进度信息');
  }
  function transferAmount(job){
    const budget=plannedTransferTotals(job);
    if(budget)return '已下载 '+bytes(budget.bytes)+(budget.unknownItems>0?' · 总量待确认':' / '+(budget.estimated?'预计总量 ':'总量 ')+bytes(budget.totalBytes));
    const items=activeTransferItems(job);if(!items.length)return '';
    const valid=value=>Number.isSafeInteger(value)&&value>=0;
    if(items.some(item=>!valid(item.bytes)||!valid(item.totalBytes)))return '';
    const received=items.reduce((sum,item)=>sum+item.bytes,0),total=items.reduce((sum,item)=>sum+item.totalBytes,0);
    if(!valid(received)||!valid(total))return '';
    return '当前已接收 '+bytes(received)+(items.some(item=>item.totalBytes===0)?' · 总量待确认':' / '+bytes(total));
  }
  let monitorRequest=null,monitorJobs=[],monitorAt=0,monitorFailed=false;
  function drawMonitor(){
    const progress=$('monitor-progress'),title=$('monitor-title'),detail=$('monitor-detail'),speed=$('monitor-speed');
    const current=monitorJobs.find(job=>job.status==='running')||monitorJobs.find(job=>job.status==='cancelling')||monitorJobs[0];
    const stale=!monitorAt||Date.now()-monitorAt>4500;
    $('sidebar-job-count').hidden=!monitorJobs.length;$('sidebar-job-count').textContent=String(monitorJobs.length)+(monitorJobs.length===100?'+':'');
    if(stale||monitorFailed){title.textContent='下载状态暂未更新';detail.textContent=monitorFailed?'连接中断，正在重试…':'等待下载服务响应…';speed.textContent='—';progress.removeAttribute('value');progress.setAttribute('aria-valuetext','状态暂不可用');return;}
    if(!current){title.textContent='暂无下载任务';detail.textContent='当前没有活动下载';speed.textContent='0 B/s';progress.max=1;progress.value=0;progress.setAttribute('aria-valuetext','暂无活动下载');return;}
    title.textContent=jobHeading(current);
    const fresh=monitorJobs.filter(job=>['running','cancelling'].includes(job.status)&&job.transfer&&Number.isFinite(job.transfer.at)&&Date.now()-job.transfer.at<=4000&&job.transfer.at-Date.now()<=2000);
    const downloading=fresh.filter(job=>job.transfer.phase==='downloading');
    const bps=downloading.reduce((sum,job)=>sum+(Number.isFinite(job.transfer.bytesPerSecond)?Math.max(0,job.transfer.bytesPerSecond):0),0);
    speed.textContent=downloading.length?bytes(bps)+'/s':'—';
    const p=jobProgress(current),{total,done}=p;
    const phase=current.status==='queued'?'排队中':current.status==='cancelling'?'正在取消':fresh.find(job=>job.id===current.id)?({preparing:'正在解析',downloading:'正在下载',decrypting:'正在解密',tagging:'正在写入标签',publish:'正在保存',processing:'正在处理'}[current.transfer?.phase]||'正在处理'):'等待传输状态';
    const amount=plannedTransferTotals(current)?transferAmount(current):'';
    detail.textContent=phase+(p.weighted&&p.known?' · 预估 '+p.percent+'%':'')+(amount?' · '+amount:total?' · 已处理 '+done+' / '+total+' 首':' · 正在解析曲目')+(monitorJobs.length>1?' · '+monitorJobs.length+(monitorJobs.length===100?'+':'')+' 项活动任务':'');
    applyJobProgress(progress,p);
  }
  function refreshMonitor(){if(monitorRequest)return monitorRequest;monitorRequest=(async()=>{try{const result=await api('/api/v1/jobs?active=true&limit=100');if(!Array.isArray(result.jobs))throw new Error('invalid_jobs');monitorJobs=result.jobs.filter(job=>['queued','running','cancelling'].includes(job.status)&&!removedJobs.has(job.id));monitorAt=Date.now();monitorFailed=false;}catch{monitorFailed=true;}drawMonitor();})().finally(()=>monitorRequest=null);return monitorRequest;}
  function drawJobTransfer(card) {
    const job=card.job,field=card.querySelector('.job-transfer'),value=job.transfer;
    const p=jobProgress(job);applyJobProgress(card.querySelector('progress'),p);
    if(!['running','cancelling'].includes(job.status)){field.textContent='';return;}
    if(!freshTransfer(value)||!Number.isFinite(value.bytesPerSecond)||!Number.isFinite(value.bytes)){field.textContent='等待传输状态…';return;}
    const phase={preparing:'正在解析',downloading:'正在下载',processing:'正在处理',decrypting:'正在解密',tagging:'正在写入标签',publish:'正在保存'}[value.phase]||'正在处理';
    field.textContent=phase+(p.weighted&&p.known?' · 预估 '+p.percent+'%':'')+(value.phase==='downloading'?' · '+bytes(Math.max(0,value.bytesPerSecond))+'/s':'')+' · '+(transferAmount(job)||'本次已接收 '+bytes(Math.max(0,value.bytes)));
  }
  let jobRefresh = null, jobListVersion = 0, jobListFailed = false, removalAcknowledged = false;
  const jobOperationErrors = {
    output_busy: '其他下载或文件删除正在进行。请稍后重试，或选择“仅删除任务”保留文件。',
    job_deleting: '此任务正在删除，请等待完成后刷新列表。',
    file_in_use: '文件正在被其他程序使用。请关闭播放器等程序后重试；任务记录已保留。',
    file_access_denied: '没有权限删除文件，或文件为只读。请检查权限，或选择“仅删除任务”。',
    file_changed: '文件已被修改，为避免误删已保留。可选择“仅删除任务”保留文件。',
    file_delete_failed: '部分文件未能删除，任务记录已保留。请检查文件占用和权限后重试，也可仅删除任务。',
    job_active: '任务仍在运行，请先取消，等待结束后再删除。',
    service_maintenance: '服务正在停止或更新，请稍后重试。'
  };
  function jobError(error) {
    const message = String(error?.message || error || '');
    for (const [code, text] of Object.entries(jobOperationErrors)) if (new RegExp('\\b' + code + '\\b').test(message)) return text;
    if (/abort|timeout|timed out/i.test(message)) return '请求超时，结果尚未确认。正在刷新任务状态，请确认后再重试。';
    return '操作未完成，任务记录未确认删除。请检查本机服务后重试。';
  }
  function syncJobOperation(card) {
    const operation = jobOperations.get(card.dataset.jobId);
    card.setAttribute('aria-busy', String(!!operation));
    for (const control of card.querySelectorAll('[data-job-action]')) {
      control.disabled = !!operation;
      control.textContent = operation?.action === control.dataset.jobAction ? (operation.deleting ? '正在删除…' : '正在处理…') : control.dataset.label;
      control.setAttribute('aria-label', control.textContent);
    }
  }
  async function refreshJobsAfterMutation() {
    if (jobRefresh) await jobRefresh;
    if (state.view === 'tasks') await jobs();
  }
  async function jobAction(id, action) {
    if (jobOperations.has(id) || removedJobs.has(id)) return;
    const deleting = action === 'record' || action === 'files';
    if (action === 'files' && !confirm('删除此任务及其拥有的下载文件？')) return;
    const card = jobCards.get(id);
    if (!card) return;
    jobOperations.set(id, { action, deleting }); ++jobListVersion; removalAcknowledged = false;
    const feedback = card.querySelector('.job-feedback');
    feedback.textContent = deleting ? '正在删除，请稍候…' : '正在处理…'; feedback.classList.remove('error');
    syncJobOperation(card);
    const suffix = { record: '/record', files: '', cancel: '/cancel', retry: '/retry' }[action];
    try {
      try { await api('/api/v1/jobs/' + id + suffix, deleting ? 'DELETE' : 'POST'); }
      catch (error) { if (!deleting || !/\bnot_found\b/.test(String(error?.message || error))) throw error; }
      ++jobListVersion;
      if (deleting) {
        removedJobs.add(id); jobCards.delete(id); card.remove(); removalAcknowledged = true;
        $('jobs-status').textContent = action === 'record' ? '任务已删除，下载文件已保留。' : '任务及其拥有的文件已删除。';
      } else { feedback.textContent = action === 'cancel' ? '已请求取消。' : '已请求重试。'; }
    } catch (error) {
      feedback.textContent = jobError(error); feedback.classList.add('error');
    } finally {
      jobOperations.delete(id); syncJobOperation(card);
      void refreshJobsAfterMutation();
    }
  }
  function drawJob(job) {
    let card = jobCards.get(job.id);
    if (!card) {
      card = node('article', undefined, 'job'); card.dataset.jobId = job.id;
      const feedback = node('p', '', 'job-feedback'); feedback.setAttribute('role', 'status');
      const heading=node('div',undefined,'job-heading');const folderStatus=node('p','','job-folder-feedback');folderStatus.setAttribute('role','status');let folder;
      folder=button('',async()=>{if(folder.disabled)return;folder.disabled=true;folder.setAttribute('aria-busy','true');try{const result=await desktop.revealJobFolder(job.id);folderStatus.textContent=result?.revealed===false?'已打开下载目录，任务尚无可定位文件。':'';}catch(error){folderStatus.textContent=/native_directory_missing/.test(error?.message||'')?'保存目录不存在，请检查磁盘或目录。':'无法打开所在文件夹，请稍后重试。';}finally{folder.disabled=!desktop.revealJobFolder;folder.removeAttribute('aria-busy');}},'打开所在文件夹');folder.className='job-folder-button';folder.title='打开所在文件夹';folder.disabled=!desktop.revealJobFolder;
      const icon=document.createElementNS('http://www.w3.org/2000/svg','svg');icon.setAttribute('viewBox','0 0 24 24');icon.setAttribute('class','ui-icon');icon.setAttribute('aria-hidden','true');const shape=document.createElementNS('http://www.w3.org/2000/svg','path');shape.setAttribute('d','M3 7V4h7l2 3h9v13H3Z');icon.append(shape);folder.append(icon);heading.append(node('h3'),folder);
      card.append(heading,folderStatus,node('p', '', 'job-summary'), node('p','','job-transfer'),node('p','','job-failure'), node('progress'), node('ul'), node('div', undefined, 'job-actions'), feedback);
      jobCards.set(job.id, card);
    }
    card.job=job;card.querySelector('h3').textContent = jobHeading(job);card.querySelector('h3').title=job.url;drawJobTransfer(card);
    card.querySelector('.job-summary').textContent = (statusNames[job.status] || job.status) + ' · 成功 ' + job.succeeded + ' · 失败 ' + job.failed + ' · 跳过 ' + job.skipped + ' · 不可用 ' + job.unavailable;
    const items = card.querySelector('ul'), signature = JSON.stringify(job.items || []);
    const failedItems=(job.items||[]).filter(item=>item.status==='failed'||item.status==='unavailable');
    card.querySelector('.job-failure').textContent=['failed','partial','interrupted'].includes(job.status)?(job.errorCode&&job.errorCode!=='execution_failed'?downloadFailure(job):failedItems.length?downloadFailure(failedItems[0]):downloadFailure(job)):'';
    if (card.itemsSignature !== signature) {
      const scroll = items.scrollTop; items.replaceChildren();
      for (const item of job.items || []) {const row=node('li',(item.name||item.sourceId)+' · '+(statusNames[item.status]||item.status));if(['failed','unavailable','interrupted'].includes(item.status))row.append(node('p',downloadFailure(item),'job-item-failure'));items.append(row);}
      items.hidden = !job.items?.length; items.scrollTop = scroll; card.itemsSignature = signature;
    }
    // Preserve the actual button nodes across polling, including pointer-down
    // to pointer-up. Do not rebuild a pending action even if its status changes.
    if (card.actionStatus !== job.status && !jobOperations.has(job.id)) {
      const actions = card.querySelector('.job-actions'); actions.replaceChildren();
      const add = (label, action) => { const control = button(label, () => jobAction(job.id, action)); control.dataset.jobAction = action; control.dataset.label = label; actions.append(control); };
      if (['queued', 'running', 'cancelling'].includes(job.status)) add('取消', 'cancel');
      else {
        if (['failed', 'partial', 'interrupted', 'cancelled'].includes(job.status)) add('重试失败项', 'retry');
        add('仅删除任务', 'record'); add('删除任务与文件', 'files');
      }
      card.actionStatus = job.status;
    }
    syncJobOperation(card);
    return card;
  }
  function jobs() {
    if (jobRefresh) return jobRefresh;
    const version = jobListVersion;
    jobRefresh = (async () => {
      try {
        const result = await api('/api/v1/jobs?limit=30');
        if (state.view !== 'tasks' || version !== jobListVersion) return;
        const list = $('jobs'), visible = new Set();
        list.querySelector('.jobs-empty')?.remove();
        let previous = null;
        for (const job of result.jobs || []) {
          if (removedJobs.has(job.id)) continue;
          visible.add(job.id); const card = drawJob(job);
          const next = previous ? previous.nextElementSibling : list.firstElementChild;
          if (next !== card) list.insertBefore(card, next);
          previous = card;
        }
        for (const [id, card] of jobCards) if (!visible.has(id) && !jobOperations.has(id)) { card.remove(); jobCards.delete(id); }
        if (!jobCards.size) list.append(node('p', '还没有下载任务。', 'jobs-empty'));
        void resolveJobNames();
        if (jobListFailed) { $('jobs-status').textContent = ''; jobListFailed = false; }
      } catch {
        if (state.view !== 'tasks' || version !== jobListVersion) return;
        jobListFailed = true;
        $('jobs-status').textContent = removalAcknowledged ? '任务已删除，但列表刷新失败，稍后会自动重试。' : '任务列表刷新失败，已保留当前列表，稍后会自动重试。';
      }
    })().finally(() => { jobRefresh = null; });
    return jobRefresh;
  }
  const fields = [['downloadRegion','下载地区',['','us','cn','hk','tw','jp','gb']],['metadataRegion','元数据匹配地区',['','cn','us','jp']],['lyricsRegion','歌词地区',['','cn','us','jp']],['language','元数据语言',['','zh-CN','zh-TW','en-US','ja']],['lyricsType','歌词类型',['lyrics','syllable-lyrics']],['lyricsFormat','歌词格式',['lrc','ttml']],['coverFormat','封面格式',['jpg','png']],['coverSize','封面尺寸',['500x500','1000x1000','3000x3000','5000x5000']],['alacMax','ALAC 采样率上限',[44100,48000,96000,192000]],['saveLyrics','保存歌词文件'],['embedLyrics','嵌入歌词'],['embedCover','嵌入封面'],['saveArtistCover','保存艺人封面'],['saveAnimatedArtwork','保存动态封面'],['useSongInfoForPlaylist','使用专辑曲序']];
  function drawPreferences(value) { state.preferences = value; $('preference-fields').replaceChildren(); for (const [key, label, options] of fields) { const field = node('label', label, options ? '' : 'check'); const control = node(options ? 'select' : 'input'); control.dataset.preference = key; if (options) { for (const choice of options) { const option = node('option', choice === '' ? '自动 / 关闭' : String(choice)); option.value = String(choice); control.append(option); } if (![...control.options].some(option => option.value === String(value[key]))) { const option = node('option', String(value[key])); option.value = String(value[key]); control.append(option); } control.value = String(value[key]); } else { control.type = 'checkbox'; control.checked = value[key]; } field.append(control); $('preference-fields').append(field); } $('filename').value = value.songFileFormat; }
  async function cacheSettings(){if(!desktop.cacheInfo){$('cache-usage').textContent='缓存设置需要新版桌面服务。';$('save-cache-limit').disabled=true;$('clear-cache').disabled=true;return;}try{const info=await desktop.cacheInfo();$('cache-limit').value=String(info.limitMB);$('cache-usage').textContent=info.available?'已用 '+bytes(info.usedBytes)+' / '+info.limitMB+' MB · '+info.entries+' 项目录与封面缓存':'缓存暂不可用，仍可在线浏览。';}catch{$('cache-usage').textContent='暂时无法读取缓存占用。';}}
  async function settings() {void cacheSettings(); try { const [preferences, concurrency, current] = await Promise.all([api('/api/v1/settings/download'), api('/api/v1/settings/download-concurrency'), api('/api/native/state')]); drawPreferences(preferences.settings); $('concurrency').value = concurrency.downloadConcurrency; $('download-directory').textContent = current.downloadDirectory || ''; $('runtime-state').textContent = current.runtime.phase === 'ready' ? 'Windows 原生服务 · 已就绪' : 'Windows 原生服务 · 正在启动'; } catch { notice('设置暂不可用，请检查本机服务。'); } }
  $('choose-download-directory').disabled=!desktop.chooseDownloadDirectory;
  $('choose-download-directory').onclick=async()=>{const control=$('choose-download-directory');if(control.disabled)return;control.disabled=true;try{const result=await desktop.chooseDownloadDirectory();if(result&&!result.canceled){$('download-directory').textContent=result.directory;$('directory-feedback').textContent='下载位置已保存，新任务生效；已有任务保留原位置。';}}catch(error){$('directory-feedback').textContent=/native_directory_denied/.test(error?.message||'')?'该目录不可写入，请重新选择。':'下载位置未更改，请检查目录后重试。';}finally{control.disabled=!desktop.chooseDownloadDirectory;}};
  $('save-cache-limit').onclick=async()=>{const limit=Number($('cache-limit').value);if(!$('cache-limit').value.trim()||!Number.isInteger(limit)||limit<0||limit>10240){$('cache-feedback').textContent='请输入 0–10240 MB，0 为关闭缓存。';return;}const control=$('save-cache-limit');control.disabled=true;try{await desktop.setCacheLimit(limit);prefetched.clear();await cacheSettings();$('cache-feedback').textContent=limit?'缓存上限已调整，超出容量的旧缓存已自动清理。':'缓存已关闭并清理。';}catch{$('cache-feedback').textContent='调整未完成，请重试。';}finally{control.disabled=false;}};
  $('clear-cache').onclick=async()=>{const control=$('clear-cache');control.disabled=true;try{await desktop.clearCache();prefetched.clear();await cacheSettings();$('cache-feedback').textContent='缓存已清理，下载文件与登录会话不受影响。';}catch{$('cache-feedback').textContent='清理未完成，请重试。';}finally{control.disabled=false;}};
  $('refresh-catalog').onclick=async()=>{const control=$('refresh-catalog');control.disabled=true;++state.generation;try{if(state.view==='tasks'){await Promise.all([jobs(),refreshMonitor()]);return;}if(state.view==='settings'){await settings();return;}await desktop.refreshCatalog?.();prefetched.clear();detailHistory.length=0;void loadSidebarLibrary(true);if(state.detail){state.browseSnapshot=null;await detail(state.detail,true);}else if(searching)await browse(state.catalogPath);else await feed(state.feed);}catch{notice('刷新未完成，请稍后重试。');}finally{control.disabled=false;}};
  for (const token of ['{tracknum:02d}', '{disk}', '{title}', '{artist}', '{album}', '{album_artist}', '{year}', '{song_id}', ' - ', '. ']) $('filename-tokens').append(button(token, () => { const field = $('filename'); field.setRangeText(token, field.selectionStart, field.selectionEnd, 'end'); field.focus(); }));
  for (const control of document.querySelectorAll('[data-view]')) control.onclick = () => view(control.dataset.view);
  for (const control of document.querySelectorAll('[data-feed]')) control.onclick = () => { view('browse'); void feed(control.dataset.feed); };
  function setSidebar(expanded,save=false){document.body.classList.toggle('nav-open',expanded);$('nav-toggle').setAttribute('aria-expanded',String(expanded));$('nav-toggle').setAttribute('aria-label',expanded?'收起侧边栏':'展开侧边栏');if(save)try{localStorage.setItem('native-v2-sidebar',expanded?'expanded':'collapsed');}catch{}}
  let sidebarPreference='';try{sidebarPreference=localStorage.getItem('native-v2-sidebar')||'';}catch{}setSidebar(innerWidth>900&&sidebarPreference!=='collapsed');
  $('nav-toggle').onclick = () => setSidebar(!document.body.classList.contains('nav-open'),true);
  $('sidebar-search-toggle').onclick=()=>{setSidebar(true);$('search-input').focus();};
  for(const control of document.querySelectorAll('[data-feed]'))preloadOnIntent(control,()=>'/api/v1/catalog/'+control.dataset.feed+'?'+new URLSearchParams({region:$('region').value,...(control.dataset.feed==='charts'?{}:{offset:'0'})}));
  $('quality').onchange=()=>gate();
  $('session-light').onclick = () => { view('settings'); if(state.session !== 'authenticated') void desktop.openLogin().catch(()=>notice('登录窗口暂不可用。')); }; $('open-login').onclick = () => desktop.openLogin().catch(()=>notice('登录窗口暂不可用。')); $('recheck').onclick = () => desktop.recheck().catch(() => notice('验证暂不可用，请检查网络。'));
  $('export-log').onclick = () => desktop.exportDiagnostics().catch(()=>notice('诊断导出未完成。')); $('browse-back').onclick = goBack;
  $('download-monitor').onclick=()=>view('tasks');
  document.addEventListener('keydown', event => { if(event.key==='Escape'&&!event.defaultPrevented&&state.view==='browse'&&state.detail){event.preventDefault();closeDetail();} });
  $('region').onchange = () => { $('region').dataset.selected = 'true';void loadSidebarLibrary(true); if (state.detail) { state.browseSnapshot = null;detailHistory.length=0; void detail(state.detail,true); } else void feed(state.feed); };
  $('search-form').onsubmit = event => { event.preventDefault(); view('browse'); searching=true;discardDetail();updateNavigation();if(innerWidth<=900)setSidebar(false); void browse('/api/v1/catalog/search?' + new URLSearchParams({ region: $('region').value, term: $('search-input').value.trim(), type: '', offset: '0' })); };
  $('manual-form').onsubmit = event => { event.preventDefault(); void queue($('manual-url').value.trim()); };
  $('stop-playback').onclick = () => stopPlayback(); for (const event of ['playing','pause','ended']) $('audio').addEventListener(event, () => { $('player-status').textContent = (statusNames[event] || { playing: '正在播放', pause: '已暂停', ended: '播放结束' }[event]) + ' · ' + ($('audio').dataset.mode || ''); }); $('audio').onerror = () => { if(state.playbackItem&&$('audio').dataset.mode==='完整播放'){void preview(state.playbackItem,state.playbackGeneration);}else if(state.playbackItem)$('player-status').textContent = '音频暂不可用，请重试'; };
  try { const saved = JSON.parse(localStorage.getItem('native-v2-download-defaults') || '{}'); if (['aac-lc','alac','atmos'].includes(saved.quality)) $('quality').value = saved.quality; if (['skip','overwrite','fail'].includes(saved.existing)) $('existing').value = saved.existing; } catch {}
  $('save-settings').onclick = async () => { if (!state.preferences) return; const next = { ...state.preferences, songFileFormat: $('filename').value }; for (const control of document.querySelectorAll('[data-preference]')) next[control.dataset.preference] = control.type === 'checkbox' ? control.checked : control.dataset.preference === 'alacMax' ? Number(control.value) : control.value; try { await api('/api/v1/settings/download','PUT',next); await api('/api/v1/settings/download-concurrency','PUT',{downloadConcurrency:Number($('concurrency').value)}); localStorage.setItem('native-v2-download-defaults',JSON.stringify({quality:$('quality').value,existing:$('existing').value})); notice('设置已保存，下一个任务生效。'); } catch { notice('保存失败，请检查设置值。'); } };
  $('reset-settings').onclick = async () => { try { await api('/api/v1/settings/download','DELETE'); await api('/api/v1/settings/download-concurrency','DELETE'); $('quality').value='alac';$('existing').value='skip';localStorage.removeItem('native-v2-download-defaults');await settings();await gate(); } catch {notice('恢复默认未完成，请重试。');} };
  desktop.onSession(observe); desktop.getState().then(observe).catch(() => observe({state:'runtime_unavailable'}));
  const icons={home:['m3 10 9-7 9 7','M5 9v12h5v-7h4v7h5V9'],grid:['M3 3h7v7H3z','M14 3h7v7h-7z','M3 14h7v7H3z','M14 14h7v7h-7z'],library:['M5 3h14v18H5z','M8 7h8','M8 11h8','m9 16 2 2 4-4'],download:['M12 3v12','m7 10 5 5 5-5','M4 16v5h16v-5'],settings:['M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8','m10 3-1 3-3 1-3-1-1 4 2 2-1 3 3 2 3-1 2 2 4-1 1-3 3-1 1-4-2-2 1-3-3-2-3 1-2-2Z'],search:['M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14','m15 15 6 6'],sidebar:['M3 4h18v16H3z','M8 4v16'],music:['M9 18V5l10-2v13','m9 9 10-2','M5 16a3 2 0 1 0 0 4 3 2 0 0 0 0-4','M15 14a3 2 0 1 0 0 4 3 2 0 0 0 0-4'],refresh:['M20 11a8 8 0 1 0-2 6','M20 4v7h-7']};
  for(const target of document.querySelectorAll('[data-icon]')){const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 24 24');svg.setAttribute('aria-hidden','true');svg.classList.add('ui-icon');for(const d of icons[target.dataset.icon]||[]){const path=document.createElementNS(svg.namespaceURI,'path');path.setAttribute('d',d);svg.append(path);}target.append(svg);}
  if(desktop.onReady)desktop.onReady(()=>{void feed('home');void loadSidebarLibrary(true);void refreshMonitor();});else {void feed('home');void refreshMonitor();} setInterval(() => {void refreshMonitor();drawMonitor();if (state.view === 'tasks') {void jobs();for(const card of jobCards.values())drawJobTransfer(card);} }, 1000);
})();

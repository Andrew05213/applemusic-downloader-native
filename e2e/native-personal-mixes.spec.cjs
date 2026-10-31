'use strict';
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const renderer = pathToFileURL(path.resolve(__dirname, '../src/renderer/native-index.html')).href;

async function install(page, { delayed = false, failed = false, empty = false, missingArtwork = false, title = 'Playlists Made for You' } = {}) {
  const names = ['New Music', 'Heavy Rotation', 'Your Essentials', 'Get Up!', 'Chill'];
  const palettes = [['#ea5561','#e9b7c1'],['#ffcb06','#db5904'],['#5318ef','#e57cbe'],['#e70008','#ffc312'],['#005d8c','#02c9ba']];
  await page.route('https://is1-ssl.mzstatic.com/mixes/*', route => {
    if (missingArtwork) return route.abort();
    const index = Number(new URL(route.request().url()).pathname.split('/').pop());
    const [a,b] = palettes[index];
    return route.fulfill({contentType:'image/svg+xml',body:`<svg xmlns="http://www.w3.org/2000/svg" width="500" height="500"><defs><linearGradient id="g" x2="1" y2="1"><stop stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient><filter id="blur"><feGaussianBlur stdDeviation="35"/></filter></defs><path fill="url(#g)" d="M0 0h500v500H0z"/><ellipse cx="120" cy="230" rx="200" ry="80" fill="${b}" filter="url(#blur)"/><text x="470" y="35" fill="white" text-anchor="end" font-family="Arial" font-size="22" font-weight="bold">Music</text><text x="250" y="265" fill="white" text-anchor="middle" font-family="Arial" font-size="45" font-weight="bold">${names[index]}</text></svg>`});
  });
  await page.addInitScript(({names,delayed,failed,empty,title}) => {
    HTMLMediaElement.prototype.play = function () { return Promise.resolve(); };
    HTMLMediaElement.prototype.pause = function () {};
    HTMLMediaElement.prototype.load = function () {};
    const mixes = names.map((title,index) => ({id:'pl.mix'+index,type:'playlists',playlistType:'personal-mix',title,artist:'Apple Music for You',description:'Laufey、HOYO-MiX、ChilliChill 和更多你喜爱的音乐人',artwork:'https://is1-ssl.mzstatic.com/mixes/'+index,url:'https://music.apple.com/us/playlist/pl.mix'+index,downloadable:true}));
    const album = {id:'300',type:'albums',title:'Other Album',artist:'Other Artist',url:'https://music.apple.com/us/album/300',downloadable:true};
    const song = {id:'701',type:'songs',title:'Mix First Song',artist:'Fixture Artist',downloadable:true,url:'https://music.apple.com/us/song/701'};
    window.mixCalls=[]; window.pendingMixes=[];
    window.releaseMixes=()=>window.pendingMixes.splice(0).forEach(resolve=>resolve());
    window.nativeDesktop={
      getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady(fn){queueMicrotask(fn);},openLogin:async()=>{},exportDiagnostics:async()=>true,
      api:async(target,options={})=>{
        window.mixCalls.push({target,...options}); const url=new URL(target,'http://fixture');
        if(url.pathname==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true}}};
        if(url.pathname==='/api/v1/catalog/home')return {feed:'personal',groups:[{title,type:'playlists',recommendationId:'rec-mixes',items:mixes},{title:'Recently Played',type:'albums',items:[album]}]};
        if(url.pathname==='/api/v1/catalog/library')return {groups:[{title:'我的歌单',type:'playlists',items:mixes}]};
        if(url.pathname==='/api/v1/catalog/search')return {groups:[{title:'Made for You',type:'playlists',items:mixes}]};
        if(url.pathname==='/api/v1/catalog/detail'){
          if(delayed)await new Promise(resolve=>window.pendingMixes.push(resolve));
          if(failed)throw new Error('fixture unavailable');
          const resource=mixes.find(item=>item.id===url.searchParams.get('id'))||album;
          return {resource,tracks:empty?[]:[{...song,id:'700',unavailableReason:'catalog_resource_unavailable'},song]};
        }
        if(url.pathname==='/api/native/playback')return options.method==='DELETE'?{ok:true}:{ok:true,mode:'full',mediaURL:'http://127.0.0.1:45678/api/native/playback/'+'a'.repeat(32)};
        if(url.pathname==='/api/v1/browser-download/resolve')return {url:options.body.url};
        if(url.pathname==='/api/v1/jobs'&&options.method==='POST')return {jobs:[{id:'fixture-job',url:options.body.urls[0]}]};
        throw new Error('Unexpected fixture route '+target);
      }
    };
  }, {names,delayed,failed,empty,title});
  await page.goto(renderer);
  await expect(page.getByRole('heading',{name:'专属推荐歌单',exact:true})).toBeVisible();
}

for (const width of [1534, 800, 390]) test(`personal mixes use portrait cards with intact artwork and horizontal browsing at ${width}px`, async({page})=>{
  await page.setViewportSize({width,height:850});
  await install(page);
  await expect(page.getByRole('heading',{name:'专属推荐歌单',exact:true})).toHaveCSS('font-size','20px');
  await expect(page.getByRole('heading',{name:'Recently Played',exact:true})).toHaveCSS('font-size','20px');
  const row=page.getByRole('region',{name:'专属推荐歌单',exact:true});
  await expect(row.locator('.personal-mix-card')).toHaveCount(5);
  await expect(row.locator('[data-download],.card-actions')).toHaveCount(0);
  await expect(row.locator('.personal-mix-summary').first()).toContainText('Laufey');
  await expect(row.locator('.artwork-missing')).toHaveCount(0);
  const box=await row.locator('.personal-mix-card').first().boundingBox();
  expect(box.height/box.width).toBeCloseTo(4/3,1);
  expect(await row.locator('.personal-mix-artwork').first().evaluate(img=>getComputedStyle(img).objectFit)).toBe('contain');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  if(width===1534){expect(await row.evaluate(el=>el.scrollWidth<=el.clientWidth+2)).toBe(true);}
  else{expect(await row.evaluate(el=>el.scrollWidth>el.clientWidth)).toBe(true);await row.locator('.personal-mix-card').last().scrollIntoViewIfNeeded();expect(await row.evaluate(el=>el.scrollLeft)).toBeGreaterThan(0);await row.evaluate(el=>el.scrollLeft=0);}
  const normal=page.locator('.catalog-card').filter({has:page.getByRole('button',{name:'查看 Other Album 详情',exact:true})});
  await expect(normal).not.toHaveClass(/personal-mix-card/);
  await row.locator('.personal-mix-card').first().hover();
  await expect(row.getByRole('button',{name:'播放 New Music',exact:true})).toHaveCSS('opacity','1');
  await page.screenshot({path:`test-results/native-personal-mixes-${width}.png`,fullPage:true});
});

test('mix cover opens detail and return restores the original carousel and focus; downloads remain in detail',async({page})=>{
  await install(page);
  const cover=page.getByRole('button',{name:'查看 Heavy Rotation 详情',exact:true});
  await cover.focus();await page.keyboard.press('Enter');
  await expect(page.locator('.detail-hero h1')).toHaveText('Heavy Rotation');
  await page.locator('.detail-hero').getByRole('button',{name:'下载 Heavy Rotation',exact:true}).click();
  await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
  await page.getByRole('button',{name:'返回上一页',exact:true}).click();
  await expect(cover).toBeFocused();
  await expect(page.locator('.personal-mix-card')).toHaveCount(5);
  expect(await page.evaluate(()=>window.mixCalls.filter(call=>call.target.startsWith('/api/v1/catalog/home')).length)).toBe(1);
});

test('overlay play resolves a real song without navigating or queuing a download',async({page})=>{
  await install(page);
  await page.locator('.personal-mix-card').first().hover();
  await page.getByRole('button',{name:'播放 New Music',exact:true}).click();
  await expect(page.locator('#player-title')).toHaveText('Mix First Song · Fixture Artist');
  await expect(page.locator('.detail-hero')).toHaveCount(0);
  await expect(page.getByRole('region',{name:'专属推荐歌单',exact:true})).toBeVisible();
  expect(await page.evaluate(()=>window.mixCalls.filter(call=>call.target==='/api/native/playback'&&call.method==='POST').map(call=>call.body.id))).toEqual(['701']);
  expect(await page.evaluate(()=>window.mixCalls.filter(call=>call.target==='/api/v1/jobs'))).toHaveLength(0);
});

test('a pending mix play cannot override a newer playback intent',async({page})=>{
  await install(page,{delayed:true});
  await page.locator('.personal-mix-card').first().hover();
  await page.getByRole('button',{name:'播放 New Music',exact:true}).click();
  await expect(page.getByRole('button',{name:'播放 New Music',exact:true})).toBeDisabled();
  await expect.poll(()=>page.evaluate(()=>window.pendingMixes.length)).toBe(1);
  // Stop invalidates even a collection request that has not acquired media yet.
  await page.locator('#stop-playback').evaluate(button=>button.click());
  await page.evaluate(()=>window.releaseMixes());
  await expect(page.getByRole('button',{name:'播放 New Music',exact:true})).toBeEnabled();
  expect(await page.evaluate(()=>window.mixCalls.filter(call=>call.target==='/api/native/playback'&&call.method==='POST'))).toHaveLength(0);
});

for(const mode of ['failed','empty'])test(`mix ${mode} response leaves the carousel interactive with an actionable message`,async({page})=>{
  await install(page,{[mode]:true});
  await page.locator('.personal-mix-card').first().hover();
  const play=page.getByRole('button',{name:'播放 New Music',exact:true});
  await play.click();await expect(play).toBeEnabled();
  await expect(page.locator('#notice')).toHaveText(mode==='failed'?'暂时无法播放此歌单，请进入详情重试。':'此歌单暂无可播放的歌曲，请进入详情查看。');
  await expect(page.locator('.detail-hero')).toHaveCount(0);
});

test('missing artwork keeps a readable title and ordinary library/search lists do not become mix carousels',async({page})=>{
  await install(page,{missingArtwork:true,title:'专属推荐歌单'});
  await expect(page.locator('.personal-mix-card.artwork-missing')).toHaveCount(5);
  await expect(page.locator('.personal-mix-title').first()).toBeVisible();
  await page.getByRole('button',{name:'我的歌单',exact:true}).click();
  await expect(page.locator('.personal-mix-card')).toHaveCount(0);
  await expect(page.locator('.catalog-card')).toHaveCount(5);
  await page.getByRole('searchbox').fill('mix');await page.getByRole('searchbox').press('Enter');
  await expect(page.getByRole('heading',{name:'Made for You',exact:true})).toBeVisible();
  await expect(page.locator('.personal-mix-card')).toHaveCount(0);
});

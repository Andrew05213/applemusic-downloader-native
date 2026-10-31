'use strict';
const {test,expect}=require('@playwright/test');
const {pathToFileURL}=require('node:url');
const path=require('node:path');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;

async function install(page,{authenticated=true,ranked=false}={}){
 await page.route('https://is1-ssl.mzstatic.com/search-fixture/*',route=>route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><rect width="100" height="100" fill="#426388"/><circle cx="50" cy="50" r="23" fill="#a9cde2"/></svg>'}));
 await page.addInitScript(({authenticated,ranked})=>{
  HTMLMediaElement.prototype.play=function(){return Promise.resolve();};HTMLMediaElement.prototype.pause=function(){};HTMLMediaElement.prototype.load=function(){};
  const song=(id,title)=>({id,title,type:'songs',artist:'HOYO-MiX · Fixture Artist',duration:198000,artwork:'https://is1-ssl.mzstatic.com/search-fixture/'+id,url:'https://music.apple.com/us/song/'+id,downloadable:true});
  const first=song('701','Let the Wind Tell You — Long Search Song Name'),second=song('702','If You Think of Me'),last=song('703','Next Page Song');
  const album={id:'300',title:'Fixture Album',artist:'HOYO-MiX',type:'albums',artwork:first.artwork,url:'https://music.apple.com/us/album/300',downloadable:true};
  const playlist={...album,id:'pl.301',title:'Fixture Playlist',type:'playlists',url:'https://music.apple.com/us/playlist/pl.301'};
  const artist={id:'42',title:'HOYO-MiX',type:'artists',genres:['原声音乐'],artwork:first.artwork,url:'https://music.apple.com/us/artist/42',downloadable:false};
  const albums=[album,...Array.from({length:5},(_,i)=>({...album,id:String(310+i),title:'Genshin Impact — Original Game Soundtrack '+(i+1)}))];
  const songs=[first,second,...Array.from({length:10},(_,i)=>song(String(710+i),'Soundtrack Song '+(i+1)))];
  window.searchCalls=[];
  window.nativeDesktop={getState:async()=>({state:authenticated?'authenticated':'needs_login',region:'us'}),onSession(){},onReady(fn){queueMicrotask(fn);},openLogin:async()=>{},prefetchCatalog:async()=>true,
   api:async(target,options={})=>{window.searchCalls.push({target,...options});const u=new URL(target,'http://fixture');
    if(u.pathname==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true}}};
    if(u.pathname==='/api/v1/catalog/home')return {groups:[{type:'albums',items:[album]}]};
    if(u.pathname==='/api/v1/catalog/library')return {groups:[]};
    if(u.pathname==='/api/v1/catalog/search'){
     if(u.searchParams.get('term')==='empty')return {groups:[]};
     if(u.searchParams.get('offset')==='2')return {groups:[{type:'songs',items:[last]}]};
     if(ranked)return {bestResults:[artist,album,first,...albums.slice(1)],groups:[{type:'songs',items:songs},{type:'albums',items:albums},{type:'artists',items:[artist]},{type:'playlists',items:[playlist]}]};
     return {groups:[{type:'songs',items:[first,second],nextOffset:2},{type:'albums',items:[album]},{type:'playlists',items:[playlist]}]};
    }
    if(u.pathname==='/api/v1/catalog/detail'){
     if(u.searchParams.get('type')==='artists')return u.searchParams.get('offset')==='12'?{groups:[{type:'albums',title:'专辑',items:[{...album,id:'399',title:'Later Artist Album'}]}]}:{resource:artist,groups:[{type:'albums',title:'专辑',items:albums}],nextOffset:12};
     return {resource:[first,second,last,...albums,playlist].find(item=>item.id===u.searchParams.get('id')),tracks:[first]};
    }
    if(u.pathname==='/api/native/playback')return options.method==='DELETE'?{ok:true}:{ok:true,mode:'full',mediaURL:'http://127.0.0.1:45678/api/native/playback/'+'a'.repeat(32)};
    if(u.pathname==='/api/v1/browser-download/resolve')return {url:options.body.url};
    if(u.pathname==='/api/v1/jobs'&&options.method==='POST')return {jobs:[{id:'fixture-task',url:options.body.urls[0]}]};
    if(u.pathname==='/api/v1/jobs')return {jobs:[]};
    throw Error('unexpected search fixture route');
   }};
 },{authenticated,ranked});
 await page.goto(renderer);await expect(page.locator('.catalog-card')).toHaveCount(1);
 if(await page.locator('#nav-toggle').getAttribute('aria-expanded')!=='true')await page.locator('#nav-toggle').click();
 const search=page.getByRole('searchbox');await search.fill(ranked?'hoyomix':'Genshin');await search.press('Enter');await expect(page.locator('.search-song-row')).toHaveCount(ranked?12:2);
}

for(const width of [1534,1000,800,390])test(`search songs use compact rows and keep album/playlist artwork at ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:900});await install(page);
 if(width<=900)await expect(page.locator('#nav-toggle')).toHaveAttribute('aria-expanded','false');
 await expect(page.getByRole('heading',{name:'歌曲',exact:true})).toHaveCSS('font-size','20px');
 await expect(page.getByRole('list',{name:'歌曲',exact:true})).toBeVisible();
 const row=page.locator('.search-song-row').first(),cover=await row.locator('.artwork').boundingBox();
 expect(cover.width).toBeLessThanOrEqual(44);expect((await row.boundingBox()).height).toBeLessThanOrEqual(80);
 await expect(row.getByRole('button',{name:/播放/})).toBeVisible();await expect(row.getByRole('button',{name:/下载/})).toBeEnabled();
 await expect(row.locator('.search-song-mobile-artist')).toBeVisible();await expect(row.locator('.search-song-artist')).toBeHidden();
 if(width>500)await expect(row.locator('.search-song-duration')).toHaveText('3:18');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await expect(page.locator('.catalog-card')).toHaveCount(2);await expect(page.locator('.catalog-card .card-actions')).toHaveCount(0);
 await page.locator('#search-input').blur();await page.screenshot({path:`test-results/native-search-${width}.png`,fullPage:true});
});

test('search row cover/title enter detail; back restores the same row, focus and search without fetching again',async({page})=>{
 await install(page);const link=page.locator('.search-song-row .catalog-title-action').first();await link.focus();await page.keyboard.press('Enter');
 await expect(page.locator('.detail-hero h1')).toHaveText('Let the Wind Tell You — Long Search Song Name');
 await page.locator('#browse-back').click();await expect(link).toBeFocused();await expect(page.getByRole('searchbox')).toHaveValue('Genshin');
 await page.locator('.search-song-cover').first().click();await expect(page.locator('.detail-hero')).toBeVisible();await page.locator('#browse-back').click();
 expect(await page.evaluate(()=>window.searchCalls.filter(call=>call.target.startsWith('/api/v1/catalog/search?')).length)).toBe(1);
});

test('search row playback and download keep the results visible and use the selected song identity',async({page})=>{
 await install(page);await page.getByRole('button',{name:'播放 If You Think of Me',exact:true}).click();await expect(page.locator('#player-title')).toContainText('If You Think of Me');
 await expect(page.locator('.search-song-row')).toHaveCount(2);await expect(page.locator('.detail-hero')).toHaveCount(0);
 await page.getByRole('button',{name:'下载 If You Think of Me',exact:true}).click();await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
 const request=await page.evaluate(()=>window.searchCalls.find(call=>call.target==='/api/v1/jobs'&&call.method==='POST'));
 expect(request.body.urls).toEqual(['https://music.apple.com/us/song/702']);expect(request.body.titles).toEqual(['If You Think of Me']);
});

test('song pagination extends the existing list without duplicate headings and empty search clears old results',async({page})=>{
 await install(page);await page.getByRole('button',{name:'加载更多 songs',exact:true}).click();
 await expect(page.locator('.search-song-row')).toHaveCount(3);await expect(page.locator('.search-song-list')).toHaveCount(1);await expect(page.getByRole('heading',{name:'歌曲',exact:true})).toHaveCount(1);
 await expect(page.locator('.catalog-card')).toHaveCount(2);await expect(page.getByRole('button',{name:'加载更多 songs',exact:true})).toHaveCount(0);
 const search=page.getByRole('searchbox');await search.fill('empty');await search.press('Enter');await expect(page.locator('.search-song-list')).toHaveCount(0);await expect(page.locator('#notice')).toHaveText('暂无内容。');
});

test('logged-out search still shows results while song download stays gated',async({page})=>{
 await install(page,{authenticated:false});for(const button of await page.locator('.search-song-row [data-download]').all())await expect(button).toBeDisabled();
 expect(await page.evaluate(()=>window.searchCalls.some(call=>call.target==='/api/v1/jobs'&&call.method==='POST'))).toBe(false);
});

for(const width of [1800,1000,390])test(`ranked search has best-result summaries, circular artists, album rail and multi-column songs at ${width}px`,async({page})=>{
 await page.emulateMedia({reducedMotion:'reduce'});await page.setViewportSize({width,height:1000});await install(page,{ranked:true});
 await expect(page.locator('.search-best-card')).toHaveCount(8);await expect(page.locator('.search-best-card').first()).toContainText('HOYO-MiX');
 await expect(page.locator('.search-best-card').first().locator('.artwork')).toHaveCSS('border-top-left-radius','50%');
 const titles=await page.locator('#catalog-content>h2').allTextContents();expect(titles).toEqual(['艺人','专辑','歌曲','歌单']);
 await expect(page.locator('.search-artists .artist-card')).toHaveCount(1);await expect(page.locator('.search-artists [data-download]')).toHaveCount(0);
 const rail=page.locator('.search-collection-grid').nth(1);expect(await rail.evaluate(el=>getComputedStyle(el).gridAutoFlow)).toBe('column');
 const rows=page.locator('.search-song-row');const a=await rows.nth(0).boundingBox(),b=await rows.nth(1).boundingBox(),d=await rows.nth(3).boundingBox();
 if(width>700){expect(b.x).toBe(a.x);expect(b.y).toBeGreaterThan(a.y);expect(d.x).toBeGreaterThan(a.x);expect(d.y).toBe(a.y);}
 else{expect(d.x).toBe(a.x);expect(d.y).toBeGreaterThan(b.y);}
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:`test-results/native-search-ranked-${width}.png`,fullPage:true});
});

test('best artist opens real artist detail and album pagination; back restores ranked search without a new request',async({page})=>{
 await install(page,{ranked:true});const best=page.getByRole('button',{name:'查看最佳结果 HOYO-MiX 详情',exact:true});await best.click();
 await expect(page.locator('.artist-hero h1')).toHaveText('HOYO-MiX');await expect(page.locator('.artist-hero [data-download],.artist-hero .card-actions')).toHaveCount(0);
 await expect(page.locator('.catalog-card')).toHaveCount(6);await page.locator('#catalog-more').click();await expect(page.getByText('Later Artist Album',{exact:true})).toBeVisible();
 expect(await page.evaluate(()=>window.searchCalls.some(call=>call.target.includes('type=artists')&&call.target.includes('id=42')&&call.target.includes('offset=12')))).toBe(true);
 await page.locator('#browse-back').click();await expect(best).toBeFocused();await expect(page.locator('.search-song-row')).toHaveCount(12);
 expect(await page.evaluate(()=>window.searchCalls.filter(call=>call.target.startsWith('/api/v1/catalog/search?')).length)).toBe(1);
});

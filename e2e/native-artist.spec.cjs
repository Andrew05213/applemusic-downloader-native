'use strict';
const {test,expect}=require('@playwright/test'),path=require('node:path'),{pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;
async function install(page,{collaboration=false,missingID=false,missingViews=false,delayed=false}={}){
 await page.route('https://is1-ssl.mzstatic.com/artist-fixture/*',route=>route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="500" height="500"><defs><linearGradient id="g"><stop stop-color="#2d69a0"/><stop offset="1" stop-color="#825ba9"/></linearGradient></defs><rect width="500" height="500" fill="url(#g)"/><text x="250" y="265" font-size="55" font-family="Arial" fill="white" text-anchor="middle">Fixture Music</text></svg>'}));
 await page.addInitScript(({collaboration,missingID,missingViews,delayed})=>{
  HTMLMediaElement.prototype.play=function(){return Promise.resolve();};HTMLMediaElement.prototype.pause=function(){};HTMLMediaElement.prototype.load=function(){};
  const art='https://is1-ssl.mzstatic.com/artist-fixture/cover';
  const album=(id,title)=>({id,type:'albums',title,artist:'HOYO-MiX',artistId:missingID?'':'42',relatedArtist:missingID?'':'HOYO-MiX',artists:missingID?[]:collaboration?[{id:'42',name:'HOYO-MiX'},{id:'43',name:'Singer & Composer'}]:[{id:'42',name:'HOYO-MiX'}],artwork:art,genres:['Soundtrack'],releaseDate:'2026-10-22',trackCount:2,url:'https://music.apple.com/us/album/'+id,downloadable:true});
  const initial=album('300','Initial Album'),latest=album('399','Actual Latest - Single'),albums=Array.from({length:6},(_,i)=>album(String(310+i),'Original Game Soundtrack '+(i+1)));
  const songs=Array.from({length:9},(_,i)=>({id:String(701+i),type:'songs',title:'Top Song '+(i+1),artist:'HOYO-MiX',albumTitle:'Original Game Soundtrack',releaseDate:'2026-10-22',duration:81000,artwork:art,url:'https://music.apple.com/us/song/'+(701+i),downloadable:true}));
  window.artistCalls=[];window.artistPending=[];window.releaseArtist=()=>window.artistPending.splice(0).forEach(fn=>fn());
  window.nativeDesktop={getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady(fn){queueMicrotask(fn);},openLogin:async()=>{},prefetchCatalog:async()=>true,
   api:async(target,options={})=>{window.artistCalls.push({target,...options});const u=new URL(target,'http://fixture');
    if(u.pathname==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true}}};
    if(u.pathname==='/api/v1/catalog/home')return {groups:[{type:'albums',title:'最近播放',items:[initial]}]};
    if(u.pathname==='/api/v1/catalog/library')return {groups:[]};
    if(u.pathname==='/api/v1/catalog/artist-albums')return {groups:[]};
    if(u.pathname==='/api/v1/catalog/detail'){
     if(u.searchParams.get('type')==='artists'){
      const id=u.searchParams.get('id');const response={resource:{id,type:'artists',title:id==='43'?'Singer & Composer':'HOYO-MiX',artwork:art},groups:[{type:'albums',title:'专辑',items:albums}],artistHome:missingViews?{}:{latestRelease:latest,topSongs:songs}};
      if(delayed)await new Promise(resolve=>window.artistPending.push(resolve));return response;
     }
     return {resource:[initial,latest,...albums].find(item=>item.id===u.searchParams.get('id')),tracks:[songs[0]]};
    }
    if(u.pathname==='/api/native/playback')return options.method==='DELETE'?{ok:true}:{ok:true,mode:'full',mediaURL:'http://127.0.0.1:45678/api/native/playback/'+'a'.repeat(32)};
    if(u.pathname==='/api/v1/browser-download/resolve')return {url:options.body.url};
    if(u.pathname==='/api/v1/jobs'&&options.method==='POST')return {jobs:[{id:'fixture-job',url:options.body.urls[0]}]};
    if(u.pathname==='/api/v1/jobs')return {jobs:[]};
    throw Error('unexpected artist fixture route');
   }};
 },{collaboration,missingID,missingViews,delayed});
 await page.goto(renderer);await page.locator('.catalog-title-action').first().click();await expect(page.locator('.detail-hero h1')).toHaveText('Initial Album');
}

for(const width of [1800,1280,800,390])test(`red artist name opens the centered artist homepage at ${width}px`,async({page})=>{
 await page.emulateMedia({reducedMotion:'reduce'});await page.setViewportSize({width,height:950});await install(page);
 const link=page.getByRole('button',{name:'查看艺人 HOYO-MiX 主页',exact:true});await expect(link).toHaveCSS('color','rgb(255, 55, 93)');await link.click();
 await expect(page.locator('.artist-hero h1')).toHaveText('HOYO-MiX');await expect(page.getByRole('region',{name:'最新发布',exact:true})).toBeVisible();
 await expect(page.locator('.artist-top-songs .search-song-row')).toHaveCount(9);await expect(page.locator('.artist-release-copy')).toContainText('Actual Latest - Single');
 await expect(page.locator('.artist-release-copy')).toContainText('2026年10月22日');await expect(page.locator('.artist-hero [data-download]')).toHaveCount(0);
 const hero=await page.locator('.artist-hero').boundingBox(),portrait=await page.locator('.artist-portrait .artwork').boundingBox();expect(Math.abs(portrait.x+portrait.width/2-hero.x-hero.width/2)).toBeLessThanOrEqual(1);
 await expect(page.locator('.artist-portrait .artwork')).toHaveCSS('border-top-left-radius','50%');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 expect(await page.locator('.search-collection-grid').evaluate(el=>el.scrollLeft)).toBe(0);
 expect(await page.evaluate(()=>window.artistCalls.some(call=>call.target.includes('type=artists')&&call.target.includes('id=42')))).toBe(true);
 await page.screenshot({path:`test-results/native-artist-${width}.png`,fullPage:true});
 await page.locator('#browse-back').click();await expect(link).toBeFocused();await expect(page.locator('.detail-hero h1')).toHaveText('Initial Album');
});

test('artist links are keyboard accessible and collaborators navigate by their separate real IDs',async({page})=>{
 await install(page,{collaboration:true});const collaborator=page.getByRole('button',{name:'查看艺人 Singer & Composer 主页',exact:true});await collaborator.focus();await page.keyboard.press('Enter');
 await expect(page.locator('.artist-hero h1')).toHaveText('Singer & Composer');expect(await page.evaluate(()=>window.artistCalls.some(call=>call.target.includes('type=artists')&&call.target.includes('id=43')))).toBe(true);
 await page.locator('#browse-back').click();await expect(collaborator).toBeFocused();await expect(page.locator('.detail-artist-link')).toHaveCount(2);
});

test('artist header and ranked song actions play songs, while latest release download queues only the album',async({page})=>{
 await install(page);await page.locator('.detail-artist-link').click();await expect(page.locator('.artist-hero')).toBeVisible();
 await page.getByRole('button',{name:'播放 HOYO-MiX',exact:true}).click();await expect(page.locator('#player-title')).toContainText('Top Song 1');
 await page.getByRole('button',{name:'播放 Top Song 2',exact:true}).click();await expect(page.locator('#player-title')).toContainText('Top Song 2');
 await page.locator('.artist-latest [data-download]').click();await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
 const calls=await page.evaluate(()=>window.artistCalls);const queued=calls.find(call=>call.target==='/api/v1/jobs'&&call.method==='POST');expect(queued.body.urls).toEqual(['https://music.apple.com/us/album/399']);
 expect(calls.filter(call=>call.target==='/api/native/playback'&&call.method==='POST').map(call=>call.body.id)).toEqual(['701','702']);
});

test('latest release and album tiles open detail; nested back restores the same artist homepage',async({page})=>{
 await install(page);await page.locator('.detail-artist-link').click();await page.locator('.artist-release-copy .catalog-title-action').click();await expect(page.locator('.detail-hero h1')).toHaveText('Actual Latest - Single');
 await page.locator('#browse-back').click();await expect(page.locator('.artist-hero h1')).toHaveText('HOYO-MiX');await page.locator('.catalog-card .catalog-title-action').first().click();await expect(page.locator('.detail-hero h1')).toHaveText('Original Game Soundtrack 1');
 await page.locator('#browse-back').click();await expect(page.locator('.artist-hero h1')).toHaveText('HOYO-MiX');expect(await page.evaluate(()=>window.artistCalls.filter(call=>call.target.includes('type=artists')).length)).toBe(1);
});

test('missing artist identity stays plain text instead of guessing a homepage',async({page})=>{
 await install(page,{missingID:true});await expect(page.locator('.detail-artist')).toHaveText('HOYO-MiX');await expect(page.locator('.detail-artist-link')).toHaveCount(0);
 expect(await page.evaluate(()=>window.artistCalls.some(call=>call.target.includes('type=artists')))).toBe(false);
});

test('missing artist views retain the profile and albums without invented latest release or rankings',async({page})=>{
 await install(page,{missingViews:true});await page.locator('.detail-artist-link').click();await expect(page.locator('.artist-hero')).toBeVisible();await expect(page.locator('.artist-latest,.artist-top-songs')).toHaveCount(0);
 await expect(page.locator('.artist-play')).toBeDisabled();await expect(page.locator('.catalog-card')).toHaveCount(6);
});

test('back during an artist request discards its late homepage response',async({page})=>{
 await install(page,{delayed:true});const link=page.locator('.detail-artist-link');await link.click();await expect.poll(()=>page.evaluate(()=>window.artistPending.length)).toBe(1);
 await page.locator('#browse-back').click();await expect(page.locator('.detail-hero h1')).toHaveText('Initial Album');await page.evaluate(()=>window.releaseArtist());
 await expect(page.locator('.artist-hero')).toHaveCount(0);await expect(link).toBeFocused();
});

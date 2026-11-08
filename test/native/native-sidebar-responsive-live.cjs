'use strict';
// Real renderer layout and navigation with synthetic data only; no Apple calls.
const {chromium}=require('playwright'),{expect}=require('@playwright/test');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {pathToFileURL}=require('node:url');
async function main(){
 const browser=await chromium.launch({headless:true});
 const output=path.resolve('.artifacts/sidebar-responsive');fs.mkdirSync(output,{recursive:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:900},reducedMotion:'reduce'}),errors=[],remote=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route(/^https?:/,route=>{remote.push(route.request().url());return route.abort();});
  await page.addInitScript(()=>{
   window.fixtureRequests=[];
   const album={id:'300',type:'albums',title:'Fixture Album',artist:'Fixture Artist',url:'https://music.apple.com/us/album/300'};
   window.nativeDesktop={onSession:()=>{},getState:async()=>({state:'authenticated',region:'us'}),openLogin:async()=>{},api:async target=>{
    fixtureRequests.push(target);const p=new URL(target,'http://fixture').pathname;
    if(p==='/api/v1/catalog/home'||p==='/api/v1/catalog/search'||p==='/api/v1/catalog/discover')return {groups:[{type:'albums',title:'Fixture Albums',items:[album]}]};
    if(p==='/api/v1/catalog/library')return {groups:[]};
    if(p==='/api/v1/jobs')return {jobs:[{id:'fixture',title:'Fixture Album',url:album.url,status:'running',total:3,succeeded:1,failed:0,skipped:0,unavailable:0}]};
    if(p==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true}}};
    if(p==='/api/v1/settings/download')return {settings:{downloadRegion:'',metadataRegion:'',lyricsRegion:'',language:'',saveLyrics:false,embedLyrics:true,lyricsType:'lyrics',lyricsFormat:'lrc',embedCover:true,coverFormat:'jpg',coverSize:'500x500',saveArtistCover:false,saveAnimatedArtwork:false,useSongInfoForPlaylist:false,alacMax:192000,songFileFormat:'{title}'}};
    if(p==='/api/v1/settings/download-concurrency')return {downloadConcurrency:3};
    if(p==='/api/native/state')return {runtime:{phase:'ready'},downloadDirectory:'synthetic only'};
    throw Error('fixture_route_unhandled: '+p);
   }};
  });
  await page.goto(pathToFileURL(path.resolve('src/renderer/native-index.html')).href);
  await expect(page.locator('.catalog-title-action')).toHaveText('Fixture Album');
  async function layout(width,expanded){
   await page.setViewportSize({width,height:900});
   await expect(page.locator('#nav-toggle')).toHaveAttribute('aria-expanded',String(expanded));
   await expect.poll(()=>page.locator('#sidebar').evaluate(e=>e.getBoundingClientRect().width)).toBe(expanded?244:64);
   const boxes=await page.evaluate(()=>{
    const rect=s=>{const r=document.querySelector(s).getBoundingClientRect();return {x:r.x,right:r.right,width:r.width};};
    return {sidebar:rect('#sidebar'),content:rect('.native-content'),brand:rect('.window-brand'),toggle:rect('#nav-toggle'),monitor:rect('#download-monitor')};
   });
   assert.ok(boxes.content.x>=boxes.sidebar.right,'sidebar covers page content');
   assert.ok(boxes.toggle.right<=boxes.brand.right,'header toggle overflows its column');
   assert.ok(boxes.monitor.x>=boxes.brand.right,'monitor overlaps navigation');
   await expect(page.locator('[data-view="settings"] .nav-label'))[expanded?'toBeVisible':'toBeHidden']();
   // Both the outline and hub must be inside the 24px SVG viewBox, not clipped.
   const paths=await page.locator('[data-view="settings"] svg path').evaluateAll(nodes=>nodes.map(n=>{const r=n.getBBox();return {x:r.x,y:r.y,right:r.x+r.width,bottom:r.y+r.height};}));
   assert.equal(paths.length,2);
   for(const p of paths)assert.ok(p.x>=1&&p.y>=1&&p.right<=23&&p.bottom<=23,JSON.stringify(p));
  }
  await layout(1440,true);await page.screenshot({path:path.join(output,'wide.png')});
  await layout(888,false);await expect(page.locator('#nav-toggle')).toBeDisabled();
  await expect(page.locator('[data-view="settings"]')).toHaveAttribute('title','设置与状态');
  await page.getByRole('button',{name:'设置与状态',exact:true}).click();
  await expect(page.locator('#page-title')).toHaveText('设置与状态');
  await expect(page.locator('#download-directory')).toHaveText('synthetic only');
  await page.screenshot({path:path.join(output,'narrow-settings.png')});
  await page.getByRole('button',{name:'下载任务',exact:true}).click();
  await expect(page.locator('#page-title')).toHaveText('下载任务');
  await expect(page.locator('.job h3')).toHaveText('Fixture Album');
  await page.screenshot({path:path.join(output,'narrow-tasks.png')});
  await page.locator('#sidebar-search-toggle').click();await expect(page.locator('#search-input')).toBeFocused();
  assert.equal(await page.locator('#search-form').count(),1);
  const search=await page.locator('#search-form').boundingBox(),heading=await page.locator('.native-toolbar').boundingBox();
  assert.ok(search.y+search.height<=heading.y,'compact search covers the page heading');
  await page.locator('#search-input').fill('fixture');await page.locator('#search-input').press('Enter');
  await expect(page.locator('#page-title')).toHaveText('搜索');await expect(page.locator('#compact-search')).toBeHidden();
  assert.ok(await page.evaluate(()=>fixtureRequests.some(p=>p.includes('/catalog/search?')&&p.includes('term=fixture'))));
  await layout(901,true);await expect(page.locator('#search-input')).toHaveValue('fixture');
  assert.equal(await page.evaluate(()=>localStorage.getItem('native-v2-sidebar')),null,'automatic collapse persisted a manual preference');
  await layout(900,false);await layout(1440,true);
  await page.locator('#nav-toggle').click();await layout(1440,false);
  await layout(888,false);await layout(1440,false);
  assert.equal(await page.evaluate(()=>localStorage.getItem('native-v2-sidebar')),'collapsed');
  await page.locator('#nav-toggle').click();await layout(1440,true);
  await layout(700,false);await page.reload();await layout(700,false);await layout(1440,true);
  await layout(888,false);await page.locator('#sidebar-search-toggle').click();
  await page.locator('#search-input').press('Escape');await expect(page.locator('#compact-search')).toBeHidden();
  await expect(page.locator('#sidebar-search-toggle')).toBeFocused();
  assert.deepEqual(errors,[]);assert.deepEqual(remote,[]);
  console.log(JSON.stringify({passed:true,automaticCollapse:true,noOverlay:true,restoresPreference:true,compactSearch:true,settingsIconBounds:true,remoteRequests:0,output}));
 }finally{await browser.close();}
}
main().catch(error=>{console.error(error);process.exitCode=1;});

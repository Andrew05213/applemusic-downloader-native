'use strict';
const {test,expect}=require('@playwright/test');
const path=require('node:path');
const {pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;

async function install(page,kind='playlists'){
  await page.route(/^https?:\/\//,route=>route.fulfill({status:204,body:''}));
  await page.addInitScript(kind=>{
    const library=kind==='library-playlists';
    const resource={id:library?'p.large':kind==='albums'?'300':'pl.large',type:kind,title:'Large Collection',artist:'Fixture Artist',artistId:'42',artists:[{id:'42',name:'Fixture Artist'}],trackCount:250,downloadable:true,url:library?'https://music.apple.com/us/library/playlist/p.large':'https://music.apple.com/us/'+(kind==='albums'?'album/300':'playlist/pl.large')};
    const tracks=Array.from({length:250},(_,index)=>({id:String(index+1),type:'songs',title:'Track '+(index+1),artist:'Fixture Artist',duration:60000,downloadable:true,url:'https://music.apple.com/us/song/'+(index+1)}));
    // A playlist can legitimately contain the same song more than once.
    if(kind!=='albums')tracks[100]={...tracks[0]};
    tracks[149]={...tracks[149],downloadable:false,url:'',unavailableReason:'catalog_resource_unavailable'};
    const fixture=window.paginationFixture={calls:[],pending:[],holdOffset:null,failNext:false};
    window.nativeDesktop={
      getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady(fn){queueMicrotask(fn);},openLogin:async()=>{},
      api:async(target,options={})=>{
        fixture.calls.push({target,...options});const url=new URL(target,'http://fixture');
        if(url.pathname==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true}}};
        if(url.pathname==='/api/v1/catalog/home')return {groups:[{type:kind,title:'Collections',items:[resource]}]};
        if(url.pathname==='/api/v1/catalog/library')return {groups:[]};
        if(url.pathname==='/api/v1/catalog/artist-albums')return {groups:[]};
        if(url.pathname==='/api/v1/jobs')return {jobs:[]};
        if(url.pathname==='/api/v1/catalog/detail'&&url.searchParams.get('type')==='artists')return {resource:{id:'42',type:'artists',title:'Fixture Artist'},groups:[]};
        if(url.pathname==='/api/v1/catalog/'+(library?'library-detail':'detail')){
          const offset=Number(url.searchParams.get('offset'));
          if(offset>0&&fixture.failNext){fixture.failNext=false;throw Error('fixture_network_failure');}
          if(offset===fixture.holdOffset)await new Promise(resolve=>fixture.pending.push(resolve));
          // Apple returns resource metadata on page one only. Subsequent
          // relationship pages have tracks/nextOffset, without resource.
          return {...(offset===0?{resource}:{}),tracks:tracks.slice(offset,offset+100),nextOffset:offset+100<250?offset+100:null};
        }
        throw Error('unexpected pagination fixture route '+target);
      }
    };
  },kind);
  await page.goto(renderer);
  await page.locator('.catalog-title-action').filter({hasText:'Large Collection'}).click();
  await expect(page.locator('.track-row')).toHaveCount(100);
}

const offsets=page=>page.evaluate(()=>window.paginationFixture.calls.filter(call=>/\/catalog\/(?:library-)?detail\?/.test(call.target)&&new URL(call.target,'http://fixture').searchParams.get('type')!=='artists').map(call=>Number(new URL(call.target,'http://fixture').searchParams.get('offset'))));

test.afterEach(async({page},info)=>{
  if(info.status!==info.expectedStatus)await page.screenshot({path:info.outputPath('pagination-failure.png'),fullPage:true});
});

for(const kind of ['playlists','library-playlists','albums']){
  test(`${kind} appends all 250 tracks from relation-only pages without replacing its header`,async({page})=>{
    await install(page,kind);
    await page.evaluate(()=>window.originalCollectionHeader=document.querySelector('.detail-hero'));
    const more=page.locator('#catalog-more');
    await more.click();await expect(page.locator('.track-row')).toHaveCount(200);
    await more.click();await expect(page.locator('.track-row')).toHaveCount(250);
    await expect(more).toBeHidden();
    await expect(page.locator('.detail-hero h1')).toHaveText('Large Collection');
    expect(await page.evaluate(()=>window.originalCollectionHeader===document.querySelector('.detail-hero'))).toBe(true);
    expect(await page.locator('.track-row > span:first-child').allTextContents()).toEqual(Array.from({length:250},(_,i)=>String(i+1)));
    await expect(page.locator('.track-row').nth(249)).toContainText('Track 250');
    await expect(page.locator('.track-row').nth(149).locator('[data-download]')).toBeDisabled();
    if(kind!=='albums')await expect(page.getByRole('button',{name:'播放 Track 1',exact:true})).toHaveCount(2);
    await expect(page.locator('.detail-metadata')).toHaveText('250 首歌曲 · 250 分钟');
    expect(await offsets(page)).toEqual([0,100,200]);
    expect(await page.evaluate(()=>window.paginationFixture.calls.some(call=>call.method==='POST'))).toBe(false);
  });
}

test('a failed page keeps loaded tracks and retries that offset without dropping or duplicating entries',async({page})=>{
  await install(page);
  await page.evaluate(()=>window.paginationFixture.failNext=true);
  await page.locator('#catalog-more').click();
  await expect(page.locator('#notice')).toContainText('目录暂不可用');
  await expect(page.locator('.track-row')).toHaveCount(100);
  await expect(page.locator('#catalog-more')).toBeVisible();
  await page.locator('#catalog-more').click();await expect(page.locator('.track-row')).toHaveCount(200);
  await page.locator('#catalog-more').click();await expect(page.locator('.track-row')).toHaveCount(250);
  expect(await offsets(page)).toEqual([0,100,100,200]);
});

test('returning from an artist restores loaded collection pages and the next cursor without refetching',async({page})=>{
  await install(page);
  await page.locator('#catalog-more').click();await expect(page.locator('.track-row')).toHaveCount(200);
  await page.locator('.detail-artist-link').click();await expect(page.locator('.artist-hero h1')).toHaveText('Fixture Artist');
  await page.locator('#browse-back').click();await expect(page.locator('.track-row')).toHaveCount(200);
  await page.locator('#catalog-more').click();await expect(page.locator('.track-row')).toHaveCount(250);
  expect(await offsets(page)).toEqual([0,100,200]);
});

test('leaving a collection while a later page is pending ignores that late page',async({page})=>{
  await install(page);
  await page.evaluate(()=>window.paginationFixture.holdOffset=100);
  await page.locator('#catalog-more').click();
  await expect.poll(()=>page.evaluate(()=>window.paginationFixture.pending.length)).toBe(1);
  await page.locator('#browse-back').click();
  await expect(page.getByRole('heading',{name:'Collections',exact:true})).toBeVisible();
  await page.evaluate(()=>window.paginationFixture.pending.splice(0).forEach(resolve=>resolve()));
  await expect(page.locator('.track-row')).toHaveCount(0);
  await expect(page.locator('.detail-hero')).toHaveCount(0);
  await expect(page.locator('#catalog-more')).toBeHidden();
});

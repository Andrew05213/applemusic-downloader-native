'use strict';
const {test,expect}=require('@playwright/test'),path=require('node:path'),{pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;
async function install(page,{empty=false,failed=false}={}){
 await page.setViewportSize({width:1280,height:900});
 await page.addInitScript(({empty,failed})=>{
  const item=(id,type,title)=>({id,type,title,artist:'Fixture Artist',url:'https://music.apple.com/us/'+(type==='songs'?'song':'album')+'/'+id,downloadable:true});
  const songs=[item('102','songs','First Ranked Fixture'),item('101','songs','Second Ranked Fixture')],album=item('201','albums','Chart Album Fixture');
  const f=window.chartFixture={calls:[],empty,failed};
  window.nativeDesktop={getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady:fn=>queueMicrotask(fn),openLogin:async()=>{},refreshCatalog:async()=>{},
   api:async(target,options={})=>{f.calls.push({target,...options});const u=new URL(target,'http://fixture');
    if(target==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true}}};
    if(u.pathname==='/api/v1/jobs'){if(options.method==='POST')return {jobs:[{id:'a'.repeat(32),url:options.body.urls[0]}]};return {jobs:[]};}
    if(target==='/api/v1/browser-download/resolve')return {url:options.body.url};
    if(u.pathname==='/api/v1/catalog/charts'){if(f.failed)throw Error('native_api_failed');return {schemaVersion:1,storefront:u.searchParams.get('region'),groups:f.empty?[]:[{type:'songs',items:songs},{type:'albums',items:[album]}]};}
    if(u.pathname==='/api/v1/catalog/detail')return {resource:album,tracks:songs};
    if(u.pathname.startsWith('/api/v1/catalog/'))return {groups:[]};
    return {};
   }
  };
 },{empty,failed});
 await page.goto(renderer);await expect(page.locator('#notice')).toHaveText('暂无内容。');
}

test('charts use the selected storefront, preserve rank, open details and still queue ALAC downloads',async({page})=>{
 await install(page);await page.locator('#region').selectOption('cn');await page.getByRole('button',{name:'排行榜',exact:true}).click();
 await expect(page.locator('#page-title')).toHaveText('排行榜');await expect(page.getByRole('heading',{name:'歌曲',exact:true})).toBeVisible();await expect(page.getByRole('heading',{name:'专辑',exact:true})).toBeVisible();
 await expect(page.locator('.catalog-title-action')).toHaveText(['First Ranked Fixture','Second Ranked Fixture','Chart Album Fixture']);
 const chartRequest=await page.evaluate(()=>window.chartFixture.calls.find(call=>call.target.startsWith('/api/v1/catalog/charts'))?.target);
 expect(chartRequest).toBe('/api/v1/catalog/charts?region=cn');
 const download=page.getByRole('button',{name:'下载 First Ranked Fixture',exact:true});await expect(download).toBeEnabled();await download.click();await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
 expect(await page.evaluate(()=>window.chartFixture.calls.find(call=>call.target==='/api/v1/jobs'&&call.method==='POST').body)).toEqual({urls:['https://music.apple.com/us/song/102'],titles:['First Ranked Fixture'],quality:'alac',existingPolicy:'skip'});
 await page.locator('.catalog-title-action').filter({hasText:'Chart Album Fixture'}).click();await expect(page.locator('.detail-hero h1')).toHaveText('Chart Album Fixture');
 await page.getByRole('button',{name:'返回上一页',exact:true}).click();await expect(page.locator('.catalog-title-action')).toHaveText(['First Ranked Fixture','Second Ranked Fixture','Chart Album Fixture']);
 expect(await page.evaluate(()=>window.chartFixture.calls.filter(call=>call.target.startsWith('/api/v1/catalog/charts')).length)).toBe(1);
});

test('empty chart results are not presented as a login or network failure',async({page})=>{
 await install(page,{empty:true});await page.getByRole('button',{name:'排行榜',exact:true}).click();
 await expect(page.locator('#page-title')).toHaveText('排行榜');await expect(page.locator('#notice')).toHaveText('暂无内容。');await expect(page.locator('.catalog-card')).toHaveCount(0);await expect(page.locator('#refresh-catalog')).not.toHaveAttribute('aria-busy','true');
});

test('chart request failure can be retried without changing login or losing the selected feed',async({page})=>{
 await install(page,{failed:true});await page.getByRole('button',{name:'排行榜',exact:true}).click();await expect(page.locator('#notice')).toContainText('目录暂不可用');
 await page.evaluate(()=>window.chartFixture.failed=false);await page.getByRole('button',{name:'刷新当前目录',exact:true}).click();
 await expect(page.locator('.catalog-title-action')).toHaveText(['First Ranked Fixture','Second Ranked Fixture','Chart Album Fixture']);await expect(page.locator('#page-title')).toHaveText('排行榜');await expect(page.locator('#notice')).toHaveText('');
 expect(await page.evaluate(()=>window.chartFixture.calls.filter(call=>call.target.startsWith('/api/v1/catalog/charts')).length)).toBe(2);
});

'use strict';
const {test,expect}=require('@playwright/test'),path=require('node:path'),{pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;

async function install(page,{width=1280,empty=false,failed=false}={}){
 await page.setViewportSize({width,height:900});
 await page.route('https://is1-ssl.mzstatic.com/discover-fixture/*',route=>{
  const colors=['#943839','#205872','#67396e','#c0852e','#315749','#385697'];
  const color=colors[Number(new URL(route.request().url()).pathname.split('/').pop())%colors.length];
  return route.fulfill({contentType:'image/svg+xml',body:`<svg xmlns="http://www.w3.org/2000/svg" width="960" height="540"><rect width="960" height="540" fill="${color}"/><path d="M0 420Q200 90 440 380T960 190V540H0Z" fill="#fff" opacity=".15"/><circle cx="730" cy="145" r="160" fill="#fff" opacity=".16"/></svg>`});
 });
 await page.addInitScript(({empty,failed})=>{
  const item=(id,type,title,region='cn')=>({id,type,title,artist:'音乐人',artwork:'https://is1-ssl.mzstatic.com/discover-fixture/'+id,url:'https://music.apple.com/'+region+'/'+(type==='songs'?'song':'album')+'/'+id,downloadable:true});
  const songs=Array.from({length:12},(_,i)=>item(String(100+i),'songs',['夏日来信','下一站','日落之下'][i%3]+(i>2?' '+(i+1):'')));
  const featured=['城市之间','听见风的声音','夜晚的最后一首歌','早晨第一班车','把心事唱成歌'].map((title,i)=>({...item(String(201+i),'albums',title),editorialLabel:i?'编辑推荐':'全新专辑',description:'跟着新声音出发，听见流行乐里的不同色彩。'}));
  const f=window.discoveryFixture={calls:[],empty,failed};
  window.nativeDesktop={getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady:fn=>queueMicrotask(fn),openLogin:async()=>{},refreshCatalog:async()=>{},
   api:async(target,options={})=>{f.calls.push({target,...options});const u=new URL(target,'http://fixture');
    if(target==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true}}};
    if(u.pathname==='/api/v1/jobs'){if(options.method==='POST')return {jobs:[{id:'a'.repeat(32),url:options.body.urls[0]}]};return {jobs:[]};}
    if(target==='/api/v1/browser-download/resolve')return {url:options.body.url};
    if(u.pathname==='/api/v1/catalog/discover'){
     if(f.failed)throw Error('native_api_failed');
     const region=u.searchParams.get('region');
     return {schemaVersion:1,storefront:region,groups:f.empty?[]:[
      {layout:'featured',title:'编辑精选',items:featured},
      {layout:'shelf',title:region==='cn'?'本周新发行':'New This Week',items:['城市之间','全新的今天','流光','第四封信','森林上空','午夜列车'].map((title,i)=>item(String(201+i),'albums',title,region))},
      {layout:'tracks',title:'本周新歌',items:songs}
     ]};
    }
    if(u.pathname==='/api/v1/catalog/detail')return {resource:featured.find(value=>value.id===u.searchParams.get('id')),tracks:songs.slice(0,3)};
    if(u.pathname.startsWith('/api/v1/catalog/'))return {groups:[]};
    return {};
   }
  };
 },{empty,failed});
 await page.goto(renderer);await expect(page.locator('#notice')).toHaveText('暂无内容。');
}
const choose=page=>page.getByRole('button',{name:'新发现',exact:true});
const fetches=page=>page.evaluate(()=>window.discoveryFixture.calls.filter(call=>call.target.startsWith('/api/v1/catalog/discover')).map(call=>call.target));

test('discovery preserves editorial order, storefront, native details and downloads',async({page},testInfo)=>{
 await install(page);
 await expect(page.getByRole('button',{name:'排行榜',exact:true})).toHaveCount(0);
 await page.locator('#region').selectOption('cn');await choose(page).click();
 await expect(choose(page)).toHaveAttribute('aria-current','page');
 await expect(page.locator('#page-title')).toHaveText('新发现');
 await expect(page.locator('.discover-section h2')).toHaveText(['编辑精选','本周新发行','本周新歌']);
 await expect(page.locator('.discover-feature-cover img').first()).toHaveJSProperty('complete',true);
 expect(await fetches(page)).toEqual(['/api/v1/catalog/discover?region=cn']);
 const shelf=page.locator('.discover-feature-shelf');
 const next=page.getByRole('button',{name:'向右浏览 编辑精选',exact:true});
 const previous=page.getByRole('button',{name:'向左浏览 编辑精选',exact:true});
 await expect(previous).toHaveAttribute('aria-disabled','true');
 await next.click();
 await expect.poll(()=>shelf.evaluate(el=>el.scrollLeft)).toBeGreaterThan(0);
 await shelf.getByRole('button',{name:'查看 把心事唱成歌 详情'}).last().click();
 await expect(page.locator('.detail-hero h1')).toHaveText('把心事唱成歌');
 await page.getByRole('button',{name:'返回上一页',exact:true}).click();
 await expect(shelf).toBeVisible();
 await expect.poll(()=>shelf.evaluate(el=>el.scrollLeft)).toBeGreaterThan(0);
 expect(await fetches(page)).toHaveLength(1);
 await previous.click();
 await expect(previous).toHaveAttribute('aria-disabled','true');
 const download=page.getByRole('button',{name:'下载 夏日来信',exact:true});
 await expect(download).toBeEnabled();await download.click();
 await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
 expect(await page.evaluate(()=>window.discoveryFixture.calls.find(call=>call.target==='/api/v1/jobs'&&call.method==='POST').body)).toEqual({urls:['https://music.apple.com/cn/song/100'],titles:['夏日来信'],quality:'alac',existingPolicy:'skip'});
 await page.locator('#region').selectOption('us');
 await expect(page.locator('.discover-section h2')).toHaveText(['编辑精选','New This Week','本周新歌']);
 expect(await fetches(page)).toEqual(['/api/v1/catalog/discover?region=cn','/api/v1/catalog/discover?region=us']);
 await page.screenshot({path:testInfo.outputPath('discover-desktop.png'),animations:'disabled'});
});

test('discovery shelves stay inside a narrow window and empty results do not look like login errors',async({page},testInfo)=>{
 await install(page,{width:640});
 await choose(page).click();await expect(page.locator('.discover-feature')).toHaveCount(5);
 await expect.poll(()=>page.locator('.discover-feature-cover img').first().evaluate(img=>img.naturalWidth)).toBeGreaterThan(0);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBe(640);
 expect(await page.locator('.discover-feature-shelf').evaluate(el=>el.scrollWidth>el.clientWidth)).toBe(true);
 await page.screenshot({path:testInfo.outputPath('discover-narrow.png'),animations:'disabled'});
 await page.evaluate(()=>window.discoveryFixture.empty=true);
 await page.locator('#region').selectOption('cn',{force:true});
 await expect(page.locator('#notice')).toHaveText('当前地区暂无发现内容。');
 await expect(page.locator('.discover-section')).toHaveCount(0);
});

test('discovery offers an inline retry and does not display the previous region after an error',async({page})=>{
 await install(page);
 await choose(page).click();await expect(page.locator('.discover-feature')).toHaveCount(5);
 await page.evaluate(()=>window.discoveryFixture.failed=true);
 await page.locator('#region').selectOption('cn');
 await expect(page.locator('#notice')).toContainText('新发现暂时无法载入');
 await expect(page.locator('.discover-section')).toHaveCount(0);
 await page.evaluate(()=>window.discoveryFixture.failed=false);
 await page.getByRole('button',{name:'重新载入',exact:true}).click();
 await expect(page.locator('.discover-feature')).toHaveCount(5);
 await expect(page.locator('#notice')).toHaveText('');
 expect(await fetches(page)).toEqual(['/api/v1/catalog/discover?region=us','/api/v1/catalog/discover?region=cn','/api/v1/catalog/discover?region=cn']);
});

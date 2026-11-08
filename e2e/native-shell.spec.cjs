'use strict';
const {test,expect}=require('@playwright/test'),path=require('node:path'),{pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;
async function install(page,{clock=false}={}){
 if(clock)await page.clock.install();
 await page.route(/^https?:\/\//,route=>route.fulfill({status:204,body:''}));
 await page.addInitScript(()=>{
  HTMLMediaElement.prototype.play=function(){return Promise.resolve();};HTMLMediaElement.prototype.pause=function(){};HTMLMediaElement.prototype.load=function(){};
  const resource=(id,title='Fixture Album')=>({id,type:'albums',title,artist:'Fixture Artist',artistId:'42',relatedArtist:'Fixture Artist',genres:['J-Pop'],releaseDate:'2026-10-23',copyright:'© 2026 Fixture Records',audioVariants:['lossless'],trackCount:2,url:'https://music.apple.com/us/album/'+id,downloadable:true});
  const album=resource('300'),other=resource('302','Other Work'),library={id:'p.private',type:'library-playlists',title:'我的私人歌单',artist:'Fixture',url:'https://music.apple.com/us/library/playlist/p.private',downloadable:true};
  const tracks=[1,2].map(n=>({id:String(700+n),type:'songs',title:'Song '+n,artist:'Fixture Artist',duration:180000,url:'https://music.apple.com/us/song/'+(700+n),downloadable:true}));
  window.shellFixture={calls:[],jobs:[{id:'job-one',title:'正在下载的专辑',url:album.url,status:'running',total:10,succeeded:2,failed:1,skipped:0,unavailable:0,transfer:{at:Date.now(),phase:'downloading',bytes:4000000,bytesPerSecond:2097152}}],hold:false,failed:false,relatedFailed:false,holdRelated:false,pending:[],pendingRelated:[],cache:{available:true,limitMB:500,usedBytes:3145728,entries:20},prefetch:[]};
  window.nativeDesktop={getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady(fn){queueMicrotask(fn);},openLogin:async()=>{},exportDiagnostics:async()=>true,
   cacheInfo:async()=>({...window.shellFixture.cache}),setCacheLimit:async limit=>{window.shellFixture.cache.limitMB=limit;},clearCache:async()=>{window.shellFixture.cache.usedBytes=0;window.shellFixture.cache.entries=0;},refreshCatalog:async()=>{window.shellFixture.refreshed=true;},prefetchCatalog:async target=>window.shellFixture.prefetch.push(target),
   api:async(target,options={})=>{const f=window.shellFixture;f.calls.push({target,...options});const u=new URL(target,'http://fixture');
    if(u.pathname==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true}}};
    if(target==='/api/v1/jobs?active=true&limit=100'){if(f.hold)await new Promise(resolve=>f.pending.push(resolve));if(f.failed)throw Error('offline');return {jobs:structuredClone(f.jobs)};}
    if(target==='/api/v1/jobs?limit=30')return {jobs:structuredClone(f.jobs)};
    if(u.pathname==='/api/v1/catalog/home')return {groups:[{type:'albums',title:'最近播放',items:[album]}]};
    if(u.pathname==='/api/v1/catalog/library')return {groups:[{type:'library-playlists',title:'我的歌单',items:[library]}]};
    if(u.pathname==='/api/v1/catalog/library-detail')return {resource:library,tracks};
    if(u.pathname==='/api/v1/catalog/detail')return {resource:u.searchParams.get('id')==='302'?other:album,tracks};
    if(u.pathname==='/api/v1/catalog/artist-albums'){if(f.holdRelated)await new Promise(resolve=>f.pendingRelated.push(resolve));if(f.relatedFailed)throw Error('offline');return {groups:[{type:'albums',items:[album,other,other]}]};}
    if(target==='/api/v1/settings/download')return {settings:{songFileFormat:'{title}'}};
    if(target==='/api/v1/settings/download-concurrency')return {downloadConcurrency:3};
    if(target==='/api/native/state')return {runtime:{phase:'ready'},downloadDirectory:'D:\\Music'};
    throw Error('unexpected route '+target);
   }
  };
 });
 await page.goto(renderer);await expect(page.getByRole('heading',{name:'最近播放',exact:true})).toBeVisible();await expect(page.locator('#monitor-speed')).toHaveText('2.0 MiB/s');
}
for(const width of [1800,1280,800,390])test(`official-style sidebar and download chrome fit ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:900});await install(page);
 await expect(page.locator('.browse-tabs')).toHaveCount(0);
 await expect(page.locator('#sidebar [data-feed]')).toHaveCount(3);
 const back=await page.locator('#browse-back').boundingBox();expect(back.x).toBeLessThanOrEqual(12);expect(back.y).toBeLessThanOrEqual(16);
 await expect(page.locator('#monitor-progress')).toHaveAttribute('value','3');await expect(page.locator('#monitor-progress')).toHaveAttribute('max','10');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:`test-results/native-shell-${width}.png`,fullPage:true});
});

for(const width of [1280,800,390])test(`page, section and item headings keep a clear type hierarchy at ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:900});await install(page);
 const size=locator=>locator.evaluate(el=>parseFloat(getComputedStyle(el).fontSize));
 const pageSize=await size(page.locator('#page-title'));
 const sectionSize=await size(page.locator('#catalog-content h2').first());
 const itemSize=await size(page.locator('.catalog-card h3').first());
 expect(pageSize).toBeGreaterThan(sectionSize);expect(sectionSize).toBe(20);expect(itemSize).toBe(14);expect(sectionSize-itemSize).toBeGreaterThanOrEqual(6);
 expect(await size(page.locator('.catalog-title-action').first())).toBe(itemSize);
 await page.locator('.catalog-title-action').first().click();await expect(page.locator('.detail-related h2')).toBeVisible();
 expect(await size(page.locator('.detail-hero h1'))).toBeGreaterThan(await size(page.locator('.detail-related h2')));
 expect(await size(page.locator('.detail-related h2'))).toBeGreaterThan(await size(page.locator('.detail-related h3').first()));
 await page.getByRole('button',{name:'设置与状态',exact:true}).click();
 expect(await size(page.locator('.settings-group h2').first())).toBe(20);expect(await size(page.locator('.settings-group h3').first())).toBe(14);
});
test('monitor runs on every page, counts processed tracks honestly and opens task details',async({page})=>{
 await install(page,{clock:true});await expect(page.locator('#monitor-detail')).toHaveText('正在下载 · 已处理 3 / 10 首');
 await page.getByRole('button',{name:'查看下载进度',exact:true}).click();await expect(page.locator('#tasks-view')).toBeVisible();
 await page.evaluate(()=>{window.shellFixture.jobs[0].transfer={...window.shellFixture.jobs[0].transfer,phase:'decrypting',bytesPerSecond:999999,at:Date.now()};});await page.clock.fastForward(1000);
 await expect(page.locator('#monitor-speed')).toHaveText('—');await expect(page.locator('#monitor-detail')).toContainText('正在解密');
 await page.evaluate(()=>{window.shellFixture.jobs=[];});await page.clock.fastForward(1000);await expect(page.locator('#monitor-title')).toHaveText('暂无下载任务');await expect(page.locator('#monitor-progress')).toHaveAttribute('value','0');
});
test('slow monitor requests coalesce and stale/error snapshots never keep old speed or fake a completed bar',async({page})=>{
 await install(page,{clock:true});await page.evaluate(()=>window.shellFixture.hold=true);await page.clock.fastForward(6000);
 expect(await page.evaluate(()=>window.shellFixture.pending.length)).toBe(1);await expect(page.locator('#monitor-speed')).toHaveText('—');await expect(page.locator('#monitor-progress')).not.toHaveAttribute('value');
 await page.evaluate(()=>{window.shellFixture.failed=true;window.shellFixture.hold=false;window.shellFixture.pending.splice(0).forEach(resolve=>resolve());});await expect(page.locator('#monitor-detail')).toHaveText('连接中断，正在重试…');
 await page.evaluate(()=>{window.shellFixture.failed=false;window.shellFixture.jobs[0].transfer.at=Date.now();});await page.clock.fastForward(1000);await expect(page.locator('#monitor-speed')).toHaveText('2.0 MiB/s');
});
test('detail shows real metadata and authoritative artist works; back restores each detail without refetch',async({page})=>{
 await install(page);await page.locator('.catalog-title-action').filter({hasText:'Fixture Album'}).click();
 await expect(page.locator('.detail-traits')).toHaveText('J-Pop · 2026年 · 无损');await expect(page.locator('.detail-metadata')).toHaveText('2026年10月23日\n2 首歌曲 · 6 分钟\n© 2026 Fixture Records');
 await expect(page.locator('.detail-related .catalog-card')).toHaveCount(1);await expect(page.locator('.detail-related h2')).toHaveText('更多Fixture Artist的作品');
 await page.locator('.detail-related .catalog-title-action').click();await expect(page.locator('.detail-hero h1')).toHaveText('Other Work');
 await page.locator('#browse-back').click();await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
 await page.locator('#browse-back').click();await expect(page.getByRole('heading',{name:'最近播放',exact:true})).toBeVisible();
 expect(await page.evaluate(()=>window.shellFixture.calls.filter(call=>call.target.startsWith('/api/v1/catalog/detail?')).length)).toBe(2);
});
test('related failure is local and retryable, and a late response cannot overwrite another page',async({page})=>{
 await install(page);await page.evaluate(()=>window.shellFixture.relatedFailed=true);await page.locator('.catalog-title-action').click();await expect(page.locator('.detail-related')).toContainText('其他作品暂时无法载入');
 await page.evaluate(()=>window.shellFixture.relatedFailed=false);await page.locator('.detail-related').getByRole('button',{name:'重试',exact:true}).click();await expect(page.locator('.detail-related .catalog-card')).toHaveCount(1);
 await page.locator('#browse-back').click();await page.evaluate(()=>window.shellFixture.holdRelated=true);await page.locator('.catalog-title-action').click();await expect.poll(()=>page.evaluate(()=>window.shellFixture.pendingRelated.length)).toBe(1);await page.locator('#browse-back').click();await page.evaluate(()=>window.shellFixture.pendingRelated.splice(0).forEach(resolve=>resolve()));await expect(page.locator('.detail-related')).toHaveCount(0);
});
test('sidebar lists real private playlists, compact search uses a content row, and hover preloads at most once',async({page})=>{
 await install(page);await expect(page.locator('#sidebar-playlists')).toContainText('我的私人歌单');
 await page.locator('.catalog-artwork-action').hover();await page.waitForTimeout(200);await page.locator('.catalog-title-action').focus();await page.waitForTimeout(200);expect(await page.evaluate(()=>window.shellFixture.prefetch.length)).toBe(1);
 await page.locator('#sidebar-playlists button').click();await expect(page.locator('.detail-hero h1')).toHaveText('我的私人歌单');
 await page.setViewportSize({width:390,height:800});
 await expect(page.locator('#nav-toggle')).toBeDisabled();await expect(page.locator('#nav-toggle')).toHaveAttribute('aria-expanded','false');
 await page.getByRole('button',{name:'展开搜索',exact:true}).click();await expect(page.getByRole('searchbox')).toBeFocused();
 await expect(page.locator('#compact-search #search-form')).toHaveCount(1);await expect(page.locator('#search-form')).toHaveCount(1);
 const side=await page.locator('#sidebar').boundingBox(),content=await page.locator('.native-content').boundingBox();
 expect(side.width).toBe(64);expect(content.x).toBeGreaterThanOrEqual(side.x+side.width);
 const form=await page.locator('#search-form').boundingBox(),detail=await page.locator('.detail-hero').boundingBox();
 expect(form.y+form.height).toBeLessThanOrEqual(detail.y);
 await page.keyboard.press('Escape');await expect(page.locator('#compact-search')).toBeHidden();await expect(page.locator('#sidebar-search-toggle')).toBeFocused();
 await expect(page.locator('.detail-hero h1')).toHaveText('我的私人歌单');
 await page.setViewportSize({width:1280,height:900});await expect(page.locator('#nav-toggle')).toHaveAttribute('aria-expanded','true');
 await expect(page.locator('#sidebar-playlists')).toContainText('我的私人歌单');
});
test('settings changes the 500 MB budget, rejects blank input and clears only cache',async({page})=>{
 await install(page);await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.locator('#cache-limit')).toHaveValue('500');await expect(page.locator('#cache-usage')).toContainText('3.0 MiB');
 await page.locator('#cache-limit').fill('');await page.getByRole('button',{name:'调整容量',exact:true}).click();await expect(page.locator('#cache-feedback')).toContainText('请输入');
 await page.locator('#cache-limit').fill('250');await page.getByRole('button',{name:'调整容量',exact:true}).click();await expect(page.locator('#cache-usage')).toContainText('250 MB');
 await page.getByRole('button',{name:'清理缓存',exact:true}).click();await expect(page.locator('#cache-usage')).toContainText('0 B');await expect(page.locator('#cache-feedback')).toContainText('下载文件与登录会话不受影响');
});
test('reduced motion disables reveal animation while normal chrome uses partial glass',async({page})=>{
 await page.emulateMedia({reducedMotion:'reduce'});await install(page);expect(await page.locator('#catalog-content').evaluate(el=>getComputedStyle(el).animationName)).toBe('none');expect(await page.locator('#app-header').evaluate(el=>getComputedStyle(el).backdropFilter)).toContain('blur');
});

async function weighted(page,patch={}){
 await page.evaluate(patch=>{
  const f=window.shellFixture,j=f.jobs[0];Object.assign(j,{attempt:1,total:1,succeeded:0,failed:0,skipped:0,unavailable:0,items:[{sourceId:'701',name:'正在下载的歌曲',status:'running'}]},patch.job||{});
  j.transfer={at:Date.now(),phase:'downloading',bytes:5242880,bytesPerSecond:1048576,items:[{sourceId:'701',bytes:5242880,totalBytes:10485760,estimated:true,processedBytes:0,progress:.25,phase:'downloading'}],...patch.transfer};
 },patch);await page.clock.fastForward(1000);
}
const cardProgress=page=>page.locator('[data-job-id="job-one"] progress');
async function expectProgress(page,value){await expect.poll(()=>page.locator('#monitor-progress').evaluate(el=>el.value)).toBeCloseTo(value,5);await expect.poll(()=>cardProgress(page).evaluate(el=>el.value)).toBeCloseTo(value,5);}

test('weighted bars use 1:1 download/processing shares and wait for durable completion',async({page})=>{
 await install(page,{clock:true});await page.getByRole('button',{name:'查看下载进度',exact:true}).click();await weighted(page);
 await expectProgress(page,.25);await expect(page.locator('.job-transfer')).toContainText('预估 25%');await expect(page.locator('.job-transfer')).toContainText('5.0 MiB / 10.0 MiB');await expect(page.locator('.job-transfer')).not.toContainText('约');
 await weighted(page,{transfer:{bytes:10485760,phase:'decrypting',items:[{sourceId:'701',bytes:10485760,totalBytes:10485760,processedBytes:0,progress:.5,phase:'decrypting'}]}});
 await expectProgress(page,.5);await expect(page.locator('#monitor-speed')).toHaveText('—');await expect(page.locator('.job-transfer')).not.toContainText('约');
 await weighted(page,{transfer:{phase:'decrypting',items:[{sourceId:'701',bytes:10485760,totalBytes:10485760,processedBytes:5242880,progress:.75,phase:'decrypting'}]}});await expectProgress(page,.75);
 await weighted(page,{transfer:{phase:'publish',items:[{sourceId:'701',bytes:10485760,totalBytes:10485760,processedBytes:10485760,progress:.99,phase:'publish'}]}});await expectProgress(page,.99);
 await page.evaluate(()=>{Object.assign(window.shellFixture.jobs[0],{status:'succeeded',succeeded:1,items:[{sourceId:'701',status:'succeeded'}]});});await page.clock.fastForward(1000);
 await expect(cardProgress(page)).toHaveAttribute('value','1');await expect(page.locator('.job-transfer')).toHaveText('');
});

test('parallel partial items aggregate without double counting completed receipts',async({page})=>{
 await install(page,{clock:true});await page.getByRole('button',{name:'查看下载进度',exact:true}).click();
 await weighted(page,{job:{total:3,succeeded:1,items:[{sourceId:'700',status:'succeeded'},{sourceId:'701',status:'running'},{sourceId:'702',status:'running'}]},transfer:{items:[{sourceId:'700',progress:.99},{sourceId:'701',bytes:50,totalBytes:100,progress:.25},{sourceId:'702',bytes:100,totalBytes:100,processedBytes:50,progress:.75}]}});
 await expectProgress(page,2);await expect(page.locator('#monitor-detail')).toContainText('预估 66.7%');
});

test('estimate corrections cannot rewind a running attempt; retries reset its progress',async({page})=>{
 await install(page,{clock:true});await page.getByRole('button',{name:'查看下载进度',exact:true}).click();await weighted(page);await expectProgress(page,.25);
 await weighted(page,{transfer:{items:[{sourceId:'701',bytes:5242880,totalBytes:20971520,progress:.125,phase:'downloading'}]}});await expectProgress(page,.25);
 await weighted(page,{job:{attempt:2},transfer:{items:[{sourceId:'701',bytes:1048576,totalBytes:10485760,progress:.05,phase:'downloading'}]}});await expectProgress(page,.05);
});

test('unknown size and stale telemetry stay indeterminate instead of advancing with time',async({page})=>{
 await install(page,{clock:true});await page.getByRole('button',{name:'查看下载进度',exact:true}).click();
 await weighted(page,{transfer:{items:[{sourceId:'701',bytes:5242880,totalBytes:0,progress:0,phase:'downloading'}]}});
 await expect(cardProgress(page)).not.toHaveAttribute('value');await expect(page.locator('.job-transfer')).toContainText('总量待确认');
 await weighted(page);await expectProgress(page,.25);
 await page.clock.fastForward(6000);await expect(cardProgress(page)).not.toHaveAttribute('value');await expect(page.locator('#monitor-progress')).not.toHaveAttribute('value');await expect(page.locator('.job-transfer')).toHaveText('等待传输状态…');
});

for(const width of [1280,390])test(`weighted progress and transfer size fit ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:844});await install(page,{clock:true});await page.getByRole('button',{name:'查看下载进度',exact:true}).click();await weighted(page);await expectProgress(page,.25);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.screenshot({path:`test-results/native-weighted-progress-${width}.png`,fullPage:true});
});

for(const width of [1280,390])test(`whole collection totals survive three concurrent slots at ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:844});await install(page,{clock:true});await page.getByRole('button',{name:'查看下载进度',exact:true}).click();
 const mib=1048576, totals={items:10,unknownItems:0,bytes:15*mib,totalBytes:100*mib,processedBytes:0,estimated:true,progress:.075};
 await weighted(page,{job:{total:10},transfer:{totals}});await expectProgress(page,.75);
 await expect(page.locator('.job-transfer')).toContainText('已下载 15.0 MiB / 预计总量 100.0 MiB');await expect(page.locator('#monitor-detail')).toContainText('100.0 MiB');
 await weighted(page,{job:{total:10,succeeded:3},transfer:{items:[],totals:{...totals,bytes:30*mib,processedBytes:30*mib,progress:.3}}});await expectProgress(page,3);
 await weighted(page,{job:{total:10,succeeded:3},transfer:{totals:{...totals,bytes:45*mib,processedBytes:30*mib,progress:.375}}});await expectProgress(page,3.75);
 await expect(page.locator('.job-transfer')).toContainText('45.0 MiB / 预计总量 100.0 MiB');await expect(page.locator('.job-transfer')).not.toContainText('约');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.screenshot({path:`test-results/native-whole-progress-${width}.png`,fullPage:true});
});

test('whole collection missing sizes are indeterminate and malformed totals fall back safely',async({page})=>{
 await install(page,{clock:true});await page.getByRole('button',{name:'查看下载进度',exact:true}).click();
 const totals={items:10,unknownItems:1,bytes:500,totalBytes:1000,processedBytes:0,estimated:true,progress:.25};
 await weighted(page,{job:{total:10},transfer:{totals}});await expect(cardProgress(page)).not.toHaveAttribute('value');await expect(page.locator('.job-transfer')).toContainText('总量待确认');await expect(page.locator('.job-transfer')).not.toContainText('预计总量');
 await weighted(page,{transfer:{totals:{...totals,bytes:-1}}});await expectProgress(page,.25);await expect(page.locator('.job-transfer')).not.toContainText('NaN');
});

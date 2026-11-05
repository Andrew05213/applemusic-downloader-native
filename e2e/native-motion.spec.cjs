'use strict';
const {test,expect}=require('@playwright/test'),path=require('node:path'),{pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;
async function install(page){
 await page.setViewportSize({width:1280,height:850});
 await page.route(/^https?:\/\//,route=>route.fulfill({status:204,body:''}));
 await page.addInitScript(()=>{
  const original=Element.prototype.animate;
  const f=window.motionFixture={animations:[],calls:[],holdDelete:false,pending:[],jobs:[{id:'a'.repeat(32),title:'Midnight Sessions',url:'https://music.apple.com/us/album/300',status:'succeeded',total:2,succeeded:2,failed:0,skipped:0,unavailable:0,items:[{sourceId:'701',name:'Afterglow',status:'succeeded'}]}]};
  Element.prototype.animate=function(frames,options){const animation=original.call(this,frames,options);f.animations.push({target:this.id||this.className,frames,options,animation});return animation;};
  HTMLMediaElement.prototype.play=function(){return Promise.resolve();};HTMLMediaElement.prototype.pause=function(){};HTMLMediaElement.prototype.load=function(){};
  const albums=['Midnight Sessions','Soft Focus','Blue Hour','Quiet City','Afterglow','Open Skies'].map((title,i)=>({id:String(300+i),type:'albums',title,artist:'Studio Collective',artistId:'42',artwork:'https://is1-ssl.mzstatic.com/fixture/'+i+'.jpg',url:'https://music.apple.com/us/album/'+(300+i),downloadable:true}));
  window.nativeDesktop={getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady:fn=>queueMicrotask(fn),openLogin:async()=>{},
   artworkURL:url=>{const colors=['#b75e7b','#8b7bb4','#426d96','#b9885d','#6c9384','#b17665'],i=Number(url.match(/(\d+)\.jpg/)?.[1]||0);return 'data:image/svg+xml,'+encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="500" height="500"><rect width="500" height="500" fill="'+colors[i%6]+'"/><circle cx="250" cy="250" r="145" fill="none" stroke="white" stroke-opacity=".24" stroke-width="70"/><text x="35" y="447" font-family="sans-serif" font-size="25" fill="white">'+albums[i%6].title+'</text></svg>');},
   cacheInfo:async()=>({available:true,usedBytes:1048576,entries:5,limitMB:500}),
   api:async(target,options={})=>{f.calls.push({target,method:options.method||'GET'});const u=new URL(target,'http://fixture');
    if(u.pathname==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true}}};
    if(target==='/api/v1/jobs?active=true&limit=100')return {jobs:[]};
    if(target==='/api/v1/jobs?limit=30')return {jobs:structuredClone(f.jobs)};
    if(options.method==='DELETE'){if(f.holdDelete)await new Promise(resolve=>f.pending.push(resolve));f.jobs=[];return {ok:true};}
    if(u.pathname==='/api/v1/catalog/detail')return {resource:albums.find(a=>a.id===u.searchParams.get('id'))||albums[0],tracks:[{id:'701',type:'songs',title:'Afterglow',artist:'Studio Collective',duration:180000,downloadable:true,url:'https://music.apple.com/us/song/701'}]};
    if(u.pathname==='/api/v1/catalog/library'||u.pathname==='/api/v1/catalog/artist-albums')return {groups:[]};
    if(u.pathname.startsWith('/api/v1/catalog/'))return {groups:[{title:'为你推荐',type:'albums',items:albums},{title:'最近播放',type:'albums',items:albums.slice(2)}]};
    if(target==='/api/v1/settings/download')return {settings:{songFileFormat:'{title}'}};
    if(target==='/api/v1/settings/download-concurrency')return {downloadConcurrency:3};
    if(target==='/api/native/state')return {runtime:{phase:'ready'},downloadDirectory:'D:\\Music'};
    return {};
   }
  };
 });
 await page.goto(renderer);await expect(page.getByRole('heading',{name:'为你推荐',exact:true})).toBeVisible();
 await expect.poll(()=>page.evaluate(()=>document.getAnimations().filter(a=>a.playState==='running').length)).toBe(0);
}
test('navigation is directional, bounded and cancelled when quickly changing pages',async({page})=>{
 await install(page);await page.evaluate(()=>window.motionFixture.animations=[]);
 await page.getByRole('button',{name:'下载任务',exact:true}).click();await page.getByRole('button',{name:'设置与状态',exact:true}).click();
 await expect(page.locator('#settings-view')).toBeVisible();
 const transitions=await page.evaluate(()=>window.motionFixture.animations.filter(a=>['page-title','settings-view'].includes(a.target)).map(a=>({target:a.target,duration:a.options.duration,from:a.frames[0].transform,state:a.animation.playState})));
 expect(transitions.map(a=>a.target)).toEqual(['page-title','settings-view']);expect(transitions.every(a=>a.duration===420)).toBe(true);expect(transitions[0].state).toBe('idle');
 await page.locator('#browse-back').click();await expect(page.locator('#tasks-view')).toBeVisible();
 expect(await page.evaluate(()=>window.motionFixture.animations.filter(a=>a.target==='page-title').at(-1).frames[0].transform)).toBe('translateX(-8px)');
});
test('artwork gets restrained hover and press feedback without changing layout',async({page},info)=>{
 await install(page);const cover=page.locator('.catalog-artwork-action').first(),art=cover.locator('.artwork');
 const before=await cover.boundingBox();await cover.hover();await expect.poll(()=>art.evaluate(el=>parseFloat(getComputedStyle(el).scale))).toBeGreaterThan(1);
 expect((await cover.boundingBox()).width).toBe(before.width);
 await page.screenshot({path:info.outputPath('motion-home.png')});
 await page.mouse.down();await expect.poll(()=>art.evaluate(el=>parseFloat(getComputedStyle(el).scale))).toBeLessThan(1);await page.mouse.up();
 await expect(page.locator('.detail-hero h1')).toHaveText('Midnight Sessions');await expect.poll(()=>page.evaluate(()=>document.getAnimations().filter(a=>a.playState==='running').length)).toBe(0);
 await page.screenshot({path:info.outputPath('motion-detail.png')});
});
test('polling does not replay motion and deletion completes without waiting for animation',async({page},info)=>{
 await install(page);await page.getByRole('button',{name:'下载任务',exact:true}).click();await expect(page.locator('.job')).toHaveCount(1);
 expect(await page.locator('.job-actions').evaluate(el=>{for(let p=el;p;p=p.parentElement)if(p.getAnimations().some(a=>a.effect.getKeyframes().some(f=>f.transform&&f.transform!=='none')))return false;return true;})).toBe(true);
 await expect.poll(()=>page.evaluate(()=>document.getAnimations().filter(a=>a.playState==='running').length)).toBe(0);
 await page.evaluate(()=>window.motionFixture.animations=[]);await page.waitForTimeout(1150);expect(await page.evaluate(()=>window.motionFixture.animations.length)).toBe(0);
 await page.evaluate(()=>window.motionFixture.holdDelete=true);await page.getByRole('button',{name:'仅删除任务',exact:true}).click();await expect(page.locator('.job')).toHaveAttribute('aria-busy','true');
 await expect(page.getByRole('button',{name:'正在删除…',exact:true})).toBeDisabled();
 await page.screenshot({path:info.outputPath('motion-task-pending.png')});
 await page.evaluate(()=>{window.motionFixture.holdDelete=false;window.motionFixture.pending.splice(0).forEach(resolve=>resolve());});
 await expect(page.locator('.job')).toHaveCount(0);await expect(page.locator('#jobs-status')).toHaveText('任务已删除，下载文件已保留。');
 expect(await page.evaluate(()=>window.motionFixture.calls.filter(c=>c.method==='DELETE').length)).toBe(1);
});
test('reduced-motion preferences disable spatial motion and immediately cancel in-flight effects',async({page})=>{
 await page.emulateMedia({reducedMotion:'reduce'});await install(page);expect(await page.evaluate(()=>window.motionFixture.animations.length)).toBe(0);
 await page.locator('.catalog-artwork-action').first().hover();expect(await page.locator('.artwork').first().evaluate(el=>getComputedStyle(el).scale)).toBe('none');
 await page.getByRole('button',{name:'设置与状态',exact:true}).click();expect(await page.evaluate(()=>window.motionFixture.animations.length)).toBe(0);
 await page.emulateMedia({reducedMotion:'no-preference'});await page.evaluate(()=>{window.inFlight=window.nativeMotion.enter(document.getElementById('settings-view'),'page');});
 expect(await page.evaluate(()=>window.inFlight.playState)).toBe('running');await page.emulateMedia({reducedMotion:'reduce'});await expect.poll(()=>page.evaluate(()=>window.inFlight.playState)).toBe('idle');
 expect(await page.locator('#settings-view').evaluate(el=>getComputedStyle(el).transform)).toBe('none');
});
test('login panels and errors animate only on changed state, with no automatic submission',async({page})=>{
 await page.addInitScript(()=>{window.loginCalls=0;window.errorMotionCount=0;const animate=Element.prototype.animate;Element.prototype.animate=function(...args){if(this.id==='login-error')window.errorMotionCount++;return animate.apply(this,args);};window.nativeDesktop={getState:async()=>({state:'needs_login'}),onSession:fn=>window.sendLoginState=fn,cancelLogin:async()=>{},recheck:async()=>({state:'needs_login'}),login:async()=>{window.loginCalls++;return {state:'needs_2fa'};},submitCode:async()=>{window.loginCalls++;throw Error('native_login_failed');}};});
 await page.goto(pathToFileURL(path.resolve(__dirname,'../src/renderer/native-login.html')).href);
 await page.getByLabel('Apple 账户',{exact:true}).fill('fixture@example.invalid');await page.getByLabel('密码',{exact:true}).fill('fixture');await page.getByRole('button',{name:'登录',exact:true}).click();
 await expect(page.locator('#login-code')).toBeFocused();await page.locator('#login-code').fill('123456');await page.getByRole('button',{name:'验证',exact:true}).click();await expect(page.locator('#login-error')).toContainText('登录未完成');
 expect(await page.evaluate(()=>window.errorMotionCount)).toBe(1);await page.locator('#login-code').fill('123456');await page.getByRole('button',{name:'验证',exact:true}).click();await expect.poll(()=>page.evaluate(()=>window.errorMotionCount)).toBe(2);
 expect(await page.evaluate(()=>window.loginCalls)).toBe(3);await page.emulateMedia({reducedMotion:'reduce'});await expect.poll(()=>page.evaluate(()=>document.getAnimations().filter(a=>a.playState==='running').length)).toBe(0);
});

'use strict';
const {test,expect}=require('@playwright/test');
const path=require('node:path');
const fs=require('node:fs');
const {pathToFileURL}=require('node:url');
const root=path.resolve(__dirname,'..');
const preferences={downloadRegion:'',metadataRegion:'',lyricsRegion:'',language:'',saveLyrics:false,embedLyrics:true,lyricsType:'lyrics',lyricsFormat:'lrc',embedCover:true,coverFormat:'jpg',coverSize:'500x500',saveArtistCover:false,saveAnimatedArtwork:false,useSongInfoForPlaylist:false,alacMax:192000,mvMax:2160,songFileFormat:'{SongNumer}. {SongName}'};
async function install(page,{session='authenticated',full=false}={}){
  await page.addInitScript(({preferences,session,full})=>{
    window.calls=[];window.loginOpens=0;window.pendingSearch=[];
    const item=(id,title)=>({id,title,artist:'Fixture Artist',type:'songs',url:'https://music.apple.com/us/song/'+id,downloadable:true,duration:60000,previewUrl:'https://audio-ssl.itunes.apple.com/fixture.wav'});
    window.nativeDesktop={
      getState:async()=>({state:session,region:session==='authenticated'?'us':undefined}),onSession(fn){window.sessionUpdate=fn;},onReady(fn){queueMicrotask(fn);},openLogin:async()=>{window.loginOpens++;},exportDiagnostics:async()=>true,recheck:async()=>({state:session}),
      api:async(target,options={})=>{
        window.calls.push({target,...options});
        if(target==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{'aac-lc':{available:true},alac:{available:true},atmos:{available:true}}};
        if(target.startsWith('/api/v1/catalog/search')){const query=new URL(target,'http://fixture').searchParams;const term=query.get('term');if(term==='slow')return new Promise(resolve=>window.pendingSearch.push(()=>resolve({groups:[{type:'songs',items:[item('101','Old result')]}]})));if(term==='pages')return {groups:[{type:'songs',nextOffset:query.get('offset')==='1'?null:1,items:[item(query.get('offset')==='1'?'104':'103',query.get('offset')==='1'?'Second page':'First page')]}]};return {groups:[{type:'songs',items:[item('102',term)]}]};}
        if(target.startsWith('/api/v1/catalog/'))return {groups:[{type:'songs',items:[item('100','Fixture Song')]}]};
        if(target==='/api/native/playback'){if(options.method==='DELETE')return {ok:true};if(full)return {ok:true,mode:'full',mediaURL:'http://127.0.0.1:45678/api/native/playback/'+'a'.repeat(32)};throw new Error('native_playback_unavailable');}
        if(target==='/api/v1/settings/download')return {settings:preferences};
        if(target==='/api/v1/settings/download-concurrency')return {downloadConcurrency:3};
        if(target==='/api/native/state')return {runtime:{phase:'ready'},session:{state:session,region:'us'},downloadDirectory:'D:\\Music'};
        if(target==='/api/v1/browser-download/resolve')return {url:options.body.url};
        if(target==='/api/v1/jobs'&&options.method==='POST')return {jobs:[{id:'123',url:options.body.urls[0]}]};
        if(target.startsWith('/api/v1/jobs'))return {jobs:[]};
        throw new Error('unexpected fixture route');
      }
    };
  },{preferences,session,full});
  await page.goto(pathToFileURL(path.join(root,'src/renderer/native-index.html')).href);
}
test('native browse, Enter search supersession, settings and queue receipt',async({page})=>{
  const errors=[];page.on('pageerror',e=>errors.push(e.message));await install(page);
  await expect(page.getByText('Fixture Song',{exact:true})).toBeVisible();
  await expect(page.locator('#nav-toggle')).toHaveAttribute('aria-expanded','true');
  const search=page.getByRole('searchbox');await search.fill('slow');await search.press('Enter');await search.fill('new query');await search.press('Enter');
  await expect(page.getByText('new query',{exact:true})).toBeVisible();await page.evaluate(()=>window.pendingSearch.forEach(resolve=>resolve()));await expect(page.getByText('Old result',{exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'下载 new query',exact:true}).click();await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
  await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.locator('#concurrency')).toHaveValue('3');
  await page.getByRole('button',{name:'{title}',exact:true}).click();await expect(page.locator('#filename')).toHaveValue(/\{title\}/);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();await expect(page.locator('#notice')).toHaveText('设置已保存，下一个任务生效。');
  expect(errors).toEqual([]);expect(await page.locator('iframe,webview').count()).toBe(0);
});

test('new download default is ALAC, saved choices survive and reset restores ALAC',async({page})=>{
 await install(page);await expect(page.locator('#quality')).toHaveValue('alac');
 await page.getByRole('button',{name:'下载 Fixture Song',exact:true}).click();
 await expect.poll(()=>page.evaluate(()=>window.calls.find(c=>c.target==='/api/v1/jobs'&&c.method==='POST')?.body.quality)).toBe('alac');
 await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.locator('#filename')).toHaveValue(preferences.songFileFormat);
 await page.locator('#quality').selectOption('aac-lc');await page.getByRole('button',{name:'保存设置',exact:true}).click();await expect(page.locator('#notice')).toHaveText('设置已保存，下一个任务生效。');
 await page.reload();await expect(page.locator('#quality')).toHaveValue('aac-lc');
 await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.locator('#filename')).toHaveValue(preferences.songFileFormat);await page.getByRole('button',{name:'恢复默认',exact:true}).click();await expect(page.locator('#quality')).toHaveValue('alac');
 await page.reload();await expect(page.locator('#quality')).toHaveValue('alac');
});

// Real decodable PCM, generated in memory; never actual Apple media.
function wav(){const rate=8000;const b=Buffer.alloc(44+rate*2);b.write('RIFF');b.writeUInt32LE(b.length-8,4);b.write('WAVEfmt ',8);b.writeUInt32LE(16,16);b.writeUInt16LE(1,20);b.writeUInt16LE(1,22);b.writeUInt32LE(rate,24);b.writeUInt32LE(rate*2,28);b.writeUInt16LE(2,32);b.writeUInt16LE(16,34);b.write('data',36);b.writeUInt32LE(rate*2,40);return b;}
for(const full of [true,false])test(`one play action uses ${full?'native full receipt':'labeled preview fallback'}`,async({page})=>{
  await page.route('**/api/native/playback/*',route=>route.fulfill({contentType:'audio/wav',body:wav()}));
  await page.route('https://audio-ssl.itunes.apple.com/fixture.wav',route=>route.fulfill({contentType:'audio/wav',body:wav()}));
  await install(page,{full});await page.getByRole('button',{name:'播放 Fixture Song',exact:true}).click();
  await expect.poll(()=>page.locator('#audio').evaluate(audio=>audio.readyState)).toBeGreaterThan(0);
  await expect(page.locator('#audio')).toHaveAttribute('data-mode',full?'完整播放':'试听');
  await page.getByRole('button',{name:'停止播放',exact:true}).click();await expect(page.locator('#player')).toBeHidden();
});

test('one login form transitions to 2FA and never stores password',async({page})=>{
  await page.addInitScript(()=>{window.requests=[];window.nativeDesktop={getState:async()=>({state:'needs_login'}),onSession(fn){window.update=fn},login:async input=>{window.requests.push(Object.keys(input));return {state:'needs_2fa'}},submitCode:async code=>({state:code==='123456'?'authenticated':'needs_2fa'}),recheck:async()=>({state:'unverified'}),cancelLogin:async()=>{}}});
  await page.goto(pathToFileURL(path.join(root,'src/renderer/native-login.html')).href);await page.getByLabel('Apple 账户').fill('fixture@example.invalid');await page.getByLabel('密码',{exact:true}).fill('SENTINEL_PASSWORD');await page.getByRole('button',{name:'登录',exact:true}).click();await expect(page.getByLabel('验证码')).toBeVisible();await expect(page.locator('#login-password')).toHaveValue('');await page.getByLabel('验证码').fill('123456');await page.getByRole('button',{name:'验证',exact:true}).click();await expect(page.locator('#login-status')).toHaveText('登录成功');expect(await page.evaluate(()=>JSON.stringify(localStorage))).not.toContain('SENTINEL');
});

test('login is device-only and the retired SMS UI cannot reappear',async({page})=>{
 await page.addInitScript(()=>{window.requests=[];window.nativeDesktop={getState:async()=>({state:'needs_login'}),onSession(fn){window.update=fn},login:async input=>{window.requests.push(input);return {state:'needs_2fa'}},submitCode:async code=>{window.requests.push({code});return {state:'authenticated',region:'us'}},cancelLogin:async()=>{},recheck:async()=>({state:'needs_login'})};});
 await page.goto(pathToFileURL(path.join(root,'src/renderer/native-login.html')).href);
 await expect(page.locator('#verification-method,#sms-fields,#sms-send')).toHaveCount(0);
 await page.evaluate(()=>window.update({state:'needs_2fa',factor:{challengeId:'a'.repeat(32),stage:'phone',phones:[{id:'7',label:'•••• 42'}],retryAfterSeconds:0}}));
 await expect(page.locator('#account-fields')).toBeVisible();await expect(page.locator('#code-field')).toBeHidden();
 await page.getByLabel('Apple 账户').fill('fixture@example.invalid');await page.getByLabel('密码',{exact:true}).fill('SENTINEL_PASSWORD');await page.getByRole('button',{name:'登录',exact:true}).click();
 await page.getByLabel('验证码',{exact:true}).fill('123456');await page.getByRole('button',{name:'验证',exact:true}).click();await expect(page.locator('#login-status')).toHaveText('登录成功');
 expect(await page.evaluate(()=>window.requests)).toEqual([{appleEmail:'fixture@example.invalid',applePassword:'SENTINEL_PASSWORD'},{code:'123456'}]);
});

test('device login stage and retained failure survive a fresh needs-login snapshot',async({page})=>{
 await page.addInitScript(()=>{window.nativeDesktop={getState:async()=>({state:'authenticating',stage:'device_login'}),onSession(fn){window.update=fn},cancelLogin:async()=>{},recheck:async()=>({state:'needs_login'})};});
 await page.goto(pathToFileURL(path.join(root,'src/renderer/native-login.html')).href);
 await expect(page.locator('#login-status')).toHaveText('正在验证 Apple 账户…');
 await page.evaluate(()=>window.update({state:'needs_login',diagnostics:{events:[],lastFailure:{stage:'device_login',outcome:'failed',code:'native_login_failed'}}}));
 await expect(page.locator('#login-error')).toHaveText('登录未完成，请导出诊断后重试。');
 await page.evaluate(()=>window.update({state:'needs_login',code:'native_auth_cancelled'}));
 await expect(page.locator('#login-error')).toHaveText('本次登录已取消。');
});

test('retired verification failures stay in history without appearing as current network errors',async({page})=>{
 await page.addInitScript(()=>{window.nativeDesktop={getState:async()=>({state:'needs_login',code:'native_gsa_unavailable',stage:'adi_otp',diagnostics:{events:[],lastFailure:{stage:'adi_otp',outcome:'failed',code:'native_gsa_unavailable'}}}),onSession(fn){window.update=fn},cancelLogin:async()=>{},recheck:async()=>({state:'needs_login'})};});
 await page.goto(pathToFileURL(path.join(root,'src/renderer/native-login.html')).href);
 await expect(page.locator('#login-error')).toHaveText('');await expect(page.locator('#account-fields')).toBeVisible();
 await page.evaluate(()=>window.update({state:'needs_login',code:'native_login_failed',stage:'device_login',diagnostics:{events:[],lastFailure:{stage:'adi_provision_finish',outcome:'failed',code:'native_adi_body_read_failed',httpStatus:200}}}));
 await expect(page.locator('#login-error')).toHaveText('登录未完成，请导出诊断后重试。');
});

test('Music developer rejection is not presented as a device-code or password failure',async({page})=>{
 await page.addInitScript(()=>{window.nativeDesktop={getState:async()=>({state:'unverified',code:'native_music_developer_rejected',stage:'music_storefront'}),onSession(fn){window.update=fn},recheck:async()=>({state:'authenticated',region:'us'}),cancelLogin:async()=>{}}});
 await page.goto(pathToFileURL(path.join(root,'src/renderer/native-login.html')).href);
 await expect(page.locator('#login-error')).toHaveText('目录授权被拒绝（401），不是设备验证码错误；请重新验证会话或导出诊断。');
 await expect(page.locator('#account-fields')).toBeHidden();await expect(page.locator('#code-field')).toBeHidden();
 await page.evaluate(()=>window.update({state:'needs_login',code:'native_music_user_rejected',stage:'music_storefront'}));
 await expect(page.locator('#login-error')).toHaveText('Music 用户会话被拒绝（403）；请重新登录或导出诊断，不代表设备验证码错误。');
 await page.getByRole('button',{name:'重新验证会话',exact:true}).click();await expect(page.locator('#login-status')).toHaveText('登录成功');
});

test('verification guidance is conditional, collapsed and preserves focus on repeated session updates',async({page})=>{
  await page.addInitScript(()=>{window.submittedCodes=[];window.nativeDesktop={getState:async()=>({state:'needs_login'}),onSession(fn){window.update=fn},submitCode:async code=>{window.submittedCodes.push(code);return {state:'authenticated'}},cancelLogin:async()=>{},recheck:async()=>({state:'unverified'})}});
  await page.goto(pathToFileURL(path.join(root,'src/renderer/native-login.html')).href);
  const help=page.locator('#two-factor-help');const summary=help.locator('summary');
  await expect(help).toBeHidden();await page.evaluate(()=>window.update({state:'needs_2fa'}));
  await expect(help).toBeVisible();await expect(help).not.toHaveAttribute('open','');await expect(page.getByLabel('验证码',{exact:true})).toBeFocused();
  await page.getByLabel('验证码',{exact:true}).fill('123');await summary.focus();await page.keyboard.press('Enter');await expect(help).toHaveAttribute('open','');
  await page.evaluate(()=>window.update({state:'needs_2fa'}));await expect(summary).toBeFocused();await expect(page.getByLabel('验证码',{exact:true})).toHaveValue('123');
  await expect(help).toContainText('受信任设备');await expect(help).not.toContainText('短信');await expect(help).toContainText('其他登录会话');
  await expect(page.locator('#sms-send')).toHaveCount(0);await expect(page.locator('input[type="tel"],iframe,webview')).toHaveCount(0);await expect(page.locator('#login-code')).toHaveAttribute('autocomplete','one-time-code');
  await page.getByLabel('验证码',{exact:true}).fill('123456');await page.getByRole('button',{name:'验证',exact:true}).click();
  await expect(page.locator('#login-status')).toHaveText('登录成功');await expect(help).toBeHidden();await expect(help).not.toHaveAttribute('open','');expect(await page.evaluate(()=>window.submittedCodes)).toEqual(['123456']);
  await page.evaluate(()=>window.update({state:'needs_2fa'}));await expect(help).toBeVisible();await expect(help).not.toHaveAttribute('open','');
  await summary.click();await page.evaluate(()=>window.update({state:'unverified'}));await expect(help).toBeHidden();await expect(help).not.toHaveAttribute('open','');
});

for(const width of [460,390])test(`verification guidance fits ${width}px without horizontal overflow`,async({page})=>{
  await page.setViewportSize({width,height:560});await page.addInitScript(()=>{window.nativeDesktop={getState:async()=>({state:'needs_2fa'}),onSession(){},cancelLogin:async()=>{},recheck:async()=>({state:'unverified'})}});
  await page.goto(pathToFileURL(path.join(root,'src/renderer/native-login.html')).href);await page.locator('#two-factor-help summary').click();await expect(page.locator('#two-factor-help')).toHaveAttribute('open','');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await expect(page.getByRole('button',{name:'验证',exact:true})).toBeEnabled();await page.screenshot({path:`test-results/native-verification-guidance-${width}.png`,fullPage:true});
});

test('late full-audio failure falls back exactly once with the same play action',async({page})=>{
  let previews=0;await page.route('**/api/native/playback/*',route=>route.fulfill({contentType:'audio/wav',body:wav()}));await page.route('https://audio-ssl.itunes.apple.com/fixture.wav',route=>{previews++;return route.fulfill({contentType:'audio/wav',body:wav()});});
  await install(page,{full:true});await page.getByRole('button',{name:'播放 Fixture Song',exact:true}).click();await expect(page.locator('#audio')).toHaveAttribute('data-mode','完整播放');
  await page.locator('#audio').evaluate(audio=>audio.dispatchEvent(new Event('error')));await expect(page.locator('#audio')).toHaveAttribute('data-mode','试听');await expect.poll(()=>page.locator('#audio').evaluate(audio=>audio.readyState)).toBeGreaterThan(0);expect(previews).toBe(1);
});

test('native group pagination consumes the old control and offline session cannot queue',async({page})=>{
  await install(page,{session:'needs_login'});await expect(page.getByRole('button',{name:'下载 Fixture Song',exact:true})).toBeDisabled();
  await page.getByRole('searchbox').fill('pages');await page.getByRole('searchbox').press('Enter');await page.getByRole('button',{name:'加载更多 songs',exact:true}).click();await expect(page.getByText('First page',{exact:true})).toBeVisible();await expect(page.getByText('Second page',{exact:true})).toBeVisible();await expect(page.getByRole('button',{name:'加载更多 songs',exact:true})).toHaveCount(0);
  expect(await page.evaluate(()=>window.calls.some(call=>call.target==='/api/v1/jobs'&&call.method==='POST'))).toBe(false);
});

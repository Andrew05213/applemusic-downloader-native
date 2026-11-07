'use strict';
const { _electron:electron }=require('playwright');const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');const assert=require('node:assert/strict');
const {expect}=require('@playwright/test');
const root=path.resolve(__dirname,'../..');
async function main(){
 const [executable,manifest,mode]=process.argv.slice(2);assert.equal(JSON.parse(fs.readFileSync(manifest)).isolated,true);
 const profile=path.join(root,'.tmp-v2-native','electron-'+crypto.randomUUID());fs.mkdirSync(profile,{recursive:true});fs.writeFileSync(path.join(profile,'.native-fixture'),'synthetic-only\n');
 // Synthetic prior failure only; no Apple provisioning/authentication attempt.
 const {DiagnosticJournal}=require('../../electron/native-diagnostics');
 const synthetic=[
  {traceId:'c'.repeat(32),seq:1,attempt:1,at:Date.now()-3000,stage:'adi_provision_finish',outcome:'failed',code:'native_adi_body_empty',httpStatus:200},
  {traceId:'c'.repeat(32),seq:2,attempt:2,at:Date.now()-2000,stage:'music_storefront',outcome:'failed',code:'native_music_developer_rejected',httpStatus:401,appleErrorCode:'40100'},
  {traceId:'c'.repeat(32),seq:3,attempt:3,at:Date.now()-1000,stage:'music_storefront',outcome:'failed',code:'native_music_user_rejected',httpStatus:403,appleErrorCode:'40300'},
  {traceId:'c'.repeat(32),seq:4,attempt:4,at:Date.now()-500,stage:'device_login',outcome:'failed',code:'native_login_store_response_failed',exitCode:1,storeResponseType:0,storeErrorCode:10,storeExternalErrorCode:-20101},
  {traceId:'c'.repeat(32),seq:5,attempt:5,at:Date.now()-300,stage:'device_login',outcome:'waiting',loginEvent:'dialog_other'},
  {traceId:'c'.repeat(32),seq:6,attempt:5,at:Date.now()-200,stage:'device_login',outcome:'waiting',loginEvent:'dialog_unhandled'},
  {traceId:'c'.repeat(32),seq:7,attempt:5,at:Date.now(),stage:'device_login',outcome:'failed',code:'native_login_failed'}
 ];
 new DiagnosticJournal(path.join(profile,'auth-diagnostics.json')).observe({diagnostics:{events:synthetic}});
 const env={SystemRoot:process.env.SystemRoot,WINDIR:process.env.WINDIR,TEMP:process.env.TEMP,TMP:process.env.TMP,PATH:'',LOCALAPPDATA:process.env.LOCALAPPDATA,APPDATA:process.env.APPDATA};
 const startedAt=Date.now();
 const app=await electron.launch({executablePath:executable,args:[...(mode==='--packaged'?[]:[path.join(root,'src/main/native-main.js')]),'--native-manifest',path.resolve(manifest),'--native-fixture-profile',profile],env,timeout:30000});
 let exited=false;app.on('close',()=>{exited=true;});
 const evidence={scope:'real-electron-native-entry-isolated',realAccountsUsed:0,mediaRequestsAttempted:0};
 try{
  if(mode==='--packaged'){const identity=await app.evaluate(({app})=>({packaged:app.isPackaged,version:app.getVersion(),name:app.getName(),profile:app.getPath('userData'),electron:process.versions.electron,path:app.getAppPath()}));assert.equal(identity.packaged,true);assert.equal(identity.version,require('../../package.json').version);assert.equal(identity.name,require('../../electron-builder.native.cjs').productName);assert.equal(path.resolve(identity.profile),path.resolve(profile));assert.equal(identity.electron,'45.0.0');assert.ok(identity.path.endsWith('app.asar'));evidence.packagedIdentityVerified=true;evidence.localVersion=identity.version;evidence.productName=identity.name;evidence.isolatedProfileVerified=true;}
  const startupDeadline=Date.now()+10000;let splash;
  while(Date.now()<startupDeadline){splash=app.windows().find(win=>win.url().endsWith('/native-startup.html'));if(splash)break;await new Promise(resolve=>setTimeout(resolve,100));}
  assert.ok(splash,'missing startup screen');
  const initial=await app.evaluate(({BrowserWindow})=>BrowserWindow.getAllWindows().map(win=>({url:win.webContents.getURL(),visible:win.isVisible(),fullscreen:win.isFullScreen(),maximized:win.isMaximized(),resizable:win.isResizable(),bounds:win.getBounds()})));
  assert.equal(initial.find(win=>win.url.endsWith('/native-index.html')).visible,false);
  const startupWindow=initial.find(win=>win.url.endsWith('/native-startup.html'));
  evidence.startupBounds=startupWindow.bounds;
  assert.equal(startupWindow.fullscreen,false);assert.equal(startupWindow.maximized,false);assert.equal(startupWindow.resizable,true);
  // Windows non-client/DPI rounding can add two DIP to the requested bounds.
  // Fullscreen/maximized checks above remain strict.
  assert.ok(startupWindow.bounds.width>=760&&startupWindow.bounds.width<=1102,JSON.stringify(startupWindow));
  assert.ok(startupWindow.bounds.height>=560&&startupWindow.bounds.height<=722,JSON.stringify(startupWindow));
  assert.equal(initial.some(win=>win.url.endsWith('/native-login.html')),false);
  await app.evaluate(({app})=>app.emit('second-instance'));
  assert.equal(await app.evaluate(({BrowserWindow})=>BrowserWindow.getAllWindows().find(win=>win.webContents.getURL().endsWith('/native-index.html')).isVisible()),false);
  await splash.locator('#startup-logo').waitFor({state:'visible'});
  assert.equal(await splash.locator('button:visible,input').count(),0);
  await splash.screenshot({path:path.join(profile,'startup.png')});
  evidence.windowedLogoStartupVerified=true;evidence.startupBounds=startupWindow.bounds;evidence.mainHiddenUntilStartupReady=true;
  if(mode==='--close-startup'){
    const closed=app.waitForEvent('close',{timeout:20000});
    await splash.close();await closed;
    evidence.startupCloseExits=true;evidence.passed=true;return;
  }
  const preparingOwner=app.windows().find(win=>win.url().endsWith('/native-index.html'));
  await preparingOwner.waitForFunction(()=>typeof window.nativeDesktop?.api==='function');
  const preparingState=await preparingOwner.evaluate(()=>window.nativeDesktop.api('/api/native/state'));
  assert.ok(['stopped','starting','ready'].includes(preparingState.runtime.phase),JSON.stringify(preparingState.runtime));
  evidence.controlAPIElapsedMs=Date.now()-startedAt;
  const deadline=Date.now()+90000;let login;
  while(Date.now()<deadline){login=app.windows().find(win=>win.url().endsWith('/native-login.html'));if(login)break;await new Promise(resolve=>setTimeout(resolve,250));}
  assert.ok(login,'missing automatic login window');const owner=app.windows().find(win=>win.url().endsWith('/native-index.html'));assert.ok(owner);
  evidence.automaticLoginElapsedMs=Date.now()-startedAt;
  assert.equal(app.windows().some(win=>win.url().endsWith('/native-startup.html')),false);
  assert.equal(await app.evaluate(({BrowserWindow})=>BrowserWindow.getAllWindows().find(win=>win.webContents.getURL().endsWith('/native-index.html')).isVisible()),true);
  await owner.getByRole('button',{name:'设置与状态',exact:true}).click();await owner.getByRole('button',{name:'登录',exact:true}).click();
  assert.equal(app.windows().filter(win=>win.url().endsWith('/native-login.html')).length,1);
  assert.equal(await owner.locator('iframe,webview').count(),0);assert.equal(await owner.locator('#concurrency').inputValue(),'3');
  assert.equal(await owner.locator('#quality').inputValue(),'alac');evidence.defaultALACVerified=true;
  // Device-only entry: this smoke intentionally stops before any login.
  assert.equal(await login.locator('#verification-method,#sms-fields,#sms-send').count(),0);
  assert.equal(await login.locator('#login-email').count(),1);
  assert.equal(await login.locator('#login-password').count(),1);
  assert.equal(await login.locator('iframe,webview').count(),0);
  evidence.deviceOnlyEntryVerified=true;
  // Only the private UI transport is substituted. Packaged preload, owner
  // checks, validators and rendering remain production code.
  await app.evaluate(({app})=>{
    const Service=process.mainModule.require(app.getAppPath()+'/electron/native-service.js').NativeService;
    const original=Service.prototype.api;
    global.__authFixture={q:null,answers:[]};
    Service.prototype.api=async function(target,options={}){
      if(target==='/api/native/interaction'){
        if(options.method==='POST'){global.__authFixture.answers.push(structuredClone(options.body));global.__authFixture.q=null;return {accepted:true};}
        return {interaction:global.__authFixture.q};
      }
      return original.call(this,target,options);
    };
  });
  const baseInteraction={protocolVersion:1,scope:'d'.repeat(32),attemptId:'18446744073709551615',requestId:'9007199254740993',expiresAfterMs:120000,kind:1,unsupported:0,textTruncated:false,title:'PRIVATE_APPLE_TITLE',message:'<img src=x onerror=PRIVATE_SCRIPT()>',buttons:[{id:'18446744073709551614',label:'使用现有账户',role:1,enabled:true,unsupported:0}],fields:[]};
  const setPrompt=async q=>{await app.evaluate((_electron,q)=>{global.__authFixture.q=q;},q);await expect(login.locator('#interaction-title')).toHaveText(({1:'选择登录方式',2:'Apple 账户验证未通过',3:'Apple 账户验证',4:'确认账户信息',5:'输入设备验证码',6:'暂不支持此验证步骤'})[q.kind]);};
  await setPrompt(baseInteraction);
  await expect(login.locator('#interaction-message')).toContainText(baseInteraction.message);
  assert.equal(await login.locator('#interaction-message img').count(),0);
  await assert.rejects(()=>owner.evaluate(()=>window.nativeDesktop.getInteraction()),/native_sender_rejected/);
  await assert.rejects(()=>owner.evaluate(()=>window.nativeDesktop.api('/api/native/interaction')),/native_api_rejected/);
  await assert.rejects(()=>login.evaluate(()=>window.nativeDesktop.answerInteraction({protocolVersion:1})),/native_interaction_rejected/);
  await login.getByRole('button',{name:'使用现有账户',exact:true}).click();
  const credentialPrompt={...baseInteraction,requestId:'9007199254740994',kind:4,title:'输入账户信息',buttons:[{...baseInteraction.buttons[0],label:'继续验证',role:2}],fields:[{id:'1',label:'Apple 账户',type:7,required:true,minBytes:1,maxBytes:512,digitsOnly:false},{id:'2',label:'密码',type:2,required:true,minBytes:1,maxBytes:4096,digitsOnly:false}]};
  await setPrompt(credentialPrompt);
  await login.locator('#interaction-fields input[data-field-id="1"]').fill('fixture@example.invalid');
  await login.locator('#interaction-fields input[data-field-id="2"]').fill('PRIVATE_PASSWORD');
  assert.equal(await login.locator('#interaction-fields input[data-field-id="2"]').getAttribute('type'),'password');
  await login.screenshot({path:path.join(profile,'auth-interaction-credentials.png')});
  await new Promise(resolve=>setTimeout(resolve,700));
  assert.equal(await login.locator('#interaction-fields input[data-field-id="2"]').inputValue(),'PRIVATE_PASSWORD','polling replaced user input');
  await login.getByRole('button',{name:'继续',exact:true}).click();
  const codePrompt={...baseInteraction,requestId:'9007199254740995',kind:5,title:'输入设备验证码',buttons:[{...baseInteraction.buttons[0],label:'确认验证码',role:2}],fields:[{id:'3',label:'验证码',type:5,required:true,minBytes:6,maxBytes:6,digitsOnly:true}]};
  await setPrompt(codePrompt);await login.locator('#interaction-fields input').fill('12');
  await login.getByRole('button',{name:'验证',exact:true}).click();
  await expect(login.locator('#interaction-error')).toHaveText('请按字段要求完成输入。');
  await login.locator('#interaction-fields input').fill('123456');await login.getByRole('button',{name:'验证',exact:true}).click();
  await setPrompt({...baseInteraction,requestId:'9007199254740996',kind:2,title:'Check the account information you entered and try again.',buttons:[{...baseInteraction.buttons[0],label:'Retry',role:4},{id:'9007199254740999',label:'Cancel',role:3,enabled:true,unsupported:0}]});
  assert.equal(await login.getByRole('button',{name:'取消登录',exact:true}).count(),1);
  await expect(login.locator('.login-actions')).toBeHidden();
  await expect(login.locator('#interaction-message')).toHaveText('请检查输入的 Apple 账户信息，然后重新输入。');
  await login.screenshot({path:path.join(profile,'auth-failure-chinese.png')});
  await login.getByRole('button',{name:'重新输入',exact:true}).click();
  await setPrompt({...baseInteraction,requestId:'9007199254740997',kind:6,unsupported:2,title:'暂不支持的步骤'});
  await expect(login.locator('#interaction-note')).toContainText('暂不支持');
  assert.equal(await login.getByRole('button',{name:'使用现有账户',exact:true}).count(),0);
  await login.getByRole('button',{name:'关闭提示',exact:true}).click();
  await setPrompt({...baseInteraction,requestId:'9007199254740998',title:'取消测试'});
  await login.getByRole('button',{name:'取消登录',exact:true}).click();
  await expect(login.locator('#auth-interaction')).toBeHidden();
  const responses=await app.evaluate(()=>global.__authFixture.answers);
  assert.equal(responses.length,6);assert.equal(responses[0].attemptId,baseInteraction.attemptId);assert.equal(responses[1].values[1].value,'PRIVATE_PASSWORD');assert.equal(responses[2].values[0].value,'123456');assert.equal(responses[4].disposition,3);assert.equal(responses[5].disposition,2);
  assert.equal(await login.locator('#interaction-fields input').count(),0);
  evidence.nativeInteractionCredentialsCodeRetryCancelUnsupportedVerified=true;
  assert.equal(await login.locator('#login-error').textContent(),'登录未完成，请导出诊断后重试。');
  await login.getByRole('button',{name:'暂不登录'}).click();await new Promise(resolve=>setTimeout(resolve,2500));assert.equal(app.windows().filter(win=>win.url().endsWith('/native-login.html')).length,0);
  // Exercise the packaged discovery route with network deliberately disabled.
  // An isolated backend must reject the request, not fall back to the charts.
  assert.equal(await owner.getByRole('button',{name:'排行榜',exact:true}).count(),0);
  const discoverResult=await owner.evaluate(async()=>{try{await window.nativeDesktop.api('/api/v1/catalog/discover?region=cn');return 'unexpected_success';}catch(error){return error.message;}});
  assert.match(discoverResult,/native_runtime_isolated/);
  await owner.getByRole('button',{name:'新发现',exact:true}).click();
  await expect(owner.locator('#page-title')).toHaveText('新发现');
  await expect(owner.getByRole('button',{name:'重新载入',exact:true})).toBeVisible();
  evidence.discoveryEntryAndIsolatedRouteVerified=true;
  await owner.getByRole('button',{name:'设置与状态',exact:true}).click();
  if(process.env.AMDL_STREAM_OUTPUT){
    // Exercise the adapter shipped inside the extracted ASAR with generated
    // sine-wave AAC only. This does not authenticate or request Apple media.
    const folder=path.resolve(process.env.AMDL_STREAM_OUTPUT),receipt=JSON.parse(fs.readFileSync(path.join(folder,'receipt.json')));
    const chunks=Object.fromEntries(['init',...receipt.stream.segments.map((_,i)=>'segment/'+i)].map(key=>[key,fs.readFileSync(path.join(folder,key==='init'?'init.mp4':`segment-${key.split('/')[1]}.m4s`)).toString('base64')]));
    const result=await owner.evaluate(async({receipt,chunks})=>{
      if(typeof window.nativeDesktop.playbackChunk!=='function'||typeof window.nativeDesktop.cancelPlaybackChunks!=='function')throw Error('packaged_stream_ipc_missing');
      const audio=document.createElement('audio');audio.muted=true;document.body.append(audio);const calls=[];let failed;
      const player=new NativeSegmentPlayer(audio,receipt,{playbackChunk:async path=>{const key=path.slice(receipt.streamPath.length+1);calls.push(key);return Uint8Array.from(atob(chunks[key]),c=>c.charCodeAt(0));},cancelPlaybackChunks:async()=>{}},error=>{failed=error;});
      const wait=async check=>{const started=Date.now();while(!check()){if(failed)throw failed;if(Date.now()-started>5000)throw Error('packaged_stream_fixture_timeout');await new Promise(resolve=>setTimeout(resolve,20));}};
      try{await player.start();await wait(()=>audio.currentTime>.1);audio.currentTime=32;await wait(()=>audio.currentTime>32.1);return {aac:true,directSeek:calls.includes('segment/16'),intermediateSkipped:!calls.includes('segment/13')};}
      finally{player.dispose();audio.pause();audio.removeAttribute('src');audio.load();audio.remove();}
    },{receipt,chunks});
    assert.deepEqual(result,{aac:true,directSeek:true,intermediateSkipped:true});evidence.packagedRendererAACFixture=result;
  }
  // Exercise actual packaged preload -> owner-checked IPC -> Windows service
  // persistence, substituting only the OS picker response in this fixture.
  const originalDirectory=await owner.evaluate(async()=>(await window.nativeDesktop.api('/api/native/state')).downloadDirectory);
  await app.evaluate(({dialog})=>{dialog.showOpenDialog=async()=>({canceled:true,filePaths:[]});});
  await owner.getByRole('button',{name:'选择文件夹',exact:true}).click();
  await expect(owner.locator('#choose-download-directory')).toBeEnabled();
  assert.equal(await owner.locator('#download-directory').textContent(),originalDirectory);
  const selectedDirectory=path.join(profile,'测试下载目录');fs.mkdirSync(selectedDirectory);
  await app.evaluate(({dialog},directory)=>{dialog.showOpenDialog=async(_owner,options)=>{
    if(options.title!=='选择下载目录'||!options.properties.includes('openDirectory'))throw Error('fixture_picker_invalid');
    return {canceled:false,filePaths:[directory]};
  };},selectedDirectory);
  await owner.getByRole('button',{name:'选择文件夹',exact:true}).click();
  await expect(owner.locator('#directory-feedback')).toContainText('新任务生效');
  assert.equal(await owner.locator('#download-directory').textContent(),selectedDirectory);
  assert.equal(await owner.evaluate(async()=>(await window.nativeDesktop.api('/api/native/state')).downloadDirectory),selectedDirectory);
  await owner.getByRole('button',{name:'下载任务',exact:true}).click();
  await owner.getByRole('button',{name:'设置与状态',exact:true}).click();
  await expect(owner.locator('#download-directory')).toHaveText(selectedDirectory);
  assert.deepEqual(fs.readdirSync(selectedDirectory),[],'writeability probe was not removed');
  evidence.downloadDirectoryCancelAndSaveVerified=true;evidence.directoryIPCAndServiceVerified=true;
  const exportedFile=path.join(profile,'exported-diagnostics.txt');
  await app.evaluate(({dialog},file)=>{dialog.showSaveDialog=async()=>({canceled:false,filePath:file});},exportedFile);
  await owner.getByRole('button',{name:'导出诊断',exact:true}).click();
  const exportDeadline=Date.now()+10000;while(!fs.existsSync(exportedFile)&&Date.now()<exportDeadline)await new Promise(resolve=>setTimeout(resolve,100));
  const exported=JSON.parse(fs.readFileSync(exportedFile,'utf8'));assert.equal(exported.schemaVersion,2);assert.equal(exported.diagnostics.lastFailure.stage,'device_login');assert.equal(exported.diagnostics.lastFailure.code,'native_login_failed');assert.ok(exported.diagnostics.events.some(e=>e.stage==='adi_provision_finish'&&e.code==='native_adi_body_empty'&&e.httpStatus===200));
  for(const expected of synthetic.slice(1,3))assert.ok(exported.diagnostics.events.some(e=>e.stage==='music_storefront'&&e.code===expected.code&&e.httpStatus===expected.httpStatus&&e.appleErrorCode===expected.appleErrorCode));
  assert.deepEqual(exported.diagnostics.events.find(e=>e.code==='native_login_store_response_failed'),synthetic[3]);
  for(const expected of synthetic.slice(4,6))assert.deepEqual(exported.diagnostics.events.find(e=>e.loginEvent===expected.loginEvent),expected);
  assert.doesNotMatch(fs.readFileSync(exportedFile,'utf8'),/PRIVATE_|fixture@example|123456|onerror/);
  assert.doesNotMatch(fs.readFileSync(path.join(profile,'auth-diagnostics.json'),'utf8'),/PRIVATE_|fixture@example|123456|onerror/);
  evidence.musicHTTPDiagnosticsExportVerified=true;
  assert.equal(new DiagnosticJournal(path.join(profile,'auth-diagnostics.json')).snapshot().lastFailure.stage,'device_login');
  evidence.diagnosticsExportVerified=true;evidence.retainedFailureVerified=true;evidence.syntheticPriorFailure=true;
  evidence.passed=true;evidence.automaticUnifiedLogin=true;evidence.noEmbeddedWeb=true;evidence.dismissDoesNotReopen=true;
 }catch(error){
  evidence.failure=error.message;
  const page=app.windows().find(win=>win.url().endsWith('/native-index.html'));
  try{evidence.failureState=await page.evaluate(async()=>{try{const state=await window.nativeDesktop.api('/api/native/state');return {runtime:state.runtime?.phase,code:state.runtime?.code,session:state.session?.state};}catch(error){return {apiError:error.message};}});}catch{evidence.failureState={apiError:'fixture_state_unavailable'};}
  const visible=app.windows().find(win=>win.url().endsWith('/native-startup.html'))||page;
  if(visible)await visible.screenshot({path:path.join(profile,'failure.png')}).catch(()=>{});
  throw error;
 }finally{if(!exited)await app.close();fs.writeFileSync(path.join(profile,'evidence.json'),JSON.stringify(evidence,null,2)+'\n');process.stdout.write(JSON.stringify({...evidence,evidence:path.join(profile,'evidence.json')})+'\n');}
}
main().catch(error=>{process.stderr.write('native_electron_fixture_failed: '+error.message+'\n');process.exitCode=1;});

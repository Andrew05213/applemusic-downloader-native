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
  {traceId:'c'.repeat(32),seq:4,attempt:4,at:Date.now(),stage:'device_login',outcome:'failed',code:'native_login_failed'}
 ];
 new DiagnosticJournal(path.join(profile,'auth-diagnostics.json')).observe({diagnostics:{events:synthetic}});
 const env={SystemRoot:process.env.SystemRoot,WINDIR:process.env.WINDIR,TEMP:process.env.TEMP,TMP:process.env.TMP,PATH:'',LOCALAPPDATA:process.env.LOCALAPPDATA,APPDATA:process.env.APPDATA};
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
  const deadline=Date.now()+90000;let login;
  while(Date.now()<deadline){login=app.windows().find(win=>win.url().endsWith('/native-login.html'));if(login)break;await new Promise(resolve=>setTimeout(resolve,250));}
  assert.ok(login,'missing automatic login window');const owner=app.windows().find(win=>win.url().endsWith('/native-index.html'));assert.ok(owner);
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
  assert.equal(await login.locator('#login-error').textContent(),'登录未完成，请导出诊断后重试。');
  await login.getByRole('button',{name:'暂不登录'}).click();await new Promise(resolve=>setTimeout(resolve,2500));assert.equal(app.windows().filter(win=>win.url().endsWith('/native-login.html')).length,0);
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

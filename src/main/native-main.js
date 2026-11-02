'use strict';
const { app, BrowserWindow, ipcMain, dialog, session, protocol, shell, net: electronNet } = require('electron');
const path = require('node:path');
const fs = require('node:fs');
const net = require('node:net');
const { pathToFileURL } = require('node:url');
const { NativeService, allowedAPI } = require('../../electron/native-service');
const { NativeLoginCoordinator, safeSession } = require('../../electron/native-login');
const { VERSION } = require('../../electron/native-version');
const { NativeCache, catalogTarget, artworkHandler } = require('../../electron/native-cache');
protocol?.registerSchemesAsPrivileged?.([{scheme:'amdl-artwork',privileges:{standard:true,secure:true,supportFetchAPI:true}}]);
let cache;
const {DiagnosticJournal}=require('../../electron/native-diagnostics');
const { NativeStartupCoordinator } = require('../../electron/native-startup');

let owner; let login; let service; let timer; let closing = false; let polling = false;
let state = { state: 'checking' };
let journal;
let startup; let startupWindow; let mainLoaded; let mainRevealed = false;
let directoryPickerPending=false;const revealingJobs=new Set();
const actionStages={'native:login':'desktop_login_submit','native:two-factor':'desktop_code','native:recheck':'desktop_recheck'};
const renderer = path.join(__dirname, '../renderer');
const preload = path.join(__dirname, '../preload/native-preload.js');
const nativeURL = pathToFileURL(path.join(renderer, 'native-index.html')).href;
const loginURL = pathToFileURL(path.join(renderer, 'native-login.html')).href;
const startupURL = pathToFileURL(path.join(renderer, 'native-startup.html')).href;
const owns = (event, win, url) => !!win && !win.isDestroyed() && event.sender === win.webContents && event.senderFrame === win.webContents.mainFrame && event.senderFrame.url === url;
function trusted(event) { return owns(event, owner, nativeURL) || owns(event, login?.window, loginURL); }
function isolated(win, expected) {
  win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  win.webContents.on('will-navigate', (event, target) => { if (target !== expected) event.preventDefault(); });
  win.webContents.on('will-redirect', (event) => event.preventDefault());
}
async function unusedPort() { const server = net.createServer(); await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); }); const port = server.address().port; await new Promise(resolve => server.close(resolve)); return port; }
async function revealMain() {
  if (mainRevealed || closing) return;
  await mainLoaded;
  if (mainRevealed || closing || !owner || owner.isDestroyed()) return;
  mainRevealed = true;
  const boot = startupWindow; startupWindow = null;
  if (boot && !boot.isDestroyed()) boot.close();
  owner.show(); owner.focus();
  login?.observe(state);
  if (state.state === 'needs_2fa') login?.open();
}
function observe(input) {
  const previousState=state.state;
  state = safeSession(input); journal?.observe(state);
  if(previousState==='authenticated'&&state.state==='needs_login')void cache?.clear('personal').catch(()=>{});
  if(journal)state=safeSession({...state,diagnostics:journal.snapshot(128)});
  if (mainRevealed) login?.observe(state);
  else startup?.observe(state);
  if (owner && !owner.isDestroyed()) owner.webContents.send('native:session-state', state);
}
async function refresh() {
  if (polling || closing || !service || service.failed || service.closed) return;
  polling = true;
  try { const current = await service.api('/api/native/state'); observe(current.runtime.phase === 'failed' ? { state: 'runtime_unavailable' } : current.session); }
  catch { journal?.record('desktop_service','failed','native_runtime_unavailable');observe({ state: 'runtime_unavailable' }); }
  finally { polling = false; }
}
async function start() {
  const args = process.argv; const index = args.indexOf('--native-manifest');
  const manifest = index >= 0 ? path.resolve(args[index + 1] || '') : path.join(process.resourcesPath, 'native-runtime/bundle.json');
  const data = path.join(app.getPath('userData'), 'native-v2');
  const output = path.join(app.getPath('music'), 'Apple Music Downloads 2.0 Test');
  const executable = app.isPackaged ? path.join(process.resourcesPath, 'bin/amdl-service.exe') : path.join(__dirname, '../../electron/bin/amdl-service.exe');
  const toolDirectory = app.isPackaged ? path.join(process.resourcesPath, 'bin') : path.join(__dirname, '../../electron/bin');
  const guestPort = await unusedPort();
  if (closing) return;
  service = new NativeService({ executable, manifest, dataDirectory: data, outputDirectory: output, guestPort, toolDirectory });
  const started = service;
  service.on('closed', () => { if (!closing && service === started) observe({ state: 'runtime_unavailable' }); });
  try { await service.ready; }
  catch(error) { journal?.record('desktop_service','failed',error?.message); throw error; }
  if(closing)return; if(owner&&!owner.isDestroyed())owner.webContents.send('native:backend-ready'); await refresh();
}
function bindIPC() {
  for (const [name, action] of [
    ['native:startup-state', () => startup.snapshot()],
    ['native:startup-enter', () => { startup.finish(); return true; }],
    ['native:startup-quit', () => { app.quit(); return true; }],
  ]) ipcMain.handle(name, event => {
    if (!owns(event, startupWindow, startupURL)) throw new Error('native_sender_rejected');
    return action();
  });
  const handle = (name, action) => ipcMain.handle(name, async (event, ...args) => {
    if (!trusted(event)) throw new Error('native_sender_rejected');
    const stage=actionStages[name];if(stage)journal?.record(stage,'begin');
    try { const result=await action(event, ...args);if(stage)journal?.record(stage,'ok');return result; } catch(error) { if(stage)journal?.record(stage,'failed',error?.message);throw new Error(/^[a-z][a-z0-9_]{0,79}$/.test(error?.message)?error.message:'native_operation_failed'); }
  });
  handle('native:state', () => state);
  handle('native:playback-chunk',(event,target)=>{if(!owns(event,owner,nativeURL))throw Error('native_sender_rejected');return service.playbackChunk(target);});
  handle('native:playback-cancel',(event,base)=>{if(!owns(event,owner,nativeURL)||typeof base!=='string'||!/^\/api\/native\/playback\/[a-f0-9]{32}$/.test(base))throw Error('native_sender_rejected');service.cancelPlaybackChunks(base);return true;});
  handle('native:open-login', () => { login.open(); return true; });
  handle('native:api', (event, target, options = {}) => {
    if (!owns(event, owner, nativeURL) || !allowedAPI(options.method || 'GET', target) || JSON.stringify(options.body || {}).length > 32768 || target.startsWith('/api/native/login') || target.startsWith('/api/native/two-factor') || target.startsWith('/api/native/download-directory') || /^\/api\/native\/jobs\//.test(target)) throw new Error('native_api_rejected');
    if((options.method||'GET')==='GET'&&catalogTarget(target)&&cache)return cache.json(target,()=>service.api(target,options));
    return service.api(target, options);
  });
  handle('native:choose-download-directory',async event=>{
    if(!owns(event,owner,nativeURL))throw new Error('native_sender_rejected');
    if(directoryPickerPending)throw new Error('native_directory_busy');directoryPickerPending=true;
    try{const current=await service.api('/api/native/state');const result=await dialog.showOpenDialog(owner,{title:'选择下载目录',defaultPath:current.downloadDirectory,properties:['openDirectory','createDirectory','dontAddToRecent']});
      if(result.canceled||!result.filePaths?.length)return {canceled:true};
      const saved=await service.api('/api/native/download-directory',{method:'PUT',body:{directory:result.filePaths[0]}});return {canceled:false,directory:saved.directory};
    }finally{directoryPickerPending=false;}
  });
  handle('native:reveal-job-folder',async(event,id)=>{
    if(!owns(event,owner,nativeURL))throw new Error('native_sender_rejected');
    if(typeof id!=='string'||!/^[a-f0-9]{32}$/.test(id))throw new Error('native_job_invalid');
    if(revealingJobs.has(id))return false;revealingJobs.add(id);
    try{const result=await service.api('/api/native/jobs/'+id+'/location');
      if(!path.isAbsolute(result.directory||'')||!fs.statSync(result.directory).isDirectory())throw new Error('native_directory_missing');
      const root=await fs.promises.realpath(result.directory);
      if(result.file){if(!path.isAbsolute(result.file))throw new Error('native_location_invalid');const target=await fs.promises.realpath(result.file),relative=path.relative(root,target);if(!relative||relative==='..'||relative.startsWith('..'+path.sep)||path.isAbsolute(relative)||!(await fs.promises.stat(target)).isFile())throw new Error('native_location_invalid');shell.showItemInFolder(target);return {revealed:true};}
      const error=await shell.openPath(root);if(error)throw new Error('native_folder_open_failed');return {revealed:false};
    }catch(error){if(error?.code==='ENOENT')throw new Error('native_directory_missing');throw error;}finally{revealingJobs.delete(id);}
  });
  for(const [name,action] of [
    ['native:cache-info',()=>cache?.info()||{available:false,limitMB:500,usedBytes:0,entries:0}],
    ['native:cache-limit',limit=>cache.setLimit(limit)],
    ['native:cache-clear',()=>cache.clear()],
    ['native:catalog-refresh',()=>cache.clear('catalog')],
    ['native:catalog-prefetch',async target=>{if(!catalogTarget(target)||!allowedAPI('GET',target))throw new Error('native_api_rejected');if(!(await cache.info()).limitMB)return false;await cache.json(target,()=>service.api(target));return true;}],
  ])handle(name,(event,...args)=>{if(!owns(event,owner,nativeURL))throw new Error('native_sender_rejected');return action(...args);});
  handle('native:login', async (event, input) => {
    if (!owns(event, login.window, loginURL) || typeof input?.appleEmail !== 'string' || typeof input.applePassword !== 'string' || input.appleEmail.length > 512 || input.applePassword.length > 4096 || (input.method !== undefined && input.method !== 'device')) throw new Error('native_login_rejected');
    await cache?.clear('personal');
    const next = await service.api('/api/native/login', { method: 'POST', body: { appleEmail: input.appleEmail, applePassword: input.applePassword } }); observe(next); return state;
  });
  handle('native:two-factor', async (event, code) => { if (!owns(event, login.window, loginURL) || typeof code !== 'string' || !/^\d{6}$/.test(code)) throw new Error('native_code_rejected'); const next = await service.api('/api/native/two-factor', { method: 'POST', body: { code } }); observe(next); return state; });
  handle('native:cancel-login', async () => { try { observe(await service.api('/api/native/cancel-login', { method: 'POST' })); } catch {} if (login.window && !login.window.isDestroyed()) login.window.close(); return true; });
  let rechecking;
  handle('native:recheck', () => {
    if (rechecking) return rechecking;
    rechecking = (async () => {
      if (service?.failed && !service.closed) await service.close();
      if (!service || service.closed) await start();
      else await service.api(state.state === 'runtime_unavailable' ? '/api/native/retry' : '/api/native/recheck', { method: 'POST' });
      await refresh(); return state;
    })().finally(() => { rechecking = null; });
    return rechecking;
  });
  handle('native:export-diagnostics', async () => {
    const result = await dialog.showSaveDialog(owner, { defaultPath: 'AMDL-native-diagnostics.txt', filters: [{ name: '诊断日志', extensions: ['txt'] }] });
    if (result.canceled || !result.filePath) return false;
    const snapshot = await service.api('/api/native/state').catch(() => ({ session: state }));
    journal?.observe(snapshot.session);
    const safe = { schemaVersion:2,exportedAt:new Date().toISOString(),version: VERSION, session: safeSession(snapshot.session), runtime: snapshot.runtime ? { phase: snapshot.runtime.phase, code: snapshot.runtime.code, authState: snapshot.runtime.authState } : { phase: 'unavailable' },diagnostics:journal?.snapshot()||{events:[]} };
    fs.writeFileSync(result.filePath, JSON.stringify(safe, null, 2) + '\n', { encoding: 'utf8', mode: 0o600 }); return true;
  });
}
app.whenReady().then(async () => {
  app.setName('Apple Music Downloader Native');
  app.setAppUserModelId('io.github.andrew05213.applemusicdownloader.v2test');
  const profileIndex=process.argv.indexOf('--native-fixture-profile');
  if(profileIndex>=0){const fixtureProfile=process.argv[profileIndex+1];const selected=process.argv.indexOf('--native-manifest');const bundle=selected>=0?JSON.parse(fs.readFileSync(process.argv[selected+1],'utf8')):null;if(!path.isAbsolute(fixtureProfile||'')||!fs.existsSync(path.join(fixtureProfile,'.native-fixture'))||bundle?.isolated!==true||bundle.controlProtocol!==1)throw new Error('native_fixture_profile_invalid');app.setPath('userData',fixtureProfile);}
  // Compatibility identity: keep beta sessions, task bindings and preferences.
  else app.setPath('userData', path.join(app.getPath('appData'), 'AppleMusicDownloader-v2-test'));
  journal=new DiagnosticJournal(path.join(app.getPath('userData'),'auth-diagnostics.json'));
  if(!app.requestSingleInstanceLock()){app.quit();return;}
  app.on('second-instance',()=>{const win=mainRevealed?owner:startupWindow;if(win&&!win.isDestroyed()){if(win.isMinimized())win.restore();win.show();win.focus();}});
  const uiSession = session.fromPartition('persist:amdl-v2-native-ui');
  cache=new NativeCache(path.join(app.getPath('userData'),'ui-cache'));await cache.ready;await cache.clear('personal');
  uiSession.protocol.handle('amdl-artwork',artworkHandler(cache,(...args)=>electronNet.fetch(...args)));
  uiSession.setPermissionRequestHandler((_wc, _permission, callback) => callback(false));
  owner = new BrowserWindow({ show: false, width: 1440, height: 900, minWidth: 760, minHeight: 560, ...(process.platform==='win32'?{titleBarStyle:'hidden',titleBarOverlay:{color:'#29292b',symbolColor:'#c1c1ca',height:60}}:{}), backgroundColor: '#202020', webPreferences: { preload, session: uiSession, sandbox: true, contextIsolation: true, nodeIntegration: false, autoplayPolicy: 'no-user-gesture-required' } });
  owner.setTitle('Apple Music Downloader Native · '+VERSION);
  owner.setMenuBarVisibility(false); isolated(owner, nativeURL);
  const ownerID = owner.webContents.id;
  login = new NativeLoginCoordinator({ createWindow: () => {
    const win = new BrowserWindow({ parent: owner, width: 460, height: 560, resizable: false, title: '登录 Apple Music', backgroundColor: '#202024', webPreferences: { preload, session: uiSession, sandbox: true, contextIsolation: true, nodeIntegration: false } });
    win.setMenuBarVisibility(false); isolated(win, loginURL); win.webContents.once('did-finish-load', () => login.deliver()); void win.loadURL(loginURL); return win;
  }, onDismiss: () => service?.api('/api/native/cancel-login', { method: 'POST' }).then(observe).catch(() => {}) });
  uiSession.webRequest.onBeforeSendHeaders((details, callback) => callback({ requestHeaders: service?.mediaHeaders(details, ownerID) || details.requestHeaders }));
  startup = new NativeStartupCoordinator({
    onFinish: () => { void revealMain().catch(() => app.quit()); },
    onChange: value => { if (startupWindow && !startupWindow.isDestroyed()) startupWindow.webContents.send('native:startup-state', value); },
  });
  startupWindow = new BrowserWindow({ show: false, width: 1100, height: 720, minWidth: 760, minHeight: 560, center: true, fullscreen: false, frame: true, title: 'Music 下载器', backgroundColor: '#1c1c1e', webPreferences: { preload: path.join(__dirname, '../preload/native-startup-preload.js'), session: uiSession, sandbox: true, contextIsolation: true, nodeIntegration: false } });
  const boot = startupWindow;
  boot.setMenuBarVisibility(false); isolated(boot, startupURL);
  boot.once('closed', () => { if (startupWindow !== boot) return; startupWindow = null; if (!mainRevealed && !closing) app.quit(); });
  bindIPC();
  mainLoaded = owner.loadURL(nativeURL);
  mainLoaded.catch(() => startup.fail());
  await boot.loadURL(startupURL);
  if (closing || boot.isDestroyed()) return;
  boot.show();
  try { await start(); } catch { observe({ state: 'runtime_unavailable' }); }
  if (!closing) timer = setInterval(refresh, 2000);
}).catch(() => app.quit());
app.on('window-all-closed', () => app.quit());
app.on('before-quit', (event) => {
  if (closing) return;
  event.preventDefault(); closing = true; clearInterval(timer); login?.dispose();
  startup?.dispose(); if (startupWindow && !startupWindow.isDestroyed()) startupWindow.hide();
  // Snapshot the guest's retained history before shutting it down. Bound this
  // final read; an unresponsive guest must not keep the application open.
  (async()=>{let timeout;try{if(service&&!service.closed){const snapshot=await Promise.race([service.api('/api/native/state'),new Promise(resolve=>{timeout=setTimeout(()=>resolve(null),3000);})]);if(snapshot)journal?.observe(snapshot.session);}}catch{}finally{clearTimeout(timeout);}await service?.close();})().finally(() => app.quit());
});

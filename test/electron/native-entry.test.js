'use strict';
const test=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const path=require('node:path');const os=require('node:os');
const {nativeLaunch}=require('../../electron/start-native');
const root=path.resolve(__dirname,'../..');
function ipcFixture(apiOverride,savePath) {
 const vm=require('node:vm');const handlers=new Map();const requests=[];const frame={url:'file:///fixture/native-login.html'};const webContents={mainFrame:frame};const win={webContents,isDestroyed:()=>false};
 const context=vm.createContext({require(name){if(name==='electron')return {app:{whenReady:()=>new Promise(()=>{}),on(){}},dialog:{showSaveDialog:async()=>({canceled:false,filePath:savePath})},ipcMain:{handle:(name,handler)=>handlers.set(name,handler)}};if(name==='../../electron/native-service')return {NativeService:class{},allowedAPI:require('../../electron/native-service').allowedAPI};if(name==='../../electron/native-login')return require('../../electron/native-login');if(name==='../../electron/native-version')return {VERSION:'fixture'};return require(name);},__dirname:path.join(root,'src/main'),process,console,URL,setInterval,clearInterval,fixtureWindow:win,fixtureAPI:async(target,options)=>{requests.push({target,options});return apiOverride?apiOverride(target,options):{state:'authenticating',secret:'SENTINEL'};}});
 vm.runInContext(fs.readFileSync(path.join(root,'src/main/native-main.js'),'utf8'),context);
 vm.runInContext('login={window:fixtureWindow,observe(){}};service={api:fixtureAPI};bindIPC();',context);
 frame.url=vm.runInContext('loginURL',context);return {handlers,requests,event:{sender:webContents,senderFrame:frame},context};
}

test('actual export IPC retains the last failure after cancel and guest reset',async t=>{
 const vm=require('node:vm');const {DiagnosticJournal}=require('../../electron/native-diagnostics');const dir=fs.mkdtempSync(path.join(os.tmpdir(),'native-export-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));const destination=path.join(dir,'export.txt');
 const f=ipcFixture(async target=>target==='/api/native/state'?{runtime:{phase:'ready',authState:'needs_login'},session:{state:'needs_login'}}:{state:'needs_login',code:'native_auth_cancelled'},destination);
 f.context.fixtureJournal=new DiagnosticJournal(path.join(dir,'auth-diagnostics.json'));
 f.context.fixtureWindow.close=()=>{};
 f.context.fixtureFailure={state:'needs_login',code:'native_login_failed',diagnostics:{events:[{traceId:'a'.repeat(32),seq:1,attempt:1,at:Date.now(),stage:'lite_bind',outcome:'failed',code:'native_music_binding_failed'}]}};
 vm.runInContext('journal=fixtureJournal; observe(fixtureFailure);',f.context);
 await f.handlers.get('native:cancel-login')(f.event);await f.handlers.get('native:export-diagnostics')(f.event);
 const out=JSON.parse(fs.readFileSync(destination));assert.equal(out.schemaVersion,2);assert.equal(out.diagnostics.lastFailure.stage,'lite_bind');assert.equal(out.diagnostics.lastFailure.code,'native_music_binding_failed');assert.equal(out.session.state,'needs_login');assert.doesNotMatch(fs.readFileSync(destination,'utf8'),/SENTINEL/);
 assert.equal(new DiagnosticJournal(path.join(dir,'auth-diagnostics.json')).snapshot().lastFailure.stage,'lite_bind');
});
test('actual export IPC retains specific finish response code and HTTP 200',async t=>{
 const vm=require('node:vm');const {DiagnosticJournal}=require('../../electron/native-diagnostics');const dir=fs.mkdtempSync(path.join(os.tmpdir(),'native-finish-export-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));const destination=path.join(dir,'export.txt');
 const f=ipcFixture(async target=>target==='/api/native/state'?{runtime:{phase:'ready',authState:'needs_login'},session:{state:'needs_login'}}:{state:'needs_login',code:'native_auth_cancelled'},destination);
 f.context.fixtureJournal=new DiagnosticJournal(path.join(dir,'auth-diagnostics.json'));f.context.fixtureWindow.close=()=>{};
 f.context.fixtureFailure={state:'needs_login',code:'native_gsa_provider_unavailable',stage:'adi_provision_finish',diagnostics:{events:[{traceId:'b'.repeat(32),seq:1,attempt:1,at:Date.now(),stage:'adi_provision_finish',outcome:'failed',code:'native_adi_plist_integer_range',httpStatus:200,body:'SENTINEL'}]}};
 vm.runInContext('journal=fixtureJournal; observe(fixtureFailure);',f.context);
 await f.handlers.get('native:cancel-login')(f.event);await f.handlers.get('native:export-diagnostics')(f.event);
 const out=JSON.parse(fs.readFileSync(destination));assert.equal(out.diagnostics.lastFailure.stage,'adi_provision_finish');assert.equal(out.diagnostics.lastFailure.code,'native_adi_plist_integer_range');assert.equal(out.diagnostics.lastFailure.httpStatus,200);assert.doesNotMatch(fs.readFileSync(destination,'utf8'),/SENTINEL/);
 assert.equal(new DiagnosticJournal(path.join(dir,'auth-diagnostics.json')).snapshot().lastFailure.code,'native_adi_plist_integer_range');
});

test('retired SMS IPC is absent and login rejects its legacy method',async()=>{
 const f=ipcFixture();assert.equal(f.handlers.has('native:sms-send'),false);assert.equal(f.handlers.has('native:sms-code'),false);
 for(const method of ['sms','voice'])await assert.rejects(f.handlers.get('native:login')(f.event,{appleEmail:'fixture@example.invalid',applePassword:'fixture',method}),/native_login_rejected/);
 assert.equal(f.requests.length,0);
 for(const method of [undefined,'device'])await f.handlers.get('native:login')(f.event,{appleEmail:'fixture@example.invalid',applePassword:'fixture',method,mediaUserToken:'SENTINEL'});
 assert.equal(f.requests.length,2);for(const request of f.requests)assert.deepEqual(Object.keys(request.options.body).sort(),['appleEmail','applePassword']);
});

test('playback bytes are exposed only to the app owner frame, never the login window',async()=>{
 const vm=require('node:vm'),f=ipcFixture();
 for(const name of ['native:playback-chunk','native:playback-cancel'])await assert.rejects(f.handlers.get(name)(f.event,'/api/native/playback/'+'a'.repeat(32)+'/init'),/native_sender_rejected/);
 f.context.byteCalls=[];vm.runInContext('owner=fixtureWindow;fixtureWindow.webContents.mainFrame.url=nativeURL;service.playbackChunk=async path=>{byteCalls.push(path);return new Uint8Array([1,2])};service.cancelPlaybackChunks=base=>byteCalls.push(base)',f.context);
 const target='/api/native/playback/'+'a'.repeat(32);
 assert.deepEqual([...await f.handlers.get('native:playback-chunk')(f.event,target+'/init')],[1,2]);
 assert.equal(await f.handlers.get('native:playback-cancel')(f.event,target),true);
 await assert.rejects(f.handlers.get('native:playback-cancel')(f.event,target+'/init'),/native_sender_rejected/);
 await assert.rejects(f.handlers.get('native:playback-chunk')({...f.event,senderFrame:{url:f.event.senderFrame.url}},target+'/init'),/native_sender_rejected/);
 assert.equal(f.context.byteCalls.length,2);
});

test('device login and six-digit code reach the real NativeService transport',async t=>{
 const http=require('node:http');const {EventEmitter}=require('node:events');const {PassThrough}=require('node:stream');const {NativeService}=require('../../electron/native-service');const received=[];let token;
 const server=http.createServer((request,response)=>{assert.equal(request.headers.authorization,'Bearer '+token);let raw='';request.on('data',part=>{raw+=part;});request.on('end',()=>{received.push({target:request.url,method:request.method,body:JSON.parse(raw)});response.setHeader('Content-Type','application/json');response.end(JSON.stringify({state:'authenticating',secret:'SENTINEL'}));});});await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));t.after(()=>server.close());
 const child=new EventEmitter();child.stdout=new PassThrough();child.stderr=new PassThrough();child.stdin=new PassThrough();child.kill=()=>child.emit('close',1);child.stdin.once('data',raw=>{token=JSON.parse(raw).apiAccessToken;queueMicrotask(()=>child.stdout.write(JSON.stringify({nativeServer:true,version:require('../../package.json').version,port:server.address().port})+'\n'));});child.stdin.once('finish',()=>child.emit('close',0));
 const service=new NativeService({executable:'fixture.exe',manifest:'bundle.json',dataDirectory:'fixture',outputDirectory:'fixture',guestPort:12340,spawnChild:()=>child});t.after(()=>service.close());await service.ready;
 const f=ipcFixture((...args)=>service.api(...args));
 await f.handlers.get('native:login')(f.event,{appleEmail:'fixture@example.invalid',applePassword:'fixture'});
 await f.handlers.get('native:two-factor')(f.event,'123456');
 assert.deepEqual(received,[{target:'/api/native/login',method:'POST',body:{appleEmail:'fixture@example.invalid',applePassword:'fixture'}},{target:'/api/native/two-factor',method:'POST',body:{code:'123456'}}]);
 assert.equal(f.handlers.has('native:sms-send'),false);
});

test('private auth interaction IPC enforces login owner and validates scoped answers',async()=>{
 const vm=require('node:vm');
 const q={protocolVersion:1,scope:'a'.repeat(32),attemptId:'18446744073709551615',requestId:'9007199254740993',expiresAfterMs:1000,kind:4,unsupported:0,textTruncated:false,title:'PRIVATE_TITLE',message:'PRIVATE_MESSAGE',buttons:[{id:'2',label:'Submit',role:2,enabled:true,unsupported:0}],fields:[{id:'3',label:'Code',type:5,required:true,minBytes:6,maxBytes:6,digitsOnly:true}]};
 const accepted=[];
 const f=ipcFixture(async(target,options)=>{assert.equal(target,'/api/native/interaction');if(options?.method==='POST'){accepted.push(structuredClone(options.body));return {accepted:true};}return {interaction:q};});
 const got=await f.handlers.get('native:get-auth-interaction')(f.event);assert.equal(got.attemptId,q.attemptId);
 const a={protocolVersion:1,scope:q.scope,attemptId:q.attemptId,requestId:q.requestId,buttonId:'2',disposition:1,values:[{fieldId:'3',value:'123456'}]};
 for(const change of [{scope:'b'.repeat(32)},{requestId:'1'},{values:[{fieldId:'3',value:'12345x'}]}])await assert.rejects(f.handlers.get('native:answer-auth-interaction')(f.event,{...a,...change}),/native_interaction_rejected/);
 await f.handlers.get('native:answer-auth-interaction')(f.event,a);assert.equal(accepted[0].values[0].value,'123456');
 await assert.rejects(f.handlers.get('native:get-auth-interaction')({...f.event,senderFrame:{url:f.event.senderFrame.url}}),/native_sender_rejected/);
 vm.runInContext('owner=fixtureWindow;fixtureWindow.webContents.mainFrame.url=nativeURL;',f.context);
 await assert.rejects(f.handlers.get('native:get-auth-interaction')(f.event),/native_sender_rejected/);
 await assert.rejects(f.handlers.get('native:answer-auth-interaction')(f.event,a),/native_sender_rejected/);
 await assert.rejects(f.handlers.get('native:api')(f.event,'/api/native/interaction'),/native_api_rejected/);
});
test('default development entry selects only native app-owned UI and protocol-1 runtime',t=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-entry-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));const manifest=path.join(directory,'bundle.json');fs.writeFileSync(manifest,JSON.stringify({controlProtocol:1}));
 const launch=nativeLaunch(root,['--native-manifest',manifest],'electron.exe');assert.equal(launch.args[0],path.join(root,'src/main/native-main.js'));assert.equal(require('../../package.json').scripts.start,'node electron/start-native.js');
 assert.throws(()=>nativeLaunch(root,[],'electron.exe'),/native_manifest_argument_required/);fs.writeFileSync(manifest,'{}');assert.throws(()=>nativeLaunch(root,['--native-manifest',manifest],'electron.exe'),/native_bundle_protocol_required/);
 for(const name of ['src/main/native-main.js','src/preload/native-preload.js','electron/native-service.js','electron/start-native.js']){const source=fs.readFileSync(path.join(root,name),'utf8');assert.doesNotMatch(source,/require\([^)]*(?:wsl-manager|apple-music-browser|official-web-player|drm-runtime)|WebContentsView|\.cookies\./);}
 for(const name of ['dist:win:online','dist:win:offline'])assert.equal(require('../../package.json').scripts[name],'npm run dist:native');
});

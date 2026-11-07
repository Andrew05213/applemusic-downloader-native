'use strict';
const test=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const os=require('node:os');const path=require('node:path');
const event=(seq=1)=>({traceId:'a'.repeat(32),seq,attempt:1,at:Date.now(),stage:'gsa_authenticate',outcome:'failed',code:'native_gsa_authentication_failed',httpStatus:401});
test('base login errors survive the exact session filter used by export',()=>{
 const {safeSession}=require('../../electron/native-login');
 for(const code of ['native_login_failed','native_auth_cancelled','native_auth_required','native_session_unverified']) assert.equal(safeSession({state:'needs_login',code}).code,code);
});
test('journal preserves bounded redacted failures through dismissal and restart',t=>{
 const {DiagnosticJournal}=require('../../electron/native-diagnostics');const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-diag-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));const file=path.join(directory,'auth-diagnostics.json');
 const j=new DiagnosticJournal(file);j.observe({state:'needs_login',diagnostics:{events:[{...event(),password:'SENTINEL'}],lastFailure:event()}});j.observe({state:'needs_login',code:'native_auth_cancelled'});
 const restored=new DiagnosticJournal(file);assert.equal(restored.snapshot().lastFailure.stage,'gsa_authenticate');assert.equal(restored.snapshot().lastFailure.httpStatus,401);
 for(let i=2;i<400;i++)restored.observe({diagnostics:{events:[{...event(i),outcome:'ok',code:undefined,httpStatus:undefined}]}});
 assert.equal(restored.snapshot().events.length,256);assert.equal(restored.snapshot().lastFailure.code,'native_gsa_authentication_failed');assert.doesNotMatch(fs.readFileSync(file,'utf8'),/SENTINEL/);
});
test('diagnostic wire schema rejects unknown stages, codes and identity fields',()=>{
 const {sanitizeDiagnostics}=require('../../electron/native-diagnostics');
 for(const patch of [{stage:'SENTINEL'},{code:'SENTINEL'},{traceId:'user@example.invalid'},{at:Infinity},{seq:-1},{outcome:'SENTINEL'},{httpStatus:9999}]) assert.equal(sanitizeDiagnostics({events:[{...event(),...patch}]}).events.length,0);
 const d=sanitizeDiagnostics({events:[{...event(),token:'SENTINEL'}],lastFailure:event(),body:'SENTINEL'});assert.doesNotMatch(JSON.stringify(d),/SENTINEL/);
});

test('unexpected failures are fixed codes and disk errors do not block diagnostics',t=>{
 const {DiagnosticJournal}=require('../../electron/native-diagnostics');const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-diag-failure-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));const file=path.join(directory,'auth-diagnostics.json');
 fs.writeFileSync(file,'SENTINEL_NOT_JSON');const j=new DiagnosticJournal(file);assert.equal(j.snapshot().storageCode,'native_diagnostics_read_failed');j.record('desktop_login_submit','failed');assert.equal(j.snapshot().lastFailure.code,'native_operation_failed');assert.doesNotMatch(fs.readFileSync(file,'utf8'),/SENTINEL/);
 const blocked=new DiagnosticJournal(path.join(file,'cannot-create.json'));assert.doesNotThrow(()=>blocked.record('desktop_login_submit','failed','SENTINEL_ACCOUNT'));assert.equal(blocked.snapshot().lastFailure.code,'native_operation_failed');assert.equal(blocked.snapshot().storageCode,'native_diagnostics_write_failed');
});

test('specific ADI response codes survive journal, restart and the actual sandboxed preload',t=>{
 const vm=require('node:vm');const {CODES,DiagnosticJournal}=require('../../electron/native-diagnostics');
 const source=fs.readFileSync(path.resolve(__dirname,'../../apple-music-downloader/internal/nativeauth/adi_diagnostics.go'),'utf8');
 const expected=[...source.matchAll(/"(native_adi_[a-z_]+)"/g)].map(match=>match[1]);assert.ok(expected.length>=20);
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-adi-codes-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 const file=path.join(directory,'auth-diagnostics.json');const journal=new DiagnosticJournal(file);let bridge;let listener;
 vm.runInNewContext(fs.readFileSync(path.resolve(__dirname,'../../src/preload/native-preload.js'),'utf8'),{require(name){assert.equal(name,'electron');return {contextBridge:{exposeInMainWorld(_name,api){bridge=api;}},ipcRenderer:{invoke:async()=>{},on(_name,fn){listener=fn;},removeListener(){}}};}});
 let copied;bridge.onSession(value=>{copied=value;});
 for(const [index,code]of expected.entries()){
  assert.equal(CODES.has(code),true,'guest code missing from desktop: '+code);
  const leaf={...event(index+1),stage:'adi_provision_finish',code,httpStatus:200};
  listener(null,{state:'needs_login',code,stage:'adi_provision_finish',diagnostics:{events:[{...leaf,body:'SENTINEL'}],lastFailure:leaf}});
  assert.equal(copied.code,code);assert.equal(copied.diagnostics.lastFailure.code,code);assert.equal(copied.diagnostics.lastFailure.httpStatus,200);
  journal.observe(copied);assert.equal(new DiagnosticJournal(file).snapshot().lastFailure.code,code);
 }
 assert.doesNotMatch(fs.readFileSync(file,'utf8'),/SENTINEL/);
});

test('Music 401 and 403 retain fixed Apple codes through preload, journal and restart',t=>{
 const vm=require('node:vm');const {safeSession}=require('../../electron/native-login');const {DiagnosticJournal}=require('../../electron/native-diagnostics');
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-music-codes-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));const file=path.join(directory,'auth-diagnostics.json');let bridge,listener,copied;
 vm.runInNewContext(fs.readFileSync(path.resolve(__dirname,'../../src/preload/native-preload.js'),'utf8'),{require(){return {contextBridge:{exposeInMainWorld(_name,api){bridge=api}},ipcRenderer:{invoke:async()=>{},on(_name,fn){listener=fn},removeListener(){}}}}});bridge.onSession(value=>{copied=value});
 const journal=new DiagnosticJournal(file);
 for(const [i,status]of [401,403].entries()){
  const code=status===401?'native_music_developer_rejected':'native_music_user_rejected';const leaf={...event(i+1),stage:'music_storefront',code,httpStatus:status,appleErrorCode:String(status)+'00'};
  listener(null,{state:status===401?'unverified':'needs_login',code,stage:'music_storefront',diagnostics:{events:[{...leaf,body:'SENTINEL_TOKEN'}],lastFailure:leaf}});
  assert.equal(copied.code,code);assert.equal(copied.diagnostics.lastFailure.appleErrorCode,String(status)+'00');
  journal.observe(safeSession(copied));const restored=new DiagnosticJournal(file).snapshot();assert.equal(restored.lastFailure.httpStatus,status);assert.equal(restored.lastFailure.appleErrorCode,String(status)+'00');assert.equal(restored.lastFailure.code,code);
 }
 assert.doesNotMatch(fs.readFileSync(file,'utf8'),/SENTINEL/);
});

test('upstream Apple diagnostic codes are a fixed vocabulary, not response text',()=>{
 const {sanitizeDiagnostics}=require('../../electron/native-diagnostics');
 for(const appleErrorCode of ['SENTINEL_SECRET','40300 SENTINEL','99999',40100]){
  const leaf={...event(),stage:'music_storefront',code:'native_music_user_rejected',httpStatus:403,appleErrorCode};
  const d=sanitizeDiagnostics({events:[leaf],lastFailure:leaf});assert.equal(d.events.length,0);assert.equal(d.lastFailure,undefined);
 }
});

test('Go and desktop Music diagnostic vocabularies stay aligned',()=>{
 const {APPLE_CODES,CODES}=require('../../electron/native-diagnostics');
 const source=fs.readFileSync(path.resolve(__dirname,'../../apple-music-downloader/internal/nativeauth/music_diagnostics.go'),'utf8');
 const expected=[...source.matchAll(/"(native_music_[a-z_]+)"/g)].map(m=>m[1]);assert.equal(expected.length,6);for(const code of expected)assert.equal(CODES.has(code),true,code);
 assert.deepEqual([...APPLE_CODES].sort(),[...source.matchAll(/"([0-9]{5})"/g)].map(m=>m[1]).sort());
});

test('device login preserves only bounded failure metadata through preload and the journal',t=>{
 const vm=require('node:vm');const {DiagnosticJournal,CODES,sanitizeDiagnostics}=require('../../electron/native-diagnostics');
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-device-login-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));const file=path.join(directory,'auth-diagnostics.json');let bridge,listener,copied;
 vm.runInNewContext(fs.readFileSync(path.resolve(__dirname,'../../src/preload/native-preload.js'),'utf8'),{require(){return {contextBridge:{exposeInMainWorld(_name,api){bridge=api}},ipcRenderer:{invoke:async()=>{},on(_name,fn){listener=fn},removeListener(){}}}}});
 bridge.onSession(value=>{copied=value});
 const source=fs.readFileSync(path.resolve(__dirname,'../../apple-music-downloader/internal/nativeauth/diagnostics.go'),'utf8');
 const expected=[...new Set([...source.matchAll(/"(native_login_(?:process|protocol|store|input)_[a-z_]+)"/g)].map(m=>m[1]))];assert.equal(expected.length,9);for(const code of expected)assert.ok(CODES.has(code),code);
 const leaf={...event(),stage:'device_login',code:'native_login_store_response_failed',exitCode:1,storeResponseType:0,storeErrorCode:10,storeExternalErrorCode:-20101};delete leaf.httpStatus;
 listener(null,{state:'needs_login',stage:'device_login',diagnostics:{events:[{...leaf,message:'SENTINEL'}],lastFailure:leaf}});
 assert.equal(copied.diagnostics.events[0].message,undefined);
 const journal=new DiagnosticJournal(file);journal.observe(copied);assert.deepEqual(new DiagnosticJournal(file).snapshot().lastFailure,leaf);
 for(const patch of [{exitCode:256},{exitSignal:9},{exitCode:undefined,exitSignal:65},{storeErrorCode:'-7'},{storeResponseType:-1},{storeExternalErrorCode:9007199254740992},{storeExternalErrorCode:'secret'},{stage:'music_storefront'},{code:'native_login_store_no_response'}]){
  const invalid={...leaf,...patch};assert.equal(sanitizeDiagnostics({events:[invalid]}).events.length,0);
 }
 const vocabulary=source.match(/var DeviceLoginEvents = \[\]string\{([\s\S]*?)\}/)[1];const events=[...vocabulary.matchAll(/"([a-z_]+)"/g)].map(m=>m[1]);assert.equal(events.length,16);
 for(const [index,loginEvent] of events.entries()){
  const notice={traceId:leaf.traceId,seq:index+2,attempt:2,at:Date.now(),stage:'device_login',outcome:'waiting',loginEvent};
  listener(null,{state:'needs_login',diagnostics:{events:[{...notice,title:'SENTINEL',message:'SENTINEL'}]}});
  assert.deepEqual(JSON.parse(JSON.stringify(copied.diagnostics.events[0])),notice);journal.observe(copied);
  assert.ok(new DiagnosticJournal(file).snapshot().events.some(e=>e.loginEvent===loginEvent));
 }
 for(const patch of [{loginEvent:'SENTINEL'},{stage:'device_code'},{outcome:'failed'},{code:'native_login_failed'},{exitCode:1}])assert.equal(sanitizeDiagnostics({events:[{traceId:leaf.traceId,seq:30,attempt:2,at:Date.now(),stage:'device_login',outcome:'waiting',loginEvent:'dialog_other',...patch}]}).events.length,0);
 assert.doesNotMatch(fs.readFileSync(file,'utf8'),/SENTINEL/);
});

test('intermediate Store rejection survives preload and export before Retry without becoming lastFailure',t=>{
 const vm=require('node:vm');const {DiagnosticJournal,sanitizeDiagnostics}=require('../../electron/native-diagnostics');
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-waiting-response-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 let bridge,listener,copied;
 vm.runInNewContext(fs.readFileSync(path.resolve(__dirname,'../../src/preload/native-preload.js'),'utf8'),{require(){return {contextBridge:{exposeInMainWorld(_name,api){bridge=api}},ipcRenderer:{invoke:async()=>{},on(_name,fn){listener=fn},removeListener(){}}}}});
 bridge.onSession(value=>{copied=value});
 const response={...event(1),stage:'device_login',outcome:'waiting',code:undefined,loginEvent:'store_response_received',storeResponseType:0,storeErrorCode:10,storeExternalErrorCode:-5000,storeProtocolErrorCode:-22947,storeAuthStatus:-1,httpStatus:200};
 listener(null,{state:'needs_interaction',diagnostics:{events:[{...response,title:'PRIVATE_MESSAGE',password:'PRIVATE_PASSWORD'}]}});
 assert.equal(copied.state,'needs_interaction');assert.equal(copied.diagnostics.events[0].storeExternalErrorCode,-5000);
 const journal=new DiagnosticJournal(path.join(directory,'auth.json'));journal.observe(copied);
 assert.equal(journal.snapshot().events[0].storeResponseType,0);assert.equal(journal.snapshot().lastFailure,undefined);
 assert.doesNotMatch(fs.readFileSync(path.join(directory,'auth.json'),'utf8'),/PRIVATE/);
 for(const patch of [{loginEvent:'dialog_other'},{exitCode:1},{outcome:'ok'},{storeExternalErrorCode:'PRIVATE'},{storeProtocolErrorCode:9007199254740992}])assert.equal(sanitizeDiagnostics({events:[{...response,...patch}]}).events.length,0);
});

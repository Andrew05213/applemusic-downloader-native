'use strict';
const test=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const path=require('node:path');const vm=require('node:vm');
const root=path.resolve(__dirname,'../..');
test('retired SMS API routes are never admitted',()=>{
 const {allowedAPI}=require('../../electron/native-service');
 for(const target of ['/api/native/sms/send','/api/native/sms/code','/api/native/sms/send?phoneId=7'])for(const method of ['GET','POST'])assert.equal(allowedAPI(method,target),false);
 assert.equal(allowedAPI('POST','/api/native/two-factor'),true);
});
test('retired SMS controls do not exist in the real sandboxed preload',()=>{
 let bridge;let listener;vm.runInNewContext(fs.readFileSync(path.join(root,'src/preload/native-preload.js'),'utf8'),{require(name){assert.equal(name,'electron');return {contextBridge:{exposeInMainWorld(_name,value){bridge=value;}},ipcRenderer:{invoke:async()=>{},on(_name,fn){listener=fn;},removeListener(){}}};}});
 assert.equal(bridge.sendSMS,undefined);assert.equal(bridge.submitSMSCode,undefined);assert.equal(typeof bridge.submitCode,'function');
 let copied;bridge.onSession(value=>{copied=value;});listener(null,{state:'needs_2fa',factor:{challengeId:'a'.repeat(32),stage:'phone',phones:[{id:'7',label:'•••• 42'}],retryAfterSeconds:0}});
 assert.equal(copied.state,'needs_login');assert.equal(copied.code,'native_login_failed');assert.equal(copied.factor,undefined);
});
test('a stale SMS challenge cannot become a device-code prompt',()=>{
 const {safeSession}=require('../../electron/native-login');const result=safeSession({state:'needs_2fa',factor:{challengeId:'SENTINEL',phones:[]}});
 assert.equal(result.state,'needs_login');assert.equal(result.code,'native_login_failed');assert.equal(result.factor,undefined);assert.doesNotMatch(JSON.stringify(result),/SENTINEL/);
});
test('future runtime builds omit the retired GSA/ADI implementation and helper',()=>{
 for(const file of ['electron/build-native-runtime.js','electron/native-package.js','native-runtime/Dockerfile','native-runtime/build-guest.sh'])assert.doesNotMatch(fs.readFileSync(path.join(root,file),'utf8'),/internal\/(?:grandslam|localanisette)|native_adi|amdl-adi/);
 for(const folder of ['grandslam','localanisette']){
  const dir=path.join(root,'apple-music-downloader/internal',folder);assert.equal(fs.existsSync(dir)?fs.readdirSync(dir).filter(name=>name.endsWith('.go')).length:0,0);
 }
});

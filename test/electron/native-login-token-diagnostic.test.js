'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const {safeEvent}=require('../../electron/native-diagnostics');
const root=path.resolve(__dirname,'../..');
const read=name=>fs.readFileSync(path.join(root,name),'utf8');
const base={traceId:'e'.repeat(32),seq:1,attempt:3,at:1700000000000,stage:'device_login'};
const terminal={...base,outcome:'failed',code:'native_login_token_cache_failed'};
const retry={...base,outcome:'waiting'};
const fetchDetail={stage:'developer_token',reason:'developer_token_fetch_failed',attempt:5,usedCache:false,httpStatus:403};
const cacheDetail={stage:'token_cache_json',reason:'cache_replace_failed',attempt:0,usedCache:false};
const recovered={stage:'music_token_fallback',reason:'cache_fallback_used',attempt:1,usedCache:true,httpStatus:200};

function preload(){
 let bridge,listener,copy;
 vm.runInNewContext(read('src/preload/native-preload.js'),{
  require(name){
   assert.equal(name,'electron');
   return {contextBridge:{exposeInMainWorld(_name,value){bridge=value;}},ipcRenderer:{on(_name,fn){listener=fn;},removeListener(){}}};
  },
 });
 bridge.onSession(value=>{copy=value;});
 return e=>{
  listener({}, {state:'needs_login',diagnostics:{events:[e],lastFailure:e}});
  return JSON.parse(JSON.stringify(copy.diagnostics));
 };
}

test('main and sandbox preload agree on safe token details and reject mislabelled events',()=>{
 const copy=preload();
 for(const e of [
  {...terminal,tokenDiagnostic:{...fetchDetail,token:'PRIVATE_BODY'}},
  {...terminal,exitCode:1,tokenDiagnostic:cacheDetail},
  {...retry,tokenDiagnostic:recovered},
  {...retry,tokenDiagnostic:{stage:'refresh',reason:'retry_exhausted',attempt:5,usedCache:false}},
  terminal,
  {...terminal,tokenDiagnostic:recovered},
  {...terminal,tokenDiagnostic:{...fetchDetail,reason:'PRIVATE_BODY'}},
  {...terminal,tokenDiagnostic:{...fetchDetail,attempt:0}},
  {...terminal,tokenDiagnostic:{...fetchDetail,httpStatus:null}},
  {...terminal,stage:'music_tokens',tokenDiagnostic:fetchDetail},
  {...terminal,code:'native_login_store_response_failed',tokenDiagnostic:fetchDetail},
  {...retry,loginEvent:'store_response_received',tokenDiagnostic:recovered},
 ]){
  const safe=safeEvent(e),output=copy(e);
  assert.deepEqual(output.events,safe?[safe]:[]);
  assert.deepEqual(output.lastFailure,safe?.outcome==='failed'?safe:undefined);
  assert.doesNotMatch(JSON.stringify(output),/PRIVATE_BODY/);
 }
 assert.deepEqual(copy({...terminal,tokenDiagnostic:cacheDetail}).lastFailure.tokenDiagnostic,cacheDetail);
});

test('login prompt shows the concrete terminal reason, not a recovered or stale observation',()=>{
 const elements=new Map();
 function get(id){
  if(!elements.has(id))elements.set(id,{textContent:'',addEventListener(){},focus(){}});
  return elements.get(id);
 }
 let render;
 const window={
  nativeAccountInput:{attach(){return {};}},
  nativeDesktop:{onSession(fn){render=fn;},getState(){return new Promise(()=>{});}},
 };
 vm.runInNewContext(read('src/renderer/native-login.js'),{window,document:{getElementById:get,querySelector:get}});
 const session={state:'needs_login',stage:'device_login',code:'native_login_failed'};
 const show=(lastFailure,override={})=>{
  render({...session,diagnostics:{lastFailure},...override});
  return get('login-error').textContent;
 };
 assert.match(show({...terminal,tokenDiagnostic:fetchDetail}),/开发者令牌（HTTP 403）/);
 assert.match(show({...terminal,tokenDiagnostic:cacheDetail}),/无法替换Music 会话缓存文件/);
 assert.match(show(terminal),/获取或保存 Music 会话失败/);
 assert.match(show({...terminal,tokenDiagnostic:recovered}),/获取或保存 Music 会话失败/);
 assert.match(show({...terminal,tokenDiagnostic:fetchDetail},{code:'native_auth_cancelled'}),/本次登录已取消/);
 assert.doesNotMatch(show({...terminal,tokenDiagnostic:fetchDetail},{stage:'restore_session'}),/开发者令牌/);
});

'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { NativeLoginCoordinator, safeSession } = require('../../electron/native-login');

class WindowFixture extends EventEmitter {
  constructor() { super(); this.dead = false; this.messages = []; this.webContents = { send: (_name, state) => this.messages.push(state) }; }
  isDestroyed() { return this.dead; }
  show() { if (this.dead) throw new Error('destroyed'); }
  focus() { if (this.dead) throw new Error('destroyed'); }
  close() { this.dead = true; this.emit('closed'); }
}
test('one native login window is shared by startup, settings and 2FA', async () => {
  const windows = []; let cancellations = 0;
  const login = new NativeLoginCoordinator({ createWindow: () => { const win = new WindowFixture(); windows.push(win); return win; }, onDismiss: () => { cancellations++; } });
  login.observe({ state: 'checking' }); assert.equal(windows.length, 0);
  login.observe({ state: 'authenticated', region: 'us' }); assert.equal(windows.length, 0);
  login.observe({ state: 'needs_login' }); login.observe({ state: 'needs_login' }); login.open();
  assert.equal(windows.length, 1);
  login.observe({ state: 'needs_2fa' }); assert.equal(windows.length, 1);
  login.observe({ state: 'authenticated', region: 'us' }); assert.equal(windows[0].dead, true); assert.equal(cancellations, 0);
  login.observe({ state: 'needs_login' }); assert.equal(windows.length, 2);
  windows[1].close(); login.observe({ state: 'needs_login' }); await Promise.resolve(); assert.equal(windows.length, 2); assert.equal(cancellations, 1);
  login.open(); assert.equal(windows.length, 3); login.dispose();
});
test('network failure never auto-opens a password dialog or calls destroyed windows', () => {
  const windows = [];
  const login = new NativeLoginCoordinator({ createWindow: () => { const win = new WindowFixture(); windows.push(win); return win; } });
  login.observe({ state: 'unverified' }); login.observe({ state: 'runtime_unavailable' }); assert.equal(windows.length, 0);
  login.open(); const win = windows[0]; win.dead = true;
  assert.doesNotThrow(() => login.observe({ state: 'authenticated', region: 'us' }));
  login.dispose(); assert.doesNotThrow(() => login.open()); assert.equal(windows.length, 1);
});

const smsFactor = () => ({ challengeId: 'a'.repeat(32), stage: 'phone', phones: [{ id: '7', label: '•••• 42' }], retryAfterSeconds: 0 });
test('retired factors are rejected without copying challenge or phone data',()=>{
 for(const state of ['needs_2fa','authenticating','authenticated']){
  const result=safeSession({state,region:state==='authenticated'?'us':undefined,factor:{...smsFactor(),secret:'SENTINEL'}});
  assert.deepEqual(result,{state:'needs_login',code:'native_login_failed'});assert.equal(Object.isFrozen(result),true);assert.doesNotMatch(JSON.stringify(result),/SENTINEL/);
 }
});

test('sandboxed preload copies device-only sessions and retains historical diagnostics', async () => {
  const vm = require('node:vm'); const fs = require('node:fs'); const path = require('node:path'); const listeners = new Map(); const calls = []; let exposed;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../../src/preload/native-preload.js'), 'utf8'), { require: () => ({ contextBridge: { exposeInMainWorld: (_key, value) => { exposed = value; } }, ipcRenderer: { invoke: async (...args) => { calls.push(args); }, on: (key, listener) => listeners.set(key, listener), removeListener() {} } }) });
  let received; exposed.onSession(value => { received = value; });
  for (const value of [
    { state: 'needs_2fa', factor: smsFactor(), token: 'SENTINEL' },
    { state: 'needs_login', code: 'native_music_binding_failed', body: 'SENTINEL' },
    { state: 'needs_login', code: 'native_gsa_provider_unavailable' },
    { state: 'needs_login', code: 'native_login_failed',stage:'adi_initialize',diagnostics:{events:[{traceId:'a'.repeat(32),seq:1,attempt:1,at:Date.now(),stage:'adi_initialize',outcome:'failed',code:'native_adi_provider_unavailable'}]} },
    { state: 'needs_2fa', factor: { ...smsFactor(), selectedPhoneId: '999' } },
    { state: 'needs_login', code: 'native_gsa_SENSITIVE' },
  ]) {
    listeners.get('native:session-state')({}, value);
    assert.deepEqual(JSON.parse(JSON.stringify(received)), JSON.parse(JSON.stringify(safeSession(value))));
    assert.doesNotMatch(JSON.stringify(received), /SENTINEL/);
  }
  assert.equal(exposed.sendSMS,undefined);assert.equal(exposed.submitSMSCode,undefined);
  await exposed.login({appleEmail:'fixture@example.invalid',applePassword:'fixture'});await exposed.submitCode('123456');
  assert.deepEqual(JSON.parse(JSON.stringify(calls)), [['native:login',{appleEmail:'fixture@example.invalid',applePassword:'fixture'}], ['native:two-factor','123456']]);
});

test('Go and main share the diagnostic stage and code vocabulary',()=>{
 const fs=require('node:fs');const path=require('node:path');const {CODES,STAGES}=require('../../electron/native-diagnostics');
 const source=fs.readFileSync(path.join(__dirname,'../../apple-music-downloader/internal/nativeauth/diagnostics.go'),'utf8');
 for(const name of ['DiagnosticStages','DiagnosticExtraCodes']){const array=source.match(new RegExp('var '+name+' = \\[\\]string\\{([^}]+)\\}'))?.[1];assert.ok(array);for(const value of array.match(/"[^"]+"/g).map(s=>JSON.parse(s)))assert.equal((name==='DiagnosticStages'?STAGES:CODES).has(value),true,value);}
});

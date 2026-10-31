'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { NativeStartupCoordinator } = require('../../electron/native-startup');

function fixture() {
  let elapsed; let finishes = 0; const changes = []; const cleared = [];
  const startup = new NativeStartupCoordinator({ onFinish: () => { finishes++; }, onChange: state => changes.push(state), schedule: (fn, ms) => { assert.equal(ms, 90000); elapsed = fn; return 7; }, unschedule: id => cleared.push(id) });
  return { startup, changes, cleared, finishCount: () => finishes, timeout: () => elapsed() };
}
for (const state of ['authenticated', 'needs_login', 'needs_2fa']) test(`startup reveals the application exactly once for ${state}`, () => {
  const f = fixture();
  f.startup.observe({ state: 'checking' });
  f.startup.observe({ state: 'authenticating' });
  assert.equal(f.finishCount(), 0);
  f.startup.observe({ state }); f.startup.observe({ state }); f.timeout();
  assert.equal(f.finishCount(), 1);
  assert.deepEqual(f.startup.snapshot(), { phase: 'complete' });
  assert.deepEqual(f.cleared, [7]);
});
test('startup failures and watchdog expose recovery without changing account state', () => {
  for (const state of ['unverified', 'runtime_unavailable', 'watchdog']) {
    const f = fixture();
    if (state === 'watchdog') f.timeout(); else f.startup.observe({ state });
    assert.equal(f.finishCount(), 0);
    assert.deepEqual(f.changes, [{ phase: 'failed' }]);
    f.startup.observe({ state: 'checking' });
    assert.deepEqual(f.startup.snapshot(), { phase: 'failed' });
    f.startup.finish(); f.startup.finish();
    assert.equal(f.finishCount(), 1);
  }
});
test('late successful recovery can leave the failure screen but disposal cannot reopen UI', () => {
  const recovered = fixture(); recovered.timeout(); recovered.startup.observe({ state: 'authenticated' });
  assert.equal(recovered.finishCount(), 1);
  const closed = fixture(); closed.startup.dispose(); closed.timeout(); closed.startup.observe({ state: 'needs_login' }); closed.startup.finish();
  assert.equal(closed.finishCount(), 0); assert.deepEqual(closed.changes, []);
});
test('startup preload exposes only fixed presentation actions and copies its state', async () => {
  const calls = []; const listeners = new Map(); let exposed;
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, '../../src/preload/native-startup-preload.js'), 'utf8'), { require: name => {
    assert.equal(name, 'electron');
    return { contextBridge: { exposeInMainWorld: (key, value) => { assert.equal(key, 'nativeStartup'); exposed = value; } }, ipcRenderer: { invoke: async (...args) => { calls.push(args); return { phase: 'loading', token: 'SENTINEL' }; }, on: (key, value) => listeners.set(key, value), removeListener: key => listeners.delete(key) } };
  } });
  assert.deepEqual(Object.keys(exposed).sort(), ['enter', 'getState', 'onState', 'quit']);
  assert.equal(JSON.stringify(await exposed.getState()), '{"phase":"loading"}');
  let value; const unsubscribe = exposed.onState(state => { value = state; });
  listeners.get('native:startup-state')({}, { phase: 'bad', token: 'SENTINEL' });
  assert.equal(JSON.stringify(value), '{"phase":"failed"}');
  await exposed.enter(); await exposed.quit(); unsubscribe();
  assert.deepEqual(calls, [['native:startup-state'], ['native:startup-enter'], ['native:startup-quit']]);
  assert.equal(listeners.size, 0);
});
test('startup resources enter the exact native package whitelist', () => {
  const { UI_FILES } = require('../../electron/native-package');
  const config = require('../../electron-builder.native.cjs');
  for (const file of ['electron/native-startup.js', 'src/preload/native-startup-preload.js', 'src/renderer/native-startup.html', 'src/renderer/native-startup.js', 'src/renderer/native-startup.css']) {
    assert.ok(UI_FILES.includes(file)); assert.ok(config.files.includes(file)); assert.ok(fs.existsSync(path.resolve(__dirname, '../..', file)));
  }
});

test('startup defaults to a movable normal window, not fullscreen or frameless', () => {
  const source = fs.readFileSync(path.resolve(__dirname, '../../src/main/native-main.js'), 'utf8');
  const options = source.match(/startupWindow = new BrowserWindow\(\{([^\n]+)\}\);/)[1];
  assert.match(options, /width: 1100, height: 720/);
  assert.match(options, /center: true, fullscreen: false, frame: true/);
  assert.doesNotMatch(options, /fullscreen: true|frame: false/);
});

function mainFixture() {
  const calls = []; const handlers = new Map();
  const window = name => {
    const frame = { url: '' };
    return { isDestroyed: () => false, webContents: { mainFrame: frame, send() {} }, close: () => calls.push(name + '-close'), show: () => calls.push(name + '-show'), focus: () => calls.push(name + '-focus') };
  };
  const boot = window('splash'); const owner = window('main');
  const context = vm.createContext({ require: name => name === 'electron' ? { app: { whenReady: () => new Promise(() => {}), on() {}, quit: () => calls.push('quit') }, ipcMain: { handle: (name, action) => handlers.set(name, action) } } : require(name), __dirname: path.resolve(__dirname, '../../src/main'), process, console, URL, setInterval, clearInterval, fixtureBoot: boot, fixtureOwner: owner, fixtureCalls: calls });
  vm.runInContext(fs.readFileSync(path.resolve(__dirname, '../../src/main/native-main.js'), 'utf8'), context);
  vm.runInContext('startupWindow=fixtureBoot;owner=fixtureOwner;login={observe(){fixtureCalls.push("login");}};startup={snapshot(){return {phase:"loading"}},finish(){fixtureCalls.push("finish")},observe(value){fixtureCalls.push("startup-"+value.state)}};bindIPC();', context);
  boot.webContents.mainFrame.url = vm.runInContext('startupURL', context);
  owner.webContents.mainFrame.url = vm.runInContext('nativeURL', context);
  return { calls, handlers, context, boot, owner, event: win => ({ sender: win.webContents, senderFrame: win.webContents.mainFrame }) };
}
test('startup IPC accepts only the exact startup window and main frame', () => {
  const f = mainFixture();
  for (const name of ['native:startup-state', 'native:startup-enter', 'native:startup-quit']) {
    assert.throws(() => f.handlers.get(name)(f.event(f.owner)), /native_sender_rejected/);
    assert.throws(() => f.handlers.get(name)({ sender: f.boot.webContents, senderFrame: { url: f.boot.webContents.mainFrame.url } }), /native_sender_rejected/);
  }
  assert.equal(JSON.stringify(f.handlers.get('native:startup-state')(f.event(f.boot))), '{"phase":"loading"}');
  f.handlers.get('native:startup-enter')(f.event(f.boot));
  assert.deepEqual(f.calls, ['finish']);
});
test('actual main presentation gates login and reveals the owner before delivering its session', async () => {
  const f = mainFixture();
  vm.runInContext('observe({state:"checking"});observe({state:"needs_login"});mainLoaded=Promise.resolve();', f.context);
  assert.deepEqual(f.calls, ['startup-checking', 'startup-needs_login']);
  await vm.runInContext('revealMain()', f.context);
  assert.deepEqual(f.calls.slice(2), ['splash-close', 'main-show', 'main-focus', 'login']);
  await vm.runInContext('revealMain()', f.context);
  assert.equal(f.calls.filter(value => value === 'main-show').length, 1);
});
test('closing during the pending main-page load cannot reveal windows later', async () => {
  const f = mainFixture(); let release;
  f.context.fixtureLoaded = new Promise(resolve => { release = resolve; });
  const pending = vm.runInContext('mainLoaded=fixtureLoaded;revealMain()', f.context);
  vm.runInContext('closing=true;', f.context); release(); await pending;
  assert.deepEqual(f.calls, []);
});

test('recheck disposes failed bootstrap before one replacement, including concurrent clicks', async () => {
  const f = mainFixture(); let release;
  f.context.fixtureClose = new Promise(resolve => { release = resolve; });
  vm.runInContext('service={failed:true,closed:false,async close(){fixtureCalls.push("close-old");await fixtureClose;this.closed=true;}};start=async()=>{fixtureCalls.push("start-new");service={closed:false,failed:false}};refresh=async()=>{fixtureCalls.push("refresh")};', f.context);
  const first = f.handlers.get('native:recheck')(f.event(f.owner));
  const second = f.handlers.get('native:recheck')(f.event(f.owner));
  assert.deepEqual(f.calls, ['close-old']);
  release(); await Promise.all([first,second]);
  assert.deepEqual(f.calls, ['close-old','start-new','refresh']);
});

test('polling cannot overwrite the retained bootstrap failure with generic API errors', async () => {
  const f = mainFixture();
  vm.runInContext('service={failed:true,closed:false,api(){throw Error("must not poll a failed service")}};journal={record(){fixtureCalls.push("generic-error")}};', f.context);
  await vm.runInContext('refresh()', f.context);
  assert.deepEqual(f.calls, []);
});

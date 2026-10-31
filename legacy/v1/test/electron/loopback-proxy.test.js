'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const { EventEmitter } = require('node:events');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { PassThrough } = require('node:stream');

const {
  CONTROL_PROXY_STAGES,
  LOOPBACK_HOST,
  WslLoopbackProxy: WslLoopbackProxyBase,
  parseHttpHandshake,
} = require('../../electron/loopback-proxy');

function createWindowsHelperFixture(body = 'trusted helper fixture') {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-loopback-helper-'));
  const filename = path.join(directory, 'amdl-windows-probe.exe');
  fs.writeFileSync(filename, body);
  return {
    directory,
    filename,
    binding: {
      file: 'amdl-windows-probe.exe',
      size: fs.statSync(filename).size,
      sha256: crypto.createHash('sha256').update(fs.readFileSync(filename)).digest('hex'),
    },
  };
}

const defaultWindowsHelperFixture = createWindowsHelperFixture();
const rendererDirectory = path.resolve(__dirname, '../../src/renderer');
process.once('exit', () => fs.rmSync(defaultWindowsHelperFixture.directory, { recursive: true, force: true }));

class WslLoopbackProxy extends WslLoopbackProxyBase {
  constructor(options = {}) {
    super({
      windowsProbeExe: defaultWindowsHelperFixture.filename,
      windowsHelperBindingProvider: async () => defaultWindowsHelperFixture.binding,
      ...options,
    });
  }
}

test('connected desktop serves its packaged UI instead of an older same-version WSL UI', async () => {
  const harness = fakeServerHarness();
  const requests = [];
  const child = fakeChild();
  child.stdin.on('data', (chunk) => requests.push(chunk.toString()));
  let spawns = 0;
  const proxy = new WslLoopbackProxy({
    rendererDirectory,
    createServer: harness.createServer,
    spawn: () => { spawns += 1; return child; },
    authorizeRequest: () => true,
  });
  await proxy.start();
  try {
    for (const filename of ['index.html', 'job-data.js', 'renderer.js', 'browse.js', 'catalog.js', 'playback.js', 'styles.css', 'client.css', 'client-navigation.js']) {
      const socket = harness.connect();
      const target = filename === 'index.html' ? '/' : `/${filename}?v=old-runtime`;
      socket.send(`GET ${target} HTTP/1.1\r\nHost: 127.0.0.1:41234\r\n\r\n`);
      await settle();
      const output = Buffer.concat(socket.outbound);
      const boundary = output.indexOf('\r\n\r\n');
      assert.match(output.toString(), /^HTTP\/1.1 200 OK/);
      assert.deepEqual(output.subarray(boundary + 4), fs.readFileSync(path.join(rendererDirectory, filename)));
      assert.match(output.toString(), /Cache-Control: no-store/);
      assert.equal(socket.ended, true);
    }
    assert.equal(spawns, 0, 'WSL must never supply desktop HTML, CSS or scripts');
    const api = harness.connect();
    api.send(requestHeaders());
    await settle();
    assert.equal(spawns, 1, 'download API still uses the authenticated WSL bridge');
    assert.match(requests.join(''), /GET \/api\/v1\/health\/ready/);
  } finally { await proxy.stop(); }
});

test('packaged UI keeps authorization, HEAD framing and missing-resource failure closed', async () => {
  const harness = fakeServerHarness();
  let authorized = false;
  let spawns = 0;
  const proxy = new WslLoopbackProxy({ rendererDirectory, createServer: harness.createServer,
    authorizeRequest: () => authorized, spawn: () => { spawns += 1; return fakeChild(); } });
  await proxy.start();
  try {
    const denied = harness.connect();
    denied.send('GET / HTTP/1.1\r\nHost: localhost\r\n\r\n');
    await settle();
    assert.match(Buffer.concat(denied.outbound).toString(), /^HTTP\/1.1 403/);
    authorized = true;
    const head = harness.connect();
    head.send('HEAD /client.css?v=test HTTP/1.1\r\nHost: localhost\r\n\r\n');
    await settle();
    const output = Buffer.concat(head.outbound).toString();
    assert.match(output, /^HTTP\/1.1 200/);
    assert.match(output, new RegExp(`Content-Length: ${fs.statSync(path.join(rendererDirectory, 'client.css')).size}\\r\\n`));
    assert.equal(output.split('\r\n\r\n')[1], '');
    const post = harness.connect();
    post.send('POST / HTTP/1.1\r\nHost: localhost\r\n\r\n');
    await settle();
    assert.match(Buffer.concat(post.outbound).toString(), /^HTTP\/1.1 405/);
    proxy.rendererDirectory = path.join(rendererDirectory, 'missing-fixture');
    const missing = harness.connect();
    missing.send('GET / HTTP/1.1\r\nHost: localhost\r\n\r\n');
    await settle();
    assert.match(Buffer.concat(missing.outbound).toString(), /^HTTP\/1.1 503/);
    assert.equal(spawns, 0, 'a missing desktop asset must not fall back to the stale WSL UI');
  } finally { await proxy.stop(); }
});

test('stop during packaged UI authorization cannot publish a late page', async () => {
  const harness = fakeServerHarness();
  let resume;
  const authorized = new Promise((resolve) => { resume = resolve; });
  const proxy = new WslLoopbackProxy({ rendererDirectory, createServer: harness.createServer,
    authorizeRequest: () => authorized, spawn: () => { throw new Error('unexpected helper'); } });
  await proxy.start();
  const socket = harness.connect();
  socket.send('GET / HTTP/1.1\r\nHost: localhost\r\n\r\n');
  await proxy.stop();
  resume(true);
  await settle();
  assert.equal(socket.outbound.length, 0);
});

test('Go and Electron proxy stage allowlists remain exactly synchronized', () => {
  const source = fs.readFileSync(path.join(
    __dirname,
    '..',
    '..',
    'apple-music-downloader',
    'internal',
    'appliance',
    'protocol.go',
  ), 'utf8');
  const goStages = [...source.matchAll(/ProxyStage[A-Za-z]+\s+ProxyErrorStage\s+=\s+"([a-z_]+)"/g)]
    .map((match) => match[1])
    .sort();
  assert.deepEqual([...CONTROL_PROXY_STAGES].sort(), goStages);
});

test('an unknown non-empty backend stage fails closed as backend_unavailable', () => {
  const proxy = new WslLoopbackProxy();
  const base = {
    correlationId: 7,
    responseHeadersReceived: false,
    helperPid: null,
    requestBytes: 0,
    responseBytes: 0,
    stderrBytes: 0,
    controlProxyStage: 'unlisted_stage',
  };
  assert.match(proxy.connectionDiagnostic(base, 'close', 503), /control_stage=backend_unavailable/);
  assert.match(proxy.connectionDiagnostic({ ...base, controlProxyStage: null }, 'close', 503), /control_stage=none/);
});

async function settle() {
  for (let index = 0; index < 4; index += 1) await new Promise((resolve) => setImmediate(resolve));
}

class FakeSocket extends EventEmitter {
  constructor() {
    super();
    this.remoteAddress = '127.0.0.1';
    this.outbound = [];
    this.pipeDestination = null;
    this.destroyed = false;
    this.ended = false;
    this.idleTimeoutMs = null;
    this.idleHandler = null;
  }

  pause() { this.paused = true; }
  resume() { this.paused = false; }
  setTimeout(milliseconds, callback) { this.idleTimeoutMs = milliseconds; this.idleHandler = callback; }
  pipe(destination) { this.pipeDestination = destination; return destination; }
  unpipe(destination) { if (!destination || this.pipeDestination === destination) this.pipeDestination = null; }
  send(value) {
    const chunk = Buffer.from(value);
    if (this.pipeDestination) this.pipeDestination.write(chunk);
    else this.emit('data', chunk);
  }
  write(value, callback) {
    this.outbound.push(Buffer.from(value));
    if (callback) callback();
    return true;
  }
  end(value, callback) {
    if (value) this.write(value);
    this.ended = true;
    if (callback) callback();
  }
  destroy() {
    if (this.destroyed) return;
    this.destroyed = true;
    this.emit('close');
  }
}

function fakeChild() {
  const child = new EventEmitter();
  child.stdin = new PassThrough();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.killed = false;
	child.pid = 4242;
  child.kill = () => {
    if (child.killed) return;
    child.killed = true;
    queueMicrotask(() => child.emit('close', null, 'SIGTERM'));
  };
  return child;
}

function requestHeaders() {
  return 'GET /api/v1/health/ready HTTP/1.1\r\nHost: 127.0.0.1:41234\r\n\r\n';
}

function fakeServerHarness() {
  let connectionHandler;
  let listenOptions;
  const server = new EventEmitter();
  server.listen = (options, callback) => { listenOptions = options; callback(); };
  server.address = () => ({ address: LOOPBACK_HOST, port: 41234 });
  server.close = (callback) => callback();
  return {
    createServer(options, handler) {
      assert.deepEqual(options, { pauseOnConnect: true });
      connectionHandler = handler;
      return server;
    },
    connect(socket = new FakeSocket()) { connectionHandler(socket); return socket; },
    listenOptions: () => listenOptions,
  };
}

function deferredServerHarness() {
  const records = [];
  return {
    createServer(options, handler) {
      assert.deepEqual(options, { pauseOnConnect: true });
      const server = new EventEmitter();
      const record = { server, handler, listenCallback: null, listenOptions: null, closeCalls: 0 };
      server.listen = (listenOptions, callback) => {
        record.listenOptions = listenOptions;
        record.listenCallback = callback;
      };
      server.address = () => ({ address: LOOPBACK_HOST, port: 42000 + records.length });
      server.close = (callback) => { record.closeCalls += 1; callback(); };
      records.push(record);
      return server;
    },
    release(index = 0) { records[index].listenCallback(); },
    records,
  };
}

test('concurrent proxy starts share one pending listener', async () => {
  const harness = deferredServerHarness();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer });

  const first = proxy.start(0);
  const second = proxy.start(0);
  assert.equal(first, second);
  assert.equal(harness.records.length, 1);
  harness.release();

  assert.deepEqual(await Promise.all([first, second]), [42001, 42001]);
  assert.equal(proxy.port, 42001);
  await proxy.stop();
  assert.equal(harness.records[0].closeCalls, 1);
});

test('stop waits for a pending proxy start and closes the resulting listener', async () => {
  const harness = deferredServerHarness();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer });

  const starting = proxy.start(0);
  const stopping = proxy.stop();
  assert.equal(harness.records[0].closeCalls, 0);
  harness.release();

  assert.equal(await starting, 42001);
  await stopping;
  assert.equal(harness.records.length, 1);
  assert.equal(harness.records[0].closeCalls, 1);
  assert.equal(proxy.server, null);
  assert.equal(proxy.port, null);
});

test('start requested behind a pending stop creates a fresh listener', async () => {
  const harness = deferredServerHarness();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer });

  const firstStart = proxy.start(0);
  const stopping = proxy.stop();
  const restart = proxy.start(0);
  harness.release(0);

  assert.equal(await firstStart, 42001);
  await stopping;
  assert.equal(harness.records[0].closeCalls, 1);
  assert.equal(harness.records.length, 2);
  harness.release(1);
  assert.equal(await restart, 42002);
  assert.equal(proxy.port, 42002);
  await proxy.stop();
  assert.equal(harness.records[1].closeCalls, 1);
});

test('a synchronous listener construction failure returns the proxy to stopped', async () => {
  const failure = new Error('listener construction failed');
  const proxy = new WslLoopbackProxy({ createServer: () => { throw failure; } });

  await assert.rejects(proxy.start(0), (error) => error === failure);
  assert.equal(proxy.lifecycleState, 'stopped');
  assert.equal(proxy.admissionOpen, false);
  assert.equal(proxy.server, null);
  assert.equal(proxy.port, null);
});

test('listener errors record a fixed stage without retaining the original error text', async () => {
  const harness = deferredServerHarness();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer });
  const starting = proxy.start();
  harness.release();
  await starting;

  harness.records[0].server.emit('error', new Error('listener-secret-sentinel'));

  assert.match(proxy.diagnostics(), /stage=listener_error/);
  assert.doesNotMatch(proxy.diagnostics(), /listener-secret-sentinel/);
  await proxy.stop();
});

test('a listener close failure retains the listener for a later stop retry', async () => {
  const server = new EventEmitter();
  let closeCalls = 0;
  server.listen = (_options, callback) => callback();
  server.address = () => ({ address: LOOPBACK_HOST, port: 42009 });
  server.close = (callback) => {
    closeCalls += 1;
    if (closeCalls === 1) callback(new Error('close sentinel'));
    else callback();
  };
  const proxy = new WslLoopbackProxy({ createServer: () => server });
  await proxy.start(0);

  await assert.rejects(proxy.stop(), /close sentinel/);
  assert.equal(proxy.lifecycleState, 'stopping');
  assert.equal(proxy.closingServer, server);
  await proxy.stop();
  assert.equal(closeCalls, 2);
  assert.equal(proxy.lifecycleState, 'stopped');
  assert.equal(proxy.closingServer, null);
});

test('fallback proxy validates an HTTP handshake before invoking fixed proxy-stdio argv', async () => {
  let spawnCall;
  const child = fakeChild();
  const harness = fakeServerHarness();
  const proxy = new WslLoopbackProxy({
    processEnvironment: {
      Path: 'C:\\Windows\\System32',
      APPLE_PASSWORD: 'proxy-password-sentinel',
      MEDIA_USER_TOKEN: 'proxy-token-sentinel',
    },
    createServer: harness.createServer,
    spawn: (command, args, options) => {
      spawnCall = { command, args, options };
      return child;
    },
  });

  assert.equal(await proxy.start(0), 41234);
  assert.deepEqual(harness.listenOptions(), { host: '127.0.0.1', port: 0, exclusive: true });

  const socket = harness.connect();
  assert.equal(spawnCall, undefined, 'a bare TCP connection must not create a WSL process');
  socket.send('GET /api/v1/health/ready HTTP/1.1\r\nHost: 127.0.0.1:41234\r\n\r\n');
  await settle();

  assert.equal(spawnCall.command, defaultWindowsHelperFixture.filename);
  assert.deepEqual(spawnCall.args, ['proxy-wsl-stdio']);
  assert.equal(spawnCall.options.shell, false);
  assert.deepEqual(spawnCall.options.stdio, ['pipe', 'pipe', 'pipe']);
  assert.deepEqual(spawnCall.options.env, { Path: 'C:\\Windows\\System32' });
  await proxy.stop();
});

test('proxy fails closed before spawn when the packaged helper binding is unavailable', async () => {
  let spawnCalls = 0;
  const harness = fakeServerHarness();
  const proxy = new WslLoopbackProxyBase({
    windowsProbeExe: defaultWindowsHelperFixture.filename,
    createServer: harness.createServer,
    spawn: () => { spawnCalls += 1; return fakeChild(); },
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();

  assert.equal(spawnCalls, 0, 'a helper without a descriptor binding must never execute');
  assert.match(Buffer.concat(socket.outbound).toString('utf8'), /^HTTP\/1\.1 502 Bad Gateway\r\n/);
  assert.match(proxy.diagnostics(), /stage=helper_identity/);
  await proxy.stop();
});

test('proxy rejects a response when the helper changes after spawn and before close', async (t) => {
  const fixture = createWindowsHelperFixture('original helper bytes');
  t.after(() => fs.rmSync(fixture.directory, { recursive: true, force: true }));
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxyBase({
    windowsProbeExe: fixture.filename,
    windowsHelperBindingProvider: async () => fixture.binding,
    createServer: harness.createServer,
    spawn: () => child,
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();

  fs.appendFileSync(fixture.filename, ' changed after spawn');
  child.stdout.end('HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n');
  child.emit('close', 0, null);
  await settle();

  assert.equal(socket.destroyed, true, 'post-exit helper identity drift must invalidate the response');
  assert.equal(socket.ended, false);
  assert.match(proxy.diagnostics(), /stage=helper_identity/);
  await proxy.stop();
});

test('stop prevents spawn when helper binding resolution is still pending', async () => {
  const harness = fakeServerHarness();
  let releaseBinding;
  let spawnCalls = 0;
  const bindingRequested = new Promise((resolve) => {
    releaseBinding = resolve;
  });
  const proxy = new WslLoopbackProxyBase({
    windowsProbeExe: defaultWindowsHelperFixture.filename,
    windowsHelperBindingProvider: async () => {
      await bindingRequested;
      return defaultWindowsHelperFixture.binding;
    },
    createServer: harness.createServer,
    spawn: () => { spawnCalls += 1; return fakeChild(); },
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();

  const stopping = proxy.stop();
  releaseBinding();
  await stopping;
  await settle();

  assert.equal(spawnCalls, 0, 'a binding resolution released after stop must not spawn a helper');
  assert.equal(socket.destroyed, true);
});

test('post-spawn identity drift keeps stop pending until the killed helper closes', async (t) => {
  const fixture = createWindowsHelperFixture('trusted helper bytes');
  t.after(() => fs.rmSync(fixture.directory, { recursive: true, force: true }));
  const harness = fakeServerHarness();
  const child = fakeChild();
  child.kill = () => { child.killed = true; };
  const proxy = new WslLoopbackProxyBase({
    windowsProbeExe: fixture.filename,
    windowsHelperBindingProvider: async () => fixture.binding,
    createServer: harness.createServer,
    spawn: () => {
      fs.appendFileSync(fixture.filename, ' post-spawn drift');
      return child;
    },
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  assert.equal(child.killed, true, 'post-spawn identity drift must terminate the helper');

  let stopped = false;
  const stopping = proxy.stop().then(() => { stopped = true; });
  await settle();
  assert.equal(stopped, false, 'stop must retain ownership until the killed helper closes');
  child.emit('close', null, 'SIGTERM');
  await stopping;
  assert.equal(stopped, true);
});

test('pre-header bridge spawn failure returns 502 and records only the spawn stage', async () => {
  const harness = fakeServerHarness();
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    spawn: () => { throw new Error('spawn-secret-sentinel'); },
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();

  const response = Buffer.concat(socket.outbound).toString('utf8');
  assert.match(response, /^HTTP\/1\.1 502 Bad Gateway\r\n/);
  assert.match(proxy.diagnostics(), /stage=spawn/);
  assert.doesNotMatch(proxy.diagnostics(), /spawn-secret-sentinel/);
  await proxy.stop();
});

test('pre-header bridge close returns 503 and records only the close stage', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stderr.write('apple-response-secret-sentinel');
  child.stderr.write('appliance proxy failed: read_');
  child.stderr.write('frame\n');
  child.emit('close', 17, 'SIGTERM');
  await settle();

  const response = Buffer.concat(socket.outbound).toString('utf8');
  assert.match(response, /^HTTP\/1\.1 503 Service Unavailable\r\n/);
  assert.match(proxy.diagnostics(), /id=1 stage=close helper_pid=4242 exit_code=17 signal=SIGTERM response_headers=false request_bytes=\d+ response_bytes=0 stderr_bytes=\d+ status=503 control_stage=read_frame/);
  assert.doesNotMatch(proxy.diagnostics(), /apple-response-secret-sentinel/);
  await proxy.stop();
});

test('pre-header stderr pipe error returns a redacted 502 instead of escaping EventEmitter', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stderr.emit('error', new Error('stderr-secret-sentinel'));
  await settle();

  assert.match(Buffer.concat(socket.outbound).toString('utf8'), /^HTTP\/1\.1 502 Bad Gateway\r\n/);
  assert.match(proxy.diagnostics(), /stage=bridge_stderr/);
  assert.doesNotMatch(proxy.diagnostics(), /stderr-secret-sentinel/);
  await proxy.stop();
});

test('pre-header response timeout returns 504 and records only the timeout stage', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const timers = [];
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    responseHeaderTimeoutMs: 123,
    setTimeout: (callback, delay) => {
      const timer = { callback, delay };
      timers.push(timer);
      return timer;
    },
    clearTimeout: () => {},
    spawn: () => child,
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  const responseTimer = timers.find((timer) => timer.delay === 123);
  assert.ok(responseTimer, 'response-header timer must be installed after spawning');
  responseTimer.callback();
  await settle();

  const response = Buffer.concat(socket.outbound).toString('utf8');
  assert.match(response, /^HTTP\/1\.1 504 Gateway Timeout\r\n/);
  assert.match(proxy.diagnostics(), /stage=response_headers_timeout/);
  await proxy.stop();
});

test('a response that already emitted headers never receives a second proxy error response', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const timers = [];
  const clearedTimers = [];
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    responseHeaderTimeoutMs: 123,
    setTimeout: (callback, delay) => {
      const timer = { callback, delay };
      timers.push(timer);
      return timer;
    },
    clearTimeout: (timer) => { clearedTimers.push(timer); },
    spawn: () => child,
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stdout.write('HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n');
  await settle();
  const responseBeforeClose = Buffer.concat(socket.outbound).toString('utf8');
  const responseTimer = timers.find((timer) => timer.delay === 123);
  assert.ok(responseTimer);
  assert.ok(clearedTimers.includes(responseTimer), 'response-header timer must be cancelled after headers arrive');
  child.emit('close', 0, null);
  responseTimer.callback();
  await settle();

  const response = Buffer.concat(socket.outbound).toString('utf8');
  assert.equal(response, responseBeforeClose);
  assert.match(response, /^HTTP\/1\.1 200 OK\r\n/);
  assert.doesNotMatch(response, /502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout/);
  await proxy.stop();
});

test('post-header abnormal helper close aborts a truncated stream without a second response', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stdout.write('HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\npartial');
  await settle();
  const forwarded = Buffer.concat(socket.outbound).toString('utf8');
  child.emit('close', 17, 'SIGSEGV');
  await settle();

  assert.equal(socket.destroyed, true);
  assert.equal(socket.ended, false, 'an abnormal close must not look like a clean EOF');
  assert.equal(Buffer.concat(socket.outbound).toString('utf8'), forwarded);
  assert.doesNotMatch(forwarded, /502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout/);
  assert.match(proxy.diagnostics(), /stage=stream_close/);
  await proxy.stop();
});

test('post-header helper close with an unknown exit code fails closed instead of completing the response', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stdout.end('HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n');
  await settle();
  child.emit('close', null, null);
  await settle();

  assert.equal(socket.destroyed, true, 'an unknown helper outcome must not be treated as a clean response');
  assert.equal(socket.ended, false);
  assert.match(proxy.diagnostics(), /stage=stream_close/);
  await proxy.stop();
});

test('post-header stderr pipe error aborts only the existing response stream', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stdout.write('HTTP/1.1 200 OK\r\n\r\npartial');
  await settle();
  const forwarded = Buffer.concat(socket.outbound).toString('utf8');
  child.stderr.emit('error', new Error('stderr-secret-sentinel'));
  await settle();

  assert.equal(socket.destroyed, true);
  assert.equal(Buffer.concat(socket.outbound).toString('utf8'), forwarded);
  assert.doesNotMatch(forwarded, /502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout/);
  assert.doesNotMatch(proxy.diagnostics(), /stderr-secret-sentinel/);
  await proxy.stop();
});

test('a post-response bridge stdin error waits for a clean helper close before preserving the primary response', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send('POST /upload HTTP/1.1\r\nHost: 127.0.0.1:41234\r\nContent-Length: 0\r\n\r\n');
  await settle();
  child.stdout.end('HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK');
  await settle();
  child.stdin.emit('error', new Error('synthetic upload teardown'));
  await settle();

  const response = Buffer.concat(socket.outbound).toString('latin1');
  assert.match(response, /^HTTP\/1\.1 200 OK\r\n/);
  assert.match(response, /\r\n\r\nOK$/);
  assert.equal(socket.destroyed, false, 'a completed response must not be destroyed by upload teardown');
  assert.equal(socket.ended, false, 'a stream error alone cannot prove post-response teardown');
  child.emit('close', 0, null);
  await settle();
  assert.equal(socket.ended, true);
  assert.match(proxy.diagnostics(), /stage=post_response_teardown/);
  await proxy.stop();
});

test('a Go download_stream marker never preserves a truncated response as teardown', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stdout.end('HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nab');
  child.stderr.write('appliance proxy failed: download_stream\n');
  await settle();
  child.emit('close', 34, null);
  await settle();

  assert.equal(socket.destroyed, true, 'a framing-truncated response must remain an error');
  assert.doesNotMatch(proxy.diagnostics(), /stage=post_response_teardown/);
  await proxy.stop();
});

test('a response-before-upload_stream failure never preserves the already-forwarded response', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  child.stdout.end('HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK');
  await settle();
  child.stderr.write('appliance proxy failed: upload_stream\n');
  child.emit('close', 34, null);
  await settle();

  assert.equal(socket.destroyed, true, 'a response-before-upload failure must remain failed closed');
  assert.equal(socket.ended, false);
  assert.doesNotMatch(proxy.diagnostics(), /stage=post_response_teardown/);
  assert.match(proxy.diagnostics(), /control_stage=upload_stream/);
  await proxy.stop();
});

test('backend bytes remain gated until a complete valid response header then stream without a body cap', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();

  child.stdout.write('HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n');
  await settle();
  assert.equal(socket.outbound.length, 0, 'partial response headers must not reach Chromium');

  child.stdout.write('\r\nevent: ready\ndata: {}\n\n');
  await settle();
  const largeChunk = Buffer.alloc(2 * 1024 * 1024, 0x61);
  child.stdout.write(largeChunk);
  await settle();
  const response = Buffer.concat(socket.outbound);
  assert.match(response.subarray(0, 96).toString('utf8'), /^HTTP\/1\.1 200 OK\r\nContent-Type: text\/event-stream\r\n\r\nevent: ready/);
  assert.ok(response.length >= largeChunk.length, 'response bodies must stream without a total-size buffer');
  await proxy.stop();
});

test('response handoff preserves queued body bytes and waits for backpressure before clean close', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = new FakeSocket();
  let firstWrite = true;
  let firstWriteCallback = null;
  socket.write = (value, callback) => {
    socket.outbound.push(Buffer.from(value));
    if (firstWrite) {
      firstWrite = false;
      firstWriteCallback = callback || null;
      return false;
    }
    callback?.();
    return true;
  };
  harness.connect(socket);
  socket.send(requestHeaders());
  await settle();

  child.stdout.write('HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\nfirst-');
  child.stdout.end('second');
  child.emit('close', 0, null);
  await settle();

  assert.equal(socket.ended, false, 'clean child close must wait for gated stdout to drain');
  firstWriteCallback?.();
  socket.emit('drain');
  await settle();
  assert.equal(
    Buffer.concat(socket.outbound).toString('utf8'),
    'HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\nfirst-second',
  );
  assert.equal(socket.ended, true);
  await proxy.stop();
});

test('an oversized or malformed backend response header fails closed before forwarding bytes', async () => {
  for (const payload of [
    Buffer.alloc(33 * 1024, 0x61),
    Buffer.from('not-http\r\nHeader: value\r\n\r\n', 'utf8'),
    Buffer.from('HTTP/1.1 200 OK\r\nMissing-Colon\r\n\r\n', 'utf8'),
    Buffer.from('HTTP/1.1 200 OK\r\n Folded: value\r\n\r\n', 'utf8'),
    Buffer.from('HTTP/1.1 200 OK\r\nX-Bad: value\x00suffix\r\n\r\n', 'latin1'),
  ]) {
    const harness = fakeServerHarness();
    const child = fakeChild();
    const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
    await proxy.start();
    const socket = harness.connect();
    socket.send(requestHeaders());
    await settle();
    child.stdout.write(payload);
    await settle();
    const response = Buffer.concat(socket.outbound).toString('utf8');
    assert.match(response, /^HTTP\/1\.1 502 Bad Gateway\r\n/);
    assert.doesNotMatch(response, /not-http|a{128}/);
    assert.match(proxy.diagnostics(), /stage=response_headers_invalid/);
    await proxy.stop();
  }
});

test('stop waits for every killed helper to emit close', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  child.kill = () => { child.killed = true; };
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();

  let stopped = false;
  const stopping = proxy.stop().then(() => { stopped = true; });
  await settle();
  assert.equal(child.killed, true);
  assert.equal(stopped, false, 'stop resolved before the helper close event');
  child.stderr.emit('error', new Error('post-kill-stderr-secret-sentinel'));
  assert.doesNotMatch(proxy.diagnostics(), /post-kill-stderr-secret-sentinel/);
  child.emit('close', null, 'SIGTERM');
  await stopping;
  assert.equal(stopped, true);
});

test('stop fails closed and retains a helper whose close cannot be confirmed', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const timers = [];
  child.kill = () => { child.killed = true; };
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    spawn: () => child,
    childExitTimeoutMs: 321,
    setTimeout: (callback, delay) => {
      const timer = { callback, delay };
      timers.push(timer);
      return timer;
    },
    clearTimeout: () => {},
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();

  const stopping = proxy.stop();
  await settle();
  const closeTimer = timers.find((timer) => timer.delay === 321);
  assert.ok(closeTimer, 'helper close confirmation timer was not installed');
  closeTimer.callback();
  await assert.rejects(stopping, /helper exit was not confirmed/);
  assert.equal(child.killed, true);
  assert.equal(proxy.children.has(child), true, 'unconfirmed helper must remain tracked');
  assert.match(proxy.diagnostics(), /stage=helper_close_timeout helper_pid=4242/);
  proxy.children.delete(child);
});

test('stop closes admission before waiting for helpers and rejects a late connection without spawning', async () => {
  const harness = fakeServerHarness();
  const firstChild = fakeChild();
  let spawnCalls = 0;
  firstChild.kill = () => { firstChild.killed = true; };
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    spawn: () => { spawnCalls += 1; return firstChild; },
    childExitTimeoutMs: 1_000,
  });
  await proxy.start();
  const first = harness.connect();
  first.send(requestHeaders());
  await settle();
  assert.equal(spawnCalls, 1);

  const stopping = proxy.stop();
  await settle();
  assert.equal(proxy.lifecycleState, 'stopping');
  const late = harness.connect();
  await settle();
  assert.equal(spawnCalls, 1, 'late admission must not create another helper');
  assert.equal(late.ended, true);
  assert.match(Buffer.concat(late.outbound).toString('utf8'), /^HTTP\/1\.1 503 Service Unavailable\r\n/);

  firstChild.emit('close', null, 'SIGTERM');
  await stopping;
  assert.equal(proxy.lifecycleState, 'stopped');
});

test('rejected stopping and capacity sockets absorb late transport errors', async () => {
  const harness = fakeServerHarness();
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, maxConnections: 1 });
  await proxy.start();

  proxy.admissionOpen = false;
  proxy.setLifecycleState('stopping');
  const stoppingSocket = harness.connect();
  assert.equal(stoppingSocket.ended, true);
  assert.doesNotThrow(() => stoppingSocket.emit('error', new Error('late stopping reset')));

  proxy.admissionOpen = true;
  proxy.setLifecycleState('accepting');
  proxy.connections.add({});
  const capacitySocket = harness.connect();
  assert.equal(capacitySocket.ended, true);
  assert.doesNotThrow(() => capacitySocket.emit('error', new Error('late capacity reset')));
  proxy.connections.clear();

  await proxy.stop();
});

test('stop prevents helper spawn when authorization is still awaiting a decision', async () => {
  const harness = fakeServerHarness();
  let releaseAuthorization;
  let spawnCalls = 0;
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    authorizeRequest: async () => new Promise((resolve) => { releaseAuthorization = resolve; }),
    spawn: () => { spawnCalls += 1; return fakeChild(); },
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  assert.equal(typeof releaseAuthorization, 'function');

  const stopping = proxy.stop();
  releaseAuthorization(true);
  await stopping;
  await settle();
  assert.equal(spawnCalls, 0, 'a stop during authorization must not create a helper');
  assert.equal(proxy.lifecycleState, 'stopped');
});

test('an unconfirmed stop retains stopping ownership and rejects overlapping restart', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const timers = [];
  child.kill = () => { child.killed = true; };
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    spawn: () => child,
    childExitTimeoutMs: 123,
    setTimeout: (callback, delay) => {
      const timer = { callback, delay };
      timers.push(timer);
      return timer;
    },
    clearTimeout: () => {},
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send(requestHeaders());
  await settle();
  const stopping = proxy.stop();
  await settle();
  timers.find((timer) => timer.delay === 123).callback();
  await assert.rejects(stopping, /helper exit was not confirmed/);
  assert.equal(proxy.lifecycleState, 'stopping');
  await assert.rejects(proxy.start(0), /stopping/);
  assert.equal(proxy.children.has(child), true);
  child.emit('close', null, 'SIGKILL');
  await proxy.stop();
  assert.equal(proxy.lifecycleState, 'stopped');
});

test('proxy authorization and header injection hooks run before WSL spawn and overwrite spoofed headers', async () => {
  const child = fakeChild();
  const stdin = [];
  child.stdin.on('data', (chunk) => stdin.push(Buffer.from(chunk)));
  let releaseAuthorization;
  let spawnCalls = 0;
  let observed;
  const authorization = new Promise((resolve) => { releaseAuthorization = resolve; });
  const harness = fakeServerHarness();
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    authorizeRequest: async (metadata) => { observed = metadata; return authorization; },
    injectRequestHeaders: () => ({ 'X-AMDL-Proxy-Authorization': 'trusted-ephemeral-value' }),
    spawn: () => { spawnCalls += 1; return child; },
  });
  await proxy.start();
  const socket = harness.connect();
  socket.send('GET /events HTTP/1.1\r\nHost: localhost:41234\r\nX-AMDL-Proxy-Authorization: spoofed\r\n\r\n');
  await settle();
  assert.equal(spawnCalls, 0, 'authorization must complete before spawning WSL');
  assert.equal(observed.method, 'GET');
  assert.equal(observed.target, '/events');
  assert.deepEqual(observed.headers.host, ['localhost:41234']);

  releaseAuthorization({ authorized: true });
  await settle();
  assert.equal(spawnCalls, 1);
  const forwarded = Buffer.concat(stdin).toString('latin1');
  assert.match(forwarded, /X-AMDL-Proxy-Authorization: trusted-ephemeral-value\r\n/);
  assert.match(forwarded, /Connection: close\r\n/);
  assert.doesNotMatch(forwarded, /spoofed/);
  child.stderr.write('bridge rejected trusted-ephemeral-value\n');
  await settle();
  assert.doesNotMatch(proxy.diagnostics(), /trusted-ephemeral-value/);
	assert.doesNotMatch(proxy.diagnostics(), /bridge rejected/);
  await proxy.stop();
});

test('unauthorized or malformed handshakes never spawn a WSL process', async () => {
  for (const scenario of [
    { request: 'CONNECT example.test:443 HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n' },
    { request: 'GET http://example.test/ HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n' },
    { request: 'GET / HTTP/1.1\r\nHost: example.test\r\n\r\n' },
    { request: 'GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n', authorizeRequest: () => false },
  ]) {
    let spawnCalls = 0;
    const harness = fakeServerHarness();
    const proxy = new WslLoopbackProxy({
      createServer: harness.createServer,
      authorizeRequest: scenario.authorizeRequest,
      spawn: () => { spawnCalls += 1; return fakeChild(); },
    });
    await proxy.start();
    const socket = harness.connect();
    socket.send(scenario.request);
    await settle();
    assert.equal(spawnCalls, 0);
    assert.equal(socket.ended, true);
    assert.match(Buffer.concat(socket.outbound).toString('utf8'), /HTTP\/1\.1 (?:400|403|405)/);
    await proxy.stop();
  }
});

test('connection limit rejects excess pending handshakes without spawning WSL', async () => {
  let spawnCalls = 0;
  const harness = fakeServerHarness();
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    maxConnections: 1,
    spawn: () => { spawnCalls += 1; return fakeChild(); },
  });
  await proxy.start();
  const first = harness.connect();
  const excess = harness.connect();
  await settle();
  assert.equal(spawnCalls, 0);
  assert.equal(first.destroyed, false);
  assert.equal(excess.ended, true);
  assert.match(Buffer.concat(excess.outbound).toString('utf8'), /503 Service Unavailable/);
  assert.match(proxy.diagnostics(), /concurrency limit/);
  await proxy.stop();
  assert.equal(first.destroyed, true);
});

test('handshake and established-connection timeouts release resources', async () => {
  const handshakeTimers = [];
  const harness = fakeServerHarness();
  const child = fakeChild();
  const proxy = new WslLoopbackProxy({
    createServer: harness.createServer,
    spawn: () => child,
    setTimeout: (callback) => { handshakeTimers.push(callback); return callback; },
    clearTimeout: () => {},
  });
  await proxy.start();

  const stalled = harness.connect();
  handshakeTimers.shift()();
  assert.equal(stalled.ended, true);
  assert.match(Buffer.concat(stalled.outbound).toString('utf8'), /408 Request Timeout/);

  const active = harness.connect();
  active.send('GET /api/v1/jobs HTTP/1.1\r\nHost: 127.0.0.1:41234\r\n\r\n');
  await settle();
  assert.equal(child.killed, false);
  active.idleHandler();
  assert.equal(active.destroyed, true);
  assert.equal(child.killed, true);
  await proxy.stop();
});

test('accepted connections remain raw streaming pipes for SSE and large downloads', async () => {
  const harness = fakeServerHarness();
  const child = fakeChild();
  const receivedByBackend = [];
  child.stdin.on('data', (chunk) => receivedByBackend.push(Buffer.from(chunk)));
  const proxy = new WslLoopbackProxy({ createServer: harness.createServer, spawn: () => child });
  await proxy.start();
  const socket = harness.connect();
  socket.send('POST /upload HTTP/1.1\r\nHost: 127.0.0.1:41234\r\nContent-Length: 2097152\r\n\r\n');
  await settle();
  const largeChunk = Buffer.alloc(2 * 1024 * 1024, 0x61);
  socket.send(largeChunk);
  child.stdout.write('HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\nevent: progress\ndata: {}\n\n');
  child.stdout.write(largeChunk);
  await settle();

  assert.ok(Buffer.concat(receivedByBackend).length >= largeChunk.length, 'request bodies stream without proxy buffering limits');
  assert.ok(Buffer.concat(socket.outbound).length >= largeChunk.length, 'SSE and large responses stream without proxy buffering limits');
  assert.equal(socket.ended, false);
  await proxy.stop();
});

test('HTTP parser rejects ambiguous request framing', () => {
  const request = Buffer.from('POST / HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n');
  assert.throws(() => parseHttpHandshake(request), /Ambiguous HTTP request framing/);
});

'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const net = require('node:net');
const crypto = require('node:crypto');
const { spawn } = require('node:child_process');

const root = path.resolve(__dirname, '../..');
const executable = path.join(root, 'electron', 'bin', 'amdl-desktop.exe');
const args = process.argv.slice(2);

async function unusedPort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}

function portOpen(port) {
  return new Promise((resolve) => {
    const socket = net.connect({ port, host: '127.0.0.1' });
    let finished = false;
    const finish = (open) => { if (!finished) { finished = true; socket.destroy(); resolve(open); } };
    socket.setTimeout(500, () => finish(false));
    socket.once('connect', () => finish(true));
    socket.once('error', () => finish(false));
  });
}

function controller(manifest, state, port) {
  const child = spawn(executable, ['--manifest', manifest, '--state-dir', state, '--port', String(port)], {
    shell: false, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
    env: { SystemRoot: process.env.SystemRoot, WINDIR: process.env.WINDIR, TEMP: process.env.TEMP, TMP: process.env.TMP, PATH: '' },
  });
  const pending = new Map();
  let nextID = 1;
  let buffer = '';
  let closed = false;
  function rejectAll(code) {
    for (const entry of pending.values()) { clearTimeout(entry.timer); entry.reject(new Error(code)); }
    pending.clear();
  }
  child.stdout.setEncoding('utf8');
  child.stdout.on('data', (chunk) => {
    buffer += chunk;
    if (buffer.length > 128 * 1024) { rejectAll('native_live_protocol_invalid'); child.stdin.end(); return; }
    let index;
    while ((index = buffer.indexOf('\n')) !== -1) {
      const line = buffer.slice(0, index); buffer = buffer.slice(index + 1);
      let receipt;
      try { receipt = JSON.parse(line); } catch { rejectAll('native_live_protocol_invalid'); continue; }
      const entry = pending.get(receipt.id);
      if (!entry) continue;
      pending.delete(receipt.id); clearTimeout(entry.timer);
      if (receipt.ok !== true) entry.reject(new Error(/^native_[a-z_]+$/.test(receipt.code) ? receipt.code : 'native_live_operation_failed'));
      else entry.resolve(receipt);
    }
  });
  // Drain but never expose arbitrary native diagnostics or future tokens.
  child.stderr.resume();
  child.once('error', () => rejectAll('native_live_backend_unavailable'));
  const completion = new Promise((resolve) => child.once('close', (code) => {
    closed = true; rejectAll('native_live_backend_closed'); resolve(code);
  }));
  function request(command, timeout = 5000) {
    return new Promise((resolve, reject) => {
      if (closed) { reject(new Error('native_live_backend_closed')); return; }
      const id = nextID++;
      const timer = setTimeout(() => { pending.delete(id); reject(new Error('native_live_receipt_timeout')); }, timeout);
      pending.set(id, { timer, resolve, reject });
      child.stdin.write(JSON.stringify({ id, command }) + '\n', (error) => {
        if (error) rejectAll('native_live_protocol_closed');
      });
    });
  }
  return { request, completion, async dispose() {
    if (closed) return;
    child.stdin.end();
    const timer = setTimeout(() => { if (!closed) child.kill(); }, 3000);
    await completion; clearTimeout(timer);
  } };
}

async function main() {
  if (args.length !== 2 || args[0] !== '--bundle') throw new Error('native_live_arguments_invalid');
  const bundle = path.resolve(root, args[1]);
  const manifest = path.join(bundle, 'bundle.json');
  const document = JSON.parse(fs.readFileSync(manifest, 'utf8'));
  assert.equal(document.isolated, true, 'native_live_requires_isolated_bundle');
  const fixture = path.join(root, '.tmp-v2-native', 'live-' + crypto.randomUUID());
  fs.mkdirSync(fixture, { recursive: true });
  const state = path.join(fixture, 'state');
  const template = path.join(bundle, document.dataTemplate);
  const templateDigest = () => crypto.createHash('sha256').update(fs.readFileSync(template)).digest('hex');
  const before = templateDigest();
  const port = await unusedPort();
  const control = controller(manifest, state, port);
  const evidence = { schemaVersion: 1, runtime: 'native-lite', isolated: true, sessionVerified: false, loginsAttempted: 0, mediaRequestsAttempted: 0, bootMilliseconds: [] };
  try {
    await control.request('check', 30000);
    process.stdout.write('native_fixture_check_passed\n');
    assert.equal(fs.existsSync(state), false);
    const blocker = net.createServer();
    await new Promise((resolve, reject) => { blocker.once('error', reject); blocker.listen(port, '127.0.0.1', resolve); });
    try {
      await assert.rejects(control.request('start', 30000), /^Error: native_port_in_use$/);
      assert.equal(fs.existsSync(state), false);
    } finally { await new Promise((resolve) => blocker.close(resolve)); }
    evidence.portCollisionRejected = true;
    process.stdout.write('native_fixture_port_collision_passed\n');
    for (let attempt = 0; attempt < 2; attempt++) {
      const begin = performance.now();
      const ready = await control.request('start', 90000);
      process.stdout.write('native_fixture_boot_receipt\n');
      evidence.bootMilliseconds.push(Math.round(performance.now() - begin));
      assert.equal(ready.state.runtime, 'native-lite');
      assert.equal(ready.state.phase, 'ready');
      assert.equal(ready.state.sessionVerified, false);
      assert.deepEqual(ready.state.regions, []);
      if(document.controlProtocol===1){const session=await control.request('session');assert.equal(session.session.state,'needs_login');evidence.emptySessionDetected=true;process.stdout.write('native_fixture_empty_session_passed\n');}
      assert.equal((await control.request('status')).state.phase, 'ready');
      const stopped=await control.request('stop',30000);assert.equal(stopped.state.phase, 'stopped');if(document.controlProtocol===1){assert.equal(stopped.state.gracefulStop,true);evidence.gracefulShutdownVerified=true;}
      process.stdout.write('native_fixture_stop_receipt\n');
      assert.equal(await portOpen(port), false, 'native_live_port_not_released');
      assert.equal(fs.statSync(path.join(state, 'data.img')).size, fs.statSync(template).size);
    }
    const cancellation = control.request('start', 90000).then(() => ({ unexpected: true }), (error) => ({ code: error.message }));
    await new Promise((resolve) => setTimeout(resolve, 1000));
    assert.equal((await control.request('stop', 30000)).state.phase, 'stopped');
    assert.equal((await cancellation).code, 'native_operation_cancelled');
    assert.equal(await portOpen(port), false);
    evidence.startupCancellationVerified = true;
    assert.equal(templateDigest(), before, 'native_live_template_changed');
    await control.request('shutdown');
    assert.equal(await control.completion, 0);
    const eof = controller(manifest, state, port);
    try {
      const pending = eof.request('start', 90000).then(() => ({ unexpected: true }), (error) => ({ code: error.message }));
      await new Promise((resolve) => setTimeout(resolve, 1000));
      await eof.dispose();
      assert.equal(await eof.completion, 0);
      assert.equal((await pending).code, 'native_operation_cancelled');
      assert.equal(await portOpen(port), false);
      evidence.eofCleanupVerified = true;
    } finally { await eof.dispose(); }
    assert.equal(templateDigest(), before, 'native_live_template_changed');
    evidence.passed = true;
  } catch (error) {
    evidence.passed = false;
    evidence.code = /^native_[a-z_]+$/.test(error.message) ? error.message : 'native_live_assertion_failed';
    throw error;
  } finally {
    await control.dispose();
    fs.writeFileSync(path.join(fixture, 'evidence.json'), JSON.stringify(evidence, null, 2) + '\n');
    process.stdout.write(JSON.stringify({ ...evidence, evidence: path.join(fixture, 'evidence.json') }) + '\n');
  }
}

main().catch((error) => { process.stderr.write((/^native_[a-z_]+$/.test(error.message) ? error.message : 'native_live_failed') + '\n'); process.exitCode = 1; });

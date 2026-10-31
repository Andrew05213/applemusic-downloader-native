'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { PassThrough } = require('node:stream');

const {
  LOCK_COMMAND,
  acquireLocalPocMigrationLock,
  withLocalPocMigrationLock,
} = require('../../electron/local-poc-migration-lock');

function concurrentLockSpawner() {
  let held = false;
  const calls = [];
  const children = [];
  const spawn = (command, args, options) => {
    const child = new EventEmitter();
    child.stdin = new PassThrough();
    child.stdout = new PassThrough();
    child.stderr = new PassThrough();
    child.closed = false;
    const close = (code, signal = null) => {
      if (child.closed) return;
      child.closed = true;
      queueMicrotask(() => child.emit('close', code, signal));
    };
    child.kill = () => { if (child.acquired) held = false; close(null, 'SIGTERM'); };
    child.stdin.once('finish', () => {
      if (child.acquired) held = false;
      close(child.acquired ? 0 : 3);
    });
    calls.push({ command, args: [...args], options: { ...options } });
    children.push(child);
    queueMicrotask(() => {
      if (held) {
        child.stdout.write(`${JSON.stringify({ version: 1, acquired: false, reason: 'busy' })}\n`);
        return;
      }
      held = true;
      child.acquired = true;
      child.stdout.write(`${JSON.stringify({ version: 1, acquired: true })}\n`);
    });
    return child;
  };
  return { spawn, calls, children, isHeld: () => held };
}

function lockManager(spawn) {
  return {
    windowsProbeExe: 'C:\\Program Files\\Apple Music Downloader\\resources\\bin\\amdl-windows-probe.exe',
    processEnvironment: { PATH: 'C:\\Windows\\System32' },
    runner: { spawn },
  };
}

test('migration lock uses one fixed helper argv and rejects a concurrent process until release', async () => {
  const harness = concurrentLockSpawner();
  const manager = lockManager(harness.spawn);
  const first = await acquireLocalPocMigrationLock(manager);
  assert.equal(harness.isHeld(), true);

  await assert.rejects(
    acquireLocalPocMigrationLock(manager),
    (error) => error.code === 'local_poc_migration_locked' && error.step === 'migration_lock_busy',
  );
  assert.equal(harness.isHeld(), true);
  assert.equal(harness.calls.length, 2);
  for (const call of harness.calls) {
    assert.equal(call.command, manager.windowsProbeExe);
    assert.deepEqual(call.args, [LOCK_COMMAND]);
    assert.equal(call.options.shell, false);
    assert.deepEqual(call.options.stdio, ['pipe', 'pipe', 'pipe']);
    assert.equal(JSON.stringify(call.args).includes('测试'), false);
  }

  await first.release();
  assert.equal(harness.isHeld(), false);
  const afterRelease = await acquireLocalPocMigrationLock(manager);
  assert.equal(harness.isHeld(), true);
  await afterRelease.release();
  assert.equal(harness.isHeld(), false);
});

test('migration lock remains held for the complete callback and fails closed if helper exits', async () => {
  const harness = concurrentLockSpawner();
  const manager = lockManager(harness.spawn);
  let observedHeld = false;
  await withLocalPocMigrationLock(manager, async (lock) => {
    lock.assertHeld();
    observedHeld = harness.isHeld();
  });
  assert.equal(observedHeld, true);
  assert.equal(harness.isHeld(), false);

  const lost = await acquireLocalPocMigrationLock(manager);
  harness.children.at(-1).emit('close', 9, null);
  assert.throws(
    () => lost.assertHeld(),
    (error) => error.code === 'local_poc_migration_lock_failed' && error.step === 'migration_lock_lost',
  );
});

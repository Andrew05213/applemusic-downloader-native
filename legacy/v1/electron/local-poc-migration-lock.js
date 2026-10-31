'use strict';

const { WslSmokeError } = require('./wsl-smoke');

const LOCK_COMMAND = 'hold-local-poc-migration-lock';
const LOCK_PROTOCOL_VERSION = 1;
const MAX_LOCK_OUTPUT_BYTES = 4096;
const heldLocks = new WeakSet();

function lockError(code, step) {
  return new WslSmokeError(code, step);
}

function parseReadyDocument(line) {
  let document;
  try {
    document = JSON.parse(line);
  } catch {
    throw lockError('local_poc_migration_lock_failed', 'migration_lock_protocol');
  }
  const keys = Object.keys(document || {}).sort();
  const expectedKeys = document?.acquired === true
    ? ['acquired', 'version']
    : ['acquired', 'reason', 'version'];
  if (document?.version !== LOCK_PROTOCOL_VERSION
      || typeof document.acquired !== 'boolean'
      || JSON.stringify(keys) !== JSON.stringify(expectedKeys.sort())
      || document.acquired === false && document.reason !== 'busy') {
    throw lockError('local_poc_migration_lock_failed', 'migration_lock_protocol');
  }
  return document;
}

function createCloseState(child) {
  const state = {
    closed: false,
    exitCode: null,
    signal: null,
    spawnError: null,
  };
  state.promise = new Promise((resolve) => {
    const finish = () => {
      if (state.closed) return;
      state.closed = true;
      resolve(state);
    };
    child.once('error', (error) => {
      state.spawnError = error;
      finish();
    });
    child.once('close', (exitCode, signal) => {
      state.exitCode = exitCode;
      state.signal = signal;
      finish();
    });
  });
  return state;
}

class LocalPocMigrationLock {
  constructor(child, closeState) {
    this.child = child;
    this.closeState = closeState;
    this.releasing = false;
    this.released = false;
    heldLocks.add(this);
  }

  assertHeld() {
    if (!heldLocks.has(this) || this.releasing || this.released || this.closeState.closed) {
      throw lockError('local_poc_migration_lock_failed', 'migration_lock_lost');
    }
  }

  async release({ timeoutMs = 15_000 } = {}) {
    if (this.released) return;
    this.assertHeld();
    this.releasing = true;
    this.child.stdin.end();
    let timer;
    const timeout = new Promise((_, reject) => {
      timer = setTimeout(() => reject(lockError('local_poc_migration_lock_failed', 'migration_lock_release')), timeoutMs);
    });
    try {
      const closed = await Promise.race([this.closeState.promise, timeout]);
      if (closed.spawnError || closed.exitCode !== 0 || closed.signal) {
        throw lockError('local_poc_migration_lock_failed', 'migration_lock_release');
      }
      this.released = true;
      heldLocks.delete(this);
    } catch (error) {
      if (typeof this.child.kill === 'function') this.child.kill();
      throw error instanceof WslSmokeError
        ? error
        : lockError('local_poc_migration_lock_failed', 'migration_lock_release');
    } finally {
      if (timer) clearTimeout(timer);
    }
  }
}

async function acquireLocalPocMigrationLock(manager, { timeoutMs = 15_000 } = {}) {
  if (!manager || typeof manager.windowsProbeExe !== 'string'
      || typeof manager.runner?.spawn !== 'function') {
    throw lockError('local_poc_migration_lock_failed', 'migration_lock_spawn');
  }
  let child;
  try {
    child = manager.runner.spawn(manager.windowsProbeExe, [LOCK_COMMAND], {
      windowsHide: true,
      shell: false,
      stdio: ['pipe', 'pipe', 'pipe'],
      env: manager.processEnvironment,
    });
  } catch {
    throw lockError('local_poc_migration_lock_failed', 'migration_lock_spawn');
  }
  if (!child?.stdin || !child?.stdout || !child?.stderr || typeof child.once !== 'function') {
    if (typeof child?.kill === 'function') child.kill();
    throw lockError('local_poc_migration_lock_failed', 'migration_lock_spawn');
  }

  const closeState = createCloseState(child);
  return new Promise((resolve, reject) => {
    let settled = false;
    let stdout = Buffer.alloc(0);
    let stderrBytes = 0;
    let timer;
    const finish = (error, lock) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      child.stdout.removeListener('data', onStdout);
      child.stderr.removeListener('data', onStderr);
      if (error) {
        child.stdin.end();
        reject(error);
      } else {
        resolve(lock);
      }
    };
    const abort = (step) => {
      if (typeof child.kill === 'function') child.kill();
      finish(lockError('local_poc_migration_lock_failed', step));
    };
    const onStderr = (chunk) => {
      stderrBytes += Buffer.byteLength(chunk);
      if (stderrBytes > MAX_LOCK_OUTPUT_BYTES) abort('migration_lock_protocol');
    };
    const onStdout = (chunk) => {
      stdout = Buffer.concat([stdout, Buffer.from(chunk)]);
      if (stdout.length > MAX_LOCK_OUTPUT_BYTES) {
        abort('migration_lock_protocol');
        return;
      }
      const newline = stdout.indexOf(0x0a);
      if (newline < 0) return;
      const trailing = stdout.subarray(newline + 1).toString('utf8').trim();
      if (trailing !== '') {
        abort('migration_lock_protocol');
        return;
      }
      let ready;
      try {
        ready = parseReadyDocument(stdout.subarray(0, newline).toString('utf8'));
      } catch (error) {
        abort(error.step || 'migration_lock_protocol');
        return;
      }
      stdout.fill(0);
      if (!ready.acquired) {
        finish(lockError('local_poc_migration_locked', 'migration_lock_busy'));
        return;
      }
      if (closeState.closed) {
        finish(lockError('local_poc_migration_lock_failed', 'migration_lock_lost'));
        return;
      }
      finish(null, new LocalPocMigrationLock(child, closeState));
    };
    child.stdout.on('data', onStdout);
    child.stderr.on('data', onStderr);
    closeState.promise.then(() => {
      if (!settled) finish(lockError('local_poc_migration_lock_failed', 'migration_lock_spawn'));
    });
    timer = setTimeout(() => abort('migration_lock_timeout'), timeoutMs);
  });
}

function assertLocalPocMigrationLock(lock) {
  if (!heldLocks.has(lock)) {
    throw lockError('local_poc_migration_lock_failed', 'migration_lock_proof');
  }
  lock.assertHeld();
  return lock;
}

async function withLocalPocMigrationLock(manager, operation, {
  acquire = acquireLocalPocMigrationLock,
} = {}) {
  if (typeof operation !== 'function') throw new TypeError('migration lock operation must be a function');
  const lock = await acquire(manager);
  let result;
  let operationError = null;
  try {
    assertLocalPocMigrationLock(lock);
    result = await operation(lock);
    assertLocalPocMigrationLock(lock);
  } catch (error) {
    operationError = error;
  }
  try {
    await lock.release();
  } catch (releaseError) {
    throw releaseError;
  }
  if (operationError) throw operationError;
  return result;
}

module.exports = {
  LOCK_COMMAND,
  LocalPocMigrationLock,
  acquireLocalPocMigrationLock,
  assertLocalPocMigrationLock,
  parseReadyDocument,
  withLocalPocMigrationLock,
};

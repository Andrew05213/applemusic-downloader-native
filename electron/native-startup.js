'use strict';

// Startup presentation only: it never starts authentication or changes a session.
class NativeStartupCoordinator {
  constructor({ onFinish, onChange = () => {}, timeout = 90000, schedule = setTimeout, unschedule = clearTimeout }) {
    this.onFinish = onFinish;
    this.onChange = onChange;
    this.unschedule = unschedule;
    this.phase = 'loading';
    this.finished = false;
    this.disposed = false;
    this.timer = schedule(() => this.fail(), timeout);
  }
  snapshot() { return { phase: this.phase, ...(this.phase === 'failed' && this.reason ? {reason:this.reason} : {}) }; }
  clearTimer() { if (this.timer !== null) this.unschedule(this.timer); this.timer = null; }
  observe(session) {
    if (this.disposed || this.finished) return;
    if (['authenticated', 'needs_login', 'needs_2fa'].includes(session?.state)) this.finish();
    else if (['unverified', 'runtime_unavailable'].includes(session?.state)) {
      const reason = session.state === 'runtime_unavailable' && session.code
        ? (session.code === 'native_service_version_mismatch' ? 'version_mismatch' : 'local_service') : undefined;
      this.fail(reason);
    }
  }
  fail(reason) {
    if (this.disposed || this.finished || (this.phase === 'failed' && (!reason || reason === this.reason))) return;
    this.clearTimer();
    this.reason = reason;
    this.phase = 'failed';
    this.onChange(this.snapshot());
  }
  finish() {
    if (this.disposed || this.finished) return;
    this.clearTimer();
    this.finished = true;
    this.phase = 'complete';
    this.onFinish();
  }
  dispose() { this.disposed = true; this.clearTimer(); }
}

// Local, bounded breadcrumbs. Never store argv, URLs, exception messages or stacks.
function startupTrace(directory) {
  const fs = require('node:fs'), path = require('node:path');
  const file = path.join(directory, 'desktop-startup.log');
  const allowed = new Set(['entry','ready','single_instance','other_instance','cache','windows','load_pages','splash_visible','main_visible']);
  let stage = 'entry';
  function write(outcome) {
    try {
      fs.mkdirSync(directory, { recursive: true });
      if (fs.existsSync(file) && fs.statSync(file).size >= 16384) fs.renameSync(file, file + '.previous');
      fs.appendFileSync(file, JSON.stringify({at:new Date().toISOString(),pid:process.pid,stage,outcome})+'\n', {mode:0o600});
    } catch { /* Diagnostics must never prevent launch. */ }
  }
  write('begin');
  return {
    mark(next) { if (allowed.has(next)) { stage = next; write('reached'); } },
    fail() { write('failed'); return stage; },
  };
}
module.exports = { NativeStartupCoordinator, startupTrace };

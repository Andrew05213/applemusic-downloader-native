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

module.exports = { NativeStartupCoordinator };

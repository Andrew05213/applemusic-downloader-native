'use strict';
const {CODES,STAGES,sanitizeDiagnostics}=require('./native-diagnostics');

const STATES = new Set(['checking', 'needs_login', 'authenticating', 'needs_2fa', 'needs_interaction', 'authenticated', 'unverified', 'runtime_unavailable']);
function safeSession(input) {
  const legacyFactor=input?.factor!=null;
  const state = legacyFactor?'needs_login':STATES.has(input?.state) ? input.state : 'runtime_unavailable';
  const region = state === 'authenticated' && /^[a-z]{2}$/.test(input?.region) ? input.region : undefined;
  if (state === 'authenticated' && !region) return Object.freeze({ state: 'unverified' });
	return Object.freeze({ state, ...(region ? { region } : {}), ...(legacyFactor?{code:'native_login_failed'}:CODES.has(input?.code) ? { code: input.code } : {}),...(STAGES.has(input?.stage)?{stage:input.stage}:{}),...(input?.diagnostics?{diagnostics:sanitizeDiagnostics(input.diagnostics,128)}:{}) });
}
class NativeLoginCoordinator {
  constructor({ createWindow, onDismiss = () => {} }) {
    this.createWindow = createWindow; this.onDismiss = onDismiss;
    this.window = null; this.state = { state: 'checking' }; this.dismissed = false; this.disposed = false;
  }
  deliver() {
    if (!this.window || this.window.isDestroyed()) return;
    try { this.window.webContents.send('native:session-state', this.state); } catch { /* A closing window has no work left. */ }
  }
  observe(input) {
    if (this.disposed) return;
    this.state = safeSession(input);
    if (this.state.state === 'authenticated') {
      this.dismissed = false;
      const win = this.window; this.window = null;
      if (win && !win.isDestroyed()) win.close();
      return;
    }
    if (this.state.state === 'needs_login' && !this.dismissed) this.open();
    this.deliver();
  }
  open() {
    if (this.disposed) return;
    if (!this.window || this.window.isDestroyed()) {
      this.dismissed = false;
      const win = this.createWindow(); this.window = win;
      win.once('closed', () => {
        if (this.window !== win) return;
        this.window = null; this.dismissed = true;
        Promise.resolve().then(() => this.onDismiss()).catch(() => {});
      });
    }
    if (!this.window.isDestroyed()) { this.window.show(); this.window.focus(); this.deliver(); }
  }
  dispose() {
    this.disposed = true; const win = this.window; this.window = null;
    if (win && !win.isDestroyed()) win.close();
  }
}
module.exports = { NativeLoginCoordinator, safeSession };

'use strict';
const { EventEmitter } = require('node:events');
const { spawn } = require('node:child_process');
const { VERSION } = require('./native-version');

const COMMANDS = new Set(['check', 'status', 'start', 'stop', 'shutdown', 'session', 'login', 'two-factor', 'recheck-session', 'cancel-login']);
class NativeBackendClient extends EventEmitter {
  constructor({ executable, manifest, stateDirectory, port, spawnProcess = spawn }) {
    super(); this.nextID = 1; this.pending = new Map(); this.closed = false; this.buffer = '';
    this.child = spawnProcess(executable, ['--manifest', manifest, '--state-dir', stateDirectory, '--port', String(port)], {
      shell: false, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
      env: { SystemRoot: process.env.SystemRoot, WINDIR: process.env.WINDIR, TEMP: process.env.TEMP, TMP: process.env.TMP, PATH: '' },
    });
    this.child.stdout.setEncoding('utf8');
    this.child.stdout.on('data', (chunk) => this.receive(chunk));
    this.child.stderr.resume(); // Never forward raw process logs or account data.
    this.child.stdin.on('error', () => this.rejectPending('native_backend_closed'));
    this.child.once('error', () => this.rejectPending('native_backend_unavailable'));
    this.completion = new Promise((resolve) => this.child.once('close', (code) => {
      this.closed = true; this.rejectPending('native_backend_closed'); this.emit('closed'); resolve(code);
    }));
  }
  rejectPending(code) {
    for (const entry of this.pending.values()) { clearTimeout(entry.timer); entry.reject(new Error(code)); }
    this.pending.clear();
  }
  receive(chunk) {
    this.buffer += chunk;
    if (this.buffer.length > 1024 * 1024) { this.rejectPending('native_protocol_invalid'); this.child.stdin.end(); return; }
    let newline;
    while ((newline = this.buffer.indexOf('\n')) !== -1) {
      const line = this.buffer.slice(0, newline); this.buffer = this.buffer.slice(newline + 1);
      let value;
      try { value = JSON.parse(line); } catch { this.rejectPending('native_protocol_invalid'); this.child.stdin.end(); return; }
      const entry = this.pending.get(value.id);
      if (!entry) continue;
      this.pending.delete(value.id); clearTimeout(entry.timer);
      if (value.ok === true && value.version === VERSION && value.state?.runtime === 'native-lite') entry.resolve(value);
      else entry.reject(new Error(/^native_[a-z_]+$/.test(value.code) ? value.code : 'native_operation_failed'));
    }
  }
  request(command, input = {}, timeout = command === 'start' ? 90000 : command === 'stop' || command === 'shutdown' ? 30000 : 45000) {
    if (!COMMANDS.has(command) || this.closed) return Promise.reject(new Error('native_command_unavailable'));
    const frame = { id: this.nextID++, command };
    if (command === 'login') frame.credentials = { appleEmail: input.appleEmail, applePassword: input.applePassword };
    if (command === 'two-factor') frame.code = input.code;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(frame.id); reject(new Error('native_receipt_timeout')); }, timeout);
      this.pending.set(frame.id, { timer, resolve, reject });
      this.child.stdin.write(JSON.stringify(frame) + '\n', (error) => { if (error) this.rejectPending('native_backend_closed'); });
      if (frame.credentials) { frame.credentials.appleEmail = ''; frame.credentials.applePassword = ''; }
      if (frame.code) frame.code = '';
    });
  }
  async close() {
    if (this.closed) return;
    try { await this.request('shutdown'); } catch { /* EOF invokes owned cleanup even if receipts fail. */ }
    this.child.stdin.end();
    await this.completion;
  }
}
module.exports = { NativeBackendClient };

'use strict';

const net = require('node:net');
const fs = require('node:fs');
const path = require('node:path');
const { spawn: nodeSpawn } = require('node:child_process');
const { Transform } = require('node:stream');
const {
  validateWindowsHelperBinding,
  verifyWindowsHelperBinding: verifyBoundWindowsHelper,
} = require('./wslinterop-recovery');

const LOOPBACK_HOST = '127.0.0.1';
const CONTROL_PATH = '/usr/local/bin/amdl-appliance-control';
const DEFAULT_MAX_CONNECTIONS = 64;
const DEFAULT_MAX_HEADER_BYTES = 32 * 1024;
const DEFAULT_HANDSHAKE_TIMEOUT_MS = 5_000;
const DEFAULT_RESPONSE_HEADER_TIMEOUT_MS = 15_000;
const DEFAULT_CHILD_EXIT_TIMEOUT_MS = 5_000;
const DEFAULT_IDLE_TIMEOUT_MS = 10 * 60_000;
const SENSITIVE_ENVIRONMENT_KEY = /^(?:APPLE_(?:EMAIL|PASSWORD)|MEDIA_USER_TOKEN|AUTHORIZATION_TOKEN|AMDL_.*(?:TOKEN|PASSWORD|SECRET))$/i;
const HTTP_TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
// Only these immutable desktop files may shadow the backend. API, SSE and
// downloaded media keep using the existing authenticated WSL transport.
const DESKTOP_RENDERER_ASSETS = new Map([
  ['/', ['index.html', 'text/html']],
  ['/index.html', ['index.html', 'text/html']],
  ['/renderer.js', ['renderer.js', 'application/javascript']],
  ['/job-data.js', ['job-data.js', 'application/javascript']],
  ['/browse.js', ['browse.js', 'application/javascript']],
  ['/catalog.js', ['catalog.js', 'application/javascript']],
  ['/playback.js', ['playback.js', 'application/javascript']],
  ['/client-navigation.js', ['client-navigation.js', 'application/javascript']],
  ['/styles.css', ['styles.css', 'text/css']],
  ['/client.css', ['client.css', 'text/css']],
]);
const MAX_DESKTOP_ASSET_BYTES = 4 * 1024 * 1024;
const CONTROL_PROXY_STAGES = new Set([
  'connect_socket',
  'request_frame',
  'read_frame',
  'decode_frame',
  'backend_unavailable',
  'backend_not_running',
  'proxy_busy',
  'upload_stream',
  'download_stream',
  'cancelled',
]);
const CONTROL_PROXY_STAGE_PATTERNS = [...CONTROL_PROXY_STAGES].flatMap((stage) => [
  { stage, pattern: Buffer.from(`appliance proxy failed: ${stage}\n`, 'ascii') },
  { stage, pattern: Buffer.from(`appliance proxy failed: ${stage}\r\n`, 'ascii') },
]);

function sanitizedEnvironment(environment = process.env) {
  return Object.fromEntries(Object.entries(environment || {}).filter(([key]) => !SENSITIVE_ENVIRONMENT_KEY.test(key)));
}

function headerBoundary(buffer) {
  const crlf = buffer.indexOf('\r\n\r\n');
  if (crlf >= 0) return { index: crlf, length: 4 };
  const lf = buffer.indexOf('\n\n');
  return lf >= 0 ? { index: lf, length: 2 } : null;
}

function validHttpResponseHead(buffer, boundary) {
  const text = buffer.subarray(0, boundary.index).toString('latin1');
  const lines = text.split(/\r?\n/);
  const statusLine = lines.shift() || '';
  if (!/^HTTP\/1\.[01] [1-5]\d\d(?:[ \t].*)?$/.test(statusLine)
      || /[\u0000-\u0008\u000a-\u001f\u007f]/.test(statusLine)) return false;
  for (const line of lines) {
    if (!line || /^[ \t]/.test(line)) return false;
    const separator = line.indexOf(':');
    const name = separator > 0 ? line.slice(0, separator) : '';
    const value = separator > 0 ? line.slice(separator + 1) : '';
    if (!HTTP_TOKEN.test(name) || /[\u0000-\u0008\u000a-\u001f\u007f]/.test(value)) return false;
  }
  return true;
}

function loopbackHostHeader(value) {
  if (typeof value !== 'string' || !value || /[\s\\/@]/.test(value)) return false;
  try {
    const parsed = new URL(`http://${value}`);
    return (parsed.hostname === LOOPBACK_HOST || parsed.hostname === 'localhost')
      && parsed.username === ''
      && parsed.password === ''
      && parsed.pathname === '/';
  } catch {
    return false;
  }
}

function parseHttpHandshake(buffer, maxHeaderBytes = DEFAULT_MAX_HEADER_BYTES) {
  const boundary = headerBoundary(buffer);
  if (!boundary) return null;
  const headerBytes = boundary.index + boundary.length;
  if (headerBytes > maxHeaderBytes) throw Object.assign(new Error('HTTP request headers are too large'), { statusCode: 431 });

  const text = buffer.subarray(0, boundary.index).toString('latin1');
  const lines = text.split(/\r?\n/);
  const requestLine = lines.shift() || '';
  const match = /^([A-Z]{1,16}) ([^ ]+) HTTP\/(1\.[01])$/.exec(requestLine);
  if (!match || (match[2] !== '*' && !match[2].startsWith('/'))) {
    throw Object.assign(new Error('A valid origin-form HTTP request is required'), { statusCode: 400 });
  }
  if (match[1] === 'CONNECT') {
    throw Object.assign(new Error('HTTP tunnelling is not supported'), { statusCode: 405 });
  }

  const headers = new Map();
  const rawHeaders = [];
  for (const line of lines) {
    if (!line || /^[ \t]/.test(line)) throw Object.assign(new Error('Malformed HTTP request header'), { statusCode: 400 });
    const separator = line.indexOf(':');
    const name = separator > 0 ? line.slice(0, separator) : '';
    const value = separator > 0 ? line.slice(separator + 1).trim() : '';
    if (!HTTP_TOKEN.test(name) || /[\u0000-\u0008\u000a-\u001f\u007f]/.test(value)) {
      throw Object.assign(new Error('Malformed HTTP request header'), { statusCode: 400 });
    }
    const lowerName = name.toLowerCase();
    const values = headers.get(lowerName) || [];
    values.push(value);
    headers.set(lowerName, values);
    rawHeaders.push({ name, value });
  }

  const hosts = headers.get('host') || [];
  if (hosts.length !== 1 || !loopbackHostHeader(hosts[0])) {
    throw Object.assign(new Error('The HTTP Host header must identify loopback'), { statusCode: 400 });
  }
  const contentLengths = headers.get('content-length') || [];
  if (contentLengths.length > 1 && new Set(contentLengths).size !== 1) {
    throw Object.assign(new Error('Conflicting Content-Length headers are not allowed'), { statusCode: 400 });
  }
  if (contentLengths.length > 0 && headers.has('transfer-encoding')) {
    throw Object.assign(new Error('Ambiguous HTTP request framing is not allowed'), { statusCode: 400 });
  }

  return {
    method: match[1],
    target: match[2],
    httpVersion: match[3],
    headers,
    rawHeaders,
    requestLine,
    boundary,
  };
}

function normalizeInjectedHeaders(value) {
  if (value === undefined || value === null) return [];
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new TypeError('Injected HTTP headers must be an object');
  const entries = Object.entries(value);
  if (entries.length > 32) throw new RangeError('Too many injected HTTP headers');
  return entries.map(([name, rawValue]) => {
    const headerValue = String(rawValue);
    if (!HTTP_TOKEN.test(name) || /[\r\n\u0000]/.test(headerValue)) throw new TypeError('Injected HTTP header is invalid');
    return { name, value: headerValue };
  });
}

function injectHeaders(buffer, handshake, additions, maxHeaderBytes = DEFAULT_MAX_HEADER_BYTES) {
  if (additions.length === 0) return buffer;
  const replaced = new Set(additions.map(({ name }) => name.toLowerCase()));
  const lines = [handshake.requestLine];
  for (const header of handshake.rawHeaders) {
    if (!replaced.has(header.name.toLowerCase())) lines.push(`${header.name}: ${header.value}`);
  }
  for (const header of additions) lines.push(`${header.name}: ${header.value}`);
  const encoded = Buffer.from(`${lines.join('\r\n')}\r\n\r\n`, 'latin1');
  if (encoded.length > maxHeaderBytes) throw Object.assign(new Error('HTTP request headers are too large'), { statusCode: 431 });
  return Buffer.concat([encoded, buffer.subarray(handshake.boundary.index + handshake.boundary.length)]);
}

function httpErrorResponse(statusCode) {
  const status = ({ 400: 'Bad Request', 403: 'Forbidden', 405: 'Method Not Allowed', 408: 'Request Timeout', 431: 'Request Header Fields Too Large', 502: 'Bad Gateway', 503: 'Service Unavailable', 504: 'Gateway Timeout' })[statusCode] || 'Bad Request';
  const body = `${status}\n`;
  return Buffer.from(`HTTP/1.1 ${statusCode} ${status}\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: ${Buffer.byteLength(body)}\r\nConnection: close\r\n\r\n${body}`, 'utf8');
}

class WslLoopbackProxy {
  constructor(options = {}) {
    this.windowsProbeExe = options.windowsProbeExe || 'amdl-windows-probe.exe';
    if (options.windowsHelperBindingProvider !== undefined && typeof options.windowsHelperBindingProvider !== 'function') {
      throw new TypeError('windowsHelperBindingProvider must be a function');
    }
    this.windowsHelperBindingProvider = options.windowsHelperBindingProvider || null;
    this.spawn = options.spawn || nodeSpawn;
    this.processEnvironment = sanitizedEnvironment(options.processEnvironment);
    this.createServer = options.createServer || net.createServer;
    // Both hooks run before proxy-stdio is spawned. Header injection forces
    // Connection: close so a future per-request proxy token cannot be bypassed
    // by sending a second keep-alive request on an already authorized bridge.
    this.authorizeRequest = options.authorizeRequest || (() => true);
    this.injectRequestHeaders = options.injectRequestHeaders || (() => ({}));
    this.rendererDirectory = options.rendererDirectory || null;
    this.maxConnections = options.maxConnections || DEFAULT_MAX_CONNECTIONS;
    this.maxHeaderBytes = options.maxHeaderBytes || DEFAULT_MAX_HEADER_BYTES;
    this.handshakeTimeoutMs = options.handshakeTimeoutMs || DEFAULT_HANDSHAKE_TIMEOUT_MS;
    this.responseHeaderTimeoutMs = options.responseHeaderTimeoutMs || DEFAULT_RESPONSE_HEADER_TIMEOUT_MS;
    this.childExitTimeoutMs = options.childExitTimeoutMs || DEFAULT_CHILD_EXIT_TIMEOUT_MS;
    this.idleTimeoutMs = options.idleTimeoutMs || DEFAULT_IDLE_TIMEOUT_MS;
    this.setTimer = options.setTimeout || setTimeout;
    this.clearTimer = options.clearTimeout || clearTimeout;
    this.server = null;
    this.children = new Set();
    this.connections = new Set();
    this.stderr = [];
    this.port = null;
    this.startPromise = null;
    this.stopPromise = null;
    // Admission is explicitly quiesced before any listener/helper teardown.
    // Keep the state visible for lifecycle owners and tests; `state` is a
    // compatibility alias for callers that inspect the proxy instance.
    this.lifecycleState = 'stopped';
    this.state = 'stopped';
    this.admissionOpen = false;
    this.closingServer = null;
    this.closingServerPromise = null;
    this.nextCorrelationId = 1;
  }

  setLifecycleState(value) {
    this.lifecycleState = value;
    this.state = value;
  }

  isAccepting() {
    return this.lifecycleState === 'accepting' && this.admissionOpen;
  }

  serveDesktopRenderer(state, handshake, buffered) {
    if (!this.rendererDirectory) return false;
    // Do not decode or normalize a user-supplied filesystem path. The exact
    // allowlist is the only mapping; query strings are cache keys, not files.
    const asset = DESKTOP_RENDERER_ASSETS.get(handshake.target.split('?')[0]);
    if (!asset) return false;
    if (handshake.method !== 'GET' && handshake.method !== 'HEAD') {
      this.rejectConnection(state, 405, 'Rejected a desktop asset method');
      return true;
    }
    const lengths = handshake.headers.get('content-length') || [];
    if (handshake.headers.has('transfer-encoding') || lengths.some((value) => value !== '0')
        || buffered.length !== handshake.boundary.index + handshake.boundary.length) {
      this.rejectConnection(state, 400, 'Rejected desktop asset request framing');
      return true;
    }
    let body;
    try {
      const filename = path.join(this.rendererDirectory, asset[0]);
      const info = fs.statSync(filename);
      if (!info.isFile() || info.size > MAX_DESKTOP_ASSET_BYTES) throw new Error('invalid asset');
      body = fs.readFileSync(filename);
      if (body.length > MAX_DESKTOP_ASSET_BYTES) throw new Error('invalid asset');
    } catch {
      // Never fall back to WSL's possibly older UI, including same-version
      // installations. Missing bundled files are a desktop installation error.
      this.rejectConnection(state, 503, 'Packaged desktop renderer unavailable');
      return true;
    }
    const head = Buffer.from(`HTTP/1.1 200 OK\r\nContent-Type: ${asset[1]}; charset=utf-8\r\nContent-Length: ${body.length}\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\nConnection: close\r\n\r\n`, 'ascii');
    const response = handshake.method === 'HEAD' ? head : Buffer.concat([head, body]);
    const socket = state.socket;
    this.finishConnection(state);
    try { socket.end(response, () => socket.destroy?.()); } catch { socket.destroy?.(); }
    return true;
  }

  commandArguments() {
    return ['proxy-wsl-stdio'];
  }

  async resolveWindowsHelperBinding() {
    if (!this.windowsHelperBindingProvider) throw new Error('Windows helper binding is unavailable');
    return validateWindowsHelperBinding(await this.windowsHelperBindingProvider());
  }

  verifyWindowsHelperBinding(binding) {
    const expected = validateWindowsHelperBinding(binding);
    verifyBoundWindowsHelper(this.windowsProbeExe, expected);
    return expected;
  }

  recordDiagnostic(message, secrets = []) {
    let output = String(message || '');
    for (const secret of secrets.filter(Boolean)) output = output.split(String(secret)).join('[REDACTED]');
    const lines = output.split(/\r?\n/).filter(Boolean);
    this.stderr = [...this.stderr, ...lines].slice(-200);
  }

  finishConnection(state, { killChild = true } = {}) {
    if (state.closed) return;
    state.closed = true;
    if (state.handshakeTimer !== null) this.clearTimer(state.handshakeTimer);
    state.handshakeTimer = null;
    if (state.responseHeaderTimer !== null) this.clearTimer(state.responseHeaderTimer);
    state.responseHeaderTimer = null;
    if (state.postResponseCloseTimer !== null) this.clearTimer(state.postResponseCloseTimer);
    state.postResponseCloseTimer = null;
    if (state.onData) state.socket.removeListener?.('data', state.onData);
    if (state.onForwardedRequestData) state.socket.removeListener?.('data', state.onForwardedRequestData);
    if (state.onPrematureEnd) state.socket.removeListener?.('end', state.onPrematureEnd);
    this.connections.delete(state);
    const child = state.child;
    state.child = null;
    const responseGate = state.responseGate;
    state.responseGate = null;
    if (child) {
      state.socket.unpipe?.(child.stdin);
      child.stdout?.unpipe?.(responseGate);
      if (state.onBackendStderr) child.stderr?.removeListener?.('data', state.onBackendStderr);
      if (killChild && !state.childClosed && typeof child.kill === 'function' && !child.killed) child.kill();
    }
    if (responseGate) {
      responseGate.unpipe?.(state.socket);
      if (state.onResponseGateError) responseGate.removeListener?.('error', state.onResponseGateError);
      if (state.onResponseGateEnd) responseGate.removeListener?.('end', state.onResponseGateEnd);
      responseGate.destroy?.();
    }
  }

  rejectConnection(state, statusCode, diagnostic, { killChild = true } = {}) {
    if (state.closed) return;
    if (diagnostic) this.recordDiagnostic(diagnostic);
    const socket = state.socket;
    this.finishConnection(state, { killChild });
    try {
      socket.end(httpErrorResponse(statusCode), () => socket.destroy?.());
    } catch {
      socket.destroy?.();
    }
  }

  rejectBeforeResponseHeaders(state, statusCode, stage, { killChild = true } = {}) {
    if (state.closed || state.responseHeadersReceived) return;
    this.rejectConnection(state, statusCode, this.connectionDiagnostic(state, stage, statusCode), { killChild });
  }

  connectionDiagnostic(state, stage, statusCode, details = {}) {
    const integer = (value) => Number.isSafeInteger(value) && value >= 0 ? String(value) : 'unknown';
    const signal = typeof details.signal === 'string' && /^SIG[A-Z0-9]+$/.test(details.signal)
      ? details.signal
      : 'none';
    // A non-empty stage is evidence from the backend stream.  If it is not
    // in the cross-language allowlist, fail closed as backend_unavailable
    // instead of silently erasing the diagnostic and making the failure look
    // like a local transport event.  No stage is distinct from an unknown
    // stage and remains `none` for connections that produced no marker.
    const controlStage = state.controlProxyStage === null || state.controlProxyStage === undefined
      ? 'none'
      : (CONTROL_PROXY_STAGES.has(state.controlProxyStage)
        ? state.controlProxyStage
        : 'backend_unavailable');
    return `[proxy] id=${state.correlationId} stage=${stage} helper_pid=${integer(state.helperPid)} exit_code=${integer(details.exitCode)} signal=${signal} response_headers=${state.responseHeadersReceived} request_bytes=${state.requestBytes} response_bytes=${state.responseBytes} stderr_bytes=${state.stderrBytes} status=${statusCode} control_stage=${controlStage}`;
  }

  observeBackendStderr(state, chunk) {
    const value = Buffer.from(chunk);
    state.stderrBytes += value.length;
    for (const byte of value) {
      for (const matcher of state.stderrMatchers) {
        if (byte === matcher.pattern[matcher.offset]) matcher.offset += 1;
        else matcher.offset = byte === matcher.pattern[0] ? 1 : 0;
        if (matcher.offset === matcher.pattern.length) {
          state.controlProxyStage = matcher.stage;
          matcher.offset = 0;
        }
      }
    }
  }

  createResponseGate(state) {
    const gate = new Transform({
      transform: (chunk, _encoding, callback) => {
        if (state.closed) {
          callback();
          return;
        }
        const value = Buffer.from(chunk);
        state.responseBytes += value.length;
        if (state.responseHeadersReceived) {
          callback(null, value);
          return;
        }
        const buffered = Buffer.concat([state.responseHeaderBytes, value]);
        const boundary = headerBoundary(buffered);
        if (!boundary) {
          if (buffered.length > this.maxHeaderBytes) {
            callback(new Error('response_headers_invalid'));
            return;
          }
          state.responseHeaderBytes = buffered;
          callback();
          return;
        }
        if (boundary.index + boundary.length > this.maxHeaderBytes || !validHttpResponseHead(buffered, boundary)) {
          callback(new Error('response_headers_invalid'));
          return;
        }
        state.responseHeadersReceived = true;
        state.responseHeaderBytes = Buffer.alloc(0);
        if (state.responseHeaderTimer !== null) this.clearTimer(state.responseHeaderTimer);
        state.responseHeaderTimer = null;
        callback(null, buffered);
      },
    });
    state.onResponseGateError = () => {
      if (state.responseHeadersReceived) {
        this.finishConnection(state);
        state.socket.destroy?.();
      } else {
        this.rejectBeforeResponseHeaders(state, 502, 'response_headers_invalid');
      }
    };
    state.onResponseGateEnd = () => {
      state.responseStreamEnded = true;
      this.completeCleanResponseIfReady(state);
    };
    gate.once('error', state.onResponseGateError);
    gate.once('end', state.onResponseGateEnd);
    return gate;
  }

  completeCleanResponseIfReady(state) {
    if (state.closed || !state.responseHeadersReceived || !state.responseStreamEnded || !state.childCloseClean) return;
    if (state.postResponseTeardown) {
      this.recordDiagnostic(this.connectionDiagnostic(state, 'post_response_teardown', 0, state.postResponseTeardownDetails));
    }
    const socket = state.socket;
    this.finishConnection(state, { killChild: false });
    socket.end?.();
  }

  deferPostResponseTeardown(state, details = {}) {
    if (state.closed || !state.responseHeadersReceived || !state.responseStreamEnded) return false;
    // A late stdio error alone is not proof that the Go bridge classified the
    // request-side close as post-response teardown.  In particular, a full
    // response can be written before Client.Proxy returns upload_stream. Keep
    // the bytes pending until the helper confirms success with exit code 0.
    state.postResponseTeardown = true;
    state.postResponseTeardownDetails = details;
    if (state.postResponseCloseTimer === null) {
      state.postResponseCloseTimer = this.setTimer(() => {
        if (state.closed || state.childClosed) return;
        this.recordDiagnostic(this.connectionDiagnostic(state, 'post_response_close_timeout', 0));
        this.finishConnection(state);
        state.socket.destroy?.();
      }, this.childExitTimeoutMs);
    }
    return true;
  }

  async acceptHandshake(state, buffered, handshake) {
    try {
      const metadata = {
        method: handshake.method,
        target: handshake.target,
        httpVersion: handshake.httpVersion,
        headers: Object.fromEntries([...handshake.headers].map(([name, values]) => [name, [...values]])),
        remoteAddress: state.socket.remoteAddress || '',
      };
      const decision = await this.authorizeRequest(metadata);
      if (state.closed) return;
      const authorized = decision === true || decision === undefined || decision?.authorized === true;
      if (!authorized) {
        this.rejectConnection(state, 403, 'Rejected an unauthorized local proxy request');
        return;
      }
      // Keep the same owner-bound token authorization and synchronous stop
      // admission as API requests, but obtain the UI from this desktop build.
      if (state.closed || !this.isAccepting()) return;
      if (this.serveDesktopRenderer(state, handshake, buffered)) return;
      let additions = normalizeInjectedHeaders(await this.injectRequestHeaders(metadata));
      if (additions.length > 0) {
        additions = [
          ...additions.filter(({ name }) => name.toLowerCase() !== 'connection'),
          { name: 'Connection', value: 'close' },
        ];
      }
      if (state.closed || !this.isAccepting()) return;
      const forwarded = injectHeaders(buffered, handshake, additions, this.maxHeaderBytes);
      // The authorization and injection hooks are async.  Re-check admission
      // immediately before spawn so a stop that began during either await can
      // never create a helper after the listener was quiesced.
      if (state.closed || !this.isAccepting()) return;

      if (state.handshakeTimer !== null) this.clearTimer(state.handshakeTimer);
      state.handshakeTimer = null;
      let helperBinding;
      try {
        helperBinding = this.verifyWindowsHelperBinding(await this.resolveWindowsHelperBinding());
      } catch {
        this.rejectBeforeResponseHeaders(state, 502, 'helper_identity');
        return;
      }
      if (state.closed || !this.isAccepting()) return;
      let child;
      try {
        child = this.spawn(this.windowsProbeExe, this.commandArguments(), {
          windowsHide: true,
          shell: false,
          stdio: ['pipe', 'pipe', 'pipe'],
          env: this.processEnvironment,
        });
      } catch {
        this.rejectBeforeResponseHeaders(state, 502, 'spawn');
        return;
      }
      if (!child || typeof child.once !== 'function' || !child.stdin || !child.stdout || !child.stderr) {
        this.rejectBeforeResponseHeaders(state, 502, 'spawn');
        return;
      }
      // Publish child ownership before the post-spawn identity check. If that
      // check fails, stop() must still wait for the killed helper's close event.
      state.child = child;
      state.helperPid = Number.isSafeInteger(child.pid) && child.pid > 0 ? child.pid : null;
      this.children.add(child);
      const onTrackedChildClose = () => {
        this.children.delete(child);
        state.childClosed = true;
      };
      child.once('close', onTrackedChildClose);
      const onEarlyChildError = () => {};
      child.once('error', onEarlyChildError);
      try {
        this.verifyWindowsHelperBinding(helperBinding);
      } catch {
        this.rejectBeforeResponseHeaders(state, 502, 'helper_identity');
        return;
      }
      if (state.closed) {
        child.kill?.();
        return;
      }
      state.requestBytes = forwarded.length;
      child.removeListener?.('error', onEarlyChildError);

      state.responseHeaderTimer = this.setTimer(() => {
        this.rejectBeforeResponseHeaders(state, 504, 'response_headers_timeout');
      }, this.responseHeaderTimeoutMs);
      state.responseGate = this.createResponseGate(state);
      child.stdout.pipe(state.responseGate).pipe(state.socket, { end: false });

      state.socket.once('error', () => {
        this.finishConnection(state);
        state.socket.destroy?.();
      });
      child.once('error', () => {
        if (state.closed) return;
        if (state.responseHeadersReceived) {
          if (!this.deferPostResponseTeardown(state)) {
            this.finishConnection(state);
            state.socket.destroy?.();
          }
        }
        else this.rejectBeforeResponseHeaders(state, 502, 'spawn');
      });
      child.once('close', (exitCode, signal) => {
        if (state.onBackendStderrError) child.stderr?.removeListener?.('error', state.onBackendStderrError);
        if (state.child !== child || state.closed) return;
        try {
          this.verifyWindowsHelperBinding(helperBinding);
        } catch {
          this.recordDiagnostic(this.connectionDiagnostic(state, 'helper_identity', 0, { exitCode, signal }));
          if (state.responseHeadersReceived) {
            this.finishConnection(state, { killChild: false });
            state.socket.destroy?.();
          } else {
            this.rejectBeforeResponseHeaders(state, 502, 'helper_identity', { killChild: false });
          }
          return;
        }
        if (!state.responseHeadersReceived) {
          this.rejectConnection(
            state,
            503,
            this.connectionDiagnostic(state, 'close', 503, { exitCode, signal }),
            { killChild: false },
          );
          return;
        }
        // Only an explicit zero exit code proves that the helper completed the
        // already-framed response. Node can report null/undefined while a
        // child is still being torn down; treating that as EOF would turn an
        // unconfirmed helper outcome into a successful browser response.
        if (exitCode !== 0 || signal) {
          this.recordDiagnostic(this.connectionDiagnostic(state, 'stream_close', 0, { exitCode, signal }));
          this.finishConnection(state, { killChild: false });
          state.socket.destroy?.();
          return;
        }
        state.childCloseClean = true;
        this.completeCleanResponseIfReady(state);
      });
      child.stdin?.once?.('error', () => {
        if (state.closed) return;
        if (state.responseHeadersReceived) {
          if (!this.deferPostResponseTeardown(state)) {
            this.finishConnection(state);
            state.socket.destroy?.();
          }
        }
        else this.rejectBeforeResponseHeaders(state, 502, 'bridge_stdin');
      });
      child.stdout?.once?.('error', () => {
        if (state.closed) return;
        if (state.responseHeadersReceived) {
          if (!this.deferPostResponseTeardown(state)) {
            this.finishConnection(state);
            state.socket.destroy?.();
          }
        }
        else this.rejectBeforeResponseHeaders(state, 502, 'bridge_stdout');
      });
      state.onBackendStderr = (chunk) => this.observeBackendStderr(state, chunk);
      state.onBackendStderrError = () => {
        if (state.closed) return;
        if (state.responseHeadersReceived) {
          if (this.deferPostResponseTeardown(state)) return;
          this.recordDiagnostic(this.connectionDiagnostic(state, 'bridge_stderr', 0));
          this.finishConnection(state);
          state.socket.destroy?.();
        } else {
          this.rejectBeforeResponseHeaders(state, 502, 'bridge_stderr');
        }
      };
      child.stderr.on('data', state.onBackendStderr);
      child.stderr.once('error', state.onBackendStderrError);
      state.onForwardedRequestData = (chunk) => { state.requestBytes += Buffer.byteLength(chunk); };
      state.socket.on?.('data', state.onForwardedRequestData);
      try {
        child.stdin.write(forwarded);
        state.socket.pipe(child.stdin);
        state.socket.resume?.();
      } catch {
        this.rejectBeforeResponseHeaders(state, 502, 'bridge_stdin');
      }
    } catch (error) {
      this.rejectConnection(state, error?.statusCode || 403, 'Rejected an invalid local proxy request');
    }
  }

  rejectIncomingConnection(socket) {
    // Rejected sockets are deliberately not tracked as live proxy
    // connections, but they can still receive an asynchronous ECONNRESET
    // while the synthetic 503 is being flushed.  Consume every such error and
    // destroy the one-shot socket so a late local RST cannot become an
    // unhandled EventEmitter error in Electron.
    const absorbError = () => {
      try { socket.destroy?.(); } catch { /* The socket is already terminal. */ }
    };
    socket.on?.('error', absorbError);
    try { socket.end(httpErrorResponse(503), () => socket.destroy?.()); } catch { absorbError(); }
  }

  handleConnection(socket) {
    if (!this.isAccepting()) {
      this.recordDiagnostic('[proxy] stage=proxy_stopping admission=rejected');
      this.rejectIncomingConnection(socket);
      return;
    }
    if (this.connections.size >= this.maxConnections) {
      this.rejectIncomingConnection(socket);
      this.recordDiagnostic('Rejected a local proxy connection because the concurrency limit was reached');
      return;
    }

    const state = {
      correlationId: this.nextCorrelationId,
      socket,
      child: null,
      closed: false,
      chunks: [],
      length: 0,
      handshakeTimer: null,
      responseHeaderTimer: null,
      postResponseCloseTimer: null,
      responseHeaderBytes: Buffer.alloc(0),
      responseHeadersReceived: false,
      responseStreamEnded: false,
      responseGate: null,
      childClosed: false,
      childCloseClean: false,
      postResponseTeardown: false,
      postResponseTeardownDetails: {},
      helperPid: null,
      requestBytes: 0,
      responseBytes: 0,
      stderrBytes: 0,
      controlProxyStage: null,
      stderrMatchers: CONTROL_PROXY_STAGE_PATTERNS.map(({ stage, pattern }) => ({ stage, pattern, offset: 0 })),
      onData: null,
      onPrematureEnd: null,
      onBackendStderr: null,
      onBackendStderrError: null,
      onResponseGateError: null,
      onResponseGateEnd: null,
      onForwardedRequestData: null,
    };
    this.nextCorrelationId += 1;
    this.connections.add(state);
    socket.once('close', () => this.finishConnection(state));
    socket.once('error', () => this.finishConnection(state));
    if (typeof socket.setTimeout === 'function') {
      socket.setTimeout(this.idleTimeoutMs, () => {
        this.recordDiagnostic('Closed an idle local proxy connection');
        socket.destroy?.();
        this.finishConnection(state);
      });
    }
    state.handshakeTimer = this.setTimer(() => {
      this.rejectConnection(state, 408, 'Timed out while waiting for a local HTTP handshake');
    }, this.handshakeTimeoutMs);
    state.onPrematureEnd = () => this.finishConnection(state);
    state.onData = (chunk) => {
      if (state.closed) return;
      const value = Buffer.from(chunk);
      state.chunks.push(value);
      state.length += value.length;
      const buffered = Buffer.concat(state.chunks, state.length);
      const boundary = headerBoundary(buffered);
      if (!boundary) {
        if (state.length > this.maxHeaderBytes) this.rejectConnection(state, 431, 'Rejected oversized local HTTP request headers');
        return;
      }
      if (boundary.index + boundary.length > this.maxHeaderBytes) {
        this.rejectConnection(state, 431, 'Rejected oversized local HTTP request headers');
        return;
      }
      state.socket.pause?.();
      state.socket.removeListener?.('data', state.onData);
      state.socket.removeListener?.('end', state.onPrematureEnd);
      let handshake;
      try { handshake = parseHttpHandshake(buffered, this.maxHeaderBytes); } catch (error) {
        this.rejectConnection(state, error?.statusCode || 400, 'Rejected a malformed local HTTP handshake');
        return;
      }
      void this.acceptHandshake(state, buffered, handshake);
    };
    socket.on('data', state.onData);
    socket.once('end', state.onPrematureEnd);
    socket.resume?.();
  }

  start(port = 0) {
    // A start requested after stop() owns the desired final state. Check the
    // pending stop first: it may itself be waiting for an earlier start, and
    // returning that stale startPromise would hand out a port that is about to
    // be closed.
    if (this.stopPromise) return this.stopPromise.then(() => this.start(port));
    if (this.lifecycleState === 'stopping') {
      return Promise.reject(new Error('local WSL proxy is still stopping'));
    }
    if (this.server) return Promise.resolve(this.port);
    if (this.startPromise) return this.startPromise;
    this.setLifecycleState('starting');
    this.admissionOpen = false;
    const operation = new Promise((resolve, reject) => {
      let server;
      try {
        server = this.createServer({ pauseOnConnect: true }, (socket) => this.handleConnection(socket));
      } catch (error) {
        this.admissionOpen = false;
        this.setLifecycleState('stopped');
        reject(error);
        return;
      }
      let settled = false;
      const onError = (error) => {
        if (!settled) {
          settled = true;
          this.server = null;
          this.port = null;
          this.admissionOpen = false;
          if (this.lifecycleState !== 'stopping') this.setLifecycleState('stopped');
          reject(error);
        }
      };
      server.once('error', onError);
      server.listen({ host: LOOPBACK_HOST, port, exclusive: true }, () => {
        if (settled) return;
        settled = true;
        server.removeListener('error', onError);
        // Listener errors can contain an OS-provided endpoint description. Keep
        // the proxy diagnostic contract structural so those strings never reach
        // the desktop error page or persisted diagnostics.
        server.on('error', () => this.recordDiagnostic('[proxy] stage=listener_error'));
        this.server = server;
        const address = server.address();
        this.port = address && typeof address === 'object' ? address.port : port;
        // stop() may have quiesced admission while listen() was pending.  In
        // that case the listener is owned by the pending stop and must never
        // accept a request, even for the brief period before close().
        if (this.lifecycleState !== 'stopping') {
          this.admissionOpen = true;
          this.setLifecycleState('accepting');
        }
        resolve(this.port);
      });
    });
    let tracked;
    tracked = operation.finally(() => {
      if (this.startPromise === tracked) this.startPromise = null;
    });
    this.startPromise = tracked;
    return tracked;
  }

  waitForChildClose(child) {
    if (!this.children.has(child)) return Promise.resolve(true);
    return new Promise((resolve) => {
      let settled = false;
      let timer = null;
      const finish = (closed) => {
        if (settled) return;
        settled = true;
        if (timer !== null) this.clearTimer(timer);
        child.removeListener?.('close', onClose);
        if (!closed) {
          const pid = Number.isSafeInteger(child.pid) && child.pid > 0 ? child.pid : 'unknown';
          this.recordDiagnostic(`[proxy] stage=helper_close_timeout helper_pid=${pid}`);
        }
        resolve(closed);
      };
      const onClose = () => finish(true);
      child.once('close', onClose);
      timer = this.setTimer(() => finish(false), this.childExitTimeoutMs);
    });
  }

  stop() {
    if (this.stopPromise) return this.stopPromise;
    // The admission transition is synchronous and happens before any await.
    // This closes the TOCTOU window where a late capabilities/SSE request
    // could otherwise spawn a helper not present in the shutdown snapshot.
    this.admissionOpen = false;
    this.setLifecycleState('stopping');
    const operation = (async () => {
      if (this.startPromise) {
        try { await this.startPromise; } catch { /* A failed start leaves no listener to close. */ }
      }

      // Detach the listener immediately.  `server.close()` may wait for
      // sockets that are still open, so call it before destroying tracked
      // connections but await it only after all helpers have been confirmed.
      // A previous close failure leaves the actual listener owned by
      // `closingServer`; retry that same object before any new listener can be
      // created.  This keeps the stopped boundary recoverable without losing
      // child/listener ownership.
      const server = this.server || this.closingServer;
      this.server = null;
      this.port = null;
      let serverClose = this.closingServerPromise;
      if (server && !serverClose) {
        this.closingServer = server;
        serverClose = new Promise((resolve, reject) => {
          let settled = false;
          const finish = (error) => {
            if (settled) return;
            settled = true;
            if (error) reject(error);
            else resolve();
          };
          try {
            const result = server.close((error) => finish(error));
            // Some test doubles and older Node implementations return a
            // promise rather than reporting close errors through the callback.
            if (result && typeof result.then === 'function') result.then(() => finish(), finish);
          } catch (error) {
            finish(error);
          }
        });
        this.closingServerPromise = serverClose;
      }
      for (const state of [...this.connections]) {
        this.finishConnection(state);
        state.socket.destroy?.();
      }
      const closingChildren = [...this.children];
      for (const child of closingChildren) {
        if (typeof child.kill === 'function' && !child.killed) child.kill();
      }
      const closeResults = await Promise.all(closingChildren.map((child) => this.waitForChildClose(child)));
      const unconfirmedChildren = closingChildren.filter((_child, index) => !closeResults[index]);
      for (let index = 0; index < closingChildren.length; index += 1) {
        if (closeResults[index]) this.children.delete(closingChildren[index]);
      }
      if (serverClose) {
        try {
          await serverClose;
        } catch (error) {
          // Keep closingServer for the next stop retry, but do not retain a
          // rejected promise that would short-circuit every future attempt.
          this.closingServerPromise = null;
          throw error;
        }
      }
      if (unconfirmedChildren.length > 0) {
        throw new Error('local WSL proxy helper exit was not confirmed');
      }
      this.closingServer = null;
      this.closingServerPromise = null;
      this.setLifecycleState('stopped');
    })();
    let tracked;
    tracked = operation.finally(() => {
      if (this.stopPromise === tracked) this.stopPromise = null;
    });
    this.stopPromise = tracked;
    return tracked;
  }

  diagnostics() {
    return this.stderr.join('\n').split(/\r?\n/).slice(-200).join('\n');
  }
}

module.exports = {
  CONTROL_PROXY_STAGES,
  CONTROL_PATH,
  DEFAULT_CHILD_EXIT_TIMEOUT_MS,
  DEFAULT_HANDSHAKE_TIMEOUT_MS,
  DEFAULT_IDLE_TIMEOUT_MS,
  DEFAULT_MAX_CONNECTIONS,
  DEFAULT_MAX_HEADER_BYTES,
  DEFAULT_RESPONSE_HEADER_TIMEOUT_MS,
  LOOPBACK_HOST,
  WslLoopbackProxy,
  parseHttpHandshake,
  sanitizedEnvironment,
};

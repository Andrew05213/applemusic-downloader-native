'use strict';

const { execFile: nodeExecFile, spawn: nodeSpawn } = require('node:child_process');
const { promisify } = require('node:util');

const execFileAsync = promisify(nodeExecFile);
const DEFAULT_MAX_BUFFER = 2 * 1024 * 1024;

function assertArgumentArray(command, args) {
  if (typeof command !== 'string' || !command || command.includes('\u0000')) {
    throw new TypeError('Process command must be a non-empty string without NUL bytes');
  }
  if (!Array.isArray(args) || args.some((value) => typeof value !== 'string' || value.includes('\u0000'))) {
    throw new TypeError('Process arguments must be an array of strings without NUL bytes');
  }
}

function decodeProcessOutput(value) {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value.replace(/^\uFEFF/, '').replace(/\u0000/g, '');
  const buffer = Buffer.isBuffer(value) ? value : Buffer.from(value);
  if (buffer.length === 0) return '';

  const hasUtf16Bom = buffer.length >= 2 && buffer[0] === 0xff && buffer[1] === 0xfe;
  let oddNuls = 0;
  const sampleLength = Math.min(buffer.length, 512);
  for (let index = 1; index < sampleLength; index += 2) {
    if (buffer[index] === 0) oddNuls += 1;
  }
  const looksUtf16 = hasUtf16Bom || oddNuls > Math.max(2, Math.floor(sampleLength / 8));
  return buffer.toString(looksUtf16 ? 'utf16le' : 'utf8').replace(/^\uFEFF/, '').replace(/\u0000/g, '');
}

function redactSecrets(value, secrets = []) {
  let output = String(value || '');
  const candidates = [...new Set(
    secrets
      .filter((secret) => typeof secret === 'string' || Buffer.isBuffer(secret))
      .map((secret) => Buffer.isBuffer(secret) ? secret.toString('utf8') : secret)
      .filter(Boolean),
  )].sort((left, right) => right.length - left.length);
  for (const secret of candidates) output = output.split(secret).join('[REDACTED]');
  return output;
}

class ProcessExecutionError extends Error {
  constructor(message, details = {}) {
    super(message);
    this.name = 'ProcessExecutionError';
    this.exitCode = details.exitCode;
    this.signal = details.signal;
    this.stdout = details.stdout || '';
    this.stderr = details.stderr || '';
  }
}

class ProcessRunner {
  constructor(options = {}) {
    this.execFile = options.execFile || execFileAsync;
    this.spawn = options.spawn || nodeSpawn;
  }

  async exec(command, args, options = {}) {
    assertArgumentArray(command, args);
    const secrets = options.secrets || [];
    try {
      const result = await this.execFile(command, args, {
        windowsHide: true,
        shell: false,
        timeout: options.timeoutMs || 60_000,
        maxBuffer: options.maxBuffer || DEFAULT_MAX_BUFFER,
        encoding: 'buffer',
        env: options.env || process.env,
        cwd: options.cwd,
      });
      return {
        stdout: redactSecrets(decodeProcessOutput(result && result.stdout), secrets),
        stderr: redactSecrets(decodeProcessOutput(result && result.stderr), secrets),
      };
    } catch (error) {
      const stdout = redactSecrets(decodeProcessOutput(error && error.stdout), secrets);
      const stderr = redactSecrets(decodeProcessOutput(error && error.stderr), secrets);
      const detail = stderr.trim() || stdout.trim() || redactSecrets(error && error.message, secrets) || 'process failed';
      throw new ProcessExecutionError(detail, {
        exitCode: error && (error.code ?? error.exitCode),
        signal: error && error.signal,
        stdout,
        stderr,
      });
    }
  }

  input(command, args, input, options = {}) {
    assertArgumentArray(command, args);
    const source = Buffer.isBuffer(input) ? input : Buffer.from(String(input || ''), 'utf8');
    if (source.length > (options.maxInputBytes || 64 * 1024)) {
      throw new RangeError('Process stdin payload exceeds the configured limit');
    }
    const payload = Buffer.from(source);
    const secrets = options.secrets || [];
    const maxBuffer = options.maxBuffer || DEFAULT_MAX_BUFFER;

    return new Promise((resolve, reject) => {
      let child;
      let stdout = Buffer.alloc(0);
      let stderr = Buffer.alloc(0);
      let settled = false;
      let timer;

      const wipe = () => payload.fill(0);
      const finish = (error, result) => {
        if (settled) return;
        settled = true;
        if (timer) clearTimeout(timer);
        wipe();
        if (error) reject(error);
        else resolve(result);
      };
      const append = (current, chunk) => {
        const next = Buffer.concat([current, Buffer.from(chunk)]);
        if (next.length > maxBuffer) {
          if (child && typeof child.kill === 'function') child.kill();
          finish(new ProcessExecutionError('Process output exceeded the configured limit'));
          return current;
        }
        return next;
      };

      try {
        child = this.spawn(command, args, {
          windowsHide: true,
          shell: false,
          stdio: ['pipe', 'pipe', 'pipe'],
          env: options.env || process.env,
          cwd: options.cwd,
        });
      } catch (error) {
        finish(new ProcessExecutionError(redactSecrets(error.message, secrets)));
        return;
      }

      if (child.stdout) child.stdout.on('data', (chunk) => { stdout = append(stdout, chunk); });
      if (child.stderr) child.stderr.on('data', (chunk) => { stderr = append(stderr, chunk); });
      child.once('error', (error) => {
        finish(new ProcessExecutionError(redactSecrets(error.message, secrets)));
      });
      child.once('close', (exitCode, signal) => {
        const decodedStdout = redactSecrets(decodeProcessOutput(stdout), secrets);
        const decodedStderr = redactSecrets(decodeProcessOutput(stderr), secrets);
        if (exitCode === 0) {
          finish(null, { stdout: decodedStdout, stderr: decodedStderr });
          return;
        }
        finish(new ProcessExecutionError(
          decodedStderr.trim() || decodedStdout.trim() || `Process exited with code ${exitCode}`,
          { exitCode, signal, stdout: decodedStdout, stderr: decodedStderr },
        ));
      });

      timer = setTimeout(() => {
        if (child && typeof child.kill === 'function') child.kill();
        finish(new ProcessExecutionError(`Process timed out after ${options.timeoutMs || 60_000}ms`));
      }, options.timeoutMs || 60_000);

      child.stdin.once('error', (error) => {
        finish(new ProcessExecutionError(redactSecrets(error.message, secrets)));
      });
      child.stdin.end(payload, wipe);
    });
  }
}

module.exports = {
  ProcessExecutionError,
  ProcessRunner,
  assertArgumentArray,
  decodeProcessOutput,
  redactSecrets,
};

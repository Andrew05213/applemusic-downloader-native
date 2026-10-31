'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');
const {
  ELECTRON_VERSION,
  EXECUTABLE,
  SMOKE_PROFILE_MARKER,
  resolveDRMRuntime,
} = require('./drm-runtime');

const TIMEOUT_MS = 90_000;
const MAX_OUTPUT_BYTES = 64 * 1024;

function stopProcessTree(child) {
  if (process.platform === 'win32' && child.pid) {
    const taskkill = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'taskkill.exe');
    spawnSync(taskkill, ['/pid', String(child.pid), '/T', '/F'], {
      shell: false,
      windowsHide: true,
      stdio: 'ignore',
      timeout: 5_000,
    });
  }
  child.kill('SIGKILL');
}

function smokeReceipt(output) {
  for (const line of output.trim().split(/\r?\n/).reverse()) {
    try {
      const result = JSON.parse(line);
      if (result.realCredentialsRead !== false || result.mediaDownloaded !== false) continue;
      if (result.ok === false && /^[a-z_]+$/.test(result.stage)) {
        return { ok: false, code: 'drm_fixture_failed', stage: result.stage,
          runtime: result.runtime, realCredentialsRead: false, mediaDownloaded: false };
      }
      if (result.ok === true && result.runtime === ELECTRON_VERSION
          && ['cdmReady', 'eme', 'encryptedFixture', 'cookieFixture'].every((key) => result[key] === true)) {
        return { ok: true, runtime: result.runtime, cdmReady: true, eme: true,
          encryptedFixture: true, cookieFixture: true, realCredentialsRead: false, mediaDownloaded: false };
      }
    } catch {
      // Native Electron diagnostics are not a fixture receipt.
    }
  }
  return null;
}

async function runDRMSmoke({
  repositoryRoot = path.resolve(__dirname, '..'),
  temporaryDirectory = os.tmpdir(),
  timeoutMs = TIMEOUT_MS,
  spawnChild = spawn,
  resolveRuntime = resolveDRMRuntime,
  environment = process.env,
} = {}) {
  const runtimeDirectory = resolveRuntime(repositoryRoot);
  const runDirectory = fs.mkdtempSync(path.join(temporaryDirectory, 'amdl-drm-smoke-'));
  const profile = path.join(runDirectory, 'profile');
  fs.mkdirSync(profile);
  fs.writeFileSync(path.join(profile, SMOKE_PROFILE_MARKER), 'synthetic-only\n');
  const childEnvironment = { ...environment, AMDL_DRM_FIXTURE_PROFILE: profile };
  delete childEnvironment.ELECTRON_RUN_AS_NODE;
  try {
    return await new Promise((resolve) => {
      const failure = (code) => ({ ok: false, code, stage: 'process',
        realCredentialsRead: false, mediaDownloaded: false });
      let child;
      try {
        child = spawnChild(path.join(runtimeDirectory, EXECUTABLE),
          [path.join(repositoryRoot, 'electron', 'drm-runtime-smoke.cjs')], {
            cwd: repositoryRoot,
            env: childEnvironment,
            stdio: ['ignore', 'pipe', 'pipe'],
            shell: false,
            windowsHide: true,
          });
      } catch {
        resolve(failure('drm_smoke_launch_failed'));
        return;
      }
      let output = '';
      let processFailure = '';
      const timer = setTimeout(() => {
        processFailure = 'drm_smoke_timeout';
        stopProcessTree(child);
      }, timeoutMs);
      child.stdout.on('data', (chunk) => {
        if (Buffer.byteLength(output) + chunk.length > MAX_OUTPUT_BYTES) {
          processFailure = 'drm_smoke_output_limit';
          stopProcessTree(child);
        } else {
          output += chunk.toString('utf8');
        }
      });
      // The fixture reports a bounded stage code; do not forward raw diagnostics.
      child.stderr.resume();
      child.once('error', () => {
        processFailure = 'drm_smoke_launch_failed';
      });
      child.once('close', (code) => {
        clearTimeout(timer);
        const receipt = smokeReceipt(output);
        if (processFailure) {
          resolve(failure(processFailure));
        } else if (!receipt || (code === 0) !== receipt.ok) {
          resolve(failure('drm_smoke_result_missing'));
        } else {
          resolve(receipt);
        }
      });
    });
  } finally {
    // Delete only the fresh directory returned by mkdtemp, after the child exits.
    fs.rmSync(runDirectory, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
  }
}

if (require.main === module) {
  runDRMSmoke().then((result) => {
    console.log(JSON.stringify(result));
    process.exitCode = result.ok ? 0 : 1;
  }).catch(() => {
    console.log(JSON.stringify({ ok: false, code: 'drm_smoke_setup_failed', stage: 'setup',
      realCredentialsRead: false, mediaDownloaded: false }));
    process.exitCode = 1;
  });
}

module.exports = { runDRMSmoke, smokeReceipt };

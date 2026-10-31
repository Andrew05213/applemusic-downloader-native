#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const fsp = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { execFile } = require('node:child_process');
const { fileURLToPath } = require('node:url');

const packageManifest = require('../package.json');
const { validateApplianceManifest, verifyArtifactFile } = require('./appliance-manifest');
const { ProcessRunner } = require('./process-runner');
const { WindowsCapabilities, defaultWindowsHelperPath } = require('./windows-capabilities');
const {
  WslManager,
  sanitizeProcessEnvironment,
} = require('./wsl-manager');
const {
  AuditedProcessRunner,
  WslSmokeError,
  publicSmokeError,
  runWslSmoke,
  safeIdentity,
  verifyOwnedAppliance,
} = require('./wsl-smoke');
const {
  BOOTSTRAP_FILE,
  loadInteropRecoveryDescriptorWithIdentity,
  requiresInteropRecoveryBinding,
} = require('./wslinterop-recovery');
const { adoptLocalPoc } = require('./local-poc-adoption');
const { bootstrapLocalPocIdentity } = require('./local-poc-bootstrap');
const { verifyWrapperMountsClean } = require('./wsl-mount-smoke');

const APP_NAME = 'AppleMusicDownloader';
const WORKER_MARKER = 'AMDL_WSL_SMOKE_WORKER';
const MODE_ARGUMENTS = new Set(['--check-only', '--network-only', '--lifecycle', '--adopt-local-poc', '--bootstrap-local-poc-identity']);
const MANIFEST_ARGUMENT = '--manifest';
const MAX_MANIFEST_BYTES = 1024 * 1024;

function usage() {
  return [
    'Usage:',
    '  node electron/run-wsl-smoke.js --check-only [--manifest <final local-poc manifest>]',
    '  node electron/run-wsl-smoke.js --network-only --manifest <final v1.3.5 manifest>',
    '  node electron/run-wsl-smoke.js --lifecycle',
    '  node electron/run-wsl-smoke.js --adopt-local-poc',
    '  node electron/run-wsl-smoke.js --bootstrap-local-poc-identity --legacy-runtime <旧包> --target-manifest <新 manifest> --target-runtime <新包>',
    '',
    '--check-only  仅验证既有且 ownership 匹配的 Appliance；指定 --manifest 时还会在 stopped 状态执行无凭据 Windows bridge/Bag preflight。',
    '--network-only  仅验证最终 manifest 绑定的 stopped Appliance 网络桥和 Bag preflight；不检查、读取或交付 credentials.bin。',
    '--lifecycle   在无凭据、无活动任务时验证 start/stop/restart 和管理进程退出语义。',
    '--adopt-local-poc  开发者专用；仅在全部严格身份条件满足时为既有本地 PoC 写入所有权。',
    '--bootstrap-local-poc-identity  开发者专用；证明旧 PoC 内容后，通过旧 update-stdin 原地迁移并接管。',
    '',
    '除显式 bootstrap 外，此入口永不执行 update；所有模式都禁止 unregister、wsl --shutdown，且不读取 config.yaml。',
  ].join('\n');
}

function samePath(left, right) {
  return path.normalize(path.resolve(left)).toLowerCase() === path.normalize(path.resolve(right)).toLowerCase();
}

async function regularFile(filename, maximumBytes = Number.MAX_SAFE_INTEGER) {
  const info = await fsp.lstat(filename);
  if (!info.isFile() || info.isSymbolicLink() || info.size <= 0 || info.size > maximumBytes) {
    throw new WslSmokeError('target_manifest_mismatch', 'target_file');
  }
  return info;
}

async function loadCheckOnlyManifest(repositoryRoot, manifestPath, {
  packageVersion = packageManifest.version,
  artifactVerifier = verifyArtifactFile,
} = {}) {
  const distDirectory = path.join(repositoryRoot, 'appliance', 'dist');
  const expectedManifest = path.join(
    distDirectory,
    `applemusic-downloader-wsl-manifest-${packageVersion}-amd64.json`,
  );
  try {
    const distInfo = await fsp.lstat(distDirectory);
    if (!distInfo.isDirectory() || distInfo.isSymbolicLink() || !samePath(manifestPath, expectedManifest)) {
      throw new Error('manifest path is not the controlled final artifact');
    }
    await regularFile(expectedManifest, MAX_MANIFEST_BYTES);
    const manifestRealPath = await fsp.realpath(expectedManifest);
    if (!samePath(manifestRealPath, expectedManifest)) throw new Error('manifest path changed');
    const manifestBytes = await fsp.readFile(expectedManifest);
    const manifest = validateApplianceManifest(JSON.parse(manifestBytes.toString('utf8')));
    if (manifest.version !== packageVersion || manifest.productVersion !== packageVersion
        || manifest.channel !== 'local-poc' || manifest.distroName !== APP_NAME
        || manifest.architecture !== 'amd64' || manifest.releasePolicy.publicReleaseAllowed !== false) {
      throw new Error('manifest identity is not the current private build');
    }
    let artifactsVerified = 0;
    for (const artifact of [manifest.rootfs, manifest.runtime]) {
      const url = new URL(artifact.url);
      if (url.protocol !== 'file:') throw new Error('artifact is not local');
      const artifactPath = fileURLToPath(url);
      const expectedArtifact = path.join(distDirectory, artifact.file);
      if (!samePath(artifactPath, expectedArtifact)) throw new Error('artifact path is not controlled');
      await regularFile(expectedArtifact);
      const artifactRealPath = await fsp.realpath(expectedArtifact);
      if (!samePath(artifactRealPath, expectedArtifact)) throw new Error('artifact path changed');
      await artifactVerifier(artifact, expectedArtifact);
      artifactsVerified += 1;
    }
    const manifestAfterVerification = await fsp.readFile(expectedManifest);
    if (!manifestAfterVerification.equals(manifestBytes)) throw new Error('manifest changed while artifacts were verified');
    return { manifest, artifactsVerified };
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    throw new WslSmokeError('target_manifest_mismatch', 'target_manifest');
  }
}

function parseMode(argv) {
  if (argv.length === 1 && argv[0] === '--help') return { mode: 'help' };
  if (argv.includes('--worker-start')) {
    if (argv.length !== 1 || process.env[WORKER_MARKER] !== '1') throw new WslSmokeError('unsafe_process', 'worker');
    return { mode: 'worker' };
  }
  const selectedModes = argv.filter((argument) => MODE_ARGUMENTS.has(argument));
  if (selectedModes.length > 1) {
    throw new WslSmokeError('unsafe_process', 'arguments');
  }
  const mode = selectedModes[0] === '--network-only' ? 'network-only'
    : selectedModes[0] === '--lifecycle' ? 'lifecycle'
    : selectedModes[0] === '--adopt-local-poc' ? 'adopt-local-poc'
      : selectedModes[0] === '--bootstrap-local-poc-identity' ? 'bootstrap-local-poc-identity'
        : 'check-only';
  if (mode !== 'bootstrap-local-poc-identity') {
    let manifestPath = '';
    for (let index = 0; index < argv.length; index += 1) {
      const argument = argv[index];
      if (MODE_ARGUMENTS.has(argument)) continue;
      if ((mode === 'check-only' || mode === 'network-only') && argument === MANIFEST_ARGUMENT && !manifestPath
          && index + 1 < argv.length && !argv[index + 1].startsWith('--')) {
        manifestPath = argv[index + 1];
        index += 1;
        continue;
      }
      throw new WslSmokeError('unsafe_process', 'arguments');
    }
    if (mode === 'network-only' && !manifestPath) throw new WslSmokeError('unsafe_process', 'arguments');
    return { mode, manifestPath };
  }
  const values = {};
  const allowedValues = new Set(['--legacy-runtime', '--target-manifest', '--target-runtime']);
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--bootstrap-local-poc-identity') continue;
    if (!allowedValues.has(argument) || index + 1 >= argv.length || argv[index + 1].startsWith('--')
        || Object.hasOwn(values, argument)) throw new WslSmokeError('unsafe_process', 'arguments');
    values[argument] = argv[index + 1];
    index += 1;
  }
  if (Object.keys(values).length !== allowedValues.size) throw new WslSmokeError('unsafe_process', 'arguments');
  return {
    mode,
    legacyRuntimePath: values['--legacy-runtime'],
    targetManifestPath: values['--target-manifest'],
    targetRuntimePath: values['--target-runtime'],
  };
}

function defaultAppDataDir() {
  const localAppData = process.env.LOCALAPPDATA;
  if (!localAppData || !path.win32.isAbsolute(localAppData)) {
    throw new WslSmokeError('smoke_failed', 'host_state');
  }
  return path.win32.join(localAppData, APP_NAME);
}

async function readRegisteredState(appDataDir) {
  const statePath = path.win32.join(appDataDir, 'appliance-state.json');
  let document;
  try {
    document = JSON.parse(await fsp.readFile(statePath, 'utf8'));
  } catch {
    throw new WslSmokeError('owner_identity_mismatch', 'host_state');
  }
  if (!document || document.schemaVersion !== 1 || document.distroName !== APP_NAME
      || typeof document.downloadDir !== 'string' || !path.win32.isAbsolute(document.downloadDir)) {
    throw new WslSmokeError('owner_identity_mismatch', 'host_state');
  }
  return { document, statePath };
}

async function fileExists(filename) {
  try {
    const info = await fsp.lstat(filename);
    return info.isFile() || info.isSymbolicLink();
  } catch (error) {
    if (error.code === 'ENOENT') return false;
    throw new WslSmokeError('credentials_present', 'lifecycle_precondition');
  }
}

function systemInfoProvider(capabilities) {
  return async () => {
    try {
      const result = await capabilities.probe();
      if (result.ok) {
        return {
          virtualizationEnabled: result.virtualizationEnabled === true ? true : null,
          rebootRequired: false,
        };
      }
    } catch { /* Real wsl.exe probes below are authoritative for smoke. */ }
    return { virtualizationEnabled: null, rebootRequired: false };
  };
}

async function createRuntime({
  allowTerminate = false,
  allowUpdate = false,
  networkOnly = false,
  audits = [],
  resources = [],
  expectedManifest = null,
} = {}) {
  const appDataDir = defaultAppDataDir();
  const { document, statePath } = await readRegisteredState(appDataDir);
  const processEnvironment = sanitizeProcessEnvironment(process.env);
  const baseRunner = new ProcessRunner();
  const audit = new AuditedProcessRunner(baseRunner, { allowTerminate, allowUpdate, networkOnly });
  audits.push(audit);
  const repositoryRoot = path.resolve(__dirname, '..');
  const helperPath = defaultWindowsHelperPath({ appPath: repositoryRoot, resourcesPath: '' });
  if (!fs.existsSync(helperPath)) throw new WslSmokeError('smoke_failed', 'windows_helper');
  let interopRecoveryIdentity = null;
  if (requiresInteropRecoveryBinding(expectedManifest?.version)) {
    try {
      interopRecoveryIdentity = loadInteropRecoveryDescriptorWithIdentity({
        expectedVersion: expectedManifest.version,
        repositoryRoot,
        packaged: false,
      });
    } catch {
      throw new WslSmokeError('target_manifest_mismatch', 'recovery_descriptor');
    }
  }
  const capabilities = new WindowsCapabilities({
    helperPath,
    helperBindingProvider: () => {
      const binding = interopRecoveryIdentity?.descriptor?.windowsHelper;
      if (!binding) throw new Error('windows helper binding is unavailable');
      return binding;
    },
    runner: (command, args) => audit.exec(command, args, { timeoutMs: 30_000, env: processEnvironment }),
  });
  const manager = new WslManager({
    appDataDir,
    statePath,
    installDir: path.win32.join(appDataDir, 'wsl'),
    ownershipPath: path.win32.join(appDataDir, 'ownership.id'),
    downloadDir: document.downloadDir,
    protectedDownloadRoots: [appDataDir],
    windowsProbeExe: helperPath,
    windowsHelperBindingProvider: () => {
      const binding = interopRecoveryIdentity?.descriptor?.windowsHelper;
      if (!binding) throw new Error('windows helper binding is unavailable');
      return binding;
    },
    interopBootstrapExe: path.win32.join(path.win32.dirname(helperPath), BOOTSTRAP_FILE),
    ...(interopRecoveryIdentity ? {
      interopRecoveryIdentityProvider: () => interopRecoveryIdentity,
    } : {}),
    processEnvironment,
    systemInfo: systemInfoProvider(capabilities),
    runner: audit,
    spawn: (command, args, options) => audit.spawn(command, args, options),
  });
  resources.push(manager);
  return { manager, audit, state: document, appDataDir };
}

async function closeDesktopResources(managers) {
  for (const manager of [...new Set(managers)].reverse()) {
    if (!manager) continue;
    // A check-only manager normally never owns a Windows listener. Calling
    // discardLocalProxy in that shape would still persist a cleared endpoint,
    // clobbering metadata published by the running desktop process. Only
    // clean up an object this process demonstrably owns (including a failed
    // stop that remains latched for safe retry).
    if (typeof manager.discardLocalProxy === 'function' && (manager.proxy || manager.proxyStopPending)) {
      await manager.discardLocalProxy({ clearPersistedEndpoint: true });
      continue;
    }
    if (!manager.proxy) continue;
    await manager.proxy.stop();
    manager.proxy = null;
    manager.proxyLocalPort = null;
    manager.proxyBackendPort = null;
    manager.serviceBaseUrl = '';
  }
}

function runWorkerProcess() {
  const environment = {
    ...sanitizeProcessEnvironment(process.env),
    [WORKER_MARKER]: '1',
  };
  return new Promise((resolve, reject) => {
    execFile(process.execPath, [__filename, '--worker-start'], {
      shell: false,
      windowsHide: true,
      encoding: 'utf8',
      timeout: 210_000,
      env: environment,
      maxBuffer: 1024 * 1024,
    }, (error, stdout) => {
      if (error) {
        reject(new WslSmokeError('smoke_failed', 'manager_exit'));
        return;
      }
      try {
        const result = JSON.parse(stdout);
        if (!result?.ok || result.identity?.service !== 'applemusic-downloader'
            || result.identity?.runtime !== 'wsl-appliance') {
          throw new Error('worker identity mismatch');
        }
        resolve(result);
      } catch {
        reject(new WslSmokeError('identity_mismatch', 'manager_exit'));
      }
    });
  });
}

function mergeAudits(audits, workerAudit = null) {
  const summaries = audits.map((audit) => audit.summary());
  if (workerAudit) summaries.push(workerAudit);
  const merged = {
    commandCount: 0,
    binaries: {},
    dockerCommandsObserved: 0,
    forbiddenCommandsObserved: 0,
    updateCommandsObserved: 0,
    sensitiveEnvironmentKeysObserved: 0,
    stdinOnlyControlDocuments: 0,
  };
  for (const summary of summaries) {
    merged.commandCount += Number(summary.commandCount || 0);
    merged.dockerCommandsObserved += Number(summary.dockerCommandsObserved || 0);
    merged.forbiddenCommandsObserved += Number(summary.forbiddenCommandsObserved || 0);
    merged.updateCommandsObserved += Number(summary.updateCommandsObserved || 0);
    merged.sensitiveEnvironmentKeysObserved += Number(summary.sensitiveEnvironmentKeysObserved || 0);
    merged.stdinOnlyControlDocuments += Number(summary.stdinOnlyControlDocuments || 0);
    for (const [binary, count] of Object.entries(summary.binaries || {})) {
      merged.binaries[binary] = (merged.binaries[binary] || 0) + Number(count || 0);
    }
  }
  return merged;
}

async function workerMain() {
  const audits = [];
  const { manager, state } = await createRuntime({ allowTerminate: false, audits });
  const expectedVersion = typeof state.installedVersion === 'string' ? state.installedVersion : '';
  await verifyOwnedAppliance(manager, expectedVersion);
  const status = await manager.start(undefined, { expectedVersion });
  const result = {
    ok: true,
    identity: safeIdentity(status),
    audit: mergeAudits(audits),
  };
  await new Promise((resolve) => process.stdout.write(`${JSON.stringify(result)}\n`, resolve));
  // Intentionally end only the desktop-side manager process. The detached WSL
  // keepalive and Appliance backend must remain alive for the parent probe.
  process.exit(0);
}

async function main() {
  const parsed = parseMode(process.argv.slice(2));
  const mode = parsed.mode;
  if (mode === 'help') {
    process.stdout.write(`${usage()}\n`);
    return 0;
  }
  if (mode === 'worker') {
    await workerMain();
    return 0;
  }

  const audits = [];
  const resources = [];
  let workerAudit = null;
  try {
    const repositoryRoot = path.resolve(__dirname, '..');
    const selectedTarget = (mode === 'check-only' || mode === 'network-only') && parsed.manifestPath
      ? await loadCheckOnlyManifest(repositoryRoot, parsed.manifestPath)
      : null;
    const runtime = await createRuntime({
      allowTerminate: mode === 'lifecycle' || mode === 'bootstrap-local-poc-identity',
      allowUpdate: mode === 'bootstrap-local-poc-identity',
      networkOnly: mode === 'network-only',
      audits,
      resources,
      expectedManifest: selectedTarget?.manifest || null,
    });
    // network-only deliberately does not even lstat credentials.bin. Its
    // accepted surface is limited to ownership/status and the network bridge.
    const credentialsPresent = mode === 'network-only'
      ? false
      : await fileExists(path.win32.join(runtime.appDataDir, 'credentials.bin'));
    if (mode === 'bootstrap-local-poc-identity') {
      const bootstrap = await bootstrapLocalPocIdentity(runtime.manager, {
        credentialsPresent,
        legacyRuntimePath: parsed.legacyRuntimePath,
        targetManifestPath: parsed.targetManifestPath,
        targetRuntimePath: parsed.targetRuntimePath,
      });
      const result = {
        schemaVersion: 1,
        smoke: 'applemusic-downloader-wsl-appliance',
        mode,
        ok: true,
        bootstrap,
        audit: mergeAudits(audits),
        host: { platform: process.platform, architecture: os.arch(), dockerRequired: false },
      };
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    }
    const adoption = mode === 'adopt-local-poc'
      ? await adoptLocalPoc(runtime.manager, { credentialsPresent })
      : null;
    const result = await runWslSmoke({
      manager: runtime.manager,
      lifecycle: mode === 'lifecycle',
      networkOnly: mode === 'network-only',
      credentialsPresent,
      expectedManifest: selectedTarget?.manifest || null,
      managerFactory: async () => (await createRuntime({ allowTerminate: true, audits, resources })).manager,
      detachedStarter: async () => {
        const worker = await runWorkerProcess();
        workerAudit = worker.audit;
      },
      mountInspector: async (manager, version, step) => {
        try {
          return await verifyWrapperMountsClean(manager, version, { step });
        } catch {
          throw new WslSmokeError('wrapper_mount_residue', step);
        }
      },
    });
    if (adoption) {
      result.mode = 'adopt-local-poc';
      result.adoption = adoption;
    }
    if (selectedTarget) result.targetArtifactsVerified = selectedTarget.artifactsVerified;
    result.audit = mergeAudits(audits, workerAudit);
    result.host = {
      platform: process.platform,
      architecture: os.arch(),
      dockerRequired: false,
    };
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    const result = {
      schemaVersion: 1,
      smoke: 'applemusic-downloader-wsl-appliance',
      mode,
      ok: false,
      error: publicSmokeError(error),
      audit: mergeAudits(audits, workerAudit),
      safety: {
        updateAttempted: mergeAudits(audits, workerAudit).updateCommandsObserved > 0,
        unregisterAttempted: false,
        globalShutdownAttempted: false,
        credentialsRead: false,
        mediaDownloaded: false,
      },
    };
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 2;
  } finally {
    await closeDesktopResources(resources);
  }
}

if (require.main === module) {
  main().then((code) => {
    if (process.exitCode === undefined) process.exitCode = code;
  }).catch(() => {
    process.stdout.write(`${JSON.stringify({
      schemaVersion: 1,
      smoke: 'applemusic-downloader-wsl-appliance',
      ok: false,
      error: publicSmokeError(new WslSmokeError('smoke_failed', 'entrypoint')),
    }, null, 2)}\n`);
    process.exitCode = 2;
  });
}

module.exports = { closeDesktopResources, loadCheckOnlyManifest, parseMode };

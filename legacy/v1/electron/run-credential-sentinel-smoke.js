#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const fsp = require('node:fs/promises');
const path = require('node:path');

const { validateApplianceManifest } = require('./appliance-manifest');
const { manifestCandidates } = require('./release-manifest');
const { ProcessRunner } = require('./process-runner');
const { WindowsCapabilities, defaultWindowsHelperPath } = require('./windows-capabilities');
const {
  WslManager,
  sanitizeProcessEnvironment,
} = require('./wsl-manager');
const {
  CredentialSentinelProcessAudit,
  CredentialSentinelSmokeError,
  discoverHostArtifacts,
  publicCredentialSmokeError,
  runCredentialSentinelSmoke,
} = require('./credential-sentinel-smoke');

const APP_NAME = 'AppleMusicDownloader';
const EXECUTE_ARGUMENT = '--execute-private-runtime';
const ALLOWED_ARGUMENTS = new Set([EXECUTE_ARGUMENT, '--help']);

function usage() {
  return [
    'Usage:',
    `  node electron/run-credential-sentinel-smoke.js ${EXECUTE_ARGUMENT}`,
    '',
    '此命令只用于已完成原地更新的私有 v1.2 runtime。',
    '它会在严格核对 ownership、runtime digest、事务 ID、无桌面凭据和 0 个活动任务后，',
    '短暂注入随机测试凭据并审计 stdin/FD、进程、日志、SQLite、API 与本地产物。',
    '不会读取 config.yaml、创建 DPAPI 文件、创建下载任务、下载媒体、更新或 unregister。',
  ].join('\n');
}

function parseArguments(argv) {
  if (argv.some((value) => !ALLOWED_ARGUMENTS.has(value)) || argv.length > 1) {
    throw new CredentialSentinelSmokeError('precondition_failed', 'arguments', 'arguments');
  }
  if (argv[0] === '--help') return 'help';
  if (argv[0] !== EXECUTE_ARGUMENT) throw new CredentialSentinelSmokeError('precondition_failed', 'arguments', 'arguments');
  return 'execute';
}

function defaultAppDataDir() {
  const localAppData = process.env.LOCALAPPDATA;
  if (!localAppData || !path.win32.isAbsolute(localAppData)) {
    throw new CredentialSentinelSmokeError('precondition_failed', 'host_state', 'runtime_identity');
  }
  return path.win32.join(localAppData, APP_NAME);
}

async function readRegisteredState(appDataDir) {
  const statePath = path.win32.join(appDataDir, 'appliance-state.json');
  let document;
  try {
    document = JSON.parse(await fsp.readFile(statePath, 'utf8'));
  } catch {
    throw new CredentialSentinelSmokeError('precondition_failed', 'host_state', 'runtime_identity');
  }
  if (!document || document.schemaVersion !== 1 || document.distroName !== APP_NAME
      || typeof document.downloadDir !== 'string' || !path.win32.isAbsolute(document.downloadDir)) {
    throw new CredentialSentinelSmokeError('precondition_failed', 'host_state', 'runtime_identity');
  }
  return { document, statePath };
}

async function exists(filename) {
  try {
    await fsp.lstat(filename);
    return true;
  } catch (error) {
    if (error.code === 'ENOENT') return false;
    throw new CredentialSentinelSmokeError('precondition_failed', 'credentials_store', 'credentials_store');
  }
}

function loadCredentialSmokeTargetManifest(repositoryRoot) {
  const distDirectory = path.resolve(repositoryRoot, 'appliance', 'dist');
  let distIdentity;
  try {
    const distInfo = fs.lstatSync(distDirectory);
    if (!distInfo.isDirectory() || distInfo.isSymbolicLink()) throw new Error('invalid dist directory');
    distIdentity = fs.realpathSync.native(distDirectory);
  } catch {
    throw new CredentialSentinelSmokeError('precondition_failed', 'target_manifest', 'runtime_identity');
  }
  const versioned = manifestCandidates({ repositoryRoot, resourcesPath: '' })
    .filter((candidate) => /^applemusic-downloader-wsl-manifest-1\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?-amd64\.json$/.test(path.basename(candidate)))
    .filter((candidate) => fs.existsSync(candidate));
  if (versioned.length !== 1) {
    throw new CredentialSentinelSmokeError('precondition_failed', 'target_manifest', 'runtime_identity');
  }
  const filename = versioned[0];
  try {
    const info = fs.lstatSync(filename);
    const finalPath = fs.realpathSync.native(filename);
    if (!info.isFile() || info.isSymbolicLink()
        || path.win32.dirname(finalPath).toLowerCase() !== distIdentity.toLowerCase()) {
      throw new Error('invalid target manifest identity');
    }
    return validateApplianceManifest(JSON.parse(fs.readFileSync(finalPath, 'utf8')));
  } catch (error) {
    if (error instanceof CredentialSentinelSmokeError) throw error;
    throw new CredentialSentinelSmokeError('precondition_failed', 'target_manifest', 'runtime_identity');
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
    } catch { /* WslManager performs the authoritative WSL checks. */ }
    return { virtualizationEnabled: null, rebootRequired: false };
  };
}

async function createRuntime() {
  const appDataDir = defaultAppDataDir();
  const { document, statePath } = await readRegisteredState(appDataDir);
  const processEnvironment = sanitizeProcessEnvironment(process.env);
  const baseRunner = new ProcessRunner();
  const audit = new CredentialSentinelProcessAudit(baseRunner);
  const repositoryRoot = path.resolve(__dirname, '..');
  const helperPath = defaultWindowsHelperPath({ appPath: repositoryRoot, resourcesPath: '' });
  if (!fs.existsSync(helperPath)) {
    throw new CredentialSentinelSmokeError('precondition_failed', 'windows_helper', 'runtime_identity');
  }
  const capabilities = new WindowsCapabilities({
    helperPath,
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
    processEnvironment,
    systemInfo: systemInfoProvider(capabilities),
    runner: audit,
    spawn: (command, args, options) => audit.spawn(command, args, options),
  });
  const credentialsPath = path.win32.join(appDataDir, 'credentials.bin');
  return {
    appDataDir,
    audit,
    credentialsPresent: () => exists(credentialsPath),
    manager,
    repositoryRoot,
  };
}

async function closeManager(manager) {
  if (!manager) return;
  let stopError = null;
  try {
    if (typeof manager.discardLocalProxy === 'function') {
      await manager.discardLocalProxy({ clearPersistedEndpoint: true });
    } else if (manager.proxy) {
      await manager.proxy.stop();
      manager.proxy = null;
      manager.proxyLocalPort = null;
      manager.proxyBackendPort = null;
      manager.serviceBaseUrl = '';
    }
  } catch (error) {
    stopError = error;
  } finally {
    manager.runtimeCredentials = null;
  }
  if (stopError) throw stopError;
}

async function main() {
  let runtime;
  try {
    const mode = parseArguments(process.argv.slice(2));
    if (mode === 'help') {
      process.stdout.write(`${usage()}\n`);
      return 0;
    }
    runtime = await createRuntime();
    const targetManifest = loadCredentialSmokeTargetManifest(runtime.repositoryRoot);
    const artifactPaths = await discoverHostArtifacts(runtime.repositoryRoot, {
      additionalRoots: [runtime.manager.artifactDir],
    });
    const result = await runCredentialSentinelSmoke({
      manager: runtime.manager,
      targetManifest,
      audit: runtime.audit,
      credentialsPresent: runtime.credentialsPresent,
      artifactPaths,
    });
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    const normalized = error instanceof CredentialSentinelSmokeError
      ? error
      : new CredentialSentinelSmokeError('credential_smoke_failed', 'entrypoint');
    process.stdout.write(`${JSON.stringify({
      schemaVersion: 1,
      smoke: 'applemusic-downloader-credential-sentinel',
      ok: false,
      error: publicCredentialSmokeError(normalized),
    }, null, 2)}\n`);
    return 2;
  } finally {
    await closeManager(runtime?.manager);
  }
}

if (require.main === module) {
  main().then((code) => {
    process.exitCode = code;
  }).catch(() => {
    process.stdout.write(`${JSON.stringify({
      schemaVersion: 1,
      smoke: 'applemusic-downloader-credential-sentinel',
      ok: false,
      error: publicCredentialSmokeError(new CredentialSentinelSmokeError('credential_smoke_failed', 'entrypoint')),
    }, null, 2)}\n`);
    process.exitCode = 2;
  });
}

module.exports = {
  EXECUTE_ARGUMENT,
  createRuntime,
  loadCredentialSmokeTargetManifest,
  main,
  parseArguments,
};

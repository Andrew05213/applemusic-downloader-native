'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const VERSION_PATTERN = /^1\.[0-9]+\.[0-9]+$/;
const RECOVERY_SCHEMA_VERSION = 1;
const BOOTSTRAP_FILE = 'amdl-wslinterop-bootstrap.exe';
const WINDOWS_HELPER_FILE = 'amdl-windows-probe.exe';

// This records the historical bootstrap identity for package compatibility.
// Automatic execution is disabled: a legacy supervisor reopens verified
// Windows pathnames later, which permits a per-user path TOCTOU replacement.
// It is intentionally a build identity, not a broad version comparison.
const INCIDENT_SOURCE = Object.freeze({
  productVersion: '1.2.13',
  applianceBuildId: '44dce654d3076fa9e1f600fd566523b89299510d75052a6d6b716972dd49c3ee',
  runtimeSha256: 'cc29eb17fec8fc9378053e7b391c6a8e77e3a111bd4a254922ed562958700ac3',
  androidMainBuiltSha256: '110562ac9bc44d9eb07a69d9123943e0783d6a14fe0a9428568fbe59e42145d4',
  rendererSha256: '95e58bd1d5cf3ee1ac17895192b799ab530a76606248a0362e1f6f2e13c91df5',
  windowsHelperSha256: '7f2f4d8232a5eb075145408b41f6d14299a79e1eb8baac2f9c5f8f5cef84789f',
});

function canonicalSha256(value) {
  return typeof value === 'string' && SHA256_PATTERN.test(value);
}

function requiresInteropRecoveryBinding(version) {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(String(version || ''));
  if (!match) return false;
  const actual = [Number(match[1]), Number(match[2]), Number(match[3])];
  if (actual[0] !== 1) return actual[0] > 1;
  if (actual[1] !== 2) return actual[1] > 2;
  return actual[2] >= 14;
}

function sha256FileSync(filename) {
  return crypto.createHash('sha256').update(fs.readFileSync(filename)).digest('hex');
}

function positiveSize(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function validateFileBinding(value, expectedFile, label) {
  if (!value || typeof value !== 'object' || Array.isArray(value)
      || Object.keys(value).sort().join(',') !== 'file,sha256,size'
      || value.file !== expectedFile || !canonicalSha256(value.sha256) || !positiveSize(value.size)) {
    throw new TypeError(`${label} binding is invalid`);
  }
  return Object.freeze({ file: value.file, sha256: value.sha256, size: value.size });
}

function validateSource(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)
      || Object.keys(value).sort().join(',')
        !== 'androidMainBuiltSha256,applianceBuildId,productVersion,rendererSha256,runtimeSha256,windowsHelperSha256') {
    throw new TypeError('legacy recovery source identity is invalid');
  }
  for (const [field, expected] of Object.entries(INCIDENT_SOURCE)) {
    if (value[field] !== expected) throw new TypeError(`legacy recovery source ${field} is invalid`);
  }
  return INCIDENT_SOURCE;
}

function validateInteropRecoveryDescriptor(value, { expectedVersion = '' } = {}) {
  if (!value || typeof value !== 'object' || Array.isArray(value)
      || Object.keys(value).sort().join(',') !== 'bootstrap,schemaVersion,source,targetVersion,windowsHelper'
      || value.schemaVersion !== RECOVERY_SCHEMA_VERSION || !VERSION_PATTERN.test(value.targetVersion)
      || (expectedVersion && value.targetVersion !== expectedVersion)) {
    throw new TypeError('WSLInterop recovery descriptor is invalid');
  }
  const descriptor = {
    schemaVersion: RECOVERY_SCHEMA_VERSION,
    targetVersion: value.targetVersion,
    source: validateSource(value.source),
    bootstrap: validateFileBinding(value.bootstrap, BOOTSTRAP_FILE, 'bootstrap'),
    windowsHelper: validateFileBinding(value.windowsHelper, WINDOWS_HELPER_FILE, 'Windows helper'),
  };
  return Object.freeze(descriptor);
}

function validateWindowsHelperBinding(value) {
  return validateFileBinding(value, WINDOWS_HELPER_FILE, 'Windows helper');
}

// This check deliberately uses Node's filesystem APIs rather than the helper
// itself. It is the trust gate that must run before the helper can execute a
// canonicalization, probe, lock, import, or elevated WSL action.
function verifyBoundRegularFile(filename, binding, expectedFile, label) {
  const expected = validateFileBinding(binding, expectedFile, label);
  if (typeof filename !== 'string' || !filename || path.basename(filename).toLowerCase() !== expected.file) {
    throw new Error(`${label} path is invalid`);
  }
  const info = fs.lstatSync(filename);
  if (!info.isFile() || info.isSymbolicLink() || info.size !== expected.size) {
    throw new Error(`${label} file identity is invalid`);
  }
  if (sha256FileSync(filename) !== expected.sha256) {
    throw new Error(`${label} SHA-256 changed`);
  }
  return expected;
}

function verifyWindowsHelperBinding(filename, binding) {
  return verifyBoundRegularFile(filename, binding, WINDOWS_HELPER_FILE, 'Windows helper');
}

function createInteropRecoveryDescriptor({ targetVersion, bootstrapPath, windowsHelperPath }) {
  if (!VERSION_PATTERN.test(targetVersion || '')) throw new TypeError('targetVersion is invalid');
  const bind = (filename, expectedName) => {
    const info = fs.lstatSync(filename);
    if (!info.isFile() || info.isSymbolicLink() || path.basename(filename) !== expectedName || !positiveSize(info.size)) {
      throw new Error(`recovery ${expectedName} is not a regular file`);
    }
    return { file: expectedName, sha256: sha256FileSync(filename), size: info.size };
  };
  return validateInteropRecoveryDescriptor({
    schemaVersion: RECOVERY_SCHEMA_VERSION,
    targetVersion,
    source: INCIDENT_SOURCE,
    bootstrap: bind(bootstrapPath, BOOTSTRAP_FILE),
    windowsHelper: bind(windowsHelperPath, WINDOWS_HELPER_FILE),
  }, { expectedVersion: targetVersion });
}

function loadInteropRecoveryDescriptorWithIdentity({
  expectedVersion = '',
  resourcesPath = process.resourcesPath,
  repositoryRoot = path.resolve(__dirname, '..'),
  packaged = false,
  explicitPath = '',
} = {}) {
  const candidates = [
    explicitPath,
    packaged && resourcesPath && path.join(resourcesPath, 'recovery', 'wslinterop-recovery.json'),
    path.join(repositoryRoot, 'electron', 'generated', 'wslinterop-recovery.json'),
  ].filter(Boolean);
  const filename = candidates.find((candidate) => fs.existsSync(candidate));
  if (!filename) throw new Error('此桌面构建未包含 WSLInterop recovery descriptor。请重新下载安装包。');
  const bytes = fs.readFileSync(filename);
  return Object.freeze({
    filename,
    sha256: crypto.createHash('sha256').update(bytes).digest('hex'),
    descriptor: validateInteropRecoveryDescriptor(JSON.parse(bytes.toString('utf8')), { expectedVersion }),
  });
}

function loadInteropRecoveryDescriptor(options = {}) {
  return loadInteropRecoveryDescriptorWithIdentity(options).descriptor;
}

function legacyInteropSourceMatches({ descriptor, manifest, status, state }) {
  let recovery;
  try { recovery = validateInteropRecoveryDescriptor(descriptor); } catch { return false; }
  const source = recovery.source;
  if (manifest?.version !== source.productVersion || manifest?.productVersion !== source.productVersion
      || manifest?.applianceBuildId !== source.applianceBuildId
      || manifest?.runtime?.sha256 !== source.runtimeSha256) return false;
  if (status?.version !== source.productVersion || status?.productVersion !== source.productVersion
      || status?.applianceBuildId !== source.applianceBuildId
      || status?.runtimeSha256 !== source.runtimeSha256
      || status?.androidMainBuiltSha256 !== source.androidMainBuiltSha256
      || status?.rendererSha256 !== source.rendererSha256) return false;
  const helper = status?.windowsHelperSha256 || '';
  if (helper !== source.windowsHelperSha256) {
    if (helper !== '' || status?.network?.state !== 'unprepared') return false;
  }
  return state?.installedVersion === source.productVersion
    && state?.installedRuntimeSha256 === source.runtimeSha256
    && typeof state?.installedRuntimeTransactionId === 'string'
    && state.installedRuntimeTransactionId === status?.runtimeTransactionId;
}

module.exports = {
  BOOTSTRAP_FILE,
  INCIDENT_SOURCE,
  RECOVERY_SCHEMA_VERSION,
  WINDOWS_HELPER_FILE,
  canonicalSha256,
  requiresInteropRecoveryBinding,
  createInteropRecoveryDescriptor,
  legacyInteropSourceMatches,
  loadInteropRecoveryDescriptor,
  loadInteropRecoveryDescriptorWithIdentity,
  sha256FileSync,
  verifyBoundRegularFile,
  validateInteropRecoveryDescriptor,
  validateWindowsHelperBinding,
  verifyWindowsHelperBinding,
};

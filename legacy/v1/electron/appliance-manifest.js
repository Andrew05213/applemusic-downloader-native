'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const https = require('node:https');
const path = require('node:path');
const { fileURLToPath } = require('node:url');
const { pipeline } = require('node:stream/promises');

const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const COMMIT_PATTERN = /^[a-f0-9]{40}$/;
const IMAGE_DIGEST_PATTERN = /^(?:[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?\/)*(?:[a-z0-9]+(?:[._-][a-z0-9]+)*)(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[a-f0-9]{64}$/;
const VERSION_PATTERN = /^1\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/;
const DISTRO_NAME = 'AppleMusicDownloader';
const CHANNELS = new Set(['local-poc', 'private-ci', 'private-build', 'public']);
const PROVENANCE_STATUSES = new Set([
  'mismatched-unverified-local-poc-only',
  'verified-source-build',
]);
const URL_REQUEST_ABI = Object.freeze({
  allocationBytes: 592,
  objectOffset: 32,
  objectBytes: 560,
  objectAlignment: 16,
});
const HTTP_MESSAGE_ABI = Object.freeze({
  allocationBytes: 0x1e0,
  objectOffset: 0x18,
  objectBytes: 0x1c8,
  storageAlignment: 16,
  objectAlignment: 8,
});
const STORE_ERROR_CODE_ABI = Object.freeze({
  objectOffset: 0x20,
  valueBytes: 4,
});

const MANIFEST_FIELDS = [
  'schemaVersion',
  'version',
  'architecture',
  'distroName',
  'channel',
  'builtAt',
  'rootfs',
  'runtime',
  'inputs',
  'releasePolicy',
];
const BUILD_IDENTITY_MANIFEST_FIELDS = [
  'productVersion',
  'sourceDateEpoch',
  'applianceBuildId',
];
const ARTIFACT_FIELDS = [
  'file',
  'url',
  'sha256',
  'size',
  'uncompressedSize',
  'compressedBytes',
  'uncompressedBytes',
];
const BASE_INPUT_FIELDS = [
  'inputLockSha256',
  'versionsLockSha256',
  'packagesLockSha256',
  'packagesResolvedLockSha256',
  'releasePolicySha256',
  'backendTreeSha256',
  'webUiTreeSha256',
  'wrapperTreeSha256',
  'wrapperBinarySha256',
  'androidMainSha256',
  'noticeSha256',
  'wrapperPatchSha256',
  'goBuilderImage',
  'ubuntuRuntimeImage',
  'ubuntuSnapshot',
  'wrapperCommit',
  'cjsonCommit',
  'dobbyCommit',
  'androidNdkVersion',
  'androidNdkSha256',
  'cmakeVersion',
  'cmakeLinuxAmd64Sha256',
  'mediaplatformBinarySha256',
  'storeservicescoreBinarySha256',
  'urlRequestAllocationBytes',
  'urlRequestObjectOffset',
  'urlRequestObjectBytes',
  'urlRequestObjectAlignment',
  'wrapperBuiltSha256',
  'androidMainBuiltSha256',
  'wrapperProvenanceStatus',
];
const HTTP_MESSAGE_INPUT_FIELDS = [
  'androidAppMusicBinarySha256',
  'httpMessageAllocationBytes',
  'httpMessageObjectOffset',
  'httpMessageObjectBytes',
  'httpMessageStorageAlignment',
  'httpMessageObjectAlignment',
];
const STORE_ERROR_CODE_INPUT_FIELDS = [
  'storeErrorCodeObjectOffset',
  'storeErrorCodeValueBytes',
];
const BUILT_PIN_PROVENANCE_INPUT_FIELDS = ['wrapperBuiltFromPatchSha256'];
const NETWORK_PREFLIGHT_INPUT_FIELDS = ['networkPreflightSourceSha256'];
const STANDALONE_NETWORK_PREFLIGHT_INPUT_FIELDS = ['androidNetworkPreflightBuiltSha256'];
const RENDERER_IDENTITY_INPUT_FIELDS = ['rendererSha256'];
const ANDROID_CA_BUNDLE_INPUT_FIELDS = ['androidCaBundlePath', 'androidCaBundleSha256'];
const SHA256_INPUT_FIELDS = [
  'inputLockSha256',
  'versionsLockSha256',
  'packagesLockSha256',
  'packagesResolvedLockSha256',
  'releasePolicySha256',
  'backendTreeSha256',
  'webUiTreeSha256',
  'wrapperTreeSha256',
  'wrapperBinarySha256',
  'androidMainSha256',
  'noticeSha256',
  'wrapperPatchSha256',
  'androidNdkSha256',
  'cmakeLinuxAmd64Sha256',
  'mediaplatformBinarySha256',
  'storeservicescoreBinarySha256',
  'androidAppMusicBinarySha256',
  'wrapperBuiltSha256',
  'wrapperBuiltFromPatchSha256',
  'androidMainBuiltSha256',
  'networkPreflightSourceSha256',
  'androidNetworkPreflightBuiltSha256',
  'rendererSha256',
  'androidCaBundleSha256',
];
const COMMIT_INPUT_FIELDS = ['wrapperCommit', 'cjsonCommit', 'dobbyCommit'];
const RELEASE_POLICY_FIELDS = ['publicReleaseAllowed', 'blockers'];

function assertObject(value, label) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new TypeError(`${label} must be an object`);
  }
}

function assertExactFields(value, fields, label) {
  assertObject(value, label);
  const expected = new Set(fields);
  const missing = fields.filter((field) => !Object.prototype.hasOwnProperty.call(value, field));
  if (missing.length > 0) throw new TypeError(`${label} is missing required field(s): ${missing.join(', ')}`);
  const unknown = Object.keys(value).filter((field) => !expected.has(field));
  if (unknown.length > 0) throw new TypeError(`${label} contains unknown field(s): ${unknown.join(', ')}`);
}

function assertManifestFields(value) {
  assertObject(value, 'Appliance manifest');
  const presentIdentity = BUILD_IDENTITY_MANIFEST_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const requiredIdentity = requiresBuildIdentity(value.version) || presentIdentity.length > 0
    ? BUILD_IDENTITY_MANIFEST_FIELDS
    : [];
  const expected = new Set([...MANIFEST_FIELDS, ...presentIdentity]);
  const missing = [...MANIFEST_FIELDS, ...requiredIdentity]
    .filter((field) => !Object.prototype.hasOwnProperty.call(value, field));
  if (missing.length > 0) {
    throw new TypeError(`Appliance manifest is missing required field(s): ${missing.join(', ')}`);
  }
  const unknown = Object.keys(value).filter((field) => !expected.has(field));
  if (unknown.length > 0) {
    throw new TypeError(`Appliance manifest contains unknown field(s): ${unknown.join(', ')}`);
  }
}

function requirePositiveInteger(value, label) {
  if (!Number.isSafeInteger(value) || value <= 0) throw new TypeError(`${label} must be a positive integer`);
  return value;
}

function requirePattern(value, pattern, label) {
  if (typeof value !== 'string' || !pattern.test(value)) throw new TypeError(`${label} has an invalid format`);
  return value;
}

function validateUtcDateTime(value, label) {
  const match = typeof value === 'string'
    && /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,3}))?Z$/.exec(value);
  if (!match) throw new TypeError(`${label} must be a UTC RFC 3339 timestamp`);
  const [, year, month, day, hour, minute, second, fraction = '0'] = match;
  const milliseconds = Number(fraction.padEnd(3, '0'));
  const date = new Date(Date.UTC(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute), Number(second), milliseconds));
  if (date.getUTCFullYear() !== Number(year)
      || date.getUTCMonth() !== Number(month) - 1
      || date.getUTCDate() !== Number(day)
      || date.getUTCHours() !== Number(hour)
      || date.getUTCMinutes() !== Number(minute)
      || date.getUTCSeconds() !== Number(second)) {
    throw new TypeError(`${label} must be a valid UTC RFC 3339 timestamp`);
  }
  return value;
}

function validateUbuntuSnapshot(value) {
  const match = typeof value === 'string'
    && /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z$/.exec(value);
  if (!match) throw new TypeError('inputs.ubuntuSnapshot must use YYYYMMDDTHHMMSSZ');
  const [, year, month, day, hour, minute, second] = match;
  const date = new Date(Date.UTC(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute), Number(second)));
  if (date.getUTCFullYear() !== Number(year)
      || date.getUTCMonth() !== Number(month) - 1
      || date.getUTCDate() !== Number(day)
      || date.getUTCHours() !== Number(hour)
      || date.getUTCMinutes() !== Number(minute)
      || date.getUTCSeconds() !== Number(second)) {
    throw new TypeError('inputs.ubuntuSnapshot is not a valid UTC snapshot timestamp');
  }
  return value;
}

function validateArtifact(value, label = 'artifact', options = {}) {
  const strict = options.strict === true;
  if (!value || typeof value !== 'object') throw new TypeError(`${label} artifact is required`);
  if (strict) assertExactFields(value, ARTIFACT_FIELDS, `${label} artifact`);
  if (typeof value.url !== 'string') throw new TypeError(`${label}.url is required`);
  let parsed;
  try { parsed = new URL(value.url); } catch { throw new TypeError(`${label}.url must be an absolute URL`); }
  if (!['https:', 'file:'].includes(parsed.protocol)) throw new TypeError(`${label}.url must use HTTPS or file:`);
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new TypeError(`${label}.url must not contain credentials, query parameters, or fragments`);
  }
  const rawSha256 = String(value.sha256 || '');
  const sha256 = strict ? rawSha256 : rawSha256.toLowerCase();
  if (!SHA256_PATTERN.test(sha256)) throw new TypeError(`${label}.sha256 must be a 64-character lowercase SHA-256`);
  const size = value.size === undefined ? undefined : Number(value.size);
  if (size !== undefined && (!Number.isSafeInteger(size) || size <= 0)) throw new TypeError(`${label}.size must be a positive integer`);
  const uncompressedSize = value.uncompressedSize === undefined ? undefined : Number(value.uncompressedSize);
  if (uncompressedSize !== undefined && (!Number.isSafeInteger(uncompressedSize) || uncompressedSize <= 0)) {
    throw new TypeError(`${label}.uncompressedSize must be a positive integer`);
  }
  if (strict) {
    if (typeof value.file !== 'string' || !/^[A-Za-z0-9._-]+$/.test(value.file) || ['.', '..'].includes(value.file)) {
      throw new TypeError(`${label}.file must be a safe basename`);
    }
    const compressedBytes = requirePositiveInteger(value.compressedBytes, `${label}.compressedBytes`);
    const uncompressedBytes = requirePositiveInteger(value.uncompressedBytes, `${label}.uncompressedBytes`);
    if (size !== compressedBytes) throw new TypeError(`${label}.size must equal ${label}.compressedBytes`);
    if (uncompressedSize !== uncompressedBytes) {
      throw new TypeError(`${label}.uncompressedSize must equal ${label}.uncompressedBytes`);
    }
    return {
      file: value.file,
      url: parsed.toString(),
      sha256,
      size,
      uncompressedSize,
      compressedBytes,
      uncompressedBytes,
    };
  }
  return { url: parsed.toString(), sha256, ...(size ? { size } : {}), ...(uncompressedSize ? { uncompressedSize } : {}) };
}

function requiresHttpMessageAbi(version) {
  return versionAtLeast(version, 1, 2, 8);
}

function requiresStoreErrorCodeAbi(version) {
  return versionAtLeast(version, 1, 2, 10);
}

function requiresBuiltPinProvenance(version) {
  return versionAtLeast(version, 1, 2, 12);
}

function requiresBuildIdentity(version) {
  return versionAtLeast(version, 1, 2, 13);
}

function requiresAndroidCABundleIdentity(version) {
  return versionAtLeast(version, 1, 2, 15);
}

function requiresStandaloneNetworkPreflightIdentity(version) {
  return versionAtLeast(version, 1, 3, 5);
}

function versionAtLeast(version, major, minor, patch) {
  const match = /^(\d+)\.(\d+)\.(\d+)/.exec(String(version || ''));
  if (!match) return false;
  const actual = [Number(match[1]), Number(match[2]), Number(match[3])];
  const required = [major, minor, patch];
  for (let index = 0; index < required.length; index += 1) {
    if (actual[index] !== required[index]) return actual[index] > required[index];
  }
  return true;
}

function canonicalJson(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) throw new TypeError('Canonical build identity only accepts safe integers');
    return String(value);
  }
  if (Array.isArray(value)) return `[${value.map((item) => canonicalJson(item)).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) =>
      `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(',')}}`;
  }
  throw new TypeError('Canonical build identity contains an unsupported value');
}

function computeApplianceBuildId(value) {
  const identity = {
    schemaVersion: 1,
    productVersion: value.productVersion,
    architecture: value.architecture,
    sourceDateEpoch: value.sourceDateEpoch,
    inputs: value.inputs,
    releasePolicy: value.releasePolicy,
  };
  return crypto.createHash('sha256').update(canonicalJson(identity), 'utf8').digest('hex');
}

function validateInputs(value, version) {
  assertObject(value, 'inputs');
  const presentHttpFields = HTTP_MESSAGE_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const presentStoreErrorCodeFields = STORE_ERROR_CODE_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const presentBuiltPinFields = BUILT_PIN_PROVENANCE_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const presentNetworkPreflightFields = NETWORK_PREFLIGHT_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const presentStandaloneNetworkPreflightFields = STANDALONE_NETWORK_PREFLIGHT_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const presentRendererIdentityFields = RENDERER_IDENTITY_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const presentAndroidCABundleFields = ANDROID_CA_BUNDLE_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  const presentOptionalFields = [
    ...presentHttpFields,
    ...presentStoreErrorCodeFields,
    ...presentBuiltPinFields,
    ...presentNetworkPreflightFields,
    ...presentStandaloneNetworkPreflightFields,
    ...presentRendererIdentityFields,
    ...presentAndroidCABundleFields,
  ];
  const expectedFields = [...BASE_INPUT_FIELDS, ...presentOptionalFields];
  const missing = BASE_INPUT_FIELDS.filter((field) => !Object.prototype.hasOwnProperty.call(value, field));
  if (missing.length > 0) throw new TypeError(`inputs is missing required field(s): ${missing.join(', ')}`);
  const unknown = Object.keys(value).filter((field) => !expectedFields.includes(field));
  if (unknown.length > 0) throw new TypeError(`inputs contains unknown field(s): ${unknown.join(', ')}`);
  if (requiresHttpMessageAbi(version)) {
    const missingHttp = HTTP_MESSAGE_INPUT_FIELDS.filter((field) => !Object.prototype.hasOwnProperty.call(value, field));
    if (missingHttp.length > 0) throw new TypeError(`inputs is missing required field(s): ${missingHttp.join(', ')}`);
  }
  if (requiresStoreErrorCodeAbi(version)) {
    const missingStoreErrorCode = STORE_ERROR_CODE_INPUT_FIELDS.filter((field) => !Object.prototype.hasOwnProperty.call(value, field));
    if (missingStoreErrorCode.length > 0) {
      throw new TypeError(`inputs is missing required field(s): ${missingStoreErrorCode.join(', ')}`);
    }
  }
  if (requiresBuiltPinProvenance(version)) {
    const missingBuiltPin = BUILT_PIN_PROVENANCE_INPUT_FIELDS.filter((field) =>
      !Object.prototype.hasOwnProperty.call(value, field));
    if (missingBuiltPin.length > 0) {
      throw new TypeError(`inputs is missing required field(s): ${missingBuiltPin.join(', ')}`);
    }
  }
  if (requiresBuildIdentity(version)) {
    const missingNetworkPreflight = NETWORK_PREFLIGHT_INPUT_FIELDS.filter((field) =>
      !Object.prototype.hasOwnProperty.call(value, field));
    if (missingNetworkPreflight.length > 0) {
      throw new TypeError(`inputs is missing required field(s): ${missingNetworkPreflight.join(', ')}`);
    }
    const missingRendererIdentity = RENDERER_IDENTITY_INPUT_FIELDS.filter((field) =>
      !Object.prototype.hasOwnProperty.call(value, field));
    if (missingRendererIdentity.length > 0) {
      throw new TypeError(`inputs is missing required field(s): ${missingRendererIdentity.join(', ')}`);
    }
  }
  if (requiresAndroidCABundleIdentity(version)) {
    const missingAndroidCABundle = ANDROID_CA_BUNDLE_INPUT_FIELDS.filter((field) =>
      !Object.prototype.hasOwnProperty.call(value, field));
    if (missingAndroidCABundle.length > 0) {
      throw new TypeError(`inputs is missing required field(s): ${missingAndroidCABundle.join(', ')}`);
    }
  }
  if (requiresStandaloneNetworkPreflightIdentity(version)) {
    const missingStandaloneNetworkPreflight = STANDALONE_NETWORK_PREFLIGHT_INPUT_FIELDS.filter((field) =>
      !Object.prototype.hasOwnProperty.call(value, field));
    if (missingStandaloneNetworkPreflight.length > 0) {
      throw new TypeError(`inputs is missing required field(s): ${missingStandaloneNetworkPreflight.join(', ')}`);
    }
  }
  const normalized = {};
  for (const field of SHA256_INPUT_FIELDS.filter((field) =>
    Object.prototype.hasOwnProperty.call(value, field))) {
    normalized[field] = requirePattern(value[field], SHA256_PATTERN, `inputs.${field}`);
  }
  normalized.goBuilderImage = requirePattern(value.goBuilderImage, IMAGE_DIGEST_PATTERN, 'inputs.goBuilderImage');
  normalized.ubuntuRuntimeImage = requirePattern(value.ubuntuRuntimeImage, IMAGE_DIGEST_PATTERN, 'inputs.ubuntuRuntimeImage');
  normalized.ubuntuSnapshot = validateUbuntuSnapshot(value.ubuntuSnapshot);
  for (const field of COMMIT_INPUT_FIELDS) {
    normalized[field] = requirePattern(value[field], COMMIT_PATTERN, `inputs.${field}`);
  }
  if (Object.prototype.hasOwnProperty.call(value, 'androidCaBundlePath')) {
    if (value.androidCaBundlePath !== '/usr/local/android/cert.pem') {
      throw new TypeError('inputs.androidCaBundlePath is invalid');
    }
    normalized.androidCaBundlePath = value.androidCaBundlePath;
  }
  if (Object.prototype.hasOwnProperty.call(value, 'wrapperBuiltFromPatchSha256')
      && normalized.wrapperBuiltFromPatchSha256 !== normalized.wrapperPatchSha256) {
    throw new TypeError('inputs.wrapperBuiltFromPatchSha256 must match inputs.wrapperPatchSha256');
  }
  normalized.androidNdkVersion = requirePattern(value.androidNdkVersion, /^r[1-9][0-9]*[a-z]$/, 'inputs.androidNdkVersion');
  normalized.cmakeVersion = requirePattern(value.cmakeVersion, /^[0-9]+\.[0-9]+\.[0-9]+$/, 'inputs.cmakeVersion');
  normalized.urlRequestAllocationBytes = requirePositiveInteger(
    value.urlRequestAllocationBytes,
    'inputs.urlRequestAllocationBytes',
  );
  normalized.urlRequestObjectOffset = requirePositiveInteger(
    value.urlRequestObjectOffset,
    'inputs.urlRequestObjectOffset',
  );
  normalized.urlRequestObjectBytes = requirePositiveInteger(
    value.urlRequestObjectBytes,
    'inputs.urlRequestObjectBytes',
  );
  normalized.urlRequestObjectAlignment = requirePositiveInteger(
    value.urlRequestObjectAlignment,
    'inputs.urlRequestObjectAlignment',
  );
  const expectedAbi = [
    ['urlRequestAllocationBytes', URL_REQUEST_ABI.allocationBytes],
    ['urlRequestObjectOffset', URL_REQUEST_ABI.objectOffset],
    ['urlRequestObjectBytes', URL_REQUEST_ABI.objectBytes],
    ['urlRequestObjectAlignment', URL_REQUEST_ABI.objectAlignment],
  ];
  for (const [field, expected] of expectedAbi) {
    if (normalized[field] !== expected) {
      throw new TypeError(`inputs.${field} does not match the locked URLRequest ABI`);
    }
  }
  if (normalized.urlRequestAllocationBytes
        !== normalized.urlRequestObjectOffset + normalized.urlRequestObjectBytes) {
    throw new TypeError('inputs URLRequest allocation does not match the locked object envelope');
  }
  if (normalized.urlRequestObjectOffset % normalized.urlRequestObjectAlignment !== 0
      || normalized.urlRequestObjectBytes % normalized.urlRequestObjectAlignment !== 0) {
    throw new TypeError('inputs URLRequest object envelope does not satisfy the locked alignment');
  }
  if (presentHttpFields.length > 0) {
    const http = {};
    for (const field of HTTP_MESSAGE_INPUT_FIELDS.filter((field) => field !== 'androidAppMusicBinarySha256')) {
      if (Object.prototype.hasOwnProperty.call(value, field)) {
        http[field] = requirePositiveInteger(value[field], `inputs.${field}`);
      }
    }
    if (Object.prototype.hasOwnProperty.call(value, 'androidAppMusicBinarySha256')) {
      // The digest is validated above with the other SHA-256 inputs.
      http.androidAppMusicBinarySha256 = normalized.androidAppMusicBinarySha256;
    }
    const expectedHttp = [
      ['httpMessageAllocationBytes', HTTP_MESSAGE_ABI.allocationBytes],
      ['httpMessageObjectOffset', HTTP_MESSAGE_ABI.objectOffset],
      ['httpMessageObjectBytes', HTTP_MESSAGE_ABI.objectBytes],
      ['httpMessageStorageAlignment', HTTP_MESSAGE_ABI.storageAlignment],
      ['httpMessageObjectAlignment', HTTP_MESSAGE_ABI.objectAlignment],
    ];
    for (const [field, expected] of expectedHttp) {
      if (http[field] !== undefined && http[field] !== expected) {
        throw new TypeError(`inputs.${field} does not match the locked HTTPMessage ABI`);
      }
    }
    if (http.httpMessageAllocationBytes !== undefined
        && http.httpMessageObjectOffset !== undefined
        && http.httpMessageObjectBytes !== undefined
        && http.httpMessageAllocationBytes
          !== http.httpMessageObjectOffset + http.httpMessageObjectBytes) {
      throw new TypeError('inputs HTTPMessage allocation does not match the locked object envelope');
    }
    if (http.httpMessageObjectOffset !== undefined && http.httpMessageObjectAlignment !== undefined
        && http.httpMessageObjectOffset % http.httpMessageObjectAlignment !== 0) {
      throw new TypeError('inputs HTTPMessage object offset does not satisfy the locked alignment');
    }
    if (http.httpMessageObjectBytes !== undefined && http.httpMessageObjectAlignment !== undefined
        && http.httpMessageObjectBytes % http.httpMessageObjectAlignment !== 0) {
      throw new TypeError('inputs HTTPMessage object bytes do not satisfy the locked alignment');
    }
    for (const field of HTTP_MESSAGE_INPUT_FIELDS) {
      if (Object.prototype.hasOwnProperty.call(value, field)) normalized[field] = http[field];
    }
  }
  if (presentStoreErrorCodeFields.length > 0) {
    const storeErrorCode = {};
    for (const field of STORE_ERROR_CODE_INPUT_FIELDS) {
      if (Object.prototype.hasOwnProperty.call(value, field)) {
        storeErrorCode[field] = requirePositiveInteger(value[field], `inputs.${field}`);
      }
    }
    const expectedStoreErrorCode = [
      ['storeErrorCodeObjectOffset', STORE_ERROR_CODE_ABI.objectOffset],
      ['storeErrorCodeValueBytes', STORE_ERROR_CODE_ABI.valueBytes],
    ];
    for (const [field, expected] of expectedStoreErrorCode) {
      if (storeErrorCode[field] !== undefined && storeErrorCode[field] !== expected) {
        throw new TypeError(`inputs.${field} does not match the locked StoreErrorCode ABI`);
      }
    }
    for (const field of STORE_ERROR_CODE_INPUT_FIELDS) {
      if (Object.prototype.hasOwnProperty.call(value, field)) normalized[field] = storeErrorCode[field];
    }
  }
  if (!PROVENANCE_STATUSES.has(value.wrapperProvenanceStatus)) {
    throw new TypeError('inputs.wrapperProvenanceStatus is not recognized');
  }
  normalized.wrapperProvenanceStatus = value.wrapperProvenanceStatus;
  return Object.fromEntries([...BASE_INPUT_FIELDS, ...presentOptionalFields]
    .map((field) => [field, normalized[field]]));
}

function validateReleasePolicy(value) {
  assertExactFields(value, RELEASE_POLICY_FIELDS, 'releasePolicy');
  if (typeof value.publicReleaseAllowed !== 'boolean') {
    throw new TypeError('releasePolicy.publicReleaseAllowed must be a boolean');
  }
  if (!Array.isArray(value.blockers) || value.blockers.length === 0
      || value.blockers.some((blocker) => typeof blocker !== 'string' || blocker.trim() === '')) {
    throw new TypeError('releasePolicy.blockers must be a non-empty array of non-empty strings');
  }
  return { publicReleaseAllowed: value.publicReleaseAllowed, blockers: [...value.blockers] };
}

function validateApplianceManifest(value) {
  assertManifestFields(value);
  if (value.schemaVersion !== 1) throw new TypeError('Unsupported appliance manifest schemaVersion');
  if (!VERSION_PATTERN.test(String(value.version || ''))) throw new TypeError('Appliance version must be a semantic v1 version');
  if (value.architecture !== 'amd64') throw new TypeError('Appliance architecture must be amd64');
  if (value.distroName !== DISTRO_NAME) throw new TypeError(`Appliance distroName must be ${DISTRO_NAME}`);
  if (!CHANNELS.has(value.channel)) throw new TypeError('Appliance channel is not recognized');
  const builtAt = validateUtcDateTime(value.builtAt, 'Appliance builtAt');
  const rootfs = validateArtifact(value.rootfs, 'rootfs', { strict: true });
  const runtime = validateArtifact(value.runtime, 'runtime', { strict: true });
  const inputs = validateInputs(value.inputs, value.version);
  const releasePolicy = validateReleasePolicy(value.releasePolicy);
  let buildIdentity = {};
  const hasBuildIdentity = BUILD_IDENTITY_MANIFEST_FIELDS.some((field) =>
    Object.prototype.hasOwnProperty.call(value, field));
  if (hasBuildIdentity) {
    const productVersion = requirePattern(
      value.productVersion,
      VERSION_PATTERN,
      'Appliance productVersion',
    );
    if (productVersion !== value.version) {
      throw new TypeError('Appliance productVersion must match version');
    }
    const sourceDateEpoch = requirePositiveInteger(
      value.sourceDateEpoch,
      'Appliance sourceDateEpoch',
    );
    if (Date.parse(builtAt) !== sourceDateEpoch * 1000) {
      throw new TypeError('Appliance builtAt must match sourceDateEpoch');
    }
    const applianceBuildId = requirePattern(
      value.applianceBuildId,
      SHA256_PATTERN,
      'Appliance applianceBuildId',
    );
    const expectedBuildId = computeApplianceBuildId({
      productVersion,
      architecture: 'amd64',
      sourceDateEpoch,
      inputs,
      releasePolicy,
    });
    if (applianceBuildId !== expectedBuildId) {
      throw new TypeError('Appliance applianceBuildId does not match canonical build identity');
    }
    buildIdentity = { productVersion, sourceDateEpoch, applianceBuildId };
  }
  if (releasePolicy.publicReleaseAllowed && inputs.wrapperProvenanceStatus !== 'verified-source-build') {
    throw new TypeError('Public release requires verified Wrapper provenance');
  }
  if (value.channel === 'public') {
    if (!releasePolicy.publicReleaseAllowed) throw new TypeError('Public channel is blocked by releasePolicy');
    for (const [label, artifact] of [['rootfs', rootfs], ['runtime', runtime]]) {
      if (new URL(artifact.url).protocol !== 'https:') throw new TypeError(`Public channel ${label}.url must use HTTPS`);
    }
  }
  return {
    schemaVersion: 1,
    version: value.version,
    ...buildIdentity,
    architecture: 'amd64',
    distroName: DISTRO_NAME,
    channel: value.channel,
    builtAt,
    rootfs,
    runtime,
    inputs,
    releasePolicy,
  };
}

async function sha256File(filename) {
  const hash = crypto.createHash('sha256');
  await pipeline(fs.createReadStream(filename), hash);
  return hash.digest('hex');
}

async function verifyArtifactFile(artifact, filename) {
  const normalized = validateArtifact(artifact, 'artifact');
  const info = await fsp.lstat(filename);
  if (!info.isFile() || info.isSymbolicLink()) throw new Error('Artifact is not a regular file');
  if (normalized.size && info.size !== normalized.size) {
    throw new Error(`Artifact size mismatch: expected ${normalized.size}, received ${info.size}`);
  }
  const digest = await sha256File(filename);
  if (digest !== normalized.sha256) {
    throw new Error(`Artifact SHA-256 mismatch: expected ${normalized.sha256}, received ${digest}`);
  }
  return filename;
}

function downloadHttps(url, destination, options = {}, redirects = 0) {
  return new Promise((resolve, reject) => {
    if (redirects > 5) {
      reject(new Error('Too many artifact download redirects'));
      return;
    }
    const request = https.get(url, { timeout: options.timeoutMs || 180_000 }, (response) => {
      if ([301, 302, 303, 307, 308].includes(response.statusCode) && response.headers.location) {
        response.resume();
        const redirected = new URL(response.headers.location, url);
        if (redirected.protocol !== 'https:' || redirected.username || redirected.password || redirected.search || redirected.hash) {
          reject(new Error('Artifact redirect must use credential-free HTTPS without query parameters'));
          return;
        }
        downloadHttps(redirected, destination, options, redirects + 1).then(resolve, reject);
        return;
      }
      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`Artifact download returned HTTP ${response.statusCode}`));
        return;
      }
      let received = 0;
      response.on('data', (chunk) => {
        received += chunk.length;
        if (received > (options.maxBytes || 8 * 1024 * 1024 * 1024)) request.destroy(new Error('Artifact exceeds the maximum allowed size'));
      });
      pipeline(response, fs.createWriteStream(destination, { flags: 'wx', mode: 0o600 })).then(resolve, reject);
    });
    request.on('timeout', () => request.destroy(new Error('Artifact download timed out')));
    request.on('error', reject);
  });
}

async function defaultDownload(url, destination, options = {}) {
  const parsed = new URL(url);
  if (parsed.protocol === 'file:') {
    await pipeline(fs.createReadStream(fileURLToPath(parsed)), fs.createWriteStream(destination, { flags: 'wx', mode: 0o600 }));
    return;
  }
  await downloadHttps(parsed, destination, options);
}

async function downloadAndVerifyArtifact(artifact, destination, options = {}) {
  const normalized = validateArtifact(artifact, 'artifact');
  await fsp.mkdir(path.dirname(destination), { recursive: true });

  try {
    const existing = await fsp.stat(destination);
    if ((!normalized.size || existing.size === normalized.size) && await sha256File(destination) === normalized.sha256) return destination;
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }

  const temporary = `${destination}.${process.pid}.${crypto.randomBytes(6).toString('hex')}.part`;
  try {
    await (options.download || defaultDownload)(normalized.url, temporary, {
      timeoutMs: options.timeoutMs,
      maxBytes: normalized.size || options.maxBytes,
    });
    const stat = await fsp.stat(temporary);
    if (normalized.size && stat.size !== normalized.size) throw new Error(`Artifact size mismatch: expected ${normalized.size}, received ${stat.size}`);
    const digest = await sha256File(temporary);
    if (digest !== normalized.sha256) throw new Error(`Artifact SHA-256 mismatch: expected ${normalized.sha256}, received ${digest}`);

    try {
      await fsp.rename(temporary, destination);
    } catch (error) {
      if (!['EEXIST', 'EPERM'].includes(error.code)) throw error;
      const existingDigest = await sha256File(destination).catch(() => '');
      if (existingDigest !== normalized.sha256) throw new Error('Verified artifact cannot replace an existing different file');
      await fsp.unlink(temporary);
    }
    return destination;
  } catch (error) {
    await fsp.unlink(temporary).catch(() => {});
    throw error;
  }
}

function artifactCacheName(kind, version, artifact) {
  if (!['rootfs', 'runtime'].includes(kind)) throw new TypeError('Unknown appliance artifact kind');
  const normalized = validateArtifact(artifact, kind);
  return `applemusic-downloader-wsl-${kind}-${version}-amd64-${normalized.sha256.slice(0, 16)}.tar.gz`;
}

module.exports = {
  DISTRO_NAME,
  artifactCacheName,
  computeApplianceBuildId,
  defaultDownload,
  downloadAndVerifyArtifact,
  sha256File,
  verifyArtifactFile,
  validateApplianceManifest,
  validateArtifact,
};

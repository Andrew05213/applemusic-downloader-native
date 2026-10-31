'use strict';

const { computeApplianceBuildId } = require('../../../electron/appliance-manifest');

const ROOTFS_NAME = 'applemusic-downloader-wsl-rootfs-1.2.0-amd64.tar.gz';
const RUNTIME_NAME = 'applemusic-downloader-runtime-1.2.0-amd64.tar.gz';

function createArtifact(kind, overrides = {}) {
  const isRuntime = kind === 'runtime';
  const file = isRuntime ? RUNTIME_NAME : ROOTFS_NAME;
  return {
    file,
    url: `https://downloads.example.test/${file}`,
    sha256: (isRuntime ? 'b' : 'a').repeat(64),
    size: isRuntime ? 2048 : 1024,
    uncompressedSize: isRuntime ? 8192 : 4096,
    compressedBytes: isRuntime ? 2048 : 1024,
    uncompressedBytes: isRuntime ? 8192 : 4096,
    ...overrides,
  };
}

function createInputs(overrides = {}) {
  return {
    inputLockSha256: '0'.repeat(64),
    versionsLockSha256: '1'.repeat(64),
    packagesLockSha256: '2'.repeat(64),
    packagesResolvedLockSha256: '3'.repeat(64),
    releasePolicySha256: '4'.repeat(64),
    backendTreeSha256: '5'.repeat(64),
    webUiTreeSha256: '6'.repeat(64),
    wrapperTreeSha256: '7'.repeat(64),
    wrapperBinarySha256: '8'.repeat(64),
    androidMainSha256: '9'.repeat(64),
    noticeSha256: 'a'.repeat(64),
    wrapperPatchSha256: 'b'.repeat(64),
    goBuilderImage: `golang:1.23.1-bookworm@sha256:${'c'.repeat(64)}`,
    ubuntuRuntimeImage: `ubuntu:22.04@sha256:${'d'.repeat(64)}`,
    ubuntuSnapshot: '20260728T000000Z',
    wrapperCommit: 'e'.repeat(40),
    cjsonCommit: 'f'.repeat(40),
    dobbyCommit: '0'.repeat(40),
    androidNdkVersion: 'r23b',
    androidNdkSha256: '1'.repeat(64),
    cmakeVersion: '3.25.3',
    cmakeLinuxAmd64Sha256: '2'.repeat(64),
    mediaplatformBinarySha256: '3'.repeat(64),
    storeservicescoreBinarySha256: '5'.repeat(64),
    androidAppMusicBinarySha256: '6'.repeat(64),
    urlRequestAllocationBytes: 592,
    urlRequestObjectOffset: 32,
    urlRequestObjectBytes: 560,
    urlRequestObjectAlignment: 16,
    httpMessageAllocationBytes: 0x1e0,
    httpMessageObjectOffset: 0x18,
    httpMessageObjectBytes: 0x1c8,
    httpMessageStorageAlignment: 16,
    httpMessageObjectAlignment: 8,
    storeErrorCodeObjectOffset: 0x20,
    storeErrorCodeValueBytes: 4,
    wrapperBuiltSha256: '3'.repeat(64),
    wrapperBuiltFromPatchSha256: 'b'.repeat(64),
    androidMainBuiltSha256: '4'.repeat(64),
    androidNetworkPreflightBuiltSha256: '5'.repeat(64),
    networkPreflightSourceSha256: 'c'.repeat(64),
    rendererSha256: 'd'.repeat(64),
    androidCaBundlePath: '/usr/local/android/cert.pem',
    androidCaBundleSha256: 'e'.repeat(64),
    wrapperProvenanceStatus: 'mismatched-unverified-local-poc-only',
    ...overrides,
  };
}

function createManifest(overrides = {}) {
  const manifest = {
    schemaVersion: 1,
    version: '1.2.0',
    architecture: 'amd64',
    distroName: 'AppleMusicDownloader',
    channel: 'private-ci',
    builtAt: '2026-07-29T00:00:00Z',
    rootfs: createArtifact('rootfs'),
    runtime: createArtifact('runtime'),
    inputs: createInputs(),
    releasePolicy: {
      publicReleaseAllowed: false,
      blockers: ['Synthetic test manifest is not releasable.'],
    },
    ...overrides,
  };
  const match = /^(\d+)\.(\d+)\.(\d+)/.exec(manifest.version);
  const modernIdentity = match && (Number(match[1]) > 1
    || (Number(match[1]) === 1 && (Number(match[2]) > 2
      || (Number(match[2]) === 2 && Number(match[3]) >= 13))));
  if (modernIdentity) {
    if (!Object.prototype.hasOwnProperty.call(overrides, 'productVersion')) {
      manifest.productVersion = manifest.version;
    }
    if (!Object.prototype.hasOwnProperty.call(overrides, 'sourceDateEpoch')) {
      manifest.sourceDateEpoch = Math.floor(Date.parse(manifest.builtAt) / 1000);
    }
    if (!Object.prototype.hasOwnProperty.call(overrides, 'applianceBuildId')) {
      manifest.applianceBuildId = computeApplianceBuildId(manifest);
    }
  }
  return manifest;
}

module.exports = {
  ROOTFS_NAME,
  RUNTIME_NAME,
  createArtifact,
  createInputs,
  createManifest,
};

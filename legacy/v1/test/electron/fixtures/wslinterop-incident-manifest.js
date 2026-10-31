'use strict';

const { computeApplianceBuildId } = require('../../../electron/appliance-manifest');
const { INCIDENT_SOURCE } = require('../../../electron/wslinterop-recovery');

// The recovery gate is tied to this released 1.2.13 input identity.  Keep the
// fixture self-contained so tests do not depend on ignored local artifacts.
const INCIDENT_INPUTS = Object.freeze({
  androidAppMusicBinarySha256: '0a575431868e40ddb29e9e0bac7a4993d6e05971b45403cabe358c081b54a36e',
  androidMainBuiltSha256: INCIDENT_SOURCE.androidMainBuiltSha256,
  androidMainSha256: 'a87d3d86395871c2c7fac1d737ae4a5d8c6b7bc6dd72a3957f645951c2bcaa2b',
  androidNdkSha256: 'c6e97f9c8cfe5b7be0a9e6c15af8e7a179475b7ded23e2d1c1fa0945d6fb4382',
  androidNdkVersion: 'r23b',
  backendTreeSha256: '9087ab4aee672e2da137dacac1dd8cee58cb1bad780c16f1e48a33d7d12c677d',
  cjsonCommit: 'c859b25da02955fef659d658b8f324b5cde87be3',
  cmakeLinuxAmd64Sha256: 'd4d2ba83301b215857d3b6590cd4434a414fa151c5807693abe587bd6c03581e',
  cmakeVersion: '3.25.3',
  dobbyCommit: '9d271803881f467aabfc24593c7dcaeb710fe861',
  goBuilderImage: 'golang:1.23.1-bookworm@sha256:31817d38d90894d73ce3fe3b7f01c5f213ac7e9681bf462d4ad65afa8e902c7e',
  httpMessageAllocationBytes: 480,
  httpMessageObjectAlignment: 8,
  httpMessageObjectBytes: 456,
  httpMessageObjectOffset: 24,
  httpMessageStorageAlignment: 16,
  inputLockSha256: 'eb7faad3da51925d2323d185e9f69d30fde47952d8d3b354ca319fd32d0cd85a',
  mediaplatformBinarySha256: '96ec8f0a9bdd597534fb876d1945f153077d688826dcc7673807e113280d5203',
  networkPreflightSourceSha256: 'dff085d8a896ad6458a97c4bfe097070d63ad2e4cc7ac2cb19a63d3e4b5fe308',
  noticeSha256: '13118d64053e70fddb4f0c4bdd6f241d9814e2692f1ef312583fe00d363b2e6b',
  packagesLockSha256: 'dfdaf5880f072e800a23bccb981ba7de76c649caa66bbcad7c98b1dc815b13fd',
  packagesResolvedLockSha256: '14d89d98b6019c5fe773303632de7bea84bf60f4c88900b3cdf4cff24ed908a1',
  releasePolicySha256: '1307a66646b7070012b82627d4b5c7a9285f42f0dfd3c8395a85642605d0825c',
  rendererSha256: INCIDENT_SOURCE.rendererSha256,
  storeErrorCodeObjectOffset: 32,
  storeErrorCodeValueBytes: 4,
  storeservicescoreBinarySha256: '6238261363d7943f1a0a27116b52544831f3e295e8533a634641fe5b09d92c7e',
  ubuntuRuntimeImage: 'ubuntu:22.04@sha256:0d779ea97881505f5ef0039336ee85edba27519bdba968c284c86ee066a973c8',
  ubuntuSnapshot: '20260728T000000Z',
  urlRequestAllocationBytes: 592,
  urlRequestObjectAlignment: 16,
  urlRequestObjectBytes: 560,
  urlRequestObjectOffset: 32,
  versionsLockSha256: '7fc08b53b498e6e55b6bc1b345168bb0f53a7f8d95febcefb456198e32d7146a',
  webUiTreeSha256: 'a759964b6f20bb11483c9348c33873ed7acdc203708a9165cfd3f092aaf69084',
  wrapperBinarySha256: 'cfe72b434e4a30cbcb2a0e5dd5b0f9af139d0fd724a91cad2a746ab92394decb',
  wrapperBuiltFromPatchSha256: 'a2d8213e016a7e9b9d0ed7c44a09ccf7393535d98e5019eee80c1857acd0a2e2',
  wrapperBuiltSha256: 'f5e8e999cc4adbb5e3911e6067ebe8ccfc4b4f79f559d65c849dbbd396e508fa',
  wrapperCommit: '0d4823df8097a88fd62dc5f8112dc4a5c733b65a',
  wrapperPatchSha256: 'a2d8213e016a7e9b9d0ed7c44a09ccf7393535d98e5019eee80c1857acd0a2e2',
  wrapperProvenanceStatus: 'mismatched-unverified-local-poc-only',
  wrapperTreeSha256: '3a62ac49bd9cd9bbf0824e4e475f7429283ea3056d82f1cd11143a8aac60203e',
});

const INCIDENT_RELEASE_POLICY = Object.freeze({
  publicReleaseAllowed: false,
  blockers: [
    'The legacy Wrapper executables are provenance-mismatched inputs and must never replace the reproducibly rebuilt outputs.',
    'No verified Wrapper redistribution license is recorded for the source-built outputs.',
    'Android/Apple runtime material and runv3 CDM material lack verified redistribution authorization.',
  ],
});

function createInteropIncidentManifest() {
  const manifest = {
    schemaVersion: 1,
    version: INCIDENT_SOURCE.productVersion,
    productVersion: INCIDENT_SOURCE.productVersion,
    sourceDateEpoch: 1786233600,
    applianceBuildId: INCIDENT_SOURCE.applianceBuildId,
    architecture: 'amd64',
    distroName: 'AppleMusicDownloader',
    channel: 'local-poc',
    builtAt: '2026-08-09T00:00:00Z',
    rootfs: {
      file: 'applemusic-downloader-wsl-rootfs-1.2.13-amd64.tar.gz',
      url: 'https://downloads.example.test/applemusic-downloader-wsl-rootfs-1.2.13-amd64.tar.gz',
      sha256: '9736f03d564a90d0361cbbeaa37c3547e4bcdbb722f0c27c6828b8e8fbc98f3e',
      size: 243864637,
      uncompressedSize: 661227520,
      compressedBytes: 243864637,
      uncompressedBytes: 661227520,
    },
    runtime: {
      file: 'applemusic-downloader-runtime-1.2.13-amd64.tar.gz',
      url: 'https://downloads.example.test/applemusic-downloader-runtime-1.2.13-amd64.tar.gz',
      sha256: INCIDENT_SOURCE.runtimeSha256,
      size: 59876212,
      uncompressedSize: 148746240,
      compressedBytes: 59876212,
      uncompressedBytes: 148746240,
    },
    inputs: { ...INCIDENT_INPUTS },
    releasePolicy: { ...INCIDENT_RELEASE_POLICY, blockers: [...INCIDENT_RELEASE_POLICY.blockers] },
  };
  if (computeApplianceBuildId(manifest) !== INCIDENT_SOURCE.applianceBuildId) {
    throw new Error('WSLInterop incident manifest fixture drifted from its locked build identity');
  }
  return manifest;
}

module.exports = { createInteropIncidentManifest };

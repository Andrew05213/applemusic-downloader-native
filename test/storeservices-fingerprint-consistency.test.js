'use strict';

// The StoreServicesCore fingerprint that gates the renderer's code-8 business
// message exists in three places: the reviewed input lock (authoritative), the
// Go manager constant that travels with the derived diagnostic, and the renderer
// constant that independently re-checks it. Nothing at runtime measures the
// actually-loaded libstoreservicescore.so from JavaScript or Go — that identity
// is enforced separately by the appliance Docker build (locked-library SHA gate)
// and the install-time runtime SHA. This test is the mechanical guard that the
// three compiled-in copies never drift apart: if a future change updates the
// locked library or the lock file without updating both code constants, this
// fails closed instead of letting the manager advertise a stale fingerprint and
// wrongly enable the code-8 mapping.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const repositoryRoot = path.join(__dirname, '..');

function readVersionsLockFingerprint() {
  const text = fs.readFileSync(
    path.join(repositoryRoot, 'legacy', 'v1', 'appliance', 'versions.lock'),
    'utf8',
  );
  const match = text.match(/^STORESERVICESCORE_BINARY_SHA256=([0-9a-f]{64})$/m);
  assert.ok(match, 'versions.lock must lock STORESERVICESCORE_BINARY_SHA256 as 64 hex chars');
  return match[1];
}

function readGoManagerFingerprint() {
  const text = fs.readFileSync(
    path.join(repositoryRoot, 'apple-music-downloader', 'wrapper_manager.go'),
    'utf8',
  );
  const match = text.match(/wrapperAuditedStoreServicesCoreSha256\s*=\s*"([0-9a-fA-F]{64})"/);
  assert.ok(match, 'wrapper_manager.go must define wrapperAuditedStoreServicesCoreSha256');
  return match[1].toLowerCase();
}

function readRendererFingerprint() {
  const text = fs.readFileSync(
    path.join(repositoryRoot, 'web', 'renderer.js'),
    'utf8',
  );
  const match = text.match(/STORESERVICES_AUDITED_SHA256\s*=\s*'([0-9a-fA-F]{64})'/);
  assert.ok(match, 'renderer.js must define STORESERVICES_AUDITED_SHA256');
  return match[1].toLowerCase();
}

test('the shared Go manager and Web renderer fingerprints match the archived Appliance lock', () => {
  const authoritative = readVersionsLockFingerprint();
  const goConstant = readGoManagerFingerprint();
  const rendererConstant = readRendererFingerprint();

  assert.equal(
    goConstant,
    authoritative,
    'wrapper_manager.go fingerprint drifted from legacy/v1/appliance/versions.lock; regenerate both together',
  );
  assert.equal(
    rendererConstant,
    authoritative,
    'Web renderer fingerprint drifted from legacy/v1/appliance/versions.lock; regenerate both together',
  );
});

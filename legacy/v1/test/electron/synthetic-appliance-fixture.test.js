'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const zlib = require('node:zlib');
const { fileURLToPath } = require('node:url');

const { validateApplianceManifest, verifyArtifactFile } = require('../../electron/appliance-manifest');
const { enforceManifestVersion, enforcePublicReleasePolicy } = require('../../electron/inject-appliance-manifest');
const packageManifest = require('../../electron/legacy-desktop-package.json');
const fixture = require('./fixtures/create-synthetic-appliance');

function generate(directory) {
  fixture.main(['--output', directory]);
  const metadata = JSON.parse(fs.readFileSync(path.join(directory, fixture.METADATA_NAME), 'utf8'));
  const archive = fs.readFileSync(path.join(directory, fixture.ROOTFS_NAME));
  const runtimeArchive = fs.readFileSync(path.join(directory, fixture.RUNTIME_NAME));
  const offline = JSON.parse(fs.readFileSync(path.join(directory, fixture.OFFLINE_MANIFEST_NAME), 'utf8'));
  const online = JSON.parse(fs.readFileSync(path.join(directory, fixture.ONLINE_MANIFEST_NAME), 'utf8'));
  return { archive, runtimeArchive, metadata, offline, online };
}

test('synthetic appliance fixture is deterministic, conspicuous and contains no runtime payload', () => {
  const first = generate(fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-synthetic-a-')));
  const second = generate(fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-synthetic-b-')));
  assert.deepEqual(first.archive, second.archive);
  assert.deepEqual(first.runtimeArchive, second.runtimeArchive);
  assert.notDeepEqual(first.archive, first.runtimeArchive);
  assert.equal(first.archive.readUInt32LE(4), 0, 'gzip mtime must be zero');
  assert.equal(first.runtimeArchive.readUInt32LE(4), 0, 'runtime gzip mtime must be zero');
  assert.equal(first.metadata.containsThirdPartyMaterials, false);
  assert.equal(first.metadata.runtimeUsable, false);
  assert.equal(first.metadata.releaseUploadAllowed, false);
  assert.match(first.metadata.archive, /CI-SYNTHETIC-NOT-FOR-RUNTIME/);
  assert.match(first.metadata.runtimeArchive, /CI-SYNTHETIC-NOT-FOR-RUNTIME/);

  for (const archive of [first.archive, first.runtimeArchive]) {
    const text = zlib.gunzipSync(archive).toString('utf8');
    assert.match(text, /CI-SYNTHETIC-NOT-FOR-RUNTIME/);
    for (const forbidden of ['wrapper-linux', 'Android', 'media-user-token', 'APPLE_PASSWORD', 'jobs.db']) {
      assert.equal(text.includes(forbidden), false, `fixture must not contain ${forbidden}`);
    }
  }
  assert.equal(crypto.createHash('sha256').update(first.archive).digest('hex'), first.metadata.sha256);
  assert.equal(crypto.createHash('sha256').update(first.runtimeArchive).digest('hex'), first.metadata.runtimeSha256);
});

test('synthetic online and offline manifests satisfy the current packaging contract and remain non-releasable', async () => {
  const generated = generate(fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-synthetic-manifest-')));
  const offline = validateApplianceManifest(generated.offline);
  const online = validateApplianceManifest(generated.online);
  assert.equal(new URL(offline.rootfs.url).protocol, 'file:');
  assert.equal(new URL(online.rootfs.url).protocol, 'https:');
  assert.equal(new URL(offline.runtime.url).protocol, 'file:');
  assert.equal(new URL(online.runtime.url).protocol, 'https:');
  assert.equal(offline.version, packageManifest.version);
  assert.equal(offline.productVersion, packageManifest.version);
  assert.doesNotThrow(() => enforceManifestVersion(offline));
  assert.doesNotThrow(() => enforceManifestVersion(online));
  assert.throws(() => enforcePublicReleasePolicy(offline, { publicRelease: true }), /Public release blocked/);
  assert.match(offline.releasePolicy.blockers.join(' '), /CI-SYNTHETIC-NOT-FOR-RUNTIME/);
  assert.equal(offline.rootfs.sha256, generated.metadata.sha256);
  assert.equal(online.rootfs.size, generated.archive.length);
  assert.equal(offline.runtime.sha256, generated.metadata.runtimeSha256);
  assert.equal(online.runtime.size, generated.runtimeArchive.length);
  await verifyArtifactFile(offline.rootfs, fileURLToPath(offline.rootfs.url));
  await verifyArtifactFile(offline.runtime, fileURLToPath(offline.runtime.url));
});


'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { fileURLToPath } = require('node:url');

const { loadApplianceManifest } = require('../../electron/release-manifest');
const { createArtifact, createManifest } = require('./fixtures/appliance-manifest-document');

test('release manifest loader accepts an explicit locked appliance manifest', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-release-manifest-'));
  const filename = path.join(directory, 'manifest.json');
  fs.writeFileSync(filename, JSON.stringify(createManifest({
    rootfs: createArtifact('rootfs', {
      url: 'https://example.test/applemusic-downloader-wsl-rootfs-1.2.0-amd64.tar.gz',
    }),
  })));

  const manifest = loadApplianceManifest({ explicitPath: filename, resourcesPath: directory });
  assert.equal(manifest.version, '1.2.0');
  assert.equal(manifest.rootfs.sha256, 'a'.repeat(64));
  assert.equal(manifest.runtime.file, 'applemusic-downloader-runtime-1.2.0-amd64.tar.gz');
  assert.equal(manifest.channel, 'private-ci');
  assert.equal(manifest.inputs.wrapperProvenanceStatus, 'mismatched-unverified-local-poc-only');
  assert.deepEqual(manifest.releasePolicy.blockers, ['Synthetic test manifest is not releasable.']);
});

test('release manifest preserves and can pin the v1.2.14 appliance build identity', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-release-build-id-'));
  const filename = path.join(directory, 'manifest.json');
  const document = createManifest({ version: '1.2.14' });
  fs.writeFileSync(filename, JSON.stringify(document));

  const manifest = loadApplianceManifest({
    explicitPath: filename,
    resourcesPath: directory,
    expectedVersion: '1.2.14',
    expectedApplianceBuildId: document.applianceBuildId,
  });
  assert.equal(manifest.productVersion, '1.2.14');
  assert.equal(manifest.applianceBuildId, document.applianceBuildId);
  assert.throws(() => loadApplianceManifest({
    explicitPath: filename,
    resourcesPath: directory,
    expectedVersion: '1.2.14',
    expectedApplianceBuildId: 'f'.repeat(64),
  }), /build identity/i);
});

test('offline release manifest rewrites both rootfs and runtime to bundled resources', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-offline-release-manifest-'));
  const resources = path.join(directory, 'resources');
  const appliance = path.join(resources, 'appliance');
  fs.mkdirSync(appliance, { recursive: true });
  const document = createManifest();
  const filename = path.join(directory, 'manifest.json');
  fs.writeFileSync(filename, JSON.stringify(document));
  fs.writeFileSync(path.join(appliance, document.rootfs.file), 'rootfs');
  fs.writeFileSync(path.join(appliance, document.runtime.file), 'runtime');

  const manifest = loadApplianceManifest({ explicitPath: filename, resourcesPath: resources });
  assert.equal(fileURLToPath(manifest.rootfs.url), path.join(appliance, document.rootfs.file));
  assert.equal(fileURLToPath(manifest.runtime.url), path.join(appliance, document.runtime.file));
});

test('expected release version selects the exact manifest instead of lexical fallback', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-versioned-release-manifest-'));
  const dist = path.join(directory, 'appliance', 'dist');
  fs.mkdirSync(dist, { recursive: true });
  for (const version of ['1.2.10', '1.2.11', '1.3.2', '1.3.3']) {
    const document = createManifest({ version });
    fs.writeFileSync(
      path.join(dist, `applemusic-downloader-wsl-manifest-${version}-amd64.json`),
      JSON.stringify(document),
    );
  }

  const manifest = loadApplianceManifest({
    repositoryRoot: directory,
    resourcesPath: directory,
    expectedVersion: '1.3.3',
  });
  assert.equal(manifest.version, '1.3.3');
  assert.throws(() => loadApplianceManifest({
    repositoryRoot: directory,
    resourcesPath: directory,
    expectedVersion: '1.2.12',
  }), /manifest|version/i);
});

test('expected release version prefers the exact dist manifest over stale generated identity', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-stale-generated-manifest-'));
  const dist = path.join(directory, 'appliance', 'dist');
  const generated = path.join(directory, 'electron', 'generated');
  fs.mkdirSync(dist, { recursive: true });
  fs.mkdirSync(generated, { recursive: true });
  const stale = createManifest({
    version: '1.3.3',
    inputs: { ...createManifest().inputs, backendTreeSha256: 'a'.repeat(64) },
  });
  const current = createManifest({
    version: '1.3.3',
    inputs: { ...createManifest().inputs, backendTreeSha256: 'b'.repeat(64) },
  });
  fs.writeFileSync(path.join(generated, 'appliance-manifest.json'), JSON.stringify(stale));
  fs.writeFileSync(path.join(dist, 'applemusic-downloader-wsl-manifest-1.3.3-amd64.json'), JSON.stringify(current));

  const manifest = loadApplianceManifest({
    repositoryRoot: directory,
    resourcesPath: directory,
    expectedVersion: '1.3.3',
  });
  assert.equal(manifest.applianceBuildId, current.applianceBuildId);
});

test('packaged release ignores an environment-style explicit manifest and only trusts resources', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-packaged-release-manifest-'));
  const resources = path.join(directory, 'resources');
  const appliance = path.join(resources, 'appliance');
  const attackerManifest = path.join(directory, 'attacker-manifest.json');
  fs.mkdirSync(appliance, { recursive: true });

  const trusted = createManifest({ version: '1.2.12' });
  const attacker = createManifest({
    version: '1.2.12',
    rootfs: createArtifact('rootfs', {
      url: 'file:///C:/attacker/rootfs.tar.gz',
      sha256: 'f'.repeat(64),
    }),
  });
  fs.writeFileSync(path.join(appliance, 'manifest.json'), JSON.stringify(trusted));
  fs.writeFileSync(attackerManifest, JSON.stringify(attacker));

  const manifest = loadApplianceManifest({
    explicitPath: attackerManifest,
    packaged: true,
    resourcesPath: resources,
    repositoryRoot: directory,
    expectedVersion: '1.2.12',
  });
  assert.equal(manifest.rootfs.sha256, trusted.rootfs.sha256);
  assert.notEqual(manifest.rootfs.url, attacker.rootfs.url);

  fs.rmSync(path.join(appliance, 'manifest.json'));
  assert.throws(() => loadApplianceManifest({
    explicitPath: attackerManifest,
    packaged: true,
    resourcesPath: resources,
    repositoryRoot: directory,
    expectedVersion: '1.2.12',
  }), /manifest/i);
});

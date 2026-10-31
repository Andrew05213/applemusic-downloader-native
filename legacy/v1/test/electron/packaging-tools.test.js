'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const asar = require('@electron/asar');
const packageManifest = require('../../package.json');

const {
  fileContainsBufferSync,
  verifyApplicationArchive,
  verifyPackagedResources,
} = require('../../electron/verify-packaged-resources');
const {
  enforceManifestVersion,
  enforcePublicReleasePolicy,
  sha256FileSync,
} = require('../../electron/inject-appliance-manifest');
const { createInteropRecoveryDescriptor } = require('../../electron/wslinterop-recovery');
const {
  ROOTFS_NAME,
  RUNTIME_NAME,
  createArtifact,
  createInputs,
  createManifest,
} = require('./fixtures/appliance-manifest-document');

const checksumWriter = path.resolve(__dirname, '..', '..', 'electron', 'write-checksums.js');

function createPackagedResources(t, {
  rootfsArchiveName = '',
  runtimeArchiveName = '',
  manifestRootfsName = ROOTFS_NAME,
  manifestRuntimeName = RUNTIME_NAME,
} = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-packaged-resources-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const appliance = path.join(directory, 'appliance');
  const bin = path.join(directory, 'bin');
  const recoveryDirectory = path.join(directory, 'recovery');
  const licenses = path.join(directory, 'third-party-licenses');
  fs.mkdirSync(appliance, { recursive: true });
  fs.mkdirSync(bin, { recursive: true });
  fs.mkdirSync(recoveryDirectory, { recursive: true });
  fs.mkdirSync(licenses, { recursive: true });
  const helperPath = path.join(bin, 'amdl-windows-probe.exe');
  const bootstrapPath = path.join(bin, 'amdl-wslinterop-bootstrap.exe');
  fs.writeFileSync(helperPath, Buffer.from('mock-pe'));
  fs.writeFileSync(bootstrapPath, Buffer.from('mock-linux-elf'));
  fs.writeFileSync(path.join(directory, 'NOTICE'), 'synthetic notice');
  fs.writeFileSync(path.join(licenses, 'runv2-LICENSE'), 'synthetic license');

  const rootfsArchive = Buffer.from('deterministic synthetic rootfs fixture');
  const runtimeArchive = Buffer.from('deterministic synthetic runtime fixture');
  const manifest = createManifest({
    rootfs: createArtifact('rootfs', {
      file: manifestRootfsName,
      url: `https://downloads.example.test/${manifestRootfsName}`,
      sha256: crypto.createHash('sha256').update(rootfsArchive).digest('hex'),
      size: rootfsArchive.length,
      compressedBytes: rootfsArchive.length,
    }),
    runtime: createArtifact('runtime', {
      file: manifestRuntimeName,
      url: `https://downloads.example.test/${manifestRuntimeName}`,
      sha256: crypto.createHash('sha256').update(runtimeArchive).digest('hex'),
      size: runtimeArchive.length,
      compressedBytes: runtimeArchive.length,
    }),
  });
  fs.writeFileSync(path.join(appliance, 'manifest.json'), `${JSON.stringify(manifest)}\n`);
  fs.writeFileSync(path.join(recoveryDirectory, 'wslinterop-recovery.json'), `${JSON.stringify(createInteropRecoveryDescriptor({
    targetVersion: manifest.version,
    bootstrapPath,
    windowsHelperPath: helperPath,
  }))}\n`);
  if (rootfsArchiveName) fs.writeFileSync(path.join(appliance, rootfsArchiveName), rootfsArchive);
  if (runtimeArchiveName) fs.writeFileSync(path.join(appliance, runtimeArchiveName), runtimeArchive);
  return directory;
}

test('packaged resource verifier enforces online and offline rootfs boundaries', async (t) => {
  const online = createPackagedResources(t);
  await verifyPackagedResources({ mode: 'online', resourcesDirectory: online, verifyAppArchive: false });

  const onlineWithRootfs = createPackagedResources(t, { rootfsArchiveName: ROOTFS_NAME });
  await assert.rejects(
    verifyPackagedResources({ mode: 'online', resourcesDirectory: onlineWithRootfs, verifyAppArchive: false }),
    /must not embed Appliance archives/,
  );

  const offline = createPackagedResources(t, {
    rootfsArchiveName: ROOTFS_NAME,
    runtimeArchiveName: RUNTIME_NAME,
  });
  await verifyPackagedResources({ mode: 'offline', resourcesDirectory: offline, verifyAppArchive: false });
});

test('offline packaged resource verifier rejects a hash-valid archive under the wrong name', async (t) => {
  const resources = createPackagedResources(t, {
    rootfsArchiveName: 'unexpected-rootfs.tar.gz',
    runtimeArchiveName: RUNTIME_NAME,
  });
  await assert.rejects(
    verifyPackagedResources({ mode: 'offline', resourcesDirectory: resources, verifyAppArchive: false }),
    /manifest rootfs and runtime archives/,
  );
});

test('application archive enforces an exact file whitelist and scans a secret sentinel', async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-asar-boundary-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const source = path.join(directory, 'source');
  const resources = path.join(directory, 'resources');
  const archive = path.join(resources, 'app.asar');
  fs.mkdirSync(source, { recursive: true });
  fs.mkdirSync(resources, { recursive: true });
  fs.mkdirSync(path.join(source, 'src', 'renderer'), { recursive: true });
  fs.writeFileSync(path.join(source, 'package.json'), '{"name":"fixture"}\n');
  const renderer = Buffer.from('console.log("renderer fixture");\n');
  fs.writeFileSync(path.join(source, 'src', 'renderer', 'renderer.js'), renderer);
  const expectedFiles = ['package.json', 'src/renderer/renderer.js'];
  const rendererSha256 = crypto.createHash('sha256').update(renderer).digest('hex');
  await asar.createPackage(source, archive);
  assert.doesNotThrow(() => verifyApplicationArchive({
    resourcesDirectory: resources,
    expectedFiles,
    expectedRendererSha256: rendererSha256,
    secretSentinel: 'AMDL_UNIQUE_PACKAGING_SENTINEL',
  }));
  assert.throws(() => verifyApplicationArchive({
    resourcesDirectory: resources,
    expectedFiles,
    expectedRendererSha256: '0'.repeat(64),
  }), /renderer SHA-256/);

  fs.rmSync(archive, { force: true });
  fs.writeFileSync(path.join(source, 'unexpected.txt'), 'extra');
  await asar.createPackage(source, archive);
  assert.throws(() => verifyApplicationArchive({
    resourcesDirectory: resources,
    expectedFiles,
  }), /whitelist mismatch/);

  fs.rmSync(archive, { force: true });
  fs.rmSync(path.join(source, 'unexpected.txt'));
  fs.writeFileSync(path.join(source, 'package.json'), 'AMDL_UNIQUE_PACKAGING_SENTINEL');
  await asar.createPackage(source, archive);
  assert.throws(() => verifyApplicationArchive({
    resourcesDirectory: resources,
    expectedFiles,
    secretSentinel: 'AMDL_UNIQUE_PACKAGING_SENTINEL',
  }), /Secret sentinel found/);
});

test('streaming sentinel scan detects a value split across chunk boundaries', (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-sentinel-boundary-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const file = path.join(directory, 'payload.bin');
  const sentinel = Buffer.from('AMDL_BOUNDARY_SENTINEL', 'utf8');
  fs.writeFileSync(file, Buffer.concat([
    Buffer.alloc((64 * 1024) - 7, 0x41),
    sentinel,
    Buffer.alloc(32, 0x42),
  ]));
  assert.equal(fileContainsBufferSync(file, sentinel), true);
  assert.equal(fileContainsBufferSync(file, Buffer.from('not-present', 'utf8')), false);
});

test('checksum writer creates a sidecar only for the requested installer mode', (t) => {
	const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-installer-checksum-'));
	t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
	const onlineName = `AppleMusicDownloader-${packageManifest.version}-Setup.exe`;
	const offlineName = `AppleMusicDownloader-${packageManifest.version}-Offline-Setup.exe`;
	const online = path.join(directory, onlineName);
	const offline = path.join(directory, offlineName);
	const stale = path.join(directory, 'AppleMusicDownloader-Setup.exe');
  fs.writeFileSync(online, 'online-installer');
  fs.writeFileSync(offline, 'offline-installer');
  fs.writeFileSync(stale, 'stale-installer');

  const result = spawnSync(process.execPath, [checksumWriter, directory, 'online'], {
    encoding: 'utf8',
    shell: false,
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.existsSync(`${online}.sha256`), true);
  assert.equal(fs.existsSync(`${offline}.sha256`), false);
  assert.equal(fs.existsSync(`${stale}.sha256`), false);
	assert.match(fs.readFileSync(`${online}.sha256`, 'utf8'), new RegExp(`  ${onlineName}\\n$`));

  const offlineResult = spawnSync(process.execPath, [checksumWriter, directory, 'offline'], {
    encoding: 'utf8',
    shell: false,
  });
  assert.equal(offlineResult.status, 0, offlineResult.stderr);
  assert.equal(fs.existsSync(`${offline}.sha256`), true);
  assert.equal(fs.existsSync(`${stale}.sha256`), false);
	assert.match(fs.readFileSync(`${offline}.sha256`, 'utf8'), new RegExp(`  ${offlineName}\\n$`));
});

test('packaging binds the desktop version and reviewed public release policy digest', (t) => {
  assert.doesNotThrow(() => enforceManifestVersion(createManifest({ version: require('../../electron/legacy-desktop-package.json').version })));
  assert.throws(
    () => enforceManifestVersion(createManifest({ version: '1.2.1' })),
    /does not match desktop version/,
  );

  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-public-policy-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const policyPath = path.join(directory, 'release-policy.json');
  const blockers = ['Synthetic policy used only by a unit test.'];
  fs.writeFileSync(policyPath, `${JSON.stringify({
    schemaVersion: 1,
    publicReleaseAllowed: true,
    blockers,
  })}\n`);
  const policyDigest = sha256FileSync(policyPath);
  const manifest = createManifest({
    version: '1.4.3',
    channel: 'public',
    inputs: createInputs({
      releasePolicySha256: policyDigest,
      wrapperProvenanceStatus: 'verified-source-build',
    }),
    releasePolicy: { publicReleaseAllowed: true, blockers },
  });

  assert.throws(() => enforcePublicReleasePolicy(manifest, {
    publicRelease: false,
    redistributionApproved: true,
    releasePolicyPath: policyPath,
  }), /explicit public release intent/);

  assert.doesNotThrow(() => enforcePublicReleasePolicy(manifest, {
    publicRelease: true,
    redistributionApproved: true,
    releasePolicyPath: policyPath,
  }));
  assert.throws(() => enforcePublicReleasePolicy({
    ...manifest,
    inputs: { ...manifest.inputs, releasePolicySha256: '0'.repeat(64) },
  }, {
    publicRelease: true,
    redistributionApproved: true,
    releasePolicyPath: policyPath,
  }), /digest does not match/);
  assert.throws(() => enforcePublicReleasePolicy({ ...manifest, channel: 'private-ci' }, {
    publicRelease: true,
    redistributionApproved: true,
    releasePolicyPath: policyPath,
  }), /channel must be public/);
});

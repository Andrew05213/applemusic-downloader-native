const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const {
  artifactCacheName,
  computeApplianceBuildId,
  downloadAndVerifyArtifact,
  validateApplianceManifest,
  verifyArtifactFile,
} = require('../../electron/appliance-manifest');
const {
  createArtifact,
  createInputs,
  createManifest,
} = require('./fixtures/appliance-manifest-document');

test('manifest preserves the complete locked WSL appliance contract', () => {
  const document = createManifest();
  assert.deepEqual(validateApplianceManifest(document), document);
});

test('manifest requires runtime, channel, builtAt, inputs and releasePolicy', () => {
  for (const field of ['runtime', 'channel', 'builtAt', 'inputs', 'releasePolicy']) {
    const document = createManifest();
    delete document[field];
    assert.throws(() => validateApplianceManifest(document), new RegExp(`missing required field.*${field}`));
  }
});

test('manifest rejects unknown fields at every contract boundary', () => {
  const mutations = [
    (document) => { document.legacy = true; },
    (document) => { document.rootfs.authorization = 'secret'; },
    (document) => { document.inputs.unreviewedDigest = 'a'.repeat(64); },
    (document) => { document.releasePolicy.reviewedAt = '2026-07-29'; },
  ];
  for (const mutate of mutations) {
    const document = createManifest();
    mutate(document);
    assert.throws(() => validateApplianceManifest(document), /unknown field/);
  }
});

test('manifest requires every artifact, input and release policy field', () => {
  const mutations = [
    (document) => { delete document.rootfs.file; },
    (document) => { delete document.runtime.uncompressedBytes; },
    (document) => { delete document.inputs.versionsLockSha256; },
    (document) => { delete document.inputs.storeservicescoreBinarySha256; },
    (document) => { delete document.inputs.urlRequestAllocationBytes; },
    (document) => { delete document.inputs.urlRequestObjectOffset; },
    (document) => { delete document.inputs.urlRequestObjectBytes; },
    (document) => { delete document.inputs.urlRequestObjectAlignment; },
    (document) => { delete document.inputs.wrapperProvenanceStatus; },
    (document) => { delete document.releasePolicy.blockers; },
  ];
  for (const mutate of mutations) {
    const document = createManifest();
    mutate(document);
    assert.throws(() => validateApplianceManifest(document), /missing required field/);
  }
});

test('manifest locks the URLRequest ABI evidence and object envelope', () => {
  const mutations = [
    (document) => { document.inputs.storeservicescoreBinarySha256 = 'A'.repeat(64); },
    (document) => { document.inputs.urlRequestAllocationBytes = 0; },
    (document) => { document.inputs.urlRequestObjectOffset = 32.5; },
    (document) => { document.inputs.urlRequestObjectBytes = 544; },
    (document) => { document.inputs.urlRequestObjectAlignment = 8; },
    (document) => {
      document.inputs.urlRequestAllocationBytes = 608;
      document.inputs.urlRequestObjectBytes = 576;
    },
  ];
  for (const mutate of mutations) {
    const document = createManifest();
    mutate(document);
    assert.throws(() => validateApplianceManifest(document), /URLRequest|storeservicescore/i);
  }
});

test('1.2.8 manifests require the evidence-backed HTTPMessage provider and envelope', () => {
  for (const field of [
    'androidAppMusicBinarySha256',
    'httpMessageAllocationBytes',
    'httpMessageObjectOffset',
    'httpMessageObjectBytes',
    'httpMessageStorageAlignment',
    'httpMessageObjectAlignment',
  ]) {
    const document = createManifest({ version: '1.2.8' });
    delete document.inputs[field];
    assert.throws(() => validateApplianceManifest(document), new RegExp(`missing required field.*${field}`));
  }
  assert.throws(() => validateApplianceManifest(createManifest({
    version: '1.2.8',
    inputs: createInputs({ httpMessageObjectOffset: 32 }),
  })), /HTTPMessage/);
  assert.doesNotThrow(() => validateApplianceManifest(createManifest({ version: '1.2.8' })));
  // Older upgrade fixtures remain valid without the new evidence fields.
  const legacy = createManifest({ version: '1.2.7' });
  for (const field of [
    'androidAppMusicBinarySha256',
    'httpMessageAllocationBytes',
    'httpMessageObjectOffset',
    'httpMessageObjectBytes',
    'httpMessageStorageAlignment',
    'httpMessageObjectAlignment',
  ]) delete legacy.inputs[field];
  assert.doesNotThrow(() => validateApplianceManifest(legacy));
});

test('1.2.10 manifests require the evidence-backed StoreErrorCode accessor contract', () => {
  for (const field of ['storeErrorCodeObjectOffset', 'storeErrorCodeValueBytes']) {
    const document = createManifest({ version: '1.2.10' });
    delete document.inputs[field];
    assert.throws(() => validateApplianceManifest(document), new RegExp(`missing required field.*${field}`));
  }
  assert.throws(() => validateApplianceManifest(createManifest({
    version: '1.2.10',
    inputs: createInputs({ storeErrorCodeObjectOffset: 0x18 }),
  })), /StoreErrorCode/);
  assert.throws(() => validateApplianceManifest(createManifest({
    version: '1.2.10',
    inputs: createInputs({ storeErrorCodeValueBytes: 8 }),
  })), /StoreErrorCode/);
  assert.doesNotThrow(() => validateApplianceManifest(createManifest({ version: '1.2.10' })));

  const legacy = createManifest({ version: '1.2.9' });
  delete legacy.inputs.storeErrorCodeObjectOffset;
  delete legacy.inputs.storeErrorCodeValueBytes;
  assert.doesNotThrow(() => validateApplianceManifest(legacy));
});

test('1.2.12 manifests require built pin provenance bound to the Wrapper patch', () => {
  const missing = createManifest({ version: '1.2.12' });
  delete missing.inputs.wrapperBuiltFromPatchSha256;
  assert.throws(() => validateApplianceManifest(missing), /missing required field.*wrapperBuiltFromPatchSha256/);

  const mismatched = createManifest({
    version: '1.2.12',
    inputs: createInputs({ wrapperBuiltFromPatchSha256: 'c'.repeat(64) }),
  });
  assert.throws(() => validateApplianceManifest(mismatched), /must match inputs\.wrapperPatchSha256/);
  assert.doesNotThrow(() => validateApplianceManifest(createManifest({ version: '1.2.12' })));

  const legacy = createManifest({ version: '1.2.11' });
  delete legacy.inputs.wrapperBuiltFromPatchSha256;
  assert.doesNotThrow(() => validateApplianceManifest(legacy));
});

test('1.2.14 requires canonical build identity and native/Web provenance while 1.2.12 remains readable', () => {
  const current = createManifest({ version: '1.2.14' });
  assert.equal(current.productVersion, '1.2.14');
  assert.equal(current.applianceBuildId, computeApplianceBuildId(current));
  assert.deepEqual(validateApplianceManifest(current), current);

  for (const field of ['productVersion', 'sourceDateEpoch', 'applianceBuildId']) {
    const missing = structuredClone(current);
    delete missing[field];
    assert.throws(() => validateApplianceManifest(missing), new RegExp(`missing required field.*${field}`));
  }
  const missingNetworkSource = structuredClone(current);
  delete missingNetworkSource.inputs.networkPreflightSourceSha256;
  assert.throws(
    () => validateApplianceManifest(missingNetworkSource),
    /missing required field.*networkPreflightSourceSha256/,
  );
  const missingRenderer = structuredClone(current);
  delete missingRenderer.inputs.rendererSha256;
  assert.throws(
    () => validateApplianceManifest(missingRenderer),
    /missing required field.*rendererSha256/,
  );

  const legacy = createManifest({ version: '1.2.12' });
  delete legacy.inputs.networkPreflightSourceSha256;
  delete legacy.inputs.rendererSha256;
  assert.doesNotThrow(() => validateApplianceManifest(legacy));
});

test('1.3.3 requires the fixed Android CA bundle identity', () => {
  const current = createManifest({ version: '1.3.3' });
  assert.doesNotThrow(() => validateApplianceManifest(current));

  for (const field of ['androidCaBundlePath', 'androidCaBundleSha256']) {
    const missing = structuredClone(current);
    delete missing.inputs[field];
    assert.throws(() => validateApplianceManifest(missing), new RegExp(`missing required field.*${field}`));
  }
  const wrongPath = structuredClone(current);
  wrongPath.inputs.androidCaBundlePath = '/etc/ssl/certs/ca-certificates.crt';
  assert.throws(() => validateApplianceManifest(wrongPath), /androidCaBundlePath/);
  const wrongDigest = structuredClone(current);
  wrongDigest.inputs.androidCaBundleSha256 = 'A'.repeat(64);
  assert.throws(() => validateApplianceManifest(wrongDigest), /androidCaBundleSha256/);

  const legacy = createManifest({ version: '1.2.14' });
  delete legacy.inputs.androidCaBundlePath;
  delete legacy.inputs.androidCaBundleSha256;
  legacy.applianceBuildId = computeApplianceBuildId(legacy);
  assert.doesNotThrow(() => validateApplianceManifest(legacy));
});

test('1.3.5 requires standalone native preflight identity while 1.3.4 remains readable', () => {
  const current = createManifest({ version: '1.3.5' });
  assert.equal(current.inputs.androidNetworkPreflightBuiltSha256, '5'.repeat(64));
  assert.deepEqual(validateApplianceManifest(current), current);

  const missing = structuredClone(current);
  delete missing.inputs.androidNetworkPreflightBuiltSha256;
  missing.applianceBuildId = computeApplianceBuildId(missing);
  assert.throws(
    () => validateApplianceManifest(missing),
    /missing required field.*androidNetworkPreflightBuiltSha256/,
  );

  const legacy = createManifest({ version: '1.3.4' });
  delete legacy.inputs.androidNetworkPreflightBuiltSha256;
  legacy.applianceBuildId = computeApplianceBuildId(legacy);
  assert.doesNotThrow(() => validateApplianceManifest(legacy));
});

test('appliance build ID is deterministic, input-sensitive and independently verified', () => {
  const first = createManifest({ version: '1.2.14' });
  const second = createManifest({ version: '1.2.14' });
  assert.equal(first.applianceBuildId, second.applianceBuildId);

  const changedInput = createManifest({
    version: '1.2.14',
    inputs: createInputs({ backendTreeSha256: 'd'.repeat(64) }),
  });
  const changedPolicy = createManifest({
    version: '1.2.14',
    releasePolicy: {
      publicReleaseAllowed: false,
      blockers: ['A different locked release blocker.'],
    },
  });
  assert.notEqual(changedInput.applianceBuildId, first.applianceBuildId);
  assert.notEqual(changedPolicy.applianceBuildId, first.applianceBuildId);

  const tampered = structuredClone(first);
  tampered.inputs.backendTreeSha256 = 'e'.repeat(64);
  assert.throws(() => validateApplianceManifest(tampered), /applianceBuildId.*canonical/i);

  const wrongVersion = structuredClone(first);
  wrongVersion.productVersion = '1.2.12';
  assert.throws(() => validateApplianceManifest(wrongVersion), /productVersion.*version/i);
});

test('manifest validates immutable digests, commits, images, snapshot and provenance status', () => {
  const mutations = [
    (document) => { document.inputs.backendTreeSha256 = 'A'.repeat(64); },
    (document) => { document.inputs.wrapperCommit = 'not-a-commit'; },
    (document) => { document.inputs.goBuilderImage = 'golang:1.23.1-bookworm'; },
    (document) => { document.inputs.ubuntuRuntimeImage = `ubuntu:22.04@sha256:${'G'.repeat(64)}`; },
    (document) => { document.inputs.ubuntuSnapshot = '20260230T000000Z'; },
    (document) => { document.inputs.wrapperProvenanceStatus = 'trust-me'; },
    (document) => { document.rootfs.compressedBytes += 1; },
    (document) => { document.releasePolicy.blockers = []; },
  ];
  for (const mutate of mutations) {
    const document = createManifest();
    mutate(document);
    assert.throws(() => validateApplianceManifest(document));
  }
});

test('manifest locks the WSL distro and credential-free artifact URLs', () => {
  assert.throws(() => validateApplianceManifest(createManifest({ distroName: 'Ubuntu' })), /distroName/);
  assert.throws(() => validateApplianceManifest(createManifest({
    rootfs: createArtifact('rootfs', { url: 'https://downloads.example.test/rootfs?token=secret' }),
  })), /query parameters/);
});

test('public manifests require approved provenance and HTTPS artifacts', () => {
  assert.throws(() => validateApplianceManifest(createManifest({ channel: 'public' })), /blocked by releasePolicy/);
  assert.throws(() => validateApplianceManifest(createManifest({
    releasePolicy: { publicReleaseAllowed: true, blockers: ['Test policy record.'] },
  })), /verified Wrapper provenance/);

  const approved = createManifest({
    channel: 'public',
    inputs: createInputs({ wrapperProvenanceStatus: 'verified-source-build' }),
    releasePolicy: { publicReleaseAllowed: true, blockers: ['Test policy record.'] },
  });
  assert.equal(validateApplianceManifest(approved).channel, 'public');
  assert.throws(() => validateApplianceManifest({
    ...approved,
    rootfs: createArtifact('rootfs', { url: 'file:///C:/fixtures/rootfs.tar.gz' }),
  }), /must use HTTPS/);
});

test('standalone artifact download helpers retain the compact artifact contract', () => {
  const artifact = { url: 'https://downloads.example.test/rootfs.tar.gz', sha256: 'A'.repeat(64), size: 123 };
  assert.match(artifactCacheName('rootfs', '1.2.0', artifact), /^applemusic-downloader-wsl-rootfs-1\.2\.0-amd64-a{16}\.tar\.gz$/);
});

test('artifact download verifies size and SHA-256 before publishing the cache file', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-artifact-'));
  const source = path.join(directory, 'source.tar.gz');
  const destination = path.join(directory, 'cache', 'rootfs.tar.gz');
  const bytes = Buffer.from('reproducible rootfs fixture');
  fs.writeFileSync(source, bytes);
  const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');

  await downloadAndVerifyArtifact({ url: pathToFileURL(source).toString(), sha256, size: bytes.length }, destination);
  assert.deepEqual(fs.readFileSync(destination), bytes);

  await assert.rejects(
    downloadAndVerifyArtifact({ url: pathToFileURL(source).toString(), sha256: 'f'.repeat(64), size: bytes.length }, path.join(directory, 'bad.tar.gz')),
    /SHA-256 mismatch/,
  );
  assert.equal(fs.existsSync(path.join(directory, 'bad.tar.gz')), false);
  assert.equal(fs.readdirSync(directory).some((name) => name.endsWith('.part')), false);
});

test('artifact use-time verification detects an in-place cache mutation before import', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-artifact-reverify-'));
  const filename = path.join(directory, 'rootfs.tar.gz');
  const bytes = Buffer.from('verified rootfs bytes');
  const sha256 = crypto.createHash('sha256').update(bytes).digest('hex');
  const artifact = { url: pathToFileURL(filename).toString(), sha256, size: bytes.length };
  fs.writeFileSync(filename, bytes);

  await verifyArtifactFile(artifact, filename);
  fs.writeFileSync(filename, Buffer.from('mutated rootfs bytes!'));

  await assert.rejects(verifyArtifactFile(artifact, filename), /SHA-256 mismatch|size mismatch/);
});

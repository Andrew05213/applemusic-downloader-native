'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  INCIDENT_SOURCE,
  legacyInteropSourceMatches,
  loadInteropRecoveryDescriptorWithIdentity,
  validateInteropRecoveryDescriptor,
} = require('../../electron/wslinterop-recovery');

const binding = (file, character) => ({ file, sha256: character.repeat(64), size: 1 });

function descriptor(targetVersion = '1.2.14') {
  return {
    schemaVersion: 1,
    targetVersion,
    source: { ...INCIDENT_SOURCE },
    bootstrap: binding('amdl-wslinterop-bootstrap.exe', 'a'),
    windowsHelper: binding('amdl-windows-probe.exe', 'b'),
  };
}

function sourceShape() {
  return {
    manifest: {
      version: INCIDENT_SOURCE.productVersion,
      productVersion: INCIDENT_SOURCE.productVersion,
      applianceBuildId: INCIDENT_SOURCE.applianceBuildId,
      runtime: { sha256: INCIDENT_SOURCE.runtimeSha256 },
    },
    status: {
      version: INCIDENT_SOURCE.productVersion,
      productVersion: INCIDENT_SOURCE.productVersion,
      applianceBuildId: INCIDENT_SOURCE.applianceBuildId,
      runtimeSha256: INCIDENT_SOURCE.runtimeSha256,
      runtimeTransactionId: 'c'.repeat(64),
      androidMainBuiltSha256: INCIDENT_SOURCE.androidMainBuiltSha256,
      rendererSha256: INCIDENT_SOURCE.rendererSha256,
      windowsHelperSha256: INCIDENT_SOURCE.windowsHelperSha256,
      network: { state: 'failed' },
    },
    state: {
      installedVersion: INCIDENT_SOURCE.productVersion,
      installedRuntimeSha256: INCIDENT_SOURCE.runtimeSha256,
      installedRuntimeTransactionId: 'c'.repeat(64),
    },
  };
}

test('recovery descriptor is strict and version-bound', () => {
  assert.doesNotThrow(() => validateInteropRecoveryDescriptor(descriptor(), { expectedVersion: '1.2.14' }));
  for (const mutate of [
    (value) => { value.bootstrap.file = 'other.exe'; },
    (value) => { value.source.runtimeSha256 = 'd'.repeat(64); },
    (value) => { value.extra = true; },
    (value) => { value.windowsHelper.size = 0; },
  ]) {
    const value = descriptor();
    mutate(value);
    assert.throws(() => validateInteropRecoveryDescriptor(value, { expectedVersion: '1.2.14' }));
  }
});

test('recovery gate accepts only the incident identity and the documented empty-helper restart state', () => {
  const shape = sourceShape();
  assert.equal(legacyInteropSourceMatches({ descriptor: descriptor(), ...shape }), true);

  shape.status.windowsHelperSha256 = '';
  shape.status.network = { state: 'unprepared' };
  assert.equal(legacyInteropSourceMatches({ descriptor: descriptor(), ...shape }), true);

  shape.status.network = { state: 'failed' };
  assert.equal(legacyInteropSourceMatches({ descriptor: descriptor(), ...shape }), false);

  const mismatched = sourceShape();
  mismatched.status.applianceBuildId = 'e'.repeat(64);
  assert.equal(legacyInteropSourceMatches({ descriptor: descriptor(), ...mismatched }), false);
});

test('recovery descriptor loader binds the exact descriptor bytes', (t) => {
  const repositoryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-wslinterop-descriptor-'));
  t.after(() => fs.rmSync(repositoryRoot, { recursive: true, force: true }));
  const filename = path.join(repositoryRoot, 'descriptor.json');
  const value = `${JSON.stringify(descriptor(), null, 2)}\n`;
  fs.writeFileSync(filename, value, 'utf8');

  const loaded = loadInteropRecoveryDescriptorWithIdentity({
    expectedVersion: '1.2.14',
    repositoryRoot,
    explicitPath: filename,
  });
  assert.equal(loaded.filename, filename);
  assert.equal(loaded.sha256, crypto.createHash('sha256').update(value, 'utf8').digest('hex'));
  assert.deepEqual(loaded.descriptor, validateInteropRecoveryDescriptor(descriptor(), { expectedVersion: '1.2.14' }));
});

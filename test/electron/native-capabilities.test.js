'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { nativeCapabilities } = require('../../electron/native-capabilities');

function ready() {
  return {
    runtime: 'native-lite', backendReady: true, wrapperReady: true,
    sessionVerified: true, catalogVerified: true, libraryVerified: true,
    downloadVerified: true, playbackVerified: true,
    legacyWslRequired: false, embeddedWebRequired: false,
  };
}

test('no observation is ready by default', () => {
  const expected = { browse: false, library: false, download: false, fullPlayback: false };
  for (const input of [undefined, null, false, '', {}]) {
    assert.deepEqual(nativeCapabilities(input), expected);
  }
});

test('each operation requires its own positive verification', () => {
  assert.deepEqual(nativeCapabilities(ready()), {
    browse: true, library: true, download: true, fullPlayback: true,
  });
  for (const [proof, capability] of [
    ['catalogVerified', 'browse'], ['libraryVerified', 'library'],
    ['downloadVerified', 'download'], ['playbackVerified', 'fullPlayback'],
  ]) {
    assert.equal(nativeCapabilities({ ...ready(), [proof]: false })[capability], false);
    assert.equal(nativeCapabilities({ ...ready(), [proof]: 'true' })[capability], false);
  }
});

test('a cached Wrapper status does not prove a login session', () => {
  assert.deepEqual(nativeCapabilities({ ...ready(), sessionVerified: false }), {
    browse: true, library: false, download: false, fullPlayback: false,
  });
});

test('catalog can work without a ready Wrapper or personal session', () => {
  assert.equal(nativeCapabilities({ ...ready(), wrapperReady: false }).browse, true);
  assert.equal(nativeCapabilities({ ...ready(), wrapperReady: false }).download, false);
});

test('2.0 does not accept WSL or a hidden browser as its runtime', () => {
  for (const observation of [
    { ...ready(), runtime: 'wsl' },
    { ...ready(), legacyWslRequired: true },
    { ...ready(), embeddedWebRequired: true },
    { ...ready(), backendReady: false },
    { ...ready(), legacyWslRequired: undefined },
    { ...ready(), embeddedWebRequired: undefined },
  ]) {
    assert.deepEqual(nativeCapabilities(observation), {
      browse: false, library: false, download: false, fullPlayback: false,
    });
  }
});

test('full playback is not inferred from successful downloads', () => {
  const result = nativeCapabilities({ ...ready(), playbackVerified: false });
  assert.equal(result.download, true);
  assert.equal(result.fullPlayback, false);
  assert.equal(Object.isFrozen(result), true);
});

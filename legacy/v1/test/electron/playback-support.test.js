'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { normalizePlaybackTrack, validMediaUserToken, preparePlaybackDRM } = require('../../electron/playback-support');

test('full playback accepts only a bounded catalog song without extra input fields', () => {
  assert.deepEqual(normalizePlaybackTrack({ id: '123', type: 'songs', title: 'Fixture' }), {
    id: '123', type: 'songs', title: 'Fixture',
  });
  assert.equal(normalizePlaybackTrack({ id: '123', type: 'songs', title: 'x'.repeat(700) }).title.length, 512);
  for (const value of [null, [], { id: 'p.private', type: 'songs' }, { id: '1', type: 'albums' },
    { id: '1', type: 'songs', url: 'https://evil.invalid' }]) {
    assert.throws(() => normalizePlaybackTrack(value), /invalid_playback_item/);
  }
});

test('only bounded printable user tokens are eligible for encrypted credential delivery', () => {
  assert.equal(validMediaUserToken('fixture-user-token-'.repeat(8)), true);
  for (const token of ['', 'x'.repeat(50), 'x'.repeat(16385), 'fixture-token-'.repeat(8) + '\n', null]) {
    assert.equal(validMediaUserToken(token), false);
  }
});

test('DRM preparation reports component readiness without exposing installation errors', async () => {
  assert.equal(await preparePlaybackDRM(undefined), false);
  assert.equal(await preparePlaybackDRM({ WIDEVINE_CDM_ID: 'fixture', whenReady: async () => {
    throw new Error('private-network-sentinel');
  } }), false);
  let requested;
  assert.equal(await preparePlaybackDRM({ WIDEVINE_CDM_ID: 'fixture', whenReady: async ids => {
    requested = ids;
  } }), true);
  assert.deepEqual(requested, ['fixture']);
});

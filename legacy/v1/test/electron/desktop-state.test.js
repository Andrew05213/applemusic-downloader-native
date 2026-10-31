'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { DesktopStateStore, normalizeState } = require('../../electron/desktop-state');

test('desktop state persists only non-secret setup progress', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-desktop-state-'));
  const statePath = path.join(directory, 'desktop-state.json');
  const store = new DesktopStateStore(statePath);

  store.save({
    setupStep: 'downloading',
    downloadDir: 'C:\\Users\\张三\\Music\\Apple Music Downloader',
    applePassword: 'SENTINEL-password',
    mediaUserToken: 'SENTINEL-token',
  });

  const disk = fs.readFileSync(statePath, 'utf8');
  assert.doesNotMatch(disk, /SENTINEL/);
  assert.equal(store.load().setupStep, 'downloading');
  assert.match(store.load().downloadDir, /张三/);
});

test('desktop state rejects invalid ports and unknown keys', () => {
  assert.deepEqual(normalizeState({ localPort: 70000, arbitrary: 'value' }), { version: 1 });
});

test('one-click defaults persist only allowlisted quality and existing-file policy', () => {
  assert.deepEqual(normalizeState({ defaultQuality: 'alac', defaultExistingPolicy: 'fail' }), {
    version: 1, defaultQuality: 'alac', defaultExistingPolicy: 'fail',
  });
  assert.deepEqual(normalizeState({ defaultQuality: 'secret', defaultExistingPolicy: 'SENTINEL' }), { version: 1 });
});

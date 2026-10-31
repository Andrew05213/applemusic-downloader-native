'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');
const path = require('node:path');

const preloadPath = path.resolve(__dirname, '..', '..', 'src', 'preload', 'preload.js');

test('desktop preload exposes only fixed no-argument settings actions', async () => {
  const originalLoad = Module._load;
  const originalCached = require.cache[preloadPath];
  let exposedName;
  let exposedAPI;
  const calls = [];
  const listeners = new Map();
  try {
    delete require.cache[preloadPath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') {
        return {
          contextBridge: {
            exposeInMainWorld(name, api) {
              exposedName = name;
              exposedAPI = api;
            },
          },
          ipcRenderer: {
            invoke: async (channel, ...args) => {
              calls.push({ channel, args });
              return { ok: true };
            },
            on: (channel, listener) => listeners.set(channel, listener),
            removeListener: (channel, listener) => { if (listeners.get(channel) === listener) listeners.delete(channel); },
          },
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    require(preloadPath);
    assert.equal(exposedName, 'desktopShell');
    assert.equal(Object.isFrozen(exposedAPI), true);
    const actions = {
      repairRuntime: 'desktop:repair-runtime',
      openDownloadSettings: 'desktop:open-download-settings',
      chooseDownloadLocation: 'desktop:choose-download-location',
      openCredentials: 'desktop:open-credentials',
      openPlaybackLogin: 'desktop:playback-open-login',
      playCatalogTrack: 'desktop:playback-play',
      stopCatalogPlayback: 'desktop:playback-stop',
      startService: 'desktop:start-service',
      showDiagnostics: 'desktop:show-diagnostics',
      exportDiagnostics: 'desktop:export-diagnostics',
      stopService: 'desktop:stop-service',
      openAppleMusic: 'desktop:music-open',
      closeAppleMusic: 'desktop:music-close',
      appleMusicBack: 'desktop:music-back',
      appleMusicForward: 'desktop:music-forward',
      appleMusicReload: 'desktop:music-reload',
      currentAppleMusicURL: 'desktop:music-current',
      setAppleMusicBounds: 'desktop:music-bounds',
      getDownloadPreferences: 'desktop:get-download-preferences',
      setDownloadPreferences: 'desktop:set-download-preferences',
    };
    assert.deepEqual(Object.keys(exposedAPI).sort(), [...Object.keys(actions), 'onAppleMusicDownloadIntent', 'onServiceState'].sort());
    for (const method of Object.keys(actions)) {
      const input = { x: 220, y: 180, width: 700, height: 500 };
      assert.deepEqual(await exposedAPI[method](input), { ok: true });
    }
    assert.deepEqual(calls, Object.entries(actions).map(([method, channel]) => ({
      channel,
      args: ['setAppleMusicBounds', 'setDownloadPreferences', 'playCatalogTrack'].includes(method) ? [{ x: 220, y: 180, width: 700, height: 500 }] : [],
    })));
    await exposedAPI.exportDiagnostics(['CKC error.', null, 'x'.repeat(4000)]);
    assert.deepEqual(calls.at(-1), { channel: 'desktop:export-diagnostics', args: [['CKC error.', 'x'.repeat(2048)]] });
    let received;
    const unsubscribe = exposedAPI.onAppleMusicDownloadIntent((value) => { received = value; });
    listeners.get('desktop:music-download-intent')({}, { url: 'https://music.apple.com/us/song/123' });
    assert.deepEqual(received, { url: 'https://music.apple.com/us/song/123' });
    unsubscribe();
    assert.equal(listeners.has('desktop:music-download-intent'), false);
    const unsubscribeState = exposedAPI.onServiceState((value) => { received = value; });
    listeners.get('desktop:shell-state')({}, { state: 'unavailable', code: 'network_preflight_failed', stage: 'tls' });
    assert.deepEqual(received, { state: 'unavailable', code: 'network_preflight_failed', stage: 'tls' });
    listeners.get('desktop:shell-state')({}, { state: 'unavailable', code: 'network_preflight_failed', stage: 'SENTINEL_STAGE' });
    assert.deepEqual(received, { state: 'unavailable', code: 'network_preflight_failed', stage: 'unknown' });
    unsubscribeState();
  } finally {
    Module._load = originalLoad;
    delete require.cache[preloadPath];
    if (originalCached) require.cache[preloadPath] = originalCached;
  }
});

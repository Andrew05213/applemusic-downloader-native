'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');
const path = require('node:path');

const preloadPath = path.resolve(__dirname, '..', '..', 'src', 'preload', 'setup-preload.js');

test('setup preload exposes only fixed setup IPC methods including read-only diagnostics', async () => {
  const originalLoad = Module._load;
  const originalCached = require.cache[preloadPath];
  const calls = [];
  let exposedName;
  let exposedAPI;
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
              return channel === 'setup:get-diagnostics' ? 'sanitized diagnostics' : null;
            },
            on() {},
            removeListener() {},
          },
        };
      }
      return originalLoad.call(this, request, parent, isMain);
    };

    require(preloadPath);
    assert.equal(exposedName, 'applianceSetup');
    assert.equal(Object.isFrozen(exposedAPI), true);
    assert.equal(await exposedAPI.getDiagnostics(), 'sanitized diagnostics');
    assert.equal(await exposedAPI.repairRuntime(), null);
    assert.deepEqual(calls, [
      { channel: 'setup:get-diagnostics', args: [] },
      { channel: 'setup:repair-runtime', args: [] },
    ]);
  } finally {
    Module._load = originalLoad;
    delete require.cache[preloadPath];
    if (originalCached) require.cache[preloadPath] = originalCached;
  }
});

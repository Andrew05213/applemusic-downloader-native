'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');
const path = require('node:path');

const preloadPath = path.resolve(__dirname, '..', '..', 'src', 'preload', 'apple-music-preload.js');

test('remote preload exposes no desktop bridge and cannot send a click without a Play anchor', () => {
  const originalLoad = Module._load;
  const originalDocument = global.document;
  const originalCached = require.cache[preloadPath];
  let pageState;
  let click;
  let button;
  const sent = [];
  let exposed = false;
  global.document = {
    readyState: 'complete',
    documentElement: { append() {} },
    querySelectorAll: () => [],
    createElement(tag) {
      const node = {
        hidden: false,
        style: {},
        setAttribute() {},
        append() {},
        addEventListener(name, handler) { if (name === 'click') click = handler; },
        attachShadow() { return { append() {} }; },
      };
      if (tag === 'button') button = node;
      return node;
    },
  };
  try {
    delete require.cache[preloadPath];
    Module._load = function load(request, parent, isMain) {
      if (request === 'electron') return {
        ipcRenderer: {
          on(channel, handler) { assert.equal(channel, 'amdl:music-page-state'); pageState = handler; },
          send(channel, generation) { sent.push({ channel, generation }); },
        },
        contextBridge: { exposeInMainWorld() { exposed = true; } },
      };
      return originalLoad.call(this, request, parent, isMain);
    };
    // The positioning tests use richer DOM/observer fixtures. This test only
    // checks that no generic bridge is exposed when no Play anchor exists.
    const originals = { window: global.window, MutationObserver: global.MutationObserver, ResizeObserver: global.ResizeObserver, requestAnimationFrame: global.requestAnimationFrame };
    global.window = { addEventListener() {} };
    global.MutationObserver = global.ResizeObserver = class { observe() {} };
    global.requestAnimationFrame = () => 1;
    try {
      require(preloadPath);
      assert.equal(exposed, false);
      assert.equal(button.hidden, true);
      pageState({}, { generation: 4, queueable: true });
      assert.equal(button.hidden, true);
      click({ isTrusted: false });
      assert.deepEqual(sent, []);
      click({ isTrusted: true });
      assert.deepEqual(sent, []);
      pageState({}, { generation: 5, queueable: false });
      assert.equal(button.hidden, true);
    } finally {
      for (const [key, value] of Object.entries(originals)) {
        if (value === undefined) delete global[key]; else global[key] = value;
      }
    }
  } finally {
    Module._load = originalLoad;
    global.document = originalDocument;
    delete require.cache[preloadPath];
    if (originalCached) require.cache[preloadPath] = originalCached;
  }
});

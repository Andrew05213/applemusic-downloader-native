'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const mainModulePath = path.resolve(__dirname, '..', '..', 'src', 'main', 'main.js');

function loadMainWithWindowMock() {
  const originalLoad = Module._load;
  const originalCachedMain = require.cache[mainModulePath];
  const windows = [];

  class MockWebContents {
    constructor() {
      this.handlers = new Map();
      this.currentURL = '';
      this.mainFrame = { url: '' };
    }

    setWindowOpenHandler(handler) { this.windowOpenHandler = handler; }
    on(name, handler) { this.handlers.set(name, handler); }
    getURL() { return this.currentURL; }
  }

  class MockBrowserWindow {
    constructor() {
      this.webContents = new MockWebContents();
      this.handlers = new Map();
      this.destroyed = false;
      windows.push(this);
    }

    static getAllWindows() { return windows.filter((window) => !window.destroyed); }
    on(name, handler) { this.handlers.set(name, handler); }
    once(name, handler) { this.handlers.set(name, handler); }
    removeMenu() {}
    show() { this.shown = true; }
    focus() { this.focused = true; }
    isDestroyed() { return this.destroyed; }
    loadURL(url) {
      this.webContents.currentURL = url;
      this.webContents.mainFrame.url = url;
      return Promise.resolve();
    }
    close() {
      this.destroyed = true;
      this.handlers.get('closed')?.();
    }
  }

  delete require.cache[mainModulePath];
  Module._load = function load(request, parent, isMain) {
    if (request === 'electron') {
      return {
        app: {
          requestSingleInstanceLock: () => true,
          whenReady: () => new Promise(() => {}),
          on() {},
          quit() {},
        },
        BrowserWindow: MockBrowserWindow,
        Menu: {},
        dialog: {},
        ipcMain: {},
        safeStorage: {},
      };
    }
    return originalLoad.call(this, request, parent, isMain);
  };

  const loaded = require(mainModulePath);
  return {
    ...loaded,
    MockBrowserWindow,
    restore() {
      Module._load = originalLoad;
      delete require.cache[mainModulePath];
      if (originalCachedMain) require.cache[mainModulePath] = originalCachedMain;
    },
    windows,
  };
}

function eventFor(window, url, senderFrame = window.webContents.mainFrame) {
  window.webContents.currentURL = url;
  senderFrame.url = url;
  return { sender: window.webContents, senderFrame };
}

test('privileged setup and credential pages require the exact window, URL, and main frame', () => {
  const harness = loadMainWithWindowMock();
  try {
    for (const expectedURL of [harness.SETUP_PAGE_URL, harness.CREDENTIALS_PAGE_URL]) {
      const expectedWindow = new harness.MockBrowserWindow();
      assert.doesNotThrow(() => harness.assertTrustedFrame(
        eventFor(expectedWindow, expectedURL),
        expectedURL,
        expectedWindow,
      ));

      const filename = path.basename(new URL(expectedURL).pathname);
      const sameNameElsewhere = pathToFileURL(path.join(__dirname, 'untrusted', filename)).href;
      assert.throws(() => harness.assertTrustedFrame(
        eventFor(expectedWindow, sameNameElsewhere),
        expectedURL,
        expectedWindow,
      ), /not allowed/);

      const wrongWindow = new harness.MockBrowserWindow();
      assert.throws(() => harness.assertTrustedFrame(
        eventFor(wrongWindow, expectedURL),
        expectedURL,
        expectedWindow,
      ), /not allowed/);

      const childFrame = { url: expectedURL };
      assert.throws(() => harness.assertTrustedFrame(
        eventFor(expectedWindow, expectedURL, childFrame),
        expectedURL,
        expectedWindow,
      ), /not allowed/);

      assert.throws(() => harness.assertTrustedFrame(
        eventFor(expectedWindow, `${expectedURL}#after-navigation`),
        expectedURL,
        expectedWindow,
      ), /not allowed/);
    }
  } finally {
    harness.restore();
  }
});

test('privileged windows reject every navigation except their canonical file URL', () => {
  const harness = loadMainWithWindowMock();
  try {
    const window = new harness.MockBrowserWindow();
    harness.restrictWindowToPage(window, harness.SETUP_PAGE_URL);
    assert.deepEqual(window.webContents.windowOpenHandler(), { action: 'deny' });

    for (const eventName of ['will-navigate', 'will-redirect']) {
      const handler = window.webContents.handlers.get(eventName);
      assert.equal(typeof handler, 'function');
      let prevented = false;
      handler({ preventDefault: () => { prevented = true; } }, harness.SETUP_PAGE_URL);
      assert.equal(prevented, false, 'the original page remains usable');

      for (const rejected of [
        `${harness.SETUP_PAGE_URL}?unexpected=1`,
        `${harness.SETUP_PAGE_URL}#unexpected`,
        `file://localhost${new URL(harness.SETUP_PAGE_URL).pathname}`,
        'https://example.test/setup.html',
      ]) {
        prevented = false;
        handler({ preventDefault: () => { prevented = true; } }, rejected);
        assert.equal(prevented, true, `${eventName} must reject ${rejected}`);
      }
    }
  } finally {
    harness.restore();
  }
});

test('the service window cancels server redirects to an untrusted origin', () => {
  const harness = loadMainWithWindowMock();
  try {
    const window = new harness.MockBrowserWindow();
    harness.installMainServiceNavigationGuard(window);
    const redirectHandler = window.webContents.handlers.get('will-redirect');
    assert.equal(typeof redirectHandler, 'function');

    let prevented = false;
    redirectHandler({ preventDefault: () => { prevented = true; } }, 'https://example.test/redirect-target');
    assert.equal(prevented, true);
  } finally {
    harness.restore();
  }
});

test('credential window reference is reused while open and cleared when closed', () => {
  const harness = loadMainWithWindowMock();
  try {
    const first = harness.openCredentialsWindow();
    assert.equal(first.webContents.getURL(), harness.CREDENTIALS_PAGE_URL);
    assert.equal(harness.openCredentialsWindow(), first);
    first.close();
    const replacement = harness.openCredentialsWindow();
    assert.notEqual(replacement, first);
    replacement.close();
  } finally {
    harness.restore();
  }
});

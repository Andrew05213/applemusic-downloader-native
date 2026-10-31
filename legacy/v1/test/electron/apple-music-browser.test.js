'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { createAppleMusicBrowser, isAllowedAppleMusicPage, queueableAppleMusicPage } = require('../../electron/apple-music-browser');

class FakeContents extends EventEmitter {
  constructor() {
    super();
    this.currentURL = '';
    this.sent = [];
    this.session = { setPermissionRequestHandler: (handler) => { this.permissionHandler = handler; } };
  }
  send(channel, payload) { this.sent.push({ channel, payload }); }
  setWindowOpenHandler(handler) { this.windowHandler = handler; }
  loadURL(url) { this.currentURL = url; return Promise.resolve(); }
  getURL() { return this.currentURL; }
  isDestroyed() { return Boolean(this.closed); }
  close() { this.closed = true; this.closeCount = (this.closeCount || 0) + 1; }
}

class FakeView {
  constructor(options) { this.options = options; this.webContents = new FakeContents(); }
  setBounds(bounds) { this.bounds = bounds; }
}

function fakeOwner() {
  const children = new Set();
  return {
    children,
    webContents: new EventEmitter(),
    contentView: {
      addChildView: (view) => children.add(view),
      removeChildView: (view) => children.delete(view),
    },
    getContentBounds: () => ({ x: 0, y: 0, width: 1200, height: 800 }),
  };
}

test('embedded browser accepts only the exact public Apple Music HTTPS origin', () => {
  for (const url of ['http://music.apple.com/us/album/123', 'https://music.apple.com.evil.test/us/album/123',
    'https://name@music.apple.com/us/album/123', 'https://music.apple.com:443/us/album/123',
    'https://127.0.0.1/', 'javascript:alert(1)', 'https://idmsa.apple.com/']) {
    assert.equal(isAllowedAppleMusicPage(url), false, url);
  }
  assert.equal(isAllowedAppleMusicPage('https://music.apple.com/us/album/123'), true);
});

test('embedded browser isolates remote pages and shows only sanitized page URLs to the trusted host', async () => {
  const owner = fakeOwner();
  const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
  await browser.open();
  const [view] = [...owner.children];
  assert.ok(view);
  assert.equal(view.options.webPreferences.nodeIntegration, false);
  assert.equal(view.options.webPreferences.contextIsolation, true);
  assert.equal(view.options.webPreferences.sandbox, true);
  assert.match(view.options.webPreferences.preload, /apple-music-preload\.js$/);
  assert.equal(view.options.webPreferences.partition, 'persist:amdl-apple-music-browser');
  assert.deepEqual(view.webContents.windowHandler(), { action: 'deny' });
  let permitted;
  view.webContents.permissionHandler(view.webContents, 'media', (allowed) => { permitted = allowed; });
  assert.equal(permitted, false);
  const blocked = { prevented: false, preventDefault() { this.prevented = true; } };
  view.webContents.emit('will-navigate', blocked, 'https://example.test/');
  assert.equal(blocked.prevented, true);
  view.webContents.currentURL = 'https://music.apple.com/us/album/123';
  assert.equal(browser.currentURL(), 'https://music.apple.com/us/album/123');
  view.webContents.currentURL = 'https://example.test/private?token=secret';
  assert.equal(browser.currentURL(), '');
  browser.setBounds({ x: 220, y: 180, width: 700, height: 500 });
  assert.deepEqual(view.bounds, { x: 220, y: 180, width: 700, height: 500 });
  browser.hide();
  assert.equal(owner.children.size, 0);
  browser.destroy();
  assert.equal(view.webContents.closed, true);
});

test('browser recreation uses the same dedicated persistent partition', async () => {
  const partitions = [];
  for (let index = 0; index < 2; index += 1) {
    const owner = fakeOwner();
    const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
    await browser.open();
    const [view] = [...owner.children];
    partitions.push(view.options.webPreferences.partition);
    browser.destroy();
  }
  assert.deepEqual(partitions, [
    'persist:amdl-apple-music-browser',
    'persist:amdl-apple-music-browser',
  ]);
});

test('embedded browser rejects bounds outside its owner before attaching a view', async () => {
  const owner = fakeOwner();
  const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
  await browser.open();
  assert.throws(() => browser.setBounds({ x: -1, y: 0, width: 500, height: 400 }), /bounds/);
  assert.throws(() => browser.setBounds({ x: 100, y: 700, width: 900, height: 200 }), /bounds/);
  browser.destroy();
});

test('owner navigation detaches the remote browser before a service page changes', async () => {
  const owner = fakeOwner();
  const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
  await browser.open();
  assert.equal(owner.children.size, 1);
  owner.webContents.emit('did-start-navigation');
  assert.equal(owner.children.size, 0);
  browser.destroy();
  assert.equal(owner.webContents.listenerCount('did-start-navigation'), 0);
});

test('remote download intent requires the visible owner main frame and the current navigation generation', async () => {
  const owner = fakeOwner();
  const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
  await browser.open();
  const [view] = [...owner.children];
  view.webContents.mainFrame = { url: 'https://music.apple.com/us/album/demo/123' };
  view.webContents.currentURL = view.webContents.mainFrame.url;
  const generation = browser.generation();
  const event = { sender: view.webContents, senderFrame: view.webContents.mainFrame };
  assert.equal(browser.acceptDownloadIntent(event, generation), 'https://music.apple.com/us/album/demo/123');
  assert.equal(browser.acceptDownloadIntent({ ...event, senderFrame: {} }, generation), '');
  assert.equal(browser.acceptDownloadIntent(event, generation + 1), '');
  view.webContents.currentURL = 'https://music.apple.com/us/album/changed/456';
  view.webContents.emit('did-navigate-in-page');
  assert.equal(browser.acceptDownloadIntent(event, generation), '');
  browser.hide();
  assert.equal(browser.acceptDownloadIntent(event, browser.generation()), '');
});

test('reopening a hidden browser publishes a fresh generation to its existing page', async () => {
  const owner = fakeOwner();
  const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
  await browser.open();
  const [view] = [...owner.children];
  const contents = view.webContents;
  contents.currentURL = 'https://music.apple.com/us/song/demo/123';
  contents.mainFrame = { url: contents.currentURL };
  contents.emit('did-finish-load');
  const before = contents.sent.at(-1).payload.generation;
  browser.hide();
  await browser.open();
  const after = contents.sent.at(-1).payload.generation;
  assert.notEqual(after, before, 'the injected button must receive the reopened generation');
  assert.equal(browser.acceptDownloadIntent({ sender: contents, senderFrame: contents.mainFrame }, after), contents.currentURL);
  browser.destroy();
});

test('main-frame navigation hides the old action; child navigation does not', async () => {
  const owner = fakeOwner();
  const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
  await browser.open();
  const [view] = [...owner.children];
  view.webContents.currentURL = 'https://music.apple.com/us/album/demo/123';
  view.webContents.emit('did-finish-load');
  const before = browser.generation();
  view.webContents.emit('did-start-navigation', {}, 'https://music.apple.com/us/album/demo/456', true, false);
  assert.equal(browser.generation(), before);
  view.webContents.emit('did-start-navigation', {}, 'https://music.apple.com/us/album/demo/456', true, true);
  assert.deepEqual(view.webContents.sent.at(-1).payload, { generation: before + 1, queueable: false });
  assert.equal(browser.currentURL(), '');
  view.webContents.emit('did-navigate-in-page', {}, 'https://music.apple.com/us/album/demo/456', false);
  assert.equal(browser.generation(), before + 1);
  assert.equal(browser.currentURL(), '', 'child navigation cannot complete a pending main navigation');
  view.webContents.emit('did-navigate-in-page', {}, view.webContents.currentURL, true);
  assert.equal(view.webContents.sent.at(-1).payload.queueable, true);
  browser.destroy();
});

test('current-page download URL strips tracking and rejects unsupported resource or sensitive query shapes', () => {
  assert.equal(queueableAppleMusicPage('https://music.apple.com/us/album/demo/123?i=456&itsct=share'), 'https://music.apple.com/us/album/demo/123?i=456');
  assert.equal(queueableAppleMusicPage('https://music.apple.com/us/playlist/demo/pl.u-abc?l=zh-CN'), 'https://music.apple.com/us/playlist/demo/pl.u-abc?l=zh-CN');
  for (const url of [
    'https://music.apple.com/us/playlist/demo/p.private',
    'https://music.apple.com/us/library/playlist/demo/p.private',
    'https://music.apple.com/us/album/demo/not-a-number',
    'https://music.apple.com/us/station/demo/not-a-station',
    'https://music.apple.com/us/album/demo/123?i=1&i=2',
    'https://music.apple.com/us/song/demo/123?l=zh-CN',
    'https://music.apple.com/us/album/demo/123?token=SENTINEL',
  ]) assert.equal(queueableAppleMusicPage(url), '', url);
});

test('closing an already destroyed owner never touches its native view or webContents', async () => {
  const owner = fakeOwner();
  let destroyed = false;
  const contentView = owner.contentView;
  const ownerContents = owner.webContents;
  owner.isDestroyed = () => destroyed;
  for (const [key, value] of [['contentView', contentView], ['webContents', ownerContents]]) {
    Object.defineProperty(owner, key, { get() {
      if (destroyed) throw new TypeError('Object has been destroyed');
      return value;
    } });
  }
  const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
  await browser.open();
  const [view] = [...owner.children];
  destroyed = true;
  assert.doesNotThrow(() => browser.destroy());
  assert.doesNotThrow(() => browser.destroy());
  assert.doesNotThrow(() => browser.hide());
  assert.equal(view.webContents.closeCount, 1);
  assert.equal(ownerContents.listenerCount('did-start-navigation'), 0);
  assert.equal(browser.currentURL(), '');
  await assert.rejects(browser.open(), /unavailable/);
});

test('destroy is idempotent with either a live or already closed child', async () => {
  for (const childAlreadyClosed of [false, true]) {
    const owner = fakeOwner();
    const browser = createAppleMusicBrowser({ owner, WebContentsView: FakeView });
    await browser.open();
    const [view] = [...owner.children];
    if (childAlreadyClosed) view.webContents.close();
    browser.destroy();
    browser.destroy();
    assert.equal(owner.children.size, 0);
    assert.equal(view.webContents.closeCount, 1);
    await assert.rejects(browser.open(), /unavailable/);
  }
});

test('closing during page load cannot reopen the child or publish late page state', async () => {
  const owner = fakeOwner();
  let finishLoad;
  class LoadingView extends FakeView {
    constructor(options) {
      super(options);
      this.webContents.loadURL = () => new Promise((resolve) => { finishLoad = resolve; });
    }
  }
  const browser = createAppleMusicBrowser({ owner, WebContentsView: LoadingView });
  const opening = browser.open();
  const [view] = [...owner.children];
  browser.destroy();
  view.webContents.emit('did-finish-load');
  view.webContents.emit('did-navigate-in-page');
  finishLoad();
  await assert.rejects(opening, /unavailable/);
  assert.equal(view.webContents.sent.length, 0);
  assert.equal(owner.children.size, 0);
  assert.equal(view.webContents.closeCount, 1);
});

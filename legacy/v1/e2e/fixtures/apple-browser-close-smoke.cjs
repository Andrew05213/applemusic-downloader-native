'use strict';

// Run with Electron, not Node. Hidden windows, local HTML only, no application startup/WSL/credentials.
const assert = require('node:assert/strict');
const { app, BrowserWindow, WebContentsView } = require('electron');
const { createAppleMusicBrowser } = require('../../electron/apple-music-browser');

app.disableHardwareAcceleration();
app.on('window-all-closed', () => {});
class OfflineView extends WebContentsView {
  constructor(options) {
    super(options);
    const loadURL = this.webContents.loadURL.bind(this.webContents);
    this.webContents.loadURL = () => loadURL('data:text/html,<title>Offline lifecycle fixture</title>');
  }
}

app.whenReady().then(async () => {
  for (let iteration = 0; iteration < 5; ++iteration) {
    for (const order of ['owner-first', 'browser-first', 'child-first']) {
      const owner = new BrowserWindow({ show: false });
      const browser = createAppleMusicBrowser({ owner, WebContentsView: OfflineView });
      await browser.open();
      const [child] = owner.contentView.children;
      assert.ok(child);
      if (order === 'owner-first') owner.destroy();
      if (order === 'child-first') child.webContents.close();
      browser.destroy();
      browser.destroy();
      browser.hide();
      assert.equal(browser.currentURL(), '');
      await assert.rejects(browser.open(), /unavailable/);
      if (!owner.isDestroyed()) owner.destroy();
    }
  }
  const owner = new BrowserWindow({ show: false });
  let finishLoad;
  class PendingView extends OfflineView {
    constructor(options) {
      super(options);
      this.webContents.loadURL = () => new Promise((resolve) => { finishLoad = resolve; });
    }
  }
  const browser = createAppleMusicBrowser({ owner, WebContentsView: PendingView });
  const opening = browser.open();
  owner.destroy();
  browser.destroy();
  finishLoad();
  await assert.rejects(opening, /unavailable/);
  console.log('Native Electron close smoke: 16 cases passed; local HTML only.');
  app.exit(0);
}).catch((error) => {
  console.error(error);
  app.exit(1);
});

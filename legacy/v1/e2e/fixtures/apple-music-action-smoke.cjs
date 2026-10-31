'use strict';

const { app, BrowserWindow, ipcMain } = require('electron');
const assert = require('node:assert/strict');
const path = require('node:path');

// Real sandboxed preload, offline document, genuine Chromium mouse events.
// Does not load the product main process, WSL, credentials or a media player.
app.whenReady().then(async () => {
  const window = new BrowserWindow({ width: 1000, height: 700, show: false,
    webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false,
      partition: 'amdl-action-offline-test', preload: path.resolve(__dirname, '../../src/preload/apple-music-preload.js') } });
  const messages = [];
  ipcMain.on('desktop:music-download-click', (event, generation) => {
    assert.equal(event.sender, window.webContents);
    messages.push(generation);
  });
  const html = `<style>body{margin:0}.container-detail-header{margin:24px;position:relative;height:300px}
    .primary-actions{position:absolute;left:0;bottom:0}.secondary-actions{position:absolute;right:0;bottom:0}
    button{height:32px;width:100px}.secondary-actions button{width:32px}</style>
    <div class="container-detail-header"><div class="primary-actions"><div class="primary-actions__button--play"><button>Play</button></div></div>
    <div class="secondary-actions"><button class="contextual-menu__trigger">More</button></div></div>`;
  await window.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(html));
  window.webContents.send('amdl:music-page-state', { generation: 7, queueable: true });
  const layout = await window.webContents.executeJavaScript(`new Promise(resolve => {
    function check(){const h=document.querySelector('[data-amdl-download-action]');
      if(!h||h.style.display!=='block'){requestAnimationFrame(check);return;}
      const a=h.getBoundingClientRect(),p=document.querySelector('.primary-actions button').getBoundingClientRect();
      resolve({x:a.x,y:a.y,height:a.height,playY:p.y,playHeight:p.height,closed:h.shadowRoot===null,
        bridge:typeof window.desktopShell,require:typeof window.require});}check();})`);
  assert.equal(layout.closed, true);
  assert.equal(layout.bridge, 'undefined');
  assert.equal(layout.require, 'undefined');
  assert.equal(layout.y + layout.height / 2, layout.playY + layout.playHeight / 2);
  const received = new Promise((resolve) => ipcMain.once('desktop:music-download-click', resolve));
  window.webContents.sendInputEvent({ type: 'mouseDown', x: Math.round(layout.x + 16), y: Math.round(layout.y + 16), button: 'left', clickCount: 1 });
  window.webContents.sendInputEvent({ type: 'mouseUp', x: Math.round(layout.x + 16), y: Math.round(layout.y + 16), button: 'left', clickCount: 1 });
  await Promise.race([received, new Promise((_, reject) => setTimeout(() => reject(new Error('genuine click timeout')), 5000))]);
  assert.deepEqual(messages, [7]);
  window.webContents.send('amdl:music-page-state', { generation: 8, queueable: false });
  await window.webContents.executeJavaScript('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
  assert.equal(await window.webContents.executeJavaScript("document.querySelector('[data-amdl-download-action]').style.display"), 'none');
  console.log('PASS: real sandboxed preload, closed shadow, Play alignment, genuine click generation and navigation hiding');
  window.destroy(); app.quit();
}).catch((error) => { console.error(error); app.exit(1); });

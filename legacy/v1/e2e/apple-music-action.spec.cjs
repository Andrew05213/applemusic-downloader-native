'use strict';

const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');
const preload = fs.readFileSync(path.resolve(__dirname, '../src/preload/apple-music-preload.js'), 'utf8');

// Offline DOM fixture uses semantic classes observed on the anonymous public
// Apple Music page; no Apple credentials, service, player or network requests.
async function install(page) {
  await page.setContent(`<style>
    body{margin:0;background:#202020;color:white;font-family:system-ui;height:1600px}
    .container-detail-header{margin:24px;position:relative;height:300px}
    h1{padding-top:40px}.primary-actions{position:absolute;left:0;bottom:0;display:flex;gap:10px}
    button{height:32px;border:0;border-radius:4px;background:#fa2d48;color:white;padding:0 20px}
    .secondary-actions{position:absolute;right:0;bottom:0}.secondary-actions button{width:32px;padding:0;background:transparent}
    </style><main><div class="container-detail-header"><h1>Fixture Playlist</h1>
    <div class="primary-actions"><div class="primary-actions__button--play"><button>播放</button></div><button>随机播放</button></div>
    <div class="secondary-actions"><button class="contextual-menu__trigger">⋯</button></div></div></main>`);
  await page.evaluate((source) => {
    window.fixtureMessages = [];
    const ipcRenderer = { on(_channel, handler) { window.fixtureState = (state) => handler({}, state); },
      send(channel, generation) { window.fixtureMessages.push({ channel, generation }); } };
    // Only the offline test has a mock bridge. The real preload exposes none.
    new Function('require', source)(() => ({ ipcRenderer }));
    window.fixtureState({ generation: 4, queueable: true });
  }, preload);
}

async function aligned(page) {
  await expect(page.locator('[data-amdl-download-action]')).toBeVisible();
  // Resize schedules one animation frame. Read all rectangles atomically and
  // wait for the layout contract, not a mixture of old/new viewport snapshots.
  await expect.poll(() => page.evaluate(() => {
    const a = document.querySelector('[data-amdl-download-action]').getBoundingClientRect();
    const p = document.querySelector('.primary-actions__button--play button').getBoundingClientRect();
    const m = document.querySelector('.contextual-menu__trigger').getBoundingClientRect();
    return a.width === 32 && Math.abs(a.y + a.height / 2 - p.y - p.height / 2) <= 1 && a.right <= m.x - 7;
  })).toBe(true);
  return page.locator('[data-amdl-download-action]').boundingBox();
}

for (const width of [1280, 390]) {
  test(`page arrow aligns with Play at ${width}px and real click sends only generation`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await install(page);
    const action = await aligned(page);
    expect(await page.locator('[data-amdl-download-action]').evaluate((host) => host.shadowRoot)).toBeNull();
    await page.locator('[data-amdl-download-action]').evaluate((host) => host.click());
    expect(await page.evaluate(() => window.fixtureMessages)).toEqual([]);
    await page.mouse.click(action.x + 16, action.y + 16);
    expect(await page.evaluate(() => window.fixtureMessages)).toEqual([{ channel: 'desktop:music-download-click', generation: 4 }]);
    await page.evaluate(() => window.fixtureState({ generation: 5, queueable: false }));
    await expect(page.locator('[data-amdl-download-action]')).toBeHidden();
  });
}

test('SPA header replacement, resize and scroll keep one action on the current Play row', async ({ page }) => {
  await install(page); await aligned(page);
  await page.evaluate(() => {
    const old = document.querySelector('.container-detail-header');
    const next = old.cloneNode(true); next.style.height = '400px'; old.replaceWith(next);
    window.fixtureState({ generation: 6, queueable: true });
  });
  await expect.poll(async () => (await page.locator('[data-amdl-download-action]').boundingBox()).y).toBe(392);
  await page.setViewportSize({ width: 600, height: 800 }); await aligned(page);
  await page.evaluate(() => window.scrollTo(0, 600));
  await expect(page.locator('[data-amdl-download-action]')).toBeHidden();
  await page.evaluate(() => window.scrollTo(0, 0)); await aligned(page);
  await page.locator('[data-amdl-download-action]').evaluate((host) => host.remove());
  await expect(page.locator('[data-amdl-download-action]')).toHaveCount(1);
  await aligned(page);
  await page.locator('.container-detail-header').evaluate((header) => header.remove());
  await expect(page.locator('[data-amdl-download-action]')).toBeHidden();
});

test('song rows and mini-player never substitute for a missing detail-header Play', async ({ page }) => {
  await install(page);
  await page.locator('.container-detail-header').evaluate((header) => { header.className = 'track-row'; });
  await expect(page.locator('[data-amdl-download-action]')).toBeHidden();
});

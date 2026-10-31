'use strict';

const { test, expect } = require('@playwright/test');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const renderer = pathToFileURL(path.resolve(__dirname, '../src/renderer/native-index.html')).href;

async function install(page, { delayedDetails = false, detailPages = false } = {}) {
  // This suite exercises local navigation, not Apple's network or actual playback.
  await page.route(/^https?:\/\//, route => route.fulfill({ status: 204, body: '' }));
  await page.addInitScript(({ delayedDetails, detailPages }) => {
    HTMLMediaElement.prototype.play = function () { return Promise.resolve(); };
    HTMLMediaElement.prototype.pause = function () {};
    HTMLMediaElement.prototype.load = function () {};
    const item = (id, title, type = 'albums') => ({
      id, title, type, artist: 'Fixture Artist', downloadable: true,
      url: type === 'library-playlists'
        ? 'https://music.apple.com/us/library/playlists/' + id
        : 'https://music.apple.com/us/' + ({ albums: 'album', playlists: 'playlist', songs: 'song' }[type]) + '/fixture/' + id,
      duration: 180000, previewUrl: 'https://audio-ssl.itunes.apple.com/navigation-fixture.wav'
    });
    const album = item('300', 'Fixture Album');
    const playlist = item('pl.301', 'Fixture Playlist', 'playlists');
    const library = item('p.302', 'Private Playlist', 'library-playlists');
    const song = item('101', 'Fixture Song', 'songs');
    window.navigationCalls = [];
    window.navigationLoginOpens = 0;
    window.pendingDetails = [];
    window.releaseDetails = async () => {
      window.pendingDetails.splice(0).forEach(resolve => resolve());
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    };
    window.nativeDesktop = {
      getState: async () => ({ state: 'authenticated', region: 'us' }),
      onSession(fn) { window.navigationSessionUpdate = fn; }, onReady(fn) { queueMicrotask(fn); },
      openLogin: async () => { window.navigationLoginOpens++; }, exportDiagnostics: async () => true,
      recheck: async () => ({ state: 'authenticated', region: 'us' }),
      api: async (target, options = {}) => {
        window.navigationCalls.push({ target, ...options });
        const url = new URL(target, 'http://fixture');
        if (url.pathname === '/api/v1/capabilities') return { queue: { state: 'ready' }, jobExecutorConfigured: true, qualities: { alac: { available: true }, 'aac-lc': { available: true } } };
        if (url.pathname === '/api/v1/catalog/home') return { groups: [{ title: 'Recently Played', type: 'albums', items: [album, playlist, song] }] };
        if (url.pathname === '/api/v1/catalog/library') return { groups: [{ type: 'library-playlists', items: [library] }] };
        if (url.pathname === '/api/v1/catalog/search') {
          const secondPage = url.searchParams.get('offset') === '1';
          const items = Array.from({ length: 12 }, (_, index) => {
            const number = index + (secondPage ? 13 : 1);
            return item(String(400 + number), 'Search Album ' + number);
          });
          return { groups: [{ type: 'albums', items, ...(secondPage ? {} : { nextOffset: 1 }) }] };
        }
        if (['/api/v1/catalog/detail', '/api/v1/catalog/library-detail'].includes(url.pathname)) {
          const id = url.searchParams.get('id');
          const resource = [album, playlist, library].find(value => value.id === id) || item(id, 'Search Album ' + (Number(id) - 400));
          const secondPage = url.searchParams.get('offset') === '1';
          const data = { resource, tracks: secondPage ? [item('703', 'Late Detail Song', 'songs')] : [item('701', 'Detail Song One', 'songs'), item('702', 'Detail Song Two', 'songs')], ...(detailPages && !secondPage ? { nextOffset: 1 } : {}) };
          if (delayedDetails || (detailPages && secondPage)) return new Promise(resolve => window.pendingDetails.push(() => resolve(data)));
          return data;
        }
        if (url.pathname === '/api/v1/browser-download/resolve') return { url: options.body.url };
        if (url.pathname === '/api/v1/jobs' && options.method === 'POST') return { jobs: [{ id: 'fixture-job', url: options.body.urls[0] }] };
        if (url.pathname === '/api/native/playback') {
          if (options.method === 'DELETE') return { ok: true };
          return { ok: true, mode: 'full', mediaURL: 'http://127.0.0.1:45678/api/native/playback/' + 'a'.repeat(32) };
        }
        throw new Error('Unexpected local navigation fixture request: ' + url.pathname);
      }
    };
  }, { delayedDetails, detailPages });
  await page.goto(renderer);
  await expect(page.getByRole('heading', { name: 'Recently Played', exact: true })).toBeVisible();
}

function artwork(page, title) {
  return page.locator('.catalog-artwork-action').filter({ has: page.locator('img') }).and(page.getByRole('button', { name: '查看 ' + title + ' 详情', exact: true }));
}

function titleAction(page, title) {
  return page.locator('.catalog-title-action').and(page.getByRole('button', { name: '查看 ' + title + ' 详情', exact: true }));
}

async function catalogCalls(page) {
  return page.evaluate(() => window.navigationCalls.filter(call => call.target.startsWith('/api/v1/catalog/')));
}

async function detailCalls(page) {
  return (await catalogCalls(page)).filter(call => /\/catalog\/(?:library-)?detail\?/.test(call.target));
}

for (const example of [
  { title: 'Fixture Album', id: '300', type: 'albums' },
  { title: 'Fixture Playlist', id: 'pl.301', type: 'playlists' },
  { title: 'Private Playlist', id: 'p.302', type: 'library-playlists' }
]) {
  test(`${example.type} opens from its cover or keyboard title without a separate open button`, async ({ page }) => {
    await install(page);
    if (example.type === 'library-playlists') await page.getByRole('button', { name: '我的歌单', exact: true }).click();
    const cover = artwork(page, example.title);
    const title = titleAction(page, example.title);
    const card = page.locator('.catalog-card').filter({ has: title });
    await expect(card.locator('[data-download], .card-actions')).toHaveCount(0);
    await expect(page.getByRole('button', { name: '打开', exact: true })).toHaveCount(0);
    await expect(cover).toBeVisible();
    await expect(title).toBeVisible();
    const listCalls = (await catalogCalls(page)).length;
    await cover.click();
    await expect(page.locator('.detail-hero h1')).toHaveText(example.title);
    const downloadAll = page.locator('.detail-hero').getByRole('button', { name: '下载 ' + example.title, exact: true });
    await expect(downloadAll).toHaveText('下载全部');
    await downloadAll.click();
    await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
    expect(await page.evaluate(() => window.navigationCalls.filter(call => call.target === '/api/v1/jobs' && call.method === 'POST').map(call => call.body.urls[0]))).toEqual([
      example.type === 'library-playlists' ? 'https://music.apple.com/us/library/playlists/' + example.id : 'https://music.apple.com/us/' + (example.type === 'albums' ? 'album' : 'playlist') + '/fixture/' + example.id
    ]);
    await expect(page.locator('#browse-back')).toHaveAccessibleName('返回上一页');
    await page.getByRole('button', { name: '返回上一页', exact: true }).click();
    await expect(cover).toBeFocused();
    await title.focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('.detail-hero h1')).toHaveText(example.title);
    await page.keyboard.press('Escape');
    await expect(title).toBeFocused();
    await expect(page.locator('.detail-hero')).toHaveCount(0);
    expect((await catalogCalls(page)).length).toBe(listCalls + 2);
    const calls = await detailCalls(page);
    expect(calls).toHaveLength(2);
    for (const call of calls) {
      const url = new URL(call.target, 'http://fixture');
      expect(url.pathname).toBe('/api/v1/catalog/' + (example.type === 'library-playlists' ? 'library-detail' : 'detail'));
      expect(url.searchParams.get('id')).toBe(example.id);
      expect(url.searchParams.get('type')).toBe(example.type === 'library-playlists' ? null : example.type);
    }
  });
}

test('cover accepts Space and the title accepts a mouse click, with neither action queuing a download', async ({ page }) => {
  await install(page);
  await artwork(page, 'Fixture Album').focus();
  await page.keyboard.press('Space');
  await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
  await page.getByRole('button', { name: '返回上一页', exact: true }).click();
  await titleAction(page, 'Fixture Album').click();
  await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
  expect(await page.evaluate(() => window.navigationCalls.some(call => call.target === '/api/v1/jobs' && call.method === 'POST'))).toBe(false);
});

test('closing detail restores the same paginated search, scroll position, input and trigger focus without a refetch', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await install(page);
  const search = page.getByRole('searchbox');
  await search.fill('saved search');
  await search.press('Enter');
  await expect(titleAction(page, 'Search Album 1')).toBeVisible();
  await page.getByRole('button', { name: '加载更多 albums', exact: true }).click();
  const lastTitle = titleAction(page, 'Search Album 24');
  await expect(lastTitle).toBeVisible();
  await expect(page.getByRole('button', { name: '加载更多 albums', exact: true })).toHaveCount(0);
  await search.fill('edited but not submitted');
  await lastTitle.scrollIntoViewIfNeeded();
  const before = await page.evaluate(() => {
    window.savedLastCard = [...document.querySelectorAll('.catalog-title-action')].find(control => control.textContent === 'Search Album 24');
    return { scroll: scrollY, rail: document.querySelector('.search-collection-grid').scrollLeft, calls: window.navigationCalls.filter(call => call.target.startsWith('/api/v1/catalog/')).length };
  });
  expect(before.rail).toBeGreaterThan(0);
  await lastTitle.click();
  await expect(page.locator('.detail-hero h1')).toHaveText('Search Album 24');
  await page.getByRole('button', { name: '返回上一页', exact: true }).click();
  await expect(search).toHaveValue('edited but not submitted');
  await expect(page.locator('.catalog-card')).toHaveCount(24);
  await expect(lastTitle).toBeFocused();
  await expect.poll(() => page.evaluate(expected => Math.abs(scrollY - expected), before.scroll), { timeout: 2000 }).toBeLessThanOrEqual(2);
  await expect.poll(() => page.locator('.search-collection-grid').evaluate(el => el.scrollLeft)).toBe(before.rail);
  expect(await page.evaluate(() => window.savedLastCard?.isConnected)).toBe(true);
  expect((await catalogCalls(page)).length).toBe(before.calls + 1);
});

test('closing an in-flight detail invalidates a late response and keeps the original list interactive', async ({ page }) => {
  await install(page, { delayedDetails: true });
  const cover = artwork(page, 'Fixture Album');
  await cover.click();
  await expect.poll(() => page.evaluate(() => window.pendingDetails.length)).toBe(1);
  await expect(page.getByRole('button', { name: '返回上一页', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '返回上一页', exact: true }).click();
  await expect(cover).toBeFocused();
  await page.evaluate(() => window.releaseDetails());
  await expect(page.locator('.detail-hero')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Recently Played', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '返回上一页', exact: true })).toBeDisabled();
  expect((await catalogCalls(page)).filter(call => call.target.startsWith('/api/v1/catalog/home'))).toHaveLength(1);
  await page.getByRole('button', { name: '下载 Fixture Song', exact: true }).click();
  await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
  expect(await detailCalls(page)).toHaveLength(1);
});

for (const destination of ['search', 'library']) {
  test(`switching to ${destination} discards the old detail return target and ignores its late response`, async ({ page }) => {
    await install(page, { delayedDetails: true });
    await artwork(page, 'Fixture Album').click();
    await expect.poll(() => page.evaluate(() => window.pendingDetails.length)).toBe(1);
    if (destination === 'search') {
      const search = page.getByRole('searchbox');
      await search.fill('replacement search');
      await search.press('Enter');
      await expect(titleAction(page, 'Search Album 1')).toBeVisible();
    } else {
      await page.getByRole('button', { name: '我的歌单', exact: true }).click();
      await expect(titleAction(page, 'Private Playlist')).toBeVisible();
    }
    await expect(page.getByRole('button', { name: '返回上一页', exact: true })).toBeVisible();
    if(destination==='search')await expect(page.locator('#browse-back')).toBeEnabled();else await expect(page.locator('#browse-back')).toBeDisabled();
    const calls = (await catalogCalls(page)).length;
    await page.evaluate(() => window.releaseDetails());
    await page.keyboard.press('Escape');
    await expect(page.locator('.detail-hero')).toHaveCount(0);
    await expect(titleAction(page, destination === 'search' ? 'Search Album 1' : 'Private Playlist')).toBeVisible();
    await expect(titleAction(page, 'Fixture Album')).toHaveCount(0);
    expect((await catalogCalls(page)).length).toBe(calls);
  });
}

test('closing during detail pagination cannot append its late tracks to the restored cards', async ({ page }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await install(page, { detailPages: true });
  await artwork(page, 'Fixture Album').click();
  await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
  await page.locator('#catalog-more').click();
  await expect.poll(() => page.evaluate(() => window.pendingDetails.length)).toBe(1);
  await page.getByRole('button', { name: '返回上一页', exact: true }).click();
  await page.evaluate(() => window.releaseDetails());
  await expect(artwork(page, 'Fixture Album')).toBeVisible();
  await expect(page.locator('.track-list,.detail-hero')).toHaveCount(0);
  await expect(page.getByText('Late Detail Song', { exact: true })).toHaveCount(0);
  await expect(page.locator('#catalog-more')).toBeHidden();
  expect((await catalogCalls(page)).filter(call => call.target.startsWith('/api/v1/catalog/home'))).toHaveLength(1);
  expect(errors).toEqual([]);
});

test('song actions and detail downloads never open or close a detail', async ({ page }) => {
  await install(page);
  await page.getByRole('button', { name: '下载 Fixture Song', exact: true }).click();
  await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
  await page.getByRole('button', { name: '播放 Fixture Song', exact: true }).click();
  await expect(page.locator('#player')).toBeVisible();
  await expect(page.locator('#player-title')).toHaveText('Fixture Song · Fixture Artist');
  expect(await detailCalls(page)).toHaveLength(0);
  await artwork(page, 'Fixture Album').click();
  await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
  await page.getByRole('button', { name: '播放 Detail Song One', exact: true }).click();
  await expect(page.locator('#player-title')).toHaveText('Detail Song One · Fixture Artist');
  await page.getByRole('button', { name: '下载 Detail Song Two', exact: true }).click();
  await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
  await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
  expect(await detailCalls(page)).toHaveLength(1);
  expect(await page.evaluate(() => window.navigationCalls.filter(call => call.target === '/api/native/playback' && call.method === 'POST').map(call => call.body.id))).toEqual(['101', '701']);
  expect(await page.evaluate(() => window.navigationCalls.filter(call => call.target === '/api/v1/jobs' && call.method === 'POST').length)).toBe(2);
});

test('session revalidation disables downloads including restored detail snapshots until verified again', async ({ page }) => {
  await install(page);
  const songDownload = page.getByRole('button', { name: '下载 Fixture Song', exact: true });
  const light = page.locator('#session-light');
  await expect(songDownload).toBeEnabled();
  await expect(light).toHaveAttribute('data-state', 'authenticated');

  await page.evaluate(() => window.navigationSessionUpdate({ state: 'checking' }));
  await expect(light).toHaveAttribute('data-state', 'checking');
  expect(await page.locator('[data-download]').evaluateAll(controls => controls.length > 0 && controls.every(control => control.disabled))).toBe(true);
  await page.evaluate(() => window.navigationSessionUpdate({ state: 'authenticated', region: 'us' }));
  await expect(songDownload).toBeEnabled();
  await expect(light).toHaveAttribute('data-state', 'authenticated');

  await page.evaluate(() => { window.savedDownloadControl = document.querySelector('.catalog-card [data-download]'); });
  await artwork(page, 'Fixture Album').click();
  await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
  await expect(page.getByRole('button', { name: '下载 Detail Song One', exact: true })).toBeEnabled();
  await page.evaluate(() => window.navigationSessionUpdate({ state: 'checking' }));
  expect(await page.locator('[data-download]').evaluateAll(controls => controls.length > 0 && controls.every(control => control.disabled))).toBe(true);
  await page.getByRole('button', { name: '返回上一页', exact: true }).click();
  await expect(songDownload).toBeDisabled();
  expect(await page.evaluate(() => window.savedDownloadControl?.isConnected && window.savedDownloadControl.disabled)).toBe(true);

  for (const state of ['unverified', 'needs_login']) {
    await page.evaluate(state => window.navigationSessionUpdate({ state }), state);
    await expect(light).toHaveAttribute('data-state', state);
    expect(await page.locator('[data-download]').evaluateAll(controls => controls.every(control => control.disabled))).toBe(true);
  }
  await page.evaluate(() => window.navigationSessionUpdate({ state: 'authenticated', region: 'us' }));
  await expect(songDownload).toBeEnabled();
  await expect(light).toHaveAttribute('data-state', 'authenticated');
  expect(await page.evaluate(() => window.navigationLoginOpens)).toBe(0);
  expect(await page.evaluate(() => window.navigationCalls.filter(call => call.target === '/api/v1/jobs' && call.method === 'POST'))).toEqual([]);
});

for (const width of [1280, 390]) {
  test(`detail has a standalone upper-left back control at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 720 });
    await install(page);
    await page.screenshot({ path: `test-results/native-navigation-cards-${width}.png`, fullPage: true });
    await artwork(page, 'Fixture Album').click();
    await expect(page.locator('.detail-hero h1')).toHaveText('Fixture Album');
    const close = page.getByRole('button', { name: '返回上一页', exact: true });
    await expect(close).toHaveText('←');
    const bounds = await close.boundingBox();
    const header = await page.locator('#app-header').boundingBox();
    expect(bounds).not.toBeNull();
    expect(header).not.toBeNull();
    expect(bounds.x).toBeLessThanOrEqual(12);
    expect(await close.evaluate(control => [...control.parentElement.children].filter(value => value.matches('button'))[0] === control)).toBe(true);
    expect(bounds.y).toBeLessThanOrEqual(16);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(header.y + header.height + 1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: `test-results/native-navigation-detail-${width}.png`, fullPage: true });
    await close.click();
    await expect(page.locator('.detail-hero')).toHaveCount(0);
  });
}

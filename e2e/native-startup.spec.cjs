'use strict';
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const url = pathToFileURL(path.resolve(__dirname, '../src/renderer/native-startup.html')).href;
async function install(page, { delayed = false } = {}) {
  await page.addInitScript(({ delayed }) => {
    window.startupCalls = [];
    window.nativeStartup = {
      getState: () => delayed ? new Promise(resolve => { window.resolveStartup = resolve; }) : Promise.resolve({ phase: 'loading' }),
      onState: fn => { window.startupUpdate = fn; },
      enter: async () => { window.startupCalls.push('enter'); },
      quit: async () => { window.startupCalls.push('quit'); },
    };
  }, { delayed });
  await page.goto(url);
}
for (const width of [1280, 390]) test(`startup shows only a small centered animated logo on a solid background at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 720 });
  await install(page);
  await expect(page.locator('#startup-logo')).toBeVisible();
  await expect(page.locator('#startup-error')).toBeHidden();
  await expect(page.locator('button:visible,input,iframe,webview,header,aside')).toHaveCount(0);
  const result = await page.locator('#startup-logo').evaluate(logo => {
    const rect = logo.getBoundingClientRect();
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2, width: rect.width, animation: getComputedStyle(logo).animationName, background: getComputedStyle(document.body).backgroundColor, overflow: document.documentElement.scrollWidth > innerWidth };
  });
  expect(Math.abs(result.x - width / 2)).toBeLessThan(1);
  expect(Math.abs(result.y - 360)).toBeLessThan(1);
  expect(result.width).toBeLessThanOrEqual(51);
  expect(result.animation).toBe('startup-breathe');
  expect(result.background).toBe('rgb(28, 28, 30)');
  expect(result.overflow).toBe(false);
  await page.screenshot({ path: `test-results/native-startup-${width}.png` });
});
test('startup respects reduced motion', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' }); await install(page);
  expect(await page.locator('#startup-logo').evaluate(logo => getComputedStyle(logo).animationName)).toBe('none');
});
test('failed startup has keyboard-accessible entry and quit rather than an endless logo', async ({ page }) => {
  await install(page);
  await page.evaluate(() => window.startupUpdate({ phase: 'failed' }));
  await expect(page.getByRole('status')).toBeVisible();
  await page.getByRole('button', { name: '进入应用', exact: true }).focus(); await page.keyboard.press('Enter');
  await page.getByRole('button', { name: '退出', exact: true }).click();
  expect(await page.evaluate(() => window.startupCalls)).toEqual(['enter', 'quit']);
});
test('a late initial snapshot cannot hide an already received startup failure', async ({ page }) => {
  await install(page, { delayed: true });
  await page.evaluate(() => { window.startupUpdate({ phase: 'failed' }); window.resolveStartup({ phase: 'loading' }); });
  await expect(page.getByRole('status')).toBeVisible();
});

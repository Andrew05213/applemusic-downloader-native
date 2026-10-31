'use strict';

const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  workers: 1,
  reporter: 'list',
  use: {
    browserName: 'chromium',
    headless: true,
    // Chrome's Unix singleton socket cannot live below a long checkout path.
    // Only the disposable CI browser uses this explicitly selected short temp;
    // application profiles/build caches keep their existing locations.
    ...(process.env.AMDL_BROWSER_TEMP ? {launchOptions:{env:{...process.env,
      TMPDIR:process.env.AMDL_BROWSER_TEMP, TMP:process.env.AMDL_BROWSER_TEMP,
      TEMP:process.env.AMDL_BROWSER_TEMP}}} : {}),
  },
});

'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const builderUtil = require('builder-util');
const { Platform } = require('app-builder-lib');
const { getConfig } = require('app-builder-lib/out/util/config/config');
const { createElectronFrameworkSupport } = require('app-builder-lib/out/electron/ElectronFramework');
const { VERSION, RUNTIME_DIRECTORIES } = require('../../electron/drm-runtime');
const manifest = require('../../electron/legacy-desktop-package.json');

const repositoryRoot = path.resolve(__dirname, '../..');

function projectFixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-builder-resolution-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'electron'));
  fs.copyFileSync(path.join(repositoryRoot,'electron/legacy-desktop-package.json'),path.join(root,'electron/legacy-desktop-package.json'));
  fs.writeFileSync(path.join(root, 'package.json'), JSON.stringify(manifest));
  for (const file of ['electron-builder.base.cjs', 'electron-builder.offline.yml']) {
    fs.copyFileSync(path.join(repositoryRoot, file), path.join(root, file));
  }
  // Keep the actual entry configs and factory; only relocate their project root.
  // The builder must resolve its own electronDist hook, not a test-called callback.
  fs.writeFileSync(path.join(root, 'electron', 'drm-build-config.js'),
    `module.exports = require(${JSON.stringify(path.join(repositoryRoot, 'electron', 'drm-build-config.js'))});\n`);
  return root;
}

function runtimeFixture(root, relative, marker, version = VERSION) {
  const directory = path.join(root, relative);
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, 'version'), version);
  fs.writeFileSync(path.join(directory, 'electron.exe'), `MZ synthetic ECS ${marker}`);
  fs.writeFileSync(path.join(directory, 'electron.exe.sig'), `synthetic upstream signature ${marker}`);
  fs.writeFileSync(path.join(directory, 'LICENSE'), 'synthetic runtime license');
  return directory;
}

function prohibitDownload(t) {
  const attempts = [];
  t.mock.method(builderUtil, 'executeAppBuilder', async (args) => {
    attempts.push(args);
    throw new Error('Unexpected app-builder execution: the selected DRM runtime must be copied without downloading Electron.');
  });
  return attempts;
}

async function prepareRuntime(config, root, appOutDir) {
  const packager = {
    config,
    projectDir: root,
    appInfo: { type: 'commonjs', productFilename: 'SyntheticFixture' },
    platform: Platform.WINDOWS,
    getElectronSrcDir: (directory) => directory,
    getElectronDestinationDir: (directory) => directory,
  };
  const framework = await createElectronFrameworkSupport(config, packager);
  return framework.prepareApplicationStageDirectory({
    packager,
    appOutDir,
    platformName: 'win32',
    arch: 'x64',
  });
}

test('runtime resolution tests exercise the same app-builder-lib version as the lockfile', () => {
  const locked = require('../../package-lock.json').packages['node_modules/app-builder-lib'].version;
  assert.equal(require('app-builder-lib/package.json').version, locked);
});

for (const [mode, configFile] of [['online', 'electron-builder.base.cjs'], ['offline', 'electron-builder.offline.yml']]) {
  for (const selection of ['isolated', 'normal']) {
    test(`${mode} builder staging copies the exact ${selection} DRM runtime and signature without downloading`, async (t) => {
      const root = projectFixture(t);
      const attempts = prohibitDownload(t);
      const normal = runtimeFixture(root, RUNTIME_DIRECTORIES[1], 'normal');
      const isolated = runtimeFixture(root, RUNTIME_DIRECTORIES[0], 'isolated',
        selection === 'isolated' ? VERSION : '37.3.1');
      const selected = selection === 'isolated' ? isolated : normal;
      const config = await getConfig(root, configFile, undefined);
      const appOutDir = path.join(root, 'staged');
      await config.beforePack();
      await prepareRuntime(config, root, appOutDir);
      assert.deepEqual(attempts, [], 'a configured DRM runtime must never enter the download path');
      for (const filename of ['electron.exe', 'electron.exe.sig']) {
        assert.deepEqual(fs.readFileSync(path.join(appOutDir, filename)), fs.readFileSync(path.join(selected, filename)));
      }
    });
  }

  for (const state of ['missing', 'wrong-version']) {
    test(`${mode} builder staging rejects a ${state} DRM runtime without downloading stock Electron`, async (t) => {
      const root = projectFixture(t);
      const attempts = prohibitDownload(t);
      if (state === 'wrong-version') runtimeFixture(root, RUNTIME_DIRECTORIES[1], 'stock', '37.3.1');
      const config = await getConfig(root, configFile, undefined);
      // Check the actual dist resolver independently of beforePack so neither
      // entry can silently fall back to an official Electron download.
      await assert.rejects(prepareRuntime(config, root, path.join(root, 'staged')), /not installed/);
      assert.deepEqual(attempts, []);
    });
  }
}

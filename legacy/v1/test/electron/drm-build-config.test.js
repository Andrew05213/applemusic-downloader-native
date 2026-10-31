'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { createDRMBuildConfig } = require('../../electron/drm-build-config');
const { VERSION, RUNTIME_DIRECTORIES } = require('../../electron/drm-runtime');
const manifest = require('../../electron/legacy-desktop-package.json');

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-drm-build-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const runtime = path.join(root, RUNTIME_DIRECTORIES[0]);
  const appOutDir = path.join(root, 'output');
  fs.mkdirSync(appOutDir);
  return { root, runtime, context: { appOutDir, packager: { appInfo: { productFilename: 'Fixture' } } } };
}

function writeRuntime(directory, integrity = 48) {
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, 'version'), VERSION);
  // A synthetic fuse wire exercises the real fuse reader without a real EXE.
  const executable = Buffer.concat([Buffer.from('MZ-synthetic-only-dL7pKGdnNz796PbbjQWNKmHXBZaB9tsX'),
    Buffer.from([1, 8, 48, 48, 48, 48, integrity, 48, 48, 48])]);
  fs.writeFileSync(path.join(directory, 'electron.exe'), executable);
  fs.writeFileSync(path.join(directory, 'electron.exe.sig'), 'synthetic upstream signature');
  return executable;
}

test('configuration loads without an installed runtime while packaging hooks reject missing or changed binaries', async (t) => {
  const { root, runtime, context } = fixture(t);
  const config = createDRMBuildConfig({ build: manifest.build, repositoryRoot: root, environment: {} });
  assert.equal(config.electronVersion, VERSION);
  assert.deepEqual(config.files, manifest.build.files);
  assert.equal(config.win.signAndEditExecutable, false);
  assert.equal(manifest.build.win.signAndEditExecutable, true);
  await assert.rejects(config.beforePack(), /not installed/);
  await assert.rejects(config.afterPack(context), /not installed/);
  writeRuntime(runtime);
  await config.beforePack();
  fs.writeFileSync(path.join(runtime, 'version'), '37.3.1');
  await assert.rejects(config.beforePack(), /mismatched/);
  await assert.rejects(config.afterPack(context), /mismatched/);
});

test('DRM packaging restores upstream EXE and detached signature bytes without changing the source', async (t) => {
  const { root, runtime, context } = fixture(t);
  const executable = writeRuntime(runtime);
  const output = path.join(context.appOutDir, 'Fixture.exe');
  fs.writeFileSync(output, 'builder-injected ASAR metadata');
  const config = createDRMBuildConfig({ build: manifest.build, repositoryRoot: root, environment: {} });
  await config.beforePack();
  await config.afterPack(context);
  assert.deepEqual(fs.readFileSync(output), executable);
  assert.deepEqual(fs.readFileSync(path.join(runtime, 'electron.exe')), executable);
  assert.deepEqual(fs.readFileSync(`${output}.sig`), fs.readFileSync(path.join(runtime, 'electron.exe.sig')));
});

test('DRM packaging rejects enabled integrity fuses or absent upstream signatures before replacing output', async (t) => {
  const { root, runtime, context } = fixture(t);
  const output = path.join(context.appOutDir, 'Fixture.exe');
  const config = createDRMBuildConfig({ build: manifest.build, repositoryRoot: root, environment: {} });
  writeRuntime(runtime, 49);
  fs.writeFileSync(output, 'untouched output');
  await assert.rejects(config.afterPack(context), /integrity/);
  assert.equal(fs.readFileSync(output, 'utf8'), 'untouched output');
  writeRuntime(runtime);
  fs.rmSync(path.join(runtime, 'electron.exe.sig'));
  await assert.rejects(config.afterPack(context));
  assert.equal(fs.readFileSync(output, 'utf8'), 'untouched output');
});

test('direct build dependencies retain their existing locked versions', () => {
  const lock = require('../../package-lock.json');
  for (const name of ['@electron/asar', '@electron/fuses']) {
    assert.equal(manifest.devDependencies[name], lock.packages[`node_modules/${name}`].version);
    assert.equal(lock.packages[''].devDependencies[name], manifest.devDependencies[name]);
  }
});

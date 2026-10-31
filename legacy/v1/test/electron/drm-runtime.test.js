'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { VERSION, RUNTIME_DIRECTORIES, runtimeSource, resolveDRMRuntime } = require('../../electron/drm-runtime');

function fixtureRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-runtime-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function writeRuntime(directory, version) {
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, 'version'), version);
  fs.writeFileSync(path.join(directory, 'electron.exe'), 'synthetic runtime');
}

test('runtime selector rejects stock or missing binaries and prefers exact isolated DRM runtime', (t) => {
  const root = fixtureRoot(t);
  const isolated = path.join(root, RUNTIME_DIRECTORIES[0]);
  const normal = path.join(root, RUNTIME_DIRECTORIES[1]);
  assert.throws(() => resolveDRMRuntime(root), /not installed/);
  writeRuntime(normal, '37.3.1');
  assert.throws(() => resolveDRMRuntime(root), /not installed/);
  fs.writeFileSync(path.join(normal, 'version'), VERSION);
  assert.equal(resolveDRMRuntime(root), normal);
  fs.mkdirSync(isolated, { recursive: true });
  fs.writeFileSync(path.join(isolated, 'version'), VERSION);
  assert.equal(resolveDRMRuntime(root), normal);
  fs.writeFileSync(path.join(isolated, 'electron.exe'), 'synthetic isolated runtime');
  assert.equal(resolveDRMRuntime(root), isolated);
  fs.writeFileSync(path.join(isolated, 'version'), '44.5.1');
  assert.equal(resolveDRMRuntime(root), normal);
});

test('installer source follows the declared immutable Castlabs dependency and rejects npm options or mutable URLs', () => {
  const manifest = require('../../electron/legacy-desktop-package.json');
  assert.equal(runtimeSource(manifest), manifest.devDependencies.electron);
  assert.equal(VERSION,'44.5.1+wvcus'); // Frozen 1.4 source fixture; native lock is tested separately.
  for (const source of [undefined, '--prefix=elsewhere', 'electron@latest',
    'https://codeload.github.com/castlabs/electron-releases/tar.gz/main',
    `${runtimeSource()}?token=synthetic`, runtimeSource().replace('https://', 'https://user:password@')]) {
    assert.throws(() => runtimeSource({ devDependencies: { electron: source } }), /immutable/);
  }
});

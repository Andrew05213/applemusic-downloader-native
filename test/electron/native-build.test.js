'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { buildNativeBackend } = require('../../electron/build-native-backend');

test('native control backend build uses fixed Go argv, never WSL or a shell', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-native-build-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  let call;
  const result = buildNativeBackend({ repositoryRoot: root, runner: (...args) => { call = args; return { status: 0 }; } });
  assert.equal(call[0], 'go');
  assert.equal(call[1].at(-1), './cmd/amdl-desktop');
  assert.equal(call[2].shell, false);
  assert.equal(call[2].env.GOOS, 'windows');
  assert.equal(call[2].env.GOARCH, 'amd64');
  assert.equal(call[2].env.CGO_ENABLED, '0');
  assert.equal(call[2].cwd, path.join(root, 'apple-music-downloader'));
  assert.ok(call[1].includes('-mod=readonly'));
  assert.ok(call[1].includes('-trimpath'));
  assert.ok(call[1].includes('-buildvcs=false'));
  assert.equal(result, path.join(root, 'electron', 'bin', 'amdl-desktop.exe'));
});

test('native control backend build reports a fixed failure without command output', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-native-build-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const response of [{ status: 1 }, { error: new Error('SENTINEL') }]) {
    assert.throws(() => buildNativeBackend({ repositoryRoot: root, runner: () => response }), /^Error: native_backend_build_failed$/);
  }
});

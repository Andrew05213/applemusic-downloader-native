'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { buildEnvironment, run } = require('../../electron/native-build-environment');

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-build-environment-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

test('build caches and temporary files stay on the repository volume, without global changes', t => {
  const root = fixture(t);
  const base = { PATH: 'preserved', TEMP: 'old-C-temp', GOCACHE: 'old-C-cache', APPDATA: 'profile-untouched' };
  const env = buildEnvironment(root, base);
  for (const key of ['TEMP', 'TMP', 'TMPDIR', 'GOTMPDIR', 'GOCACHE', 'ELECTRON_CACHE', 'ELECTRON_BUILDER_CACHE', 'npm_config_cache']) {
    assert.ok(path.relative(root, env[key]).startsWith('.tmp-v2-native' + path.sep), key);
    assert.ok(fs.statSync(env[key]).isDirectory(), key);
  }
  assert.equal(env.PATH, base.PATH);
  assert.equal(env.APPDATA, base.APPDATA);
  assert.equal(base.TEMP, 'old-C-temp');
  assert.equal(base.GOCACHE, 'old-C-cache');
  assert.deepEqual(buildEnvironment(root, base), env);
});

test('development launcher preserves argv, exit codes and shell-free execution', t => {
  const root = fixture(t);
  let call;
  const status = run(['--test', 'test/**/*.test.js'], { root, env: { PATH: 'fixture' }, runner: (...args) => { call = args; return { status: 3 }; } });
  assert.equal(status, 3);
  assert.equal(call[0], process.execPath);
  assert.deepEqual(call[1], ['--test', 'test/**/*.test.js']);
  assert.equal(call[2].cwd, root);
  assert.equal(call[2].shell, false);
  assert.equal(call[2].windowsHide, true);
  assert.ok(call[2].env.TEMP.startsWith(root + path.sep));
  assert.throws(() => run([], { root }), /native_build_command_required/);
  assert.throws(() => run(['fixture.js'], { root, runner: () => ({ error: new Error('secret') }) }), /^Error: native_build_command_failed$/);
});

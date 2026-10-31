'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const {audit, TARGETS} = require('../scripts/check-native-boundaries');
const MODULE = 'github.com/Andrew05213/applemusic_downloader';

test('native dependency audit checks all three actual build targets without a shell', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-boundary-'));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  const calls = [];
  const result = audit(root, (command, args, options) => {
    calls.push(options.env.GOOS);
    assert.equal(command, 'go');
    assert.equal(options.shell, false);
    assert.equal(options.cwd, path.join(root, 'apple-music-downloader'));
    assert(args.includes('-mod=readonly'));
    const target = args.at(-1);
    return {status: 0, stdout: MODULE + (target === '.' ? '' : '/' + target.slice(2)) + '\n'};
  });
  assert.equal(result.passed, true);
  assert.deepEqual(calls, TARGETS.map(target => target.os));
});

test('native dependency audit fails closed on errors, empty graphs and legacy packages', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-boundary-'));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  assert.throws(() => audit(root, () => ({status: 1})), /dependency_list_failed/);
  assert.throws(() => audit(root, () => ({status: 0, stdout: ''})), /dependency_target_missing/);
  assert.throws(() => audit(root, () => ({status: 0, stdout: MODULE + '\n' + MODULE + '/internal/appliance\n'})), /legacy_dependency/);
});

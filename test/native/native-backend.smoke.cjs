'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawnSync } = require('node:child_process');

const executable = path.resolve(__dirname, '../../electron/bin/amdl-desktop.exe');
const commit = require('../../native-runtime/inputs.lock.json').wrapper.commit;

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-native-smoke-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const bundle = { schemaVersion: 1, wrapperCommit: commit, qemu: 'bin/qemu-system-x86_64.exe', firmware: 'firmware', kernel: 'guest/kernel', initramfs: 'guest/initrd', dataTemplate: 'guest/blank.img', files: {} };
  for (const name of [bundle.qemu, `${bundle.firmware}/bios-256k.bin`, bundle.kernel, bundle.initramfs, bundle.dataTemplate]) {
    const bytes = Buffer.from(`SYNTHETIC-NONEXECUTABLE ${name}`);
    const destination = path.join(root, name);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.writeFileSync(destination, bytes);
    bundle.files[name] = { sha256: crypto.createHash('sha256').update(bytes).digest('hex'), size: bytes.length };
  }
  const manifest = path.join(root, 'bundle.json');
  fs.writeFileSync(manifest, JSON.stringify(bundle));
  return { root, manifest, state: path.join(root, '..', path.basename(root) + '-state') };
}

function execute(args, input = '') {
  // Absolute native executable plus no PATH: the fixture needs no WSL,
  // shell, Go toolchain, Docker or external Apple service at execution time.
  return spawnSync(executable, args, {
    input, encoding: 'utf8', shell: false, timeout: 5000,
    env: { SystemRoot: process.env.SystemRoot, WINDIR: process.env.WINDIR, PATH: '', AMDL_CONFIG: 'SENTINEL_NEVER_READ', MEDIA_USER_TOKEN: 'SENTINEL_NEVER_EXPOSE' },
  });
}

test('native EXE checks synthetic resources without starting a guest or touching account state', () => {
  assert.equal(process.platform, 'win32', 'this smoke verifies the Windows native executable');
  const result = execute(['--check-only', '--manifest', path.join(os.tmpdir(), 'amdl-nonexistent-bundle.json')]);
  assert.equal(result.status, 1);
  assert.equal(JSON.parse(result.stdout).code, 'native_bundle_missing');
  assert.equal(result.stderr, '');
  assert.doesNotMatch(result.stdout, /SENTINEL|wsl|nonexistent-bundle/);
});

test('native EXE returns complete status/check/shutdown receipts with an empty PATH', (t) => {
  const f = fixture(t);
  const checked = execute(['--check-only', '--manifest', f.manifest]);
  assert.equal(checked.status, 0, checked.stderr);
  assert.equal(JSON.parse(checked.stdout).ok, true);
  const requests = ['status', 'check', 'shutdown'].map((command, index) => JSON.stringify({ id: index + 1, command })).join('\n') + '\n';
  const result = execute(['--manifest', f.manifest, '--state-dir', f.state], requests);
  assert.equal(result.status, 0, result.stderr);
  const receipts = result.stdout.trim().split(/\r?\n/).map((line) => JSON.parse(line));
  assert.deepEqual(receipts.map((r) => r.id), [1, 2, 3]);
  for (const receipt of receipts) {
    assert.equal(receipt.ok, true);
    assert.equal(receipt.version, require('../../package.json').version);
    assert.equal(receipt.state.runtime, 'native-lite');
    assert.equal(receipt.state.sessionVerified, false);
  }
  assert.equal(fs.existsSync(f.state), false, 'check/status must not create runtime data');
  assert.doesNotMatch(result.stdout + result.stderr, /SENTINEL/);
});

test('native EXE rejects a changed input instead of accepting synthetic manifests on faith', (t) => {
  const f = fixture(t);
  fs.writeFileSync(path.join(f.root, 'guest/kernel'), 'changed');
  const result = execute(['--check-only', '--manifest', f.manifest]);
  assert.equal(result.status, 1);
  assert.equal(JSON.parse(result.stdout).code, 'native_bundle_identity_mismatch');
  assert.equal(fs.existsSync(f.state), false);
});

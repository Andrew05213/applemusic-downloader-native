'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const {
  WslMountSmokeError,
  filterVerifiedHostWarnings,
  mountInspectionArguments,
  parseMountResidue,
  verifyWrapperMountsClean,
} = require('../../electron/wsl-mount-smoke');

const VERSION = '1.2.0';
const RELEASE = `/opt/applemusic-downloader/releases/${VERSION}/rootfs`;
const ENGLISH_WARNING = 'wsl: A localhost proxy configuration was detected but not mirrored into WSL. WSL in NAT mode does not support localhost proxies.';

function mountDocument(targets = []) {
  return JSON.stringify({
    filesystems: [{
      target: '/',
      source: '/dev/sdf',
      fstype: 'ext4',
      children: targets.map((target) => ({ target, source: 'test', fstype: 'test' })),
    }],
  });
}

test('mount inspection uses one fixed root-only findmnt argv array', () => {
  assert.deepEqual(mountInspectionArguments(), [
    '--distribution', 'AppleMusicDownloader', '--user', 'root', '--exec',
    '/usr/bin/findmnt', '--json', '--output', 'TARGET,SOURCE,FSTYPE',
  ]);
});

test('mount residue parser accepts unrelated/data mounts but rejects proc and urandom layers or descendants', () => {
  assert.deepEqual(parseMountResidue(mountDocument([
    `${RELEASE}/data`,
    '/mnt/c/Users/测试 用户/Music/Apple Music Downloader',
  ]), VERSION), { procLayers: 0, urandomLayers: 0, clean: true });

  assert.deepEqual(parseMountResidue(mountDocument([
    `${RELEASE}/proc`,
    `${RELEASE}/proc/sys`,
    `${RELEASE}/dev/urandom`,
  ]), VERSION), { procLayers: 2, urandomLayers: 1, clean: false });

  assert.deepEqual(parseMountResidue(mountDocument([
    '/opt/applemusic-downloader/current/rootfs/proc',
    '/opt/applemusic-downloader/current/rootfs/dev/urandom',
  ]), VERSION), { procLayers: 1, urandomLayers: 1, clean: false });
});

test('host warning filter removes only exact complete localhost proxy lines', () => {
  assert.equal(filterVerifiedHostWarnings(`${ENGLISH_WARNING}\r\n`, 'mounts'), '');
  for (const stderr of [
    `${ENGLISH_WARNING} extra\n`,
    `${ENGLISH_WARNING}\nfindmnt: malicious diagnostic\n`,
    'wsl: Failed to start the systemd user session for root.\n',
  ]) {
    assert.notEqual(filterVerifiedHostWarnings(stderr, 'mounts').trim(), '');
  }
});

test('live verifier requires zero layers and fails closed on unknown stderr or malformed JSON', async () => {
  const calls = [];
  const manager = {
    async runWsl(args, options) {
      calls.push({ args, options });
      return { stdout: mountDocument(), stderr: `${ENGLISH_WARNING}\n` };
    },
  };
  assert.deepEqual(await verifyWrapperMountsClean(manager, VERSION), {
    procLayers: 0,
    urandomLayers: 0,
    clean: true,
  });
  assert.deepEqual(calls[0], { args: mountInspectionArguments(), options: { timeoutMs: 30_000 } });

  manager.runWsl = async () => ({ stdout: mountDocument([`${RELEASE}/proc`]), stderr: '' });
  await assert.rejects(verifyWrapperMountsClean(manager, VERSION), WslMountSmokeError);
  manager.runWsl = async () => ({ stdout: mountDocument(), stderr: 'unexpected\n' });
  await assert.rejects(verifyWrapperMountsClean(manager, VERSION), WslMountSmokeError);
  manager.runWsl = async () => ({ stdout: '{', stderr: '' });
  await assert.rejects(verifyWrapperMountsClean(manager, VERSION), WslMountSmokeError);
});

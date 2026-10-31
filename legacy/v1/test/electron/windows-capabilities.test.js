'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { WindowsCapabilities } = require('../../electron/windows-capabilities');

function helperFile() {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-windows-probe-'));
  const target = path.join(directory, 'amdl-windows-probe.exe');
  fs.writeFileSync(target, 'mock');
  return target;
}

function helperBinding(filename, sha256 = crypto.createHash('sha256').update(fs.readFileSync(filename)).digest('hex')) {
  return { file: 'amdl-windows-probe.exe', sha256, size: fs.lstatSync(filename).size };
}

test('capability helper reports Windows 11 x64 virtualization state', async () => {
  const calls = [];
  const helperPath = helperFile();
  const capabilities = new WindowsCapabilities({
    helperPath,
    helperBindingProvider: () => helperBinding(helperPath),
    platform: 'win32',
    arch: 'x64',
    release: '10.0.26200',
    runner: async (command, args) => {
      calls.push({ command, args });
      return { stdout: JSON.stringify({ architecture: 'amd64', build: 26200, virtualizationEnabled: true, rebootRequired: false }) };
    },
  });

  assert.equal((await capabilities.probe()).ok, true);
  assert.deepEqual(calls[0].args, ['probe']);
});

test('Windows 10 and 11 share a strict supported-build policy with and without the helper', async () => {
  const helperPath = helperFile();
  for (const build of [19044, 19045, 22000, 22631, 26100]) {
    for (const useHelper of [false, true]) {
      const capabilities = new WindowsCapabilities({
        platform: 'win32', arch: 'x64', release: `10.0.${build}`,
        helperPath: useHelper ? helperPath : undefined,
        helperBindingProvider: () => helperBinding(helperPath),
        runner: async () => ({ stdout: JSON.stringify({ architecture: 'amd64', build, virtualizationEnabled: true, rebootRequired: false }) }),
      });
      assert.equal((await capabilities.probe()).ok, true, `${build}, helper=${useHelper}`);
    }
  }
  for (const release of ['10.0.19043', '10.0.18363', '6.3.9600', '10.0.NaN', '10.0.', '', 'garbage']) {
    const result = await new WindowsCapabilities({ platform: 'win32', arch: 'x64', release }).probe();
    assert.equal(result.error?.code, 'unsupported_windows', release);
    assert.match(result.error.action, /Windows 10/);
  }
  for (const build of [19043, undefined, null, '19045', 'garbage', 19045.5]) {
    const result = await new WindowsCapabilities({
      platform: 'win32', arch: 'x64', release: '10.0.19045', helperPath,
      helperBindingProvider: () => helperBinding(helperPath),
      runner: async () => ({ stdout: JSON.stringify({ architecture: 'amd64', build, virtualizationEnabled: true, rebootRequired: false }) }),
    }).probe();
    assert.equal(result.error?.code, 'unsupported_windows', `invalid helper build ${build}`);
  }
});

test('Windows 10 retains x64, virtualization and reboot requirements', async () => {
  for (const arch of ['ia32', 'arm64']) {
    const result = await new WindowsCapabilities({ platform: 'win32', arch, release: '10.0.19045' }).probe();
    assert.equal(result.error.code, 'unsupported_arch');
  }
  const helperPath = helperFile();
  for (const [virtualizationEnabled, rebootRequired, code] of [[false, false, 'virtualization_disabled'], [true, true, 'reboot_required']]) {
    const result = await new WindowsCapabilities({
      platform: 'win32', arch: 'x64', release: '10.0.19045', helperPath,
      helperBindingProvider: () => helperBinding(helperPath),
      runner: async () => ({ stdout: JSON.stringify({ architecture: 'amd64', build: 19045, virtualizationEnabled, rebootRequired }) }),
    }).probe();
    assert.equal(result.error.code, code);
  }
});

test('official WSL operations use a fixed helper argv without a shell', async () => {
  const calls = [];
  const helperPath = helperFile();
  const capabilities = new WindowsCapabilities({
    helperPath,
    helperBindingProvider: () => helperBinding(helperPath),
    platform: 'win32',
    arch: 'x64',
    release: '10.0.26200',
    runner: async (command, args, options) => { calls.push({ command, args, options }); return { stdout: '' }; },
  });

  await capabilities.installWsl();
  await capabilities.updateWsl();
  assert.deepEqual(calls.map((call) => call.args), [['install-wsl'], ['update-wsl']]);
  assert.ok(calls.every((call) => call.options.timeoutMs >= 30 * 60 * 1000));
});

test('descriptor helper mismatch rejects probe and fixed actions before any helper execution', async () => {
  const helperPath = helperFile();
  const calls = [];
  const capabilities = new WindowsCapabilities({
    helperPath,
    helperBindingProvider: () => helperBinding(helperPath, '0'.repeat(64)),
    platform: 'win32',
    arch: 'x64',
    release: '10.0.26200',
    runner: async (command, args) => {
      calls.push({ command, args });
      return { stdout: JSON.stringify({ architecture: 'amd64', build: 26200, virtualizationEnabled: true, rebootRequired: false }) };
    },
  });

  const probe = await capabilities.probe();
  assert.equal(probe.ok, false);
  await assert.rejects(capabilities.installWsl());
  assert.deepEqual(calls, []);
});

test('descriptor helper bytes are rechecked after an action returns', async () => {
  const helperPath = helperFile();
  const binding = helperBinding(helperPath);
  const calls = [];
  const capabilities = new WindowsCapabilities({
    helperPath,
    helperBindingProvider: () => binding,
    platform: 'win32',
    arch: 'x64',
    release: '10.0.26200',
    runner: async (command, args) => {
      calls.push({ command, args });
      fs.appendFileSync(command, 'drift');
      return { stdout: JSON.stringify({ architecture: 'amd64', build: 26200, virtualizationEnabled: true, rebootRequired: false }) };
    },
  });

  const result = await capabilities.probe();
  assert.equal(result.ok, false);
  assert.deepEqual(calls.map((call) => call.args), [['probe']]);
});

test('closed UAC or administrator terminal returns a retryable Chinese WSL action error', async () => {
  const helperPath = helperFile();
  const capabilities = new WindowsCapabilities({
    helperPath,
    helperBindingProvider: () => helperBinding(helperPath),
    platform: 'win32',
    arch: 'x64',
    release: '10.0.26200',
    runner: async () => {
      const error = new Error('helper failed');
      error.stderr = 'elevation_cancelled';
      throw error;
    },
  });

  await assert.rejects(capabilities.installWsl(), (error) => {
    assert.equal(error.code, 'wsl_missing');
    assert.match(error.message, /管理员授权|安装窗口/);
    assert.match(error.action, /安装 WSL2.*重试/);
    return true;
  });
});

test('virtualization and architecture failures have stable Chinese recovery actions', async () => {
  const wrongArch = new WindowsCapabilities({ platform: 'win32', arch: 'arm64', release: '10.0.26200' });
  assert.equal((await wrongArch.probe()).error.code, 'unsupported_arch');

  const helperPath = helperFile();
  const disabled = new WindowsCapabilities({
    helperPath,
    helperBindingProvider: () => helperBinding(helperPath),
    platform: 'win32',
    arch: 'x64',
    release: '10.0.26200',
    runner: async () => ({ stdout: JSON.stringify({ architecture: 'amd64', build: 26200, virtualizationEnabled: false, rebootRequired: false }) }),
  });
  const result = await disabled.probe();
  assert.equal(result.error.code, 'virtualization_disabled');
  assert.match(result.error.action, /BIOS\/UEFI/);
});

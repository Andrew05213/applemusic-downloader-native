'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { WslManager, WslManagerError } = require('../../electron/wsl-manager');
const { createManifest } = require('./fixtures/appliance-manifest-document');

test('same-version stopped CA repair requests a bound recovery admission', async () => {
  const manifest = createManifest({ version: '1.4.3' });
  const state = { manifest, installedVersion: manifest.version, installedRuntimeSha256: manifest.runtime.sha256,
    installedRuntimeTransactionId: 'c'.repeat(64) };
  const manager = Object.create(WslManager.prototype);
  manager.readState = async () => state;
  manager.ensureProxyStopConfirmed = async () => {};
  manager.verifyDistroOwnership = async () => ({ ownerId: 'd'.repeat(64) });
  manager.runControl = async () => ({ stdout: JSON.stringify({ version: manifest.version, state: 'stopped',
    runtimeSha256: manifest.runtime.sha256, runtimeTransactionId: state.installedRuntimeTransactionId }) });
  manager.recordUpgradeProgress = async () => {};
  let admission;
  manager.prepareNetworkForStart = async options => { admission = options.caRepairIdentity; throw new WslManagerError('network_preflight_failed'); };
  await assert.rejects(manager.updateUnlocked(manifest, { repair: true }));
  assert.equal(admission?.runtimeSha256, manifest.runtime.sha256);
  assert.equal(admission?.runtimeTransactionId, state.installedRuntimeTransactionId);
  assert.equal(admission?.applianceBuildId, manifest.applianceBuildId);
  admission = undefined;
  await assert.rejects(manager.updateUnlocked(manifest));
  assert.equal(admission, undefined);
  const submitted = [];
  const transactionId = 'e'.repeat(64);
  let stillBound = false;
  manager.prepareNetworkForStart = async () => ({ caRepairAdmission: { generationId: '1'.repeat(64) } });
  manager.confirmCABundleRepairAdmission = async () => stillBound;
  manager.cacheArtifact = async () => 'C:\\fixture\\runtime.tar.gz';
  manager.windowsPathToWsl = async () => '/mnt/c/fixture/runtime.tar.gz';
  manager.discardLocalProxy = async () => {};
  manager.verifyDistroOwnership = async () => ({ ownerId: 'd'.repeat(64), registration: { basePath: 'C:\\fixture' } });
  manager.writeState = async patch => { Object.assign(state, patch); return state; };
  const statusControl = manager.runControl;
  manager.runControl = async (op, options) => {
    if (op === 'update-stdin') { submitted.push(JSON.parse(options.input)); return {}; }
    return statusControl();
  };
  manager.waitForSupervisorRestart = async () => {};
  manager.start = async () => ({ runtimeSha256: manifest.runtime.sha256, runtimeTransactionId: transactionId });
  await assert.rejects(manager.updateUnlocked(manifest, { repair: true, transactionId }),
    error => error.code === 'network_preflight_failed');
  assert.equal(submitted.length, 0);
  assert.equal(state.updateTransaction, undefined);
  stillBound = true;
  await manager.updateUnlocked(manifest, { repair: true, transactionId });
  assert.equal(submitted.length, 1);
  assert.equal(submitted[0].mode, 'repair');
  assert.equal(submitted[0].version, manifest.version);
  assert.equal(state.installedRuntimeTransactionId, transactionId);
});

for (const scenario of ['missing', 'invalid', 'tls', 'running', 'generation', 'helper', 'binding',
  'runtimeSha256', 'runtimeTransactionId', 'applianceBuildId', 'androidMainBuiltSha256',
  'androidNetworkPreflightBuiltSha256', 'androidCaBundleSha256', 'rendererSha256', 'unknown-field']) {
  test(`CA recovery requires exact stopped runtime and broker identity: ${scenario}`, async () => {
    const manifest = createManifest({ version: '1.4.3' });
    const generationId = '1'.repeat(64), helperSha256 = '2'.repeat(64);
    const caRepairIdentity = { runtimeSha256: manifest.runtime.sha256, runtimeTransactionId: '3'.repeat(64),
      applianceBuildId: manifest.applianceBuildId,
      ...Object.fromEntries(['androidMainBuiltSha256', 'androidNetworkPreflightBuiltSha256',
        'androidCaBundleSha256', 'androidCaBundlePath', 'rendererSha256'].map(k => [k, manifest.inputs[k]])) };
    const status = { ...caRepairIdentity, service: 'applemusic-downloader', runtime: 'wsl-appliance',
      version: manifest.version, productVersion: manifest.version, state: 'stopped', windowsHelperSha256: helperSha256,
      network: { schemaVersion: 2, generationId, path: 'windows_stdio_bridge', state: 'failed',
        stage: scenario === 'invalid' ? 'ca_bundle_invalid' : 'ca_bundle_missing', windowsPath: 'unreachable',
        wslDirectPath: 'unknown', proxyPropagated: false, caSource: 'bundled_v1', curlCode: 0, sslVerifyResult: 0 } };
    if (scenario === 'tls') status.network.stage = 'tls';
    if (scenario === 'running') status.state = 'running';
    if (scenario === 'generation') status.network.generationId = '4'.repeat(64);
    if (scenario === 'helper') status.windowsHelperSha256 = '4'.repeat(64);
    if (scenario === 'unknown-field') status.network.raw = 'SENTINEL';
    if (Object.hasOwn(caRepairIdentity, scenario)) status[scenario] = 'f'.repeat(64);
    const manager = Object.create(WslManager.prototype);
    manager.expectedService = status.service;
    manager.expectedRuntime = status.runtime;
    manager.assertWindowsHelperBinding = async () => ({ sha256: scenario === 'binding' ? '4'.repeat(64) : helperSha256 });
    manager.runControl = async () => ({ stdout: JSON.stringify(status) });
    assert.equal(await manager.confirmCABundleRepairAdmission({ expectedVersion: manifest.version,
      caRepairIdentity, generationId, helperSha256 }), ['missing', 'invalid'].includes(scenario));
    if (['missing', 'invalid'].includes(scenario)) {
      manager.networkGenerationProvider = async () => generationId;
      manager.windowsProbeExe = 'C:\\fixture\\amdl-windows-probe.exe';
      manager.assertWindowsHelperBinding = async () => ({ file: 'amdl-windows-probe.exe', sha256: helperSha256 });
      manager.canonicalizeWindowsFileWithHelper = async canonicalPath => ({ canonicalPath, objectId: '00112233:445566778899aabb' });
      manager.windowsHelperPathToWsl = async () => '/mnt/c/fixture/amdl-windows-probe.exe';
      manager.runControl = async operation => ({ stdout: JSON.stringify(operation === 'network-prepare-stdin'
        ? { ...status, network: { ...status.network, state: 'prepared', stage: 'prepared', windowsPath: 'unknown' } }
        : status) });
      const result = await manager.prepareNetworkForStart({ expectedVersion: manifest.version, caRepairIdentity });
      assert.equal(result.caRepairAdmission.generationId, generationId);
      assert.equal(result.state, undefined, 'repair permission must not claim network-ready');
      await assert.rejects(manager.prepareNetworkForStart({ expectedVersion: manifest.version }),
        error => error.code === 'network_preflight_failed');
      status.network.generationId = 'e'.repeat(64);
      assert.equal(await manager.confirmCABundleRepairAdmission(result.caRepairAdmission), false,
        'submission must reject a broker changed after the initial permission');
    }
  });
}

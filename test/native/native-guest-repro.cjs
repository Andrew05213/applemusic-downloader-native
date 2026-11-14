'use strict';

const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');
const { makeManifest } = require('../../electron/build-native-runtime');

function compareBundles(first, second) {
  const isolated = JSON.parse(fs.readFileSync(path.join(first, 'bundle.json'), 'utf8')).isolated;
  const a = makeManifest(first,{isolated});
  const b = makeManifest(second,{isolated});
  assert.deepEqual(a, b);
  for (const name of ['bundle.json', ...Object.keys(a.files)]) {
    assert.equal(fs.readFileSync(path.join(first, name)).equals(fs.readFileSync(path.join(second, name))), true);
  }
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(first, 'bundle.json'), 'utf8')), a);
  const evidence = [first, second].map((directory) => JSON.parse(fs.readFileSync(path.join(directory, 'evidence/build.json'), 'utf8')));
  assert.match(evidence[0].guestAssemblyRun, /^[a-f0-9-]{36}$/);
  assert.match(evidence[1].guestAssemblyRun, /^[a-f0-9-]{36}$/);
  assert.notEqual(evidence[0].guestAssemblyRun, evidence[1].guestAssemblyRun);
  for (const field of ['toolchainID', 'toolchainDefinitionSHA256', 'toolchainEvidenceSHA256', 'extractionToolsLockSHA256', 'sourceDateEpoch', 'sourceSHA256', 'qemuInstallerSHA512', 'patchSHA256', 'recipeSHA256', 'initSHA256', 'assemblySHA256', 'inputsLockSHA256', 'sessionPatchSHA256', 'nativeLoginSHA256', 'nativeServiceSHA256', 'guestAgentSHA256', 'guestAgentSourceSHA256', 'storeBridgeSHA256', 'storeBridgePatchSHA256', 'networkCheckSHA256']) {
    assert.notEqual(evidence[0][field], undefined);
    assert.equal(evidence[0][field], evidence[1][field]);
  }
  return { schemaVersion: 1, passed: true, scope: 'two-forced-guest-assembly-exports',
    filesCompared: Object.keys(a.files).length + 1, assemblyRuns: evidence.map((entry) => entry.guestAssemblyRun),
    manifestSHA256: crypto.createHash('sha256').update(fs.readFileSync(path.join(first, 'bundle.json'))).digest('hex') };
}

if (require.main === module) {
  try {
    const args = process.argv.slice(2);
    if (args.length !== 2 || path.resolve(args[0]) === path.resolve(args[1])) throw new Error();
    const result = compareBundles(...args.map((name) => path.resolve(name)));
    const directory = path.resolve(__dirname, '../../.tmp-v2-native');
    fs.mkdirSync(directory, { recursive: true });
    const evidence = path.join(directory, 'repro-' + crypto.randomUUID() + '.json');
    fs.writeFileSync(evidence, JSON.stringify(result, null, 2) + '\n', { flag: 'wx' });
    process.stdout.write(JSON.stringify({ ...result, evidence }) + '\n');
  } catch { process.stderr.write('native_guest_exports_differ\n'); process.exitCode = 1; }
}

module.exports = { compareBundles };

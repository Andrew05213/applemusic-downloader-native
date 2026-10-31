'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {testFiles, contract} = require('../../electron/native-ci');
const release = require('../../electron/native-release');
const root = path.resolve(__dirname, '../..');

test('native default suite excludes legacy WSL helper preconditions', async () => {
  const files = testFiles();
  assert(files.includes('test/electron/native-ci.test.js'));
  assert(files.includes('test/electron/native-stream.test.js'));
  assert(!files.some(file => file.includes('wsl-manager')));
  assert.equal((await contract()).publicReleaseAllowed, true);
});
test('native installer explicitly carries pinned cJSON and Dobby license texts', () => {
  const resources = require('../../electron-builder.native.cjs').extraResources;
  for (const name of ['cJSON-LICENSE', 'Dobby-LICENSE']) {
    const relative = 'third-party-licenses/' + name;
    assert(resources.some(resource => resource.from === relative && resource.to === relative));
    assert(fs.statSync(path.join(root, relative)).size > 0);
  }
});
test('CI and release use native verification and never a WSL delivery job', () => {
  const verify = fs.readFileSync(path.join(root, '.github/workflows/verify.yml'), 'utf8');
  const workflow = fs.readFileSync(path.join(root, '.github/workflows/release-native.yml'), 'utf8');
  assert.match(verify, /workflow_call:/);
  for (const command of ['verify:native', 'npm test', 'build:native-backend', 'build:native-service', 'test:native-backend', 'test:web']) assert(verify.includes(command));
  assert.match(verify, /TestStreamGeneratedAACFixture/);
  assert.match(verify, /test -count=1 -run '\^TestStreamGeneratedAACFixture\$'/);
  assert.match(verify, /ffmpeg .* -xerror/);
  assert.match(verify, /go test -race \.\/\.\.\./);
  for (const text of [verify, workflow]) assert.doesNotMatch(text, /build:windows-helper|dist:win:|appliance\/scripts|wsl\.exe|docker build|upload-artifact/);
  assert.match(workflow, /needs: verify/);
  assert.match(workflow, /public_release:/);
  assert.match(workflow, /release:native -- run/);
  assert.match(workflow, /default: false/);
  assert.doesNotMatch(workflow, /run:.*\$\{\{.*inputs/);
  assert.match(verify, /native-ci-windows\.ps1/);
  assert.match(verify, /AMDL_BROWSER_TEMP=/);
  assert.match(fs.readFileSync(path.join(root, 'playwright.config.cjs'), 'utf8'), /TMPDIR:process\.env\.AMDL_BROWSER_TEMP/);
  const windows = fs.readFileSync(path.join(root, 'electron/native-ci-windows.ps1'), 'utf8');
  assert.match(windows, /go test -race -skip '\^TestWrapperSession' \.\/\.\.\./);
  assert.match(windows, /GetOwner/);
  assert.doesNotMatch(windows, /Set-Acl|icacls|Set-LocalUser|AdjustTokenPrivileges/);
});
test('verification and release pin Node 24 actions and explicit hosted runner generations', () => {
  const pins = {
    checkout: '3d3c42e5aac5ba805825da76410c181273ba90b1',
    'setup-go': 'b7ad1dad31e06c5925ef5d2fc7ad053ef454303e',
    'setup-node': '949feb2413d6458794dcd2491c4babbbce0c15c1',
  };
  for (const [file, runners] of [
    ['verify.yml', ['ubuntu-24.04', 'windows-2025']],
    ['release-native.yml', ['windows-2025']],
  ]) {
    const text = fs.readFileSync(path.join(root, '.github/workflows', file), 'utf8');
    assert.deepEqual([...text.matchAll(/runs-on:\s+(\S+)/g)].map(match => match[1]), runners);
    const actions = [...text.matchAll(/uses:\s+actions\/([\w-]+)@(\S+)/g)];
    assert.equal(actions.length, runners.length * 3, file);
    for (const [, name, sha] of actions) assert.equal(sha, pins[name], file + ': ' + name);
    assert.equal([...text.matchAll(/node-version:\s+22\b/g)].length, runners.length, file);
    assert.equal([...text.matchAll(/go-version:\s+'1\.25\.0'/g)].length, runners.length, file);
    assert.doesNotMatch(text, /ACTIONS_ALLOW_USE_UNSECURE_NODE_VERSION|ACTIONS_RUNNER_FORCE_ACTIONS_NODE_VERSION/);
  }
});

test('release requires an explicit bounded immutable runtime asset', () => {
  const digest = 'a'.repeat(64);
  const metadata = {id:123, name:'amdl-native-runtime-ci.zip', state:'uploaded', size:400, digest:'sha256:' + digest};
  assert.doesNotThrow(() => release.assetIdentity(metadata, '123', digest));
  for (const changed of [{id:124}, {size:0}, {size:2**31}, {digest:null}, {name:'legacy-rootfs.zip'}, {state:'new'}]) {
    assert.throws(() => release.assetIdentity({...metadata, ...changed}, '123', digest), /asset_identity_invalid/);
  }
  assert.throws(() => release.assetIdentity(metadata, '../123', digest), /asset_identity_invalid/);
});
const listing = entries => '7-Zip\n----------\n' + entries.map(([name, extra = '']) => `Path = ${name}\nSize = 1\nAttributes = A\n${extra}`).join('\n\n');
test('release rejects unsafe, ambiguous, linked or oversized ZIP entries before extraction', () => {
  assert.equal(release.archiveEntries(listing([['bundle.json'], ['guest/rootfs.img'], ['evidence/build.json']])), 3);
  for (const bad of ['../secret', '/guest/a', 'guest/../a', 'guest/a:stream', 'guest/CON', 'guest/a.', 'guest//a', 'guest\\..\\a', 'profile/cookies']) {
    assert.throws(() => release.archiveEntries(listing([['bundle.json'], [bad]])), /archive_invalid/);
  }
  for (const extra of ['Symbolic Link = other', 'Hard Link = other', 'Encrypted = +', 'Size = 3000000000', 'Attributes = A lrwxrwxrwx']) {
    assert.throws(() => release.archiveEntries(listing([['bundle.json'], ['guest/a', extra]])), /archive_invalid/);
  }
  assert.throws(() => release.archiveEntries(listing([['bundle.json'], ['guest/a'], ['guest/A']])), /archive_invalid/);
  assert.throws(() => release.archiveEntries(listing([['guest/a']])), /archive_invalid/);
});
test('release requires explicit public mode for a public repository', () => {
  const env = {GITHUB_REPOSITORY:'owner/repo'};
  const privateRunner = () => JSON.stringify({private:true, full_name:'owner/repo'});
  const publicRunner=() => JSON.stringify({private:false, full_name:'owner/repo'});
  assert.equal(release.releaseRepository(env, privateRunner), 'owner/repo');
  assert.equal(release.releaseRepository({...env, AMDL_PUBLIC_RELEASE:'1'}, publicRunner), 'owner/repo');
  assert.throws(() => release.releaseRepository({...env, GH_HOST:'other.example'}, privateRunner), /mode_invalid/);
  assert.throws(() => release.releaseRepository(env, publicRunner), /private_only/);
  const policy=require('../../native-runtime/release-policy.json'), previous=policy.publicReleaseAllowed;
  try {policy.publicReleaseAllowed=false;assert.throws(()=>release.releaseRepository({...env,AMDL_PUBLIC_RELEASE:'1'},publicRunner),/mode_invalid/);} finally {policy.publicReleaseAllowed=previous;}
});
test('release refuses to replace an existing release or tag', () => {
  assert.doesNotThrow(() => release.ensureTagAvailable('o/r', 'v2.0.0', () => '[[]]'));
  assert.throws(() => release.ensureTagAvailable('o/r', 'v2.0.0', () => '[[{"tag_name":"v2.0.0"}]]'), /tag_exists/);
  assert.throws(() => release.ensureTagAvailable('o/r', 'v2.0.0', () => '[[{"ref":"refs/tags/v2.0.0"}]]'), /tag_exists/);
  assert.throws(() => release.sourceIdentity(() => ' M package.json'), /source_dirty/);
  assert.throws(() => release.ownedOutput(path.join(root, '..', 'outside.zip')), /output_must_be_new/);
});

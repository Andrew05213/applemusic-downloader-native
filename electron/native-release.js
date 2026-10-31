'use strict';

// Native release orchestration. Never boot a guest, read a user profile,
// fetch an arbitrary URL, or fall back to a stale/legacy appliance artifact.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {spawnSync} = require('node:child_process');
const {ROOT, hash, walk, verifyBundleSources} = require('./native-package');
const {makeManifest} = require('./build-native-runtime');
const {VERSION} = require('./native-version');
const LIMIT = 1024 * 1024 * 1024;

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {cwd:ROOT, env:process.env, encoding:'utf8',
    maxBuffer:16 * 1024 * 1024, shell:false, windowsHide:true, ...options});
  if (result.error || result.status !== 0) throw Error('native_release_command_failed: ' + command);
  return result.stdout;
}
function releaseRepository(env = process.env, runner = run) {
  const repo = env.GITHUB_REPOSITORY;
  const isPublicMode=env.AMDL_PUBLIC_RELEASE==='1';
  if (!/^[\w.-]+\/[\w.-]+$/.test(repo || '') || (env.GH_HOST && env.GH_HOST !== 'github.com') ||
      (isPublicMode && require('../native-runtime/release-policy.json').publicReleaseAllowed!==true)) throw Error('native_release_mode_invalid');
  const metadata = JSON.parse(runner('gh', ['api', 'repos/' + repo]));
  if (typeof metadata.private!=='boolean' || typeof metadata.full_name!=='string' || metadata.full_name.toLowerCase() !== repo.toLowerCase() ||
      (!isPublicMode && metadata.private!==true)) throw Error('native_release_private_only');
  return repo;
}
function assetIdentity(metadata, id, digest) {
  if (!/^[1-9]\d{0,15}$/.test(String(id)) || !/^[a-f0-9]{64}$/.test(digest || '') ||
      String(metadata.id) !== String(id) || metadata.state !== 'uploaded' ||
      !/^amdl-native-runtime-[\w.-]+\.zip$/.test(metadata.name || '') ||
      !Number.isSafeInteger(metadata.size) || metadata.size <= 0 || metadata.size > LIMIT ||
      metadata.digest !== 'sha256:' + digest) throw Error('native_release_asset_identity_invalid');
}
function archiveEntries(listing) {
  const split = listing.split(/\r?\n----------\r?\n/);
  if (split.length !== 2) throw Error('native_release_archive_listing_invalid');
  const entries = split[1].trim().split(/\r?\n\r?\n/).map(block => Object.fromEntries(
    block.split(/\r?\n/).map(line => {const i = line.indexOf(' = '); return i < 0 ? [] : [line.slice(0,i), line.slice(i+3)];}).filter(pair => pair.length)));
  let size = 0;
  const seen = new Set();
  if (!entries.length || entries.length > 20000) throw Error('native_release_archive_invalid');
  for (const entry of entries) {
    const name = (entry.Path || '').replaceAll('\\', '/');
    const parts = name.split('/');
    const bytes = Number(entry.Size);
    if (!name || name.startsWith('/') || parts.some(part => !part || part === '.' || part === '..' ||
        /[\x00-\x1f:*?"<>|]/.test(part) || /[. ]$/.test(part) || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(part)) ||
        !(name === 'bundle.json' || /^(guest|qemu|notices|evidence)(\/|$)/.test(name)) ||
        seen.has(name.toLowerCase()) || !Number.isSafeInteger(bytes) || bytes < 0 || bytes > LIMIT ||
        entry.Encrypted === '+' || Object.keys(entry).some(key => /link/i.test(key)) ||
        /(?:^|\s)l[rwx-]{9}/.test(entry.Attributes || '')) throw Error('native_release_archive_invalid');
    size += bytes;
    if (size > 2 * LIMIT) throw Error('native_release_archive_invalid');
    seen.add(name.toLowerCase());
  }
  if (!seen.has('bundle.json')) throw Error('native_release_archive_invalid');
  return entries.length;
}
function sevenZip() {
  if (process.platform !== 'win32') throw Error('native_windows_build_required');
  return path.join(ROOT, 'node_modules/7zip-bin/win/x64/7za.exe');
}
function verifyBundle(bundle) {
  const build = verifyBundleSources(bundle);
  const manifest = JSON.parse(fs.readFileSync(path.join(bundle, 'bundle.json'), 'utf8'));
  if (manifest.isolated !== false || manifest.controlProtocol !== 1 ||
      JSON.stringify(manifest) !== JSON.stringify(makeManifest(bundle, {isolated:false}))) throw Error('native_network_bundle_invalid');
  // Reject symlinks even in evidence; the runtime verifier hashes payload trees.
  walk(bundle);
  return build;
}
function ownedOutput(file) {
  const selected = path.resolve(file);
  const relative = path.relative(path.join(ROOT, '.artifacts'), selected);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || fs.existsSync(selected)) throw Error('native_release_output_must_be_new');
  return selected;
}
function pack(bundle, output) {
  verifyBundle(bundle);
  const target = ownedOutput(output);
  if (!/^amdl-native-runtime-[\w.-]+\.zip$/.test(path.basename(target))) throw Error('native_release_asset_name_invalid');
  fs.mkdirSync(path.dirname(target), {recursive:true});
  run(sevenZip(), ['a', '-tzip', '-mx=5', target, 'bundle.json', 'guest', 'qemu', 'notices', 'evidence'], {cwd:bundle});
  archiveEntries(run(sevenZip(), ['l', '-slt', target]));
  return {archive:target, sha256:hash(target), size:fs.statSync(target).size};
}
function unpack(archive, output, digest, size) {
  if (!/^[a-f0-9]{64}$/.test(digest || '') || fs.statSync(archive).size !== size || hash(archive) !== digest) throw Error('native_release_asset_hash_mismatch');
  const bundle = ownedOutput(output);
  archiveEntries(run(sevenZip(), ['l', '-slt', archive]));
  run(sevenZip(), ['x', '-y', '-o' + bundle, archive]);
  verifyBundle(bundle);
  return bundle;
}
function fetchBundle(env = process.env) {
  const repo = releaseRepository(env);
  const id = env.AMDL_RUNTIME_ASSET_ID, digest = env.AMDL_RUNTIME_SHA256;
  if (!/^[1-9]\d{0,15}$/.test(id || '') || !/^[a-f0-9]{64}$/.test(digest || '')) throw Error('native_release_asset_selection_required');
  const endpoint = 'repos/' + repo + '/releases/assets/' + id;
  const metadata = JSON.parse(run('gh', ['api', endpoint]));
  assetIdentity(metadata, id, digest);
  const directory = ownedOutput(path.join(ROOT, '.artifacts/native-release', crypto.randomUUID()));
  fs.mkdirSync(directory, {recursive:true});
  const archive = path.join(directory, 'runtime.zip');
  const fd = fs.openSync(archive, 'wx');
  try {run('gh', ['api', endpoint, '-H', 'Accept: application/octet-stream'], {encoding:null, stdio:['ignore', fd, 'pipe']});}
  finally {fs.closeSync(fd);}
  const bundle = unpack(archive, path.join(directory, 'bundle'), digest, metadata.size);
  return {bundle, assetID:id, sha256:digest};
}
function sourceIdentity(runner = run) {
  if (runner('git', ['status', '--porcelain', '--untracked-files=normal']).trim()) throw Error('native_release_source_dirty');
  const commit = runner('git', ['rev-parse', 'HEAD']).trim();
  if (!/^[a-f0-9]{40}$/.test(commit) || (process.env.GITHUB_SHA && process.env.GITHUB_SHA !== commit)) throw Error('native_release_source_invalid');
  return commit;
}
function ensureTagAvailable(repo, tag, runner = run) {
  // Check both published/draft releases and existing tags. Never update/clobber.
  for (const suffix of ['releases?per_page=100', 'git/matching-refs/tags/' + tag]) {
    const pages = JSON.parse(runner('gh', ['api', '--paginate', '--slurp', 'repos/' + repo + '/' + suffix]));
    if (!Array.isArray(pages) || pages.some(page => !Array.isArray(page))) throw Error('native_release_tag_check_failed');
    if (pages.flat().some(item => item.tag_name === tag || item.ref === 'refs/tags/' + tag)) throw Error('native_release_tag_exists');
  }
}
function release(env = process.env) {
  if (!['true', 'false'].includes(env.AMDL_CREATE_DRAFT || 'false')) throw Error('native_release_mode_invalid');
  const repo = releaseRepository(env);
  const commit = sourceIdentity();
  const tag = 'v' + VERSION;
  if (env.AMDL_CREATE_DRAFT === 'true') ensureTagAvailable(repo, tag);
  const input = fetchBundle(env);
  process.stdout.write(JSON.stringify({phase:'runtime_verified', commit, assetID:input.assetID, sha256:input.sha256}) + '\n');
  const log = path.join(path.dirname(input.bundle), 'build.log');
  const fd = fs.openSync(log, 'wx');
  try {run(process.execPath, ['electron/dist-native.js'], {env:{...process.env, ...env, AMDL_NATIVE_BUNDLE:input.bundle}, stdio:['ignore', fd, fd], encoding:null});}
  catch (error) {
    // Only compiler/packager output: this task never opens an account profile.
    process.stderr.write(fs.readFileSync(log, 'utf8').slice(-12000) + '\n');
    throw error;
  }
  finally {fs.closeSync(fd);}
  const resultLine = fs.readFileSync(log, 'utf8').trim().split(/\r?\n/).at(-1);
  const built = JSON.parse(resultLine);
  const installer = path.resolve(built.installer);
  const relative = path.relative(path.join(ROOT, '.artifacts/installers'), installer);
  if (relative.startsWith('..') || path.isAbsolute(relative) || path.basename(installer) !== 'AppleMusicDownloader-' + VERSION + '-Setup.exe' ||
      hash(installer) !== built.installerSHA256 ||
      fs.readFileSync(installer + '.sha256', 'utf8') !== built.installerSHA256 + '  ' + path.basename(installer) + '\n') throw Error('native_release_installer_invalid');
  const audited = require('./audit-native-installer').audit(installer);
  if (audited.installerSHA256 !== built.installerSHA256) throw Error('native_release_audit_mismatch');
  const notes = path.join(path.dirname(input.bundle), 'release-notes.txt');
  const publicMode=env.AMDL_PUBLIC_RELEASE==='1';
  fs.writeFileSync(notes, `Native build ${VERSION}\n\nSource: ${commit}\nRuntime asset: ${input.assetID}\nRuntime SHA256: ${input.sha256}\nInstaller SHA256: ${built.installerSHA256}\n\nUnsigned Windows x64 NSIS; no WSL or browser runtime required.\nThird-party rights and corresponding-source review remain incomplete.\n`, {flag:'wx'});
  if (env.AMDL_CREATE_DRAFT === 'true') {
    releaseRepository(env); // Recheck immediately before the only remote write.
    if (sourceIdentity() !== commit) throw Error('native_release_source_changed');
    ensureTagAvailable(repo, tag);
    run('gh', ['release', 'create', tag, installer, installer + '.sha256', '--repo', repo,
      '--target', commit, '--draft', '--prerelease', '--title', 'Native ' + VERSION + (publicMode?'':' (private)'), '--notes-file', notes]);
  }
  return {passed:true, commit, installerSHA256:built.installerSHA256, runtimeSHA256:input.sha256,
    installer, draftCreated:env.AMDL_CREATE_DRAFT === 'true', publicReleaseAllowed:publicMode};
}
function main() {
  if (process.env.AMDL_PUBLIC_RELEASE === '1' && require('../native-runtime/release-policy.json').publicReleaseAllowed!==true) throw Error('native_public_release_blocked');
  const [action, ...args] = process.argv.slice(2);
  let result;
  if (action === 'pack' && args.length === 2) result = pack(path.resolve(args[0]), args[1]);
  else if (action === 'run' && args.length === 0) result = release();
  else throw Error('native_release_arguments_invalid');
  process.stdout.write(JSON.stringify(result) + '\n');
}
if (require.main === module) {try {main();} catch(error) {process.stderr.write(error.message + '\n'); process.exitCode = 1;}}
module.exports = {assetIdentity, archiveEntries, releaseRepository, verifyBundle, ownedOutput, ensureTagAvailable, sourceIdentity, pack, unpack};

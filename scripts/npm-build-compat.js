'use strict';
// Build-time API adaptation only. The matching implementation remains the
// real, registry-integrity-locked minimatch 11; never rename an old algorithm
// or edit package versions to silence audit. No network or subprocesses here.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const ROOT = path.resolve(__dirname, '..');
const PATCHES = require('./npm-build-compat.lock.json').patches;
const hash = data => crypto.createHash('sha256').update(data).digest('hex');
function within(root, file) {
  const relative = path.relative(root, file);
  return relative && !relative.startsWith('..' + path.sep) && relative !== '..' && !path.isAbsolute(relative);
}
function prepare(root = ROOT, { checkOnly = false } = {}) {
  const modules = path.join(root, 'node_modules');
  if (fs.lstatSync(modules).isSymbolicLink()) throw Error('native_build_dependencies_linked');
  const canonical = fs.realpathSync(modules);
  const lock = JSON.parse(fs.readFileSync(path.join(root, 'package-lock.json'), 'utf8'));
  const plans = [];
  let engines = 0;
  for (const [relative, entry] of Object.entries(lock.packages)) {
    const name = relative.endsWith('/minimatch') ? 'minimatch' : relative.endsWith('/glob') ? 'glob' : '';
    if (!name) continue;
    const expected = name === 'minimatch' ? '11.0.3' : Number(entry.version?.split('.')[0]) >= 9 ? '14.0.3' : '7.2.3';
    const file = path.resolve(root, relative, 'package.json');
    if (!within(canonical, fs.realpathSync(file))) throw Error('native_build_compat_path_invalid');
    const installed = JSON.parse(fs.readFileSync(file, 'utf8'));
    if (entry.version !== expected || installed.name !== name || installed.version !== expected) throw Error('native_build_compat_engine_mismatch');
    engines++;
  }
  if (!engines) throw Error('native_build_compat_engine_missing');
  for (const patch of PATCHES) {
    let count = 0;
    for (const [relative, entry] of Object.entries(lock.packages)) {
      if (!(relative === 'node_modules/' + patch.package || relative.endsWith('/node_modules/' + patch.package))) continue;
      if (patch.unmodifiedVersions?.includes(entry.version)) continue;
      if (entry.version !== patch.version) throw Error('native_build_compat_version_changed');
      const directory = path.resolve(root, relative);
      if (!within(modules, directory)) throw Error('native_build_compat_path_invalid');
      const file = path.join(directory, patch.file);
      if (!within(canonical, fs.realpathSync(file)) || !fs.lstatSync(file).isFile()) throw Error('native_build_compat_path_invalid');
      const installed = JSON.parse(fs.readFileSync(path.join(directory, 'package.json'), 'utf8'));
      if (installed.name !== patch.package || installed.version !== patch.version) throw Error('native_build_compat_version_changed');
      const original = fs.readFileSync(file), digest = hash(original);
      if (digest !== patch.beforeSHA256 && digest !== patch.afterSHA256) throw Error('native_build_compat_identity_mismatch');
      if (digest === patch.beforeSHA256) {
        if (checkOnly) throw Error('native_build_compat_not_prepared');
        let text = original.toString('utf8');
        for (const edit of patch.edits || [{from:patch.from,to:patch.to,count:1}]) {
          if (text.split(edit.from).length !== edit.count + 1) throw Error('native_build_compat_patch_invalid');
          text = text.replaceAll(edit.from, edit.to);
        }
        const updated = Buffer.from(text);
        if (hash(updated) !== patch.afterSHA256) throw Error('native_build_compat_patch_invalid');
        plans.push({file, updated});
      }
      count++;
    }
    if (!count) throw Error('native_build_compat_dependency_missing');
  }
  // Validate every package before writing any adaptation. Existing patches
  // are byte-checked, making repeat installs/checks safe and deterministic.
  for (const {file, updated} of plans) fs.writeFileSync(file, updated);
  return {prepared: true, changed: plans.length};
}
if (require.main === module) {
  try {
    if (process.argv.slice(2).some(arg => arg !== '--check') || process.argv.length > 3) throw Error('native_build_compat_arguments_invalid');
    console.log(JSON.stringify(prepare(ROOT, {checkOnly: process.argv.includes('--check')})));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
module.exports = {prepare, PATCHES};

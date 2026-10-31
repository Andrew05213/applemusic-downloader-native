'use strict';

const path = require('node:path');
const {spawnSync} = require('node:child_process');
const {buildEnvironment} = require('../electron/native-build-environment');
const ROOT = path.resolve(__dirname, '..');
const MODULE = 'github.com/Andrew05213/applemusic_downloader';
const TARGETS = [
  {name: 'service', os: 'windows', target: '.'},
  {name: 'control', os: 'windows', target: './cmd/amdl-desktop'},
  {name: 'guest', os: 'linux', target: './cmd/amdl-guest'},
];
const LEGACY = ['internal/appliance', 'cmd/amdl-appliance', 'cmd/amdl-windows-probe', 'cmd/amdl-wslinterop-bootstrap'];

function audit(root = ROOT, runner = spawnSync) {
  const base = buildEnvironment(root);
  // Hosted CI already has setup-go's managed module cache. Local checks keep
  // their downloads on the repository volume, without global Go changes.
  if (!base.GOMODCACHE && base.GITHUB_ACTIONS !== 'true') {
    base.GOMODCACHE = path.join(root, '.tmp-v2-native/development/go-mod-cache');
  }
  const targets = {};
  for (const {name, os, target} of TARGETS) {
    const result = runner('go', ['list', '-mod=readonly', '-deps', '-f', '{{.ImportPath}}', target], {
      cwd: path.join(root, 'apple-music-downloader'), shell: false, windowsHide: true,
      encoding: 'utf8', maxBuffer: 8 * 1024 * 1024, timeout: 120000,
      env: {...base, GOOS: os, GOARCH: 'amd64', CGO_ENABLED: '0'},
    });
    if (result.error || result.status !== 0) throw new Error('native_dependency_list_failed:' + name);
    const dependencies = [...new Set(String(result.stdout).trim().split(/\r?\n/))];
    if (!dependencies.includes(MODULE + (target === '.' ? '' : '/' + target.slice(2)))) {
      throw new Error('native_dependency_target_missing:' + name);
    }
    for (const legacy of LEGACY) {
      if (dependencies.some(pkg => pkg === MODULE + '/' + legacy || pkg.startsWith(MODULE + '/' + legacy + '/'))) {
        throw new Error('native_legacy_dependency:' + name);
      }
    }
    targets[name] = dependencies.filter(pkg => pkg === MODULE || pkg.startsWith(MODULE + '/'));
  }
  return {passed: true, targets};
}

if (require.main === module) {
  try { console.log(JSON.stringify(audit(), null, 2)); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
module.exports = {audit, TARGETS};

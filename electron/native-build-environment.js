'use strict';

// Development-only storage. Never redirect the installed application's profile,
// login state or downloads, and never change machine-wide TEMP/Go settings.
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const ROOT = path.resolve(__dirname, '..');

function buildEnvironment(root = ROOT, base = process.env) {
  const home = path.join(root, '.tmp-v2-native', 'development');
  const directories = {
    TEMP: path.join(home, 'temp'), TMP: path.join(home, 'temp'), TMPDIR: path.join(home, 'temp'),
    GOTMPDIR: path.join(home, 'go-temp'), GOCACHE: path.join(home, 'go-cache'),
    ELECTRON_CACHE: path.join(home, 'electron-cache'),
    ELECTRON_BUILDER_CACHE: path.join(root, '.tmp-v2-native', 'builder-cache'),
    npm_config_cache: path.join(home, 'npm-cache'),
  };
  for (const directory of new Set(Object.values(directories))) fs.mkdirSync(directory, { recursive: true });
  return { ...base, ...directories };
}

function run(args, options = {}) {
  if (!Array.isArray(args) || args.length === 0) throw new Error('native_build_command_required');
  const root = options.root || ROOT;
  const result = (options.runner || spawnSync)(process.execPath, args, {
    cwd: root, env: buildEnvironment(root, options.env || process.env),
    shell: false, windowsHide: true, stdio: 'inherit',
  });
  if (result.error || result.signal) throw new Error('native_build_command_failed');
  return result.status === null ? 1 : result.status;
}

if (require.main === module) {
  try { process.exitCode = run(process.argv.slice(2)); }
  catch (error) { process.stderr.write(error.message + '\n'); process.exitCode = 1; }
}
module.exports = { buildEnvironment, run };

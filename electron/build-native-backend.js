'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { VERSION } = require('./native-version');

function buildNativeBackend(options = {}) {
  const root = options.repositoryRoot || path.resolve(__dirname, '..');
  const output = path.join(root, 'electron', 'bin', 'amdl-desktop.exe');
  fs.mkdirSync(path.dirname(output), { recursive: true });
  const result = (options.runner || spawnSync)('go', [
    'build', '-mod=readonly', '-trimpath', '-buildvcs=false',
    '-ldflags=-s -w -buildid= -X main.version='+VERSION, '-o', output, './cmd/amdl-desktop',
  ], {
    cwd: path.join(root, 'apple-music-downloader'),
    env: { ...require('./native-build-environment').buildEnvironment(root), GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '0' },
    shell: false,
    stdio: 'inherit',
  });
  if (result.error || result.status !== 0) throw new Error('native_backend_build_failed');
  return output;
}

if (require.main === module) {
  try { buildNativeBackend(); } catch { process.stderr.write('native_backend_build_failed\n'); process.exitCode = 1; }
}

module.exports = { buildNativeBackend };

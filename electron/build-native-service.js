'use strict';
const path = require('node:path');
const fs = require('node:fs');
const { spawnSync } = require('node:child_process');
const { VERSION } = require('./native-version');
const root = path.resolve(__dirname, '..');
const output = path.join(root, 'electron/bin/amdl-service.exe');
fs.mkdirSync(path.dirname(output), { recursive: true });
const result = spawnSync('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w -buildid= -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.Version='+VERSION, '-o', output, '.'], {
  cwd: path.join(root, 'apple-music-downloader'), shell: false, stdio: 'inherit', env: { ...require('./native-build-environment').buildEnvironment(root), GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '0' },
});
if (result.error || result.status !== 0) { process.stderr.write('native_service_build_failed\n'); process.exitCode = 1; }

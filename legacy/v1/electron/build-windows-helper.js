'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const packageManifest = require('../package.json');
const { createInteropRecoveryDescriptor, sha256FileSync } = require('./wslinterop-recovery');

const repositoryRoot = path.resolve(__dirname, '..');
const binDirectory = path.join(repositoryRoot, 'electron', 'bin');
const output = path.join(binDirectory, 'amdl-windows-probe.exe');
const bootstrapOutput = path.join(binDirectory, 'amdl-wslinterop-bootstrap.exe');
const descriptorOutput = path.join(repositoryRoot, 'electron', 'generated', 'wslinterop-recovery.json');
fs.mkdirSync(binDirectory, { recursive: true });
fs.mkdirSync(path.dirname(descriptorOutput), { recursive: true });

function runBuild(args, environment) {
  const result = spawnSync('go', args, {
    cwd: path.join(repositoryRoot, 'apple-music-downloader'),
    env: { ...require('./native-build-environment').buildEnvironment(repositoryRoot), ...environment },
    shell: false,
    stdio: 'inherit',
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status || 1);
}

runBuild([
  'build',
  '-trimpath',
  '-buildvcs=false',
  '-ldflags=-s -w -buildid=',
  '-o', output,
  './cmd/amdl-windows-probe',
], { GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '0' });

const helperSha256 = sha256FileSync(output);

runBuild([
  'build',
  '-trimpath',
  '-buildvcs=false',
  `-ldflags=-s -w -buildid= -X main.expectedWindowsHelperSHA256=${helperSha256}`,
  '-o', bootstrapOutput,
  './cmd/amdl-wslinterop-bootstrap',
], { GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' });

const descriptor = createInteropRecoveryDescriptor({
  targetVersion: packageManifest.version,
  bootstrapPath: bootstrapOutput,
  windowsHelperPath: output,
});
fs.writeFileSync(descriptorOutput, `${JSON.stringify(descriptor, null, 2)}\n`, 'utf8');

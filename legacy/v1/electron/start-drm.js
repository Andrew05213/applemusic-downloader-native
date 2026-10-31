'use strict';

const path = require('node:path');
const { spawn } = require('node:child_process');
const { EXECUTABLE, resolveDRMRuntime } = require('./drm-runtime');

const root = path.resolve(__dirname, '..');
const environment = { ...process.env };
delete environment.ELECTRON_RUN_AS_NODE;

try {
  const executable = path.join(resolveDRMRuntime(root), EXECUTABLE);
  const child = spawn(executable, [root], {
    cwd: root,
    stdio: 'inherit',
    env: environment,
    shell: false,
  });
  child.on('error', () => {
    console.error('Unable to launch the pinned DRM runtime. Run npm run install:drm and retry.');
    process.exitCode = 1;
  });
  child.on('exit', (code) => {
    process.exitCode = code ?? 1;
  });
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}

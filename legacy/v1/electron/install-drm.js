'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const {
  VERSION,
  ISOLATED_INSTALL_DIRECTORY,
  runtimeSource,
  assertInstalledDRMRuntime,
} = require('./drm-runtime');

function installationCommands(root, {
  manifest = require('./legacy-desktop-package.json'),
  npmCLI = process.env.npm_execpath,
} = {}) {
  const source = runtimeSource(manifest);
  if (!npmCLI || !path.isAbsolute(npmCLI) || path.basename(npmCLI) !== 'npm-cli.js') {
    throw new Error('Run this installer with npm run install:drm.');
  }
  const installDirectory = path.join(root, ISOLATED_INSTALL_DIRECTORY);
  const packageDirectory = path.join(installDirectory, 'node_modules', 'electron');
  return {
    packageDirectory,
    commands: [
      [npmCLI, 'install', '--prefix', installDirectory, '--no-save', '--package-lock=false',
        '--ignore-scripts', '--no-audit', '--no-fund', '--', source],
      [path.join(packageDirectory, 'install.js')],
    ],
  };
}

function installDRM({
  root = path.resolve(__dirname, '..'),
  run = spawnSync,
  environment = process.env,
  ...options
} = {}) {
  const plan = installationCommands(root, { npmCLI: environment.npm_execpath, ...options });
  const childEnvironment = { ...environment };
  delete childEnvironment.ELECTRON_RUN_AS_NODE;
  for (const [index, argumentsList] of plan.commands.entries()) {
    if (index === 1) {
      const installed = JSON.parse(fs.readFileSync(path.join(plan.packageDirectory, 'package.json'), 'utf8'));
      if (installed.version !== VERSION) {
        throw new Error('The installed Electron package does not match the pinned DRM version.');
      }
    }
    // Invoke npm's JavaScript entry directly: no cmd.exe/PowerShell quoting and
    // no implicit lifecycle scripts. The pinned package installer runs once.
    const result = run(process.execPath, argumentsList, {
      cwd: root,
      env: childEnvironment,
      stdio: 'inherit',
      shell: false,
      windowsHide: true,
    });
    if (result.error || result.status !== 0) {
      throw new Error(index === 0 ? 'DRM package installation failed.' : 'DRM binary installation failed.');
    }
  }
  assertInstalledDRMRuntime(path.join(plan.packageDirectory, 'dist'));
}

if (require.main === module) {
  try {
    installDRM();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}

module.exports = { installationCommands, installDRM };

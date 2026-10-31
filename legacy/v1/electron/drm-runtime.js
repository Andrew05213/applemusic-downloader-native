'use strict';

const fs = require('node:fs');
const path = require('node:path');

const VERSION = '44.5.1+wvcus';
const ELECTRON_VERSION = VERSION.split('+')[0];
const EXECUTABLE = 'electron.exe';
const ISOLATED_INSTALL_DIRECTORY = 'node_modules/.amdl-drm';
const RUNTIME_DIRECTORIES = Object.freeze([
  `${ISOLATED_INSTALL_DIRECTORY}/node_modules/electron/dist`,
  'node_modules/electron/dist',
]);
const SMOKE_PROFILE_MARKER = '.amdl-synthetic-drm-profile';

function runtimeSource(packageManifest = require('./legacy-desktop-package.json')) {
  const source = packageManifest.devDependencies?.electron;
  // Only the declared immutable ECS source is accepted as an npm package spec.
  // Reject credentials, shell-like arguments, mutable refs and URL suffixes.
  if (typeof source !== 'string'
      || !/^https:\/\/codeload\.github\.com\/castlabs\/electron-releases\/tar\.gz\/[a-f0-9]{40}$/.test(source)) {
    throw new Error('The Electron dependency must name an immutable Castlabs source archive.');
  }
  return source;
}

function assertInstalledDRMRuntime(directory) {
  const installedVersion = fs.readFileSync(path.join(directory, 'version'), 'utf8').trim();
  if (installedVersion !== VERSION || !fs.statSync(path.join(directory, EXECUTABLE)).isFile()) {
    throw new Error('Install the pinned DRM Electron runtime; refusing a stock or mismatched runtime.');
  }
  return directory;
}

function resolveDRMRuntime(root) {
  for (const relativeDirectory of RUNTIME_DIRECTORIES) {
    try {
      return assertInstalledDRMRuntime(path.join(root, relativeDirectory));
    } catch {
      // An incomplete isolated install may fall back to the exact normal install.
    }
  }
  throw new Error('The pinned DRM Electron runtime is not installed. Run npm run install:drm.');
}

module.exports = {
  VERSION,
  ELECTRON_VERSION,
  EXECUTABLE,
  ISOLATED_INSTALL_DIRECTORY,
  RUNTIME_DIRECTORIES,
  SMOKE_PROFILE_MARKER,
  runtimeSource,
  assertInstalledDRMRuntime,
  resolveDRMRuntime,
};

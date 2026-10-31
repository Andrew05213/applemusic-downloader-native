'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { getCurrentFuseWire, FuseV1Options } = require('@electron/fuses');
const { VERSION, EXECUTABLE, assertInstalledDRMRuntime, resolveDRMRuntime } = require('./drm-runtime');

const DISABLED_FUSE = '0'.charCodeAt(0);

function sha256File(filename) {
  return crypto.createHash('sha256').update(fs.readFileSync(filename)).digest('hex');
}

async function preserveDRMExecutable(runtimeDirectory, context) {
  assertInstalledDRMRuntime(runtimeDirectory);
  const source = path.join(runtimeDirectory, EXECUTABLE);
  const signature = `${source}.sig`;
  if (!fs.statSync(signature).isFile() || fs.statSync(signature).size === 0) {
    throw new Error('The upstream DRM executable signature is missing.');
  }
  // Builder injects ASAR metadata even without executable editing. Keep the
  // upstream signed executable, and never disable its integrity fuse ourselves.
  const wire = await getCurrentFuseWire(source);
  if (wire[FuseV1Options.EnableEmbeddedAsarIntegrityValidation] !== DISABLED_FUSE) {
    throw new Error('Upstream runtime requires embedded ASAR integrity; production VMP signing is required.');
  }
  const executable = path.join(context.appOutDir, `${context.packager.appInfo.productFilename}.exe`);
  fs.copyFileSync(source, executable);
  if (sha256File(executable) !== sha256File(source)) {
    throw new Error('DRM executable changed during packaging; VMP re-signing is required.');
  }
  fs.copyFileSync(signature, `${executable}.sig`);
  if (sha256File(`${executable}.sig`) !== sha256File(signature)) {
    throw new Error('DRM executable signature changed during packaging.');
  }
}

// electron-builder 26.0.12 loads this named export from the electronDist file.
function electronDist({ packager }) {
  return resolveDRMRuntime(packager.projectDir);
}

function createDRMBuildConfig({ build, repositoryRoot, environment = process.env }) {
  const config = JSON.parse(JSON.stringify(build));
  let runtimeDirectory;
  const installedRuntime = () => {
    runtimeDirectory ??= resolveDRMRuntime(repositoryRoot);
    return assertInstalledDRMRuntime(runtimeDirectory);
  };
  // 26.0.12 only resolves custom distributions through string paths, even
  // though its type declarations also accept callbacks. A hook file keeps
  // config loading independent of the installed runtime without downloading
  // a stock Electron when packaging starts.
  config.electronDist = __filename;
  config.electronVersion = VERSION;
  config.beforePack = async () => { installedRuntime(); };
  config.afterPack = async (context) => preserveDRMExecutable(installedRuntime(), context);

  // Unsigned private builds avoid winCodeSign's symlink privilege requirement.
  // Certificate selectors retain builder's signing path; the DRM hook preserves
  // the upstream executable and detached signature before that signing stage.
  const signingConfigured = ['CSC_LINK', 'WIN_CSC_LINK', 'CSC_NAME']
    .some((name) => Boolean(environment[name]));
  if (!signingConfigured) config.win.signAndEditExecutable = false;
  return config;
}

module.exports = { createDRMBuildConfig, preserveDRMExecutable, electronDist };

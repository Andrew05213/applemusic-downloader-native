'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const path = require('node:path');
const { pipeline } = require('node:stream/promises');
const { version } = require('../package.json');

const installersByMode = {
  online: `AppleMusicDownloader-${version}-Setup.exe`,
  offline: `AppleMusicDownloader-${version}-Offline-Setup.exe`,
};

async function sha256File(filename) {
  const hash = crypto.createHash('sha256');
  await pipeline(fs.createReadStream(filename), hash);
  return hash.digest('hex');
}

async function writeChecksum(directory, mode) {
  if (!Object.hasOwn(installersByMode, mode)) {
    throw new Error('Usage: write-checksums.js <directory> <online|offline>');
  }
  const installer = installersByMode[mode];
  const target = path.join(directory, installer);
  try {
    const stat = await fsp.stat(target);
    if (!stat.isFile()) throw new Error('not a file');
  } catch {
    throw new Error(`No NSIS installer found in ${directory}`);
  }

  const digest = await sha256File(target);
  const sidecar = `${target}.sha256`;
  const temporary = `${sidecar}.${process.pid}.tmp`;
  try {
    await fsp.writeFile(temporary, `${digest}  ${installer}\n`, { encoding: 'utf8', mode: 0o600 });
    await fsp.rename(temporary, sidecar);
  } finally {
    await fsp.rm(temporary, { force: true });
  }
  console.log(`Wrote ${installer}.sha256`);
  return sidecar;
}

async function main() {
  const directory = path.resolve(process.argv[2] || 'dist');
  const mode = process.argv[3];
  await writeChecksum(directory, mode);
}

if (require.main === module) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

module.exports = { sha256File, writeChecksum };

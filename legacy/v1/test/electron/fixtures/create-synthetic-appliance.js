'use strict';

// CI-only fixture generator. The archive intentionally contains no executable,
// Wrapper, Android, Apple, CDM, credentials, tokens, or media. It cannot boot as
// a WSL distribution and must never be uploaded as a release artifact.

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const zlib = require('node:zlib');
const { pathToFileURL } = require('node:url');
const packageManifest = require('../../../electron/legacy-desktop-package.json');
const {
  createArtifact,
  createInputs,
  createManifest,
} = require('./appliance-manifest-document');

// Packaging enforces an exact desktop/Appliance version match. Synthetic
// identity remains conspicuous in filenames, metadata and the release policy.
const VERSION = packageManifest.version;
const MARKER = 'CI-SYNTHETIC-NOT-FOR-RUNTIME';
const ROOTFS_NAME = `applemusic-downloader-wsl-rootfs-${MARKER}-${VERSION}-amd64.tar.gz`;
const RUNTIME_NAME = `applemusic-downloader-runtime-${MARKER}-${VERSION}-amd64.tar.gz`;
const OFFLINE_MANIFEST_NAME = 'synthetic-offline-appliance-manifest.json';
const ONLINE_MANIFEST_NAME = 'synthetic-online-appliance-manifest.json';
const METADATA_NAME = 'synthetic-appliance-fixture-metadata.json';
const SOURCE_DATE_EPOCH = 1785283200;

function parseOutput(argumentsList) {
  const index = argumentsList.indexOf('--output');
  if (index === -1 || !argumentsList[index + 1] || argumentsList.length !== 2) {
    throw new Error('usage: create-synthetic-appliance.js --output DIRECTORY');
  }
  return path.resolve(argumentsList[index + 1]);
}

function writeOctal(buffer, offset, length, value) {
  const encoded = Math.trunc(value).toString(8).padStart(length - 1, '0');
  if (encoded.length >= length) throw new Error('tar numeric field overflow');
  buffer.write(encoded, offset, length - 1, 'ascii');
  buffer[offset + length - 1] = 0;
}

function tarHeader(name, size) {
  if (!/^[\x20-\x7e]{1,100}$/.test(name) || name.includes('..') || name.startsWith('/')) {
    throw new Error(`unsafe synthetic tar entry: ${name}`);
  }
  const header = Buffer.alloc(512);
  header.write(name, 0, 100, 'ascii');
  writeOctal(header, 100, 8, 0o644);
  writeOctal(header, 108, 8, 0);
  writeOctal(header, 116, 8, 0);
  writeOctal(header, 124, 12, size);
  writeOctal(header, 136, 12, SOURCE_DATE_EPOCH);
  header.fill(0x20, 148, 156);
  header[156] = '0'.charCodeAt(0);
  header.write('ustar\0', 257, 6, 'ascii');
  header.write('00', 263, 2, 'ascii');
  header.write('root', 265, 4, 'ascii');
  header.write('root', 297, 4, 'ascii');
  const checksum = header.reduce((sum, byte) => sum + byte, 0);
  const checksumText = checksum.toString(8).padStart(6, '0');
  header.write(checksumText, 148, 6, 'ascii');
  header[154] = 0;
  header[155] = 0x20;
  return header;
}

function createTar(entries) {
  const chunks = [];
  for (const [name, text] of [...entries].sort(([left], [right]) => left.localeCompare(right, 'en'))) {
    const contents = Buffer.from(text, 'utf8');
    chunks.push(tarHeader(name, contents.length), contents);
    const padding = (512 - (contents.length % 512)) % 512;
    if (padding) chunks.push(Buffer.alloc(padding));
  }
  chunks.push(Buffer.alloc(1024));
  return Buffer.concat(chunks);
}

function sha256(contents) {
  return crypto.createHash('sha256').update(contents).digest('hex');
}

function manifest(rootfs, runtime) {
  return createManifest({
    version: VERSION,
    builtAt: new Date(SOURCE_DATE_EPOCH * 1000).toISOString(),
    rootfs,
    runtime,
    inputs: createInputs(),
    releasePolicy: {
      publicReleaseAllowed: false,
      blockers: [`${MARKER}: synthetic fixture cannot be released or used as a runtime.`],
    },
  });
}

function writeJSON(filename, value) {
  fs.writeFileSync(filename, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600 });
}

function main(argumentsList = process.argv.slice(2)) {
  const output = parseOutput(argumentsList);
  fs.mkdirSync(output, { recursive: true });
  const artifacts = {};
  for (const [kind, file] of [['rootfs', ROOTFS_NAME], ['runtime', RUNTIME_NAME]]) {
    const entries = [[`${MARKER}.txt`, [
      `This is a deterministic CI packaging fixture for ${kind}.`,
      'It contains no third-party runtime material and cannot run as WSL.',
      'Never publish this archive or an installer containing it.',
      '',
    ].join('\n')]];
    if (kind === 'rootfs') entries.push(['etc/wsl.conf', `[user]\ndefault=root\n# ${MARKER}\n`]);
    const tar = createTar(entries);
    const archive = zlib.gzipSync(tar, { level: 9, mtime: 0 });
    const archivePath = path.join(output, file);
    fs.writeFileSync(archivePath, archive, { mode: 0o600 });
    artifacts[kind] = createArtifact(kind, {
      file,
      url: pathToFileURL(archivePath).toString(),
      sha256: sha256(archive),
      size: archive.length,
      uncompressedSize: tar.length,
      compressedBytes: archive.length,
      uncompressedBytes: tar.length,
    });
  }

  writeJSON(path.join(output, OFFLINE_MANIFEST_NAME), manifest(artifacts.rootfs, artifacts.runtime));
  writeJSON(path.join(output, ONLINE_MANIFEST_NAME), manifest(
    { ...artifacts.rootfs, url: `https://artifacts.invalid/applemusic-downloader/${ROOTFS_NAME}` },
    { ...artifacts.runtime, url: `https://artifacts.invalid/applemusic-downloader/${RUNTIME_NAME}` },
  ));
  writeJSON(path.join(output, METADATA_NAME), {
    schemaVersion: 1,
    marker: MARKER,
    version: VERSION,
    archive: ROOTFS_NAME,
    runtimeArchive: RUNTIME_NAME,
    sha256: artifacts.rootfs.sha256,
    runtimeSha256: artifacts.runtime.sha256,
    compressedBytes: artifacts.rootfs.compressedBytes,
    uncompressedBytes: artifacts.rootfs.uncompressedBytes,
    runtimeCompressedBytes: artifacts.runtime.compressedBytes,
    runtimeUncompressedBytes: artifacts.runtime.uncompressedBytes,
    containsThirdPartyMaterials: false,
    runtimeUsable: false,
    releaseUploadAllowed: false,
  });
  process.stdout.write(`${JSON.stringify({
    output,
    archive: path.join(output, ROOTFS_NAME),
    runtimeArchive: path.join(output, RUNTIME_NAME),
    offlineManifest: path.join(output, OFFLINE_MANIFEST_NAME),
    onlineManifest: path.join(output, ONLINE_MANIFEST_NAME),
  })}\n`);
}

if (require.main === module) main();

module.exports = {
  MARKER,
  METADATA_NAME,
  OFFLINE_MANIFEST_NAME,
  ONLINE_MANIFEST_NAME,
  ROOTFS_NAME,
  RUNTIME_NAME,
  VERSION,
  createTar,
  main,
  manifest,
};

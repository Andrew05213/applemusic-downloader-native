'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const asar = require('@electron/asar');
const { sha256File, validateApplianceManifest } = require('./appliance-manifest');
const { validateInteropRecoveryDescriptor } = require('./wslinterop-recovery');

const root = path.resolve(__dirname, '..');
const defaultResourcesDirectory = path.join(root, 'dist', 'win-unpacked', 'resources');
const SCAN_CHUNK_BYTES = 64 * 1024;
const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const EXPECTED_APP_FILES = [
  'electron/appliance-manifest.js',
  'electron/apple-music-browser.js',
  'electron/credentials.js',
  'electron/playback-support.js',
  'electron/official-web-player.js',
  'electron/desktop-state.js',
  'electron/diagnostic-export.js',
  'electron/loopback-proxy.js',
  'electron/process-runner.js',
  'electron/release-manifest.js',
  'electron/setup-controller.js',
  'electron/windows-capabilities.js',
  'electron/windows-support.js',
  'electron/wslinterop-recovery.js',
  'electron/wsl-manager.js',
  'package.json',
  'src/main/credentials.html',
  'src/main/credentials.js',
  'src/main/main.js',
  'src/main/setup.css',
  'src/main/setup.html',
  'src/main/setup.js',
  'src/preload/credentials-preload.js',
  'src/preload/apple-music-preload.js',
  'src/preload/preload.js',
  'src/preload/setup-preload.js',
  'src/renderer/client-navigation.js',
  'src/renderer/client.css',
  'src/renderer/browse.js',
  'src/renderer/catalog.js',
  'src/renderer/playback.js',
  'src/renderer/index.html',
  'src/renderer/job-data.js',
  'src/renderer/renderer.js',
  'src/renderer/styles.css',
];

function fileContainsBufferSync(file, needle) {
  if (needle.length === 0) return false;
  const descriptor = fs.openSync(file, 'r');
  const chunk = Buffer.allocUnsafe(SCAN_CHUNK_BYTES);
  let carry = Buffer.alloc(0);
  try {
    while (true) {
      const bytesRead = fs.readSync(descriptor, chunk, 0, chunk.length, null);
      if (bytesRead === 0) return false;
      const current = chunk.subarray(0, bytesRead);
      const window = carry.length === 0 ? current : Buffer.concat([carry, current]);
      if (window.includes(needle)) return true;
      const overlap = Math.min(needle.length - 1, window.length);
      carry = overlap === 0 ? Buffer.alloc(0) : Buffer.from(window.subarray(window.length - overlap));
    }
  } finally {
    fs.closeSync(descriptor);
  }
}

function sha256AsarMemberSync(archive, member) {
  const info = asar.statFile(archive, member.replaceAll('/', path.sep), false);
  const rawHeader = asar.getRawHeader(archive);
  if (!info || info.unpacked === true || !Number.isInteger(info.size) || info.size < 0
      || typeof info.offset !== 'string' || !/^(0|[1-9][0-9]*)$/.test(info.offset)
      || !Number.isInteger(rawHeader.headerSize) || rawHeader.headerSize < 0) {
    throw new Error(`Packaged app.asar member ${member} is not a canonical packed file`);
  }
  const offset = Number(info.offset);
  const position = 8 + rawHeader.headerSize + offset;
  const archiveSize = fs.statSync(archive).size;
  if (!Number.isSafeInteger(offset) || !Number.isSafeInteger(position)
      || position < 0 || position + info.size > archiveSize) {
    throw new Error(`Packaged app.asar member ${member} has an invalid extent`);
  }
  const descriptor = fs.openSync(archive, 'r');
  const hash = crypto.createHash('sha256');
  const chunk = Buffer.allocUnsafe(Math.min(SCAN_CHUNK_BYTES, Math.max(1, info.size)));
  let consumed = 0;
  try {
    while (consumed < info.size) {
      const wanted = Math.min(chunk.length, info.size - consumed);
      const bytesRead = fs.readSync(descriptor, chunk, 0, wanted, position + consumed);
      if (bytesRead === 0) throw new Error(`Packaged app.asar member ${member} is truncated`);
      hash.update(chunk.subarray(0, bytesRead));
      consumed += bytesRead;
    }
  } finally {
    fs.closeSync(descriptor);
  }
  return hash.digest('hex');
}

function verifyApplicationArchive({
  resourcesDirectory,
  expectedFiles = EXPECTED_APP_FILES,
  expectedRendererSha256 = '',
  secretSentinel = process.env.AMDL_SECRET_SENTINEL || '',
}) {
  if (Buffer.byteLength(secretSentinel, 'utf8') > 4096) throw new Error('Secret sentinel is too long');
  const archive = path.join(resourcesDirectory, 'app.asar');
  if (!fs.statSync(archive).isFile()) throw new Error('Packaged app.asar is missing');
  asar.uncache(archive);
  const entries = asar.listPackage(archive)
    .map((entry) => entry.replace(/^[/\\]+/, '').replaceAll('\\', '/'))
    .filter(Boolean);
  const files = entries.filter((entry) => !entries.some((candidate) => candidate.startsWith(`${entry}/`))).sort();
  const expected = [...expectedFiles].sort();
  if (JSON.stringify(files) !== JSON.stringify(expected)) {
    const missing = expected.filter((entry) => !files.includes(entry));
    const unexpected = files.filter((entry) => !expected.includes(entry));
    throw new Error(`Packaged app.asar whitelist mismatch (missing: ${missing.join(', ') || '<none>'}; unexpected: ${unexpected.join(', ') || '<none>'})`);
  }
  if (expectedRendererSha256) {
    if (!SHA256_PATTERN.test(expectedRendererSha256)
        || sha256AsarMemberSync(archive, 'src/renderer/renderer.js') !== expectedRendererSha256) {
      throw new Error('Packaged renderer SHA-256 does not match its Appliance manifest');
    }
  }
  if (secretSentinel) {
    const needle = Buffer.from(secretSentinel, 'utf8');
    // ASAR stores packed file payloads without compression. Scan the archive
    // stream directly instead of extracting entries: @electron/asar's Windows
    // extractor cannot reliably reopen archives below non-ASCII paths.
    if (fileContainsBufferSync(archive, needle)) {
      throw new Error('Secret sentinel found in packaged app.asar');
    }
  }
}

async function verifyPackagedResources({
  mode,
  resourcesDirectory = defaultResourcesDirectory,
  verifyAppArchive = true,
  secretSentinel,
}) {
  if (!['online', 'offline'].includes(mode)) throw new Error('mode must be online or offline');
  const applianceDirectory = path.join(resourcesDirectory, 'appliance');
  const helper = path.join(resourcesDirectory, 'bin', 'amdl-windows-probe.exe');
  const bootstrap = path.join(resourcesDirectory, 'bin', 'amdl-wslinterop-bootstrap.exe');
  const recoveryPath = path.join(resourcesDirectory, 'recovery', 'wslinterop-recovery.json');
  const notice = path.join(resourcesDirectory, 'NOTICE');
  const runv2License = path.join(resourcesDirectory, 'third-party-licenses', 'runv2-LICENSE');
  const manifestPath = path.join(applianceDirectory, 'manifest.json');
  if (!fs.statSync(helper).isFile()) throw new Error('Packaged Windows capability helper is missing');
  if (!fs.statSync(bootstrap).isFile()) throw new Error('Packaged WSLInterop recovery bootstrap is missing');
  if (!fs.statSync(notice).isFile()) throw new Error('Packaged NOTICE is missing');
  if (!fs.statSync(runv2License).isFile()) throw new Error('Packaged runv2 third-party license is missing');
  if (!fs.statSync(manifestPath).isFile()) throw new Error('Packaged appliance manifest is missing');
  const manifest = validateApplianceManifest(JSON.parse(fs.readFileSync(manifestPath, 'utf8')));
  if (!fs.statSync(recoveryPath).isFile()) throw new Error('Packaged WSLInterop recovery descriptor is missing');
  const recovery = validateInteropRecoveryDescriptor(JSON.parse(fs.readFileSync(recoveryPath, 'utf8')), {
    expectedVersion: manifest.version,
  });
  for (const [label, filename, binding] of [
    ['Windows capability helper', helper, recovery.windowsHelper],
    ['WSLInterop recovery bootstrap', bootstrap, recovery.bootstrap],
  ]) {
    if (fs.statSync(filename).size !== binding.size || (await sha256File(filename)) !== binding.sha256) {
      throw new Error(`Packaged ${label} does not match its recovery descriptor`);
    }
  }
  if (verifyAppArchive) {
    verifyApplicationArchive({
      resourcesDirectory,
      expectedRendererSha256: manifest.inputs?.rendererSha256 || '',
      secretSentinel,
    });
  }
  const entries = fs.readdirSync(applianceDirectory).sort();
  const archives = entries.filter((name) => name.toLowerCase().endsWith('.tar.gz'));
  const unexpected = entries.filter((name) => name !== 'manifest.json' && !name.toLowerCase().endsWith('.tar.gz'));
  if (unexpected.length > 0) throw new Error(`Unexpected packaged appliance resources: ${unexpected.join(', ')}`);

  if (mode === 'online') {
    if (archives.length !== 0) throw new Error('Online installer must not embed Appliance archives');
  } else {
    const expectedArchives = [manifest.rootfs.file, manifest.runtime.file].sort();
    if (JSON.stringify(archives) !== JSON.stringify(expectedArchives)) {
      throw new Error(`Offline installer must embed exactly the manifest rootfs and runtime archives (found ${archives.join(', ')})`);
    }
    for (const [label, artifact] of [['rootfs', manifest.rootfs], ['runtime', manifest.runtime]]) {
      const archive = path.join(applianceDirectory, artifact.file);
      if (await sha256File(archive) !== artifact.sha256) throw new Error(`Packaged offline ${label} SHA-256 does not match its manifest`);
      if (fs.statSync(archive).size !== artifact.size) {
        throw new Error(`Packaged offline ${label} size does not match its manifest`);
      }
    }
  }
  console.log(`Verified ${mode} packaged appliance resources`);
}

async function main() {
  const mode = process.argv[2];
  if (!['online', 'offline'].includes(mode)) throw new Error('Usage: verify-packaged-resources.js <online|offline> [resources-directory]');
  const resourcesDirectory = process.argv[3] ? path.resolve(process.argv[3]) : defaultResourcesDirectory;
  await verifyPackagedResources({ mode, resourcesDirectory });
}

if (require.main === module) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

module.exports = {
  EXPECTED_APP_FILES,
  fileContainsBufferSync,
  verifyApplicationArchive,
  verifyPackagedResources,
};

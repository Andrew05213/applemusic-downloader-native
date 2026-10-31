'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const path = require('node:path');

const {
  validateApplianceManifest,
  verifyArtifactFile,
} = require('./appliance-manifest');
const {
  EXPECTED_RUNTIME,
  EXPECTED_SERVICE,
  applianceIdentityMatches,
} = require('./wsl-manager');
const {
  adoptLocalPocWhileLocked,
  assertDistroMarkerAbsent,
  registrationIdentity,
  sameWindowsPath,
} = require('./local-poc-adoption');
const { withLocalPocMigrationLock } = require('./local-poc-migration-lock');
const { WslSmokeError, safeIdentity } = require('./wsl-smoke');

const DISTRO_NAME = 'AppleMusicDownloader';
const RELEASE_ROOT = '/opt/applemusic-downloader/releases';
const CURRENT_LINK = '/opt/applemusic-downloader/current';
const RELEASE_METADATA = '.amdl-release.json';
const UPDATE_TRANSACTION = '/var/lib/applemusic-downloader/update-transaction.json';
const BOOTSTRAP_STATE_FIELD = 'localPocBootstrap';
const SHA256_PATTERN = /^[a-f0-9]{64}$/;
const VERSION_PATTERN = /^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$/;
const MAX_RUNTIME_BYTES = 512 * 1024 * 1024;
const MAX_LIST_BYTES = 64 * 1024 * 1024;
const MAX_COMPARE_BYTES = 2 * 1024 * 1024;
const MAX_LEGACY_MOUNT_LAYERS = 32;
const VERIFIED_WSL_HOST_WARNING_LINES = new Set([
  'wsl: A localhost proxy configuration was detected but not mirrored into WSL. WSL in NAT mode does not support localhost proxies.',
  'wsl: 检测到 localhost 代理配置，但未镜像到 WSL。NAT 模式下的 WSL 不支持 localhost 代理。',
]);

function reject(step) {
  throw new WslSmokeError('local_poc_bootstrap_rejected', step);
}

function fail(step) {
  throw new WslSmokeError('local_poc_bootstrap_failed', step);
}

function releaseDirectory(version) {
  if (!VERSION_PATTERN.test(version || '')) reject('bootstrap_version');
  return `${RELEASE_ROOT}/${version}`;
}

function releaseMetadataPath(version) {
  return `${releaseDirectory(version)}/${RELEASE_METADATA}`;
}

function valueIsAbsent(value) {
  return value === undefined || value === null;
}

function canonicalManifestDigest(manifest) {
  return crypto.createHash('sha256').update(JSON.stringify(manifest), 'utf8').digest('hex');
}

function assertLocalPocManifest(manifest, { legacy = false } = {}) {
  if (manifest.channel !== 'local-poc' || manifest.releasePolicy.publicReleaseAllowed !== false) {
    reject(legacy ? 'bootstrap_legacy_manifest' : 'bootstrap_target_manifest');
  }
  return manifest;
}

async function assertMetadataAbsent(manager, version) {
  const target = releaseMetadataPath(version);
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/test', '!', '-e', target,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/test', '!', '-L', target,
    ], { timeoutMs: 15_000 });
  } catch {
    reject('bootstrap_metadata_exists');
  }
}

async function assertLegacyReleaseTree(manager, manifest, { allowedMetadata = null } = {}) {
  const release = releaseDirectory(manifest.version);
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/test', '-d', release,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/test', '!', '-L', release,
    ], { timeoutMs: 15_000 });
    const current = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/readlink', '-f', '--', CURRENT_LINK,
    ], { timeoutMs: 15_000 });
    if (current.stdout.trim() !== release) reject('bootstrap_release_tree');
    if (allowedMetadata) {
      try { await readPublishedMetadata(manager, allowedMetadata); } catch {
        await assertMetadataAbsent(manager, manifest.version);
      }
    } else {
      await assertMetadataAbsent(manager, manifest.version);
    }
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('bootstrap_release_tree');
  }
  return release;
}

async function validateLegacyBootstrapCandidate(manager, {
  credentialsPresent = false,
  allowRecoveryMarker = false,
} = {}) {
  const state = await manager.readState();
  const recovery = state?.[BOOTSTRAP_STATE_FIELD];
  if (!state || state.schemaVersion !== 1 || state.distroName !== DISTRO_NAME
      || state.ownership !== undefined && state.ownership !== null
      || state.installTransaction !== undefined && state.installTransaction !== null
      || state.updateTransaction !== undefined && state.updateTransaction !== null
      || state.pendingManifest !== undefined && state.pendingManifest !== null
      || !valueIsAbsent(state.installedRuntimeSha256)
      || !valueIsAbsent(state.installedRuntimeTransactionId)
      || credentialsPresent || (!allowRecoveryMarker && !valueIsAbsent(recovery))) {
    reject('bootstrap_host_state');
  }
  let hostOwner;
  try { hostOwner = await manager.readOwnershipId(); } catch { reject('bootstrap_host_marker'); }
  if (hostOwner !== '') reject('bootstrap_host_marker');
  await assertDistroMarkerAbsent(manager);

  const distro = await manager.registeredDistro();
  if (!distro || distro.name !== DISTRO_NAME || distro.version !== 2) reject('bootstrap_distro');
  const registered = await registrationIdentity(manager);
  let expectedBase;
  try { expectedBase = await manager.canonicalizeWindowsPathWithHelper(manager.installDir); } catch { reject('bootstrap_registration'); }
  if (!sameWindowsPath(registered.basePath, expectedBase.canonicalPath)
      || !sameWindowsPath(state.installDir, expectedBase.canonicalPath)) {
    reject('bootstrap_registration');
  }

  let manifest;
  try { manifest = assertLocalPocManifest(validateApplianceManifest(state.manifest), { legacy: true }); } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('bootstrap_legacy_manifest');
  }
  if (state.installedVersion !== manifest.version) reject('bootstrap_legacy_manifest');

  let probe;
  let status;
  try {
    probe = JSON.parse((await manager.runControl('probe', { timeoutMs: 20_000 })).stdout);
    status = await manager.status();
  } catch { reject('bootstrap_runtime_identity'); }
  if (!applianceIdentityMatches(probe, EXPECTED_SERVICE, EXPECTED_RUNTIME, manifest.version)
      || !applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, manifest.version)
      || !valueIsAbsent(status.runtimeSha256)
      || !valueIsAbsent(status.runtimeTransactionId)
      || !valueIsAbsent(status.updateInProgress)
      || status.credentialsConfigured !== false
      || status.configured !== true
      || !['running', 'starting', 'stopped'].includes(status.state)) {
    reject('bootstrap_runtime_identity');
  }
  await assertLegacyReleaseTree(manager, manifest);
  let active;
  try {
    if (!['running', 'starting'].includes(status.state)) reject('bootstrap_active_jobs');
    await manager.prepareLocalEndpoint({ expectedVersion: manifest.version });
    active = await manager.activeJobs();
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('bootstrap_active_jobs');
  }
  if (active.length !== 0) reject('bootstrap_active_jobs');
  return { state, manifest, status, registration: registered, recovery };
}

function validateWindowsInputPath(value, step) {
  if (typeof value !== 'string' || value !== value.trim() || value.includes('\0')
      || /^(?:\\\\|\/\/|\\\\[?.]\\)/.test(value)
      || !/^[A-Za-z]:[\\/]/.test(value) || !path.win32.isAbsolute(value)) {
    reject(step);
  }
  return path.win32.normalize(value);
}

async function windowsFileToVerifiedWslPath(manager, windowsPath, artifact, {
  artifactVerifier = verifyArtifactFile,
  step = 'bootstrap_artifact',
} = {}) {
  const normalized = validateWindowsInputPath(windowsPath, step);
  try {
    await artifactVerifier(artifact, normalized);
    const canonical = await manager.canonicalizeWindowsFileWithHelper(normalized);
    if (!sameWindowsPath(canonical.canonicalPath, normalized)) reject(step);
    const converted = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--exec',
      '/usr/bin/wslpath', '-a', '-u', normalized,
    ], { timeoutMs: 15_000 });
    const wslPath = converted.stdout.trim();
    if (!/^\/mnt\/[a-z](?:\/|$)/i.test(wslPath) || path.posix.normalize(wslPath) !== wslPath) reject(step);
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/test', '-f', wslPath,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/test', '!', '-L', wslPath,
    ], { timeoutMs: 15_000 });
    const stat = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/stat', '-c', '%s', '--', wslPath,
    ], { timeoutMs: 30_000 });
    const digest = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/sha256sum', '--', wslPath,
    ], { timeoutMs: 180_000 });
    if (Number(stat.stdout.trim()) !== artifact.size
        || digest.stdout.slice(0, 64) !== artifact.sha256) reject(step);
    return { windowsPath: normalized, wslPath, sha256: artifact.sha256, size: artifact.size };
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject(step);
  }
}

async function loadTargetManifest(manager, filename, legacyManifest, {
  manifestReader,
} = {}) {
  const normalizedPath = validateWindowsInputPath(filename, 'bootstrap_target_manifest');
  let document;
  try {
    const canonical = await manager.canonicalizeWindowsFileWithHelper(normalizedPath);
    if (!sameWindowsPath(canonical.canonicalPath, normalizedPath)) reject('bootstrap_target_manifest');
    if (manifestReader) {
      document = await manifestReader(normalizedPath);
    } else {
      const info = await fsp.lstat(normalizedPath);
      if (!info.isFile() || info.isSymbolicLink() || info.size <= 0 || info.size > 1024 * 1024) reject('bootstrap_target_manifest');
      document = JSON.parse(await fsp.readFile(normalizedPath, 'utf8'));
    }
    const manifest = assertLocalPocManifest(validateApplianceManifest(document));
    if (manifest.version !== legacyManifest.version || manifest.runtime.sha256 === legacyManifest.runtime.sha256
        || manifest.runtime.size > MAX_RUNTIME_BYTES) reject('bootstrap_target_manifest');
    return { manifest, manifestSha256: canonicalManifestDigest(manifest), path: normalizedPath };
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('bootstrap_target_manifest');
  }
}

function runtimeExcludedRelativePath(relative) {
  return relative === RELEASE_METADATA
    || relative === 'rootfs/proc'
    || relative.startsWith('rootfs/data/')
    || relative.startsWith('rootfs/proc/')
    || relative.startsWith('rootfs/dev/');
}

function normalizeArchiveMember(value, version) {
  if (typeof value !== 'string' || value.includes('\\') || /[\0\r\n]/.test(value) || value.trim() !== value) reject('bootstrap_archive_list');
  let member = value;
  while (member.startsWith('./')) member = member.slice(2);
  member = member.replace(/\/$/, '');
  if (member === '' || member === '.') return undefined;
  if (member.startsWith('/') || path.posix.normalize(member) !== member
      || member.split('/').some((part) => part === '' || part === '.' || part === '..')) reject('bootstrap_archive_list');
  const prefix = `opt/applemusic-downloader/releases/${version}`;
  if (['opt', 'opt/applemusic-downloader', 'opt/applemusic-downloader/releases'].includes(member)) return undefined;
  if (member === prefix) return '';
  if (!member.startsWith(`${prefix}/`)) reject('bootstrap_archive_list');
  const relative = member.slice(prefix.length + 1);
  return runtimeExcludedRelativePath(relative) ? null : relative;
}

function normalizeCurrentMember(value) {
  if (typeof value !== 'string' || value.includes('\\') || /[\0\r\n]/.test(value)) reject('bootstrap_current_list');
  if (value === '') return '';
  if (value.startsWith('/') || path.posix.normalize(value) !== value
      || value.split('/').some((part) => part === '' || part === '.' || part === '..')) reject('bootstrap_current_list');
  return runtimeExcludedRelativePath(value) ? null : value;
}

function tarExcludeArguments(version) {
  const prefix = `./opt/applemusic-downloader/releases/${version}`;
  return [
    `--exclude=${prefix}/${RELEASE_METADATA}`,
    `--exclude=${prefix}/rootfs/data/*`,
    `--exclude=${prefix}/rootfs/proc`,
    `--exclude=${prefix}/rootfs/proc/*`,
    `--exclude=${prefix}/rootfs/dev/*`,
  ];
}

function tarComparePrefixExclusions(version) {
  const prefix = `./opt/applemusic-downloader/releases/${version}`;
  return [
    '--exclude=.',
    '--exclude=./opt',
    '--exclude=./opt/applemusic-downloader',
    '--exclude=./opt/applemusic-downloader/releases',
    `--exclude=${prefix}`,
  ];
}

function runtimeMountInspectionCommand() {
  return [
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
    '/usr/bin/findmnt', '--json', '--output', 'TARGET,SOURCE,FSTYPE',
  ];
}

function runtimeProofCommands({ version, artifactWslPath }) {
  const release = releaseDirectory(version);
  const trustedSnapshot = /^\/var\/lib\/applemusic-downloader\/\.bootstrap-[a-f0-9]{64}\.bundle$/.test(artifactWslPath || '');
  if ((!/^\/mnt\/[a-z]\//i.test(artifactWslPath || '') && !trustedSnapshot)
      || path.posix.normalize(artifactWslPath) !== artifactWslPath) reject('bootstrap_artifact');
  const exclusions = tarExcludeArguments(version);
  return {
    mounts: runtimeMountInspectionCommand(),
    archiveList: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/env', 'LC_ALL=C', '/usr/bin/tar', '--list', '--gzip', '--quoting-style=escape',
      '--file', artifactWslPath, ...exclusions,
    ],
    currentList: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/find', release, '-xdev', '(',
      '-path', `${release}/rootfs/data`, '-o',
      '-path', `${release}/rootfs/proc`, '-o',
      '-path', `${release}/rootfs/dev`,
      ')', '-printf', '%P\\0', '-prune', '-o', '-printf', '%P\\0',
    ],
    compare: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/env', 'LC_ALL=C', '/usr/bin/tar', '--compare', '--gzip', '--one-file-system',
      '--no-recursion', '--strip-components=5', '--show-transformed-names', '--quoting-style=escape',
      '--directory', release, '--file', artifactWslPath,
      ...tarComparePrefixExclusions(version), ...exclusions,
    ],
  };
}

function captureWsl(manager, args, {
  timeoutMs = 60_000,
  maxStdoutBytes = MAX_LIST_BYTES,
  maxStderrBytes = MAX_COMPARE_BYTES,
} = {}) {
  return manager.resolveWslExecutable().then((executable) => new Promise((resolve, rejectPromise) => {
    let child;
    let stdoutBytes = 0;
    let stderrBytes = 0;
    const stdout = [];
    const stderr = [];
    let settled = false;
    let failure = null;
    let timer;
    let forceTimer;
    const finish = (error, result) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      if (forceTimer) clearTimeout(forceTimer);
      if (error) rejectPromise(error);
      else resolve(result);
    };
    const abort = (step) => {
      if (!failure) failure = new WslSmokeError('local_poc_bootstrap_failed', step);
      child?.kill?.();
      if (!forceTimer) forceTimer = setTimeout(() => finish(failure), 5_000);
    };
    try {
      child = manager.runner.spawn(executable, args, {
        windowsHide: true,
        shell: false,
        stdio: ['ignore', 'pipe', 'pipe'],
        env: bootstrapProofEnvironment(manager.processEnvironment),
      });
    } catch {
      finish(new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_process_spawn'));
      return;
    }
    if (!child?.stdout || !child?.stderr) {
      abort('bootstrap_process_stream');
      return;
    }
    child.stdout.on('data', (chunk) => {
      stdoutBytes += chunk.length;
      if (stdoutBytes > maxStdoutBytes) abort('bootstrap_process_output');
      else stdout.push(Buffer.from(chunk));
    });
    child.stderr.on('data', (chunk) => {
      stderrBytes += chunk.length;
      if (stderrBytes > maxStderrBytes) abort('bootstrap_process_output');
      else stderr.push(Buffer.from(chunk));
    });
    child.once('error', () => finish(new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_process_spawn')));
    child.once('close', (exitCode, signal) => {
      if (failure) finish(failure);
      else finish(null, { exitCode, signal, stdout: Buffer.concat(stdout), stderr: Buffer.concat(stderr) });
    });
    timer = setTimeout(() => abort('bootstrap_process_timeout'), timeoutMs);
  }));
}

function decodeUtf8(buffer, step) {
  const value = buffer.toString('utf8');
  if (value.includes('\uFFFD') || value.includes('\0')) reject(step);
  return value;
}

function bootstrapProofEnvironment(source = {}) {
  const environment = {};
  for (const [name, value] of Object.entries(source || {})) {
    if (name.toLowerCase() === 'wsl_utf8') continue;
    environment[name] = value;
  }
  environment.WSL_UTF8 = '1';
  return environment;
}

function filterVerifiedWslHostWarnings(buffer, step) {
  const value = decodeUtf8(Buffer.from(buffer || ''), step);
  const kept = [];
  let offset = 0;
  while (offset < value.length) {
    const newline = value.indexOf('\n', offset);
    const end = newline < 0 ? value.length : newline + 1;
    const segment = value.slice(offset, end);
    const withoutNewline = newline < 0 ? segment : segment.slice(0, -1);
    const line = withoutNewline.endsWith('\r') ? withoutNewline.slice(0, -1) : withoutNewline;
    if (!VERIFIED_WSL_HOST_WARNING_LINES.has(line)) kept.push(segment);
    offset = end;
  }
  return Buffer.from(kept.join(''), 'utf8');
}

function filterProofStderr(result, step) {
  return { ...result, stderr: filterVerifiedWslHostWarnings(result.stderr, step) };
}

function flattenMountEntries(value, output = []) {
  if (!value || typeof value !== 'object') return output;
  if (typeof value.target === 'string') output.push({
    target: value.target,
    source: value.source,
    fstype: value.fstype,
  });
  if (Array.isArray(value.children)) value.children.forEach((child) => flattenMountEntries(child, output));
  return output;
}

function assertOnlyExpectedRuntimeMounts(stdout, version, { expectedDataLayers = 1 } = {}) {
  if (![0, 1].includes(expectedDataLayers)) reject('bootstrap_mounts');
  let document;
  try { document = JSON.parse(stdout); } catch { reject('bootstrap_mounts'); }
  if (!Array.isArray(document?.filesystems)) reject('bootstrap_mounts');
  const entries = flattenMountEntries({ children: document.filesystems });
  const release = releaseDirectory(version);
  const root = entries.filter((entry) => entry.target === '/');
  if (root.length !== 1 || root[0].fstype !== 'ext4' || !/^\/dev\/[A-Za-z0-9._-]+$/.test(root[0].source || '')) {
    reject('bootstrap_mounts');
  }
  const expected = {
    data: {
      target: `${release}/rootfs/data`,
      source: `${root[0].source}[/var/lib/applemusic-downloader/wrapper-state]`,
      fstype: 'ext4',
    },
    proc: { target: `${release}/rootfs/proc`, source: 'proc', fstype: 'proc' },
    urandom: { target: `${release}/rootfs/dev/urandom`, source: 'none[/urandom]', fstype: 'devtmpfs' },
  };
  const counts = { data: 0, proc: 0, urandom: 0 };
  for (const entry of entries) {
    if (entry.target !== release && !entry.target.startsWith(`${release}/`)) continue;
    const kind = Object.keys(expected).find((name) => entry.target === expected[name].target);
    if (!kind || entry.source !== expected[kind].source || entry.fstype !== expected[kind].fstype) {
      reject('bootstrap_mounts');
    }
    counts[kind] += 1;
  }
  if (counts.data !== expectedDataLayers || counts.proc > MAX_LEGACY_MOUNT_LAYERS
      || counts.urandom > MAX_LEGACY_MOUNT_LAYERS) reject('bootstrap_mounts');
  return counts;
}

function parseArchiveMembers(stdout, version) {
  const members = new Set();
  for (const line of stdout.split('\n')) {
    if (!line) continue;
    const member = normalizeArchiveMember(line.replace(/\r$/, ''), version);
    if (member === null || member === undefined) continue;
    if (members.has(member)) reject('bootstrap_archive_list');
    members.add(member);
  }
  if (!members.has('') || !members.has('amdl-appliance') || members.size < 5) reject('bootstrap_archive_list');
  return members;
}

function parseCurrentMembers(buffer) {
  const parts = buffer.toString('utf8').split('\0');
  if (parts.at(-1) !== '') reject('bootstrap_current_list');
  parts.pop();
  const members = new Set();
  for (const part of parts) {
    if (part.includes('\uFFFD')) reject('bootstrap_current_list');
    const member = normalizeCurrentMember(part);
    if (member === null) continue;
    if (members.has(member)) reject('bootstrap_current_list');
    members.add(member);
  }
  if (!members.has('') || !members.has('amdl-appliance') || members.size < 5) reject('bootstrap_current_list');
  return members;
}

function assertEqualMemberSets(archive, current) {
  if (archive.size !== current.size) reject('bootstrap_runtime_content');
  for (const member of archive) if (!current.has(member)) reject('bootstrap_runtime_content');
}

function validateCompareOutput(result, archiveMembers, version) {
  if (![0, 1].includes(result.exitCode)) reject('bootstrap_runtime_content');
  const combined = `${decodeUtf8(result.stdout, 'bootstrap_runtime_content')}\n${decodeUtf8(result.stderr, 'bootstrap_runtime_content')}`;
  const lines = combined.split(/\r?\n/).filter(Boolean);
  let differences = 0;
  for (const line of lines) {
    if (line === 'tar: Exiting with failure status due to previous errors') continue;
    const match = /^(.*): (Mode differs|Mod time differs)$/.exec(line);
    if (!match) reject('bootstrap_runtime_content');
    let member;
    try {
      const raw = match[1].replace(/^\.\//, '');
      member = raw.startsWith('opt/') ? normalizeArchiveMember(raw, version) : normalizeCurrentMember(raw);
    } catch { reject('bootstrap_runtime_content'); }
    if (member === null || !archiveMembers.has(member)) reject('bootstrap_runtime_content');
    differences += 1;
  }
  if ((result.exitCode === 0 && differences !== 0) || (result.exitCode === 1 && differences === 0)) reject('bootstrap_runtime_content');
  return differences;
}

async function proveInstalledRuntimeContent(manager, { manifest, artifactWslPath, allowedMetadata = null }) {
  await assertLegacyReleaseTree(manager, manifest, { allowedMetadata });
  const commands = runtimeProofCommands({ version: manifest.version, artifactWslPath });
  const mounts = filterProofStderr(
    await captureWsl(manager, commands.mounts, { maxStdoutBytes: 2 * 1024 * 1024 }),
    'bootstrap_mounts',
  );
  if (mounts.exitCode !== 0 || mounts.stderr.length !== 0) reject('bootstrap_mounts');
  assertOnlyExpectedRuntimeMounts(decodeUtf8(mounts.stdout, 'bootstrap_mounts'), manifest.version);
  const archiveResult = filterProofStderr(
    await captureWsl(manager, commands.archiveList, { timeoutMs: 180_000 }),
    'bootstrap_archive_list',
  );
  if (archiveResult.exitCode !== 0 || archiveResult.stderr.length !== 0) reject('bootstrap_archive_list');
  const archiveMembers = parseArchiveMembers(decodeUtf8(archiveResult.stdout, 'bootstrap_archive_list'), manifest.version);
  const currentResult = filterProofStderr(
    await captureWsl(manager, commands.currentList, { timeoutMs: 180_000 }),
    'bootstrap_current_list',
  );
  if (currentResult.exitCode !== 0 || currentResult.stderr.length !== 0) reject('bootstrap_current_list');
  const currentMembers = parseCurrentMembers(currentResult.stdout);
  assertEqualMemberSets(archiveMembers, currentMembers);
  const comparison = filterProofStderr(await captureWsl(manager, commands.compare, {
    timeoutMs: 300_000,
    maxStdoutBytes: MAX_COMPARE_BYTES,
    maxStderrBytes: MAX_COMPARE_BYTES,
  }), 'bootstrap_runtime_content');
  return {
    memberCount: archiveMembers.size,
    metadataDifferenceCount: validateCompareOutput(comparison, archiveMembers, manifest.version),
  };
}

function metadataDocument(version, runtimeSha256, transactionId) {
  if (!VERSION_PATTERN.test(version || '') || !SHA256_PATTERN.test(runtimeSha256 || '') || !SHA256_PATTERN.test(transactionId || '')) {
    reject('bootstrap_metadata');
  }
  return { schemaVersion: 1, version, runtimeSha256, transactionId };
}

function metadataBytes(metadata) {
  return Buffer.from(`${JSON.stringify(metadata)}\n`, 'utf8');
}

function metadataMatchesExact(stdout, expected) {
  const expectedBytes = metadataBytes(expected);
  const actual = Buffer.from(stdout, 'utf8');
  const matches = actual.equals(expectedBytes);
  expectedBytes.fill(0);
  return matches;
}

async function readPublishedMetadata(manager, metadata) {
  await inspectPublishedMetadata(manager, metadata, releaseMetadataPath(metadata.version));
  return metadata;
}

async function inspectPublishedMetadata(manager, metadata, target) {
  const expectedBytes = metadataBytes(metadata);
  const expectedDigest = crypto.createHash('sha256').update(expectedBytes).digest('hex');
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '-f', target,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-L', target,
    ], { timeoutMs: 15_000 });
    const stat = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/stat', '-c', '%d:%i:%h:%s:%a:%U', '--', target,
    ], { timeoutMs: 15_000 });
    const digest = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/sha256sum', '--', target,
    ], { timeoutMs: 15_000 });
    const match = /^(\d+):(\d+):(\d+):(\d+):(\d+):([^:\r\n]+)$/.exec(stat.stdout.trim());
    if (!match || match[4] !== String(expectedBytes.length) || match[5] !== '644' || match[6] !== 'root'
        || Number(match[3]) < 1 || !digest.stdout.startsWith(`${expectedDigest} `)) reject('bootstrap_metadata');
    return {
      device: match[1],
      inode: match[2],
      links: match[3],
      size: match[4],
      mode: match[5],
      owner: match[6],
      digest: expectedDigest,
    };
  } finally {
    expectedBytes.fill(0);
  }
}

async function assertWslEntryAbsent(manager, filename) {
  await manager.runWsl([
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-e', filename,
  ], { timeoutMs: 15_000 });
  await manager.runWsl([
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-L', filename,
  ], { timeoutMs: 15_000 });
}

async function restoreQuarantinedMetadata(manager, target, quarantine) {
  await assertWslEntryAbsent(manager, target);
  await manager.runWsl([
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
    '/usr/bin/ln', '--no-dereference', '--', quarantine, target,
  ], { timeoutMs: 15_000 });
  const quarantineIdentity = await manager.runWsl([
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
    '/usr/bin/stat', '-c', '%d:%i', '--', quarantine,
  ], { timeoutMs: 15_000 });
  const restoredIdentity = await manager.runWsl([
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
    '/usr/bin/stat', '-c', '%d:%i', '--', target,
  ], { timeoutMs: 15_000 });
  if (!/^\d+:\d+\s*$/.test(quarantineIdentity.stdout)
      || quarantineIdentity.stdout.trim() !== restoredIdentity.stdout.trim()) {
    throw new Error('restored metadata inode mismatch');
  }
  await manager.runWsl([
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/unlink', '--', quarantine,
  ], { timeoutMs: 15_000 });
  await assertWslEntryAbsent(manager, quarantine);
}

async function removePublishedReleaseIdentity(manager, metadata) {
  const expected = metadataDocument(metadata.version, metadata.runtimeSha256, metadata.transactionId);
  const target = releaseMetadataPath(metadata.version);
  const quarantine = `${releaseDirectory(metadata.version)}/.${RELEASE_METADATA}.rollback-${crypto.randomBytes(16).toString('hex')}.quarantine`;
  let quarantined = false;
  try {
    try {
      await assertMetadataAbsent(manager, metadata.version);
      return;
    } catch { /* An existing entry must match exact bytes before unlink. */ }
    await assertWslEntryAbsent(manager, quarantine);
    const before = await inspectPublishedMetadata(manager, expected, target);
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/mv', '--', target, quarantine,
    ], { timeoutMs: 15_000 });
    quarantined = true;
    await assertMetadataAbsent(manager, metadata.version);
    const isolated = await inspectPublishedMetadata(manager, expected, quarantine);
    if (before.device !== isolated.device || before.inode !== isolated.inode) reject('bootstrap_metadata');
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/unlink', '--', quarantine,
    ], { timeoutMs: 15_000 });
    quarantined = false;
    await assertWslEntryAbsent(manager, quarantine);
    await assertMetadataAbsent(manager, metadata.version);
  } catch (error) {
    if (quarantined) {
      try {
        await restoreQuarantinedMetadata(manager, target, quarantine);
        quarantined = false;
      } catch {
        throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_metadata_rollback');
      }
    }
    throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_metadata_rollback');
  }
}

async function publishReleaseIdentityNoReplace(manager, metadata, { allowExistingExact = false } = {}) {
  const expected = metadataDocument(metadata.version, metadata.runtimeSha256, metadata.transactionId);
  if (allowExistingExact) {
    try { return await readPublishedMetadata(manager, expected); } catch { /* Publish only when truly absent below. */ }
  }
  const release = releaseDirectory(expected.version);
  const target = releaseMetadataPath(expected.version);
  const temporary = `${release}/.${RELEASE_METADATA}.${crypto.randomBytes(16).toString('hex')}.tmp`;
  const payload = metadataBytes(expected);
  let published = false;
  let primaryError = null;
  try {
    await assertMetadataAbsent(manager, expected.version);
    await manager.runner.input(await manager.resolveWslExecutable(), [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/dd', `of=${temporary}`, 'bs=4096', 'conv=fsync,excl', 'status=none',
    ], payload, { timeoutMs: 20_000, maxInputBytes: 4096, env: manager.processEnvironment });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/chmod', '0644', temporary,
    ], { timeoutMs: 15_000 });
    await assertMetadataAbsent(manager, expected.version);
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/ln', '--', temporary, target,
    ], { timeoutMs: 15_000 });
    published = true;
    await readPublishedMetadata(manager, expected);
  } catch (error) {
    primaryError = error;
  } finally {
    payload.fill(0);
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/rm', '-f', '--', temporary,
    ], { timeoutMs: 15_000 }).catch(() => {
      if (!primaryError) primaryError = new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_metadata_cleanup');
    });
  }
  if (primaryError) {
    if (published) {
      try { await removePublishedReleaseIdentity(manager, expected); } catch {
        throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_metadata_rollback');
      }
    }
    if (primaryError instanceof WslSmokeError) throw primaryError;
    reject('bootstrap_metadata');
  }
  return expected;
}

function immutableBridgePaths(manager, transactionId) {
  if (!SHA256_PATTERN.test(transactionId || '')) reject('bootstrap_transaction');
  const bridgeDirectory = path.win32.join(manager.appDataDir, 'bootstrap-bridge');
  return {
    snapshotWslPath: `/var/lib/applemusic-downloader/.bootstrap-${transactionId}.bundle`,
    bridgeDirectory,
    placeholderWindowsPath: path.win32.join(bridgeDirectory, `${transactionId}.bundle`),
  };
}

function immutableBridgeCommands({ snapshotWslPath, placeholderWslPath }) {
  if (!/^\/var\/lib\/applemusic-downloader\/\.bootstrap-[a-f0-9]{64}\.bundle$/.test(snapshotWslPath || '')
      || !/^\/mnt\/[a-z]\//i.test(placeholderWslPath || '')
      || path.posix.normalize(placeholderWslPath) !== placeholderWslPath) reject('bootstrap_bridge');
  return {
    snapshotWrite: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/dd', `of=${snapshotWslPath}`, 'bs=1048576', 'conv=fsync,excl', 'status=none',
    ],
    snapshotMode: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/chmod', '0600', snapshotWslPath,
    ],
    bind: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/mount', '--bind', snapshotWslPath, placeholderWslPath,
    ],
    readOnly: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/mount', '-o', 'remount,bind,ro', snapshotWslPath, placeholderWslPath,
    ],
    inspect: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/findmnt', '--json', '--mountpoint', placeholderWslPath,
      '--output', 'TARGET,SOURCE,FSTYPE,OPTIONS',
    ],
    unmount: [
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/umount', '--', placeholderWslPath,
    ],
  };
}

function parseBridgeMount(stdout, { snapshotWslPath, placeholderWslPath }) {
  let document;
  try { document = JSON.parse(stdout); } catch { reject('bootstrap_bridge_mount'); }
  const rows = Array.isArray(document?.filesystems) ? document.filesystems : [];
  if (rows.length !== 1) reject('bootstrap_bridge_mount');
  const row = rows[0];
  const options = String(row.options || '').split(',');
  const source = String(row.source || '');
  if (row.target !== placeholderWslPath || String(row.fstype || '').toLowerCase() !== 'ext4'
      || !options.includes('ro')
      || !(source === snapshotWslPath || source.endsWith(`[${snapshotWslPath}]`))) {
    reject('bootstrap_bridge_mount');
  }
  return true;
}

async function exactBridgeMountPresent(manager, bridge) {
  const commands = immutableBridgeCommands(bridge);
  try {
    const result = await manager.runWsl(commands.inspect, { timeoutMs: 15_000 });
    parseBridgeMount(result.stdout, bridge);
    return true;
  } catch (error) {
    if (Number(error?.exitCode) === 1) return false;
    if (error instanceof WslSmokeError) throw error;
    throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_bridge_cleanup');
  }
}

async function wslEntryPresent(manager, filename) {
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '-e', filename,
    ], { timeoutMs: 15_000 });
    return true;
  } catch (error) {
    if (Number(error?.exitCode) !== 1) throw error;
  }
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '-L', filename,
    ], { timeoutMs: 15_000 });
    throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_bridge_cleanup');
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    if (Number(error?.exitCode) === 1) return false;
    throw error;
  }
}

async function verifyWslFileIdentity(manager, filename, artifact, step) {
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '-f', filename,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-L', filename,
    ], { timeoutMs: 15_000 });
    const size = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/stat', '-c', '%s', '--', filename,
    ], { timeoutMs: 30_000 });
    const digest = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/sha256sum', '--', filename,
    ], { timeoutMs: 180_000 });
    if (Number(size.stdout.trim()) !== artifact.size || digest.stdout.slice(0, 64) !== artifact.sha256) reject(step);
  } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject(step);
  }
}

async function cleanupImmutableBundleBridge(manager, bridge) {
  const commands = immutableBridgeCommands(bridge);
  let cleanupError = null;
  try {
    if (await exactBridgeMountPresent(manager, bridge)) {
      await manager.runWsl(commands.unmount, { timeoutMs: 30_000 });
      if (await exactBridgeMountPresent(manager, bridge)) throw new Error('bridge mount remains active');
    }
  } catch (error) {
    cleanupError = error;
  }
  try {
    if (await wslEntryPresent(manager, bridge.snapshotWslPath)) {
      await manager.runWsl([
        '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
        '/usr/bin/test', '!', '-L', bridge.snapshotWslPath,
      ], { timeoutMs: 15_000 });
      await manager.runWsl([
        '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
        '/usr/bin/unlink', '--', bridge.snapshotWslPath,
      ], { timeoutMs: 30_000 });
      if (await wslEntryPresent(manager, bridge.snapshotWslPath)) throw new Error('snapshot remains present');
    }
  } catch (error) {
    if (!cleanupError) cleanupError = error;
  }
  try { await fsp.unlink(bridge.placeholderWindowsPath); } catch (error) {
    if (error.code !== 'ENOENT' && !cleanupError) cleanupError = error;
  }
  try { await fsp.rmdir(bridge.bridgeDirectory); } catch (error) {
    if (!['ENOENT', 'ENOTEMPTY'].includes(error.code) && !cleanupError) cleanupError = error;
  }
  if (cleanupError) throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_bridge_cleanup');
}

async function prepareImmutableBundleBridge(manager, {
  artifact,
  transactionId,
}, {
  fileReader = (filename) => fsp.readFile(filename),
} = {}) {
  const paths = immutableBridgePaths(manager, transactionId);
  let placeholderWslPath = '';
  let source;
  try {
    if (artifact.size > MAX_RUNTIME_BYTES) reject('bootstrap_target_runtime');
    source = await fileReader(artifact.windowsPath);
    if (!Buffer.isBuffer(source)) source = Buffer.from(source);
    const digest = crypto.createHash('sha256').update(source).digest('hex');
    if (source.length !== artifact.size || digest !== artifact.sha256) reject('bootstrap_target_runtime');

    await fsp.mkdir(paths.bridgeDirectory, { recursive: true });
    const bridgeInfo = await fsp.lstat(paths.bridgeDirectory);
    const bridgeIdentity = await manager.canonicalizeWindowsPathWithHelper(paths.bridgeDirectory);
    if (!bridgeInfo.isDirectory() || bridgeInfo.isSymbolicLink()
        || !sameWindowsPath(bridgeIdentity.canonicalPath, paths.bridgeDirectory)) reject('bootstrap_bridge');
    const placeholder = await fsp.open(paths.placeholderWindowsPath, 'wx', 0o600);
    try { await placeholder.sync(); } finally { await placeholder.close(); }
    const converted = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--exec', '/usr/bin/wslpath', '-a', '-u', paths.placeholderWindowsPath,
    ], { timeoutMs: 15_000 });
    placeholderWslPath = converted.stdout.trim();
    const bridge = { ...paths, placeholderWslPath };
    const commands = immutableBridgeCommands(bridge);

    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-e', paths.snapshotWslPath,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-L', paths.snapshotWslPath,
    ], { timeoutMs: 15_000 });
    await manager.runner.input(await manager.resolveWslExecutable(), commands.snapshotWrite, source, {
      timeoutMs: 360_000,
      maxInputBytes: artifact.size,
      env: manager.processEnvironment,
    });
    await manager.runWsl(commands.snapshotMode, { timeoutMs: 15_000 });
    await verifyWslFileIdentity(manager, paths.snapshotWslPath, artifact, 'bootstrap_snapshot');
    await manager.runWsl(commands.bind, { timeoutMs: 30_000 });
    await manager.runWsl(commands.readOnly, { timeoutMs: 30_000 });
    const mountedIdentity = await manager.runWsl(commands.inspect, { timeoutMs: 15_000 });
    parseBridgeMount(mountedIdentity.stdout, bridge);
    await verifyWslFileIdentity(manager, placeholderWslPath, artifact, 'bootstrap_bridge_mount');
    return bridge;
  } catch (error) {
    if (placeholderWslPath) {
      const bridge = { ...paths, placeholderWslPath };
      try { await cleanupImmutableBundleBridge(manager, bridge); } catch {
        throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_bridge_cleanup');
      }
    } else {
      await fsp.unlink(paths.placeholderWindowsPath).catch(() => {});
      await fsp.rmdir(paths.bridgeDirectory).catch(() => {});
    }
    if (error instanceof WslSmokeError) throw error;
    throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_bridge');
  } finally {
    source?.fill?.(0);
  }
}

async function closeManagerProxy(manager) {
  if (!manager) return;
  if (typeof manager.discardLocalProxy === 'function') {
    await manager.discardLocalProxy({ clearPersistedEndpoint: true });
    return;
  }
  if (!manager.proxy) return;
  await manager.proxy.stop();
  manager.proxy = null;
  manager.proxyLocalPort = null;
  manager.proxyBackendPort = null;
  manager.serviceBaseUrl = '';
}

function legacyLeakMountSpecs(version) {
  const release = releaseDirectory(version);
  return [
    {
      target: `${release}/rootfs/dev/urandom`,
      source: 'none[/urandom]',
      fstype: 'devtmpfs',
    },
    {
      target: `${release}/rootfs/proc`,
      source: 'proc',
      fstype: 'proc',
    },
  ];
}

async function inspectLegacyLeakTop(manager, spec) {
  let result;
  try {
    result = await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
      '/usr/bin/findmnt', '--json', '--mountpoint', spec.target,
      '--output', 'TARGET,SOURCE,FSTYPE',
    ], { timeoutMs: 15_000 });
  } catch (error) {
    if (Number(error?.exitCode ?? error?.code) === 1) return false;
    fail('bootstrap_quiesce_mounts');
  }
  result = filterProofStderr(result, 'bootstrap_quiesce_mounts');
  if (result.stderr.length !== 0) fail('bootstrap_quiesce_mounts');
  let document;
  try { document = JSON.parse(result.stdout); } catch { reject('bootstrap_mounts'); }
  const entries = flattenMountEntries({ children: document?.filesystems || [] });
  if (entries.length !== 1 || entries[0].target !== spec.target
      || entries[0].source !== spec.source || entries[0].fstype !== spec.fstype) {
    reject('bootstrap_mounts');
  }
  return true;
}

async function inspectRuntimeMounts(manager, version, { expectedDataLayers = 0 } = {}) {
  let result;
  try {
    result = await manager.runWsl(runtimeMountInspectionCommand(), { timeoutMs: 30_000 });
  } catch {
    fail('bootstrap_quiesce_mounts');
  }
  result = filterProofStderr(result, 'bootstrap_quiesce_mounts');
  if (result.stderr.length !== 0) fail('bootstrap_quiesce_mounts');
  return assertOnlyExpectedRuntimeMounts(result.stdout, version, { expectedDataLayers });
}

async function cleanupLegacyWrapperMounts(manager, version) {
  await inspectRuntimeMounts(manager, version, { expectedDataLayers: 0 });
  const detached = { proc: 0, urandom: 0 };
  for (const spec of legacyLeakMountSpecs(version)) {
    const kind = spec.target.endsWith('/rootfs/proc') ? 'proc' : 'urandom';
    for (; detached[kind] < MAX_LEGACY_MOUNT_LAYERS; detached[kind] += 1) {
      if (!await inspectLegacyLeakTop(manager, spec)) break;
      try {
        const result = filterProofStderr(await manager.runWsl([
          '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
          '/usr/bin/umount', '--', spec.target,
        ], { timeoutMs: 30_000 }), 'bootstrap_quiesce_mounts');
        if (result.stderr.length !== 0) fail('bootstrap_quiesce_mounts');
      } catch {
        fail('bootstrap_quiesce_mounts');
      }
    }
    if (await inspectLegacyLeakTop(manager, spec)) fail('bootstrap_quiesce_mounts');
  }
  const remaining = await inspectRuntimeMounts(manager, version, { expectedDataLayers: 0 });
  if (remaining.proc !== 0 || remaining.urandom !== 0 || remaining.data !== 0) {
    fail('bootstrap_quiesce_mounts');
  }
  return detached;
}

async function quiesceLegacyAppliance(manager, version, {
  mountCleaner = cleanupLegacyWrapperMounts,
  ownershipVerifier = async () => {},
  beforeTerminate = async () => {},
} = {}) {
  let active;
  try { active = await manager.activeJobs(); } catch { reject('bootstrap_active_jobs'); }
  if (active.length !== 0) reject('bootstrap_active_jobs');
  await ownershipVerifier('before_stop');
  const stopped = JSON.parse((await manager.runControl('stop', { timeoutMs: 60_000 })).stdout);
  const stoppedStatus = stopped?.status || stopped;
  if (!applianceIdentityMatches(stoppedStatus, EXPECTED_SERVICE, EXPECTED_RUNTIME, version)
      || stoppedStatus.state !== 'stopped' || stoppedStatus.credentialsConfigured !== false) {
    reject('bootstrap_quiesce');
  }
  await closeManagerProxy(manager);
  await beforeTerminate();
  await ownershipVerifier('before_terminate');
  await manager.runWsl(['--terminate', DISTRO_NAME], { timeoutMs: 60_000 });
  manager.keeperProcess = null;
  try { await manager.ensureKeeper(); } catch { fail('bootstrap_keeper'); }
  await manager.waitForSupervisorReady({ expectedVersion: version, timeoutMs: 180_000 });
  let status = await manager.status();
  if (['running', 'starting'].includes(status.state)) {
    const result = JSON.parse((await manager.runControl('stop', { timeoutMs: 60_000 })).stdout);
    status = result?.status || result;
  }
  if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, version)
      || status.state !== 'stopped' || status.credentialsConfigured !== false) reject('bootstrap_quiesce');
  await ownershipVerifier('before_mount_cleanup');
  await mountCleaner(manager, version);
  return status;
}

async function restoreApplianceBackend(manager, version) {
  try { await manager.ensureKeeper(); } catch { fail('bootstrap_lifecycle_restore'); }
  const result = JSON.parse((await manager.runControl('start', { timeoutMs: 60_000 })).stdout);
  const status = result?.status || result;
  if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, version)
      || !['running', 'starting'].includes(status.state) || status.credentialsConfigured !== false) {
    fail('bootstrap_lifecycle_restore');
  }
  await manager.prepareLocalEndpoint({ expectedVersion: version });
  await manager.waitForReady({ timeoutMs: 180_000, expectedVersion: version });
  return status;
}

function legacyUpdateDocument(version, bridge, sha256) {
  if (!VERSION_PATTERN.test(version || '') || !SHA256_PATTERN.test(sha256 || '')) reject('bootstrap_update_document');
  immutableBridgeCommands(bridge);
  return {
    schemaVersion: 1,
    mode: 'repair',
    version,
    bundlePath: bridge.placeholderWslPath,
    sha256,
  };
}

async function executeLegacyUpdate(manager, document) {
  const payload = Buffer.from(`${JSON.stringify(document)}\n`, 'utf8');
  try {
    return await manager.runControl('update-stdin', {
      input: payload,
      maxInputBytes: 4096,
      timeoutMs: 360_000,
    });
  } finally {
    payload.fill(0);
  }
}

async function assertUpdateTransactionAbsent(manager) {
  try {
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-e', UPDATE_TRANSACTION,
    ], { timeoutMs: 15_000 });
    await manager.runWsl([
      '--distribution', DISTRO_NAME, '--user', 'root', '--exec', '/usr/bin/test', '!', '-L', UPDATE_TRANSACTION,
    ], { timeoutMs: 15_000 });
  } catch { fail('bootstrap_update_recovery'); }
}

async function waitForTargetSupervisor(manager, version, {
  timeoutMs = 180_000,
  intervalMs = 500,
} = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() <= deadline) {
    try {
      const status = await manager.status();
      if (applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, version)
          && status.updateInProgress === false
          && !status.pendingTransactionId
          && status.credentialsConfigured === false
          && status.configured === true
          && ['running', 'starting', 'stopped'].includes(status.state)) return status;
    } catch { /* A supervisor restart is expected after the legacy update commits. */ }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
  fail('bootstrap_target_supervisor');
}

async function proveTargetRuntime(manager, {
  manifest,
  snapshotWslPath,
  metadata = null,
  contentProver = proveInstalledRuntimeContent,
} = {}) {
  await waitForTargetSupervisor(manager, manifest.version);
  await assertUpdateTransactionAbsent(manager);
  const proof = await contentProver(manager, {
    manifest,
    artifactWslPath: snapshotWslPath,
    allowedMetadata: metadata,
  });
  let status = await manager.status();
  if (status.state === 'stopped') {
    const started = JSON.parse((await manager.runControl('start', { timeoutMs: 60_000 })).stdout);
    status = started?.status || started;
  }
  if (!applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, manifest.version)
      || status.updateInProgress !== false || status.credentialsConfigured !== false) fail('bootstrap_target_supervisor');
  await manager.prepareLocalEndpoint({ expectedVersion: manifest.version });
  await manager.waitForReady({ timeoutMs: 180_000, expectedVersion: manifest.version });
  const active = await manager.activeJobs();
  if (active.length !== 0) reject('bootstrap_active_jobs');
  return { status, proof };
}

function createBootstrapMarker({ legacyManifest, targetManifest, targetManifestSha256, transactionId, bridge }) {
  return {
    schemaVersion: 1,
    phase: 'update_sent',
    version: targetManifest.version,
    legacyRuntimeSha256: legacyManifest.runtime.sha256,
    targetRuntimeSha256: targetManifest.runtime.sha256,
    targetManifestSha256,
    transactionId,
    snapshotWslPath: bridge.snapshotWslPath,
    placeholderWindowsPath: bridge.placeholderWindowsPath,
    placeholderWslPath: bridge.placeholderWslPath,
  };
}

function validateBootstrapMarker(value, expected) {
  const fields = [
    'schemaVersion', 'phase', 'version', 'legacyRuntimeSha256', 'targetRuntimeSha256',
    'targetManifestSha256', 'transactionId', 'snapshotWslPath',
    'placeholderWindowsPath', 'placeholderWslPath',
  ];
  if (!value || typeof value !== 'object' || Array.isArray(value)
      || Object.keys(value).sort().join(',') !== fields.sort().join(',')
      || value.schemaVersion !== 1 || !['update_sent', 'target_installed'].includes(value.phase)
      || value.version !== expected.targetManifest.version
      || value.legacyRuntimeSha256 !== expected.legacyManifest.runtime.sha256
      || value.targetRuntimeSha256 !== expected.targetManifest.runtime.sha256
      || value.targetManifestSha256 !== expected.targetManifestSha256
      || !SHA256_PATTERN.test(value.transactionId || '')) reject('bootstrap_recovery_marker');
  const paths = immutableBridgePaths({ appDataDir: expected.appDataDir }, value.transactionId);
  if (value.snapshotWslPath !== paths.snapshotWslPath
      || !sameWindowsPath(value.placeholderWindowsPath, paths.placeholderWindowsPath)
      || !/^\/mnt\/[a-z]\//i.test(value.placeholderWslPath || '')) reject('bootstrap_recovery_marker');
  return value;
}

function publishedStatusMatches(status, metadata) {
  return applianceIdentityMatches(status, EXPECTED_SERVICE, EXPECTED_RUNTIME, metadata.version)
    && status.runtimeSha256 === metadata.runtimeSha256
    && status.runtimeTransactionId === metadata.transactionId
    && status.credentialsConfigured === false
    && status.updateInProgress === false
    && !status.pendingTransactionId
    && status.configured === true
    && ['running', 'starting'].includes(status.state);
}

async function verifyPublishedTargetStatus(manager, metadata) {
  const status = await manager.status();
  if (!publishedStatusMatches(status, metadata)) reject('bootstrap_status_identity');
  const active = await manager.activeJobs();
  if (active.length !== 0) reject('bootstrap_active_jobs');
  return status;
}

async function bootstrapLocalPocIdentityLocked(manager, migrationLock, {
  credentialsPresent = false,
  legacyRuntimePath,
  targetManifestPath,
  targetRuntimePath,
  artifactVerifier = verifyArtifactFile,
  manifestReader,
  manifestLoader = loadTargetManifest,
  artifactResolver = windowsFileToVerifiedWslPath,
  candidateValidator = validateLegacyBootstrapCandidate,
  legacyContentProver = proveInstalledRuntimeContent,
  quiescer = quiesceLegacyAppliance,
  legacyRestorer = restoreApplianceBackend,
  bridgePreparer = prepareImmutableBundleBridge,
  snapshotVerifier = verifyWslFileIdentity,
  updateExecutor = executeLegacyUpdate,
  targetVerifier = proveTargetRuntime,
  bridgeCleaner = cleanupImmutableBundleBridge,
  metadataPublisher = publishReleaseIdentityNoReplace,
  metadataRemover = removePublishedReleaseIdentity,
  publishedVerifier = verifyPublishedTargetStatus,
  adopter = adoptLocalPocWhileLocked,
} = {}) {
  try { await manager.ensureKeeper(); } catch { fail('bootstrap_keeper'); }
  const initialState = await manager.readState();
  const recoveryValue = initialState?.[BOOTSTRAP_STATE_FIELD];
  let legacyManifest;
  try { legacyManifest = assertLocalPocManifest(validateApplianceManifest(initialState.manifest), { legacy: true }); } catch (error) {
    if (error instanceof WslSmokeError) throw error;
    reject('bootstrap_legacy_manifest');
  }
  const target = await manifestLoader(manager, targetManifestPath, legacyManifest, { manifestReader });
  const legacyArtifact = await artifactResolver(manager, legacyRuntimePath, legacyManifest.runtime, {
    artifactVerifier,
    step: 'bootstrap_legacy_artifact',
  });
  const targetArtifact = await artifactResolver(manager, targetRuntimePath, target.manifest.runtime, {
    artifactVerifier,
    step: 'bootstrap_target_runtime',
  });

  let marker;
  let bridge;
  let updateAttempted = false;
  let updateResponseLost = false;
  if (valueIsAbsent(recoveryValue)) {
    const candidate = await candidateValidator(manager, { credentialsPresent });
    const frozenLegacy = canonicalManifestDigest(candidate.manifest);
    await legacyContentProver(manager, { manifest: candidate.manifest, artifactWslPath: legacyArtifact.wslPath });
    const finalCandidate = await candidateValidator(manager, { credentialsPresent });
    if (canonicalManifestDigest(finalCandidate.manifest) !== frozenLegacy) reject('bootstrap_candidate_changed');
    try {
      await quiescer(manager, candidate.manifest.version);
    } catch (error) {
      try { await legacyRestorer(manager, candidate.manifest.version); } catch {
        throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_lifecycle_restore');
      }
      throw error;
    }
    const transactionId = crypto.randomBytes(32).toString('hex');
    try {
      bridge = await bridgePreparer(manager, {
        artifact: targetArtifact,
        transactionId,
      });
      marker = createBootstrapMarker({
        legacyManifest: candidate.manifest,
        targetManifest: target.manifest,
        targetManifestSha256: target.manifestSha256,
        transactionId,
        bridge,
      });
      await manager.writeState({ [BOOTSTRAP_STATE_FIELD]: marker });
    } catch (error) {
      if (bridge) await bridgeCleaner(manager, bridge).catch(() => {});
      try { await legacyRestorer(manager, candidate.manifest.version); } catch {
        throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_lifecycle_restore');
      }
      throw error;
    }
    updateAttempted = true;
    try {
      await updateExecutor(manager, legacyUpdateDocument(target.manifest.version, bridge, target.manifest.runtime.sha256));
    } catch {
      updateResponseLost = true;
      // The committed update intentionally restarts its own supervisor and may lose the response.
    }
  } else {
    marker = validateBootstrapMarker(recoveryValue, {
      legacyManifest,
      targetManifest: target.manifest,
      targetManifestSha256: target.manifestSha256,
      appDataDir: manager.appDataDir,
    });
    bridge = {
      ...immutableBridgePaths(manager, marker.transactionId),
      placeholderWslPath: marker.placeholderWslPath,
    };
    try {
      await snapshotVerifier(manager, marker.snapshotWslPath, target.manifest.runtime, 'bootstrap_snapshot');
    } catch {
      try { await bridgeCleaner(manager, bridge); } catch { fail('bootstrap_bridge_cleanup'); }
      bridge = await bridgePreparer(manager, {
        artifact: targetArtifact,
        transactionId: marker.transactionId,
      });
      if (bridge.snapshotWslPath !== marker.snapshotWslPath
          || !sameWindowsPath(bridge.placeholderWindowsPath, marker.placeholderWindowsPath)
          || bridge.placeholderWslPath !== marker.placeholderWslPath) reject('bootstrap_recovery_marker');
    }
  }

  let targetProof;
  try {
    targetProof = await targetVerifier(manager, {
      manifest: target.manifest,
      snapshotWslPath: marker.snapshotWslPath,
      metadata: metadataDocument(target.manifest.version, target.manifest.runtime.sha256, marker.transactionId),
    });
  } catch {
    // A lost legacy control response is an unknown outcome.  The old
    // supervisor continues its health/commit/rollback transaction after the
    // Windows transport exits, so terminating the distro here can turn a
    // healthy candidate into a prepared-marker rollback.  Keep both the host
    // recovery marker and immutable bridge intact; a later explicit recovery
    // run re-proves the target without replaying the update.
    fail('bootstrap_update_recovery');
  }
  await manager.writeState({ [BOOTSTRAP_STATE_FIELD]: { ...marker, phase: 'target_installed' } });
  try { await bridgeCleaner(manager, bridge); } catch { fail('bootstrap_bridge_cleanup'); }

  const metadata = metadataDocument(target.manifest.version, target.manifest.runtime.sha256, marker.transactionId);
  let metadataPublished = false;
  try {
    await metadataPublisher(manager, metadata, { allowExistingExact: recoveryValue !== undefined && recoveryValue !== null });
    metadataPublished = true;
    const status = await publishedVerifier(manager, metadata);
    await manager.writeState({
      manifest: target.manifest,
      installedVersion: target.manifest.version,
      installedRuntimeSha256: target.manifest.runtime.sha256,
      installedRuntimeTransactionId: marker.transactionId,
      [BOOTSTRAP_STATE_FIELD]: null,
    });
    const adoption = await adopter(manager, migrationLock, { credentialsPresent: false });
    return {
      bootstrapped: true,
      migratedRuntime: true,
      updateAttempted,
      recoveredResponse: updateResponseLost || !updateAttempted,
      identity: safeIdentity(status),
      runtimeSha256: metadata.runtimeSha256,
      runtimeTransactionId: metadata.transactionId,
      contentMembers: Number(targetProof?.proof?.memberCount || 0),
      adoption,
    };
  } catch (error) {
    if (metadataPublished) {
      const state = await manager.readState().catch(() => ({}));
      if (state.installedRuntimeSha256 !== metadata.runtimeSha256) {
        try { await metadataRemover(manager, metadata); } catch {
          throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_metadata_rollback');
        }
      }
    }
    if (error instanceof WslSmokeError) throw error;
    throw new WslSmokeError('local_poc_bootstrap_failed', 'bootstrap_commit');
  }
}

async function bootstrapLocalPocIdentity(manager, options = {}) {
  const migrationLocker = options.migrationLocker
    || manager.localPocMigrationLocker
    || withLocalPocMigrationLock;
  const lockedOptions = { ...options };
  delete lockedOptions.migrationLocker;
  return migrationLocker(manager, (migrationLock) => (
    bootstrapLocalPocIdentityLocked(manager, migrationLock, lockedOptions)
  ));
}

module.exports = {
  BOOTSTRAP_STATE_FIELD,
  bootstrapProofEnvironment,
  bootstrapLocalPocIdentity,
  cleanupImmutableBundleBridge,
  cleanupLegacyWrapperMounts,
  createBootstrapMarker,
  executeLegacyUpdate,
  filterVerifiedWslHostWarnings,
  assertLegacyReleaseTree,
  assertOnlyExpectedRuntimeMounts,
  canonicalManifestDigest,
  captureWsl,
  immutableBridgeCommands,
  immutableBridgePaths,
  legacyUpdateDocument,
  loadTargetManifest,
  metadataDocument,
  metadataMatchesExact,
  normalizeArchiveMember,
  normalizeCurrentMember,
  parseArchiveMembers,
  parseCurrentMembers,
  parseBridgeMount,
  prepareImmutableBundleBridge,
  proveInstalledRuntimeContent,
  proveTargetRuntime,
  publishReleaseIdentityNoReplace,
  releaseDirectory,
  releaseMetadataPath,
  removePublishedReleaseIdentity,
  runtimeProofCommands,
  runtimeMountInspectionCommand,
  quiesceLegacyAppliance,
  restoreApplianceBackend,
  validateBootstrapMarker,
  validateCompareOutput,
  validateLegacyBootstrapCandidate,
  windowsFileToVerifiedWslPath,
  waitForTargetSupervisor,
};

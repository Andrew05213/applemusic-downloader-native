'use strict';

const DISTRO_NAME = 'AppleMusicDownloader';
const VERSION_PATTERN = /^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$/;
const VERIFIED_WSL_HOST_WARNING_LINES = new Set([
  'wsl: A localhost proxy configuration was detected but not mirrored into WSL. WSL in NAT mode does not support localhost proxies.',
  'wsl: 检测到 localhost 代理配置，但未镜像到 WSL。NAT 模式下的 WSL 不支持 localhost 代理。',
]);

class WslMountSmokeError extends Error {
  constructor(step) {
    super('WSL Wrapper mount residue verification failed');
    this.name = 'WslMountSmokeError';
    this.code = 'wrapper_mount_residue';
    this.step = step;
  }
}

function mountInspectionArguments() {
  return [
    '--distribution', DISTRO_NAME, '--user', 'root', '--exec',
    '/usr/bin/findmnt', '--json', '--output', 'TARGET,SOURCE,FSTYPE',
  ];
}

function filterVerifiedHostWarnings(stderr, step = 'wrapper_mounts') {
  const value = Buffer.isBuffer(stderr) ? stderr.toString('utf8') : String(stderr || '');
  if (value.includes('\uFFFD') || value.includes('\0')) throw new WslMountSmokeError(step);
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
  return kept.join('');
}

function flattenMounts(value, output = []) {
  if (!value || typeof value !== 'object') return output;
  if (typeof value.target === 'string') output.push(value.target);
  if (Array.isArray(value.children)) value.children.forEach((child) => flattenMounts(child, output));
  return output;
}

function targetMatches(target, root) {
  return target === root || target.startsWith(`${root}/`);
}

function parseMountResidue(stdout, version, step = 'wrapper_mounts') {
  if (!VERSION_PATTERN.test(version || '')) throw new WslMountSmokeError(step);
  let document;
  try {
    document = JSON.parse(String(stdout || ''));
  } catch {
    throw new WslMountSmokeError(step);
  }
  if (!Array.isArray(document?.filesystems)) throw new WslMountSmokeError(step);
  const targets = flattenMounts({ children: document.filesystems });
  const releaseRoot = `/opt/applemusic-downloader/releases/${version}/rootfs`;
  const currentRoot = '/opt/applemusic-downloader/current/rootfs';
  const procRoots = [`${releaseRoot}/proc`, `${currentRoot}/proc`];
  const urandomRoots = [`${releaseRoot}/dev/urandom`, `${currentRoot}/dev/urandom`];
  const procLayers = targets.filter((target) => procRoots.some((root) => targetMatches(target, root))).length;
  const urandomLayers = targets.filter((target) => urandomRoots.some((root) => targetMatches(target, root))).length;
  return { procLayers, urandomLayers, clean: procLayers === 0 && urandomLayers === 0 };
}

async function verifyWrapperMountsClean(manager, version, { step = 'wrapper_mounts' } = {}) {
  if (!manager || typeof manager.runWsl !== 'function') throw new WslMountSmokeError(step);
  let result;
  try {
    result = await manager.runWsl(mountInspectionArguments(), { timeoutMs: 30_000 });
  } catch {
    throw new WslMountSmokeError(step);
  }
  if (filterVerifiedHostWarnings(result?.stderr, step).trim() !== '') throw new WslMountSmokeError(step);
  const residue = parseMountResidue(result?.stdout, version, step);
  if (!residue.clean) throw new WslMountSmokeError(step);
  return residue;
}

module.exports = {
  WslMountSmokeError,
  filterVerifiedHostWarnings,
  mountInspectionArguments,
  parseMountResidue,
  verifyWrapperMountsClean,
};

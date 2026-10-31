'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { validateApplianceManifest } = require('./appliance-manifest');

function manifestCandidates({
  explicitPath,
  expectedVersion = '',
  packaged = false,
  resourcesPath = process.resourcesPath,
  repositoryRoot = path.resolve(__dirname, '..'),
} = {}) {
  const packagedManifest = resourcesPath && path.join(resourcesPath, 'appliance', 'manifest.json');
  // A packaged desktop build must derive its release identity exclusively from
  // the manifest installed with that build. In particular, an inherited
  // AMDL_APPLIANCE_MANIFEST value must never redirect an update or repair to an
  // attacker-selected local appliance archive.
  if (packaged) return [packagedManifest].filter(Boolean);
  const distDir = path.join(repositoryRoot, 'appliance', 'dist');
  let versioned;
  try {
    const names = fs.readdirSync(distDir)
      .filter((name) => /^applemusic-downloader-wsl-manifest-1\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?-amd64\.json$/.test(name))
      .sort();
    const exactName = expectedVersion
      ? `applemusic-downloader-wsl-manifest-${expectedVersion}-amd64.json`
      : '';
    versioned = exactName ? names.find((name) => name === exactName) : names.at(-1);
  } catch { /* The appliance has not been built in this checkout. */ }
  // When a release version is known, the exact versioned artifact is the
  // authoritative local candidate.  A generated manifest can legitimately
  // be left over from an earlier build and must not mask a newer appliance
  // rebuild with the same desktop source tree.  Explicit paths still win
  // when the caller deliberately supplies one (for fixture/CI use).
  const versionedCandidate = versioned && path.join(distDir, versioned);
  const generatedCandidates = [
    path.join(repositoryRoot, 'electron', 'generated', 'appliance-manifest.json'),
    path.join(repositoryRoot, 'appliance', 'dist', 'appliance-manifest.json'),
  ];
  return [
    explicitPath,
    packagedManifest,
    ...(expectedVersion && versionedCandidate ? [versionedCandidate] : []),
    ...generatedCandidates,
    ...(!expectedVersion && versionedCandidate ? [versionedCandidate] : []),
  ].filter(Boolean);
}

function loadApplianceManifest(options = {}) {
  const candidates = manifestCandidates(options);
  const filename = candidates.find((candidate) => fs.existsSync(candidate));
  if (!filename) throw new Error('此桌面构建未包含 WSL Appliance manifest。请重新下载安装包。');
  const document = validateApplianceManifest(JSON.parse(fs.readFileSync(filename, 'utf8')));
  if (options.expectedVersion && document.version !== options.expectedVersion) {
    throw new Error('WSL Appliance manifest version does not match this desktop build.');
  }
  if (options.expectedApplianceBuildId
      && document.applianceBuildId !== options.expectedApplianceBuildId) {
    throw new Error('WSL Appliance manifest build identity does not match this desktop build.');
  }

  for (const artifactName of ['rootfs', 'runtime']) {
    const artifact = document[artifactName];
    const bundled = options.resourcesPath && path.join(options.resourcesPath, 'appliance', artifact.file);
    if (bundled && fs.existsSync(bundled)) {
      document[artifactName] = { ...artifact, url: pathToFileURL(bundled).toString() };
    }
  }
  return validateApplianceManifest(document);
}

module.exports = { loadApplianceManifest, manifestCandidates };

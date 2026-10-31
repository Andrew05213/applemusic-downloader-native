'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { fileURLToPath } = require('node:url');
const { sha256File, validateApplianceManifest } = require('./appliance-manifest');
const packageManifest = require('./legacy-desktop-package.json');

const root = path.resolve(__dirname, '..');
const generated = path.join(__dirname, 'generated');
const output = path.join(generated, 'appliance-manifest.json');
const offline = process.argv.includes('--offline');
const privateBuild = process.argv.includes('--private');
const releasePolicyPath = path.join(root, 'appliance', 'release-policy.json');

function sha256FileSync(filename) {
  return crypto.createHash('sha256').update(fs.readFileSync(filename)).digest('hex');
}

function enforceManifestVersion(manifest) {
  if (manifest.version !== packageManifest.version) {
    throw new Error(`Appliance manifest version ${manifest.version} does not match desktop version ${packageManifest.version}`);
  }
}

function enforcePublicReleasePolicy(manifest, options = {}) {
  const publicRelease = options.publicRelease ?? process.env.AMDL_PUBLIC_RELEASE === '1';
  const publicManifest = manifest?.channel === 'public';
  if (!publicRelease && !publicManifest) return;
  // A public manifest is itself a public-release request.  It must never be
  // possible to bypass repository policy by invoking the ordinary/private
  // packaging command without AMDL_PUBLIC_RELEASE.
  if (publicManifest && !publicRelease) {
    throw new Error('Public release blocked: explicit public release intent is required');
  }
  if (!publicManifest) {
    throw new Error('Public release blocked: Appliance manifest channel must be public');
  }
  const policyPath = options.releasePolicyPath || releasePolicyPath;
  let policy;
  try {
    policy = JSON.parse(fs.readFileSync(policyPath, 'utf8'));
  } catch (error) {
    throw new Error(`Public release blocked: release policy is missing or invalid (${error.message})`);
  }
  if (policy.schemaVersion !== 1 || policy.publicReleaseAllowed !== true) {
    throw new Error('Public release blocked by appliance/release-policy.json: redistribution approval is not recorded');
  }
  const approved = options.redistributionApproved ?? process.env.AMDL_REDISTRIBUTION_APPROVED === '1';
  if (!approved) {
    throw new Error('Public release blocked: explicit redistribution approval is required');
  }
  if (manifest.inputs.wrapperProvenanceStatus !== 'verified-source-build') {
    throw new Error('Public release blocked: Wrapper provenance is not verified');
  }
  if (manifest.inputs.releasePolicySha256 !== sha256FileSync(policyPath)) {
    throw new Error('Public release blocked: manifest release policy digest does not match the reviewed policy');
  }
  if (manifest.releasePolicy.publicReleaseAllowed !== policy.publicReleaseAllowed
      || JSON.stringify(manifest.releasePolicy.blockers) !== JSON.stringify(policy.blockers)) {
    throw new Error('Public release blocked: manifest release policy does not match the reviewed policy');
  }
}

function clearGeneratedArchives() {
  if (!fs.existsSync(generated)) return;
  for (const name of fs.readdirSync(generated)) {
    if (!name.toLowerCase().endsWith('.tar.gz')) continue;
    fs.rmSync(path.join(generated, name), { force: true });
  }
}

function defaultInput() {
  const dist = path.join(root, 'appliance', 'dist');
  const exact = path.join(dist, `applemusic-downloader-wsl-manifest-${packageManifest.version}-amd64.json`);
  const canonical = path.join(dist, 'appliance-manifest.json');
  if (fs.existsSync(exact)) return exact;
  if (fs.existsSync(canonical)) return canonical;
  if (!fs.existsSync(dist)) return canonical;
  const versioned = fs.readdirSync(dist)
    .filter((name) => /^applemusic-downloader-wsl-manifest-1\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?-amd64\.json$/.test(name))
    .sort()
    .at(-1);
  return versioned ? path.join(dist, versioned) : canonical;
}

async function main(options = {}) {
  const input = options.input || process.env.AMDL_APPLIANCE_MANIFEST || defaultInput();
  if (!fs.existsSync(input)) {
    throw new Error(`Appliance manifest is missing: ${input}. Build the rootfs first or set AMDL_APPLIANCE_MANIFEST.`);
  }
  const source = JSON.parse(fs.readFileSync(input, 'utf8'));
  const manifest = validateApplianceManifest(source);
  const privateIntent = options.privateBuild ?? privateBuild;
  if (privateIntent && manifest.channel === 'public') {
    throw new Error('Private/local packaging entry cannot consume a public Appliance manifest');
  }
  enforcePublicReleasePolicy(manifest, options);
  enforceManifestVersion(manifest);
  fs.mkdirSync(generated, { recursive: true });
  if (!offline) clearGeneratedArchives();
  fs.writeFileSync(output, `${JSON.stringify(manifest, null, 2)}\n`, 'utf8');

  if (offline) {
    const selections = [
      ['rootfs', manifest.rootfs, 'AMDL_OFFLINE_ROOTFS'],
      ['runtime', manifest.runtime, 'AMDL_OFFLINE_RUNTIME'],
    ].map(([label, artifact, environmentName], index) => {
      const parsed = new URL(artifact.url);
      const source = process.env[environmentName]
        || (parsed.protocol === 'file:' ? fileURLToPath(parsed) : '');
      if (!source || !fs.existsSync(source)) {
        throw new Error(`Offline installer requires ${environmentName} or a file: ${label} URL in the manifest`);
      }
      return {
        label,
        artifact,
        source,
        destination: path.join(generated, artifact.file),
        temporary: path.join(generated, `.artifact-${process.pid}-${index}.tmp`),
      };
    });
    try {
      for (const selection of selections) {
        if (await sha256File(selection.source) !== selection.artifact.sha256) {
          throw new Error(`Offline ${selection.label} SHA-256 does not match the manifest`);
        }
        if (fs.statSync(selection.source).size !== selection.artifact.size) {
          throw new Error(`Offline ${selection.label} size does not match the manifest`);
        }
        fs.copyFileSync(selection.source, selection.temporary);
      }
      clearGeneratedArchives();
      for (const selection of selections) fs.renameSync(selection.temporary, selection.destination);
    } finally {
      for (const selection of selections) fs.rmSync(selection.temporary, { force: true });
    }
    const embedded = fs.readdirSync(generated)
      .filter((name) => name.toLowerCase().endsWith('.tar.gz'))
      .sort();
    const expected = selections.map((selection) => path.basename(selection.destination)).sort();
    if (JSON.stringify(embedded) !== JSON.stringify(expected)) {
      throw new Error('Offline installer staging must contain exactly the selected rootfs and runtime archives');
    }
  }

  console.log(`Prepared ${offline ? 'offline' : 'online'} appliance manifest ${manifest.version}`);
}

if (require.main === module) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

module.exports = {
  defaultInput,
  enforceManifestVersion,
  enforcePublicReleasePolicy,
  main,
  sha256FileSync,
};

'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const fixture = require('./create-synthetic-appliance');

function main() {
  const root = path.resolve(__dirname, '..', '..', '..');
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-release-gate-'));
  fixture.main(['--output', directory]);
  const manifest = path.join(directory, fixture.ONLINE_MANIFEST_NAME);
  for (const approval of ['', '1']) {
    const result = spawnSync(process.execPath, [path.join(root, 'electron', 'inject-appliance-manifest.js')], {
      cwd: root,
      shell: false,
      encoding: 'utf8',
      env: {
        ...process.env,
        AMDL_APPLIANCE_MANIFEST: manifest,
        AMDL_PUBLIC_RELEASE: '1',
        AMDL_REDISTRIBUTION_APPROVED: approval,
      },
    });
    assert.notEqual(result.status, 0, `public packaging must fail while repository policy is closed (approval=${approval || 'unset'})`);
    assert.match(`${result.stdout}\n${result.stderr}`, /Public release blocked/);
  }

  const policy = JSON.parse(fs.readFileSync(path.join(root, 'appliance', 'release-policy.json'), 'utf8'));
  assert.equal(policy.publicReleaseAllowed, false, 'appliance public release policy must remain blocked');
  process.stdout.write('Electron and Appliance public release gates are closed\n');
}

if (require.main === module) main();

module.exports = { main };

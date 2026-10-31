'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { installationCommands, installDRM } = require('../../electron/install-drm');
const { runDRMSmoke } = require('../../electron/run-drm-smoke');
const { VERSION, ELECTRON_VERSION, SMOKE_PROFILE_MARKER } = require('../../electron/drm-runtime');

function fixtureRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-drm-tools-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

const npmCLI = path.join(path.dirname(process.execPath), 'node_modules', 'npm', 'bin', 'npm-cli.js');

test('installer passes a single immutable package spec through Node without Windows shell interpretation', (t) => {
  const root = path.join(fixtureRoot(t), '中文 & spaces');
  fs.mkdirSync(root);
  const plan = installationCommands(root, { npmCLI });
  const executions = [];
  installDRM({ root, environment: { npm_execpath: npmCLI, ELECTRON_RUN_AS_NODE: '1' }, run(command, args, options) {
    executions.push({ command, args, options });
    assert.equal(command, process.execPath);
    assert.equal(options.shell, false);
    assert.equal(options.env.ELECTRON_RUN_AS_NODE, undefined);
    if (executions.length === 1) {
      assert.equal(args.at(-2), '--');
      assert.equal(args.at(-1), require('../../electron/legacy-desktop-package.json').devDependencies.electron);
      assert.equal(args[args.indexOf('--prefix') + 1], path.join(root, 'node_modules/.amdl-drm'));
      assert.equal(args.includes('--ignore-scripts'), true);
      fs.mkdirSync(path.join(plan.packageDirectory, 'dist'), { recursive: true });
      fs.writeFileSync(path.join(plan.packageDirectory, 'package.json'), JSON.stringify({ version: VERSION }));
      fs.writeFileSync(path.join(plan.packageDirectory, 'dist', 'version'), VERSION);
      fs.writeFileSync(path.join(plan.packageDirectory, 'dist', 'electron.exe'), 'synthetic executable');
    }
    return { status: 0 };
  } });
  assert.equal(executions.length, 2);
  assert.deepEqual(executions[1].args, [path.join(plan.packageDirectory, 'install.js')]);
});

test('installer stops after npm failure and never executes an unexpected package installer', (t) => {
  const root = fixtureRoot(t);
  let calls = 0;
  assert.throws(() => installDRM({ root, environment: { npm_execpath: npmCLI }, run() {
    calls += 1;
    return { status: 1 };
  } }), /package installation failed/);
  assert.equal(calls, 1);
  assert.equal(fs.existsSync(path.join(root, 'node_modules')), false);
  assert.throws(() => installationCommands(root, { npmCLI: 'npm.cmd & extra' }), /npm run install:drm/);
});

function smokeWithNode(root, script, timeoutMs = 5_000) {
  let profile;
  return {
    get profile() { return profile; },
    result: runDRMSmoke({ repositoryRoot: root, temporaryDirectory: root, timeoutMs,
      environment: { ...process.env, ELECTRON_RUN_AS_NODE: '1', AMDL_DRM_FIXTURE_PROFILE: 'must-not-use' },
      resolveRuntime: () => root,
      spawnChild(_executable, _args, options) {
        profile = options.env.AMDL_DRM_FIXTURE_PROFILE;
        assert.notEqual(profile, 'must-not-use');
        assert.equal(fs.readFileSync(path.join(profile, SMOKE_PROFILE_MARKER), 'utf8'), 'synthetic-only\n');
        assert.equal(options.env.ELECTRON_RUN_AS_NODE, undefined);
        assert.equal(options.shell, false);
        return spawn(process.execPath, ['-e', script], options);
      },
    }),
  };
}

test('smoke runner owns a fresh profile, accepts a complete receipt, then removes only its run directory', async (t) => {
  const root = fixtureRoot(t);
  const existing = path.join(root, 'existing-profile');
  fs.mkdirSync(existing);
  fs.writeFileSync(path.join(existing, 'retain.txt'), 'existing data');
  const receipt = { ok: true, runtime: ELECTRON_VERSION, cdmReady: true, eme: true,
    encryptedFixture: true, cookieFixture: true, realCredentialsRead: false, mediaDownloaded: false };
  const run = smokeWithNode(root, `console.log(${JSON.stringify(JSON.stringify(receipt))})`);
  assert.deepEqual(await run.result, receipt);
  assert.equal(fs.existsSync(path.dirname(run.profile)), false);
  assert.equal(fs.readFileSync(path.join(existing, 'retain.txt'), 'utf8'), 'existing data');
});

test('smoke timeout terminates the child before cleaning its synthetic profile', async (t) => {
  const root = fixtureRoot(t);
  const run = smokeWithNode(root, 'setInterval(() => {}, 1000)', 200);
  assert.equal((await run.result).code, 'drm_smoke_timeout');
  assert.equal(fs.existsSync(path.dirname(run.profile)), false);
});

test('smoke cannot succeed on an exit code alone or expose a native diagnostic as a result', async (t) => {
  const root = fixtureRoot(t);
  const run = smokeWithNode(root, "console.log('native diagnostic with synthetic sentinel')");
  const result = await run.result;
  assert.equal(result.code, 'drm_smoke_result_missing');
  assert.equal(JSON.stringify(result).includes('sentinel'), false);
  assert.equal(fs.existsSync(path.dirname(run.profile)), false);
});

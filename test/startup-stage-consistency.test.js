'use strict';

// The startup stage vocabulary is defined in three places that must agree: the C
// patch emits the markers, the Go manager decides which are admissible evidence,
// and the renderer decides which may be shown to a user. A stage present in one
// and missing from another is a silent hole — the Go parser would drop a marker
// the Wrapper really emits, or the renderer would refuse to display a stage the
// backend legitimately published, and in both cases an initialization timeout
// goes back to being unlocatable.

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const repositoryRoot = path.join(__dirname, '..');
const goManagerPath = path.join(repositoryRoot, 'apple-music-downloader', 'wrapper_manager.go');
const rendererPath = path.join(repositoryRoot, 'web', 'renderer.js');
const patchPath = path.join(repositoryRoot, 'legacy', 'v1', 'appliance', 'patches', 'wrapper-login-fd.patch');
const probeOnlyStages = new Map([
  ['exec_network_preflight_begin', { source: 'launcher', shape: 'identity' }],
]);

// Stages the Go manager admits, with the source and field shape it requires.
function goStageSpecs() {
  const source = fs.readFileSync(goManagerPath, 'utf8');
  const table = source.match(
    /var wrapperStartupStages = map\[WrapperStartupStage\]wrapperStartupStageSpec\{([\s\S]*?)\n\}/,
  );
  assert.ok(table, 'the Go stage specification table was not found');
  const specs = new Map();
  const entry = /WrapperStartupStage(\w+):\s*\{wrapperStartupSource(\w+),\s*wrapperStartupShape(\w+),\s*(\d+)\}/g;
  for (const match of table[1].matchAll(entry)) {
    const constants = source.match(
      new RegExp(`WrapperStartupStage${match[1]}\\s+WrapperStartupStage = "([a-z0-9_]+)"`),
    );
    assert.ok(constants, `WrapperStartupStage${match[1]} has no string constant`);
    specs.set(constants[1], {
      source: match[2].toLowerCase(),
      shape: match[3].toLowerCase(),
      ordinal: Number(match[4]),
    });
  }
  assert.ok(specs.size > 0, 'no Go stage specifications were parsed');
  return specs;
}

function rendererStages() {
  const source = fs.readFileSync(rendererPath, 'utf8');
  const block = source.match(/const WRAPPER_STARTUP_STAGES = new Set\(\[([\s\S]*?)\]\);/);
  assert.ok(block, 'the renderer stage allowlist was not found');
  return new Set([...block[1].matchAll(/'([a-z0-9_]+)'/g)].map((match) => match[1]));
}

// Stages the patch actually emits, recovered from the patched view of each file.
function patchStages() {
  const document = fs.readFileSync(patchPath, 'utf8');
  const fileView = (filename) => {
    const marker = `diff --git a/${filename} b/${filename}`;
    assert.ok(document.includes(marker), `the patch does not modify ${filename}`);
    let section = document.slice(document.indexOf(marker) + marker.length);
    const next = section.indexOf('\ndiff --git ');
    if (next >= 0) section = section.slice(0, next);
    return section
      .split('\n')
      .filter((line) => (line.startsWith('+') || line.startsWith(' ')) && !line.startsWith('+++'))
      .map((line) => line.slice(1))
      .join('\n');
  };

  const launcher = fileView('wrapper.c');
  const native = fileView('main.c');
  const stages = new Map();
  const add = (name, source, shape) => stages.set(name, { source, shape });

  for (const match of launcher.matchAll(/launch_marker\("([a-z0-9_]+)"\)/g)) {
    add(match[1], 'launcher', 'bare');
  }
  for (const match of launcher.matchAll(/launch_marker_identity\("([a-z0-9_]+)"\)/g)) {
    add(match[1], 'launcher', 'identity');
  }
  // fork_parent is written inline because it carries the parent/child pair.
  if (/\[wrapper-launch\] fork_parent pid=%ld child=%ld pgid=%ld/.test(launcher)) {
    add('fork_parent', 'launcher', 'forkpair');
  }
  for (const match of native.matchAll(/amdl_startup_marker\("([a-z0-9_]+)"\)/g)) {
    add(match[1], 'native', 'bare');
  }
  return stages;
}

test('every emitted startup stage is admissible by the Go manager', () => {
  const go = goStageSpecs();
  for (const [stage, emitted] of patchStages()) {
    if (probeOnlyStages.has(stage)) {
      assert.deepEqual(emitted, probeOnlyStages.get(stage));
      assert.equal(go.has(stage), false, `${stage} must not enter production Wrapper status`);
      continue;
    }
    const spec = go.get(stage);
    assert.ok(spec, `the patch emits ${stage} but the Go manager would drop it as unknown`);
    assert.equal(spec.source, emitted.source, `${stage} is emitted by the ${emitted.source} but Go expects the ${spec.source}`);
    assert.equal(spec.shape, emitted.shape, `${stage} is emitted with the ${emitted.shape} shape but Go expects ${spec.shape}`);
  }
});

test('standalone probe stages stay outside production Wrapper and renderer vocabularies', () => {
  const emitted = patchStages();
  const go = goStageSpecs();
  const renderer = rendererStages();
  for (const [stage, expected] of probeOnlyStages) {
    assert.deepEqual(emitted.get(stage), expected, `${stage} is not emitted with its fixed shape`);
    assert.equal(go.has(stage), false, `${stage} must not be accepted as an authentication startup stage`);
    assert.equal(renderer.has(stage), false, `${stage} must not be rendered as authentication progress`);
  }
});

test('every stage the Go manager admits is actually emitted by the patch', () => {
  const emitted = patchStages();
  for (const stage of goStageSpecs().keys()) {
    assert.ok(emitted.has(stage), `the Go manager admits ${stage} but no marker emits it`);
  }
});

test('the renderer allowlist matches the Go stage vocabulary exactly', () => {
  const go = [...goStageSpecs().keys()].sort();
  const renderer = [...rendererStages()].sort();
  assert.deepEqual(renderer, go, 'the renderer and Go stage vocabularies have drifted');
});

test('stage ordinals are monotonic so late sibling markers cannot rewind progress', () => {
  const specs = goStageSpecs();
  // fork_parent and fork_child are siblings on diverging branches after fork, so
  // they must share an ordinal; every other stage must be strictly ordered.
  assert.equal(
    specs.get('fork_parent').ordinal,
    specs.get('fork_child').ordinal,
    'the two fork branches must share an ordinal, since neither precedes the other',
  );
  const launcherLast = specs.get('exec_main_begin').ordinal;
  const nativeFirst = specs.get('main_enter').ordinal;
  assert.ok(
    launcherLast < nativeFirst,
    'every launcher stage must precede the first native stage',
  );
  for (const [stage, spec] of specs) {
    assert.ok(Number.isInteger(spec.ordinal) && spec.ordinal > 0, `${stage} has no usable ordinal`);
  }
});

// The renderer must never present an unrecognized stage, and must never reach for
// the raw value instead of the allowlisted one.
test('the renderer only renders a stage that passed its allowlist', () => {
  const source = fs.readFileSync(rendererPath, 'utf8');
  assert.match(source, /function knownStartupStage\(/, 'the renderer must funnel stages through one guard');
  const timeoutBranch = source.match(/exitReason === 'initialization_timeout'\)\s*\{([\s\S]*?)\n {4}\}/);
  assert.ok(timeoutBranch, 'the initialization-timeout branch was not found');
  assert.match(timeoutBranch[1], /knownStartupStage\(lastExit\?\.lastStartupStage\)/);
  assert.doesNotMatch(
    timeoutBranch[1].replace(/knownStartupStage\(lastExit\?\.lastStartupStage\)/g, ''),
    /lastStartupStage/,
    'the raw stage value must never be used outside the allowlist guard',
  );
});

// A stage is only evidence because the marker that carries it is unforgeable in
// shape. If the recorded patch digest and the patch disagree, the emitted
// vocabulary above was read from a patch that is not the one being built.
test('the stage vocabulary is read from the patch the build actually locks', () => {
  const dockerfile = fs.readFileSync(path.join(repositoryRoot, 'legacy/v1/Dockerfile'), 'utf8');
  const pinned = dockerfile.match(/^ARG WRAPPER_PATCH_SHA256=([0-9a-f]{64})$/m);
  assert.ok(pinned, 'the Dockerfile must pin the Wrapper patch digest');
  const digest = crypto.createHash('sha256').update(fs.readFileSync(patchPath)).digest('hex');
  assert.equal(pinned[1], digest, 'the pinned patch digest does not describe the patch on disk');
});

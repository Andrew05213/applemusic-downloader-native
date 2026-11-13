'use strict';

const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawnSync } = require('node:child_process');
const lock = require('../native-runtime/inputs.lock.json');
const agentLock = require('../native-runtime/guest-agent.lock.json');

function digest(file, algorithm = 'sha256') {
  const hash = crypto.createHash(algorithm);
  const descriptor = fs.openSync(file, 'r');
  try {
    const buffer = Buffer.alloc(1024 * 1024);
    let count;
    while ((count = fs.readSync(descriptor, buffer, 0, buffer.length, null)) > 0) hash.update(buffer.subarray(0, count));
  } finally { fs.closeSync(descriptor); }
  return hash.digest('hex');
}

function verifyFile(file, algorithm, expected) {
  try { if (digest(file, algorithm) === expected) return; } catch { /* Fixed error only. */ }
  throw new Error('native_input_identity_mismatch');
}

function makeManifest(root,options={}) {
  const files = {};
  function walk(directory) {
    for (const name of fs.readdirSync(directory).sort()) {
      const file = path.join(directory, name);
      const stat = fs.lstatSync(file);
      if (stat.isSymbolicLink()) throw new Error('native_bundle_incomplete');
      if (stat.isDirectory()) walk(file);
      else if (stat.isFile() && stat.size > 0) files[path.relative(root, file).split(path.sep).join('/')] = { sha256: digest(file), size: stat.size };
      else throw new Error('native_bundle_incomplete');
    }
  }
  try { for (const name of ['guest', 'notices', 'qemu']) walk(path.join(root, name)); } catch { throw new Error('native_bundle_incomplete'); }
  for (const name of ['qemu/qemu-system-x86_64.exe', 'qemu/share/bios-256k.bin', 'guest/vmlinuz', 'guest/initramfs.cpio.gz', 'guest/data.img']) {
    if (!files[name]) throw new Error('native_bundle_incomplete');
  }
  return { schemaVersion: 1, wrapperCommit: lock.wrapper.commit, isolated: options.isolated!==false, controlProtocol: 1,
    qemu: 'qemu/qemu-system-x86_64.exe', firmware: 'qemu/share', kernel: 'guest/vmlinuz', initramfs: 'guest/initramfs.cpio.gz', dataTemplate: 'guest/data.img',
    files: Object.fromEntries(Object.keys(files).sort().map((name) => [name, files[name]])) };
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { shell: false, windowsHide: true, stdio: options.capture ? 'pipe' : 'inherit', encoding: 'utf8', ...options,
    env: require('./native-build-environment').buildEnvironment(path.resolve(__dirname, '..'), options.env || process.env) });
  if (result.error || result.status !== 0) throw new Error('native_build_step_failed');
  return (result.stdout || '').trim();
}

function cachedInput(cache, input, algorithm) {
  const file = path.join(cache, input.cacheName);
  if (!fs.existsSync(file)) {
    const temporary = file + '.' + crypto.randomUUID() + '.partial';
    run(process.platform==='win32'?'curl.exe':'curl', ['--fail', '--location', '--retry', '2', '--connect-timeout', '20', '--max-time', '1800', '--output', temporary, input.url]);
    verifyFile(temporary, algorithm, input[algorithm]);
    fs.renameSync(temporary, file);
  }
  verifyFile(file, algorithm, input[algorithm]);
  return file;
}

function copyQemu(extracted, output) {
  const target = path.join(output, 'qemu');
  fs.mkdirSync(path.join(target, 'share'), { recursive: true });
  // The installer is never executed. Only runtime support, firmware and
  // notices enter the bundle; NSIS plugins and other CPU EXEs do not.
  function copyDLLs(directory) {
    for (const name of fs.readdirSync(directory).sort()) {
      if (name === '$PLUGINSDIR' || name === 'share') continue;
      const file = path.join(directory, name);
      const stat = fs.lstatSync(file);
      if (stat.isSymbolicLink()) throw new Error('native_qemu_layout_invalid');
      if (stat.isDirectory()) copyDLLs(file);
      else if (/\.dll$/i.test(name) || (directory === extracted && name === 'qemu-system-x86_64.exe')) {
        const destination = path.join(target, path.relative(extracted, file));
        fs.mkdirSync(path.dirname(destination), { recursive: true });
        fs.copyFileSync(file, destination);
      }
    }
  }
  copyDLLs(extracted);
  const share = path.join(extracted, 'share');
  for (const name of fs.readdirSync(share).sort()) {
    const file = path.join(share, name);
    if (fs.lstatSync(file).isFile()) fs.copyFileSync(file, path.join(target, 'share', name));
  }
  for (const name of ['COPYING', 'COPYING.LIB']) {
    fs.copyFileSync(path.join(extracted, name), path.join(output, 'notices', 'qemu-' + name));
  }
}

function runtimeOutput(root,id) { return path.join(root, '.artifacts', 'runtime', id); }
function buildGuest(options = {}) {
  const root = options.repositoryRoot || path.resolve(__dirname, '..');
  const id = crypto.randomUUID();
  const output = path.resolve(root, options.output || runtimeOutput(root, id));
  const relative = path.relative(root, output);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || fs.existsSync(output)) throw new Error('native_output_must_be_new');
  const toolchain = require('./native-toolchain').ensureToolchain({repositoryRoot:root});
  const toolchainReference = 'amdl-native-toolchain-runtime:' + id;
  run('docker', ['tag', toolchain.imageID, toolchainReference]);
  const toolchainID = () => run('docker', ['image', 'inspect', toolchainReference, '--format', '{{.Id}}'], { capture: true });
  if (toolchainID() !== toolchain.imageID) throw new Error('native_toolchain_identity_mismatch');
  const cache = path.join(root, '.tmp-v2-native', 'cache');
  fs.mkdirSync(cache, { recursive: true });
  const source = cachedInput(cache, lock.wrapper, 'sha256');
  const context = path.join(root, '.tmp-v2-native', 'context-' + id);
  fs.mkdirSync(context);
  for (const name of ['Dockerfile', 'inputs.lock.json', 'guest-agent.lock.json', 'lite-native.patch', 'lite-session.patch', 'lite-store-bridge.patch', 'native_login.h', 'native_service.h', 'native_store_bridge.h', 'build-guest.sh', 'init']) fs.copyFileSync(path.join(root, 'native-runtime', name), path.join(context, name));
  const goVersion = run('go', ['version'], { capture: true });
  if (!goVersion.startsWith('go version ' + agentLock.goVersion + ' ')) throw new Error('native_go_identity_mismatch');
  const agentBinary = path.join(context, 'amdl-guest');
  run('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w -buildid=', '-o', agentBinary, './cmd/amdl-guest'],
    { cwd: path.join(root, 'apple-music-downloader'), env: { ...process.env, GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' } });
  const agentSources = {};
  for (const directory of ['internal/nativeauth', 'internal/guestagent', 'cmd/amdl-guest']) {
    for (const name of fs.readdirSync(path.join(root, 'apple-music-downloader', directory)).sort()) {
      if (name.endsWith('.go') && !name.endsWith('_test.go')) agentSources[directory + '/' + name] = digest(path.join(root, 'apple-music-downloader', directory, name));
    }
  }
  fs.writeFileSync(path.join(context, 'guest-agent-source.json'), JSON.stringify({ goVersion, binarySHA256: digest(agentBinary), sources: agentSources }, null, 2) + '\n');
  fs.copyFileSync(source, path.join(context, 'wrapper.tar.gz'));
  const image = 'amdl-v2-guest:' + id;
  run('docker', ['build', '--pull=false', '--network=none', '--progress=plain', '--target', 'runtime-export', '--build-arg', 'TOOLCHAIN_IMAGE=' + toolchainReference,
    '--build-arg', 'GUEST_REPRO_RUN=' + id, '--tag', image, context], { cwd: root });
  if (toolchainID() !== toolchain.imageID) throw new Error('native_toolchain_identity_mismatch');
  fs.mkdirSync(output, { recursive: true });
  const container = 'amdl-v2-export-' + id;
  run('docker', ['create', '--name', container, image, '/never-executed']);
  try {
    for (const directory of ['guest', 'notices', 'evidence']) run('docker', ['cp', container + ':/' + directory, path.join(output, directory)]);
  } finally { run('docker', ['rm', container]); }
  fs.writeFileSync(path.join(output, 'evidence', 'toolchain-build.json'), JSON.stringify(toolchain,null,2)+'\n');
  fs.writeFileSync(path.join(output, 'evidence', 'build.json'), JSON.stringify({ schemaVersion: 1, publicReleaseAllowed: false, sourceDateEpoch: lock.sourceDateEpoch,
    toolchainID: toolchain.imageID, toolchainDefinitionSHA256:toolchain.definitionSHA256,
    toolchainEvidenceSHA256:digest(path.join(output,'evidence','toolchain-build.json')),
    wrapperCommit: lock.wrapper.commit, sourceSHA256: lock.wrapper.sha256,
    qemuInstallerSHA512: lock.qemu.sha512, patchSHA256: digest(path.join(context, 'lite-native.patch')),
    recipeSHA256: digest(path.join(context, 'Dockerfile')), initSHA256: digest(path.join(context, 'init')),
    extractionToolsLockSHA256:digest(path.join(root,'native-runtime/build-tools.lock.json')),
    assemblySHA256: digest(path.join(context, 'build-guest.sh')), inputsLockSHA256: digest(path.join(context, 'inputs.lock.json')),
    sessionPatchSHA256: digest(path.join(context, 'lite-session.patch')), nativeLoginSHA256: digest(path.join(context, 'native_login.h')),
    nativeServiceSHA256: digest(path.join(context, 'native_service.h')), guestAgentSHA256: digest(agentBinary), guestAgentSourceSHA256: digest(path.join(context, 'guest-agent-source.json')),
    storeBridgeSHA256: digest(path.join(context,'native_store_bridge.h')), storeBridgePatchSHA256: digest(path.join(context,'lite-store-bridge.patch')),
    guestAssemblyRun: id,
  }, null, 2) + '\n');
  return output;
}

function buildNativeRuntime(options = {}) {
  if(process.platform!=='win32')throw new Error('native_windows_build_host_required');
  const root=options.repositoryRoot||path.resolve(__dirname,'..');
  const output=path.resolve(root,options.output||runtimeOutput(root,crypto.randomUUID()));
  const relative=path.relative(root,output);
  if(!relative||relative.startsWith('..')||path.isAbsolute(relative)||fs.existsSync(output))throw new Error('native_output_must_be_new');
  if(options.guestInput){
    const input=path.resolve(options.guestInput);
    require('./native-cloud-guest').verifyGuestExport(input,options.commit,options.receiptSHA256,root);
    fs.mkdirSync(output,{recursive:true});
    for(const name of ['guest','notices','evidence'])require('./native-package').copyTree(path.join(input,name),path.join(output,name));
  }else buildGuest({...options,output});
  const context=path.join(root,'.tmp-v2-native','qemu-'+crypto.randomUUID());fs.mkdirSync(context,{recursive:true});
  const cache=path.join(root,'.tmp-v2-native','cache');fs.mkdirSync(cache,{recursive:true});
  const qemu=cachedInput(cache,lock.qemu,'sha512'),extracted=path.join(context,'qemu-extracted');
  const sevenZip=options.sevenZip||require('./native-archive-tools').prepare(root);
  run(sevenZip,['x','-y','-o'+path.relative(root,extracted),path.relative(root,qemu),'qemu-system-x86_64.exe','*.dll','share/*','COPYING','COPYING.LIB'],{cwd:root});
  copyQemu(extracted,output);
  const qemuVersion=run(path.join(output,'qemu','qemu-system-x86_64.exe'),['--version'],{capture:true});
  if(!qemuVersion.includes('version '+lock.qemu.version))throw new Error('native_qemu_version_mismatch');
  fs.writeFileSync(path.join(output,'bundle.json'),JSON.stringify(makeManifest(output,{isolated:options.isolated}),null,2)+'\n');
  const file=path.join(output,'evidence/build.json'),build=JSON.parse(fs.readFileSync(file));
  fs.writeFileSync(file,JSON.stringify({...build,qemuVersion,manifestSHA256:digest(path.join(output,'bundle.json'))},null,2)+'\n');
  require('./native-package').verifyBundleSources(output,root);
  return output;
}

if (require.main === module) {
  try {
    const args = process.argv.slice(2);const allow=args.includes('--allow-network');const selected=args.filter(value=>value!=='--allow-network');
    if (selected.length !== 0 && (selected.length !== 2 || selected[0] !== '--out')||args.filter(value=>value==='--allow-network').length>1) throw new Error('native_build_arguments_invalid');
    process.stdout.write(buildNativeRuntime({ output: selected[1],isolated:!allow }) + '\n');
  } catch (error) { process.stderr.write((/^native_[a-z_]+$/.test(error.message) ? error.message : 'native_runtime_build_failed') + '\n'); process.exitCode = 1; }
}

module.exports = { buildNativeRuntime, buildGuest, verifyFile, makeManifest, runtimeOutput };

'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { verifyFile, makeManifest } = require('../../electron/build-native-runtime');
const { compareBundles } = require('../native/native-guest-repro.cjs');

const root = path.resolve(__dirname, '../..');

test('native guest inputs pin sources, package snapshot and QEMU publisher digest', () => {
  const lock = require('../../native-runtime/inputs.lock.json');
  assert.equal(lock.wrapper.repository, 'https://github.com/Andrew05213/wrapper');
  assert.equal(lock.wrapper.branch, 'work');
  assert.equal(lock.wrapper.commit, '80dcaa1bc6cc88e41f1d6bb16d0cc9a241d4bca3');
  assert.equal(lock.wrapper.sha256, '5041380a5488f79060bbfd0e0356ac4241f50e035bcf89a3fc0bd1b372e7dfa7');
  assert.equal(lock.wrapper.caBundleSHA256, '427c064216761a1e72a8b30f0d954d08cb6e4435a82091fb865afd20c8497285');
  assert.equal(lock.wrapper.url, 'https://codeload.github.com/Andrew05213/wrapper/tar.gz/'+lock.wrapper.commit);
  assert.equal(lock.wrapper.cacheName, 'wrapper-andrew05213-'+lock.wrapper.commit.slice(0,8)+'.tar.gz');
  assert.match(fs.readFileSync(path.join(root,'native-runtime/Dockerfile'),'utf8'),new RegExp(lock.wrapper.sha256+'  wrapper\\.tar\\.gz'));
  const runtimeSource=fs.readFileSync(path.join(root,'apple-music-downloader/internal/nativeruntime/bundle.go'),'utf8');
  assert.equal(runtimeSource.match(/const WrapperCommit = "([a-f0-9]{40})"/)[1],lock.wrapper.commit);
  assert.equal(lock.qemu.sha512, '5bcf9eed634e8575a37b74f445af41a2fe4106da512d0c30c368301d4c105037fdfab40a5287367a28a957624cddebbc8c07e16c88ab6634f554cdf3d16bf543');
  assert.equal(lock.toolchain.definition, 'native-runtime/toolchain/inputs.lock.json');
  assert.match(require('../../electron/native-toolchain').definition().definitionSHA256, /^[a-f0-9]{64}$/);
  assert.equal(lock.debianSnapshot, '20260728T000000Z');
  assert.equal(lock.packages['linux-image-6.1.0-51-amd64'], '6.1.177-1');
});

test('native input hashing refuses mutations without exposing a raw path', (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-native-input-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const file = path.join(directory, 'SENTINEL_PRIVATE_PATH');
  fs.writeFileSync(file, 'fixture');
  const hash = crypto.createHash('sha256').update('fixture').digest('hex');
  verifyFile(file, 'sha256', hash);
  fs.writeFileSync(file, 'changed');
  assert.throws(() => verifyFile(file, 'sha256', hash), /^Error: native_input_identity_mismatch$/);
});

test('native guest manifest is deterministic, isolated and contains only immutable bundle trees', (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-native-manifest-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  for (const name of ['qemu/qemu-system-x86_64.exe', 'qemu/share/bios-256k.bin', 'guest/vmlinuz', 'guest/initramfs.cpio.gz', 'guest/data.img', 'notices/wrapper-LICENSE']) {
    fs.mkdirSync(path.dirname(path.join(directory, name)), { recursive: true });
    fs.writeFileSync(path.join(directory, name), name);
  }
  fs.writeFileSync(path.join(directory, 'build-evidence.json'), 'not a runtime input');
  const a = makeManifest(directory);
  assert.deepEqual(a, makeManifest(directory));
  assert.equal(a.isolated, true);
  assert.equal(a.controlProtocol,1);
  assert.equal(makeManifest(directory,{isolated:false}).isolated,false);
  assert.equal(a.qemu, 'qemu/qemu-system-x86_64.exe');
  assert.equal(a.files['build-evidence.json'], undefined);
  assert.deepEqual(Object.keys(a.files), Object.keys(a.files).toSorted());
  fs.unlinkSync(path.join(directory, 'guest/vmlinuz'));
  assert.throws(() => makeManifest(directory), /^Error: native_bundle_incomplete$/);
});

test('native guest recipe preserves strict TLS, source pins and a fail-closed data mount', () => {
  const patch = fs.readFileSync(path.join(root, 'native-runtime/lite-native.patch'), 'utf8');
  const dockerfile = fs.readFileSync(path.join(root, 'native-runtime/Dockerfile'), 'utf8');
  const init = fs.readFileSync(path.join(root, 'native-runtime/init'), 'utf8');
  assert.match(patch, /CURLOPT_CAINFO/);
  assert.match(patch, /get_ca_bundle\(\)/);
  assert.match(patch, /strcmp\(str, get_ca_bundle\(\)\)/);
  assert.match(patch, /-.*DobbyHook.*curl_easy_setopt/);
  assert.doesNotMatch(patch.split('\n').filter((line) => line.startsWith('+')).join('\n'), /SSL_VERIFYPEER, 0|SSL_VERIFYHOST, 0|g_ssl_verify_disabled = true/);
  assert.match(dockerfile, /CMAKE_BUILD_TYPE=Release/);
  assert.match(dockerfile, /FETCHCONTENT_FULLY_DISCONNECTED=ON/);
  assert.match(fs.readFileSync(path.join(root,'native-runtime/toolchain/Dockerfile'),'utf8'), /snapshot\.debian\.org/);
  assert.match(init, /mount -t ext4 \/dev\/vda \/data \|\| fail/);
  assert.match(init, /crc32c_generic ext4/);
  const assembly = fs.readFileSync(path.join(root, 'native-runtime/build-guest.sh'), 'utf8');
  assert.match(assembly, /\$source\/rootfs\/system\/etc\/security\/cacert\.pem/);
  assert.doesNotMatch(assembly, /install .*\/etc\/ssl\/certs\/ca-certificates\.crt/);
  assert(assembly.includes("printf 'nameserver 10.0.2.3"));
  assert.doesNotMatch(patch.split('\n').filter(line=>line.startsWith('+')).join('\n'), /resolvers\[|223\.5\.5\.5|8\.8\.8\.8/);
  const ca=require('../../native-runtime/inputs.lock.json').wrapper.caBundleSHA256;
  assert(dockerfile.includes(ca+'  rootfs/system/etc/security/cacert.pem'));
  assert(dockerfile.includes('python3 /native-build/check-wrapper-network.py'));
  assert.match(assembly, /crc32c_generic ext4/);
  assert.match(dockerfile, /ARG GUEST_REPRO_RUN/);
  assert.match(init, /amdl-guest --base-dir \/data --capability-file \/run\/control\.secret/);
  assert.match(init, /qemu_fw_cfg\/by_name\/opt\/amdl\/control\/raw/);
  assert.doesNotMatch(init, /--login|--debug|config\.yaml|cookie/i);
  const session=fs.readFileSync(path.join(root,'native-runtime/lite-session.patch'),'utf8');
  assert.doesNotMatch(session,/^diff --git .*playback\.cpp|^\+.*getKdContext\(/m);
  assert.match(fs.readFileSync(path.join(root,'native-runtime/native_login.h'),'utf8'),/set_auth_interaction_handler\(native_display_interaction\)/);
  assert.match(session,/g_native_service && \(g_host != "127\.0\.0\.1" \|\| g_cors_enabled\)/);
  const service=fs.readFileSync(path.join(root,'native-runtime/native_service.h'),'utf8');
  assert.match(service,/native_decrypt\(stream, adam, uri, req\.body, output\)/);
  assert.match(service,/native_decrypt_close\(req\.get_header_value\("X-AMDL-Stream"\)\)/);
  const adapter=fs.readFileSync(path.join(root,'apple-music-downloader/internal/guestclient/client.go'),'utf8');
  assert.doesNotMatch(adapter,/"\/key\?|aes\.NewCipher|crypto\/aes/);
});

test('guest export comparison refuses changed bytes or reused assembly evidence', (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-native-repro-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const bundles = ['a', 'b'].map((name) => path.join(directory, name));
  const evidence = { toolchainID: 'fixture', toolchainDefinitionSHA256:'definition',toolchainEvidenceSHA256:'toolchain',extractionToolsLockSHA256:'extraction',sourceDateEpoch: 1786320000, sourceSHA256: 'source', qemuInstallerSHA512: 'qemu',
    patchSHA256: 'patch', recipeSHA256: 'recipe', initSHA256: 'init', assemblySHA256: 'assembly', inputsLockSHA256: 'lock', sessionPatchSHA256:'session',nativeLoginSHA256:'login',nativeServiceSHA256:'service',guestAgentSHA256:'agent',guestAgentSourceSHA256:'source',
    storeBridgeSHA256:'store',storeBridgePatchSHA256:'store-patch',networkCheckSHA256:'network-check' };
  for (const bundle of bundles) {
    for (const name of ['qemu/qemu-system-x86_64.exe', 'qemu/share/bios-256k.bin', 'guest/vmlinuz', 'guest/initramfs.cpio.gz', 'guest/data.img', 'notices/wrapper-LICENSE']) {
      fs.mkdirSync(path.dirname(path.join(bundle, name)), { recursive: true });
      fs.writeFileSync(path.join(bundle, name), name);
    }
    fs.mkdirSync(path.join(bundle, 'evidence'));
    fs.writeFileSync(path.join(bundle, 'bundle.json'), JSON.stringify(makeManifest(bundle), null, 2) + '\n');
    fs.writeFileSync(path.join(bundle, 'evidence/build.json'), JSON.stringify({ ...evidence, guestAssemblyRun: crypto.randomUUID() }));
  }
  assert.equal(compareBundles(...bundles).passed, true);
  for(const field of ['storeBridgeSHA256','storeBridgePatchSHA256','networkCheckSHA256']){
    const files=bundles.map(bundle=>path.join(bundle,'evidence/build.json'));
    const before=files.map(file=>fs.readFileSync(file));const changed=JSON.parse(before[1]);changed[field]='changed';
    fs.writeFileSync(files[1],JSON.stringify(changed));assert.throws(()=>compareBundles(...bundles),field+' differs');
    for(let index=0;index<files.length;index++){const missing=JSON.parse(before[index]);delete missing[field];fs.writeFileSync(files[index],JSON.stringify(missing));}
    assert.throws(()=>compareBundles(...bundles),field+' omitted');
    for(let index=0;index<files.length;index++)fs.writeFileSync(files[index],before[index]);
  }
  const kernel = path.join(bundles[1], 'guest/vmlinuz');
  fs.writeFileSync(kernel, 'changed');
  assert.throws(() => compareBundles(...bundles));
  fs.writeFileSync(kernel, 'guest/vmlinuz');
  fs.copyFileSync(path.join(bundles[0], 'evidence/build.json'), path.join(bundles[1], 'evidence/build.json'));
  assert.throws(() => compareBundles(...bundles));
});

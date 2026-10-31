'use strict';
const test=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const path=require('node:path');
const root=path.resolve(__dirname,'../..');

test('native 1.0 versions are validated independently of public release permission',()=>{
 const vm=require('node:vm');const source=fs.readFileSync(path.join(root,'electron/native-version.js'),'utf8');
 for(const version of ['1.0.0','1.0.1','1.0.0-beta.1']){
  const context={module:{exports:{}},require:name=>{assert.equal(name,'../package.json');return {version};}};
  vm.runInNewContext(source,context);assert.equal(context.module.exports.VERSION,version);
 }
 for(const version of ['1.0.00','1.4.3','1.0.0-beta.0','1.0.0-public','1.0.0\n','2.0.0','3.0.0']){
  assert.throws(()=>vm.runInNewContext(source,{module:{exports:{}},require:()=>({version})}),/native_version_invalid/);
 }
 assert.equal(require('../../appliance/release-policy.json').publicReleaseAllowed,false);
});
test('native installer selects standard Electron and retains compatible per-user identity',()=>{
 const pkg=require('../../package.json');const config=require('../../electron-builder.native.cjs');
 const lock=require('../../package-lock.json');assert.equal(lock.version,pkg.version);assert.equal(lock.packages['node_modules/electron'].version,'45.0.0');assert.equal(lock.packages['node_modules/electron'].resolved,'https://registry.npmjs.org/electron/-/electron-45.0.0.tgz');
 assert.equal(pkg.version,'1.0.0');assert.equal(pkg.main,'src/main/native-main.js');assert.equal(pkg.devDependencies.electron,'45.0.0');assert.equal(config.electronVersion,'45.0.0');
 assert.equal(config.appId,'io.github.andrew05213.applemusicdownloader.v2test');assert.equal(config.nsis.deleteAppDataOnUninstall,false);assert.equal(config.publish,null);
 assert.equal(config.productName,'Apple Music Downloader Native');assert.equal(config.nsis.guid,'762d2d31-e8dd-4aca-a7e5-89dbd09cb138');
 assert.equal(config.nsis.shortcutName,'Apple Music Downloader Native');
 assert.equal(config.nsis.artifactName,'AppleMusicDownloader-${version}-Setup.${ext}');assert.equal(config.nsis.license,'NATIVE-LOCAL-NOTICE.md');
 const main=fs.readFileSync(path.join(root,'src/main/native-main.js'),'utf8');
 assert.match(main,/AppleMusicDownloader-v2-test/);assert.match(main,/Apple Music Downloads 2\.0 Test/);
 assert.equal(require('../../appliance/release-policy.json').publicReleaseAllowed,false);
 assert.match(fs.readFileSync(path.join(root,config.nsis.license),'utf8'),/publicReleaseAllowed=false/);
 const installationNotes=fs.readFileSync(path.join(root,config.nsis.license),'utf8');
 assert.ok(installationNotes.includes('Version: '+pkg.version+'.'));
 assert.match(installationNotes,/Apple Music Downloader Native installation notes/);
 assert.ok(config.files.includes('src/main/native-main.js'));assert.ok(config.files.includes('electron/native-service.js'));
 assert.doesNotMatch(JSON.stringify(config.files),/wsl|official-web|apple-music-browser|drm|legacy|src\/\*\*/i);
 assert.equal(config.nsis.include,'electron/installer-native.nsh');assert.doesNotMatch(fs.readFileSync(path.join(root,config.nsis.include),'utf8'),/wsl|unregister|terminate|DeleteReg|RMDir|\.bin/i);
});
test('native installer offers a directory picker without changing per-user identity or deleting app data',()=>{
 const config=require('../../electron-builder.native.cjs');
 assert.equal(config.nsis.oneClick,false);
 assert.equal(config.nsis.allowToChangeInstallationDirectory,true);
 assert.equal(config.nsis.perMachine,false);
 assert.equal(config.nsis.allowElevation,false);
 assert.equal(config.nsis.deleteAppDataOnUninstall,false);
 assert.equal(config.appId,'io.github.andrew05213.applemusicdownloader.v2test');
 assert.equal(config.nsis.guid,'762d2d31-e8dd-4aca-a7e5-89dbd09cb138');
 // Verify the installed builder really wires this option to its directory page.
 const template=fs.readFileSync(path.join(path.dirname(require.resolve('app-builder-lib/package.json')),'templates/nsis/assistedInstaller.nsh'),'utf8');
 assert.match(template,/!ifdef allowToChangeInstallationDirectory[\s\S]*?!insertmacro MUI_PAGE_DIRECTORY/);
});

test('native package resource receipt rejects changed content and public builds retain staging verification',async t=>{
 const os=require('node:os');const crypto=require('node:crypto');const {verifyReceipt}=require('../../electron/native-package');const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-resource-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 fs.mkdirSync(path.join(directory,'bin'));fs.writeFileSync(path.join(directory,'bin/amdl-service.exe'),'fixture');const receipt={schemaVersion:1,version:require('../../package.json').version,publicReleaseAllowed:false,electronVersion:'45.0.0',ui:{},files:{'bin/amdl-service.exe':{sha256:crypto.createHash('sha256').update('fixture').digest('hex'),size:7}}};assert.equal(verifyReceipt(directory,receipt),true);fs.writeFileSync(path.join(directory,'bin/amdl-service.exe'),'changed');assert.throws(()=>verifyReceipt(directory,receipt),/identity_mismatch/);
 const previous=process.env.AMDL_PUBLIC_RELEASE;process.env.AMDL_PUBLIC_RELEASE='1';try{await assert.rejects(require('../../electron-builder.native.cjs').beforePack(),error=>error.message!=='native_public_release_blocked');}finally{if(previous===undefined)delete process.env.AMDL_PUBLIC_RELEASE;else process.env.AMDL_PUBLIC_RELEASE=previous;}
});
test('native public gate still rejects when its policy is disabled',async()=>{
 const policy=require('../../native-runtime/release-policy.json');const previous=policy.publicReleaseAllowed;const env=process.env.AMDL_PUBLIC_RELEASE;
 try{policy.publicReleaseAllowed=false;process.env.AMDL_PUBLIC_RELEASE='1';await assert.rejects(require('../../electron-builder.native.cjs').beforePack(),/native_public_release_blocked/);}
 finally{policy.publicReleaseAllowed=previous;if(env===undefined)delete process.env.AMDL_PUBLIC_RELEASE;else process.env.AMDL_PUBLIC_RELEASE=env;}
});
test('archive verifier reads normalized resource names on the host platform',async t=>{
 const os=require('node:os');const asar=require('@electron/asar');const {readArchiveFile}=require('../../electron/verify-native-package');const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-asar-path-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));fs.mkdirSync(path.join(directory,'input/src/main'),{recursive:true});fs.writeFileSync(path.join(directory,'input/src/main/native-main.js'),'fixture');await asar.createPackage(path.join(directory,'input'),path.join(directory,'fixture.asar'));assert.equal(readArchiveFile(path.join(directory,'fixture.asar'),'src/main/native-main.js').toString(),'fixture');
});
test('native package preparation and verification use the same immutable resource receipt',()=>{
 const {createReceipt,verifyReceipt}=require('../../electron/native-package');
 assert.equal(typeof createReceipt,'function');assert.equal(typeof verifyReceipt,'function');
});

function freshnessFixture(t){
 const os=require('node:os');const {makeManifest}=require('../../electron/build-native-runtime');const {UI_FILES,hash}=require('../../electron/native-package');
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-package-freshness-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 const repository=path.join(directory,'repository');const bundle=path.join(directory,'bundle');const stage=path.join(directory,'stage');
 const write=(file,value)=>{fs.mkdirSync(path.dirname(file),{recursive:true});fs.writeFileSync(file,value);};
 const inputs=require('../../native-runtime/inputs.lock.json');const agentLock=require('../../native-runtime/guest-agent.lock.json');
 write(path.join(repository,'native-runtime/inputs.lock.json'),JSON.stringify(inputs));write(path.join(repository,'native-runtime/guest-agent.lock.json'),JSON.stringify(agentLock));
 const sourceFields={patchSHA256:'lite-native.patch',recipeSHA256:'Dockerfile',initSHA256:'init',assemblySHA256:'build-guest.sh',
  sessionPatchSHA256:'lite-session.patch',nativeLoginSHA256:'native_login.h',nativeServiceSHA256:'native_service.h',
  storeBridgeSHA256:'native_store_bridge.h',storeBridgePatchSHA256:'lite-store-bridge.patch'};
 for(const file of Object.values(sourceFields))write(path.join(repository,'native-runtime',file),'fixture '+file);
 const sources={};for(const part of ['internal/nativeauth','internal/guestagent','cmd/amdl-guest']){
  const name=part+'/fixture.go';write(path.join(repository,'apple-music-downloader',name),'package fixture\n');sources[name]=hash(path.join(repository,'apple-music-downloader',name));
  write(path.join(repository,'apple-music-downloader',part,'excluded_test.go'),'not a production source');
 }
 write(path.join(repository,'node_modules/electron/dist/version'),'45.0.0');write(path.join(repository,'node_modules/electron/dist/electron.exe'),'fixture electron');
 for(const file of UI_FILES)write(path.join(repository,file),'fixture '+file);
 for(const file of ['qemu/qemu-system-x86_64.exe','qemu/share/bios-256k.bin','guest/vmlinuz','guest/initramfs.cpio.gz','guest/data.img','notices/wrapper-LICENSE'])write(path.join(bundle,file),file);
 write(path.join(bundle,'bundle.json'),JSON.stringify(makeManifest(bundle,{isolated:false})));
 const guestSource={goVersion:'go version '+agentLock.goVersion+' fixture/amd64',binarySHA256:'c'.repeat(64),sources};
 write(path.join(bundle,'evidence/guest-agent-source.json'),JSON.stringify(guestSource));
 const evidence={schemaVersion:1,publicReleaseAllowed:false,sourceDateEpoch:inputs.sourceDateEpoch,toolchainID:inputs.toolchain.id,
  wrapperCommit:inputs.wrapper.commit,sourceSHA256:inputs.wrapper.sha256,qemuInstallerSHA512:inputs.qemu.sha512,
  inputsLockSHA256:hash(path.join(repository,'native-runtime/inputs.lock.json')),
  guestAgentSHA256:guestSource.binarySHA256,guestAgentSourceSHA256:hash(path.join(bundle,'evidence/guest-agent-source.json')),
  manifestSHA256:hash(path.join(bundle,'bundle.json')),...Object.fromEntries(Object.entries(sourceFields).map(([key,file])=>[key,hash(path.join(repository,'native-runtime',file))]))};
 write(path.join(bundle,'evidence/build.json'),JSON.stringify(evidence));write(path.join(stage,'bin/amdl-service.exe'),'fixture service');write(path.join(stage,'native-runtime/fixture.data'),'fixture runtime');
 return {repository,bundle,stage,evidence,guestSource,sourceFields,write};
}

test('native package accepts only a guest whose store and complete device-login sources match this checkout',t=>{
 const {createReceipt}=require('../../electron/native-package');const fixture=freshnessFixture(t);
 assert.equal(createReceipt(fixture.stage,fixture.bundle,fixture.repository).version,require('../../package.json').version);
 for(const field of ['storeBridgeSHA256','storeBridgePatchSHA256']){
  const original=fixture.evidence[field];delete fixture.evidence[field];fixture.write(path.join(fixture.bundle,'evidence/build.json'),JSON.stringify(fixture.evidence));
  assert.throws(()=>createReceipt(fixture.stage,fixture.bundle,fixture.repository),/native_bundle_source_stale/,field+' missing');
  fixture.evidence[field]=original;fixture.write(path.join(fixture.bundle,'evidence/build.json'),JSON.stringify(fixture.evidence));
  const file=path.join(fixture.repository,'native-runtime',fixture.sourceFields[field]);const prior=fs.readFileSync(file);fixture.write(file,'changed after guest build');
  assert.throws(()=>createReceipt(fixture.stage,fixture.bundle,fixture.repository),/native_bundle_source_stale/,field+' changed');fixture.write(file,prior);
 }
 const production=path.join(fixture.repository,'apple-music-downloader/internal/nativeauth/fixture.go');fixture.write(production,'changed production source');
 assert.throws(()=>createReceipt(fixture.stage,fixture.bundle,fixture.repository),/native_bundle_source_stale/,'old guest agent accepted');
});

test('native package rejects omitted, forged or extra guest-agent source evidence',t=>{
 const {createReceipt,hash}=require('../../electron/native-package');
 for(const mode of ['missing','hash','binary','omitted-source','extra-source']){
  const fixture=freshnessFixture(t);const file=path.join(fixture.bundle,'evidence/guest-agent-source.json');
  if(mode==='missing')fs.unlinkSync(file);
  else{
   if(mode==='binary')fixture.guestSource.binarySHA256='d'.repeat(64);
   if(mode==='omitted-source')delete fixture.guestSource.sources['internal/nativeauth/fixture.go'];
   if(mode==='extra-source')fixture.guestSource.sources['internal/nativeauth/removed.go']='d'.repeat(64);
   fixture.write(file,JSON.stringify(fixture.guestSource));if(mode!=='hash')fixture.evidence.guestAgentSourceSHA256=hash(file);else fixture.evidence.guestAgentSourceSHA256='0'.repeat(64);
   fixture.write(path.join(fixture.bundle,'evidence/build.json'),JSON.stringify(fixture.evidence));
  }
  assert.throws(()=>createReceipt(fixture.stage,fixture.bundle,fixture.repository),/native_bundle_source_stale/,mode);
 }
});

test('native installer requires explicit bundle selection and has no historical guest default',()=>{
 const {selectBundle}=require('../../electron/dist-native');
 assert.throws(()=>selectBundle({}),/native_bundle_selection_required/);assert.throws(()=>selectBundle({AMDL_NATIVE_BUNDLE:'  '}),/native_bundle_selection_required/);
 assert.equal(selectBundle({AMDL_NATIVE_BUNDLE:'new guest bundle'}),path.join(root,'new guest bundle'));
 assert.doesNotMatch(fs.readFileSync(path.join(root,'electron/dist-native.js'),'utf8'),/20261021-network-dev/);
});

test('installer audit accepts a directly extracted application payload',t=>{
 const os=require('node:os');const {selectPayload}=require('../../electron/audit-native-installer');const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-direct-payload-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 fs.mkdirSync(path.join(directory,'resources'));fs.writeFileSync(path.join(directory,'resources/app.asar'),'fixture');fs.writeFileSync(path.join(directory,'resources/native-package.json'),'{}');
 assert.deepEqual(selectPayload(directory),{layout:'embedded_7z',payload:directory,archive:null});
});

test('installer audit accepts exactly one nested archive and rejects a missing payload',t=>{
 const os=require('node:os');const {selectPayload}=require('../../electron/audit-native-installer');const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-nested-payload-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 assert.throws(()=>selectPayload(directory),/payload_missing/);fs.mkdirSync(path.join(directory,'plugin'));const archive=path.join(directory,'plugin/app-64.7z');fs.writeFileSync(archive,'fixture');
 assert.deepEqual(selectPayload(directory),{layout:'nested_7z',payload:null,archive});fs.mkdirSync(path.join(directory,'other'));fs.writeFileSync(path.join(directory,'other/app-64.7z'),'fixture');assert.throws(()=>selectPayload(directory),/payload_missing/);
});

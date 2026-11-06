'use strict';
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');
const {makeManifest}=require('./build-native-runtime');const {VERSION}=require('./native-version');
const ROOT=path.resolve(__dirname,'..');const ELECTRON='45.0.0';
const UI_FILES=['src/main/native-main.js','src/preload/native-preload.js','src/preload/native-startup-preload.js','src/renderer/native-startup.html','src/renderer/native-startup.js','src/renderer/native-startup.css','src/renderer/native-index.html','src/renderer/native-login.html','src/renderer/native-login.js','src/renderer/native-renderer.js','src/renderer/native-stream.js','src/renderer/native.css','src/renderer/native-shell.css','electron/native-cache.js','electron/native-service.js','electron/native-login.js','electron/native-startup.js','electron/native-version.js','electron/native-diagnostics.js'];
UI_FILES.push('src/renderer/native-motion.js','src/renderer/native-motion.css');
const hash=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const SOURCE_FIELDS={patchSHA256:'lite-native.patch',recipeSHA256:'Dockerfile',initSHA256:'init',assemblySHA256:'build-guest.sh',
 sessionPatchSHA256:'lite-session.patch',nativeLoginSHA256:'native_login.h',nativeServiceSHA256:'native_service.h',
 storeBridgeSHA256:'native_store_bridge.h',storeBridgePatchSHA256:'lite-store-bridge.patch',extractionToolsLockSHA256:'build-tools.lock.json'};
const AGENT_DIRECTORIES=['internal/nativeauth','internal/guestagent','cmd/amdl-guest'];

// A wrapper pin alone cannot distinguish an old guest from the current
// device-only guest. Compare its native and complete production Go inputs before
// allowing a new installer to combine it with today's backend and renderer.
function verifyBundleSources(bundle,repository=ROOT){
 try{
  const native=path.join(repository,'native-runtime');
  const lock=JSON.parse(fs.readFileSync(path.join(native,'inputs.lock.json'),'utf8'));
  const agentLock=JSON.parse(fs.readFileSync(path.join(native,'guest-agent.lock.json'),'utf8'));
  const build=JSON.parse(fs.readFileSync(path.join(bundle,'evidence/build.json'),'utf8'));
  if(build.schemaVersion!==1||build.publicReleaseAllowed!==false||build.wrapperCommit!==lock.wrapper.commit||build.sourceSHA256!==lock.wrapper.sha256||
   build.sourceDateEpoch!==lock.sourceDateEpoch||build.qemuInstallerSHA512!==lock.qemu.sha512||
   build.inputsLockSHA256!==hash(path.join(native,'inputs.lock.json'))||build.manifestSHA256!==hash(path.join(bundle,'bundle.json')))
   throw new Error();
  const toolchainFile=path.join(bundle,'evidence/toolchain-build.json');
  const toolchain=require('./native-toolchain').verifyEvidence(JSON.parse(fs.readFileSync(toolchainFile,'utf8')),repository);
  if(build.toolchainID!==toolchain.imageID||build.toolchainDefinitionSHA256!==toolchain.definitionSHA256||build.toolchainEvidenceSHA256!==hash(toolchainFile))throw new Error();
  for(const [field,file]of Object.entries(SOURCE_FIELDS))if(build[field]!==hash(path.join(native,file)))throw new Error();
  const sourceFile=path.join(bundle,'evidence/guest-agent-source.json');
  if(build.guestAgentSourceSHA256!==hash(sourceFile))throw new Error();
  const source=JSON.parse(fs.readFileSync(sourceFile,'utf8'));
  if(agentLock.schemaVersion!==1||agentLock.controlProtocol!==1||typeof source.goVersion!=='string'||!source.goVersion.startsWith('go version '+agentLock.goVersion+' ')||
   !/^[a-f0-9]{64}$/.test(build.guestAgentSHA256)||source.binarySHA256!==build.guestAgentSHA256||!source.sources||typeof source.sources!=='object'||Array.isArray(source.sources))throw new Error();
  const expected={};
  for(const directory of AGENT_DIRECTORIES){
   for(const name of fs.readdirSync(path.join(repository,'apple-music-downloader',directory)).sort()){
    if(!name.endsWith('.go')||name.endsWith('_test.go'))continue;
    const file=path.join(repository,'apple-music-downloader',directory,name);
    if(!fs.lstatSync(file).isFile())throw new Error();
    expected[directory+'/'+name]=hash(file);
   }
  }
  if(Object.keys(expected).length!==Object.keys(source.sources).length||Object.entries(expected).some(([file,digest])=>source.sources[file]!==digest))throw new Error();
  return build;
 }catch{throw new Error('native_bundle_source_stale');}
}
function walk(root,prefix=''){const files={};for(const name of fs.readdirSync(path.join(root,prefix)).sort()){const relative=(prefix?prefix+'/':'')+name;const file=path.join(root,relative);const stat=fs.lstatSync(file);if(stat.isSymbolicLink())throw new Error('native_package_link_rejected');if(stat.isDirectory())Object.assign(files,walk(root,relative));else if(stat.isFile())files[relative]={sha256:hash(file),size:stat.size};else throw new Error('native_package_file_invalid');}return files;}
function verifyReceipt(root,receipt){if(receipt.schemaVersion!==1||receipt.version!==VERSION||receipt.publicReleaseAllowed!==false||receipt.electronVersion!==ELECTRON||!receipt.files||!receipt.ui)throw new Error('native_package_receipt_invalid');for(const [relative,identity]of Object.entries(receipt.files)){if(path.posix.normalize(relative)!==relative||relative.startsWith('../')||path.isAbsolute(relative)||relative.includes('\\'))throw new Error('native_package_path_invalid');const file=path.join(root,relative);if(!fs.lstatSync(file).isFile()||fs.statSync(file).size!==identity.size||hash(file)!==identity.sha256)throw new Error('native_package_identity_mismatch');}return true;}
function createReceipt(stage,bundle,repository=ROOT){
 const manifest=JSON.parse(fs.readFileSync(path.join(bundle,'bundle.json'),'utf8'));const computed=makeManifest(bundle,{isolated:false});
 if(manifest.isolated!==false||manifest.controlProtocol!==1||JSON.stringify(manifest)!==JSON.stringify(computed))throw new Error('native_network_bundle_invalid');
 const build=JSON.parse(fs.readFileSync(path.join(bundle,'evidence/build.json'),'utf8'));const lock=JSON.parse(fs.readFileSync(path.join(repository,'native-runtime/inputs.lock.json'),'utf8'));
 if(build.publicReleaseAllowed!==false||build.wrapperCommit!==lock.wrapper.commit||build.sourceSHA256!==lock.wrapper.sha256)throw new Error('native_bundle_provenance_invalid');
 verifyBundleSources(bundle,repository);
 const runtime=path.join(repository,'node_modules/electron/dist');if(fs.readFileSync(path.join(runtime,'version'),'utf8').trim()!==ELECTRON)throw new Error('native_electron_identity_invalid');
 const ui=Object.fromEntries(UI_FILES.map(file=>[file,hash(path.join(repository,file))]));
 const files={...Object.fromEntries(Object.entries(walk(path.join(stage,'bin'))).map(([p,v])=>['bin/'+p,v])),...Object.fromEntries(Object.entries(walk(path.join(stage,'native-runtime'))).map(([p,v])=>['native-runtime/'+p,v]))};
 return {schemaVersion:1,version:VERSION,publicReleaseAllowed:false,sourceDateEpoch:lock.sourceDateEpoch,electronVersion:ELECTRON,electronOriginalEXESHA256:hash(path.join(runtime,'electron.exe')),runtimeManifestSHA256:hash(path.join(bundle,'bundle.json')),wrapperCommit:lock.wrapper.commit,files,ui};
}
function prepare(stage,bundle){if(fs.existsSync(stage))throw new Error('native_stage_must_be_new');fs.mkdirSync(path.join(stage,'bin'),{recursive:true});for(const name of ['amdl-service.exe','amdl-desktop.exe'])fs.copyFileSync(path.join(ROOT,'electron/bin',name),path.join(stage,'bin',name));
 // Copy explicitly selected immutable trees; never a repository/config/profile glob.
 for(const name of ['guest','qemu','notices','evidence']){const source=path.join(bundle,name);for(const relative of Object.keys(walk(source))){const target=path.join(stage,'native-runtime',name,relative);fs.mkdirSync(path.dirname(target),{recursive:true});fs.copyFileSync(path.join(source,relative),target);}}
 fs.copyFileSync(path.join(bundle,'bundle.json'),path.join(stage,'native-runtime/bundle.json'));const receipt=createReceipt(stage,bundle);fs.writeFileSync(path.join(stage,'native-package.json'),JSON.stringify(receipt,null,2)+'\n',{flag:'wx'});verifyStage(stage);return receipt;}
function verifyStage(stage){const receipt=JSON.parse(fs.readFileSync(path.join(stage,'native-package.json'),'utf8'));verifyReceipt(stage,receipt);verifyBundleSources(path.join(stage,'native-runtime'));for(const [file,digest]of Object.entries(receipt.ui))if(hash(path.join(ROOT,file))!==digest)throw new Error('native_ui_changed_after_staging');if(hash(path.join(ROOT,'node_modules/electron/dist/electron.exe'))!==receipt.electronOriginalEXESHA256)throw new Error('native_electron_changed_after_staging');return receipt;}
module.exports={createReceipt,verifyReceipt,verifyBundleSources,prepare,verifyStage,hash,walk,UI_FILES,ELECTRON,ROOT};

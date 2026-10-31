'use strict';
const fs=require('node:fs');const path=require('node:path');const asar=require('@electron/asar');const crypto=require('node:crypto');
const {verifyReceipt,hash,UI_FILES,ELECTRON}=require('./native-package');const {VERSION}=require('./native-version');
function readArchiveFile(archive,relative){return asar.extractFile(archive,path.join(...relative.split('/')));}
function verifyPackage(directory){const resources=path.join(directory,'resources');const receipt=JSON.parse(fs.readFileSync(path.join(resources,'native-package.json'),'utf8'));verifyReceipt(resources,receipt);const archive=path.join(resources,'app.asar');const entries=asar.listPackage(archive).map(name=>name.replace(/^[/\\]/,''));const allowed=new Set([...UI_FILES,'package.json']);
 for(const entry of entries){const stat=asar.statFile(archive,entry);if(stat.files)continue;if(!allowed.has(entry.replaceAll('\\','/')))throw new Error('native_archive_unexpected_file');}
 const pkg=JSON.parse(asar.extractFile(archive,'package.json'));if(pkg.version!==VERSION||pkg.main!=='src/main/native-main.js')throw new Error('native_archive_entry_invalid');
 for(const [file,digest]of Object.entries(receipt.ui))if(crypto.createHash('sha256').update(readArchiveFile(archive,file)).digest('hex')!==digest)throw new Error('native_archive_ui_mismatch');
 const manifest=JSON.parse(fs.readFileSync(path.join(resources,'native-runtime/bundle.json'),'utf8'));if(manifest.isolated!==false||manifest.controlProtocol!==1||hash(path.join(resources,'native-runtime/bundle.json'))!==receipt.runtimeManifestSHA256)throw new Error('native_packaged_runtime_invalid');
 for(const name of ['amdl-windows-probe.exe','amdl-wslinterop-bootstrap.exe','ffmpeg.exe','mp4decrypt.exe'])if(fs.existsSync(path.join(resources,'bin',name)))throw new Error('native_legacy_resource_found');
 if(['Apple Music Downloader 2.0.exe.sig','Apple Music Downloader 2.0 Test.exe.sig'].some(name=>fs.existsSync(path.join(directory,name)))||fs.existsSync(path.join(resources,'appliance')))throw new Error('native_legacy_shell_found');
 const policy=JSON.parse(fs.readFileSync(path.join(resources,'release-policy.json'),'utf8'));if(policy.publicReleaseAllowed!==false)throw new Error('native_legacy_policy_invalid');
 const native=JSON.parse(fs.readFileSync(path.join(resources,'native-release-policy.json'),'utf8'));
 if(native.schemaVersion!==1||typeof native.publicReleaseAllowed!=='boolean'||hash(path.join(resources,'native-release-policy.json'))!==hash(path.join(__dirname,'../native-runtime/release-policy.json')))throw new Error('native_release_policy_mismatch');
 return {passed:true,version:VERSION,electronVersion:ELECTRON,publicReleaseAllowed:native.publicReleaseAllowed,legacyPublicReleaseAllowed:false,archiveSHA256:hash(archive),backendSHA256:receipt.files['bin/amdl-service.exe'].sha256,runtimeManifestSHA256:receipt.runtimeManifestSHA256,resourceFiles:Object.keys(receipt.files).length};}
if(require.main===module){try{process.stdout.write(JSON.stringify(verifyPackage(path.resolve(process.argv[2])))+'\n');}catch(error){process.stderr.write(error.message+'\n');process.exitCode=1;}}
module.exports={verifyPackage,readArchiveFile};

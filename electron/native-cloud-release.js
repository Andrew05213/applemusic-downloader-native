'use strict';
// Explicit workflow_dispatch only. Never reads an account/profile or publishes
// automatically: the final operation may create a NEW draft, never replace one.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {ROOT,hash,copyTree,verifyBundleSources}=require('./native-package');
const {VERSION}=require('./native-version');
const {sourceIdentity,releaseRepository,ensureTagAvailable}=require('./native-release');
const {buildGuest,buildNativeRuntime,makeManifest}=require('./build-native-runtime');
const {sealGuestExport}=require('./native-cloud-guest');
function run(exe,args,options={}){
 const result=spawnSync(exe,args,{cwd:ROOT,env:process.env,shell:false,windowsHide:true,encoding:'utf8',maxBuffer:16*1024*1024,...options});
 if(result.error||result.status!==0)throw Error('native_cloud_command_failed: '+path.basename(exe));
 return result.stdout||'';
}
function loggedNode(args,file,env=process.env,timeout=600000){
 const fd=fs.openSync(file,'wx');
 try{run(process.execPath,args,{env,stdio:['ignore',fd,fd],timeout});}
 catch(error){process.stderr.write(fs.readFileSync(file,'utf8').slice(-12000)+'\n');throw error;}
 finally{fs.closeSync(fd);}
 return JSON.parse(fs.readFileSync(file,'utf8').trim().split(/\r?\n/).at(-1));
}
function fixtureBundle(bundle,destination){
 verifyBundleSources(bundle);copyTree(bundle,destination);
 fs.writeFileSync(path.join(destination,'bundle.json'),JSON.stringify(makeManifest(destination,{isolated:true}),null,2)+'\n');
 const file=path.join(destination,'evidence/build.json'),build=JSON.parse(fs.readFileSync(file));
 fs.writeFileSync(file,JSON.stringify({...build,manifestSHA256:hash(path.join(destination,'bundle.json'))},null,2)+'\n');
 verifyBundleSources(destination);return path.join(destination,'bundle.json');
}
function smoke(executable,bundle,log){
 // Short, disposable fixture checkout: production code stays inside the ASAR.
 const root=path.join(process.env.RUNNER_TEMP||path.join(ROOT,'.tmp-v2-native'),'amdl-smoke-'+crypto.randomUUID());
 for(const part of ['test/native','electron'])fs.mkdirSync(path.join(root,part),{recursive:true});
 for(const file of ['test/native/native-electron-live.cjs','electron/native-build-environment.js','electron/native-diagnostics.js','package.json','electron-builder.native.cjs'])fs.copyFileSync(path.join(ROOT,file),path.join(root,file));
 fs.symlinkSync(path.join(ROOT,'node_modules'),path.join(root,'node_modules'),'junction');
 const manifest=fixtureBundle(bundle,path.join(root,'bundle'));
 const env={...process.env};delete env.AMDL_STREAM_OUTPUT;
 const evidence=loggedNode([path.join(root,'electron/native-build-environment.js'),path.join(root,'test/native/native-electron-live.cjs'),executable,manifest,'--packaged'],log,env,240000);
 if(evidence.passed!==true||evidence.localVersion!==VERSION||evidence.packagedIdentityVerified!==true||evidence.realAccountsUsed!==0||evidence.mediaRequestsAttempted!==0)throw Error('native_cloud_smoke_failed');
 return evidence;
}
function linux(){
 if(process.platform!=='linux')throw Error('native_cloud_linux_required');
 const commit=sourceIdentity(),output=buildGuest({output:'.artifacts/cloud-guest'});
 const sha256=sealGuestExport(output,commit);
 if(process.env.GITHUB_OUTPUT)fs.appendFileSync(process.env.GITHUB_OUTPUT,'guest_receipt_sha256='+sha256+'\n');
 return {passed:true,commit,guestReceiptSHA256:sha256};
}
function windows(){
 if(process.platform!=='win32')throw Error('native_cloud_windows_required');
 if(!['true','false'].includes(process.env.AMDL_CREATE_DRAFT||'false'))throw Error('native_release_mode_invalid');
 const repo=releaseRepository(),commit=sourceIdentity(),tag='v'+VERSION,create=process.env.AMDL_CREATE_DRAFT==='true';
 if(create)ensureTagAvailable(repo,tag);
 const digest=process.env.AMDL_GUEST_RECEIPT_SHA256;
 const bundle=buildNativeRuntime({guestInput:path.join(ROOT,'.artifacts/cloud-guest'),commit,receiptSHA256:digest,isolated:false});
 const logDirectory=path.join(ROOT,'.artifacts/cloud-release-'+crypto.randomUUID());fs.mkdirSync(logDirectory);
 const built=loggedNode(['electron/dist-native.js'],path.join(logDirectory,'installer.log'),{...process.env,AMDL_NATIVE_BUNDLE:bundle});
 const installer=path.resolve(built.installer),relative=path.relative(path.join(ROOT,'.artifacts/installers'),installer);
 if(relative.startsWith('..')||path.isAbsolute(relative)||path.basename(installer)!=='AppleMusicDownloader-'+VERSION+'-Setup.exe'||hash(installer)!==built.installerSHA256||fs.readFileSync(installer+'.sha256','utf8')!==built.installerSHA256+'  '+path.basename(installer)+'\n')throw Error('native_cloud_installer_invalid');
 const audit=require('./audit-native-installer').audit(installer);
 if(audit.installerSHA256!==built.installerSHA256)throw Error('native_cloud_audit_failed');
 const evidence=smoke(path.join(built.output,'win-unpacked',require('../electron-builder.native.cjs').productName+'.exe'),bundle,path.join(logDirectory,'smoke.log'));
 const changelog=fs.readFileSync(path.join(ROOT,'CHANGELOG.md'),'utf8');
 const section=changelog.split('\n## '+VERSION+' — ')[1];if(!section)throw Error('native_cloud_notes_missing');
 const body=section.slice(section.indexOf('\n')+1).split('\n## ')[0].trim().replaceAll('(NOTICE)','(https://github.com/'+repo+'/blob/'+commit+'/NOTICE)');
 const notes=path.join(logDirectory,'release-notes.md');
 fs.writeFileSync(notes,'# Apple Music Downloader Native '+VERSION+'\n\n'+body+'\n\n## 本次云端构建验证\n\nLinux/Windows CI、源码编译、安装包解包审计和实际打包程序的隔离 UI 验证通过。自动化未使用真实 Apple 账号。\n\n源码：`'+commit+'`。\n\n[构建记录](https://github.com/'+repo+'/actions/runs/'+process.env.GITHUB_RUN_ID+')\n\n安装包 SHA-256：\n\n```text\n'+built.installerSHA256+'\n```\n',{flag:'wx'});
 const result={passed:true,version:VERSION,commit,guestReceiptSHA256:digest,installerSHA256:built.installerSHA256,installerBytes:fs.statSync(installer).size,checksumFileSHA256:hash(installer+'.sha256'),filesCompared:audit.filesCompared,packagedSmokePassed:evidence.passed,draftCreated:false};
 if(create){
  releaseRepository();if(sourceIdentity()!==commit)throw Error('native_release_source_changed');ensureTagAvailable(repo,tag);
  run('gh',['release','create',tag,installer,installer+'.sha256','--repo',repo,'--target',commit,'--draft','--prerelease','--latest=false','--title','Apple Music Downloader Native '+VERSION,'--notes-file',notes]);
  result.draftCreated=true;
 }
 fs.writeFileSync(path.join(ROOT,'.artifacts/cloud-release-result.json'),JSON.stringify(result,null,2)+'\n',{flag:'wx'});
 return result;
}
if(require.main===module){try{const [step,...extra]=process.argv.slice(2);if(extra.length||!['linux','windows'].includes(step))throw Error('native_cloud_arguments_invalid');console.log(JSON.stringify(step==='linux'?linux():windows()));}catch(error){console.error(error.message);process.exitCode=1;}}
module.exports={fixtureBundle};

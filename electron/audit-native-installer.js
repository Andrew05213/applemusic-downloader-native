'use strict';
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');const {spawnSync}=require('node:child_process');
const {ROOT,hash,walk}=require('./native-package');const {VERSION}=require('./native-version');const {verifyPackage}=require('./verify-native-package');
function selectPayload(directory){
 // 7za scans the embedded 7z directly when it has no NSIS handler.
 if(fs.existsSync(path.join(directory,'resources/app.asar'))&&fs.existsSync(path.join(directory,'resources/native-package.json')))return {layout:'embedded_7z',payload:directory,archive:null};
 const inner=Object.keys(walk(directory)).filter(name=>path.basename(name)==='app-64.7z');if(inner.length!==1)throw new Error('native_audit_payload_missing');
 return {layout:'nested_7z',payload:null,archive:path.join(directory,inner[0])};
}
function audit(installer){
 const selected=path.resolve(installer);const relative=path.relative(ROOT,selected);if(relative.startsWith('..')||path.isAbsolute(relative)||!fs.statSync(selected).isFile())throw new Error('native_audit_input_invalid');
 const directory=path.join(ROOT,'.tmp-v2-native','nsis-audit-'+crypto.randomUUID());fs.mkdirSync(directory,{recursive:true});
 const seven=path.join(ROOT,'node_modules/7zip-bin/win/x64/7za.exe');
 function run(tool,args,label){const result=spawnSync(tool,args,{encoding:'utf8',shell:false,windowsHide:true,maxBuffer:16*1024*1024});fs.writeFileSync(path.join(directory,label+'.txt'),(result.stdout||'')+'\n'+(result.stderr||''),{flag:'wx'});if(result.error||result.status!==0)throw new Error('native_audit_'+label+'_failed');}
 for(const command of ['l','t'])run(seven,[command,selected],'installer-'+command);
 const outer=path.join(directory,'outer');run(seven,['x','-y','-o'+outer,selected],'installer-x');
 const selection=selectPayload(outer);let payload=selection.payload;
 if(selection.archive){for(const command of ['l','t'])run(seven,[command,selection.archive],'payload-'+command);payload=path.join(directory,'payload');run(seven,['x','-y','-o'+payload,selection.archive],'payload-x');}
 const verified=verifyPackage(payload);const exe=require('../electron-builder.native.cjs').productName+'.exe';const staged=path.join(path.dirname(selected),'win-unpacked');
 const original=walk(staged);const extracted=walk(payload);for(const [file,identity]of Object.entries(original)){if(!extracted[file]||extracted[file].sha256!==identity.sha256||extracted[file].size!==identity.size)throw new Error('native_audit_payload_differs');}
 const evidence={...verified,version:VERSION,installerSHA256:hash(selected),applicationSHA256:hash(path.join(payload,exe)),filesCompared:Object.keys(original).length,auditDirectory:directory,payload,layout:selection.layout};fs.writeFileSync(path.join(directory,'evidence.json'),JSON.stringify(evidence,null,2)+'\n',{flag:'wx'});return evidence;
}
if(require.main===module){try{process.stdout.write(JSON.stringify(audit(process.argv[2]))+'\n');}catch(error){process.stderr.write(error.message+'\n');process.exitCode=1;}}
module.exports={audit,selectPayload};

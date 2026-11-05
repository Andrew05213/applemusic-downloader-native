'use strict';
// Creates reviewed-input candidates only; never a complete source-offer finding.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {ROOT,hash}=require('../electron/native-package');
const {readManifest,sourceIdentity,safeName}=require('./release-materials');
function readPlan(input,manifestFile){
 const manifest=readManifest(manifestFile),text=fs.readFileSync(path.join(input,'SOURCE-PLAN.tsv'),'utf8');
 const lines=text.trimEnd().split(/\r?\n/),header='# amdl-debian-source-plan-v1 installer='+manifest.installer.sha256+' manifest='+hash(manifestFile)+' snapshot='+manifest.debian.snapshot;
 if(lines.shift()!==header)throw Error('debian_source_plan_identity_mismatch');
 const expected=new Set(manifest.debian.binaries.flatMap(x=>x.sources.map(s=>s.name+'='+s.version))),seen=new Set(),records=[];
 for(const line of lines){const fields=line.split('\t');if(fields.length!==2)throw Error('debian_source_plan_invalid');const record=sourceIdentity(...fields),id=record.name+'='+record.version;if(!expected.has(id)||seen.has(record.name))throw Error('debian_source_plan_invalid');seen.add(record.name);records.push(record);}
 if(records.length!==expected.size)throw Error('debian_source_plan_incomplete');return {manifest,records};
}
function validateSource(dir,record){
 const dscs=fs.readdirSync(dir).filter(name=>name.endsWith('.dsc'));if(dscs.length!==1)throw Error('debian_source_dsc_invalid');
 const dsc=dscs[0],dscFile=path.join(dir,dsc),stat=fs.lstatSync(dscFile);if(!stat.isFile()||stat.isSymbolicLink())throw Error('debian_source_dsc_invalid');
 const text=fs.readFileSync(dscFile,'utf8');
 if(text.match(/^Source: (.+)$/m)?.[1]!==record.name||text.match(/^Version: (.+)$/m)?.[1]!==record.version)throw Error('debian_source_dsc_identity_mismatch');
 const section=text.match(/^Checksums-Sha256:\r?\n((?: [^\r\n]+\r?\n)+)/m);if(!section)throw Error('debian_source_dsc_invalid');
 const entries=section[1].trim().split(/\r?\n/).map(line=>line.trim().split(/\s+/)),seen=new Set();if(!entries.length)throw Error('debian_source_dsc_invalid');
 for(const entry of entries){
  if(entry.length!==3)throw Error('debian_source_dsc_invalid');const [digest,size,name]=entry;
  if(safeName(name)!==path.basename(name)||seen.has(name)||name===dsc||!/^[a-f0-9]{64}$/.test(digest)||!/^\d+$/.test(size)||!Number.isSafeInteger(Number(size)))throw Error('debian_source_dsc_invalid');seen.add(name);
  const file=path.join(dir,name),s=fs.lstatSync(file);if(!s.isFile()||s.isSymbolicLink()||s.size!==Number(size)||hash(file)!==digest)throw Error('debian_source_hash_mismatch');
 }
 return [dsc,...entries.map(x=>x[2])];
}
function packageSources(source,output,options={}){
 const input=path.resolve(source),target=path.resolve(output),relative=path.relative(path.join(ROOT,'.artifacts'),target);
 if(!relative||relative.startsWith('..')||path.isAbsolute(relative)||fs.existsSync(target)||fs.existsSync(target+'.sha256')||!target.endsWith('.zip'))throw Error('debian_source_output_invalid');
 if(!options.manifestFile||!options.sourceRoot)throw Error('debian_frozen_source_required');
 const {manifest,records}=readPlan(input,options.manifestFile),frozen=path.resolve(options.sourceRoot);
 // Read only selected historical files. Never silently copy today's build scripts.
 for(const [file,digest] of Object.entries(manifest.historicalBuildFiles)){
  safeName(file);const p=path.join(frozen,file),s=fs.lstatSync(p);if(!s.isFile()||s.isSymbolicLink()||hash(p)!==digest)throw Error('debian_historical_build_mismatch');
 }
 const selected=records.map(record=>({record,files:validateSource(path.join(input,record.name),record)}));
 const stage=path.join(ROOT,'.artifacts','source-package-'+crypto.randomUUID());fs.mkdirSync(stage,{recursive:true});
 for(const {record,files} of selected){const dir=path.join(stage,record.name);fs.mkdirSync(dir);for(const n of files)fs.copyFileSync(path.join(input,record.name,n),path.join(dir,n),fs.constants.COPYFILE_EXCL);}
 for(const file of Object.keys(manifest.historicalBuildFiles)){const p=path.join(stage,'project-build',file);fs.mkdirSync(path.dirname(p),{recursive:true});fs.copyFileSync(path.join(frozen,file),p,fs.constants.COPYFILE_EXCL);}
 fs.copyFileSync(path.join(input,'SOURCE-PLAN.tsv'),path.join(stage,'SOURCE-PLAN.tsv'));
 fs.writeFileSync(path.join(stage,'evidence.json'),JSON.stringify({schemaVersion:1,sourceCommit:manifest.sourceCommit,installerSHA256:manifest.installer.sha256,manifestSHA256:hash(options.manifestFile),sources:records,historicalBuildFiles:manifest.historicalBuildFiles,dscSignaturesVerified:false,rebuildCorrespondenceVerified:false,staticRelinkingMaterialsVerified:false,partial:true},null,2)+'\n',{flag:'wx'});
 fs.writeFileSync(path.join(stage,'README.txt'),'Partial Debian source candidates tied to the recorded installer hash. Includes Source/Built-Using source closure and hash-verified historical build files. Does not include QEMU or prove complete corresponding source, signatures, static-relinking sufficiency, authorization or delivery.\n',{flag:'wx'});
 const seven=path.join(ROOT,'node_modules/7zip-bin',process.platform==='win32'?'win/x64/7za.exe':'linux/x64/7za');
 const result=spawnSync(seven,['a','-tzip','-mx=0',target,...records.map(x=>x.name),'project-build','SOURCE-PLAN.tsv','evidence.json','README.txt'],{cwd:stage,encoding:'utf8',shell:false,windowsHide:true});
 if(result.error||result.status!==0)throw Error('debian_source_archive_failed');
 const digest=hash(target);fs.writeFileSync(target+'.sha256',digest+'  '+path.basename(target)+'\n',{flag:'wx'});
 return {archive:target,sha256:digest,partial:true};
}
if(require.main===module){try {if(process.argv.length!==6)throw Error('debian_source_arguments_invalid');console.log(JSON.stringify(packageSources(process.argv[2],process.argv[3],{manifestFile:process.argv[4],sourceRoot:process.argv[5]})));}catch(error){console.error(error.message);process.exitCode=1;}}
module.exports={packageSources,readPlan,validateSource};

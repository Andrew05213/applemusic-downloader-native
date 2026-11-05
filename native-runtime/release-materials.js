'use strict';
// Read-only evidence tools. No network, installation, source publication or legal clearance.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),zlib=require('node:zlib');
const {spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
const hash=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const bytesHash=data=>crypto.createHash('sha256').update(data).digest('hex');
function safeName(value){if(typeof value!=='string'||!value||/[\\:\x00-\x1f]/.test(value)||path.posix.normalize(value)!==value||value.startsWith('/')||value==='..'||value.startsWith('../')||/^[A-Za-z]:/.test(value))throw Error('materials_path_invalid');return value;}
function readManifest(file){
 const m=JSON.parse(fs.readFileSync(file,'utf8'));
 if(m.schemaVersion!==1||!/^[a-f0-9]{40}$/.test(m.sourceCommit)||!/^[a-f0-9]{64}$/.test(m.installer?.sha256)||!m.files||!m.debian?.binaries?.length||m.reviewComplete!==false)throw Error('materials_manifest_invalid');
 for(const [name,v] of Object.entries(m.files)){safeName(name);if(!Number.isSafeInteger(v.size)||v.size<0||!/^[a-f0-9]{64}$/.test(v.sha256))throw Error('materials_manifest_invalid');}
 return m;
}
function outputPath(file){const p=path.resolve(file),rel=path.relative(path.join(ROOT,'.artifacts'),p);if(!rel||rel.startsWith('..')||path.isAbsolute(rel)||fs.existsSync(p))throw Error('materials_output_invalid');return p;}
function writeNew(file,data){fs.mkdirSync(path.dirname(file),{recursive:true});fs.writeFileSync(file,data,{flag:'wx'});}
function fields(text){
 const result={};let key;
 for(const line of text.split(/\r?\n/)){
  if(!line)continue;
  if(/^[ \t]/.test(line)){if(!key)throw Error('debian_control_invalid');result[key]+='\n'+line.trim();continue;}
  const m=line.match(/^([A-Za-z][A-Za-z0-9-]*): (.*)$/);if(!m||Object.hasOwn(result,m[1]))throw Error('debian_control_invalid');key=m[1];result[key]=m[2];
 }
 return result;
}
function sourceIdentity(name,version){if(!/^[a-z0-9][a-z0-9+.-]*$/.test(name)||!/^\d[0-9A-Za-z.+:~\-]*$/.test(version))throw Error('debian_source_identity_invalid');return {name,version};}
function debianSources(control){
 const f=fields(control);if(!f.Package||!f.Version||!f.Architecture)throw Error('debian_control_invalid');
 const match=(f.Source||f.Package).match(/^([a-z0-9][a-z0-9+.-]*)(?: \(([^)]+)\))?$/);if(!match)throw Error('debian_source_identity_invalid');
 const sources=[{...sourceIdentity(match[1],match[2]||f.Version),reason:'Source'}];
 for(const field of ['Built-Using','Static-Built-Using']){
  if(!f[field])continue;
  for(const item of f[field].split(',')){const m=item.trim().match(/^([a-z0-9][a-z0-9+.-]*)\s+\(=\s*([^)]+)\)$/);if(!m)throw Error('debian_source_identity_invalid');sources.push({...sourceIdentity(m[1],m[2]),reason:field});}
 }
 return {package:f.Package,version:f.Version,architecture:f.Architecture,sources};
}
function planDebian(manifestFile,binaries,output){
 const m=readManifest(manifestFile),target=outputPath(output),tsv=outputPath(output+'.tsv');
 if(binaries.length!==m.debian.binaries.length)throw Error('debian_binary_selection_incomplete');
 const expected=new Map(m.debian.binaries.map(x=>[x.sha256,x])),seen=new Set(),records=[],sources=new Map();
 for(const binary of binaries){
  if(!fs.lstatSync(binary).isFile())throw Error('debian_binary_invalid');
  const digest=hash(binary),spec=expected.get(digest);if(!spec||seen.has(digest))throw Error('debian_binary_identity_mismatch');seen.add(digest);
  const r=spawnSync('dpkg-deb',['--field',path.resolve(binary)],{encoding:'utf8',shell:false,maxBuffer:1024*1024});if(r.error||r.status!==0)throw Error('debian_metadata_read_failed');
  const info=debianSources(r.stdout);
  if(info.package!==spec.package||info.version!==spec.version||info.architecture!==spec.architecture||JSON.stringify(info.sources)!==JSON.stringify(spec.sources))throw Error('debian_metadata_identity_mismatch');
  records.push({sha256:digest,...info});
  for(const s of info.sources){const id=s.name+'='+s.version;if(!sources.has(id))sources.set(id,{name:s.name,version:s.version,evidence:[]});sources.get(id).evidence.push({binarySHA256:digest,reason:s.reason});}
 }
 const result={schemaVersion:1,sourceCommit:m.sourceCommit,installerSHA256:m.installer.sha256,manifestSHA256:hash(manifestFile),snapshot:m.debian.snapshot,binaries:records,sources:[...sources.values()].sort((a,b)=>(a.name+'='+a.version).localeCompare(b.name+'='+b.version)),signaturesVerified:false,incomplete:true};
 writeNew(target,JSON.stringify(result,null,2)+'\n');
 writeNew(tsv,'# amdl-debian-source-plan-v1 installer='+result.installerSHA256+' manifest='+result.manifestSHA256+' snapshot='+result.snapshot+'\n'+result.sources.map(x=>x.name+'\t'+x.version).join('\n')+'\n');
 return {output:target,plan:tsv,sources:result.sources.length,incomplete:true};
}
function regularFiles(root,prefix=''){
 const result={};for(const e of fs.readdirSync(path.join(root,prefix),{withFileTypes:true})){const n=prefix?prefix+'/'+e.name:e.name;safeName(n);const p=path.join(root,n);if(e.isSymbolicLink())throw Error('materials_link_rejected');if(e.isDirectory())Object.assign(result,regularFiles(root,n));else if(e.isFile()){const bytes=fs.readFileSync(p);result[n]={size:bytes.length,sha256:bytesHash(bytes)};}else throw Error('materials_file_invalid');}return result;
}
function asarFiles(file){
 const fd=fs.openSync(file,'r');try{const b=Buffer.alloc(16);if(fs.readSync(fd,b,0,16,0)!==16)throw Error('materials_asar_invalid');const len=b.readUInt32LE(12);if(len>16*1024*1024)throw Error('materials_asar_invalid');const data=Buffer.alloc(len);if(fs.readSync(fd,data,0,len,16)!==len)throw Error('materials_asar_invalid');const h=JSON.parse(data.toString()),names=[];
 function visit(n,prefix=''){for(const [key,v] of Object.entries(n.files||{})){const name=safeName(prefix+key);if(v.files)visit(v,name+'/');else{if(v.link)throw Error('materials_link_rejected');names.push(name);}}}visit(h);return names.sort();}finally{fs.closeSync(fd);}
}
function guestFiles(file){
 const data=zlib.gunzipSync(fs.readFileSync(file),{maxOutputLength:512*1024*1024}),files={},seen=new Set();let pos=0,deferred=0,ended=false;
 const align=n=>Math.ceil(n/4)*4;
 while(pos+110<=data.length){
  const header=data.subarray(pos,pos+110).toString('ascii');if(!/^07070[12][0-9a-fA-F]{104}$/.test(header))throw Error('materials_cpio_invalid');const f=Array.from({length:13},(_,i)=>parseInt(header.slice(6+i*8,14+i*8),16)),size=f[6],ns=f[11];if(ns<1||pos+110+ns>data.length||data[pos+110+ns-1]!==0)throw Error('materials_cpio_invalid');
  let name=data.subarray(pos+110,pos+110+ns-1).toString();const start=align(pos+110+ns);if(start+size>data.length)throw Error('materials_cpio_invalid');const bytes=data.subarray(start,start+size);pos=align(start+size);if(name==='TRAILER!!!'){ended=true;break;}name=name.replace(/^\.\//,'');if(name==='.')continue;safeName(name);if(seen.has(name))throw Error('materials_cpio_duplicate');seen.add(name);
  if(name==='system'||name.startsWith('system/')){deferred++;continue;}if((f[1]&0o170000)===0o100000)files[name]={size,sha256:bytesHash(bytes)};
 }
 if(!ended)throw Error('materials_cpio_invalid');return {files,deferredSystemEntries:deferred,deferredReason:'android-apple-materials-out-of-scope-by-user'};
}
function inventoryRelease(manifestFile,installer,extracted,output){
 const m=readManifest(manifestFile),target=outputPath(output);if(fs.statSync(installer).size!==m.installer.size||hash(installer)!==m.installer.sha256)throw Error('materials_installer_mismatch');
 const files=regularFiles(extracted);if(Object.keys(files).length!==Object.keys(m.files).length||Object.entries(m.files).some(([p,v])=>files[p]?.size!==v.size||files[p]?.sha256!==v.sha256))throw Error('materials_payload_mismatch');
 const guest=guestFiles(path.join(extracted,'resources/native-runtime/guest/initramfs.cpio.gz'));
 for(const [p,v] of Object.entries(m.guestFiles))if(guest.files[p]?.sha256!==v.sha256||guest.files[p]?.size!==v.size)throw Error('materials_guest_mismatch');
 const asars={};for(const name of ['app.asar','default_app.asar']){const names=asarFiles(path.join(extracted,'resources',name));asars[name]={files:names,independentNpmPaths:names.filter(p=>p.startsWith('node_modules/')||p.includes('/node_modules/')),embeddedDependenciesReviewed:false};}
 const result={schemaVersion:1,sourceCommit:m.sourceCommit,installerSHA256:m.installer.sha256,manifestSHA256:hash(manifestFile),files,guest,asars,reviewComplete:false,incomplete:true,note:'Hashes establish identities only; source correspondence and authorization remain separate.'};writeNew(target,JSON.stringify(result,null,2)+'\n');return {output:target,files:Object.keys(files).length,incomplete:true};
}
if(require.main===module){try{const [action,...args]=process.argv.slice(2);let result;if(action==='debian-plan'&&args.length>=4){const [manifest,output,...bins]=args;result=planDebian(manifest,bins,output);}else if(action==='inventory'&&args.length===4)result=inventoryRelease(...args);else throw Error('materials_arguments_invalid');console.log(JSON.stringify(result));}catch(e){console.error(e.message);process.exitCode=1;}}
module.exports={safeName,readManifest,outputPath,fields,debianSources,sourceIdentity,planDebian,regularFiles,asarFiles,guestFiles,inventoryRelease};

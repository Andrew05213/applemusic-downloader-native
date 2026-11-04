'use strict';
// Optional frozen-reference download. Never used by Native setup/dev/package.
// Never executes archived files or restores anything into the source tree.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto');
const {spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
function hash(file){const fd=fs.openSync(file,'r'),h=crypto.createHash('sha256');try{const b=Buffer.allocUnsafe(1024*1024);let n;while((n=fs.readSync(fd,b,0,b.length,null))>0)h.update(b.subarray(0,n));return h.digest('hex');}finally{fs.closeSync(fd);}}
function definition(root=ROOT){
 const lock=JSON.parse(fs.readFileSync(path.join(root,'legacy/v1/binary-assets.lock.json'),'utf8'));
 if(lock.schemaVersion!==1||lock.repository!=='Andrew05213/applemusic-downloader-native'||!/^legacy-binaries-\d{8}$/.test(lock.tag)||!/^legacy-wrapper-rootfs-system-\d{8}\.zip$/.test(lock.asset)||!/^[a-f0-9]{64}$/.test(lock.sha256)||lock.manifest!=='legacy/v1/phase2-manifest.json'||lock.prefix!=='legacy/v1/wrapper-linux/rootfs/system/'||!Number.isSafeInteger(lock.size)||lock.size<1||lock.size>256*1024*1024||!Number.isSafeInteger(lock.expandedSize)||lock.expandedSize<1||lock.expandedSize>512*1024*1024)throw Error('legacy_binary_lock_invalid');
 if(hash(path.join(root,lock.manifest))!==lock.manifestSHA256)throw Error('legacy_binary_manifest_changed');
 const files=JSON.parse(fs.readFileSync(path.join(root,lock.manifest))).files.filter(f=>f.path.startsWith(lock.prefix));
 const names=new Set();for(const f of files){if(path.posix.normalize(f.path)!==f.path||f.path.includes('\\')||/[\x00-\x1f:*?"<>|]/.test(f.path)||! /^[a-f0-9]{64}$/.test(f.sha256)||names.has(f.path.toLowerCase()))throw Error('legacy_binary_manifest_invalid');names.add(f.path.toLowerCase());}
 if(files.length!==lock.fileCount||!files.length)throw Error('legacy_binary_manifest_invalid');
 return {lock,files};
}
function verifyArchive(archive,def){if(!fs.lstatSync(archive).isFile()||fs.statSync(archive).size!==def.lock.size||hash(archive)!==def.lock.sha256)throw Error('legacy_binary_archive_identity_mismatch');}
function verifyListing(text,def){
 const sections=text.split(/\r?\n----------\r?\n/);if(sections.length!==2)throw Error('legacy_binary_listing_invalid');
 const allowed=new Set(def.files.map(f=>f.path)),dirs=new Set(),seen=new Set();let bytes=0,count=0;
 for(const file of allowed){let dir=path.posix.dirname(file);while(dir!=='.'){dirs.add(dir);dir=path.posix.dirname(dir);}}
 for(const block of sections[1].trim().split(/\r?\n\r?\n/)){
  const entry=Object.fromEntries(block.split(/\r?\n/).map(line=>{const i=line.indexOf(' = ');return i<0?[]:[line.slice(0,i),line.slice(i+3)];}).filter(p=>p.length));
  const name=(entry.Path||'').replaceAll('\\','/'),isDir=entry.Folder==='+';const size=Number(entry.Size);
  if(!(isDir?dirs:allowed).has(name)||seen.has(name.toLowerCase())||entry.Encrypted==='+'||Object.keys(entry).some(k=>/link/i.test(k))||/(?:^|\s)l[rwx-]{9}/.test(entry.Attributes||'')||!Number.isSafeInteger(size)||size<0||(isDir&&size!==0))throw Error('legacy_binary_listing_invalid');
  seen.add(name.toLowerCase());if(!isDir){bytes+=size;count++;}
 }
 if(count!==def.lock.fileCount||bytes!==def.lock.expandedSize)throw Error('legacy_binary_listing_invalid');return count;
}
function verifyTree(directory,def){
 const actual=new Set();function visit(rel){for(const name of fs.readdirSync(path.join(directory,rel))){const child=path.posix.join(rel,name),file=path.join(directory,child),stat=fs.lstatSync(file);if(stat.isSymbolicLink())throw Error('legacy_binary_tree_invalid');if(stat.isDirectory())visit(child);else if(stat.isFile())actual.add(child);else throw Error('legacy_binary_tree_invalid');}}visit('');
 if(actual.size!==def.files.length)throw Error('legacy_binary_tree_invalid');let bytes=0;
 for(const f of def.files){if(!actual.has(f.path)||hash(path.join(directory,f.path))!==f.sha256)throw Error('legacy_binary_tree_invalid');bytes+=fs.statSync(path.join(directory,f.path)).size;}
 if(bytes!==def.lock.expandedSize)throw Error('legacy_binary_tree_invalid');return {files:actual.size,bytes};
}
function run(exe,args){const r=spawnSync(exe,args,{shell:false,windowsHide:true,encoding:'utf8',maxBuffer:8*1024*1024});if(r.error||r.status!==0)throw Error('legacy_binary_command_failed');return r.stdout;}
function extract(archive,output,def,seven,runner=run){
 verifyArchive(archive,def);if(fs.existsSync(output))throw Error('legacy_binary_output_exists');
 verifyListing(runner(seven,['l','-slt',archive]),def);
 runner(seven,['x','-y','-o'+output,archive]);return verifyTree(output,def);
}
function fetch(root=ROOT){
 const def=definition(root),base=path.join(root,'.artifacts/legacy-binaries',crypto.randomUUID());fs.mkdirSync(base,{recursive:true});
 const archive=path.join(base,def.lock.asset),url='https://github.com/'+def.lock.repository+'/releases/download/'+def.lock.tag+'/'+def.lock.asset;
 run(process.platform==='win32'?'curl.exe':'curl',['--fail','--location','--proto','=https','--proto-redir','=https','--retry','2','--connect-timeout','20','--max-time','1800','--output',archive,url]);
 const output=path.join(base,'unpacked'),result=extract(archive,output,def,require('7zip-bin').path7za);
 return {...result,directory:path.join(output,def.lock.prefix),archiveSHA256:def.lock.sha256};
}
if(require.main===module){try{if(process.argv.length!==3||process.argv[2]!=='fetch')throw Error('usage: node scripts/legacy-binaries.js fetch');console.log(JSON.stringify(fetch()));}catch(e){console.error(e.message);process.exitCode=1;}}
module.exports={definition,verifyArchive,verifyListing,verifyTree,extract,fetch};

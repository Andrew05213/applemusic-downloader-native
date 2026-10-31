'use strict';
// Creates a *partial* Debian source archive, not a complete installer source offer.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto');
const {spawnSync}=require('node:child_process');
const {ROOT,hash}=require('../electron/native-package');
const components={linux:{dsc:'linux_6.1.177-1.dsc',files:['linux_6.1.177.orig.tar.xz','linux_6.1.177-1.debian.tar.xz']},busybox:{dsc:'busybox_1.35.0-4+deb12u1.dsc',files:['busybox_1.35.0.orig.tar.bz2','busybox_1.35.0-4+deb12u1.debian.tar.xz']}};
function packageSources(source,output){
 const input=path.resolve(source),target=path.resolve(output),relative=path.relative(path.join(ROOT,'.artifacts'),target);
 if(!relative||relative.startsWith('..')||path.isAbsolute(relative)||fs.existsSync(target)||fs.existsSync(target+'.sha256')||!target.endsWith('.zip'))throw Error('debian_source_output_invalid');
 const stage=path.join(ROOT,'.artifacts','source-package-'+crypto.randomUUID());fs.mkdirSync(stage,{recursive:true});
 for(const [component,spec] of Object.entries(components)){
  const dir=path.join(input,component),dsc=path.join(dir,spec.dsc);
  const text=fs.readFileSync(dsc,'utf8'), section=text.match(/^Checksums-Sha256:\r?\n((?: [^\r\n]+\r?\n)+)/m);
  if(!section)throw Error('debian_source_dsc_invalid');
  const entries=section[1].trim().split(/\r?\n/).map(line=>line.trim().split(/\s+/));
  if(entries.length!==spec.files.length)throw Error('debian_source_dsc_invalid');
  fs.mkdirSync(path.join(stage,component));
  for(const [digest,size,name] of entries){
   if(!spec.files.includes(name)||!/^[a-f0-9]{64}$/.test(digest)||!/^\d+$/.test(size))throw Error('debian_source_dsc_invalid');
   const file=path.join(dir,name),stat=fs.lstatSync(file);
   if(!stat.isFile()||stat.isSymbolicLink()||stat.size!==Number(size)||hash(file)!==digest)throw Error('debian_source_hash_mismatch');
   fs.copyFileSync(file,path.join(stage,component,name),fs.constants.COPYFILE_EXCL);
  }
  fs.copyFileSync(dsc,path.join(stage,component,spec.dsc),fs.constants.COPYFILE_EXCL);
 }
 fs.mkdirSync(path.join(stage,'project-build'));
 for(const file of ['inputs.lock.json','Dockerfile','build-guest.sh','init','collect-debian-sources.sh'])fs.copyFileSync(path.join(ROOT,'native-runtime',file),path.join(stage,'project-build',file),fs.constants.COPYFILE_EXCL);
 fs.writeFileSync(path.join(stage,'README.txt'),'Partial Debian Linux and BusyBox source archives and Debian patches. These files do not contain QEMU or establish a complete source offer for the installer. Verify signatures, binary correspondence and all other applicable obligations before distribution.\n',{flag:'wx'});
 const seven=path.join(ROOT,'node_modules/7zip-bin',process.platform==='win32'?'win/x64/7za.exe':'linux/x64/7za');
 const result=spawnSync(seven,['a','-tzip','-mx=0',target,'linux','busybox','project-build','README.txt'],{cwd:stage,encoding:'utf8',shell:false,windowsHide:true});
 if(result.error||result.status!==0)throw Error('debian_source_archive_failed');
 const digest=hash(target);fs.writeFileSync(target+'.sha256',digest+'  '+path.basename(target)+'\n',{flag:'wx'});
 return {archive:target,sha256:digest,partial:true};
}
if(require.main===module){try {if(process.argv.length!==4)throw Error('debian_source_arguments_invalid');console.log(JSON.stringify(packageSources(process.argv[2],process.argv[3])));}catch(error){console.error(error.message);process.exitCode=1;}}
module.exports={packageSources};

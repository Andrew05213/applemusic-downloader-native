'use strict';
// Extract a pinned official 7-Zip package; never execute its installer or use
// a system installation. QEMU's NSIS format needs full 7z, not npm's 7za.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),{spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
const hash=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
function verify(directory,lock){
 if(!lock?.files||Object.keys(lock.files).sort().join(',')!=='7z.dll,7z.exe,License.txt'||Object.values(lock.files).some(value=>!/^[a-f0-9]{64}$/.test(value)))throw Error('native_archive_tool_identity_mismatch');
 for(const [name,digest]of Object.entries(lock.files)){
  const file=path.join(directory,name);
  if(!fs.lstatSync(file).isFile()||hash(file)!==digest)throw Error('native_archive_tool_identity_mismatch');
 }
 return path.join(directory,'7z.exe');
}
function prepare(root=ROOT){
 const lock=JSON.parse(fs.readFileSync(path.join(root,'native-runtime/build-tools.lock.json'),'utf8')).sevenZip;
 if(!/^[a-f0-9]{64}$/.test(lock.sha256)||!/^https:\/\/github\.com\/ip7z\/7zip\/releases\/download\/[\d.]+\/7z[\w.-]+\.exe$/.test(lock.url))throw Error('native_archive_tool_identity_mismatch');
 const base=path.join(root,'.tmp-v2-native/build-tools'),output=path.join(base,'7zip-'+lock.sha256);
 if(fs.existsSync(output))return verify(output,lock);
 fs.mkdirSync(base,{recursive:true});
 function run(exe,args){const result=spawnSync(exe,args,{cwd:root,shell:false,windowsHide:true,stdio:'inherit'});if(result.error||result.status!==0)throw Error('native_archive_tool_prepare_failed');}
 const archive=path.join(base,'7zip-'+lock.sha256+'.exe');
 if(!fs.existsSync(archive)){
  const temporary=archive+'.'+crypto.randomUUID()+'.partial';
  run('curl.exe',['--fail','--location','--retry','2','--connect-timeout','20','--max-time','600','--output',temporary,lock.url]);
  if(hash(temporary)!==lock.sha256)throw Error('native_archive_tool_identity_mismatch');
  fs.renameSync(temporary,archive);
 }
 if(hash(archive)!==lock.sha256)throw Error('native_archive_tool_identity_mismatch');
 const extracted=path.join(base,'unpack-'+crypto.randomUUID());
 run(path.join(root,'node_modules/7zip-bin/win/x64/7za.exe'),['x','-y','-o'+path.relative(root,extracted),path.relative(root,archive),'7z.exe','7z.dll','License.txt']);
 verify(extracted,lock);fs.mkdirSync(output);
 for(const name of Object.keys(lock.files))fs.copyFileSync(path.join(extracted,name),path.join(output,name),fs.constants.COPYFILE_EXCL);
 return verify(output,lock);
}
module.exports={prepare,verify};

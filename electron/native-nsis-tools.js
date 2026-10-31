'use strict';
// Prefetch the exact artifacts selected by the locked electron-builder.
// Retry transport failures, but never replace a mismatching cache silently.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),{spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
const SPECS=[
 {name:'nsis-3.0.4.1',source:'nsisUtil.js',sha512:'VKMiizYdmNdJOWpRGz4trl4lD++BvYP2irAXpMilheUP0pc93iKlWAoP843Vlraj8YG19CVn0j+dCo/hURz9+Q=='},
 {name:'nsis-resources-3.4.1',source:'NsisTarget.js',sha512:'Dqd6g+2buwwvoG1Vyf6BHR1b+25QMmPcwZx40atOT57gH27rkjOei1L0JTldxZu4NFoEmW4kJgZ3DlSWVON3+Q=='}
];
const hash=(file,algorithm='sha256',encoding='hex')=>crypto.createHash(algorithm).update(fs.readFileSync(file)).digest(encoding);
function tree(directory,prefix=''){
 const result={};
 for(const name of fs.readdirSync(path.join(directory,prefix)).sort()){
  const relative=prefix?prefix+'/'+name:name,file=path.join(directory,relative),stat=fs.lstatSync(file);
  if(stat.isSymbolicLink())throw Error('native_nsis_cache_invalid');
  if(stat.isDirectory())Object.assign(result,tree(directory,relative));
  else if(stat.isFile())result[relative]=hash(file);
  else throw Error('native_nsis_cache_invalid');
 }
 return result;
}
function verifyCache(expected,directory){
 const actual=tree(directory);
 if(JSON.stringify(actual)!==JSON.stringify(expected))throw Error('native_nsis_cache_invalid');
}
function prepare(root=ROOT){
 const cache=path.join(root,'.tmp-v2-native/builder-cache/nsis');fs.mkdirSync(cache,{recursive:true});
 const library=path.dirname(require.resolve('app-builder-lib/package.json',{paths:[root]}));
 const directories={};
 function run(executable,args){const result=spawnSync(executable,args,{cwd:root,shell:false,windowsHide:true,stdio:'inherit'});if(result.error||result.status!==0)throw Error('native_nsis_prepare_failed');}
 for(const spec of SPECS){
  const source=fs.readFileSync(path.join(library,'out/targets/nsis',spec.source),'utf8');
  if(!source.includes(spec.sha512))throw Error('native_nsis_builder_version_mismatch');
  const archive=path.join(cache,spec.name+'.7z');
  if(!fs.existsSync(archive)){
   const partial=archive+'.'+crypto.randomUUID()+'.partial';
   const url='https://github.com/electron-userland/electron-builder-binaries/releases/download/'+spec.name+'/'+spec.name+'.7z';
   run('curl.exe',['--fail','--location','--retry','3','--connect-timeout','20','--max-time','600','--output',partial,url]);
   if(hash(partial,'sha512','base64')!==spec.sha512)throw Error('native_nsis_identity_mismatch');fs.renameSync(partial,archive);
  }
  if(hash(archive,'sha512','base64')!==spec.sha512)throw Error('native_nsis_identity_mismatch');
  const stage=path.join(cache,'extract-'+crypto.randomUUID());
  run(path.join(root,'node_modules/7zip-bin/win/x64/7za.exe'),['x','-y',path.relative(root,archive),'-o'+path.relative(root,stage)]);
  const expected=tree(stage),target=path.join(cache,spec.name);
  if(fs.existsSync(target))verifyCache(expected,target);else fs.renameSync(stage,target);
  directories[spec.name]=target;
 }
 return directories;
}
module.exports={prepare,verifyCache,tree,SPECS};

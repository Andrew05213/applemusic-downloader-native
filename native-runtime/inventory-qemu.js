'use strict';
// A file/hash inventory is evidence for review, not an authorization finding.
const fs=require('node:fs'),path=require('node:path');
const {walk,hash,verifyBundleSources,ROOT}=require('../electron/native-package');
const {makeManifest}=require('../electron/build-native-runtime');
function inventory(bundle,output){
 const selected=path.resolve(bundle),target=path.resolve(output),relative=path.relative(path.join(ROOT,'.artifacts'),target);
 if(!relative||relative.startsWith('..')||path.isAbsolute(relative)||fs.existsSync(target))throw Error('qemu_inventory_output_invalid');
 verifyBundleSources(selected);
 const manifest=JSON.parse(fs.readFileSync(path.join(selected,'bundle.json'),'utf8'));
 if(manifest.isolated!==false||JSON.stringify(manifest)!==JSON.stringify(makeManifest(selected,{isolated:false})))throw Error('qemu_inventory_bundle_invalid');
 const files=walk(path.join(selected,'qemu'));
 const materials=Object.entries(files).map(([file,identity])=>({path:file,...identity,
  category:file.startsWith('share/')?'firmware-or-support':file.toLowerCase().endsWith('.dll')?'dll':'executable-or-support',
  upstreamArchivePath:file,correspondingSourceStatus:'unresolved',buildMaterialStatus:'unresolved',licenseMappingStatus:'unreviewed'}));
 const result={schemaVersion:1,manifestSHA256:hash(path.join(selected,'bundle.json')),qemuInstallerSHA512:JSON.parse(fs.readFileSync(path.join(selected,'evidence/build.json'),'utf8')).qemuInstallerSHA512,
  files,materials,incomplete:true,authorizationReviewComplete:false,notice:'File presence and hashes do not establish source correspondence or redistribution rights. All distributed files need mapping, including unused DLLs and firmware.'};
 fs.mkdirSync(path.dirname(target),{recursive:true});fs.writeFileSync(target,JSON.stringify(result,null,2)+'\n',{flag:'wx'});
 return {output:target,files:Object.keys(files).length,authorizationReviewComplete:false};
}
if(require.main===module){try {if(process.argv.length!==4)throw Error('qemu_inventory_arguments_invalid');console.log(JSON.stringify(inventory(process.argv[2],process.argv[3])));}catch(error){console.error(error.message);process.exitCode=1;}}
module.exports={inventory};

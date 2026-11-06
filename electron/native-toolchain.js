'use strict';
// A local build receipt binds an image ID to the checked-in public recipe.
// It is an integrity/freshness record, not a signed remote attestation.
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto');
const {spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
const FILES=['inputs.lock.json','Dockerfile','install.py','extract-ndk.py','fetch-snapshot-packages.py'];
const sha=value=>crypto.createHash('sha256').update(value).digest('hex');
function definition(root=ROOT){
 const directory=path.join(root,'native-runtime/toolchain');
 const files=Object.fromEntries(FILES.map(name=>{const file=path.join(directory,name);if(!fs.lstatSync(file).isFile())throw Error('native_toolchain_source_invalid');return [name,sha(fs.readFileSync(file))];}));
 const lock=JSON.parse(fs.readFileSync(path.join(directory,'inputs.lock.json'),'utf8'));
 if(lock.schemaVersion!==1||lock.platform!=='linux/amd64'||!/^docker\.io\/library\/[\w.:-]+@sha256:[a-f0-9]{64}$/.test(lock.baseImage)||
    !fs.readFileSync(path.join(directory,'Dockerfile'),'utf8').includes('FROM '+lock.baseImage)||
    !/^[0-9]{8}T[0-9]{6}Z$/.test(lock.snapshot))throw Error('native_toolchain_source_invalid');
 const inputs=JSON.parse(fs.readFileSync(path.join(root,'native-runtime/inputs.lock.json'),'utf8'));
 if(inputs.toolchain.definition!=='native-runtime/toolchain/inputs.lock.json'||inputs.toolchain.ndk!==lock.ndk.revision||inputs.toolchain.cmake!==lock.cmake.version||
    inputs.toolchain.cjson!==lock.cjson.commit||inputs.toolchain.dobby!==lock.dobby.commit||inputs.debianSnapshot!==lock.snapshot||
    JSON.stringify(inputs.packages)!==JSON.stringify(lock.packages)||inputs.sourceDateEpoch!==lock.sourceDateEpoch)throw Error('native_toolchain_source_invalid');
 const recipe=fs.readFileSync(path.join(directory,'Dockerfile'),'utf8');
 if(!recipe.includes(lock.snapshot)||!recipe.includes('ARG SOURCE_DATE_EPOCH='+lock.sourceDateEpoch)||Object.entries(lock.packages).some(([name,version])=>!recipe.includes(name+'='+version)))throw Error('native_toolchain_source_invalid');
 return {schemaVersion:1,definitionSHA256:sha(JSON.stringify(files)),files,baseImage:lock.baseImage,platform:lock.platform};
}
function verifyEvidence(receipt,root=ROOT){
 const expected=definition(root);
 if(!receipt||receipt.schemaVersion!==1||!/^sha256:[a-f0-9]{64}$/.test(receipt.imageID)||receipt.definitionSHA256!==expected.definitionSHA256||
    receipt.baseImage!==expected.baseImage||receipt.platform!==expected.platform||JSON.stringify(receipt.files)!==JSON.stringify(expected.files))throw Error('native_toolchain_evidence_mismatch');
 return receipt;
}
function command(executable,args,options={}){
 const result=spawnSync(executable,args,{shell:false,windowsHide:true,encoding:'utf8',stdio:options.capture?'pipe':'inherit',
  ...options,env:require('./native-build-environment').buildEnvironment(options.cwd||ROOT,options.env||process.env)});
 if(result.error||result.status!==0)throw Error('native_toolchain_command_failed');
 return (result.stdout||'').trim();
}
function ensureToolchain(options={}){
 const root=options.repositoryRoot||ROOT,expected=definition(root),run=options.runner||command;
 const output=path.join(root,'.artifacts/toolchain',expected.definitionSHA256),receiptFile=path.join(output,'receipt.json');
 function inspect(receipt){
  verifyEvidence(receipt,root);
  const identity=JSON.parse(run('docker',['image','inspect',receipt.imageID,'--format','{{json .}}'],{capture:true,cwd:root}));
  if(identity.Id!==receipt.imageID||identity.Os!=='linux'||identity.Architecture!=='amd64'||identity.Config?.Labels?.['io.amdl.toolchain.definition']!==expected.definitionSHA256)throw Error('native_toolchain_image_mismatch');
  const resolved=JSON.parse(run('docker',['run','--rm','--network=none',receipt.imageID,'cat','/toolchain/resolved.json'],{capture:true,cwd:root}));
  if(resolved.schemaVersion!==1||resolved.inputsSHA256!==expected.files['inputs.lock.json']||resolved.baseImage!==expected.baseImage||resolved.platform!=='linux/amd64')throw Error('native_toolchain_image_mismatch');
  return receipt;
 }
 if(!options.noCache&&fs.existsSync(receiptFile)){
  const receipt=JSON.parse(fs.readFileSync(receiptFile,'utf8'));
  // A corrupt receipt is an error, not an excuse to trust a similarly named tag.
  return inspect(receipt);
 }
 const id=crypto.randomUUID(),context=path.join(root,'.tmp-v2-native/toolchain-'+id);
 fs.mkdirSync(context,{recursive:true});fs.mkdirSync(output,{recursive:true});
 for(const name of FILES)fs.copyFileSync(path.join(root,'native-runtime/toolchain',name),path.join(context,name));
 const iid=path.join(output,'image-'+id+'.id');
 const args=['build','--platform=linux/amd64','--pull','--progress=plain','--iidfile',iid,
  '--label','io.amdl.toolchain.definition='+expected.definitionSHA256,'--tag','amdl-native-toolchain:'+expected.definitionSHA256.slice(0,20),context];
 if(options.noCache)args.splice(1,0,'--no-cache');
 process.stderr.write('Building native toolchain from locked public inputs (first build downloads Android NDK and CMake).\n');
 run('docker',args,{cwd:root});
 const receipt=inspect({...expected,imageID:fs.readFileSync(iid,'utf8').trim()});
 const temporary=receiptFile+'.'+id+'.partial';fs.writeFileSync(temporary,JSON.stringify(receipt,null,2)+'\n',{flag:'wx'});fs.renameSync(temporary,receiptFile);
 return receipt;
}
if(require.main===module){try{const args=process.argv.slice(2);if(args.some(arg=>arg!=='--no-cache')||args.length>1)throw Error('native_toolchain_arguments_invalid');process.stdout.write(JSON.stringify(ensureToolchain({noCache:args.length===1}))+'\n');}catch(error){process.stderr.write(error.message+'\n');process.exitCode=1;}}
module.exports={definition,verifyEvidence,ensureToolchain,FILES};

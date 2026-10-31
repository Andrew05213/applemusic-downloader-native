'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),os=require('node:os');
const {definition,verifyEvidence,ensureToolchain,FILES}=require('../../electron/native-toolchain');
const ROOT=path.resolve(__dirname,'../..');
function fixture(t){
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'native-toolchain-'));
 t.after(()=>fs.rmSync(root,{recursive:true,force:true}));fs.mkdirSync(path.join(root,'native-runtime/toolchain'),{recursive:true});
 fs.copyFileSync(path.join(ROOT,'native-runtime/inputs.lock.json'),path.join(root,'native-runtime/inputs.lock.json'));
 for(const file of FILES)fs.copyFileSync(path.join(ROOT,'native-runtime/toolchain',file),path.join(root,'native-runtime/toolchain',file));
 return root;
}
test('toolchain uses public digest-pinned inputs and no historical image or rootfs',()=>{
 const value=definition(),lock=require('../../native-runtime/toolchain/inputs.lock.json');
 assert.match(value.baseImage,/@sha256:[a-f0-9]{64}$/);assert.equal(value.platform,'linux/amd64');
 assert.equal(lock.ndk.sha256,'c6e97f9c8cfe5b7be0a9e6c15af8e7a179475b7ded23e2d1c1fa0945d6fb4382');
 for(const file of ['native-runtime/toolchain/Dockerfile','native-runtime/toolchain/install.py','electron/build-native-runtime.js'])assert.doesNotMatch(fs.readFileSync(path.join(ROOT,file),'utf8'),/v1215|wrapper-harness|legacy\/v1|rootfs\/data/);
 const workflow=fs.readFileSync(path.join(ROOT,'.github/workflows/verify-toolchain.yml'),'utf8');
 assert.match(workflow,/contents: read/);assert.match(workflow,/build:native-toolchain -- --no-cache/);
 assert.doesNotMatch(workflow,/packages: write|upload-artifact|docker login|docker push/);
});
test('toolchain recipe mutations invalidate evidence even with a matching image ID',t=>{
 const root=fixture(t),receipt={...definition(root),imageID:'sha256:'+'a'.repeat(64)};
 assert.equal(verifyEvidence(receipt,root),receipt);
 fs.appendFileSync(path.join(root,'native-runtime/toolchain/install.py'),'\n# changed recipe\n');
 assert.throws(()=>verifyEvidence(receipt,root),/native_toolchain_evidence_mismatch/);
});
test('toolchain builds only whitelisted context files and binds cache to immutable image identity',t=>{
 const root=fixture(t),expected=definition(root),imageID='sha256:'+'a'.repeat(64),calls=[];
 fs.writeFileSync(path.join(root,'.env'),'SENTINEL_DO_NOT_COPY');
 function runner(command,args){
  assert.equal(command,'docker');calls.push(args);
  if(args[0]==='build'){
   assert(args.includes('--pull'));assert(args.includes('--platform=linux/amd64'));
   assert.deepEqual(fs.readdirSync(args.at(-1)).sort(),[...FILES].sort());
   fs.writeFileSync(args[args.indexOf('--iidfile')+1],imageID);return '';
  }
  if(args[0]==='image')return JSON.stringify({Id:imageID,Os:'linux',Architecture:'amd64',Config:{Labels:{'io.amdl.toolchain.definition':expected.definitionSHA256}}});
  if(args[0]==='run'){assert(args.includes('--network=none'));assert(args.includes(imageID));return JSON.stringify({schemaVersion:1,inputsSHA256:expected.files['inputs.lock.json'],baseImage:expected.baseImage,platform:'linux/amd64'});}
  throw Error('unexpected Docker command');
 }
 assert.equal(ensureToolchain({repositoryRoot:root,runner}).imageID,imageID);
 assert.equal(ensureToolchain({repositoryRoot:root,runner}).imageID,imageID);
 assert.equal(calls.filter(args=>args[0]==='build').length,1);
 assert.throws(()=>ensureToolchain({repositoryRoot:root,runner:()=>JSON.stringify({Id:'sha256:'+'b'.repeat(64)})}),/native_toolchain_image_mismatch/);
});

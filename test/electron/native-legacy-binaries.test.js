'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),crypto=require('node:crypto');
const {definition,verifyArchive,verifyListing,verifyTree,extract}=require('../../scripts/legacy-binaries');
const root=path.resolve(__dirname,'../..');
const sha=b=>crypto.createHash('sha256').update(b).digest('hex');
function smallDefinition(){return {lock:{fileCount:1,expandedSize:3},files:[{path:'legacy/runtime/lib.so',sha256:sha('abc')}]};}
const listing=(extra='')=>'header\n----------\nPath = legacy/runtime/lib.so\nFolder = -\nSize = 3\nEncrypted = -\n'+extra+'\n';
test('external binaries remain bound to the original frozen phase-two manifest',()=>{
 const def=definition(root);assert.equal(def.files.length,100);assert.equal(def.lock.expandedSize,121309053);
 for(const f of def.files)assert.ok(f.path.startsWith('legacy/v1/wrapper-linux/rootfs/system/'));
 const ignored=require('node:child_process').spawnSync('git',['check-ignore','--quiet','--no-index','--',def.files[0].path],{cwd:root,shell:false,windowsHide:true});assert.equal(ignored.status,0);
});
test('archive listing rejects traversal, unknown files, links, duplicates and oversized content',()=>{
 const def=smallDefinition();assert.equal(verifyListing(listing(),def),1);
 for(const bad of [listing().replace('legacy/runtime/lib.so','../outside'),listing().replace('legacy/runtime/lib.so','other.so'),listing('Symbolic Link = outside'),listing('Hard Link = outside'),listing().replace('Size = 3','Size = 999999999'),listing()+'\n'+listing().split('----------\n')[1],listing().replace('Encrypted = -','Encrypted = +')])assert.throws(()=>verifyListing(bad,def),/legacy_binary_listing_invalid/);
});
test('extracted files require exact content and reject extra files',t=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'legacy-binaries-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));
 fs.mkdirSync(path.join(dir,'legacy/runtime'),{recursive:true});const file=path.join(dir,'legacy/runtime/lib.so');fs.writeFileSync(file,'abc');
 assert.deepEqual(verifyTree(dir,smallDefinition()),{files:1,bytes:3});fs.writeFileSync(file,'xyz');assert.throws(()=>verifyTree(dir,smallDefinition()),/legacy_binary_tree_invalid/);
 fs.writeFileSync(file,'abc');fs.writeFileSync(path.join(dir,'extra'),'abc');assert.throws(()=>verifyTree(dir,smallDefinition()),/legacy_binary_tree_invalid/);
});
test('archive hash and size are checked before any extraction command',t=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'legacy-archive-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));const archive=path.join(dir,'archive.zip');fs.writeFileSync(archive,'abc');
 const def={...smallDefinition(),lock:{size:3,sha256:sha('abc')}};assert.doesNotThrow(()=>verifyArchive(archive,def));
 let calls=0;assert.throws(()=>extract(archive,path.join(dir,'unpacked'),{...def,lock:{...def.lock,sha256:'0'.repeat(64)}},'unused',()=>{calls++;}),/legacy_binary_archive_identity_mismatch/);assert.equal(calls,0);
 assert.throws(()=>verifyArchive(archive,{lock:{size:4,sha256:sha('abc')}}),/legacy_binary_archive_identity_mismatch/);
});
test('the optional legacy fetch command is not a Native runtime or build input',()=>{
 for(const file of ['scripts/native-dev.js','electron/build-native-runtime.js','native-runtime/Dockerfile','native-runtime/build-guest.sh'])assert.doesNotMatch(fs.readFileSync(path.join(root,file),'utf8'),/legacy-binaries|binary-assets/);
 assert.match(require('../../package.json').scripts['legacy:fetch'],/legacy-binaries\.js fetch$/);
});
test('tracked source files do not silently reintroduce large binary payloads',()=>{
 const result=require('node:child_process').spawnSync('git',['ls-files','-z'],{cwd:root,encoding:'utf8',shell:false,windowsHide:true});assert.equal(result.status,0);
 for(const file of result.stdout.split('\0').filter(Boolean))assert.ok(fs.statSync(path.join(root,file)).size<=1024*1024,'Move large file to a hash-pinned Release asset: '+file);
});

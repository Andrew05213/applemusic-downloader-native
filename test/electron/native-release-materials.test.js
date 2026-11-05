'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),os=require('node:os'),path=require('node:path'),crypto=require('node:crypto'),zlib=require('node:zlib');
const materials=require('../../native-runtime/release-materials');
const {buildIdentity,verifyToolchain}=require('../../native-runtime/collect-go-licenses');
const {readPlan,validateSource}=require('../../native-runtime/package-debian-sources');
const manifestFile=path.resolve(__dirname,'../../docs/compliance/releases/1.0.1/inventory.json');
const manifest=materials.readManifest(manifestFile);
function temp(t){const p=fs.mkdtempSync(path.join(os.tmpdir(),'amdl-materials-'));t.after(()=>fs.rmSync(p,{recursive:true,force:true}));return p;}
const digest=d=>crypto.createHash('sha256').update(d).digest('hex');
test('actual released Go build info selects runtime modules, not go.mod or test licenses',()=>{
 const parsed=manifest.goBinaries.map(x=>buildIdentity(path.basename(x.binary)+': '+x.goVersion+'\n'+x.buildInfo));
 assert.equal(parsed[0].modules.length,32);assert.equal(parsed[1].modules.length,1);assert.equal(parsed[2].modules.length,0);
 assert.equal(parsed[1].modules[0].module,'golang.org/x/sys');
 assert.equal(parsed[0].mainModules[0].authorizationReviewRequired,true);
 verifyToolchain(parsed,'go1.25.0');assert.throws(()=>verifyToolchain(parsed,'go1.26.0'),/toolchain_mismatch/);
 assert.throws(()=>buildIdentity('a: go1.25.0\n\t=> ./replacement\t(devel)\n'),/replacement_requires_review/);
 assert.throws(()=>buildIdentity('a: go1.25.0\n\tdep example.org/a v1.0.0 missing\n'),/dependency_invalid/);
});
test('Source and Built-Using identify signed-kernel packaging and statically linked glibc',()=>{
 const kernel=materials.debianSources('Package: linux-image-6.1.0-51-amd64\nVersion: 6.1.177-1\nArchitecture: amd64\nSource: linux-signed-amd64 (6.1.177+1)\nBuilt-Using: linux (= 6.1.177-1)\n');
 assert.deepEqual(kernel.sources,[{name:'linux-signed-amd64',version:'6.1.177+1',reason:'Source'},{name:'linux',version:'6.1.177-1',reason:'Built-Using'}]);
 const busy=materials.debianSources('Package: busybox-static\nVersion: 1:1.35.0-4+deb12u1+b1\nArchitecture: amd64\nSource: busybox (1:1.35.0-4+deb12u1)\nBuilt-Using: glibc (= 2.36-9+deb12u14)\nStatic-Built-Using: example (= 1.0),\n other (= 2.0)\n');
 assert.equal(busy.sources[1].name,'glibc');assert.equal(busy.sources[0].version,'1:1.35.0-4+deb12u1');assert.equal(busy.sources.length,4);
 assert.throws(()=>materials.debianSources('Package: a\nPackage: b\nVersion: 1\nArchitecture: amd64\n'),/control_invalid/);
 assert.throws(()=>materials.sourceIdentity('../glibc','2.36'),/identity_invalid/);
});
test('a partial Debian collection cannot omit Built-Using or claim another manifest',t=>{
 const p=temp(t),sources=[...new Map(manifest.debian.binaries.flatMap(x=>x.sources.map(s=>[s.name,s]))).values()];
 const header='# amdl-debian-source-plan-v1 installer='+manifest.installer.sha256+' manifest='+digest(fs.readFileSync(manifestFile))+' snapshot='+manifest.debian.snapshot+'\n';
 const write=records=>fs.writeFileSync(path.join(p,'SOURCE-PLAN.tsv'),header+records.map(x=>x.name+'\t'+x.version).join('\n')+'\n');
 write(sources);assert.equal(readPlan(p,manifestFile).records.length,4);
 write(sources.filter(x=>x.name!=='glibc'));assert.throws(()=>readPlan(p,manifestFile),/plan_incomplete/);
 write([...sources,sources[0]]);assert.throws(()=>readPlan(p,manifestFile),/plan_invalid/);
 fs.writeFileSync(path.join(p,'SOURCE-PLAN.tsv'),header.replace(manifest.installer.sha256,'0'.repeat(64)));assert.throws(()=>readPlan(p,manifestFile),/plan_identity_mismatch/);
});
test('source archive checks bind exact Source/Version and reject corrupt or duplicate input',t=>{
 const p=temp(t),file='a.orig.tar.xz',bytes=Buffer.from('source candidate');fs.writeFileSync(path.join(p,file),bytes);
 const dsc=(version='1.0',names=[file])=>'Source: a\nVersion: '+version+'\nChecksums-Sha256:\n'+names.map(n=>' '+digest(bytes)+' '+bytes.length+' '+n+'\n').join('');
 fs.writeFileSync(path.join(p,'a.dsc'),dsc());assert.deepEqual(validateSource(p,{name:'a',version:'1.0'}),['a.dsc',file]);
 fs.writeFileSync(path.join(p,'a.dsc'),dsc('2.0'));assert.throws(()=>validateSource(p,{name:'a',version:'1.0'}),/identity_mismatch/);
 fs.writeFileSync(path.join(p,'a.dsc'),dsc('1.0',[file,file]));assert.throws(()=>validateSource(p,{name:'a',version:'1.0'}),/dsc_invalid/);
 fs.writeFileSync(path.join(p,'a.dsc'),dsc());fs.writeFileSync(path.join(p,file),'changed');assert.throws(()=>validateSource(p,{name:'a',version:'1.0'}),/hash_mismatch/);
});
function newc(name,data=Buffer.alloc(0)){
 const fields=[1,0o100644,0,0,1,0,data.length,0,0,0,0,Buffer.byteLength(name)+1,0];let h=Buffer.from('070701'+fields.map(v=>v.toString(16).padStart(8,'0')).join('')+name+'\0');h=Buffer.concat([h,Buffer.alloc((4-h.length%4)%4),data]);return Buffer.concat([h,Buffer.alloc((4-h.length%4)%4)]);
}
test('nested guest inventory rejects traversal and duplication, and records deferred Android scope',t=>{
 const p=temp(t),f=path.join(p,'initramfs.cpio.gz'),write=entries=>fs.writeFileSync(f,zlib.gzipSync(Buffer.concat([...entries,newc('TRAILER!!!')])));
 write([newc('./bin/busybox',Buffer.from('busybox')),newc('./system/lib64/a.so',Buffer.from('deferred'))]);const result=materials.guestFiles(f);
 assert.equal(Object.keys(result.files).length,1);assert.equal(result.deferredSystemEntries,1);assert.equal(result.files['bin/busybox'].sha256,digest(Buffer.from('busybox')));
 write([newc('../escape')]);assert.throws(()=>materials.guestFiles(f),/path_invalid/);
 write([newc('./bin/busybox'),newc('bin/busybox')]);assert.throws(()=>materials.guestFiles(f),/cpio_duplicate/);
 fs.writeFileSync(f,zlib.gzipSync(newc('bin/busybox')));assert.throws(()=>materials.guestFiles(f),/cpio_invalid/);
});
test('frozen release identifies actual outer files and never marks deferred material approved',()=>{
 assert.equal(Object.keys(manifest.files).length,286);assert.equal(manifest.qemu.files.length,183);
 assert.equal(manifest.qemu.files.filter(x=>x.path.endsWith('.dll')).length,114);
 assert.equal(manifest.reviewComplete,false);assert.deepEqual(manifest.deferred,['android-apple-rootfs']);
 for(const p of ['../escape','C:\\escape','/escape','a/../escape'])assert.throws(()=>materials.safeName(p),/path_invalid/);
});
test('whole-payload verification rejects truncation, changed data and unlisted distributed files',t=>{
 const p=temp(t),payload=path.join(p,'payload');fs.mkdirSync(payload);
 const owned=path.resolve(__dirname,'../../.artifacts/materials-test-'+crypto.randomUUID());t.after(()=>fs.rmSync(owned,{recursive:true,force:true}));
 const installer=path.join(p,'installer.exe');fs.writeFileSync(installer,'selected installer');
 function asar(){const body=Buffer.from(JSON.stringify({files:{'main.js':{size:0,offset:'0'}}})),header=Buffer.alloc(16);header.writeUInt32LE(body.length,12);return Buffer.concat([header,body]);}
 const entries={'resources/native-runtime/guest/initramfs.cpio.gz':zlib.gzipSync(Buffer.concat([newc('bin/busybox',Buffer.from('busybox')),newc('TRAILER!!!')])),'resources/app.asar':asar(),'resources/default_app.asar':asar()};
 const selected={...manifest,installer:{size:18,sha256:digest(Buffer.from('selected installer'))},files:{},guestFiles:{'bin/busybox':{size:7,sha256:digest(Buffer.from('busybox'))}}};
 for(const [n,data] of Object.entries(entries)){const f=path.join(payload,n);fs.mkdirSync(path.dirname(f),{recursive:true});fs.writeFileSync(f,data);selected.files[n]={size:data.length,sha256:digest(data)};}
 const m=path.join(p,'manifest.json');fs.writeFileSync(m,JSON.stringify(selected));
 assert.equal(materials.inventoryRelease(m,installer,payload,path.join(owned,'valid.json')).files,3);
 fs.appendFileSync(path.join(payload,'resources/app.asar'),'changed');assert.throws(()=>materials.inventoryRelease(m,installer,payload,path.join(owned,'changed.json')),/payload_mismatch/);
 fs.writeFileSync(path.join(payload,'resources/app.asar'),entries['resources/app.asar']);fs.writeFileSync(path.join(payload,'extra.dll'),'unlisted');assert.throws(()=>materials.inventoryRelease(m,installer,payload,path.join(owned,'extra.json')),/payload_mismatch/);
 fs.writeFileSync(installer,'truncated');assert.throws(()=>materials.inventoryRelease(m,installer,payload,path.join(owned,'truncated.json')),/installer_mismatch/);
});

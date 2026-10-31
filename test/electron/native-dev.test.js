'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os');
const {doctor,argumentsFor,validateSelection,environment}=require('../../scripts/native-dev');

test('archive bootstrap is an explicit locked dependency, not a builder implementation detail',()=>{
 const root=path.resolve(__dirname,'../..'),pkg=require(path.join(root,'package.json')),lock=require(path.join(root,'package-lock.json'));
 assert.equal(pkg.devDependencies['7zip-bin'],'5.2.0');
 assert.equal(lock.packages['node_modules/7zip-bin'].version,pkg.devDependencies['7zip-bin']);
 assert.equal(lock.packages[''].devDependencies['7zip-bin'],pkg.devDependencies['7zip-bin']);
});
test('developer commands have explicit isolated mode and never publish',()=>{
 assert.deepEqual(argumentsFor(['setup','--isolated']),{action:'setup',isolated:true});
 assert.deepEqual(argumentsFor(['package']),{action:'package',isolated:false});
 for(const args of [['publish'],['package','--isolated'],['doctor','--skip-checks'],['run','--isolated','--isolated']])assert.throws(()=>argumentsFor(args),/native_dev_arguments_invalid/);
});
test('developer preflight reports missing prerequisites with recovery steps without starting builds',t=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'native-dev-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
 const commands=[];
 const result=doctor({root,platform:'win32',arch:'x64',nodeVersion:'v22.18.0',runner:(command,args)=>{commands.push([command,args]);return {status:1};}});
 assert.equal(result.passed,false);assert(result.checks.filter(item=>!item.ok).every(item=>item.next));
 assert(!commands.some(([,args])=>args.includes('build')||args.includes('run')));
 const original=process.env.GOMODCACHE;
 const env=environment(root);assert(env.GOMODCACHE.startsWith(root+path.sep));assert.equal(process.env.GOMODCACHE,original);
});
test('developer runtime selection cannot escape the current checkout or use old bundles',t=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'native-selection-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
 for(const bundle of ['../../old-runtime','.',path.resolve(root,'..','other-workspace')])assert.throws(()=>validateSelection({schemaVersion:1,bundle},root),/native_development_selection_invalid/);
 assert.throws(()=>validateSelection({schemaVersion:1,bundle:'.artifacts/runtime/old'},root),/native_bundle_source_stale/);
});

test('Electron 45 package is a valid prerequisite before its on-demand binary download',t=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'native-electron-prerequisite-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
 for(const [file,value]of [['node_modules/electron/package.json',JSON.stringify({version:'45.0.0'})],['node_modules/7zip-bin/win/x64/7za.exe','fixture']]){fs.mkdirSync(path.dirname(path.join(root,file)),{recursive:true});fs.writeFileSync(path.join(root,file),value);}
 const options={root,platform:'win32',arch:'x64',nodeVersion:'v22.18.0',runner:(command,args)=>({status:0,stdout:command==='go'?'go version go1.25.0 windows/amd64':command==='git'?'git version fixture':args[0]==='info'?'linux/x86_64':'docker buildx fixture'})};
 const health=doctor(options);assert.equal(health.passed,true);assert.match(health.checks.find(item=>item.name==='Electron package').detail,/setup downloads/);
 assert.equal(doctor({...options,nodeVersion:'v22.11.0'}).passed,false);
});

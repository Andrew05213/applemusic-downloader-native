'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),path=require('node:path'),fs=require('node:fs');
const {prepareElectron}=require('../../electron/dist-native');
test('fresh release packaging explicitly prepares the pinned Windows Electron runtime',()=>{
 const root=path.resolve(__dirname,'../..'),env={PATH:'fixture',ELECTRON_CACHE:'fixture-cache',ELECTRON_OVERRIDE_DIST_PATH:'untrusted',electron_use_remote_checksums:'1',npm_config_electron_use_remote_checksums:'1',ELECTRON_SKIP_BINARY_DOWNLOAD:'1'};const calls=[];
 prepareElectron(root,env,(...args)=>calls.push(args));
 assert.equal(calls.length,1);const [exe,args,selected]=calls[0];assert.equal(exe,process.execPath);assert.deepEqual(args,[path.join(root,'node_modules/electron/install.js')]);
 assert.equal(selected.ELECTRON_INSTALL_PLATFORM,'win32');assert.equal(selected.ELECTRON_INSTALL_ARCH,'x64');assert.equal(selected.electron_config_cache,'fixture-cache');assert.equal(selected.PATH,'fixture');
 for(const key of ['ELECTRON_OVERRIDE_DIST_PATH','electron_use_remote_checksums','npm_config_electron_use_remote_checksums','ELECTRON_SKIP_BINARY_DOWNLOAD'])assert.equal(Object.hasOwn(selected,key),false);
 assert.equal(env.ELECTRON_SKIP_BINARY_DOWNLOAD,'1','caller environment must not be modified');
 const source=fs.readFileSync(path.join(root,'electron/dist-native.js'),'utf8');assert.ok(source.indexOf('prepareElectron(ROOT,buildEnv)')<source.indexOf('prepare(stage,bundle)'));
});
test('Electron installer errors stop the release rather than silently using an absent runtime',()=>{
 assert.throws(()=>prepareElectron('.',{},()=>{throw Error('fixture installer failed');}),/fixture installer failed/);
});

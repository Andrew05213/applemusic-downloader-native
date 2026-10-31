'use strict';
const test=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const path=require('node:path');
const root=path.resolve(__dirname,'../..');

test('new native outputs are grouped on the repository volume, not in source root',()=>{
 const {installerOutput}=require('../../electron/dist-native');
 const {runtimeOutput}=require('../../electron/build-native-runtime');
 assert.equal(installerOutput(root,'2.0.0','fixture'),path.join(root,'.artifacts/installers/2.0.0-fixture'));
 assert.equal(runtimeOutput(root,'fixture'),path.join(root,'.artifacts/runtime/fixture'));
 const config=require('../../electron-builder.native.cjs');
 if(!process.env.AMDL_NATIVE_OUTPUT)assert.equal(config.directories.output,path.join(root,'.artifacts/installers',require('../../package.json').version));
 assert.equal(config.nsis.allowToChangeInstallationDirectory,true);
 assert.equal(config.publish,null);
});

test('generated archives and temporary trees are excluded from Git and Docker context',()=>{
 const ignore=fs.readFileSync(path.join(root,'.gitignore'),'utf8');const docker=fs.readFileSync(path.join(root,'.dockerignore'),'utf8');
 assert.match(ignore,/^\/\.artifacts\/$/m);assert.match(docker,/^\.artifacts$/m);assert.match(docker,/^\.tmp-v2-native$/m);
 for(const value of ['.artifacts/history/fixture/manifest.json','.artifacts/installers/fixture/build.json']){
  const r=require('node:child_process').spawnSync('git',['check-ignore','--quiet','--',value],{cwd:root,shell:false,windowsHide:true});assert.equal(r.status,0,value);
 }
});

test('current repository navigation links resolve and old WSL instructions are labelled historical',()=>{
 for(const name of ['README.md','docs/README.md','docs/REPOSITORY.md','docs/development/README.md']){
  const text=fs.readFileSync(path.join(root,name),'utf8');
  for(const match of text.matchAll(/\]\(([^)]+)\)/g)){
   const target=match[1];if(/^(?:https?:|#)/.test(target))continue;
   assert.ok(fs.existsSync(path.resolve(root,path.dirname(name),target.split('#')[0])),name+': '+target);
  }
 }
 assert.match(fs.readFileSync(path.join(root,'docs/legacy/README.md'),'utf8'),/不是本仓库 Native 1\.0/);
});

test('archive script defaults to preview and has no deletion or Git mutation commands',()=>{
 const script=fs.readFileSync(path.join(root,'scripts/archive-native-builds.ps1'),'utf8');
 assert.match(script,/param\(\[switch\]\$Apply\)/);assert.match(script,/if \(\$Apply -and \$report.moves.Count\)/);
 for(const boundary of ['Assert-InRoot','ReparsePoint','check-ignore','ls-files','manifest.json','ExecutablePath'])assert.ok(script.includes(boundary),boundary);
 assert.doesNotMatch(script,/Remove-Item|git\s+(?:clean|reset|checkout)|cmd\s+\/c/i);
});

'use strict';
const {test}=require('node:test');const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto');
const root=path.resolve(__dirname,'../..');
test('isolated legacy files preserve their original bytes and leave active paths',()=>{
 const manifest=require('../../legacy/v1/manifest.json');
 assert.equal(manifest.maintenanceCommit,'c0719fc61b7a553348e8d7be96321cc37eb0a0d9');
 for(const file of manifest.files){assert(file.path.startsWith('legacy/v1/'));assert(!fs.existsSync(path.join(root,file.original)),file.original);assert.equal(crypto.createHash('sha256').update(fs.readFileSync(path.join(root,file.path))).digest('hex'),file.sha256,file.path);}
});
test('default package does not expose old WSL or appliance commands',()=>{
 const pkg=require('../../package.json');
 for(const [name,command]of Object.entries(pkg.scripts)){assert.doesNotMatch(name,/wsl|appliance|windows-helper|release-gates/);assert.doesNotMatch(command,/legacy\/v1|appliance\/scripts|start-drm/);}
 assert.equal(pkg.main,'src/main/native-main.js');
});

test('phase two preserves archived inputs and moves shared Web assets without editing them',()=>{
 const manifest=require('../../legacy/v1/phase2-manifest.json');
 const binary=require('../../scripts/legacy-binaries').definition(root),external=new Map(binary.files.map(f=>[f.path,f.sha256]));
 for(const file of [...manifest.files,...manifest.web]){
  if(external.has(file.path)){assert.equal(external.get(file.path),file.sha256);continue;}
  assert.equal(crypto.createHash('sha256').update(fs.readFileSync(path.join(root,file.path))).digest('hex'),file.sha256,file.path);
  if(!file.retainedAsStub)assert(!fs.existsSync(path.join(root,file.original)),file.original);
 }
 for(const entry of fs.readdirSync(path.join(root,'src/renderer'))){
  if(/\.(?:js|html|css)$/.test(entry))assert(entry.startsWith('native'),entry);
 }
 const checkIgnore=name=>require('node:child_process').spawnSync('git',['check-ignore','--quiet','--no-index','--',name],{cwd:root,shell:false,windowsHide:true}).status;
 const account='legacy/v1/wrapper-linux/rootfs/data/data/com.apple.android.music/';
 assert.equal(checkIgnore(account+'.gitkeep'),1);
 assert.equal(checkIgnore(account+'private-session.json'),0);
 assert.equal(require('../../appliance/release-policy.json').publicReleaseAllowed,false);
 const config=require('../../electron-builder.native.cjs');
 assert.doesNotMatch(JSON.stringify(config.files),/legacy\/|(?:^|["/])web\//);
 assert(config.extraResources.some(item=>item.from==='appliance/release-policy.json'&&item.to==='release-policy.json'));
 for(const file of ['electron/build-native-runtime.js','native-runtime/Dockerfile','native-runtime/build-guest.sh']){
  assert.doesNotMatch(fs.readFileSync(path.join(root,file),'utf8'),/legacy\/v1|wrapper-linux\//,file);
 }
});
test('active native entry files cannot require archived or old desktop modules',()=>{
 const files=['src/main/native-main.js',...fs.readdirSync(path.join(root,'electron')).filter(n=>n.endsWith('.js')).map(n=>'electron/'+n)];
 for(const file of files){const text=fs.readFileSync(path.join(root,file),'utf8');assert.doesNotMatch(text,/require\([^)]*(?:legacy\/v1|wsl-manager|official-web-player|drm-runtime|apple-music-browser)/,file);for(const match of text.matchAll(/require\(['"](\.[^'"]+)['"]\)/g)){const target=path.resolve(path.dirname(path.join(root,file)),match[1]);assert([target,target+'.js',target+'.json',target+'.cjs'].some(p=>fs.existsSync(p)),file+': '+match[1]);}}
});

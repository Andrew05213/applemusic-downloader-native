'use strict';
// Developer convenience only; never changes the installed app or system tools.
const fs=require('node:fs'),path=require('node:path'),{spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
function environment(root=ROOT){
 const env=require('../electron/native-build-environment').buildEnvironment(root);
 // A clone can populate its own module cache, without a maintainer's GOPATH.
 env.GOMODCACHE=path.join(root,'.tmp-v2-native/development/go-mod-cache');
 env.electron_config_cache=env.ELECTRON_CACHE;
 env.GOWORK='off';env.GOTOOLCHAIN='local';
 env.ELECTRON_INSTALL_PLATFORM='win32';env.ELECTRON_INSTALL_ARCH='x64';
 for(const key of ['ELECTRON_OVERRIDE_DIST_PATH','electron_use_remote_checksums','npm_config_electron_use_remote_checksums'])delete env[key];
 fs.mkdirSync(env.GOMODCACHE,{recursive:true});return env;
}
function doctor(options={}){
 const root=options.root||ROOT,runner=options.runner||spawnSync;
 const checks=[];
 const check=(name,ok,detail,next)=>checks.push({name,ok,detail,...(!ok?{next}: {})});
 check('Windows x64',(options.platform||process.platform)==='win32'&&(options.arch||process.arch)==='x64','Desktop build host','Use Windows x64; Linux supports unit tests and toolchain builds only.');
 const nodeVersion=options.nodeVersion||process.version,minor=nodeVersion.match(/^v22\.(\d+)\./);
 check('Node.js',!!minor&&Number(minor[1])>=12,nodeVersion,'Install Node.js 22.12 or newer within 22.x and reopen the terminal.');
 function output(executable,args){const result=runner(executable,args,{cwd:root,shell:false,windowsHide:true,encoding:'utf8',timeout:30000});return result.status===0?(result.stdout||'').trim():'';}
 const go=output('go',['version']);
 check('Go',/^go version go1\.25\.0 /.test(go),go||'Not found','Install Go 1.25.0 and reopen the terminal.');
 const git=output('git',['--version']);check('Git',/^git version /.test(git),git||'Not found','Install Git and reopen the terminal.');
 const docker=output('docker',['info','--format','{{.OSType}}/{{.Architecture}}']);
 check('Docker Linux engine',/^linux\/(?:x86_64|amd64)$/.test(docker),docker||'Not running','Start Docker Desktop, select Linux containers, and wait for the engine to be ready.');
 const buildx=output('docker',['buildx','version']);check('Docker Buildx',buildx.includes('buildx'),buildx||'Not found','Install/update Docker Desktop with Buildx support.');
 const electronPackage=path.join(root,'node_modules/electron/package.json');let electronReady=false;
 try{electronReady=JSON.parse(fs.readFileSync(electronPackage,'utf8')).version===require('../package.json').devDependencies.electron;}catch{}
 check('Electron package',electronReady,electronReady?(fs.existsSync(path.join(root,'node_modules/electron/dist/electron.exe'))?'Binary available':'Package ready; setup downloads the pinned binary'):'Missing or wrong version','Run npm ci; setup downloads Electron 45 on demand.');
 const zip='node_modules/7zip-bin/win/x64/7za.exe';
 check('7-Zip bootstrap',fs.existsSync(path.join(root,zip)),fs.existsSync(path.join(root,zip))?'Available':'Missing','Run npm ci; do not copy another workspace node_modules.');
 return {passed:checks.every(check=>check.ok),checks};
}
function argumentsFor(args){
 const [action,...flags]=args;
 if(!['doctor','setup','run','package'].includes(action)||flags.some(flag=>flag!=='--isolated')||flags.length>1||(['doctor','package'].includes(action)&&flags.length))throw Error('native_dev_arguments_invalid');
 return {action,isolated:flags.includes('--isolated')};
}
function validateSelection(record,root=ROOT,isolated=false){
 if(!record||record.schemaVersion!==1||typeof record.bundle!=='string')throw Error('native_development_selection_invalid');
 const bundle=path.resolve(root,record.bundle),relative=path.relative(path.join(root,'.artifacts/runtime'),bundle);
 if(!relative||relative.startsWith('..')||path.isAbsolute(relative))throw Error('native_development_selection_invalid');
 require('../electron/native-package').verifyBundleSources(bundle,root);
 const manifest=JSON.parse(fs.readFileSync(path.join(bundle,'bundle.json'),'utf8'));
 if(manifest.isolated!==isolated||JSON.stringify(manifest)!==JSON.stringify(require('../electron/build-native-runtime').makeManifest(bundle,{isolated})))throw Error('native_development_selection_invalid');
 return bundle;
}
function prepare(isolated=false){
 const health=doctor();if(!health.passed){process.stderr.write(JSON.stringify(health,null,2)+'\n');throw Error('native_development_prerequisites_missing');}
 const env=environment();
 function node(args,capture=false){const result=spawnSync(process.execPath,args,{cwd:ROOT,env,shell:false,windowsHide:true,encoding:'utf8',stdio:capture?['ignore','pipe','inherit']:'inherit',maxBuffer:16*1024*1024});if(result.error||result.status!==0)throw Error('native_development_step_failed');return (result.stdout||'').trim();}
 // Electron 45 installs its binary on demand, not during npm ci. Invoke the
 // pinned package's installer explicitly before inspecting or packaging it.
 process.stderr.write('Preparing pinned Electron binary...\n');node(['node_modules/electron/install.js']);
 const selected=path.join(ROOT,'.artifacts/development',isolated?'isolated.json':'network.json');
 let bundle;
 if(fs.existsSync(selected)){
  try{bundle=validateSelection(JSON.parse(fs.readFileSync(selected,'utf8')),ROOT,isolated);}catch{process.stderr.write('Development runtime is stale or incomplete; building a new one.\n');}
 }
 if(!bundle){
  process.stderr.write('Building '+(isolated?'isolated':'network-enabled')+' runtime from locked inputs...\n');
  const output=node(['electron/build-native-runtime.js',...(isolated?[]:['--allow-network'])],true);
  const created=output.split(/\r?\n/).at(-1);
  const record={schemaVersion:1,bundle:path.relative(ROOT,created).split(path.sep).join('/')};
  bundle=validateSelection(record,ROOT,isolated);
  fs.mkdirSync(path.dirname(selected),{recursive:true});fs.writeFileSync(selected,JSON.stringify(record,null,2)+'\n');
 }
 process.stderr.write('Building native Windows services...\n');
 for(const script of ['electron/build-native-backend.js','electron/build-native-service.js'])node([script]);
 return {bundle,manifest:path.join(bundle,'bundle.json'),env,node};
}
function main(args=process.argv.slice(2)){
 const {action,isolated}=argumentsFor(args);
 if(action==='doctor'){const result=doctor();process.stdout.write(JSON.stringify(result,null,2)+'\n');if(!result.passed)process.exitCode=1;return;}
 const ready=prepare(isolated);
 if(action==='setup')process.stdout.write(JSON.stringify({passed:true,bundle:ready.bundle,manifest:ready.manifest,isolated})+'\n');
 if(action==='package'){
  const result=spawnSync(process.execPath,['electron/dist-native.js'],{cwd:ROOT,env:{...ready.env,AMDL_NATIVE_BUNDLE:ready.bundle,AMDL_PUBLIC_RELEASE:process.env.AMDL_PUBLIC_RELEASE||'0'},shell:false,windowsHide:true,stdio:'inherit'});
  if(result.error||result.status!==0)throw Error('native_development_package_failed');
 }
 if(action==='run')ready.node(['electron/start-native.js','--native-manifest',ready.manifest]);
}
if(require.main===module){try{main();}catch(error){process.stderr.write(error.message+'\n');process.exitCode=1;}}
module.exports={doctor,argumentsFor,validateSelection,environment,prepare};

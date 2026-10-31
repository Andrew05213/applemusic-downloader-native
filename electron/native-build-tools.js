'use strict';
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');const {spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
// Exact HTTPS legacy toolset retained by electron-builder 26.18.0. Exclude non-Windows
// trees before extraction; never enable symlink privileges for this build.
const SHA512='e8b408d9df413c2dd7b346684d07b5a37b4f880dbc73bf8f63af50f62fe90fc958e39a6c32d1ee2f0bf7bd1724895af7671d5c6cdc8b94147c493c0275c1f0b4';
const URL='https://github.com/electron-userland/electron-builder-binaries/releases/download/winCodeSign-2.6.0/winCodeSign-2.6.0.7z';
const digest=file=>crypto.createHash('sha512').update(fs.readFileSync(file)).digest('hex');
async function prepareWindowsTools(){
 const cache=path.join(ROOT,'.tmp-v2-native/builder-cache/winCodeSign');const target=path.join(cache,'winCodeSign-2.6.0');
 fs.mkdirSync(cache,{recursive:true});
 let archive=fs.readdirSync(cache).filter(name=>name.endsWith('.7z')).map(name=>path.join(cache,name)).find(file=>digest(file)===SHA512);
 if(!archive){const response=await fetch(URL,{signal:AbortSignal.timeout(120000)});if(!response.ok)throw new Error('native_windows_tool_download_failed');const data=Buffer.from(await response.arrayBuffer());if(crypto.createHash('sha512').update(data).digest('hex')!==SHA512)throw new Error('native_windows_tool_identity_mismatch');archive=path.join(cache,'winCodeSign-2.6.0.7z');fs.writeFileSync(archive,data,{flag:'wx'});}
 if(digest(archive)!==SHA512)throw new Error('native_windows_tool_identity_mismatch');
 const stage=path.join(cache,'extract-'+crypto.randomUUID());
 const result=spawnSync(path.join(ROOT,'node_modules/7zip-bin/win/x64/7za.exe'),['x','-y',archive,'-o'+stage,'-xr!darwin','-xr!linux','-xr!.DS_Store'],{shell:false,windowsHide:true,stdio:'inherit'});
 if(result.error||result.status!==0||!fs.existsSync(path.join(stage,'rcedit-x64.exe'))||fs.existsSync(path.join(stage,'darwin')))throw new Error('native_windows_tool_extract_failed');
 fs.writeFileSync(path.join(stage,'native-build-tool.json'),JSON.stringify({url:URL,sha512:SHA512,windowsOnly:true})+'\n',{flag:'wx'});
 const {tree,verifyCache}=require('./native-nsis-tools');
 if(fs.existsSync(target))verifyCache(tree(stage),target);else fs.renameSync(stage,target);
 return target;
}
if(require.main===module)prepareWindowsTools().then(directory=>{process.stdout.write(directory+'\n');process.stdout.write(JSON.stringify(require('./native-nsis-tools').prepare())+'\n');}).catch(error=>{process.stderr.write(error.message+'\n');process.exitCode=1;});
module.exports={prepareWindowsTools};

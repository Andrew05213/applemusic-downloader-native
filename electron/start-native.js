'use strict';
const path=require('node:path');
const fs=require('node:fs');
const {spawn}=require('node:child_process');

// The development entry is explicit, not package.json's frozen 1.4 release
// entry. No WSL setup, DRM updater, browser or legacy state is loaded.
function nativeLaunch(root,args,executable){
  if(args.length!==2||args[0]!=='--native-manifest'||!path.isAbsolute(args[1]))throw new Error('native_manifest_argument_required');
  const manifest=JSON.parse(fs.readFileSync(args[1],'utf8'));
  if(manifest.controlProtocol!==1)throw new Error('native_bundle_protocol_required');
  return {executable,args:[path.join(root,'src/main/native-main.js'),...args]};
}
if(require.main===module){
  try{
    const root=path.resolve(__dirname,'..');const executable=process.env.AMDL_NATIVE_ELECTRON_EXE||require('electron');
    if(!path.isAbsolute(executable)||!fs.existsSync(executable))throw new Error('native_electron_unavailable');
    const launch=nativeLaunch(root,process.argv.slice(2),executable);
    const env={...process.env};delete env.ELECTRON_RUN_AS_NODE;
    const child=spawn(launch.executable,launch.args,{cwd:root,env,stdio:'inherit',shell:false,windowsHide:true});
    child.on('error',()=>{process.stderr.write('native_electron_unavailable\n');process.exitCode=1;});child.on('close',code=>{process.exitCode=code??1;});
  }catch(error){process.stderr.write((/^native_[a-z_]+$/.test(error.message)?error.message:'native_start_failed')+'\n');process.exitCode=1;}
}
module.exports={nativeLaunch};

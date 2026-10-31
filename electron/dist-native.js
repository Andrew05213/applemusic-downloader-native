'use strict';
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');const {spawnSync}=require('node:child_process');
const {prepare,hash,ROOT,verifyBundleSources}=require('./native-package');const {VERSION}=require('./native-version');
function run(executable,args,env){const result=spawnSync(executable,args,{cwd:ROOT,env:env||process.env,stdio:'inherit',shell:false,windowsHide:true});if(result.error||result.status!==0)throw new Error('native_dist_step_failed');}
function selectBundle(env=process.env){if(typeof env.AMDL_NATIVE_BUNDLE!=='string'||!env.AMDL_NATIVE_BUNDLE.trim())throw new Error('native_bundle_selection_required');return path.resolve(ROOT,env.AMDL_NATIVE_BUNDLE.trim());}
function installerOutput(root,version,id){return path.join(root,'.artifacts','installers',version+'-'+id);}
function main(){
 if(process.env.AMDL_PUBLIC_RELEASE==='1' && require('../native-runtime/release-policy.json').publicReleaseAllowed!==true)throw new Error('native_public_release_blocked');
 if(process.platform!=='win32')throw new Error('native_windows_build_required');
 const bundle=selectBundle();verifyBundleSources(bundle);
 const id=crypto.randomUUID();const stage=path.join(ROOT,'electron/generated/native-'+id);const output=installerOutput(ROOT,VERSION,id);
 if(fs.existsSync(output))throw new Error('native_output_must_be_new');
 const buildEnv=require('./native-build-environment').buildEnvironment(ROOT);
 run(process.execPath,['electron/build-native-backend.js'],buildEnv);run(process.execPath,['electron/build-native-service.js'],buildEnv);run(process.execPath,['electron/native-build-tools.js'],buildEnv);prepare(stage,bundle);
 const temporary=path.join(ROOT,'.tmp-v2-native','builder-'+id);fs.mkdirSync(temporary,{recursive:true});
 // Builder 26.18 moved tool downloads to a new cache layout. Pass only the
 // artifacts our bootstrap has just hash-verified, not an ambient/system cache.
 const tools=path.join(ROOT,'.tmp-v2-native/builder-cache');
 const env={...buildEnv,AMDL_NATIVE_STAGE:stage,AMDL_NATIVE_OUTPUT:output,TEMP:temporary,TMP:temporary,TMPDIR:temporary,CSC_IDENTITY_AUTO_DISCOVERY:'false',
  ELECTRON_BUILDER_RCEDIT_PATH:path.join(tools,'winCodeSign/winCodeSign-2.6.0'),
  ELECTRON_BUILDER_NSIS_DIR:path.join(tools,'nsis/nsis-3.0.4.1'),
  ELECTRON_BUILDER_NSIS_RESOURCES_DIR:path.join(tools,'nsis/nsis-resources-3.4.1')};
 run(process.execPath,['node_modules/electron-builder/cli.js','--win','nsis','--x64','--config','electron-builder.native.cjs','--publish','never'],env);
 const verification=require('./verify-native-package').verifyPackage(path.join(output,'win-unpacked'));const filename='AppleMusicDownloader-'+VERSION+'-Setup.exe';
 const installer=path.join(output,filename);const digest=hash(installer);fs.writeFileSync(installer+'.sha256',digest+'  '+filename+'\n',{flag:'wx'});
 const result={...verification,installer,installerSHA256:digest,stage,output,privateDistributionOnly:process.env.AMDL_PUBLIC_RELEASE!=='1'};fs.writeFileSync(path.join(output,'native-build-evidence.json'),JSON.stringify(result,null,2)+'\n',{flag:'wx'});process.stdout.write(JSON.stringify(result)+'\n');
}
if(require.main===module){try{main();}catch(error){process.stderr.write(error.message+'\n');process.exitCode=1;}}
module.exports={main,selectBundle,installerOutput};

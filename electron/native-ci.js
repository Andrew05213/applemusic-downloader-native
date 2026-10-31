'use strict';
const fs=require('node:fs'),path=require('node:path'),{spawnSync}=require('node:child_process');
const ROOT=path.resolve(__dirname,'..');
function testFiles(root=ROOT){
 const shared=fs.readdirSync(path.join(root,'test')).filter(name=>name.endsWith('.test.js')).map(name=>'test/'+name);
 const desktop=fs.readdirSync(path.join(root,'test/electron')).filter(name=>/^native-.*\.test\.js$/.test(name)||name==='repository-layout.test.js').map(name=>'test/electron/'+name);
 return [...shared,...desktop].sort();
}
async function contract(root=ROOT){
 const pkg=require(path.join(root,'package.json')),policy=require(path.join(root,'appliance/release-policy.json'));
 if(pkg.main!=='src/main/native-main.js'||!/^1\.0\./.test(pkg.version)||pkg.build.extends!=='./electron-builder.native.cjs'||policy.publicReleaseAllowed!==false)throw Error('native_ci_contract_invalid');
 const config=require(path.join(root,'electron-builder.native.cjs'));
 if(config.electronVersion!=='45.0.0'||!config.nsis.allowToChangeInstallationDirectory||config.nsis.oneClick||config.publish!==null)throw Error('native_ci_packaging_invalid');
 const nativePolicy=require(path.join(root,'native-runtime/release-policy.json'));
 if(nativePolicy.schemaVersion!==1||nativePolicy.publicReleaseAllowed!==true||nativePolicy.reviewStatus!=='incomplete')throw Error('native_ci_policy_invalid');
 return {version:pkg.version,publicReleaseAllowed:true,legacyPublicReleaseAllowed:false,reviewStatus:'incomplete'};
}
async function main(){
 const action=process.argv[2];
 if(action==='contract'){console.log(JSON.stringify(await contract()));return;}
 if(action!=='unit')throw Error('native_ci_action_invalid');
 const result=spawnSync(process.execPath,['--test',...testFiles()],{cwd:ROOT,env:process.env,shell:false,windowsHide:true,stdio:'inherit'});
 process.exitCode=result.status??1;
}
if(require.main===module)main().catch(error=>{console.error(error.message);process.exitCode=1;});
module.exports={testFiles,contract};

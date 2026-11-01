'use strict';
const path=require('node:path');
const root=__dirname;
const version=require('./package.json').version;
const stage=process.env.AMDL_NATIVE_STAGE||path.join(root,'electron/generated/native-test');
const output=process.env.AMDL_NATIVE_OUTPUT||path.join(root,'.artifacts','installers',version);
module.exports={
 // Retain the beta's app identity/GUID so local upgrades keep its installation
 // and account/task state. The new repository restarts product versions at 1.0.
 extends:null,appId:'io.github.andrew05213.applemusicdownloader.v2test',productName:'Apple Music Downloader Native',
 electronVersion:'45.0.0',electronDist:path.join(root,'node_modules/electron/dist'),asar:true,npmRebuild:false,forceCodeSigning:false,publish:null,
 directories:{output},
 files:['src/main/native-main.js','src/preload/native-preload.js','src/preload/native-startup-preload.js','src/renderer/native-startup.html','src/renderer/native-startup.js','src/renderer/native-startup.css','src/renderer/native-index.html','src/renderer/native-login.html','src/renderer/native-login.js','src/renderer/native-renderer.js','src/renderer/native-stream.js','src/renderer/native.css','src/renderer/native-shell.css','electron/native-cache.js','electron/native-service.js','electron/native-login.js','electron/native-startup.js','electron/native-version.js','electron/native-diagnostics.js','package.json'],
 extraResources:[{from:path.join(stage,'bin'),to:'bin'},{from:path.join(stage,'native-runtime'),to:'native-runtime'},{from:path.join(stage,'native-package.json'),to:'native-package.json'},{from:'LICENSE',to:'LICENSE'},{from:'NOTICE',to:'NOTICE'},{from:'NATIVE-LOCAL-NOTICE.md',to:'NATIVE-LOCAL-NOTICE.md'},{from:'appliance/release-policy.json',to:'release-policy.json'},{from:'native-runtime/release-policy.json',to:'native-release-policy.json'},{from:'apple-music-downloader/utils/runv2/LICENSE',to:'third-party-licenses/runv2-LICENSE'},{from:'third-party-licenses/cJSON-LICENSE',to:'third-party-licenses/cJSON-LICENSE'},{from:'third-party-licenses/Dobby-LICENSE',to:'third-party-licenses/Dobby-LICENSE'}],
 beforePack:async()=>{if(process.env.AMDL_PUBLIC_RELEASE==='1'&&require('./native-runtime/release-policy.json').publicReleaseAllowed!==true)throw new Error('native_public_release_blocked');require('./electron/native-package').verifyStage(stage);},
 afterPack:async context=>{require('./electron/verify-native-package').verifyPackage(context.appOutDir);},
 win:{target:[{target:'nsis',arch:['x64']}],signAndEditExecutable:true},
 nsis:{guid:'762d2d31-e8dd-4aca-a7e5-89dbd09cb138',oneClick:false,perMachine:false,allowElevation:false,allowToChangeInstallationDirectory:true,deleteAppDataOnUninstall:false,runAfterFinish:false,createDesktopShortcut:true,shortcutName:'Apple Music Downloader Native',include:'electron/installer-native.nsh',license:'NATIVE-LOCAL-NOTICE.md',artifactName:'AppleMusicDownloader-${version}-Setup.${ext}'},
};

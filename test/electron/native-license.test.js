'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const os=require('node:os');
const path=require('node:path');
const crypto=require('node:crypto');
const root=path.resolve(__dirname,'../..');
const {PROJECT_NOTICES,verifyProjectNotices}=require('../../electron/verify-native-package');

test('project metadata and canonical Apache-2.0 license agree without changing dependency licenses',()=>{
 const pkg=require('../../package.json'),lock=require('../../package-lock.json');
 assert.equal(pkg.license,'Apache-2.0');assert.equal(lock.packages[''].license,pkg.license);
 // Exact bytes from https://www.apache.org/licenses/LICENSE-2.0.txt.
 const license=fs.readFileSync(path.join(root,'LICENSE'));
 assert.equal(crypto.createHash('sha256').update(license).digest('hex'),'cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30');
 assert.match(fs.readFileSync(path.join(root,'.gitattributes'),'utf8'),/^LICENSE text eol=lf$/m);
 assert.equal(lock.packages['node_modules/@electron/asar'].license,'MIT');
 assert.match(fs.readFileSync(path.join(root,'apple-music-downloader/utils/runv2/LICENSE'),'utf8'),/MIT License/);
});

test('public licensing scope retains third-party attribution and unresolved authorization boundaries',()=>{
 const readme=fs.readFileSync(path.join(root,'README.md'),'utf8'),notice=fs.readFileSync(path.join(root,'NOTICE'),'utf8');
 assert.match(readme,/\[Apache License 2\.0\]\(LICENSE\)/);
 assert.match(readme,/第三方导入代码、历史归档、依赖及运行组件不因根目录许可证而改变授权/);
 assert.match(notice,/Copyright 2026 Andrew05213/);
 assert.match(notice,/does not relicense imported code/);
 assert.match(notice,/original script to Sorrow/);
 assert.match(notice,/No rights in third-party material are granted here/);
});

test('future native installers include the project license and retain installation and third-party notices',()=>{
 const config=require('../../electron-builder.native.cjs');
 for(const name of PROJECT_NOTICES)assert.ok(config.extraResources.some(value=>value.from===name&&value.to===name),name);
 assert.equal(config.nsis.license,'NATIVE-LOCAL-NOTICE.md');
 assert.match(fs.readFileSync(path.join(root,config.nsis.license),'utf8'),/licensed under Apache-2\.0/);
 for(const name of ['cJSON-LICENSE','Dobby-LICENSE'])assert.ok(config.extraResources.some(value=>value.from==='third-party-licenses/'+name));
 assert.ok(config.extraResources.some(value=>value.from==='apple-music-downloader/utils/runv2/LICENSE'));
});

test('package verification rejects missing or changed project license and notices',t=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'amdl-project-license-'));
 t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 for(const name of PROJECT_NOTICES)fs.copyFileSync(path.join(root,name),path.join(directory,name));
 assert.equal(verifyProjectNotices(directory),true);
 for(const name of PROJECT_NOTICES){
  const file=path.join(directory,name);fs.unlinkSync(file);
  assert.throws(()=>verifyProjectNotices(directory),/native_project_notice_invalid/);
  fs.writeFileSync(file,'wrong notice');
  assert.throws(()=>verifyProjectNotices(directory),/native_project_notice_invalid/);
  fs.copyFileSync(path.join(root,name),file);
 }
 assert.equal(verifyProjectNotices(directory),true);
});

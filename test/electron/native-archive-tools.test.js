'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),crypto=require('node:crypto');
const {verify}=require('../../electron/native-archive-tools');
test('developer extraction tools have both archive and executable identities, not a system-path fallback',()=>{
 const spec=require('../../native-runtime/build-tools.lock.json').sevenZip;
 assert.match(spec.url,/^https:\/\/github\.com\/ip7z\/7zip\/releases\/download\//);assert.match(spec.sha256,/^[a-f0-9]{64}$/);
 assert.deepEqual(Object.keys(spec.files),['7z.exe','7z.dll','License.txt']);
 const source=fs.readFileSync(path.resolve(__dirname,'../../electron/build-native-runtime.js'),'utf8');
 assert.doesNotMatch(source,/ProgramFiles|C:\\\\Program Files|7-Zip.*7z\.exe/);
});
test('cached extraction binaries and DLLs are checked before use',t=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-archive-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 const files={};for(const name of ['7z.exe','7z.dll','License.txt']){fs.writeFileSync(path.join(directory,name),name);files[name]=crypto.createHash('sha256').update(name).digest('hex');}
 assert.equal(verify(directory,{files}),path.join(directory,'7z.exe'));
 fs.writeFileSync(path.join(directory,'7z.dll'),'changed');assert.throws(()=>verify(directory,{files}),/native_archive_tool_identity_mismatch/);
});

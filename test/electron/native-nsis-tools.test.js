'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path');
const {SPECS,tree,verifyCache}=require('../../electron/native-nsis-tools');
test('NSIS artifact pins match the currently locked builder, without system installations',()=>{
 const library=path.dirname(require.resolve('app-builder-lib/package.json'));
 for(const spec of SPECS){assert(fs.readFileSync(path.join(library,spec.source),'utf8').includes(spec.builderSHA256));assert.match(spec.sha512,/^[A-Za-z0-9+/]{86}==$/);}
});
test('NSIS cache verification rejects both changed and extra files',t=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'native-nsis-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
 fs.writeFileSync(path.join(root,'fixture'),'expected');const expected=tree(root);verifyCache(expected,root);
 fs.writeFileSync(path.join(root,'extra'),'unexpected');assert.throws(()=>verifyCache(expected,root),/native_nsis_cache_invalid/);fs.unlinkSync(path.join(root,'extra'));
 fs.writeFileSync(path.join(root,'fixture'),'changed');assert.throws(()=>verifyCache(expected,root),/native_nsis_cache_invalid/);
});

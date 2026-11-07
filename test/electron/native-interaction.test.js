'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {request,answer}=require('../../electron/native-interaction');
const {safeSession}=require('../../electron/native-login');
const {sanitizeDiagnostics}=require('../../electron/native-diagnostics');
function q(){return {protocolVersion:1,scope:'a'.repeat(32),attemptId:'18446744073709551615',requestId:'9007199254740993',expiresAfterMs:1000,kind:4,unsupported:0,textTruncated:false,title:'PRIVATE_TITLE',message:'<img src=x onerror=secret()>',buttons:[{id:'18446744073709551614',label:'Continue',role:2,enabled:true,unsupported:0}],fields:[{id:'1',label:'Password',type:2,required:true,minBytes:1,maxBytes:4096,digitsOnly:false}]};}
function a(r){return {protocolVersion:1,scope:r.scope,attemptId:r.attemptId,requestId:r.requestId,disposition:1,buttonId:r.buttons[0].id,values:[{fieldId:'1',value:'PRIVATE_PASSWORD'}]};}
test('interaction wire preserves uint64 identifiers and strips unknown UI data',()=>{
 const r=q();assert.deepEqual(request(r),r);assert.equal(request(null),null);
 assert.equal(request({...r,privateCache:'secret'}).privateCache,undefined);
 assert.deepEqual(answer(r,{...a(r),untrusted:'secret'}),a(r));
 for(const change of [{attemptId:'18446744073709551616'},{attemptId:9007199254740993},{scope:'x'.repeat(32)},{expiresAfterMs:300001},{title:'界'.repeat(400)},{title:'\ud800'},{fields:[...r.fields,...r.fields]}])assert.throws(()=>request({...r,...change}));
});
test('interaction answer rejects cross-request, malformed, forbidden and oversized input',()=>{
 const r=q();for(const change of [{scope:'b'.repeat(32)},{requestId:'1'},{attemptId:'1'},{buttonId:'1'},{values:[]},{values:[{fieldId:'1',value:''}]},{values:[{fieldId:'1',value:'界'.repeat(1400)}]},{values:[...a(r).values,...a(r).values]}])assert.throws(()=>answer(r,{...a(r),...change}));
 for(const change of [{kind:6},{unsupported:2},{buttons:[{...r.buttons[0],enabled:false}]},{buttons:[{...r.buttons[0],unsupported:2}]}])assert.throws(()=>answer({...r,...change},a(r)));
 assert.throws(()=>answer({...r,fields:[{...r.fields[0],digitsOnly:true}]},a(r)));
 for(const d of [2,3]){const v={...a(r),disposition:d,buttonId:undefined,values:[]};assert.equal(answer(r,v).disposition,d);assert.throws(()=>answer(r,{...v,values:a(r).values}));}
});
test('interaction content never enters safe session or diagnostic export schema',()=>{
 const r=q();const result=safeSession({state:'needs_interaction',interaction:r,password:'PRIVATE_PASSWORD',diagnostics:{events:[r]}});
 assert.equal(result.state,'needs_interaction');assert.doesNotMatch(JSON.stringify(result),/PRIVATE|onerror|title|password|message/);
 assert.deepEqual(sanitizeDiagnostics({events:[r],lastFailure:r}),{events:[]});
});

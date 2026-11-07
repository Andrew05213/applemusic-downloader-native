'use strict';
// Ephemeral UI transport only; never pass these payloads to cache/diagnostics.
const id=v=>typeof v==='string'&&/^[1-9][0-9]{0,19}$/.test(v)&&BigInt(v)<=18446744073709551615n;
const bytes=v=>Buffer.byteLength(v,'utf8');
const text=(v,max)=>typeof v==='string'&&!v.includes('\0')&&bytes(v)<=max&&Buffer.from(v,'utf8').toString('utf8')===v;
const integer=(v,min,max)=>Number.isInteger(v)&&v>=min&&v<=max;
function request(q){
 if(q===null)return null;
 if(!q||q.protocolVersion!==1||typeof q.scope!=='string'||!/^[a-f0-9]{32}$/.test(q.scope)||!id(q.attemptId)||!id(q.requestId)||!integer(q.expiresAfterMs,1,300000)||!integer(q.kind,1,6)||!integer(q.unsupported,0,5)||typeof q.textTruncated!=='boolean'||!text(q.title,1024)||!text(q.message,8192)||!Array.isArray(q.buttons)||q.buttons.length>16||!Array.isArray(q.fields)||q.fields.length>16)throw Error('native_interaction_invalid');
 let total=bytes(q.title)+bytes(q.message);const buttons=[],fields=[],seen=new Set();
 for(const b of q.buttons){if(!b||!id(b.id)||seen.has(b.id)||!text(b.label,512)||!integer(b.role,1,4)||typeof b.enabled!=='boolean'||!integer(b.unsupported,0,5))throw Error('native_interaction_invalid');seen.add(b.id);total+=bytes(b.label);buttons.push({id:b.id,label:b.label,role:b.role,enabled:b.enabled,unsupported:b.unsupported});}
 seen.clear();for(const f of q.fields){if(!f||!id(f.id)||seen.has(f.id)||!text(f.label,512)||!integer(f.type,1,7)||typeof f.required!=='boolean'||typeof f.digitsOnly!=='boolean'||!integer(f.minBytes,0,4096)||!integer(f.maxBytes,f.minBytes,4096))throw Error('native_interaction_invalid');seen.add(f.id);total+=bytes(f.label);fields.push({id:f.id,label:f.label,type:f.type,required:f.required,minBytes:f.minBytes,maxBytes:f.maxBytes,digitsOnly:f.digitsOnly});}
 if(total>24576)throw Error('native_interaction_invalid');
 return {protocolVersion:1,scope:q.scope,attemptId:q.attemptId,requestId:q.requestId,expiresAfterMs:q.expiresAfterMs,kind:q.kind,unsupported:q.unsupported,textTruncated:q.textTruncated,title:q.title,message:q.message,buttons,fields};
}
function answer(q,a){
 q=request(q);if(!q||!a||a.protocolVersion!==1||a.scope!==q.scope||a.attemptId!==q.attemptId||a.requestId!==q.requestId||!Array.isArray(a.values)||a.values.length>16)throw Error('native_interaction_rejected');
 const base={protocolVersion:1,scope:q.scope,attemptId:q.attemptId,requestId:q.requestId,disposition:a.disposition,values:[]};
 if([2,3].includes(a.disposition)){if(a.buttonId||a.values.length)throw Error('native_interaction_rejected');return base;}
 const b=q.buttons.find(b=>b.id===a.buttonId&&b.enabled&&b.unsupported===0);
 if(a.disposition!==1||q.kind===6||q.unsupported!==0||!b)throw Error('native_interaction_rejected');base.buttonId=b.id;
 if(b.role===3){if(a.values.length)throw Error('native_interaction_rejected');return base;}
 const seen=new Set();let total=0;
 for(const v of a.values){const f=q.fields.find(f=>f.id===v?.fieldId);if(!f||f.type===6||seen.has(v.fieldId)||!text(v.value,f.maxBytes)||bytes(v.value)<f.minBytes||(f.required&&!v.value)||(f.digitsOnly&&!/^[0-9]*$/.test(v.value)))throw Error('native_interaction_rejected');seen.add(v.fieldId);total+=bytes(v.value);base.values.push({fieldId:v.fieldId,value:v.value});}
 if(total>16384||q.fields.some(f=>f.required&&!seen.has(f.id)))throw Error('native_interaction_rejected');return base;
}
module.exports={request,answer};

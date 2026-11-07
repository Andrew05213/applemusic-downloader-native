'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const ui=require('../../src/renderer/native-auth-presentation');
test('authentication failure is readable Chinese with original Apple text retained',()=>{
 const q={kind:2,unsupported:0,title:'Check the account information you entered and try again.',message:'',fields:[]};
 const v=ui.view(q);assert.equal(v.title,'Apple 账户验证未通过');assert.equal(v.message,'请检查输入的 Apple 账户信息，然后重新输入。');assert.equal(v.original,q.title);assert.equal(v.showOriginal,true);assert.match(v.note,/不能确定是密码错误/);
 assert.equal(ui.button(q,{id:'1',role:3,label:'Cancel'}),'取消登录');
 assert.equal(ui.button(q,{id:'2',role:4,label:'Retry'}),'重新输入');
 assert.equal(ui.field({type:7,label:'Email'}),'Apple 账户');
 assert.equal(ui.field({type:2,label:'Password'}),'密码');
});
test('unknown Apple instructions are never guessed, hidden or used as action IDs',()=>{
 const q={kind:3,unsupported:0,title:'Unrecognized account notice',message:'<b>PRIVATE_NOTICE</b>',fields:[]};
 const v=ui.view(q);assert.match(v.message,/Unrecognized account notice/);assert.match(v.message,/<b>PRIVATE_NOTICE<\/b>/);assert.equal(v.showOriginal,false);assert.equal(v.title,'Apple 账户验证');
 const b={id:'18446744073709551615',role:4,label:'Retry'};ui.button(q,b);assert.equal(b.id,'18446744073709551615');assert.equal(b.label,'Retry');
 assert.equal(ui.view({...q,kind:6,unsupported:2}).unsupported,true);
});

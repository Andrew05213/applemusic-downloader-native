'use strict';
// Display-only translations. Never use labels/text to choose SDK actions.
((root,factory)=>{if(typeof module==='object'&&module.exports)module.exports=factory();else root.nativeAuthPresentation=factory();})(globalThis,()=>{
 const normalized=s=>String(s||'').trim().replace(/\s+/g,' ').toLowerCase();
 const translations=new Map([
  ['check the account information you entered and try again.','请检查输入的 Apple 账户信息，然后重新输入。'],
  ['check the account information you entered and try again','请检查输入的 Apple 账户信息，然后重新输入。'],
  ['sign in','登录 Apple 账户'],['sign in to apple music','登录 Apple Music'],
  ['use existing apple id','使用现有 Apple 账户'],['use existing apple account','使用现有 Apple 账户'],
  ['use existing account','使用现有账户'],['create new apple id','创建新 Apple 账户'],
  ['create new apple account','创建新 Apple 账户'],['verification code','验证码'],
 ]);
 const translate=s=>translations.get(normalized(s))||s||'';
 function button(q,b){
  if(b.role===3)return '取消登录';
  if(b.role===4)return '重新输入';
  if(b.role===2)return q.kind===5?'验证':'继续';
  return translate(b.label)||'继续';
 }
 function field(f){return ({2:'密码',3:'Apple 账户',5:'验证码',7:'Apple 账户'})[f.type]||translate(f.label)||'输入内容';}
 function view(q){
  const unsupported=q.kind===6||q.unsupported!==0||q.fields.some(f=>f.type===6);
  const title=unsupported?'暂不支持此验证步骤':({1:'选择登录方式',2:'Apple 账户验证未通过',3:'Apple 账户验证',4:'确认账户信息',5:'输入设备验证码'})[q.kind]||'Apple 账户验证';
  const source=[q.title,q.message].filter(Boolean),message=source.map(translate).filter((v,i,a)=>v&&a.indexOf(v)===i).join('\n');
  const note=unsupported?'此步骤暂不支持在原生窗口完成。不会自动打开链接或执行其他操作。':q.kind===2?'Apple 拒绝了本次认证；仅凭此提示不能确定是密码错误。':q.kind===5?'在受信任设备上批准本次登录，填写本次收到的六位验证码。':q.textTruncated?'提示内容过长，已截断显示。':'';
  return {title,message,note,unsupported,original:source.join('\n'),showOriginal:source.some(s=>translate(s)!==s)};
 }
 return Object.freeze({view,button,field});
});

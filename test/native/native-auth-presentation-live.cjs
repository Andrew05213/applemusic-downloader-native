'use strict';
// Browser-only rendering fixture. No Electron backend or real account is used.
const {chromium}=require('playwright'),{expect}=require('@playwright/test');
const path=require('node:path'),fs=require('node:fs'),assert=require('node:assert/strict'),{pathToFileURL}=require('node:url');
async function main(){
 const output=path.resolve('.artifacts/auth-retry-ui');fs.mkdirSync(output,{recursive:true});
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:460,height:600}});
  await page.addInitScript(()=>{
   window.fixture={q:null,answers:[],logins:[],state:{state:'needs_interaction'}};
   window.nativeDesktop={onSession:fn=>{fixture.onSession=fn;return()=>{};},getState:async()=>fixture.state,getInteraction:async()=>fixture.q,answerInteraction:async a=>{fixture.answers.push(structuredClone(a));fixture.q=null;return {accepted:true};},login:async input=>{fixture.logins.push(input);return {state:'needs_login'};},cancelLogin:async()=>{},recheck:async()=>fixture.state};
  });
  await page.goto(pathToFileURL(path.resolve('src/renderer/native-login.html')).href);
  const base={protocolVersion:1,scope:'a'.repeat(32),attemptId:'18446744073709551615',requestId:'9007199254740993',expiresAfterMs:120000,kind:2,unsupported:0,textTruncated:false,title:'Check the account information you entered and try again.',message:'',fields:[],buttons:[{id:'1',role:3,label:'Cancel',enabled:true,unsupported:0},{id:'2',role:4,label:'Retry',enabled:true,unsupported:0}]};
  const show=async q=>{await page.evaluate(q=>{fixture.q=q},q);await expect(page.locator('#auth-interaction')).toBeVisible();};
  await show(base);await expect(page.locator('#interaction-title')).toHaveText('Apple 账户验证未通过');
  await expect(page.locator('#interaction-message')).toHaveText('请检查输入的 Apple 账户信息，然后重新输入。');
  await expect(page.locator('.login-actions')).toBeHidden();
  assert.equal(await page.getByRole('button',{name:/取消|暂不登录|Cancel/}).count(),1);
  assert.equal(await page.getByRole('button',{name:'重新输入',exact:true}).count(),1);
  await page.screenshot({path:path.join(output,'failure-prompt.png')});
  await page.getByText('查看 Apple 原始提示',{exact:true}).click();
  await expect(page.locator('#interaction-original-text')).toHaveText(base.title);
  await page.getByRole('button',{name:'重新输入',exact:true}).click();
  const credentials={...base,kind:4,requestId:'9007199254740994',title:'Sign In',buttons:[{...base.buttons[1],role:2},{...base.buttons[0]}],fields:[{id:'7',type:7,label:'Email',required:true,minBytes:1,maxBytes:512,digitsOnly:false},{id:'8',type:2,label:'Password',required:true,minBytes:1,maxBytes:4096,digitsOnly:false}]};
  const fields=page.locator('#interaction-fields');
  await show(credentials);await fields.getByLabel('Apple 账户',{exact:true}).fill(' corrected@example.invalid ');
  const secret=' PRIVATE:pass\\\"&<密码> ';
  await fields.getByLabel('密码',{exact:true}).fill(secret);await page.getByRole('button',{name:'继续',exact:true}).click();
  await expect(page.locator('#auth-interaction')).toBeHidden();
  await show({...base,requestId:'9007199254740995'});await page.getByRole('button',{name:'重新输入',exact:true}).click();
  await show({...credentials,requestId:'9007199254740996'});
  await expect(fields.getByLabel('Apple 账户',{exact:true})).toHaveValue('corrected@example.invalid');
  await expect(fields.getByLabel('密码',{exact:true})).toHaveValue('');
  const answers=await page.evaluate(()=>fixture.answers);assert.equal(answers[1].values[0].value,'corrected@example.invalid');assert.equal(answers[1].values[1].value,secret);
  await page.getByRole('button',{name:'取消登录',exact:true}).click();await expect(page.locator('#auth-interaction')).toBeHidden();
  const last=await page.evaluate(()=>fixture.answers.at(-1));assert.equal(last.buttonId,'1');assert.deepEqual(last.values,[]);
  // Phone-only Apple accounts must not encounter HTML email validation or
  // lose the leading '+' when passed through the interaction payload.
  for(const [i,account] of ['10000000000','+8610000000000','+911000000000'].entries()){
   await show({...credentials,requestId:String(9000+i)});
   await fields.getByLabel('Apple 账户',{exact:true}).fill(account);await fields.getByLabel('密码',{exact:true}).fill(secret);
   await page.getByRole('button',{name:'继续',exact:true}).click();await expect(page.locator('#auth-interaction')).toBeHidden();
   assert.equal((await page.evaluate(()=>fixture.answers.at(-1))).values[0].value,account.startsWith('+')?account:'+86'+account);
  }
  await page.evaluate(()=>fixture.onSession({state:'needs_login'}));
  const initial=page.locator('#account-fields'),email=page.locator('#login-email'),password=page.locator('#login-password');
  await email.fill('10000000000');await expect(initial.getByRole('combobox')).toHaveValue('+86');
  await page.screenshot({path:path.join(output,'phone-login.png')});
  await password.fill(secret);await page.getByRole('button',{name:'登录',exact:true}).click();
  assert.equal((await page.evaluate(()=>fixture.logins.at(-1))).appleEmail,'+8610000000000');
  await email.fill('1000000000');await initial.getByRole('combobox').selectOption('+91');await password.fill(secret);await page.getByRole('button',{name:'登录',exact:true}).click();
  assert.equal((await page.evaluate(()=>fixture.logins.at(-1))).appleEmail,'+911000000000');
  await email.fill('+8610000000000');await expect(initial.getByRole('combobox')).toBeDisabled();await password.fill(secret);await page.getByRole('button',{name:'登录',exact:true}).click();
  assert.equal((await page.evaluate(()=>fixture.logins.at(-1))).appleEmail,'+8610000000000');
  await email.fill('Name+86@example.invalid');await expect(initial.locator('.account-phone-options')).toBeHidden();await password.fill(secret);await page.getByRole('button',{name:'登录',exact:true}).click();
  const emailLogin=await page.evaluate(()=>fixture.logins.at(-1));assert.equal(emailLogin.appleEmail,'Name+86@example.invalid');assert.equal(emailLogin.applePassword,secret);
  await email.fill('200000000');await initial.getByRole('combobox').selectOption('other');await initial.getByLabel('其他国家区号',{exact:true}).fill('+358');await password.fill(secret);await page.getByRole('button',{name:'登录',exact:true}).click();
  assert.equal((await page.evaluate(()=>fixture.logins.at(-1))).appleEmail,'+358200000000');
  await show({...credentials,requestId:'10001'});await fields.getByLabel('Apple 账户',{exact:true}).fill('1000000000');await fields.getByRole('combobox').selectOption('+91');await fields.getByLabel('密码',{exact:true}).fill(secret);
  await page.getByRole('button',{name:'继续',exact:true}).click();await expect(page.locator('#auth-interaction')).toBeHidden();
  assert.equal((await page.evaluate(()=>fixture.answers.at(-1))).values[0].value,'+911000000000');
  console.log(JSON.stringify({passed:true,chineseFailure:true,oneCancel:true,originalRetained:true,retryKeepsCorrectedAccount:true,passwordUnchangedInTransit:true,retryPasswordCleared:true,defaultChinaCode:true,otherCountryAndCustomCode:true,internationalNotDuplicated:true,emailUnchanged:true,initialAndRetryPhoneNormalized:true,realAccountsUsed:0,output}));
 }finally{await browser.close();}
}
main().catch(e=>{console.error(e);process.exitCode=1;});

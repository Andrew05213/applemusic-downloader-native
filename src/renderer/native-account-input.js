'use strict';
// Account formatting is UI-only. Never change passwords or infer a country
// from a bare, ambiguous number; '+' and '00' explicitly mark international.
((root,factory)=>{if(typeof module==='object'&&module.exports)module.exports=factory();else root.nativeAccountInput=factory();})(globalThis,()=>{
 const regions=[
  ['中国大陆','+86',11],['中国香港','+852',8],['中国澳门','+853',8],['中国台湾','+886',9],
  ['新加坡','+65',8],['美国 / 加拿大','+1',10],['英国','+44',10],['澳大利亚','+61',9],
  ['日本','+81',10],['韩国','+82',10],['印度','+91',10],['德国','+49',0],['法国','+33',9],
 ];
 const invalid=()=>{throw Error('native_phone_invalid');};
 function international(value){if(!/^\+[1-9][0-9]{5,14}$/.test(value))return invalid();return value;}
 function normalize(raw,dial='+86'){
  const value=String(raw??'').trim();
  if(!value||value.includes('@'))return value;
  const compact=value.replace(/[\s().-]/g,'');
  if(!/^[+0-9]+$/.test(compact)){if(value.startsWith('+'))return invalid();return value;}
  if(!/^\+?[0-9]+$/.test(compact))return invalid();
  if(compact.startsWith('+'))return international(compact);
  if(compact.startsWith('00'))return international('+'+compact.slice(2));
  if(!/^\+[1-9][0-9]{0,2}$/.test(dial))return invalid();
  const region=regions.find(r=>r[1]===dial),prefix=dial.slice(1);
  // Also accept an unambiguous selected-country prefix without '+' (e.g.
  // 86 + an eleven-digit China mainland number). Do not guess other prefixes.
  if(region?.[2]&&compact.startsWith(prefix)&&compact.length===prefix.length+region[2])return international('+'+compact);
  return international(dial+compact);
 }
 let serial=0;
 function attach(input,{dial='+86',onDialChange=()=>{}}={}){
  const document=input.ownerDocument,id='account-phone-'+(++serial);
  const controls=document.createElement('div');controls.className='account-phone-options';
  const label=document.createElement('label'),caption=document.createElement('span'),select=document.createElement('select');
  caption.textContent='国家/地区区号';select.setAttribute('aria-label','国家/地区区号');
  for(const [name,code]of regions){const option=document.createElement('option');option.value=code;option.textContent=name+' '+code;select.append(option);}
  const other=document.createElement('option');other.value='other';other.textContent='其他国家/地区';select.append(other);
  label.append(caption,select);controls.append(label);
  const customLabel=document.createElement('label'),custom=document.createElement('input'),customCaption=document.createElement('span');
  customCaption.textContent='国家区号';custom.type='text';custom.inputMode='tel';custom.maxLength=4;custom.placeholder='+49';custom.autocomplete='off';custom.setAttribute('aria-label','其他国家区号');customLabel.append(customCaption,custom);controls.append(customLabel);
  const hint=document.createElement('p');hint.id=id;hint.className='account-phone-hint';hint.setAttribute('aria-live','polite');
  const previousDescription=input.getAttribute('aria-describedby');input.setAttribute('aria-describedby',[previousDescription,id].filter(Boolean).join(' '));
  const anchor=input.closest('label')||input;anchor.after(controls,hint);
  const code=()=>select.value==='other'?custom.value.trim():select.value;
  function setDial(value){if(regions.some(r=>r[1]===value))select.value=value;else{select.value='other';custom.value=value;}refresh();}
  function refresh(){
   const raw=input.value.trim(),email=raw.includes('@'),explicit=!email&&/^(\+|00)/.test(raw);
   controls.hidden=email;select.disabled=explicit;custom.disabled=explicit;customLabel.hidden=select.value!=='other';
   if(email){hint.textContent='邮箱地址保持原样，无需国家区号。';return;}
   try{
    const formatted=normalize(raw,code());
    hint.textContent=explicit?'号码已带国际区号，不会重复添加。':raw&&formatted!==raw?'将使用 '+formatted+' 登录。':'手机号会补上所选国家区号，默认中国大陆 +86；已带 + 区号的号码直接使用。';
   }catch{hint.textContent='请检查手机号和国家区号；完整号码可写成 +86138…，区号只填写一次。';}
  }
  const change=()=>{refresh();onDialChange(code());};
  input.addEventListener('input',refresh);select.addEventListener('change',change);custom.addEventListener('input',change);
  setDial(dial);
  return {read:()=>normalize(input.value,code()),dial:code,setDial,refresh,dispose(){
   input.removeEventListener('input',refresh);select.removeEventListener('change',change);custom.removeEventListener('input',change);
   if(previousDescription===null)input.removeAttribute('aria-describedby');else input.setAttribute('aria-describedby',previousDescription);
   controls.remove();hint.remove();
  }};
 }
 return Object.freeze({normalize,attach});
});

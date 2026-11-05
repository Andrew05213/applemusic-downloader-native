'use strict';
// Decorative only: no timers, API calls, awaited animations or layout changes.
// An interrupted animation must never delay navigation, playback or deletion.
(function(root,create){
 if(typeof module==='object'&&module.exports)module.exports={createNativeMotion:create};
 else root.nativeMotion=create(root);
})(typeof window==='object'?window:globalThis,function(host){
 const active=new Map(),feedbackKeys=new WeakMap();
 const preference=host.matchMedia?.('(prefers-reduced-motion: reduce)');
 const easing='cubic-bezier(0.2, 0, 0, 1)';
 function cancel(element){const animation=active.get(element);if(animation){active.delete(element);animation.cancel();}}
 function cancelTree(root){for(const element of active.keys())if(element===root||root?.contains?.(element))cancel(element);}
 function cancelAll(){for(const element of [...active.keys()])cancel(element);}
 function visible(element){
  if(!element||element.isConnected===false||element.closest?.('[hidden]')||host.document?.hidden)return false;
  const rect=element.getBoundingClientRect?.();
  return !rect||(rect.width>0&&rect.height>0&&rect.bottom>0&&rect.right>0&&rect.top<(host.innerHeight||Infinity)&&rect.left<(host.innerWidth||Infinity));
 }
 function enter(element,kind='content',direction=1,delay=0){
  cancel(element);
  if(preference?.matches||typeof element?.animate!=='function'||!visible(element))return;
  // Don't animate a whole view and its children at the same time.
  for(const parent of active.keys())if(parent!==element&&parent.contains?.(element))return;
  const duration={page:420,content:260,panel:340,feedback:200,card:260}[kind]||260;
  const offset=kind==='page'||direction<0?`translateX(${direction<0?-8:8}px)`:kind==='panel'?'translateY(8px) scale(0.99)':`translateY(${kind==='feedback'?4:10}px)`;
  let animation;
  try{animation=element.animate([{opacity:0.4,transform:offset},{opacity:1,transform:'none'}],{duration,easing,delay:Math.min(100,Math.max(0,delay)),fill:'backwards'});}catch{return;}
  active.set(element,animation);
  const release=()=>{if(active.get(element)===animation)active.delete(element);};
  animation.onfinish=release;animation.oncancel=release;
  return animation;
 }
 function content(root,direction=1){
  cancelTree(root);
  // Animate a few visible groups, not every row in a potentially huge playlist.
  const groups=[...(root?.children||[])].filter(element=>element.matches?.('.detail-hero,.artist-overview,.catalog-grid,.search-best,.search-song-list,.track-list')&&visible(element)).slice(0,3);
  groups.forEach((element,index)=>enter(element,'content',direction,index*45));
 }
 function feedback(element,key){
  const text=String(key??'');if(!element||feedbackKeys.get(element)===text)return;
  feedbackKeys.set(element,text);if(text)enter(element,'feedback');else cancel(element);
 }
 function dispose(){cancelAll();preference?.removeEventListener?.('change',onPreference);host.document?.removeEventListener?.('visibilitychange',onVisibility);host.removeEventListener?.('pagehide',cancelAll);}
 function onPreference(){if(preference.matches)cancelAll();}
 function onVisibility(){if(host.document.hidden)cancelAll();}
 preference?.addEventListener?.('change',onPreference);
 host.document?.addEventListener?.('visibilitychange',onVisibility);
 host.addEventListener?.('pagehide',cancelAll);
 return {enter,content,feedback,cancelTree,cancelAll,dispose};
});

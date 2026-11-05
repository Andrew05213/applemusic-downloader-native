'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {createNativeMotion}=require('../../src/renderer/native-motion');
function fixture(reduced=false){
 const listeners=new Map(),documentListeners=new Map(),hostListeners=new Map();
 const preference={matches:reduced,addEventListener:(name,fn)=>listeners.set(name,fn),removeEventListener:name=>listeners.delete(name)};
 const host={innerHeight:800,innerWidth:1200,matchMedia:()=>preference,document:{hidden:false,addEventListener:(name,fn)=>documentListeners.set(name,fn),removeEventListener:name=>documentListeners.delete(name)},addEventListener:(name,fn)=>hostListeners.set(name,fn),removeEventListener:name=>hostListeners.delete(name)};
 const element=()=>({isConnected:true,calls:[],children:[],closest(){return this.hiddenAncestor||null;},contains(other){return this.children.includes(other);},matches(){return true;},getBoundingClientRect(){return this.rect||{width:200,height:100,top:10,bottom:110,left:10,right:210};},animate(frames,options){const animation={cancelled:false,cancel(){this.cancelled=true;this.oncancel?.();}};this.calls.push({frames,options,animation});return animation;}});
 return {motion:createNativeMotion(host),host,preference,listeners,documentListeners,hostListeners,element};
}
test('motion uses bounded eased entrances and cancels rapid replacements',()=>{
 const f=fixture(),el=f.element();const first=f.motion.enter(el,'page',-1);
 assert.equal(el.calls[0].options.duration,420);assert.equal(el.calls[0].options.easing,'cubic-bezier(0.2, 0, 0, 1)');assert.equal(el.calls[0].frames[0].transform,'translateX(-8px)');assert.equal(el.calls[0].frames[1].opacity,1);
 const second=f.motion.enter(el,'panel');assert.equal(first.cancelled,true);first.onfinish();f.motion.cancelAll();assert.equal(second.cancelled,true);
});
test('large catalogs animate at most three visible groups with a small stagger budget',()=>{
 const f=fixture(),root=f.element(),offscreen=f.element();offscreen.rect={width:200,height:100,top:900,bottom:1000,left:0,right:200};
 root.children=[offscreen,...Array.from({length:100},()=>f.element())];f.motion.content(root);
 const calls=root.children.flatMap(el=>el.calls);assert.equal(calls.length,3);assert.deepEqual(calls.map(c=>c.options.delay),[0,45,90]);assert.equal(offscreen.calls.length,0);
 const parent=f.element(),child=f.element();parent.children=[child];f.motion.enter(parent,'page');f.motion.enter(child,'content');assert.equal(child.calls.length,0);
});
test('polling identical feedback never replays the animation and empty feedback cancels it',()=>{
 const f=fixture(),el=f.element();f.motion.feedback(el,'正在处理');f.motion.feedback(el,'正在处理');assert.equal(el.calls.length,1);
 f.motion.feedback(el,'');assert.equal(el.calls[0].animation.cancelled,true);f.motion.feedback(el,'已完成');assert.equal(el.calls.length,2);
});
test('reduced motion is respected initially and cancels running animations when changed',()=>{
 const f=fixture(true),el=f.element();f.motion.enter(el);assert.equal(el.calls.length,0);
 f.preference.matches=false;f.motion.enter(el);assert.equal(el.calls.length,1);
 f.listeners.get('change')();assert.equal(el.calls[0].animation.cancelled,false,'restoring motion must not cancel a newly started effect');
 f.preference.matches=true;f.listeners.get('change')();assert.equal(el.calls[0].animation.cancelled,true);f.motion.enter(el);assert.equal(el.calls.length,1);
});
test('hidden, detached and unsupported elements are safe; hiding a document releases motion',()=>{
 const f=fixture(),el=f.element();el.isConnected=false;f.motion.enter(el);el.isConnected=true;el.hiddenAncestor={};f.motion.enter(el);delete el.hiddenAncestor;assert.equal(el.calls.length,0);
 f.motion.enter(el);f.host.document.hidden=true;f.documentListeners.get('visibilitychange')();assert.equal(el.calls[0].animation.cancelled,true);f.motion.enter(el);assert.equal(el.calls.length,1);
 f.host.document.hidden=false;assert.doesNotThrow(()=>f.motion.enter({isConnected:true}));f.motion.enter(el);f.hostListeners.get('pagehide')();assert.equal(el.calls.at(-1).animation.cancelled,true);
 f.motion.dispose();assert.equal(f.listeners.size+f.documentListeners.size+f.hostListeners.size,0);
});
test('motion assets are packaged locally and CSS has reduced-motion and bounded busy effects',()=>{
 const root=path.resolve(__dirname,'../..'),{UI_FILES}=require('../../electron/native-package');
 const {files}=require('../../electron-builder.native.cjs');
 for(const name of ['native-motion.js','native-motion.css']){assert.ok(UI_FILES.includes('src/renderer/'+name));assert.ok(files.includes('src/renderer/'+name),'installer must contain '+name);}
 for(const file of ['native-index.html','native-login.html']){const html=fs.readFileSync(path.join(root,'src/renderer',file),'utf8');assert.ok(html.includes('src="native-motion.js"'));assert.ok(html.includes('href="native-motion.css"'));}
 const css=fs.readFileSync(path.join(root,'src/renderer/native-motion.css'),'utf8');assert.match(css,/@media\(prefers-reduced-motion:reduce\)/);assert.doesNotMatch(css,/infinite|transition:\s*all|will-change/);
});

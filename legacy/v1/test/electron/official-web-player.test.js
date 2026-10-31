'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const {cookieToken,officialURL,createOfficialWebPlayer,PLAY,STOP}=require('../../electron/official-web-player');
const token='fixture-user-token-'.repeat(8);
test('fixed page adapter observes real audio, never hides an unconfirmed preview',()=>{
 const vm=require('node:vm');let clicks=0;
 const media={paused:false,ended:false,currentTime:2,duration:30,pause(){this.paused=true;}};
 const document={querySelectorAll:selector=>selector==='audio,video'?[media]:[{getAttribute:()=> '播放',disabled:false,click(){clicks++;}}]};
 assert.equal(vm.runInNewContext(PLAY,{document}),'unconfirmed');assert.equal(clicks,0);
 media.duration=180;assert.equal(vm.runInNewContext(PLAY,{document}),'unconfirmed');
 const window={MusicKit:{getInstance:()=>({nowPlayingItem:{playbackType:3}})}};
 assert.equal(vm.runInNewContext(PLAY,{document,window}),'playing');
 assert.equal(vm.runInNewContext(STOP,{document}),'stopped');assert.equal(media.paused,true);
 assert.equal(vm.runInNewContext(PLAY,{document}),'waiting');assert.equal(clicks,1);
});
test('only exact secure live named Music cookie is accepted',()=>{
 const good={name:'media-user-token',domain:'.music.apple.com',path:'/',secure:true,value:token};
 assert.equal(cookieToken(good),token);
 for(const change of [{name:'session'},{domain:'.apple.com'},{domain:'evil.music.apple.com'},{secure:false},{expirationDate:1},{value:'short'}])assert.equal(cookieToken({...good,...change}),'');
 assert.equal(officialURL('https://appleid.apple.com/auth'),true);
 assert.equal(officialURL('https://music.apple.com.evil.invalid/'),false);
});
test('consent gates cookie reads; hidden playback and close retain session',async()=>{
 const {EventEmitter}=require('node:events');let reads=0,delivered=0,consent=false;
 class Window extends EventEmitter {
  constructor(options){super();this.options=options;Window.latest=this;this.webContents=new EventEmitter();this.webContents.session={cookies:new EventEmitter()};this.webContents.session.cookies.get=async filter=>{reads++;assert.deepEqual(filter,{url:'https://music.apple.com/',name:'media-user-token'});return [{name:'media-user-token',domain:'music.apple.com',secure:true,value:token}];};this.webContents.setWindowOpenHandler=()=>{};this.webContents.getURL=()=>this.url;this.webContents.executeJavaScript=async()=> 'playing';}
  isDestroyed(){return false;}removeMenu(){}async loadURL(url){this.url=url;}show(){this.visible=true;}focus(){}hide(){this.visible=false;}destroy(){this.emit('closed');}
 }
 const controller=createOfficialWebPlayer({BrowserWindow:Window,components:{WIDEVINE_CDM_ID:'fixture',whenReady:async()=>[]},owner:()=>null,consent:async()=>consent,deliver:async update=>{assert.deepEqual(update,{mediaUserToken:token});delivered++;return {saved:true,pending:true};}});
 await controller.open();assert.equal(reads,0);assert.equal(Window.latest.visible,true);
 consent=true;await controller.open();assert.equal(delivered,1);
 assert.equal((await controller.open()).syncState,'saved_pending');assert.equal(delivered,1);
 await controller.open({type:'songs',id:'123',title:'Fixture'});assert.equal(Window.latest.visible,false);
 assert.equal(Window.latest.options.webPreferences.backgroundThrottling,false);
 assert.equal((await controller.stop()).ok,true);
 let prevented=false;Window.latest.emit('close',{preventDefault(){prevented=true;}});assert.equal(prevented,true);
 controller.destroy();assert.equal(Window.latest.webContents.session.cookies.listenerCount('changed'),0);
});
test('ambiguous cookie values never overwrite credentials; delivery failure is not success',async()=>{
 const {EventEmitter}=require('node:events');let mode='ambiguous',deliveries=0;
 class Window extends EventEmitter {
  constructor(){super();this.webContents=new EventEmitter();this.webContents.session={cookies:new EventEmitter()};this.webContents.session.cookies.get=async()=>mode==='ambiguous'?[token,token+'other'].map(value=>({name:'media-user-token',domain:'music.apple.com',secure:true,value})):[{name:'media-user-token',domain:'music.apple.com',secure:true,value:token}];this.webContents.setWindowOpenHandler=()=>{};}
  isDestroyed(){return false;}removeMenu(){}async loadURL(){}show(){}focus(){}destroy(){this.emit('closed');}
 }
 const p=createOfficialWebPlayer({BrowserWindow:Window,consent:async()=>true,deliver:async()=>{deliveries++;throw Error('secret-sentinel');}});
 assert.equal((await p.open()).syncState,'ambiguous');assert.equal(deliveries,0);
 mode='single';const result=await p.open();assert.equal(result.syncState,'failed');assert.equal(JSON.stringify(result).includes('sentinel'),false);p.destroy();
});

test('unavailable or preview-only full playback stays hidden for automatic fallback',async()=>{
 const {EventEmitter}=require('node:events');
 class Window extends EventEmitter {
  constructor(){super();Window.latest=this;this.webContents=new EventEmitter();this.webContents.session={cookies:new EventEmitter()};this.webContents.setWindowOpenHandler=()=>{};this.webContents.getURL=()=>this.url;this.webContents.executeJavaScript=async()=> 'unconfirmed';}
  isDestroyed(){return false;}removeMenu(){}async loadURL(url){this.url=url;}show(){this.visible=true;}focus(){}hide(){this.visible=false;}destroy(){this.emit('closed');}
 }
 for(const components of [undefined,{WIDEVINE_CDM_ID:'fixture',whenReady:async()=>[]}]){
  const player=createOfficialWebPlayer({BrowserWindow:Window,components,consent:async()=>false,deliver:async()=>{throw Error('not used');}});
  const result=await player.open({type:'songs',id:'123',title:'Fixture'});
  assert.notEqual(result.playing,true);assert.equal(Window.latest.visible,false);
  await player.open();assert.equal(Window.latest.visible,true); // explicit login still works
  player.destroy();
 }
});

test('stopping while the official page loads cancels the pending full play request',async()=>{
 const {EventEmitter}=require('node:events');let finishLoad;
 class Window extends EventEmitter {
  constructor(){super();Window.latest=this;this.webContents=new EventEmitter();this.webContents.session={cookies:new EventEmitter()};this.webContents.setWindowOpenHandler=()=>{};this.webContents.getURL=()=>this.url;this.webContents.executeJavaScript=async()=> 'playing';this.loads=[];}
  isDestroyed(){return false;}removeMenu(){}loadURL(url){this.url=url;this.loads.push(url);return this.loads.length===1?new Promise(resolve=>{finishLoad=resolve;}):Promise.resolve();}show(){this.visible=true;}focus(){}hide(){this.visible=false;}destroy(){this.emit('closed');}
 }
 const player=createOfficialWebPlayer({BrowserWindow:Window,components:{WIDEVINE_CDM_ID:'fixture',whenReady:async()=>[]},consent:async()=>false,deliver:async()=>{throw Error('not used');}});
 const opening=player.open({type:'songs',id:'123',title:'Fixture'});
 await new Promise(resolve=>setImmediate(resolve));
 await player.stop();finishLoad();
 assert.equal((await opening).code,'playback_superseded');
 assert.deepEqual(Window.latest.loads,['https://music.apple.com/']);
 player.destroy();
});

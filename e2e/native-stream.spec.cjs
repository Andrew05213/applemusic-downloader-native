'use strict';
const {test,expect}=require('@playwright/test');
const fs=require('node:fs');const path=require('node:path');const {pathToFileURL}=require('node:url');
const directory=process.env.AMDL_STREAM_OUTPUT;
test.skip(!directory,'requires generated AAC fixture transformed by TestStreamGeneratedAACFixture');
// Bundled Playwright Chromium excludes AAC. Use the installed codec-enabled
// browser for this opt-in real-decoder acceptance test; no official web player.
test.use({channel:process.env.AMDL_STREAM_BROWSER||'msedge'});
const receipt=directory?JSON.parse(fs.readFileSync(path.join(directory,'receipt.json'))):null;
const chunks=directory?Object.fromEntries(['init',...receipt.stream.segments.map((_,i)=>'segment/'+i)].map(key=>[key,fs.readFileSync(path.join(directory,key==='init'?'init.mp4':`segment-${key.split('/')[1]}.m4s`)).toString('base64')])):{};
test.afterEach(async({page},info)=>{if(info.status!==info.expectedStatus){console.log(await page.evaluate(()=>({mode:document.querySelector('#audio')?.dataset.mode,error:document.querySelector('#audio')?.error?.message,status:document.querySelector('#player-status')?.textContent,supported:MediaSource.isTypeSupported('audio/mp4; codecs="mp4a.40.2"'),chunks:window.chunkCalls})));await page.screenshot({path:info.outputPath('failure.png')});}});
async function install(page,options={}){
 await page.addInitScript(({receipt,chunks,options})=>{
  window.chunkCalls=[];window.calls=[];window.cancelCalls=[];window.pendingChunks=[];window.previewCalls=0;
  const item=(id,title)=>({id,title,artist:'Fixture Artist',type:'songs',url:'https://music.apple.com/us/song/'+id,downloadable:true,duration:42000,previewUrl:'https://audio-ssl.itunes.apple.com/fixture.wav'});
  window.nativeDesktop={
   getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady(fn){queueMicrotask(fn)},openLogin:async()=>{},
   cancelPlaybackChunks:async base=>{window.cancelCalls.push(base);for(const p of window.pendingChunks)if(p.base===base)p.reject(Error('cancelled'));window.pendingChunks=window.pendingChunks.filter(p=>p.base!==base);},
   playbackChunk:async target=>{
    window.chunkCalls.push(target);const base=target.slice(0,53),key=target.slice(54);
    if(options.block===key&&base.endsWith('a'.repeat(32)))await new Promise((resolve,reject)=>window.pendingChunks.push({base,resolve,reject}));
    if(options.fail===key)throw Error('fixture_segment_failed');
    return Uint8Array.from(atob(chunks[key]),c=>c.charCodeAt(0));
   },
   api:async(target,options={})=>{
    window.calls.push({target,...options});
    if(target==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true},atmos:{available:true}}};
    if(target.startsWith('/api/v1/catalog/'))return {groups:[{type:'songs',items:[item('100','Stream A'),item('102','Stream B')]}]};
    if(target==='/api/native/playback')return options.method==='DELETE'?{ok:true}:{...receipt,streamPath:'/api/native/playback/'+(options.body.id==='100'?'a':'b').repeat(32)};
    if(target==='/api/v1/browser-download/resolve')return {url:options.body.url};
    if(target==='/api/v1/jobs'&&options.method==='POST')return {jobs:[{id:'123',url:options.body.urls[0]}]};
    if(target==='/api/v1/jobs')return {jobs:[]};
    if(target==='/api/v1/settings/download')return {settings:{songFileFormat:'{title}'}};
    throw Error('unexpected fixture API');
   }
  };
 },{receipt,chunks,options});
 await page.goto(pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href);
}

test('real AAC starts before tail, seeks directly and keeps ALAC downloads independent',async({page})=>{
 const errors=[];page.on('pageerror',e=>errors.push(e.message));await install(page,{block:'segment/1'});
 await page.getByRole('button',{name:'播放 Stream A',exact:true}).click();
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(.1);
 await page.locator('#audio').evaluate(a=>a.pause());
 expect(await page.evaluate(()=>chunkCalls.map(x=>x.slice(54)))).toEqual(['init','segment/0','segment/1']);
 await expect(page.locator('#audio')).toHaveAttribute('data-mode','完整播放');
 await page.getByRole('button',{name:'下载 Stream A',exact:true}).click();await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
 expect(await page.evaluate(()=>calls.find(x=>x.target==='/api/v1/jobs'&&x.method==='POST').body.quality)).toBe('alac');
 await page.locator('#audio').evaluate(a=>{a.currentTime=32;void a.play()});
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(32.1);
 const requested=await page.evaluate(()=>chunkCalls.map(x=>x.slice(54)));expect(requested).toContain('segment/16');
 expect(requested).not.toContain('segment/2');expect(requested).not.toContain('segment/10');
 await page.locator('#audio').evaluate(a=>{a.currentTime=0;void a.play()});
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(.1);
 await expect(page.locator('#audio')).toHaveAttribute('data-mode','完整播放');
 expect(errors).toEqual([]);await page.getByRole('button',{name:'停止播放',exact:true}).click();await expect(page.locator('#player')).toBeHidden();
});

test('paused streaming preloads a bounded window, not the whole song',async({page})=>{
 await install(page);await page.getByRole('button',{name:'播放 Stream A',exact:true}).click();
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(.1);await page.locator('#audio').evaluate(a=>a.pause());
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.buffered.length?a.buffered.end(0)-a.currentTime:0)).toBeGreaterThanOrEqual(20);
 const before=await page.evaluate(()=>chunkCalls.length);await page.waitForTimeout(1100);expect(await page.evaluate(()=>chunkCalls.length)).toBe(before);
 expect(before).toBeLessThan(receipt.stream.segments.length);expect(await page.locator('#audio').evaluate(a=>a.buffered.end(0))).toBeLessThan(24);
});

test('switching songs aborts old pending fragments without falling back the new song',async({page})=>{
 const errors=[];page.on('pageerror',e=>errors.push(e.message));await install(page,{block:'segment/1'});
 await page.getByRole('button',{name:'播放 Stream A',exact:true}).click();await expect.poll(()=>page.evaluate(()=>pendingChunks.length)).toBe(1);
 await page.getByRole('button',{name:'播放 Stream B',exact:true}).click();await expect(page.locator('#player-title')).toHaveText('Stream B · Fixture Artist');
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(.1);
 await expect(page.locator('#audio')).toHaveAttribute('data-mode','完整播放');expect(await page.evaluate(()=>pendingChunks.length)).toBe(0);expect(errors).toEqual([]);
});

test('returning to an old buffered range refills the gap after it',async({page})=>{
 await install(page);await page.getByRole('button',{name:'播放 Stream A',exact:true}).click();
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(.1);await page.locator('#audio').evaluate(a=>a.pause());
 await expect.poll(()=>page.locator('#audio').evaluate(a=>a.buffered.end(0)-a.currentTime)).toBeGreaterThanOrEqual(20);
 await page.locator('#audio').evaluate(a=>{a.currentTime=33;void a.play()});await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(33.1);
 await page.locator('#audio').evaluate(a=>{a.pause();a.currentTime=20;void a.play()});await expect.poll(()=>page.locator('#audio').evaluate(a=>a.currentTime)).toBeGreaterThan(20.1);
 await expect.poll(()=>page.locator('#audio').evaluate(a=>{for(let i=0;i<a.buffered.length;i++)if(a.buffered.start(i)<=20&&a.buffered.end(i)>20)return a.buffered.end(i);return 0})).toBeGreaterThan(40);
 await expect(page.locator('#audio')).toHaveAttribute('data-mode','完整播放');
});

test('a late failed fragment falls back once to a clearly labelled real preview',async({page})=>{
 let previews=0;await page.route('https://audio-ssl.itunes.apple.com/fixture.wav',r=>{previews++;const out=Buffer.alloc(44+48000*2);out.write('RIFF');out.writeUInt32LE(out.length-8,4);out.write('WAVEfmt ',8);out.writeUInt32LE(16,16);out.writeUInt16LE(1,20);out.writeUInt16LE(1,22);out.writeUInt32LE(48000,24);out.writeUInt32LE(96000,28);out.writeUInt16LE(2,32);out.writeUInt16LE(16,34);out.write('data',36);out.writeUInt32LE(out.length-44,40);return r.fulfill({contentType:'audio/wav',body:out});});
 await install(page,{fail:'segment/1'});await page.getByRole('button',{name:'播放 Stream A',exact:true}).click();
 await expect(page.locator('#audio')).toHaveAttribute('data-mode','试听');await expect.poll(()=>previews).toBe(1);await expect(page.locator('#player-status')).toContainText('试听');
});

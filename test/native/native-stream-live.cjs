'use strict';
// Real Electron -> sandbox preload -> bounded binary IPC -> capability HTTP ->
// MSE playback. Generated sine-wave fixture only; isolated profile, no account.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict'),crypto=require('node:crypto');
const root=path.resolve(__dirname,'../..'),input=process.env.AMDL_STREAM_OUTPUT;
if(!input)throw Error('AMDL_STREAM_OUTPUT must name the generated fixture');
if(!process.versions.electron){
 const output=path.join(root,'.tmp-v2-native','stream-electron-'+crypto.randomUUID());fs.mkdirSync(output,{recursive:true});
 const env={...process.env};delete env.ELECTRON_RUN_AS_NODE;
 const result=require('node:child_process').spawnSync(require('electron'),[__filename,'--output',output],{env,windowsHide:true,encoding:'utf8',timeout:45000,maxBuffer:1024*1024});
 process.stdout.write(result.stdout||'');if(result.status!==0)process.stderr.write(result.stderr||String(result.error||'electron failed'));process.exitCode=result.status===0?0:1;
}else{
 const {app,BrowserWindow,ipcMain,session}=require('electron'),http=require('node:http');const {EventEmitter}=require('node:events');const {PassThrough}=require('node:stream');
 const {NativeService}=require('../../electron/native-service');
 const output=process.argv[process.argv.indexOf('--output')+1];app.setPath('userData',path.join(output,'profile'));app.commandLine.appendSwitch('autoplay-policy','no-user-gesture-required');
 app.whenReady().then(async()=>{
  const receipt=JSON.parse(fs.readFileSync(path.join(input,'receipt.json'))),calls=[];let token,win,service,server;
  try{
   server=http.createServer((req,res)=>{
    assert.equal(req.headers.authorization,'Bearer '+token);calls.push(req.url);
    if(req.url.startsWith(receipt.streamPath+'/')){
     const key=req.url.slice(receipt.streamPath.length+1);if(key==='segment/1')return; // stalled future fragment must not delay first sound or seek
     const file=key==='init'?'init.mp4':`segment-${key.split('/')[1]}.m4s`;
     const data=fs.readFileSync(path.join(input,file));res.setHeader('Content-Type','audio/mp4');res.end(data);return;
    }
    res.setHeader('Content-Type','application/json');
    if(req.url==='/api/native/playback'){res.end(JSON.stringify(req.method==='POST'?receipt:{ok:true}));return}
    if(req.url==='/api/v1/capabilities'){res.end(JSON.stringify({queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true}}}));return}
    if(req.url.startsWith('/api/v1/catalog/')){res.end(JSON.stringify({groups:[{type:'songs',items:[{id:'100',type:'songs',title:'Streaming Fixture',artist:'Fixture',downloadable:true,url:'https://music.apple.com/us/song/100'}]}]}));return}
    if(req.url.startsWith('/api/v1/jobs')){res.end('{"jobs":[]}');return}
    if(req.url==='/api/v1/settings/download'){res.end('{"settings":{"songFileFormat":"{title}"}}');return}
    res.end('{}');
   });await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
   const child=new EventEmitter();child.stdout=new PassThrough();child.stderr=new PassThrough();child.stdin=new PassThrough();child.kill=()=>child.emit('close',1);
   child.stdin.once('data',raw=>{token=JSON.parse(raw).apiAccessToken;queueMicrotask(()=>child.stdout.write(JSON.stringify({nativeServer:true,version:require('../../package.json').version,port:server.address().port})+'\n'))});child.stdin.once('finish',()=>child.emit('close',0));
   service=new NativeService({executable:'fixture',manifest:'fixture',dataDirectory:output,outputDirectory:output,guestPort:12340,spawnChild:()=>child});await service.ready;
   const partition=session.fromPartition('stream-fixture-'+crypto.randomUUID());partition.webRequest.onBeforeRequest({urls:['http://*/*','https://*/*']},(_details,callback)=>callback({cancel:true}));
   win=new BrowserWindow({show:false,width:1280,height:850,webPreferences:{session:partition,preload:path.join(root,'src/preload/native-preload.js'),sandbox:true,contextIsolation:true,nodeIntegration:false,backgroundThrottling:false}});
   const bind=(name,fn)=>ipcMain.handle(name,(event,...args)=>{assert.equal(event.sender,win.webContents);assert.equal(event.senderFrame,win.webContents.mainFrame);return fn(...args)});
   bind('native:state',()=>({state:'authenticated',region:'us'}));bind('native:api',(target,options)=>target.startsWith('/api/v1/catalog/library')?{groups:[]}:service.api(target,options));bind('native:playback-chunk',target=>service.playbackChunk(target));bind('native:playback-cancel',base=>service.cancelPlaybackChunks(base));
   const evaluate=source=>win.webContents.executeJavaScript(source,true);
   const wait=async source=>{const start=Date.now();while(!await evaluate(source)){if(Date.now()-start>10000)throw Error('fixture wait failed: '+source);await new Promise(resolve=>setTimeout(resolve,25))}};
   await win.loadFile(path.join(root,'src/renderer/native-index.html'));
   win.webContents.send('native:backend-ready');
   assert.equal(await evaluate(`MediaSource.isTypeSupported('audio/mp4; codecs="mp4a.40.2"')`),true,'shipping Electron has no AAC MSE');
   await wait(`document.querySelector('[aria-label="播放 Streaming Fixture"]')`);await evaluate(`document.querySelector('[aria-label="播放 Streaming Fixture"]').click()`);
   await wait(`document.querySelector('#audio').currentTime>.1`);
   assert.equal(await evaluate(`document.querySelector('#audio').dataset.mode`),'完整播放');
   assert.ok(!calls.includes(receipt.streamPath+'/segment/2'));
   await evaluate(`document.querySelector('#audio').currentTime=32`);await wait(`document.querySelector('#audio').currentTime>32.1`);
   assert.ok(calls.includes(receipt.streamPath+'/segment/16'));assert.ok(!calls.includes(receipt.streamPath+'/segment/2'));
   assert.equal(await evaluate(`document.querySelectorAll('iframe,webview').length`),0);
   await evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');await new Promise(resolve=>setTimeout(resolve,250));
   await fs.promises.writeFile(path.join(output,'stream.png'),(await win.webContents.capturePage()).toPNG());
   await evaluate(`document.querySelector('#stop-playback').click()`);await wait(`document.querySelector('#player').hidden`);
   await new Promise(resolve=>setTimeout(resolve,100));assert.equal(service.playbackRequests.size,0);
   const evidence={passed:true,electron:process.versions.electron,isolatedProfile:true,actualSandboxPreload:true,capabilityHTTP:true,firstFragmentBeforeStalledTail:true,directSeek:true,existingUI:true,noRemoteBrowser:true};
   fs.writeFileSync(path.join(output,'evidence.json'),JSON.stringify(evidence,null,2));console.log(JSON.stringify({...evidence,evidence:path.join(output,'evidence.json')}));
  }catch(error){console.error(JSON.stringify({calls}));if(win)fs.writeFileSync(path.join(output,'failure.png'),(await win.webContents.capturePage()).toPNG());throw error}
  finally{win?.destroy();await service?.close();server?.closeAllConnections();server?.close()}
  app.exit(0);
 }).catch(error=>{console.error(error);app.exit(1)});
}

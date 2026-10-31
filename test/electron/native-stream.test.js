'use strict';
const test=require('node:test');const assert=require('node:assert/strict');const http=require('node:http');
const {EventEmitter}=require('node:events');const {PassThrough}=require('node:stream');
const {NativeService,allowedAPI}=require('../../electron/native-service');

async function fixture(t,handler){
 let token;const server=http.createServer((req,res)=>{assert.equal(req.headers.authorization,'Bearer '+token);handler(req,res)});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 t.after(()=>{server.closeAllConnections();server.close()});
 const child=new EventEmitter();child.stdout=new PassThrough();child.stderr=new PassThrough();child.stdin=new PassThrough();child.kill=()=>child.emit('close',1);
 child.stdin.once('data',raw=>{token=JSON.parse(raw).apiAccessToken;queueMicrotask(()=>child.stdout.write(JSON.stringify({nativeServer:true,version:require('../../package.json').version,port:server.address().port})+'\n'))});
 child.stdin.once('finish',()=>child.emit('close',0));
 const service=new NativeService({executable:'fixture',manifest:'fixture',dataDirectory:'fixture',outputDirectory:'fixture',guestPort:12340,spawnChild:()=>child});t.after(()=>service.close());await service.ready;return service;
}
const base='/api/native/playback/'+'a'.repeat(32);
test('binary playback transport accepts only bounded private fragment paths and strips HTTP details',async t=>{
 let count=0;const service=await fixture(t,(req,res)=>{count++;res.setHeader('Content-Type','audio/mp4');res.end(Buffer.from([1,2,3,4]))});
 const result=await service.playbackChunk(base+'/segment/9');assert.ok(result instanceof Uint8Array);assert.deepEqual([...result],[1,2,3,4]);
 for(const target of [base,base+'/../init',base+'/init?url=https://example.com',base+'/segment/10000','https://example.com/init','/session/credentials']){
  await assert.rejects(service.playbackChunk(target),/native_playback_path_invalid/);assert.equal(allowedAPI('GET',target),false);
 }
 assert.equal(count,1);
});
test('binary transport rejects redirects, wrong types, empty and oversized payloads',async t=>{
 const service=await fixture(t,(req,res)=>{
  const index=Number(req.url.split('/').at(-1));
  if(index===0){res.writeHead(302,{location:'https://example.invalid/'});res.end();return}
  res.setHeader('Content-Type',index===1?'text/html':'audio/mp4');
  if(index===2)res.setHeader('Content-Length',8*1024*1024+1);
  res.end(index===3?'':'not audio');
 });
 for(let i=0;i<4;i++)await assert.rejects(service.playbackChunk(base+'/segment/'+i),/native_playback_segment_failed/);
 assert.equal(service.playbackRequests.size,0);
});
test('seeking cancels only the old fragment, not another stream or download API',async t=>{
 const seen=new Map();const service=await fixture(t,(req,res)=>{seen.set(req.url,res)});
 const a=service.playbackChunk(base+'/segment/1').catch(e=>e.message);
 const bPath='/api/native/playback/'+'b'.repeat(32)+'/segment/1';const b=service.playbackChunk(bPath);
 const job=service.api('/api/v1/jobs');
 while(seen.size<3)await new Promise(resolve=>setTimeout(resolve,5));
 service.cancelPlaybackChunks(base);assert.equal(await a,'native_playback_cancelled');
 const bResponse=seen.get(bPath);bResponse.setHeader('Content-Type','audio/mp4');bResponse.end('data');
 seen.get('/api/v1/jobs').end('{"jobs":[]}');
 assert.equal((await b).byteLength,4);assert.deepEqual(await job,{jobs:[]});
});
test('stopping playback aborts an outstanding preparation without cancelling downloads',async t=>{
 let prepared=false,jobResponse;const service=await fixture(t,(req,res)=>{
  if(req.url==='/api/native/playback'&&req.method==='POST'){prepared=true;return}
  if(req.url==='/api/v1/jobs'){jobResponse=res;return}
  res.end('{"ok":true}');
 });
 const prepare=service.api('/api/native/playback',{method:'POST',body:{id:'123',region:'us'}}).catch(()=>false);
 const jobs=service.api('/api/v1/jobs');while(!prepared||!jobResponse)await new Promise(resolve=>setTimeout(resolve,5));
 await service.api('/api/native/playback',{method:'DELETE'});assert.equal(await prepare,false);jobResponse.end('{"jobs":[]}');assert.deepEqual(await jobs,{jobs:[]});
});

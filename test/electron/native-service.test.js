'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const { EventEmitter } = require('node:events');
const { PassThrough } = require('node:stream');
const { NativeService, allowedAPI } = require('../../electron/native-service');

function pendingService() {
  const child = new EventEmitter();
  for (const name of ['stdout','stderr','stdin']) child[name] = new PassThrough();
  child.kill = () => child.emit('close', 1);
  const service = new NativeService({executable:'fixture.exe',manifest:'fixture.json',dataDirectory:'fixture-data',outputDirectory:'fixture-output',guestPort:12340,spawnChild:()=>child});
  const receipt = () => child.stdout.write(JSON.stringify({nativeServer:true,version:require('../../package.json').version,port:12345})+'\n');
  return {child,service,receipt};
}

test('bootstrap deadline fails closed and ignores late receipts without resetting its timer', async t => {
  t.mock.timers.enable({apis:['setTimeout']});
  const {child,service,receipt} = pendingService();
  const rejected = assert.rejects(service.ready, /native_service_timeout/);
  t.mock.timers.tick(29000);
  child.stdout.write('not a receipt\n');
  t.mock.timers.tick(1000);
  await rejected;
  assert.equal(service.failed, true);
  assert.equal(child.stdin.writableEnded, true);
  receipt();
  assert.equal(service.origin, '');
  await assert.rejects(service.api('/api/native/state'), /native_api_unavailable/);
  child.emit('close', 0); await service.completion;
});

test('a fresh service can start after a timed-out bootstrap; valid readiness clears deadline', async t => {
  t.mock.timers.enable({apis:['setTimeout']});
  const first = pendingService();
  const rejected = assert.rejects(first.service.ready, /native_service_timeout/);
  t.mock.timers.tick(30000); await rejected;
  first.child.emit('close', 0); await first.service.completion;
  const next = pendingService();
  t.mock.timers.tick(29999); next.receipt();
  assert.equal(await next.service.ready, 'http://127.0.0.1:12345');
  t.mock.timers.tick(60000);
  assert.equal(next.service.failed, false);
  assert.equal(next.child.stdin.writableEnded, false);
  next.child.emit('close', 0); await next.service.completion;
});

test('oversized bootstrap output fails closed before any receipt can be accepted', async () => {
  const {child,service,receipt} = pendingService();
  const rejected = assert.rejects(service.ready, /native_service_protocol_invalid/);
  child.stdout.write('x'.repeat(131073)); await rejected; receipt();
  assert.equal(service.origin, ''); assert.equal(service.failed, true);
  child.emit('close', 0); await service.completion;
});

test('native service uses fixed argv, stdin capability, private headers and complete receipts', async (t) => {
  let token; let call;
  const server = http.createServer((req, res) => {
    assert.equal(req.headers.authorization, 'Bearer ' + token);
    if(req.url==='/api/v1/jobs/abc/record'){res.writeHead(204);res.end();return;}
    if(req.url==='/api/native/playback'){res.setHeader('Content-Type','application/json');res.end(JSON.stringify({ok:true,mode:'full',mediaPath:'/api/native/playback/'+'a'.repeat(32)}));return;}
    res.setHeader('Content-Type','application/json');res.end(JSON.stringify({runtime:{phase:'ready'},session:{state:'needs_login'}}));
  }); await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));t.after(()=>server.close());
  const child = new EventEmitter();child.stdout=new PassThrough();child.stderr=new PassThrough();child.stdin=new PassThrough();child.kill=()=>child.emit('close',1);
  child.stdin.once('data',raw=>{token=JSON.parse(raw).apiAccessToken;queueMicrotask(()=>child.stdout.write(JSON.stringify({nativeServer:true,version:require('../../package.json').version,port:server.address().port})+'\n'));});
  child.stdin.once('finish',()=>child.emit('close',0));
  const service=new NativeService({executable:'fixed.exe',manifest:'bundle.json',dataDirectory:'own-data',outputDirectory:'output',guestPort:12340,toolDirectory:'own-tools',spawnChild:(...args)=>{call=args;return child;}});
  t.after(()=>service.close());await service.ready;
  assert.equal(call[2].shell,false);assert.equal(call[2].windowsHide,true);assert.deepEqual(call[1],['__native-desktop','--manifest','bundle.json','--data-dir','own-data','--output-dir','output','--guest-port','12340']);
  assert.equal(call[1].includes(token),false);assert.equal(Object.values(call[2].env).includes(token),false);assert.equal(token.length,64);
  assert.equal((await service.api('/api/native/state')).session.state,'needs_login');
  assert.deepEqual(await service.api('/api/v1/jobs/abc/record',{method:'DELETE'}),{ok:true});
  const play=await service.api('/api/native/playback',{method:'POST',body:{id:'123',region:'us'}});assert.equal(play.mediaURL,service.origin+play.mediaPath);
  assert.equal(service.mediaHeaders({webContentsId:1,url:play.mediaURL,requestHeaders:{}},1).Authorization,'Bearer '+token);
  assert.equal(service.mediaHeaders({webContentsId:2,url:play.mediaURL,requestHeaders:{}},1).Authorization,undefined);
  await assert.rejects(service.api('https://example.com'),/native_api_unavailable/);
  await service.close();assert.equal(service.closed,true);
});

test('native renderer API does not expose tokens, arbitrary origins or guest routes',()=>{
  for(const target of ['/api/native/state','/api/v1/catalog/home?region=us','/api/v1/catalog/discover?region=cn','/api/v1/jobs'])assert.equal(allowedAPI('GET',target),true);
  for(const target of ['https://example.com','/session/credentials','/api/native/tokens','/api/v1/jobs#other','/api/../native/state','/api/v1/catalog/charts'])assert.equal(allowedAPI('GET',target),false);
  assert.equal(allowedAPI('POST','/api/v1/catalog/discover?region=cn'),false);
});
test('changing discovery region supersedes the older discovery request without cancelling the library',async t=>{
  const {child,service,receipt}=pendingService();
  t.after(()=>child.emit('close',0));
  receipt();await service.ready;
  let finishLibrary;
  const libraryDone=new Promise(resolve=>{finishLibrary=resolve;});
  t.mock.method(globalThis,'fetch',async(url,{signal})=>{
    const path=new URL(url).pathname;
    if(path==='/api/v1/catalog/library'){
      await libraryDone;
      signal.throwIfAborted();
      return Response.json({library:true});
    }
    if(url.endsWith('region=us'))return new Promise((resolve,reject)=>signal.addEventListener('abort',()=>reject(signal.reason),{once:true}));
    return Response.json({storefront:'cn'});
  });
  const older=assert.rejects(service.api('/api/v1/catalog/discover?region=us'),{name:'AbortError'});
  const library=service.api('/api/v1/catalog/library?region=cn');
  assert.deepEqual(await service.api('/api/v1/catalog/discover?region=cn'),{storefront:'cn'});
  finishLibrary();
  await older;
  assert.deepEqual(await library,{library:true});
});
test('retired SMS control routes are blocked for every method',()=>{
  for(const target of ['/api/native/sms/send','/api/native/sms/code']){
    assert.equal(allowedAPI('POST',target),false);
    for(const method of ['GET','PUT','DELETE'])assert.equal(allowedAPI(method,target),false);
    assert.equal(allowedAPI('POST',target+'?token=sentinel'),false);
  }
  for(const target of ['/api/native/sms','/api/native/sms/send/','/api/native/sms/voice','/api/native/sms/send/other'])assert.equal(allowedAPI('POST',target),false);
});

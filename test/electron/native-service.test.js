'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const { EventEmitter } = require('node:events');
const { PassThrough } = require('node:stream');
const { NativeService, allowedAPI } = require('../../electron/native-service');

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
  for(const target of ['/api/native/state','/api/v1/catalog/home?region=us','/api/v1/jobs'])assert.equal(allowedAPI('GET',target),true);
  for(const target of ['https://example.com','/session/credentials','/api/native/tokens','/api/v1/jobs#other','/api/../native/state'])assert.equal(allowedAPI('GET',target),false);
});
test('retired SMS control routes are blocked for every method',()=>{
  for(const target of ['/api/native/sms/send','/api/native/sms/code']){
    assert.equal(allowedAPI('POST',target),false);
    for(const method of ['GET','PUT','DELETE'])assert.equal(allowedAPI(method,target),false);
    assert.equal(allowedAPI('POST',target+'?token=sentinel'),false);
  }
  for(const target of ['/api/native/sms','/api/native/sms/send/','/api/native/sms/voice','/api/native/sms/send/other'])assert.equal(allowedAPI('POST',target),false);
});

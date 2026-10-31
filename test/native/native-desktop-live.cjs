'use strict';
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');const net=require('node:net');const assert=require('node:assert/strict');
const {NativeService}=require('../../electron/native-service');
const root=path.resolve(__dirname,'../..');
async function main(){
  const manifest=path.resolve(process.argv[2]||'');assert.equal(JSON.parse(fs.readFileSync(manifest)).isolated,true);
  const directory=path.join(root,'.tmp-v2-native','desktop-'+crypto.randomUUID());fs.mkdirSync(directory,{recursive:true});
  const listener=net.createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
  const service=new NativeService({executable:path.join(root,'electron/bin/amdl-service.exe'),manifest,dataDirectory:path.join(directory,'data'),outputDirectory:path.join(directory,'downloads'),guestPort:port,toolDirectory:path.join(root,'electron/bin')});
  const evidence={scope:'native-desktop-real-exe-isolated',realAccountsUsed:0,mediaRequestsAttempted:0};
  try{
    await service.ready;assert.equal((await fetch(service.origin+'/api/native/state')).status,401);
    // Exercise the actual host router, not just the JS API allowlist. The
    // fixture is isolated, and these retired local routes must have no handler.
    for(const retired of ['/api/native/sms/send','/api/native/sms/code']){
      const response=await fetch(service.origin+retired,{method:'POST',headers:{Authorization:'Bearer '+service.token,'Content-Type':'application/json'},body:'{}'});
      assert.equal(response.status,404);await response.arrayBuffer();
    }
    let state;const deadline=Date.now()+90000;
    do{state=await service.api('/api/native/state');if(state.runtime.phase==='failed')throw new Error('native_fixture_runtime_failed');if(state.session.state==='needs_login')break;await new Promise(resolve=>setTimeout(resolve,250));}while(Date.now()<deadline);
    assert.equal(state.runtime.phase,'ready');assert.equal(state.session.state,'needs_login');assert.equal(state.runtime.sessionVerified,false);
    await assert.rejects(service.api('/api/native/login',{method:'POST',body:{appleEmail:'fixture@example.invalid',applePassword:'SENTINEL_PASSWORD'}}),/native_runtime_isolated/);
    await assert.rejects(service.api('/api/v1/jobs',{method:'POST',body:{urls:['https://music.apple.com/us/song/123'],quality:'aac-lc',existingPolicy:'skip'}}),/native_auth_required/);
    assert.equal((await service.api('/api/v1/settings/download-concurrency')).downloadConcurrency,3);
    assert.deepEqual((await service.api('/api/v1/jobs')).jobs,[]);
    await service.close();assert.equal(await service.completion,0);assert.equal(fs.readFileSync(path.join(directory,'data/jobs.db')).includes(Buffer.from('SENTINEL_PASSWORD')),false);
    const rebind=net.createServer();await new Promise((resolve,reject)=>{rebind.on('error',reject);rebind.listen(port,'127.0.0.1',resolve)});await new Promise(resolve=>rebind.close(resolve));
    evidence.passed=true;evidence.emptySessionDetected=true;evidence.unauthenticatedEnqueueRejected=true;evidence.retiredSMSRoutesRejected=true;evidence.defaultConcurrency=3;evidence.exitCode=0;
  }finally{await service.close();fs.writeFileSync(path.join(directory,'evidence.json'),JSON.stringify(evidence,null,2)+'\n');process.stdout.write(JSON.stringify({...evidence,evidence:path.join(directory,'evidence.json')})+'\n');}
}
main().catch(()=>{process.stderr.write('native_desktop_fixture_failed\n');process.exitCode=1;});

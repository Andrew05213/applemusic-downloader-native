'use strict';
// Actual Windows service, blank isolated runtime and fixture-only output tree.
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');const net=require('node:net');const assert=require('node:assert/strict');
const {DatabaseSync}=require('node:sqlite');const {NativeService}=require('../../electron/native-service');
const root=path.resolve(__dirname,'../..');
async function main(){
 const [manifestArgument,serviceArgument]=process.argv.slice(2);const manifest=path.resolve(manifestArgument);
 assert.equal(JSON.parse(fs.readFileSync(manifest)).isolated,true);
 const directory=path.join(root,'.tmp-v2-native','task-delete-'+crypto.randomUUID());fs.mkdirSync(directory,{recursive:true});
 const output=path.join(directory,'downloads'),data=path.join(directory,'data');fs.mkdirSync(output);
 const listener=net.createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
 const executable=path.resolve(serviceArgument||path.join(root,'electron/bin/amdl-service.exe'));
 const service=new NativeService({executable,manifest,dataDirectory:data,outputDirectory:output,guestPort:port,toolDirectory:path.join(root,'electron/bin')});
 const evidence={scope:'actual-windows-service-terminal-task-deletion',realAccountsUsed:0,mediaRequestsAttempted:0,serviceSHA256:crypto.createHash('sha256').update(fs.readFileSync(executable)).digest('hex')};
 let database;
 try{
  await service.ready;database=new DatabaseSync(path.join(data,'jobs.db'));database.exec('PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;');
  function fixture(name){
   const id=crypto.randomUUID(),fileID=crypto.randomUUID(),relative='fixture/'+name,file=path.join(output,relative);fs.mkdirSync(path.dirname(file),{recursive:true});fs.writeFileSync(file,Buffer.from('synthetic fixture; not actual media'));
   const bytes=fs.readFileSync(file),stat=fs.statSync(file,{bigint:true}),now=BigInt(Date.now())*1000000n;
   database.prepare('INSERT INTO jobs(id,url,title,quality,existing_policy,status,total,succeeded,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)').run(id,'https://music.apple.com/us/album/123','Fixture Album','alac','skip','succeeded',1,1,now,now);
   database.prepare('INSERT INTO job_files(id,job_id,relative_path,name,size,content_type,content_sha256,modified_at,created_at) VALUES(?,?,?,?,?,?,?,?,?)').run(fileID,id,relative,name,bytes.length,'audio/mp4',crypto.createHash('sha256').update(bytes).digest('hex'),stat.mtimeNs,now);
   return{id,file};
  }
  const retained=fixture('retained.m4a');assert.equal((await service.api('/api/v1/jobs/'+retained.id)).title,'Fixture Album');
  await service.api('/api/v1/jobs/'+retained.id+'/record',{method:'DELETE'});assert.equal(fs.existsSync(retained.file),true);await assert.rejects(service.api('/api/v1/jobs/'+retained.id),/not_found/);evidence.recordOnlyPreservesFile=true;
  const removed=fixture('中文-track.m4a');await service.api('/api/v1/jobs/'+removed.id,{method:'DELETE'});assert.equal(fs.existsSync(removed.file),false);await assert.rejects(service.api('/api/v1/jobs/'+removed.id),/not_found/);evidence.productionFileDeletionVerified=true;
  const nested=fixture('empty-artist/empty-album/disc-1/song.m4a');await service.api('/api/v1/jobs/'+nested.id,{method:'DELETE'});
  assert.equal(fs.existsSync(path.join(output,'fixture','empty-artist')),false);assert.equal(fs.existsSync(output),true);evidence.emptyDirectoryCleanupVerified=true;
  const shared=fixture('shared-artist/shared-album/song.m4a'),unowned=path.join(path.dirname(shared.file),'notes.txt');fs.writeFileSync(unowned,'user-owned fixture');
  await service.api('/api/v1/jobs/'+shared.id,{method:'DELETE'});assert.equal(fs.readFileSync(unowned,'utf8'),'user-owned fixture');assert.equal(fs.existsSync(path.dirname(shared.file)),true);evidence.nonemptyDirectoryPreserved=true;
  const readonly=fixture('readonly.m4a');fs.chmodSync(readonly.file,0o444);
  try{await assert.rejects(service.api('/api/v1/jobs/'+readonly.id,{method:'DELETE'}),/file_access_denied/);assert.equal(fs.existsSync(readonly.file),true);}finally{fs.chmodSync(readonly.file,0o666);}
  await service.api('/api/v1/jobs/'+readonly.id,{method:'DELETE'});assert.equal(fs.existsSync(readonly.file),false);evidence.permissionFailureAndRetryVerified=true;
  database.close();database=null;await service.close();evidence.passed=true;
 }finally{if(database)database.close();await service.close();fs.writeFileSync(path.join(directory,'evidence.json'),JSON.stringify(evidence,null,2)+'\n');process.stdout.write(JSON.stringify({...evidence,evidence:path.join(directory,'evidence.json')})+'\n');}
}
main().catch(error=>{process.stderr.write('native_task_delete_fixture_failed: '+error.message+'\n');process.exitCode=1;});

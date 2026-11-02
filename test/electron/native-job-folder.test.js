'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),vm=require('node:vm');
const root=path.resolve(__dirname,'../..');
function fixture(result){
 const handlers=new Map(),requests=[],opened=[],revealed=[];
 const frame={url:''},webContents={mainFrame:frame},win={webContents,isDestroyed:()=>false};
 const context=vm.createContext({require(name){
  if(name==='electron')return {app:{whenReady:()=>new Promise(()=>{}),on(){}},ipcMain:{handle:(name,handler)=>handlers.set(name,handler)},shell:{openPath:async target=>{opened.push(target);return '';},showItemInFolder:target=>revealed.push(target)}};
  if(name.startsWith('../../electron/'))return require(path.join(root,name.slice(6)));
  return require(name);
 },__dirname:path.join(root,'src/main'),process,console,URL,setInterval,clearInterval,fixtureWindow:win,fixtureAPI:async target=>{requests.push(target);return result;}});
 vm.runInContext(fs.readFileSync(path.join(root,'src/main/native-main.js'),'utf8'),context);
 vm.runInContext('owner=fixtureWindow;service={api:fixtureAPI};bindIPC();fixtureWindow.webContents.mainFrame.url=nativeURL;',context);
 return {invoke:id=>handlers.get('native:reveal-job-folder')({sender:webContents,senderFrame:frame},id),handlers,event:{sender:webContents,senderFrame:frame},requests,opened,revealed};
}
test('real 32-character job ID reveals its downloaded file',async t=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-folder-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 const file=path.join(directory,'歌曲.m4a');fs.writeFileSync(file,'fixture');
 const f=fixture({directory,file}),id='f23b778283aeef7d1f9eb3013ad4c3ad';
 assert.equal((await f.invoke(id)).revealed,true);
 assert.deepEqual(f.requests,['/api/native/jobs/'+id+'/location']);
 assert.deepEqual(f.revealed,[await fs.promises.realpath(file)]);assert.deepEqual(f.opened,[]);
});
test('job without an output file opens its saved directory',async t=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-folder-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 const f=fixture({directory,file:''});assert.equal((await f.invoke('a'.repeat(32))).revealed,false);
 assert.deepEqual(f.opened,[await fs.promises.realpath(directory)]);assert.deepEqual(f.revealed,[]);
});
test('malformed job IDs and untrusted senders cannot open folders',async()=>{
 const f=fixture({});
 for(const id of [undefined,{},'', 'a'.repeat(31),'A'.repeat(32),'../'+'a'.repeat(32),'12345678-1234-1234-1234-123456789abc'])await assert.rejects(f.invoke(id),/native_job_invalid/);
 await assert.rejects(f.handlers.get('native:reveal-job-folder')({...f.event,sender:{}},'a'.repeat(32)),/native_sender_rejected/);
 assert.deepEqual(f.requests,[]);assert.deepEqual(f.opened,[]);assert.deepEqual(f.revealed,[]);
});
test('missing directories and files outside the task root remain rejected',async t=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'native-folder-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 await assert.rejects(fixture({directory:path.join(directory,'missing')}).invoke('a'.repeat(32)),/native_directory_missing/);
 const nested=path.join(directory,'专辑');fs.mkdirSync(nested);const file=path.join(directory,'outside.m4a');fs.writeFileSync(file,'fixture');
 const f=fixture({directory:nested,file});await assert.rejects(f.invoke('a'.repeat(32)),/native_location_invalid/);
 assert.deepEqual(f.opened,[]);assert.deepEqual(f.revealed,[]);
});

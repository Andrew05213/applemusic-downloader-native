'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs'),path=require('node:path'),os=require('node:os');
const root=path.resolve(__dirname,'../..'),id='11111111-1111-4111-8111-111111111111';
function fixture(t){
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'native-directory-ipc-'));t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));const file=path.join(dir,'测试歌曲.m4a');fs.writeFileSync(file,'synthetic fixture');
 const handlers=new Map(),calls=[],shellCalls=[];const frame={url:''},win={isDestroyed:()=>false,webContents:{mainFrame:frame}};
 const state={result:{directory:dir,file},dialog:{canceled:false,filePaths:[dir]},openError:'',pending:null};
 const context=vm.createContext({require(name){if(name==='electron')return {app:{whenReady:()=>new Promise(()=>{}),on(){}},ipcMain:{handle:(name,action)=>handlers.set(name,action)},dialog:{showOpenDialog:async()=>state.pending?state.pending:state.dialog},shell:{showItemInFolder:target=>shellCalls.push({reveal:target}),openPath:async target=>{shellCalls.push({open:target});return state.openError;}}};return require(name);},__dirname:path.join(root,'src/main'),process,console,URL,setInterval,clearInterval,fixtureWindow:win,fixtureAPI:async(target,options)=>{calls.push({target,options});if(target==='/api/native/state')return {downloadDirectory:dir};if(target==='/api/native/download-directory')return {directory:options.body.directory};return state.result;}});
 vm.runInContext(fs.readFileSync(path.join(root,'src/main/native-main.js'),'utf8'),context);vm.runInContext('owner=fixtureWindow;service={api:fixtureAPI};bindIPC();',context);frame.url=vm.runInContext('nativeURL',context);
 return {dir,file,state,handlers,calls,shellCalls,event:{sender:win.webContents,senderFrame:frame}};
}

test('download directory picker uses only the native dialog selection; cancel never saves',async t=>{
 const f=fixture(t);const result=await f.handlers.get('native:choose-download-directory')(f.event,'C:\\untrusted-renderer-path');assert.equal(result.directory,f.dir);assert.equal(f.calls.find(call=>call.target==='/api/native/download-directory').options.body.directory,f.dir);
 f.state.dialog={canceled:true,filePaths:[]};f.calls.length=0;assert.equal((await f.handlers.get('native:choose-download-directory')(f.event)).canceled,true);assert.equal(f.calls.some(call=>call.options?.method==='PUT'),false);
});

test('directory picker coalesces requests and rejects foreign frames and generic API bypass',async t=>{
 const f=fixture(t);let release;f.state.pending=new Promise(resolve=>release=resolve);const pending=f.handlers.get('native:choose-download-directory')(f.event);await new Promise(resolve=>setImmediate(resolve));
 await assert.rejects(f.handlers.get('native:choose-download-directory')(f.event),/native_directory_busy/);release({canceled:true});await pending;
 for(const name of ['native:choose-download-directory','native:reveal-job-folder'])await assert.rejects(f.handlers.get(name)({sender:f.event.sender,senderFrame:{url:f.event.senderFrame.url}},id),/native_sender_rejected/);
 for(const target of ['/api/native/download-directory','/api/native/jobs/'+id+'/location'])await assert.rejects(f.handlers.get('native:api')(f.event,target,{method:target.includes('download-directory')?'PUT':'GET',body:{directory:f.dir}}),/native_api_rejected/);
});

test('folder reveal accepts a job ID, verifies the returned local file and never invokes a shell command',async t=>{
 const f=fixture(t);assert.equal((await f.handlers.get('native:reveal-job-folder')(f.event,id)).revealed,true);assert.deepEqual(f.shellCalls,[{reveal:fs.realpathSync(f.file)}]);
 await assert.rejects(f.handlers.get('native:reveal-job-folder')(f.event,'../../other'),/native_job_invalid/);
 f.state.result={directory:f.dir,file:''};assert.equal((await f.handlers.get('native:reveal-job-folder')(f.event,id)).revealed,false);assert.deepEqual(f.shellCalls[1],{open:fs.realpathSync(f.dir)});
 f.state.openError='synthetic failure';await assert.rejects(f.handlers.get('native:reveal-job-folder')(f.event,id),/native_folder_open_failed/);
});

test('reveal rejects locations outside the bound root and missing directories',async t=>{
 const f=fixture(t);f.state.result={directory:f.dir,file:__filename};await assert.rejects(f.handlers.get('native:reveal-job-folder')(f.event,id),/native_location_invalid/);assert.equal(f.shellCalls.length,0);
 f.state.result={directory:path.join(f.dir,'missing'),file:''};await assert.rejects(f.handlers.get('native:reveal-job-folder')(f.event,id),/native_directory_missing/);
});

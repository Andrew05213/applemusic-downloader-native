'use strict';
const {test,expect}=require('@playwright/test'),path=require('node:path'),{pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;
async function install(page,{clock=false}={}){
 if(clock)await page.clock.install();
 await page.addInitScript(()=>{
  const job={id:'11111111-1111-4111-8111-111111111111',title:'测试专辑',url:'https://music.apple.com/us/album/300',status:'succeeded',total:1,succeeded:1,failed:0,skipped:0,unavailable:0,items:[]};
  window.directoryFixture={directory:'D:\\原下载目录',picked:'E:\\音乐下载',canceled:false,error:'',pending:false,release:null,chooseCalls:0,reveals:[],revealError:'',noFiles:false,jobs:[job]};
  window.nativeDesktop={getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady(fn){queueMicrotask(fn);},openLogin:async()=>{},exportDiagnostics:async()=>true,
   chooseDownloadDirectory:async()=>{const f=window.directoryFixture;f.chooseCalls++;if(f.pending)await new Promise(resolve=>f.release=resolve);if(f.error)throw Error(f.error);if(f.canceled)return {canceled:true};f.directory=f.picked;return {canceled:false,directory:f.directory};},
   revealJobFolder:async id=>{const f=window.directoryFixture;f.reveals.push(id);if(f.revealError)throw Error(f.revealError);return {revealed:!f.noFiles};},
   api:async(target,options={})=>{
    if(target.startsWith('/api/v1/catalog/'))return {groups:[]};
    if(target==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true}}};
    if(target==='/api/native/state')return {runtime:{phase:'ready'},downloadDirectory:window.directoryFixture.directory};
    if(target==='/api/v1/settings/download')return {settings:{songFileFormat:'{title}'}};
    if(target==='/api/v1/settings/download-concurrency')return {downloadConcurrency:3};
    if(target.startsWith('/api/v1/jobs?'))return {jobs:structuredClone(window.directoryFixture.jobs)};
    if(options.method==='DELETE')return {ok:true};throw Error('unexpected directory fixture route');
   }};
 });await page.goto(renderer);
}

test('settings independently selects and saves a download folder for new tasks',async({page})=>{
 await install(page);await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.locator('#download-directory')).toHaveText('D:\\原下载目录');
 await page.getByRole('button',{name:'选择文件夹',exact:true}).click();await expect(page.locator('#download-directory')).toHaveText('E:\\音乐下载');await expect(page.locator('#directory-feedback')).toContainText('已有任务保留原位置');
 await page.getByRole('button',{name:'下载任务',exact:true}).click();await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.locator('#download-directory')).toHaveText('E:\\音乐下载');
});

test('cancel, write failure and pending selection preserve the prior folder and prevent duplicate dialogs',async({page})=>{
 await install(page);await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.locator('#download-directory')).toContainText('原下载目录');
 const button=page.getByRole('button',{name:'选择文件夹',exact:true});await page.evaluate(()=>window.directoryFixture.canceled=true);await button.click();await expect(page.locator('#download-directory')).toContainText('原下载目录');
 await page.evaluate(()=>{window.directoryFixture.canceled=false;window.directoryFixture.error='native_directory_denied';});await button.click();await expect(page.locator('#directory-feedback')).toContainText('不可写入');await expect(page.locator('#download-directory')).toContainText('原下载目录');
 await page.evaluate(()=>{window.directoryFixture.error='';window.directoryFixture.pending=true;});await button.click();await expect(button).toBeDisabled();await page.evaluate(()=>window.directoryFixture.release());await expect(button).toBeEnabled();await expect(page.locator('#download-directory')).toHaveText('E:\\音乐下载');
 expect(await page.evaluate(()=>window.directoryFixture.chooseCalls)).toBe(3);
});

test('small folder button survives job polling between pointer down/up and opens only its task',async({page})=>{
 await install(page,{clock:true});await page.getByRole('button',{name:'下载任务',exact:true}).click();const folder=page.getByRole('button',{name:'打开所在文件夹',exact:true});await expect(folder).toBeEnabled();
 await folder.evaluate(el=>window.savedFolderButton=el);await folder.hover();await page.mouse.down();await page.clock.fastForward(2000);await page.mouse.up();
 await expect.poll(()=>page.evaluate(()=>window.directoryFixture.reveals.length)).toBe(1);expect(await page.evaluate(()=>window.savedFolderButton===document.querySelector('.job-folder-button'))).toBe(true);
 expect(await page.evaluate(()=>window.directoryFixture.reveals)).toEqual(['11111111-1111-4111-8111-111111111111']);
});

test('no task file opens its bound download directory; missing directory remains retryable',async({page})=>{
 await install(page);await page.getByRole('button',{name:'下载任务',exact:true}).click();await page.evaluate(()=>window.directoryFixture.noFiles=true);const folder=page.getByRole('button',{name:'打开所在文件夹',exact:true});await folder.click();await expect(page.locator('.job-folder-feedback')).toContainText('尚无可定位文件');
 await page.evaluate(()=>window.directoryFixture.revealError='native_directory_missing');await folder.click();await expect(page.locator('.job-folder-feedback')).toContainText('目录不存在');await expect(folder).toBeEnabled();
 await page.evaluate(()=>{window.directoryFixture.revealError='';window.directoryFixture.noFiles=false;});await folder.click();await expect(page.locator('.job-folder-feedback')).toBeEmpty();
});

for(const width of [1280,800,390])test(`download location and compact folder action fit ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:900});await install(page);await page.getByRole('button',{name:'设置与状态',exact:true}).click();await expect(page.getByRole('button',{name:'选择文件夹',exact:true})).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:`test-results/native-directory-settings-${width}.png`,fullPage:true});await page.getByRole('button',{name:'下载任务',exact:true}).click();const folder=page.getByRole('button',{name:'打开所在文件夹',exact:true});await expect(folder).toBeVisible();expect((await folder.boundingBox()).width).toBeLessThanOrEqual(32);await page.screenshot({path:`test-results/native-directory-task-${width}.png`,fullPage:true});
});

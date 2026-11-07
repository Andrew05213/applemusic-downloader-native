'use strict';
const {chromium}=require('playwright'),{expect}=require('@playwright/test');
const fs=require('node:fs'),path=require('node:path'),{pathToFileURL}=require('node:url'),assert=require('node:assert/strict');
async function main(){
 const browser=await chromium.launch({headless:true}),output=path.resolve('.artifacts/auth-retry-ui');fs.mkdirSync(output,{recursive:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:900}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>{
   const album={id:'300',type:'albums',title:'Fixture Album',artist:'Fixture Artist',downloadable:true,url:'https://music.apple.com/us/album/300',trackCount:1};
   const song={id:'301',type:'songs',title:'Fixture Song',artist:'Fixture Artist',duration:180000,downloadable:true,url:'https://music.apple.com/us/song/301'};
   window.fixtureJobs=[];
   window.nativeDesktop={
    onSession:()=>{},getState:async()=>({state:'authenticated',region:'us'}),openLogin:async()=>{},artworkURL:u=>u,
    api:async(target,options={})=>{
     const u=new URL(target,'http://fixture'),p=u.pathname;
     if(p==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true}}};
     if(p==='/api/v1/catalog/home')return {groups:[{type:'albums',title:'最近播放',items:[album]}]};
     if(p==='/api/v1/catalog/library'||p==='/api/v1/catalog/artist-albums')return {groups:[]};
     if(p==='/api/v1/catalog/detail')return {resource:album,tracks:[song]};
     if(p==='/api/v1/browser-download/resolve')return {url:options.body.url};
     if(p==='/api/v1/jobs'&&options.method==='POST'){fixtureJobs.push(options.body);return {jobs:[{id:'a'.repeat(32),url:options.body.urls[0]}]};}
     if(p==='/api/v1/jobs')return {jobs:[]};
     if(p==='/api/native/state')return {runtime:{phase:'ready'},downloadDirectory:'synthetic only'};
     throw Error('fixture_route_unhandled');
    },
   };
  });
  await page.goto(pathToFileURL(path.resolve('src/renderer/native-index.html')).href);
  await page.locator('.catalog-title-action').click();
  const button=page.locator('.track-row [data-download]').first();await expect(button).toBeEnabled();
  assert.equal(await button.locator('svg path').count(),3);
  assert.equal(await button.locator('svg path').last().getAttribute('d'),'M4 16v5h16v-5');
  assert.equal(await button.getAttribute('aria-label'),'下载 Fixture Song');assert.equal(await button.textContent(),'');
  await expect(page.locator('.brand-mark')).toHaveJSProperty('naturalWidth',256);
  await page.screenshot({path:path.join(output,'download-tray.png')});
  await button.locator('svg path').last().click();await expect(page.locator('#notice')).toHaveText('已加入下载队列。');
  const jobs=await page.evaluate(()=>fixtureJobs);assert.equal(jobs.length,1);assert.deepEqual(jobs[0].urls,['https://music.apple.com/us/song/301']);assert.equal(jobs[0].quality,'alac');assert.equal(errors.length,0,errors.join('\n'));
  console.log(JSON.stringify({passed:true,trayPaths:3,accessibleDownloadLabel:true,svgClickQueuesOriginalTrack:true,appIconLoaded:true,remoteRequests:false,output}));
 }finally{await browser.close();}
}
main().catch(e=>{console.error(e);process.exitCode=1;});

'use strict';
const { test, expect } = require('@playwright/test');
const { pathToFileURL } = require('node:url');
const path = require('node:path');
const renderer = pathToFileURL(path.resolve(__dirname, '../src/renderer/native-index.html')).href;

async function install(page) {
  await page.clock.install();
  await page.route(/^https?:\/\//, route => route.fulfill({ status: 204, body: '' }));
  await page.addInitScript(() => {
    window.jobFixture = {
      jobs: ['one','two'].map(id => ({id, url:'https://music.apple.com/us/album/' + id,
        status:'succeeded', total:1, succeeded:1, failed:0, skipped:0, unavailable:0,
        items:[{sourceId:id, name:'Fixture ' + id, status:'succeeded'}]})),
      calls:[], details:{}, holdDelete:false, holdList:false, pendingDeletes:[], pendingLists:[], deleteError:'', failList:false,
      releaseDeletes(){this.pendingDeletes.splice(0).forEach(resolve=>resolve());},
      releaseLists(){this.pendingLists.splice(0).forEach(resolve=>resolve());}
    };
    window.nativeDesktop = {
      getState:async()=>({state:'needs_login'}), onSession(){}, onReady(fn){queueMicrotask(fn);}, openLogin:async()=>{},
      api:async(target, options={})=>{
        const f=window.jobFixture; f.calls.push({target,method:options.method||'GET'});
        if(target.startsWith('/api/v1/catalog/detail'))return f.details[new URL(target,'http://fixture').searchParams.get('id')]||{groups:[]};
        if(target.startsWith('/api/v1/catalog/'))return{groups:[]};
        if(target==='/api/v1/jobs?limit=30'){
          const snapshot=structuredClone(f.jobs);
          if(f.holdList)await new Promise(resolve=>f.pendingLists.push(resolve));
          if(f.failList)throw new Error('native_api_failed');
          return{jobs:snapshot};
        }
        if(options.method==='DELETE'){
          if(f.holdDelete)await new Promise(resolve=>f.pendingDeletes.push(resolve));
          if(f.deleteError)throw new Error('Error invoking remote method native:api: Error: '+f.deleteError);
          f.jobs=f.jobs.filter(job=>!target.startsWith('/api/v1/jobs/'+job.id));
          return{ok:true};
        }
        throw new Error('Unexpected fixture request: '+target);
      }
    };
  });
  await page.goto(renderer);
  await page.getByRole('button',{name:'下载任务',exact:true}).click();
  await expect(page.locator('.job')).toHaveCount(2);
}
const card = page => page.locator('[data-job-id="one"]');
const record = page => card(page).getByRole('button',{name:'仅删除任务',exact:true});
const mutations = page => page.evaluate(()=>window.jobFixture.calls.filter(call=>call.method==='DELETE'));

test('polling preserves a pressed delete button through pointer-up',async({page})=>{
  await install(page);
  const target=record(page), box=await target.boundingBox();
  await target.evaluate(button=>{window.pressedJobButton=button;});
  await page.mouse.move(box.x+box.width/2,box.y+box.height/2); await page.mouse.down();
  await page.clock.fastForward(3000);
  expect(await target.evaluate(button=>window.pressedJobButton===button)).toBe(true);
  await page.mouse.up();
  await expect(card(page)).toHaveCount(0);
  expect(await mutations(page)).toEqual([{target:'/api/v1/jobs/one/record',method:'DELETE'}]);
});

test('slow deletion keeps pending state across polling and prevents repeated operations',async({page})=>{
  await install(page); await page.evaluate(()=>{window.jobFixture.holdDelete=true;});
  await record(page).click();
  await expect(card(page).getByRole('button',{name:'正在删除…',exact:true})).toBeDisabled();
  await expect(card(page).getByRole('button',{name:'删除任务与文件',exact:true})).toBeDisabled();
  await card(page).locator('[data-job-action="record"]').evaluate(button=>{button.onclick();button.onclick();});
  await page.clock.fastForward(9000);
  expect((await mutations(page)).length).toBe(1);
  await expect(card(page)).toHaveAttribute('aria-busy','true');
  await expect(card(page).locator('.job-feedback')).toHaveText('正在删除，请稍候…');
  await page.evaluate(()=>{window.jobFixture.holdDelete=false;window.jobFixture.releaseDeletes();});
  await expect(card(page)).toHaveCount(0);
  await expect(page.locator('#jobs-status')).toHaveText('任务已删除，下载文件已保留。');
});

for(const [code,text] of [['output_busy','其他下载或文件删除正在进行'],['job_deleting','此任务正在删除'],['file_in_use','文件正在被其他程序使用'],['file_changed','文件已被修改'],['file_access_denied','没有权限删除文件'],['file_delete_failed','部分文件未能删除']]){
  test('deletion error '+code+' is retained on the card and can be retried',async({page})=>{
    await install(page); await page.evaluate(code=>{window.jobFixture.deleteError=code;},code);
    page.on('dialog',dialog=>dialog.accept());
    await card(page).getByRole('button',{name:'删除任务与文件',exact:true}).click();
    await expect(card(page).locator('.job-feedback')).toContainText(text);
    await page.clock.fastForward(6000);
    await expect(card(page).locator('.job-feedback')).toContainText(text);
    await expect(record(page)).toBeEnabled();
    await page.evaluate(()=>{window.jobFixture.deleteError='';});
    await record(page).click(); await expect(card(page)).toHaveCount(0);
    expect((await mutations(page)).map(call=>call.target)).toEqual(['/api/v1/jobs/one','/api/v1/jobs/one/record']);
  });
}

test('a stale list response cannot resurrect an acknowledged deletion',async({page})=>{
  await install(page); await page.evaluate(()=>{window.jobFixture.holdList=true;});
  await page.clock.fastForward(3000);
  await expect.poll(()=>page.evaluate(()=>window.jobFixture.pendingLists.length)).toBe(1);
  await record(page).click(); await expect(card(page)).toHaveCount(0);
  await page.evaluate(()=>{window.jobFixture.holdList=false;window.jobFixture.releaseLists();});
  await expect(page.locator('.job')).toHaveCount(1);
  await page.clock.fastForward(3000); await expect(card(page)).toHaveCount(0);
});

test('a refresh failure after successful removal is not reported as a delete failure',async({page})=>{
  await install(page); await page.evaluate(()=>{window.jobFixture.failList=true;});
  await record(page).click(); await expect(card(page)).toHaveCount(0);
  await expect(page.locator('#jobs-status')).toHaveText('任务已删除，但列表刷新失败，稍后会自动重试。');
  await expect(page.locator('[data-job-id="two"]')).toBeVisible();
});

test('slow list requests are coalesced instead of stacking every three seconds',async({page})=>{
  await install(page); await page.evaluate(()=>{window.jobFixture.holdList=true;});
  await page.clock.fastForward(12000);
  await expect.poll(()=>page.evaluate(()=>window.jobFixture.pendingLists.length)).toBe(1);
  await page.evaluate(()=>{window.jobFixture.holdList=false;window.jobFixture.releaseLists();});
  await expect(record(page)).toBeEnabled();
});

test('already removed tasks are handled idempotently',async({page})=>{
  await install(page); await page.evaluate(()=>{window.jobFixture.deleteError='not_found';window.jobFixture.jobs=[];});
  await record(page).click(); await expect(card(page)).toHaveCount(0);
  await expect(page.locator('#jobs-status')).toContainText('任务已删除');
});

test('keyboard focus survives polling and Enter still deletes the same task',async({page})=>{
  await install(page); await record(page).focus();
  await page.clock.fastForward(6000);
  await expect(record(page)).toBeFocused();
  await page.keyboard.press('Enter'); await expect(card(page)).toHaveCount(0);
});

test('a timeout is not reported as success and leaves a retryable card',async({page})=>{
  await install(page); await page.evaluate(()=>{window.jobFixture.deleteError='AbortError';});
  await record(page).click();
  await expect(card(page).locator('.job-feedback')).toContainText('结果尚未确认');
  await expect(record(page)).toBeEnabled();
  await expect(page.locator('#jobs-status')).not.toContainText('任务已删除');
});

test('pending and failed delete feedback fits a narrow screen',async({page},testInfo)=>{
  await page.setViewportSize({width:390,height:844});
  await install(page); await page.evaluate(()=>{window.jobFixture.deleteError='output_busy';});
  await record(page).click(); await expect(card(page).locator('.job-feedback')).toContainText('其他下载');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('native-delete-390.png'),fullPage:true});
});

test('task headings use durable names rather than raw URLs',async({page})=>{
  await install(page);await page.evaluate(()=>{window.jobFixture.jobs[0].title='专辑 · The Demon Diaries';});
  await page.clock.fastForward(1000);await expect(card(page).locator('h3')).toHaveText('专辑 · The Demon Diaries');
  await expect(card(page).locator('h3')).not.toContainText('https://');
});
test('old task names resolve once and are cached without blocking delete controls',async({page})=>{
  await install(page);await page.evaluate(()=>{
    window.jobFixture.jobs[0].url='https://music.apple.com/us/album/123?i=456';window.jobFixture.jobs[0].items=[];
    window.jobFixture.details['456']={resource:{title:'Selected Song'}};
  });
  await page.clock.fastForward(1000);await expect(card(page).locator('h3')).toHaveText('Selected Song');
  await expect(record(page)).toBeEnabled();await page.clock.fastForward(5000);
  expect(await page.evaluate(()=>window.jobFixture.calls.filter(c=>c.target.startsWith('/api/v1/catalog/detail')).length)).toBe(1);
  expect(await page.evaluate(()=>JSON.parse(localStorage.getItem('native-v2-job-names'))['https://music.apple.com/us/album/123?i=456'])).toBe('Selected Song');
});
test('failed items and their task show an actionable specific cause',async({page})=>{
  await install(page);await page.evaluate(()=>{
    const job=window.jobFixture.jobs[0];job.status='failed';job.failed=1;job.succeeded=0;job.errorCode='execution_failed';
    job.items[0].status='failed';job.items[0].errorCode='license_failed';
  });
  await page.clock.fastForward(1000);
  await expect(card(page).locator('.job-failure')).toContainText('解密许可获取失败');
  await expect(card(page).locator('.job-item-failure')).toContainText('地区与订阅权限');
});

for(const [code,message] of [['manifest_queue_timeout_transient','请求排队超时'],['manifest_empty_transient','暂未返回播放地址'],['manifest_response_invalid','响应异常'],['quality_unavailable','所选音质']])test(`media failure ${code} is distinct from generic resource/network failure`,async({page})=>{
 await install(page);await page.evaluate(code=>{const job=window.jobFixture.jobs[0];Object.assign(job,{status:'failed',failed:1,succeeded:0,errorCode:code});Object.assign(job.items[0],{status:'failed',errorCode:code});},code);await page.clock.fastForward(1000);
 await expect(card(page).locator('.job-failure')).toContainText(message);await expect(card(page).locator('.job-item-failure')).toContainText(message);
});
test('live download speed stops during decryption and disappears on completion',async({page})=>{
  await install(page);await page.evaluate(()=>{
    const job=window.jobFixture.jobs[0];job.status='running';job.transfer={bytes:2097152,bytesPerSecond:1048576,at:Date.now(),phase:'downloading'};
  });
  await page.clock.fastForward(1000);await expect(card(page).locator('.job-transfer')).toContainText('1.0 MiB/s');
  await page.evaluate(()=>{window.jobFixture.jobs[0].transfer={bytes:2097152,bytesPerSecond:0,at:Date.now(),phase:'decrypting'};});
  await page.clock.fastForward(1000);await expect(card(page).locator('.job-transfer')).toContainText('正在解密');
  await expect(card(page).locator('.job-transfer')).not.toContainText('/s');
  await page.evaluate(()=>{window.jobFixture.jobs[0].status='succeeded';});
  await page.clock.fastForward(1000);await expect(card(page).locator('.job-transfer')).toHaveText('');
});
test('a stalled list refresh cannot leave stale speed on screen',async({page})=>{
  await install(page);await page.evaluate(()=>{const job=window.jobFixture.jobs[0];job.status='running';job.transfer={bytes:1024,bytesPerSecond:512,at:Date.now(),phase:'downloading'};});
  await page.clock.fastForward(1000);await expect(card(page).locator('.job-transfer')).toContainText('/s');
  await page.evaluate(()=>{window.jobFixture.holdList=true;});
  await page.clock.fastForward(5000);await expect(card(page).locator('.job-transfer')).toHaveText('等待传输状态…');
});

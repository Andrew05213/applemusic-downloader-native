'use strict';
const {test,expect}=require('@playwright/test'),path=require('node:path'),{pathToFileURL}=require('node:url');
const renderer=pathToFileURL(path.resolve(__dirname,'../src/renderer/native-index.html')).href;
const defaults={downloadRegion:'',metadataRegion:'',lyricsRegion:'',language:'',saveLyrics:false,embedLyrics:true,lyricsType:'lyrics',lyricsFormat:'lrc',embedCover:true,coverFormat:'jpg',coverSize:'500x500',saveArtistCover:false,saveAnimatedArtwork:false,useSongInfoForPlaylist:false,alacMax:192000,songFileFormat:'{title}'};
async function install(page,overrides={}){
 await page.addInitScript(({defaults,overrides})=>{
  const f=window.settingsFixture={preferences:{...defaults,...overrides},concurrency:3,calls:[],changes:[]};
  document.addEventListener('change',e=>{if(e.target.matches('#settings-view select'))f.changes.push({id:e.target.id,key:e.target.dataset.preference,value:e.target.value});});
  window.nativeDesktop={getState:async()=>({state:'authenticated',region:'us'}),onSession(){},onReady:fn=>queueMicrotask(fn),openLogin:async()=>{},
   cacheInfo:async()=>({available:true,usedBytes:0,limitMB:500,entries:0}),
   api:async(target,options={})=>{f.calls.push({target,...options});const u=new URL(target,'http://fixture');
    if(target==='/api/v1/capabilities')return {queue:{state:'ready'},jobExecutorConfigured:true,qualities:{alac:{available:true},'aac-lc':{available:true},atmos:{available:true}}};
    if(u.pathname.startsWith('/api/v1/catalog/'))return {groups:[]};
    if(u.pathname==='/api/v1/jobs')return {jobs:[]};
    if(target==='/api/native/state')return {runtime:{phase:'ready'},downloadDirectory:'D:\\Music'};
    if(target==='/api/v1/settings/download'){if(options.method==='PUT')f.preferences=structuredClone(options.body);if(options.method==='DELETE')f.preferences=structuredClone(defaults);return {settings:structuredClone(f.preferences)};}
    if(target==='/api/v1/settings/download-concurrency'){if(options.method==='PUT')f.concurrency=options.body.downloadConcurrency;if(options.method==='DELETE')f.concurrency=3;return {downloadConcurrency:f.concurrency};}
    return {};
   }
  };
 },{defaults,overrides});
 await page.goto(renderer);await page.getByRole('button',{name:'设置与状态',exact:true}).click();
 await expect(page.getByLabel('ALAC 采样率上限')).toHaveValue(String(overrides.alacMax||defaults.alacMax));
 await expect.poll(()=>page.evaluate(()=>document.getAnimations().filter(a=>a.playState==='running').length)).toBe(0);
}

test('all settings dropdowns share native custom styling, including the open picker',async({page},info)=>{
 await page.setViewportSize({width:1280,height:900});await install(page);
 const appearances=await page.locator('#settings-view select').evaluateAll(controls=>controls.map(control=>({appearance:getComputedStyle(control).appearance,height:control.getBoundingClientRect().height,radius:getComputedStyle(control).borderRadius})));
 expect(appearances).toHaveLength(11);for(const style of appearances){expect(style).toEqual({appearance:'base-select',height:40,radius:'8px'});}
 expect(await page.locator('#region').evaluate(control=>getComputedStyle(control).appearance)).not.toBe('base-select');
 const quality=page.getByLabel('默认音质');await quality.click();await expect(page.locator('#quality:open')).toBeVisible();
 const picker=await quality.evaluate(control=>{const style=getComputedStyle(control,'::picker(select)');return {appearance:style.appearance,radius:style.borderRadius,padding:style.padding,background:style.backgroundColor};});
 expect(picker.appearance).toBe('base-select');expect(picker.radius).toBe('10px');expect(picker.padding).toBe('5px');expect(picker.background).toMatch(/41, 41, 45/);
 await page.screenshot({path:info.outputPath('settings-quality-open.png')});
 await quality.getByRole('option',{name:'AAC-LC',exact:true}).click();await expect(quality).toHaveValue('aac-lc');await expect(page.locator('#quality:open')).toHaveCount(0);
 expect(await page.evaluate(()=>window.settingsFixture.changes.filter(c=>c.id==='quality'))).toEqual([{id:'quality',value:'aac-lc'}]);
 expect(await page.evaluate(()=>window.settingsFixture.calls.filter(c=>c.method==='PUT'))).toEqual([]);
});

test('keyboard navigation, Escape, focus and explicit save keep native semantics',async({page})=>{
 await install(page);const quality=page.getByLabel('默认音质');await quality.focus();await page.keyboard.press('Space');await expect(page.locator('#quality:open')).toBeVisible();
 await page.keyboard.press('ArrowDown');await page.keyboard.press('Escape');await expect(quality).toBeFocused();await expect(quality).toHaveValue('alac');
 expect(await page.evaluate(()=>window.settingsFixture.changes)).toEqual([]);
 await page.keyboard.press('Space');await page.keyboard.press('ArrowDown');await page.keyboard.press('Enter');await expect(quality).toHaveValue('aac-lc');await expect(quality).toBeFocused();
 await page.getByRole('button',{name:'保存设置',exact:true}).click();await expect(page.locator('#notice')).toHaveText('设置已保存，下一个任务生效。');
 expect(await page.evaluate(()=>JSON.parse(localStorage.getItem('native-v2-download-defaults')))).toEqual({quality:'aac-lc',existing:'skip'});
 await page.reload();await expect(page.getByLabel('默认音质')).toHaveValue('aac-lc');
});

test('friendly labels preserve the original API values and numeric sampling rate',async({page})=>{
 await install(page);
 const choices=[['下载地区','us','美国'],['元数据语言','en-US','英语'],['歌词类型','syllable-lyrics','逐字歌词'],['歌词格式','ttml','TTML'],['封面格式','png','PNG'],['封面尺寸','3000x3000','3000 × 3000 px'],['ALAC 采样率上限','96000','96 kHz']];
 for(const [label,value,text]of choices){const control=page.getByLabel(label);await control.selectOption(value);await expect(control.locator('option:checked')).toHaveText(text);}
 await page.getByRole('button',{name:'保存设置',exact:true}).click();await expect(page.locator('#notice')).toContainText('设置已保存');
 expect(await page.evaluate(()=>window.settingsFixture.calls.find(c=>c.target==='/api/v1/settings/download'&&c.method==='PUT').body)).toEqual({...defaults,downloadRegion:'us',language:'en-US',lyricsType:'syllable-lyrics',lyricsFormat:'ttml',coverFormat:'png',coverSize:'3000x3000',alacMax:96000});
 await page.getByRole('button',{name:'恢复默认',exact:true}).click();await expect(page.getByLabel('ALAC 采样率上限').locator('option:checked')).toHaveText('192 kHz');await expect(page.getByLabel('下载地区').locator('option:checked')).toHaveText('自动');
});

test('existing custom values survive rendering, refresh and save',async({page})=>{
 await install(page,{language:'fr-CA',alacMax:88200,coverSize:'2000x2000'});
 await expect(page.getByLabel('元数据语言').locator('option:checked')).toHaveText('fr-CA');
 await page.getByRole('button',{name:'刷新当前目录',exact:true}).click();await expect(page.getByLabel('ALAC 采样率上限')).toHaveValue('88200');
 await page.getByRole('button',{name:'保存设置',exact:true}).click();await expect(page.locator('#notice')).toContainText('设置已保存');
 expect(await page.evaluate(()=>window.settingsFixture.preferences)).toEqual({...defaults,language:'fr-CA',alacMax:88200,coverSize:'2000x2000'});
});

for(const width of [390,760,1280])test(`native picker stays within the viewport at ${width}px`,async({page},info)=>{
 await page.setViewportSize({width,height:760});await install(page);
 const control=page.getByLabel('ALAC 采样率上限');await control.scrollIntoViewIfNeeded();await control.click();
 await expect(control.getByRole('option',{name:'44.1 kHz',exact:true})).toBeVisible();
 const layout=await control.evaluate(select=>({scrollWidth:document.documentElement.scrollWidth,width:innerWidth,height:innerHeight,rects:[...select.options].map(option=>{const r=option.getBoundingClientRect();return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,width:r.width};})}));
 expect(layout.scrollWidth).toBeLessThanOrEqual(width);for(const r of layout.rects){expect(r.width).toBeGreaterThan(100);expect(r.left).toBeGreaterThanOrEqual(0);expect(r.right).toBeLessThanOrEqual(width);expect(r.top).toBeGreaterThanOrEqual(0);expect(r.bottom).toBeLessThanOrEqual(layout.height);}
 await page.screenshot({path:info.outputPath(`settings-sampling-open-${width}.png`)});await page.keyboard.press('Escape');
});

test('forced-colors and reduced-motion modes retain visible keyboard focus',async({page})=>{
 await page.emulateMedia({forcedColors:'active',reducedMotion:'reduce'});await install(page);
 const control=page.getByLabel('默认音质');await page.keyboard.press('Tab');await control.focus();
 expect(await control.evaluate(select=>({width:getComputedStyle(select).outlineWidth,style:getComputedStyle(select).outlineStyle,adjust:getComputedStyle(select).forcedColorAdjust}))).toEqual({width:'2px',style:'solid',adjust:'auto'});
 await page.keyboard.press('Space');await expect(page.locator('#quality:open')).toBeVisible();await page.keyboard.press('Escape');await expect(control).toBeFocused();
});

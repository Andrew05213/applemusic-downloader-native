'use strict';
// Format conversion only: preserve the imagegen artwork, let the existing
// packaging tool generate Windows sizes, and extract its 256px PNG preview.
const fs=require('node:fs'),path=require('node:path'),{spawnSync}=require('node:child_process');
const root=path.resolve(__dirname,'..'),out=path.join(root,'.artifacts','app-icon-converted');
fs.mkdirSync(out,{recursive:true});
const result=spawnSync(require('app-builder-bin').appBuilderPath,['icon','--format','ico','--input','src/assets/app-icon-master.png','--out',out,'--root',root],{cwd:root,encoding:'utf8',windowsHide:true});
if(result.status!==0)throw Error('app_icon_conversion_failed');
const data=fs.readFileSync(path.join(out,'icon.ico')),count=data.readUInt16LE(4);
let png;
for(let i=0;i<count;i++){const entry=6+i*16;if(data[entry]!==0||data[entry+1]!==0)continue;const size=data.readUInt32LE(entry+8),offset=data.readUInt32LE(entry+12),part=data.subarray(offset,offset+size);if(part.subarray(0,8).equals(Buffer.from([137,80,78,71,13,10,26,10])))png=part;}
if(!png)throw Error('app_icon_png_missing');
fs.writeFileSync(path.join(root,'src/assets/app-icon.ico'),data);
fs.writeFileSync(path.join(root,'src/assets/app-icon.png'),png);
console.log(JSON.stringify({passed:true,icoSizes:count,pngWidth:png.readUInt32BE(16),pngHeight:png.readUInt32BE(20)}));

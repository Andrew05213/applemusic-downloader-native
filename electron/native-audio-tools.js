'use strict';
// Build-time only. Installed clients never fetch codecs or search PATH.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {spawnSync} = require('node:child_process');
const {Readable} = require('node:stream');
const {pipeline} = require('node:stream/promises');
const root = path.resolve(__dirname, '..');
const build = 'ffmpeg-9.0.2-essentials_build';
const archiveHash = '4705843ccaaf54257c16ad90f3e952ece33c17df964ecf7bfdbb0f49c7171077';
const members = {
  'ffmpeg.exe':'3256173f3f8bffd7df12227c68adf68025edb1832273a9530688a7bb1ed8edec',
  'ffprobe.exe':'f0d36ecbbdd3bcfac3efa078c96c7271c2e68b3810595552ac3b7f17e9a65c52',
  'LICENSE':'8ceb4b9ee5adedde47b31e975c1d90c73ad27b6b165a1dcd80c7c545eb65b903',
  'README.txt':'0342de6bb39dd421cbec2c00d89ad274b640d75ad3f2cab09ffd647ce2d8f949',
};
const hash = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
function verify(directory) {
  for (const [name, expected] of Object.entries(members)) {
    const file = path.join(directory, name);
    if (!fs.lstatSync(file).isFile() || hash(file) !== expected) throw Error('bundled_audio_tool_invalid: '+name);
  }
}
async function prepare() {
  const directory = path.join(root, 'electron/bin/audio-tools');
  try { verify(directory); return; } catch {}
  const cache = path.join(root,'.tmp-v2-native');
  fs.mkdirSync(cache,{recursive:true});
  const archive = path.join(cache, build+'.7z');
  if (!fs.existsSync(archive) || hash(archive)!==archiveHash) {
    const tmp = archive+'.'+crypto.randomUUID()+'.partial';
    try {
      const response = await fetch('https://www.gyan.dev/ffmpeg/builds/packages/'+build+'.7z', {signal:AbortSignal.timeout(180000)});
      if (!response.ok) throw Error('audio_tools_download_failed: '+response.status);
      await pipeline(Readable.fromWeb(response.body), fs.createWriteStream(tmp,{flags:'wx'}));
      if (hash(tmp)!==archiveHash) throw Error('audio_tools_archive_hash_mismatch');
      fs.renameSync(tmp,archive);
    } finally { fs.rmSync(tmp,{force:true}); }
  }
  const temp = fs.mkdtempSync(path.join(cache, 'audio-tools-'));
  try {
    const args = Object.keys(members).map(name=>build+'/'+(name.endsWith('.exe')?'bin/':'')+name);
    const res = spawnSync(require('7zip-bin').path7za,['e','-y','-o'+temp,archive,...args],{windowsHide:true});
    if (res.status!==0) throw Error('audio_tools_extraction_failed');
    verify(temp);
    fs.mkdirSync(directory,{recursive:true});
    for (const name of Object.keys(members)) fs.copyFileSync(path.join(temp,name),path.join(directory,name));
    verify(directory);
  } finally { fs.rmSync(temp,{recursive:true,force:true}); }
}
function stage(source, destination) {
  verify(source);
  fs.mkdirSync(destination,{recursive:true});
  for (const name of Object.keys(members)) fs.copyFileSync(path.join(source,name), path.join(destination,name),fs.constants.COPYFILE_EXCL);
}
if (require.main===module) prepare().catch(error=>{console.error(error.message);process.exitCode=1;});
module.exports={prepare,verify,stage};

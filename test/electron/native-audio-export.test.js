'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs=require('node:fs'), path=require('node:path'), os=require('node:os');
const audio=require('../../electron/native-audio-tools');
const bundled=path.resolve(__dirname,'../../electron/bin/audio-tools');
test('staging rejects missing or substituted tools',t=>{
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'amdl-audio-check-'));
  t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));
  const dest=path.join(dir,'stage');
  assert.throws(()=>audio.stage(dir,dest));assert.equal(fs.existsSync(dest),false);
  fs.writeFileSync(path.join(dir,'ffmpeg.exe'),'placeholder');
  assert.throws(()=>audio.stage(dir,dest),/bundled_audio_tool_invalid/);
  assert.equal(fs.existsSync(dest),false);
});
test('stages only pinned tools and their notices', {skip:!fs.existsSync(bundled)}, t=>{
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'amdl-audio-stage-'));
  t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));
  audio.stage(bundled,dir);
  assert.deepEqual(fs.readdirSync(dir).sort(),['LICENSE','README.txt','ffmpeg.exe','ffprobe.exe']);
  audio.verify(dir);
});

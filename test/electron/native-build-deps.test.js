'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {createRequire} = require('node:module');
const {spawnSync} = require('node:child_process');
const {prepare, PATCHES} = require('../../scripts/npm-build-compat');
const root = path.resolve(__dirname, '../..');
const lock = require('../../package-lock.json');
const modules = name => Object.entries(lock.packages).filter(([p]) => p === 'node_modules/' + name || p.endsWith('/node_modules/' + name));
const temporary = t => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-build-deps-'));
  t.after(() => fs.rmSync(directory, {recursive:true, force:true}));
  return directory;
};
function write(root, relative, text) {
  const file = path.join(root, relative); fs.mkdirSync(path.dirname(file), {recursive:true}); fs.writeFileSync(file, text); return file;
}
function compatibilityFixture(t) {
  const fixture = temporary(t), packages = {};
  for (const patch of PATCHES) {
    for (const [relative, entry] of modules(patch.package).filter(([,e]) => e.version === patch.version)) {
      packages[relative] = entry;
      write(fixture, relative + '/package.json', JSON.stringify({name:patch.package, version:patch.version}));
      let original = fs.readFileSync(path.join(root, relative, patch.file), 'utf8');
      for (const edit of patch.edits || [{from:patch.from,to:patch.to}]) original = original.replaceAll(edit.to,edit.from);
      write(fixture, relative + '/' + patch.file, original);
    }
  }
  for (const name of ['minimatch','glob']) {
    for (const [relative, entry] of modules(name)) {
      packages[relative] = entry;
      write(fixture, relative + '/package.json', JSON.stringify({name,version:entry.version}));
    }
  }
  write(fixture, 'package-lock.json', JSON.stringify({packages}));
  return fixture;
}

test('complete installed tree uses genuine patched engines, not audit aliases or version edits', () => {
  assert.deepEqual(prepare(root, {checkOnly:true}), {prepared:true,changed:0});
  assert.equal(modules('minimatch').length, 1);
  for (const [relative, entry] of modules('minimatch')) {
    assert.equal(entry.version, '11.0.3');
    assert.match(entry.resolved, /^https:\/\/registry\.npmjs\.org\/minimatch\/-\/minimatch-/);
    assert.match(entry.integrity, /^sha512-/);
    assert.equal(require(path.join(root,relative,'package.json')).version, '11.0.3');
  }
  for (const [,entry] of modules('glob')) assert.ok(['7.2.3','14.0.3'].includes(entry.version));
  assert.ok(modules('glob').some(([,e]) => e.version === '14.0.3'));
});

test('compatibility preparation is idempotent and rejects unknown bytes before any write', t => {
  const fixture = compatibilityFixture(t);
  assert.throws(() => prepare(fixture, {checkOnly:true}), /not_prepared/);
  const patch = PATCHES[1], [relative] = modules(patch.package).find(([,e]) => e.version === patch.version);
  const file = path.join(fixture,relative,patch.file), original = fs.readFileSync(file);
  fs.appendFileSync(file, '\nunknown code');
  assert.throws(() => prepare(fixture), /identity_mismatch/);
  const [first] = modules(PATCHES[0].package).find(([,e]) => e.version === PATCHES[0].version);
  assert.ok(fs.readFileSync(path.join(fixture,first,PATCHES[0].file),'utf8').includes(PATCHES[0].from));
  fs.writeFileSync(file, original);
  const changed=PATCHES.reduce((count,patch)=>count+modules(patch.package).filter(([,e])=>e.version===patch.version).length,0);
  assert.deepEqual(prepare(fixture), {prepared:true,changed});
  assert.deepEqual(prepare(fixture), {prepared:true,changed:0});
  assert.deepEqual(prepare(fixture, {checkOnly:true}), {prepared:true,changed:0});
  const packageFile = path.join(fixture,relative,'package.json');
  fs.writeFileSync(packageFile,JSON.stringify({name:patch.package,version:'99.0.0'}));
  assert.throws(() => prepare(fixture), /version_changed/);
});

test('preparation refuses a shared node_modules junction and mismatched installed engines', t => {
  const fixture = compatibilityFixture(t), linked = temporary(t);
  fs.symlinkSync(path.join(fixture,'node_modules'),path.join(linked,'node_modules'),process.platform==='win32'?'junction':'dir');
  assert.throws(() => prepare(linked), /dependencies_linked/);
  const [relative] = modules('minimatch')[0];
  fs.writeFileSync(path.join(fixture,relative,'package.json'),JSON.stringify({name:'minimatch',version:'10.2.6'}));
  assert.throws(() => prepare(fixture), /engine_mismatch/);
});

test('minimatch optimization cannot amplify globparts beyond the requested budget', () => {
  const script = `const assert=require('node:assert/strict');const {Minimatch}=require('minimatch');
    const mm=new Minimatch('root/'+('**/../part/leaf/').repeat(18)+'end',{optimizationLevel:2,braceExpandMax:128});
    assert.ok(mm.globParts.length<=128);assert.ok(mm.match('root/end')===false);`;
  const result = spawnSync(process.execPath,['--max-old-space-size=128','-e',script],{cwd:root,shell:false,windowsHide:true,timeout:5000,encoding:'utf8'});
  assert.equal(result.error,undefined);assert.equal(result.status,0,result.stderr);
});

test('modern glob stops reading directories when its signal aborts during the first read', async t => {
  const fixture = temporary(t);
  for (let i=0;i<4;i++) for (let j=0;j<4;j++) write(fixture,`${i}/${j}/file.txt`,'fixture');
  const asarRequire=createRequire(require.resolve('@electron/asar'));
  const {glob}=asarRequire('glob');
  const controller=new AbortController(), reason=new Error('fixture_abort');let reads=0;
  const scopedFS={...fs,readdir(...args){
    reads++;const callback=args.pop();
    fs.readdir(...args,(error,entries)=>{if(reads===1)controller.abort(reason);callback(error,entries);});
  }};
  await assert.rejects(glob('**/*',{cwd:fixture,fs:scopedFS,signal:controller.signal}),/fixture_abort/);
  const atReject=reads;await new Promise(resolve=>setTimeout(resolve,50));
  assert.equal(reads,atReject);assert.equal(reads,1);
});

test('every legacy glob keeps sync, callback, ignore, dotfile and literal-path behavior', async t => {
  const fixture=temporary(t);
  const corpus=require('../fixtures/build-glob-contracts.json');
  for(const file of corpus.files)write(fixture,file,file);
  for(const [relative,entry] of modules('glob').filter(([,e])=>e.version==='7.2.3')){
    const glob=require(path.join(root,relative));
    for(const item of corpus.cases){
      const options={cwd:fixture,...item.options};
      const normalize=files=>files.map(file=>file.replaceAll('\\','/')).sort();
      assert.deepEqual(normalize(glob.sync(item.pattern,options)),item.expected,relative+': '+item.pattern);
      const result=await new Promise((resolve,reject)=>glob(item.pattern,options,(error,files)=>error?reject(error):resolve(files)));
      assert.deepEqual(normalize(result),item.expected,relative+': '+item.pattern);
    }
    assert.equal(entry.version,'7.2.3');
  }
});

test('all ASAR callers retain unpack patterns and exact round trips with the fixed matcher', async t => {
  const fixture=temporary(t), source=path.join(fixture,'input');
  const names=['main.js','.hidden','bin/helper.node','native/sub/item.txt','歌曲 (Live) [1].txt'];
  for(const file of names)write(source,file,'fixture '+file);
  let i=0;
  for(const [relative] of modules('@electron/asar')){
    const asar=require(path.join(root,relative,'lib/asar.js'));const archive=path.join(fixture,`${i++}.asar`);
    await asar.createPackageWithOptions(source,archive,{unpack:'*.node',unpackDir:'**/native'});
    const listing=asar.listPackage(archive).map(file=>file.replaceAll('\\','/').replace(/^\/+/,''));
    for(const name of names)assert.ok(listing.includes(name),relative+': '+JSON.stringify(listing));
    assert.equal(asar.statFile(archive,path.join('bin','helper.node')).unpacked,true,relative);
    assert.equal(asar.statFile(archive,path.join('native','sub','item.txt')).unpacked,true,relative);
    const output=path.join(fixture,'out-'+i);asar.extractAll(archive,output);
    for(const file of names)assert.equal(fs.readFileSync(path.join(output,file),'utf8'),'fixture '+file,relative+': '+file);
  }
});

test('directory comparison and filelist preserve their old filtering contracts', async t => {
  const fixture=temporary(t), left=path.join(fixture,'left'),right=path.join(fixture,'right');
  for(const folder of [left,right])for(const file of ['main.js','.hidden.js','nested/歌曲.js','skip-item.js'])write(folder,file,'same');
  write(left,'ignore.map','left');write(right,'ignore.map','right');
  const compare=require('dir-compare'), options={compareContent:true,includeFilter:'*.js',excludeFilter:'skip-*'};
  assert.equal(compare.compareSync(left,right,options).same,true);
  assert.equal((await compare.compare(left,right,options)).same,true);
  write(right,'nested/歌曲.js','changed');
  assert.equal(compare.compareSync(left,right,options).same,false);
  const {FileList}=require('filelist');
  const list=new FileList().include(path.join(left,'**/*.js'),{dot:true,allowWindowsEscape:false}).exclude(file=>path.basename(file).startsWith('skip-'));
  assert.deepEqual(list.toArray().map(file=>path.relative(left,file).replaceAll('\\','/')).sort(),['.hidden.js','main.js','nested/歌曲.js']);
});

test('legacy temp cleanup keeps literal bracketed paths and never deletes neighbors', async t => {
  const fixture=temporary(t),temp=require('temp').track();t.after(()=>temp.track(false));
  const sibling=write(fixture,'neighbor/file.txt','keep');
  const directory=temp.mkdirSync({dir:fixture,prefix:'literal [1] '});write(directory,'child/file.txt','remove');
  const result=await new Promise((resolve,reject)=>temp.cleanup((error,result)=>error?reject(error):resolve(result)));
  assert.ok(result.dirs>=1);assert.equal(fs.existsSync(directory),false);assert.equal(fs.readFileSync(sibling,'utf8'),'keep');
});

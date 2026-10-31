'use strict';
const {test}=require('node:test');const assert=require('node:assert/strict');
const {audit,anchors}=require('../scripts/check-docs');
const fs=require('node:fs'),path=require('node:path');
const root=path.resolve(__dirname,'..');
const read=file=>fs.readFileSync(path.join(root,file),'utf8');
test('documentation local links and heading fragments remain valid',()=>{
 const result=audit();assert.deepEqual(result.errors,[]);assert.deepEqual(result.warnings,[]);
});
test('documentation anchors support repeated and non-Latin headings',()=>{
 const result=anchors('# 标题\n## Hello, World!\n## Hello, World!\n```text\n# ignored\n```');
 assert(result.has('标题'));assert(result.has('hello-world'));assert(result.has('hello-world-1'));assert(!result.has('ignored'));
});

test('npm commands in current guides exist in the active package',()=>{
 const {scripts}=require('../package.json');
 for(const file of ['README.md','docs/USER-GUIDE.md','docs/RELEASE.md','docs/DOCUMENTATION.md','docs/development/README.md','docs/development/dependency-boundaries.md','docs/testing/README.md']){
  for(const match of read(file).matchAll(/\bnpm\s+run\s+([a-zA-Z0-9:_-]+)/g)){
   assert(Object.hasOwn(scripts,match[1]),file+': missing npm script '+match[1]);
  }
 }
});

test('current navigation describes the frozen archive and separate 1.x maintenance tree',()=>{
 const manifest=require('../legacy/v1/manifest.json');
 const guide=read('docs/legacy/README.md');
 assert(guide.includes(manifest.maintenanceBranch));
 assert(guide.includes(manifest.maintenanceCommit));
 assert(guide.includes(manifest.files.length+' 个文件'));
 assert.match(guide,/git clone --branch codex\/maintenance-1\.x --single-branch/);
 for(const file of ['docs/REPOSITORY.md','docs/development/README.md','legacy/README.md']){
  assert(read(file).includes('legacy/v1/'),file);
  assert(read(file).includes('legacy/README.md#维护-1x'),file);
 }
 assert.doesNotMatch(read('docs/development/README.md'),/test:all` 保留完整历史集合|根目录 `start\.bat`/);
});

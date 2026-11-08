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

test('new documentation pages cannot downgrade broken links to historical warnings',t=>{
 const fixture=fs.mkdtempSync(path.join(require('node:os').tmpdir(),'public-docs-'));
 t.after(()=>fs.rmSync(fixture,{recursive:true,force:true}));
 for(const file of ['README.md','CHANGELOG.md','CONTRIBUTING.md','NATIVE-LOCAL-NOTICE.md','appliance/README.md','legacy/README.md','src/renderer/README.md','wrapper-linux/README.md','web/README.md','docs/new-topic.md']){
  fs.mkdirSync(path.dirname(path.join(fixture,file)),{recursive:true});
  fs.writeFileSync(path.join(fixture,file),['CHANGELOG.md','docs/new-topic.md'].includes(file)?'[missing](missing.md)':'# Fixture');
 }
 const result=audit(fixture);
 assert.deepEqual(result.errors,[{file:'CHANGELOG.md',target:'missing.md',reason:'missing target'},{file:'docs/new-topic.md',target:'missing.md',reason:'missing target'}]);
 assert.deepEqual(result.warnings,[]);
});

test('npm commands in current guides exist in the active package',()=>{
 const {scripts}=require('../package.json');
 for(const file of ['README.md','CONTRIBUTING.md','docs/USER-GUIDE.md','docs/RELEASE.md','docs/DOCUMENTATION.md','docs/development/README.md','docs/development/dependency-boundaries.md','docs/testing/README.md']){
  for(const match of read(file).matchAll(/\bnpm\s+run\s+([a-zA-Z0-9:_-]+)/g)){
   assert(Object.hasOwn(scripts,match[1]),file+': missing npm script '+match[1]);
  }
 }
});

test('public navigation explains the frozen archive without requiring private maintenance access',()=>{
 const manifest=require('../legacy/v1/manifest.json');
 const guide=read('docs/legacy/README.md');
 assert(guide.includes('manifest.json'));
 assert(guide.includes(manifest.files.length+' 个文件'));
 assert.doesNotMatch(guide,/git clone --branch|c0719fc|codex\/maintenance-1\.x/);
 for(const file of ['docs/REPOSITORY.md','docs/development/README.md','legacy/README.md']){
  assert(read(file).includes('legacy/v1/'),file);
  assert(read(file).includes('legacy/README.md'),file);
 }
 assert.doesNotMatch(read('docs/development/README.md'),/test:all` 保留完整历史集合|根目录 `start\.bat`/);
});

test('public guides do not reintroduce internal logs, machine paths or obsolete release navigation',()=>{
 const documents=['README.md','CHANGELOG.md','CONTRIBUTING.md','docs/README.md','docs/REPOSITORY.md','docs/RELEASE.md','docs/development/README.md','docs/testing/README.md'];
 for(const file of documents){
  assert.doesNotMatch(read(file),/[CE]:[\\/](?:Users|开发)|\.worktrees[\\/]|docs\/superpowers|releases\/2\.0\.0|testing\/20\d\d-/i,file);
 }
 const plans=path.join(root,'docs/superpowers');
 assert(!fs.existsSync(plans) || !fs.readdirSync(plans,{recursive:true,withFileTypes:true}).some(entry=>entry.isFile()));
 assert(!fs.readdirSync(path.join(root,'docs/testing')).some(name=>/^\d{4}-|^v2\./.test(name)));
 assert(!fs.existsSync(path.join(root,'NATIVE-TEST-NOTICE.md')));
 for(const entry of ['doctor:native','setup:native','dev:native','package:native','build:native-toolchain'])assert(read('docs/development/README.md').includes(entry));
 assert.match(read('NOTICE'),/docs\/compliance\/native-status\.md/);
 assert.match(read('docs/compliance/native-status.md'),/不是整套安装包的完整对应源码提供/);
});

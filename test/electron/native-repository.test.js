'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path');
const root=path.resolve(__dirname,'../..');

test('new repository has native 1.0 identity without changing source module or user data namespace',()=>{
 const pkg=require('../../package.json');
 assert.equal(pkg.name,'applemusic-downloader-native');
 assert.equal(pkg.version,'1.0.0');
 assert.equal(pkg.private,true); // Prevent npm publication, not GitHub visibility.
 assert.equal(pkg.repository.url,'https://github.com/Andrew05213/applemusic-downloader-native.git');
 assert.equal(pkg.bugs.url,'https://github.com/Andrew05213/applemusic-downloader-native/issues');
 assert.match(fs.readFileSync(path.join(root,'apple-music-downloader/go.mod'),'utf8'),/^module github\.com\/Andrew05213\/applemusic_downloader$/m);
 assert.equal(require('../../electron-builder.native.cjs').appId,'io.github.andrew05213.applemusicdownloader.v2test');
 assert.equal(require('../../package-lock.json').packages[''].version,pkg.version);
});

test('current documentation distinguishes new source baseline from old releases and provenance',()=>{
 const read=file=>fs.readFileSync(path.join(root,file),'utf8');
 assert.match(read('README.md'),/尚未发布 1\.0\.0 安装包或 Release/);
 assert.doesNotMatch(read('README.md'),/applemusic_downloader\/releases\/download/);
 assert.match(read('docs/releases/1.0.0.md'),/81e5c388347478b19e99137777abab33e769ca61/);
 assert.match(read('docs/legacy/README.md'),/不是本仓库 Native 1\.0/);
 assert.match(read('docs/RELEASE.md'),/Andrew05213\/applemusic-downloader-native/);
 for(const name of ['verify.yml','release-native.yml'])assert.doesNotMatch(read('.github/workflows/'+name),/^name: Native 2\.0 /m);
});

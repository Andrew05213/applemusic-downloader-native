'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');const path=require('node:path');
const root=path.resolve(__dirname,'../..');
const go=require('../../native-runtime/collect-go-licenses');
const qemu=require('../../native-runtime/inventory-qemu');
const debian=require('../../native-runtime/package-debian-sources');
test('compliance collectors reject unselected inputs and paths outside owned output',()=>{
 assert.throws(()=>go.collect(path.join(root,'.artifacts','invalid-inventory'),[]),/go_license_output_invalid/);
 assert.throws(()=>go.collect(path.join(root,'..','outside'),['missing']),/go_license_output_invalid/);
 assert.throws(()=>qemu.inventory('missing',path.join(root,'..','outside')),/qemu_inventory_output_invalid/);
 assert.throws(()=>debian.packageSources('missing',path.join(root,'..','outside.zip')),/debian_source_output_invalid/);
});
test('Debian source collector pins the exact snapshot and both source versions',()=>{
 const source=fs.readFileSync(path.join(root,'native-runtime/collect-debian-sources.sh'),'utf8');
 assert.match(source,/20260728T000000Z/);
 assert.match(source,/linux=6\.1\.177-1/);
 assert.match(source,/busybox=1:1\.35\.0-4\+deb12u1/);
 assert.match(source,/sha256sum -c SHA256SUMS\.source/);
});

'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {normalize}=require('../../src/renderer/native-account-input');
test('phone accounts default to China mainland and do not repeat international prefixes',()=>{
 for(const input of ['10000000000','+8610000000000','+86 100-0000-0000','008610000000000','8610000000000'])assert.equal(normalize(input),'+8610000000000',input);
 assert.equal(normalize('+911000000000','+86'),'+911000000000');
 assert.equal(normalize('1000000000','+91'),'+911000000000');
 assert.equal(normalize('911000000000','+91'),'+911000000000');
 assert.equal(normalize('2000000000','+1'),'+12000000000');
 assert.equal(normalize('200000000','+358'),'+358200000000');
 assert.equal(normalize('  '),'');
});
test('email accounts and other existing account names are not phone-normalized',()=>{
 for(const email of ['name@example.com','Name+86@example.com','10000000000@example.com']){assert.equal(normalize(email),email);assert.equal(normalize(email,'invalid'),email);}
 assert.equal(normalize(' Name+tag@example.com '),'Name+tag@example.com');
 assert.equal(normalize('legacy-account'),'legacy-account');
});
test('malformed phone prefixes cannot be silently converted or double-prefixed',()=>{
 for(const [input,dial]of [['++8610000000000','+86'],['+0','+86'],['123','+86'],['1234567890123456','+86'],['10000000000','+0'],['10000000000','+1234'],['+86abc','+86']])assert.throws(()=>normalize(input,dial),/^Error: native_phone_invalid$/);
});
test('generated application icon and tray download icon are wired into audited packaging',()=>{
 const fs=require('node:fs'),path=require('node:path'),root=path.resolve(__dirname,'../..'),config=require('../../electron-builder.native.cjs'),{UI_FILES}=require('../../electron/native-package');
 const png=fs.readFileSync(path.join(root,'src/assets/app-icon.png'));assert.equal(png.readUInt32BE(16),256);assert.equal(png.readUInt32BE(20),256);
 const ico=fs.readFileSync(path.join(root,'src/assets/app-icon.ico'));assert.equal(ico.readUInt16LE(2),1);assert.ok(ico.readUInt16LE(4)>=1);
 for(const file of ['src/assets/app-icon.png','src/assets/app-icon.ico','src/renderer/native-account-input.js']){assert.ok(UI_FILES.includes(file));assert.ok(config.files.includes(file));}
 assert.equal(config.win.icon,'src/assets/app-icon.ico');
 const renderer=fs.readFileSync(path.join(root,'src/renderer/native-renderer.js'),'utf8');assert.doesNotMatch(renderer,/↓/);assert.match(renderer,/control\.append\(icon\('download'\)\)/);
});

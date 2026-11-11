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
test('verified domestic mobile accounts remove exactly one trunk zero',()=>{
 const cases=[
  ['+44','07700900123','+447700900123'],
  ['+44','07123456789','+447123456789'],
  ['+44','07523456789','+447523456789'],
  ['+44','07923456789','+447923456789'],
  ['+61','0412345678','+61412345678'],
  ['+81','07012345678','+817012345678'],
  ['+81','08012345678','+818012345678'],
  ['+81','09012345678','+819012345678'],
  ['+33','0601020304','+33601020304'],
  ['+33','0612345678','+33612345678'],
  ['+33','0638123456','+33638123456'],
  ['+33','0652123456','+33652123456'],
  ['+33','0656123456','+33656123456'],
  ['+33','0695123456','+33695123456'],
  ['+33','0698123456','+33698123456'],
  ['+33','0699123456','+33699123456'],
  ['+33','0731234567','+33731234567'],
  ['+33','0781234567','+33781234567'],
 ];
 for(const [dial,domestic,expected]of cases){
  for(const input of [domestic,domestic.slice(1),expected.slice(1),expected,'00'+expected.slice(1)])assert.equal(normalize(input,dial),expected,input);
  assert.equal(normalize(normalize(domestic,dial),dial),expected);
 }
 assert.equal(normalize('(07700) 900-123','+44'),'+447700900123');
 assert.equal(normalize('04 1234 5678','+61'),'+61412345678');
 assert.equal(normalize('090-1234-5678','+81'),'+819012345678');
 assert.equal(normalize('06 12 34 56 78','+33'),'+33612345678');
});
test('ambiguous domestic phone accounts require explicit international input',()=>{
 const cases=[
  ['+44','0770090012'],['+44','077009001234'],['+44','07000900123'],['+44','07600900123'],['+44','02079460990'],
  ['+61','041234567'],['+61','04123456789'],['+61','0212345678'],
  ['+81','0901234567'],['+81','090123456789'],['+81','06012345678'],['+81','0312345678'],
  ['+33','061234567'],['+33','06123456789'],['+33','0600123456'],['+33','0653123456'],['+33','0655123456'],
  ['+33','0690123456'],['+33','0691123456'],['+33','0692123456'],['+33','0693123456'],['+33','0694123456'],
  ['+33','0696123456'],['+33','0697123456'],['+33','0639123456'],['+33','0708123456'],['+33','0791234567'],['+33','0123456789'],
  ['+86','013800000000'],['+886','0912345678'],['+82','01012345678'],['+49','015123456789'],
  ['+358','0401234567'],['+39','0212345678'],['+999','0123456789'],
 ];
 for(const [dial,input]of cases)assert.throws(()=>normalize(input,dial),/^Error: native_phone_invalid$/,dial+' '+input);
 // Italy's significant zero must survive when the user supplies the full number.
 assert.equal(normalize('+390212345678','+39'),'+390212345678');
 assert.equal(normalize('00390212345678','+39'),'+390212345678');
 assert.equal(normalize('+262692123456','+33'),'+262692123456');
});
test('international and custom-code phone accounts keep existing formatting rules',()=>{
 for(const input of ['+447700900123','00447700900123','447700900123'])assert.equal(normalize(input,'+44'),'+447700900123');
 for(const dial of ['invalid','+86','+999'])assert.equal(normalize('+819012345678',dial),'+819012345678');
 assert.equal(normalize('200000000','+358'),'+358200000000');
 assert.equal(normalize('123456789','+999'),'+999123456789');
 assert.equal(normalize('Name+86@example.com','+39'),'Name+86@example.com');
});
test('generated application icon and tray download icon are wired into audited packaging',()=>{
 const fs=require('node:fs'),path=require('node:path'),root=path.resolve(__dirname,'../..'),config=require('../../electron-builder.native.cjs'),{UI_FILES}=require('../../electron/native-package');
 const png=fs.readFileSync(path.join(root,'src/assets/app-icon.png'));assert.equal(png.readUInt32BE(16),256);assert.equal(png.readUInt32BE(20),256);
 const ico=fs.readFileSync(path.join(root,'src/assets/app-icon.ico'));assert.equal(ico.readUInt16LE(2),1);assert.ok(ico.readUInt16LE(4)>=1);
 for(const file of ['src/assets/app-icon.png','src/assets/app-icon.ico','src/renderer/native-account-input.js']){assert.ok(UI_FILES.includes(file));assert.ok(config.files.includes(file));}
 assert.equal(config.win.icon,'src/assets/app-icon.ico');
 const renderer=fs.readFileSync(path.join(root,'src/renderer/native-renderer.js'),'utf8');assert.doesNotMatch(renderer,/↓/);assert.match(renderer,/control\.append\(icon\('download'\)\)/);
});

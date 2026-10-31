'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { formatDiagnosticReport, exportDiagnosticReport } = require('../../electron/diagnostic-export');

test('compact runtime JSON expands records and preserves the latest wrapper evidence', () => {
 const logs = Array.from({length: 250}, (_, i) => `wrapper event ${i}: ${'x'.repeat(70)}`);
 logs.push('WRAPPER_LOGIN_COMPLETED_FIXTURE');
 const report = formatDiagnosticReport({runtime: JSON.stringify({ok:true,status:{version:'1.4.3',network:{stage:'tls'}},logs})});
 assert.match(report, /WRAPPER_LOGIN_COMPLETED_FIXTURE/);
 assert.match(report, /status.version=1\.4\.3/);
 assert.match(report, /status.network.stage=tls/);
 assert.doesNotMatch(report, /wrapper event 0:/);
 assert.match(report, /truncated/i);
});

test('runtime limits retain the real newest tail rather than the first input chunk', () => {
 const runtime = Array.from({length:5000}, (_,i) => `old record ${i} ${'x'.repeat(100)}`).join('\n') + '\nNEWEST_RUNTIME_FIXTURE';
 const report = formatDiagnosticReport({runtime});
 assert.match(report, /NEWEST_RUNTIME_FIXTURE/);
 assert.doesNotMatch(report, /old record 0 /);
 assert.match(report, /truncated/i);
});

test('structured diagnostics redact individual records without losing unrelated logs', () => {
 const report = formatDiagnosticReport({runtime: JSON.stringify({ok:true,status:{version:'1.4.3'},logs:[
  'Authorization: Bearer SENTINEL_AUTH', 'Cookie: SENTINEL_COOKIE',
  'network_stage=tls', 'WRAPPER_LOGIN_COMPLETED_FIXTURE',
 ],unknown:'SENTINEL_UNKNOWN'})});
 assert.doesNotMatch(report, /SENTINEL/);
 assert.match(report, /network_stage=tls/);
 assert.match(report, /WRAPPER_LOGIN_COMPLETED_FIXTURE/);
});

test('large compact JSON preserves newest entries and oversized or malformed records are explicit', () => {
 const logs = Array.from({length:300}, (_,i) => `entry ${i} ${'x'.repeat(1000)}`);
 logs.push('NEWEST_STRUCTURED_FIXTURE');
 assert.match(formatDiagnosticReport({runtime:JSON.stringify({logs})}), /NEWEST_STRUCTURED_FIXTURE/);
 const oversized = formatDiagnosticReport({runtime:JSON.stringify({logs:['x'.repeat(2*1024*1024)]})});
 assert.match(oversized, /truncated/);
 assert.ok(oversized.length < 1000);
 const malformed = formatDiagnosticReport({runtime:'{"logs":["SENTINEL_PRIVATE'});
 assert.match(malformed, /invalid JSON/);
 assert.doesNotMatch(malformed, /SENTINEL/);
});

test('truncation preserves newest physical records and marks long individual lines', () => {
 const runtime = 'old'.repeat(500_000) + '\nnetwork_stage=tls\n' + '中'.repeat(3000) + '\nFINAL_FIXTURE';
 const report = formatDiagnosticReport({runtime});
 assert.match(report, /network_stage=tls/);
 assert.match(report, /FINAL_FIXTURE/);
 assert.match(report, /\[truncated\]/);
 assert.ok(Buffer.byteLength(report) < 20_000);
});

test('diagnostic export preserves useful fixed failures but removes private content and bounds input', () => {
 const report = formatDiagnosticReport({ productVersion: '1.4.3', now: new Date('2026-10-03T00:00:00Z'), runtime: [
  'network_stage=tls', 'catched an exception: CKC error.',
  'Authorization: Bearer SENTINEL_AUTH', 'Media-User-Token=SENTINEL_TOKEN', 'Cookie: SENTINEL_COOKIE',
  'password: SENTINEL_PASSWORD', '2FA code=123456', 'user=someone@example.test',
  'https://user:SECRET@music.apple.com/file?token=SENTINEL_QUERY', 'C:\\Users\\SENTINEL_USER\\secret.txt',
 ].join('\n'), uiLogs: ['download failed: license_transient', ...Array(200).fill('x'.repeat(4000))] });
 assert.match(report, /1\.4\.3/); assert.match(report, /network_stage=tls/); assert.match(report, /CKC error/);
 assert.doesNotMatch(report, /SENTINEL|123456|someone@|user:SECRET/);
 assert.ok(Buffer.byteLength(report) < 300_000);
 assert.doesNotThrow(() => formatDiagnosticReport({runtime:'ok',uiLogs:[{},null,42]}));
});

test('cancelled export does not read runtime diagnostics or write a file', async () => {
 const result = await exportDiagnosticReport({dialog:{showSaveDialog:async()=>({canceled:true})},collectRuntime(){throw Error('must not collect');},writeFile(){throw Error('must not write');}});
 assert.deepEqual(result, {ok:true,cancelled:true});
});

test('export uses only the native selected path and returns no path or raw failure', async () => {
 const writes = [];
 const options={dialog:{showSaveDialog:async()=>({canceled:false,filePath:'chosen.txt'})},collectRuntime:async()=>'CKC error.',writeFile:async(...args)=>writes.push(args), productVersion:'1.4.3',uiLogs:['queue ready']};
 assert.deepEqual(await exportDiagnosticReport(options),{ok:true,cancelled:false});
 assert.equal(writes[0][0],'chosen.txt'); assert.match(writes[0][1],/CKC error/); assert.match(writes[0][1],/queue ready/);
 const failure=await exportDiagnosticReport({...options,writeFile:async()=>{throw Error('SENTINEL_PRIVATE_PATH');}});
 assert.equal(failure.ok,false); assert.equal(failure.error.code,'diagnostic_export_failed'); assert.doesNotMatch(JSON.stringify(failure),/SENTINEL/);
});

test('unavailable runtime still exports UI diagnostics and cancellation has no file side effects', async () => {
 let content;
 const result=await exportDiagnosticReport({dialog:{showSaveDialog:async()=>({canceled:false,filePath:'chosen.txt'})},collectRuntime:async()=>{throw Error('SENTINEL_RUNTIME_SECRET');},writeFile:async(_path,text)=>{content=text;},uiLogs:['network_preflight_failed stage=tls']});
 assert.equal(result.ok,true);assert.match(content,/network_preflight_failed stage=tls/);assert.doesNotMatch(content,/SENTINEL/);
});

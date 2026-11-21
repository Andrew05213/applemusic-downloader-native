'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const root=path.resolve(__dirname,'../..');
test('1.0.5-beta.2 preview workflow builds only from its branch without write access or draft creation',()=>{
 const text=fs.readFileSync(path.join(root,'.github/workflows/preview-105-beta2.yml'),'utf8');
 assert.match(text,/branches: \['codex\/preview-1\.0\.5-beta\.2'\]/);
 assert.match(text,/permissions:\s+contents: read/);
 assert.match(text,/AMDL_CREATE_DRAFT: 'false'/);
 assert.match(text,/needs: \[verify, guest\]/);
 assert.match(text,/native-ci-windows\.ps1 -CloudRelease/);
 assert.match(text,/artifact-ids: \$\{\{ needs\.guest\.outputs\.artifact_id \}\}/);
 assert.match(text,/AMDL_GUEST_RECEIPT_SHA256: \$\{\{ needs\.guest\.outputs\.guest_receipt_sha256 \}\}/);
 assert.match(text,/AppleMusicDownloader-1\.0\.5-beta\.2-Setup\.exe\.sha256/);
 assert.doesNotMatch(text,/contents: write|pull_request|workflow_dispatch|secrets:|gh release/);
});
test('wrapper compatibility branch can verify and compile but cannot publish or build an installer',()=>{
 const text=fs.readFileSync(path.join(root,'.github/workflows/wrapper-compat.yml'),'utf8');
 assert.match(text,/branches: \['codex\/wrapper-80dc-compat'\]/);
 assert.match(text,/uses: \.\/\.github\/workflows\/verify\.yml/);
 assert.match(text,/contents: read/);
 assert.match(text,/run: node electron\/native-cloud-release\.js linux/);
 assert.match(text,/retention-days: 3/);
 assert.doesNotMatch(text,/contents: write|GH_TOKEN:|secrets:|pull_request_target:|release-native|dist-native|native-cloud-release\.js windows|gh release|tags:/);
});
test('beta.4 login preview can only upload verified build attachments from its own branch',()=>{
 const text=fs.readFileSync(path.join(root,'.github/workflows/preview-105-beta4.yml'),'utf8');
 assert.match(text,/branches: \['codex\/auth-diagnostics-1\.0\.5-beta\.4'\]/);
 assert.match(text,/permissions:\s+contents: read/);
 assert.match(text,/AMDL_CREATE_DRAFT: 'false'/);
 assert.match(text,/needs: \[verify, guest\]/);
 assert.match(text,/AppleMusicDownloader-1\.0\.5-beta\.4-Setup\.exe\.sha256/);
 assert.doesNotMatch(text,/contents: write|pull_request|workflow_dispatch|secrets:|gh release/);
});
test('cloud release is an explicit same-run Linux to Windows transfer with least-privilege draft-only publishing',()=>{
 const text=fs.readFileSync(path.join(root,'.github/workflows/release-cloud.yml'),'utf8');
 assert.match(text,/workflow_dispatch:/);assert.doesNotMatch(text,/pull_request:|workflow_run:|pull_request_target:/);
 assert.deepEqual([...text.matchAll(/runs-on:\s+(\S+)/g)].map(m=>m[1]),['ubuntu-24.04','windows-2025']);
 assert.equal((text.match(/contents: write/g)||[]).length,1);assert.equal((text.match(/GH_TOKEN:/g)||[]).length,1);
 assert.match(text,/needs: \[verify, guest\]/);assert.match(text,/artifact-ids: \$\{\{ needs\.guest\.outputs\.artifact_id \}\}/);
 assert.match(text,/AMDL_GUEST_RECEIPT_SHA256: \$\{\{ needs\.guest\.outputs\.guest_receipt_sha256 \}\}/);
 assert.match(text,/retention-days: 3/);assert.match(text,/if-no-files-found: error/);
 assert.match(text,/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9/);
 assert.match(text,/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333/);
 assert.match(text,/native-ci-windows\.ps1 -CloudRelease/);
 const scope=fs.readFileSync(path.join(root,'electron/native-ci-windows.ps1'),'utf8');
 assert.match(scope,/\[switch\]\$CloudRelease/);assert.match(scope,/node electron\/native-cloud-release\.js windows/);
 assert.doesNotMatch(scope,/Set-Acl|icacls|Set-LocalUser|AdjustTokenPrivileges/);
 assert.doesNotMatch(text,/docker push|docker login|run:.*\$\{\{.*inputs/);
 const script=fs.readFileSync(path.join(root,'electron/native-cloud-release.js'),'utf8');
 assert.match(script,/sourceIdentity\(\)/);assert.match(script,/ensureTagAvailable\(repo,tag\)/);
 assert.match(script,/audit-native-installer/);assert.match(script,/native-electron-live\.cjs/);
 assert.match(script,/--draft/);assert.doesNotMatch(script,/'--clobber'|'edit'|'delete'|push.*--force/);
 assert(script.indexOf('const evidence=smoke(')<script.indexOf("run('gh',['release','create'"));
});

test('cloud test installer is uploaded only after audit and packaged smoke, without Release creation',()=>{
 const text=fs.readFileSync(path.join(root,'.github/workflows/release-cloud.yml'),'utf8');
 const step=text.slice(text.indexOf('      - name: Deliver verified test installer'));
 assert.match(step,/if: \$\{\{ !inputs\.create_draft \}\}/);
 assert.match(step,/name: native-test-installer/);
 assert.match(step,/\.artifacts\/installers\/\*\*\/AppleMusicDownloader-\*-Setup\.exe\.sha256/);
 assert.match(step,/\.artifacts\/cloud-release-result\.json/);
 assert.match(step,/retention-days: 7/);
 assert.doesNotMatch(step,/\.log|native-build-evidence|\.tmp-v2-native|win-unpacked|gh release|run:/);
 assert(text.indexOf('native-ci-windows.ps1 -CloudRelease')<text.indexOf('name: Deliver verified test installer'));
});

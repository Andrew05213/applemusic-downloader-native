'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

// Historical 1.4 installer contract, not the selected 2.0 native package.
const packageManifest = require('../../electron/legacy-desktop-package.json');
const projectRoot = path.join(__dirname, '..', '..');

function loadKeyValueFile(relativePath) {
  return Object.fromEntries(fs.readFileSync(path.join(projectRoot, relativePath), 'utf8')
    .split(/\r?\n/)
    .filter((line) => line && !line.startsWith('#'))
    .map((line) => {
      const separator = line.indexOf('=');
      return [line.slice(0, separator), line.slice(separator + 1)];
    }));
}

function loadBaseSigningSetting(overrides = {}) {
  const { createDRMBuildConfig } = require('../../electron/drm-build-config');
  return createDRMBuildConfig({
    build: packageManifest.build,
    repositoryRoot: projectRoot,
    environment: overrides,
  }).win.signAndEditExecutable;
}

test('Windows release is signing-capable but permits unsigned x64 online/offline WSL2 NSIS installers with checksums', () => {
  assert.equal(packageManifest.version, '1.4.3');
  assert.equal(packageManifest.build.win.signAndEditExecutable, true);
  assert.equal(packageManifest.build.forceCodeSigning, false);
  assert.deepEqual(packageManifest.build.win.target, [{ target: 'nsis', arch: ['x64'] }]);
  assert.match(packageManifest.scripts['dist:win:online'], /electron\/write-checksums\.js dist online/);
  assert.match(packageManifest.scripts['dist:win:offline'], /electron\/write-checksums\.js dist offline/);
  assert.match(packageManifest.scripts['dist:win:online'], /verify-packaged-resources\.js online/);
  assert.match(packageManifest.scripts['dist:win:offline'], /verify-packaged-resources\.js offline/);
  assert.match(packageManifest.scripts['dist:win:online'], /prepare:appliance/);
  assert.match(packageManifest.scripts['dist:win:offline'], /prepare:appliance:offline/);
  assert.match(packageManifest.scripts['prepare:appliance'], /--private/);
  assert.match(packageManifest.scripts['prepare:appliance:offline'], /--private/);
  assert.match(packageManifest.scripts['dist:win:online'], /--config electron-builder\.base\.cjs/);
  assert.match(packageManifest.scripts['dist:win:offline'], /--config electron-builder\.offline\.yml/);
  assert.match(packageManifest.scripts['dist:win:online'], /--publish never/);
  assert.match(packageManifest.scripts['dist:win:offline'], /--publish never/);
  assert.equal(packageManifest.scripts['fixture:appliance:ci'], 'node test/electron/fixtures/create-synthetic-appliance.js');
  assert.match(packageManifest.scripts['verify:release-gates'], /assert-public-release-blocked/);
  assert.equal(packageManifest.build.nsis.artifactName, 'AppleMusicDownloader-${version}-Setup.${ext}');
  assert.equal(packageManifest.build.nsis.allowToChangeInstallationDirectory, false);
  assert.equal(packageManifest.build.extraResources.some((resource) => String(resource.from).endsWith('.tar.gz')), false);
  assert.equal(packageManifest.build.extraResources.some((resource) => resource.from === 'docs/legacy/NOTICE-v1' && resource.to === 'NOTICE'), true);
  assert.equal(packageManifest.build.extraResources.some((resource) => resource.from.endsWith('utils/runv2/LICENSE')
    && resource.to === 'third-party-licenses/runv2-LICENSE'), true);
  assert.equal(packageManifest.build.files.includes('electron/**'), false);
  for (const runtimeModule of [
    'electron/appliance-manifest.js',
    'electron/apple-music-browser.js',
    'electron/credentials.js',
    'electron/desktop-state.js',
    'electron/loopback-proxy.js',
    'electron/process-runner.js',
    'electron/release-manifest.js',
    'electron/setup-controller.js',
    'electron/windows-capabilities.js',
    'electron/windows-support.js',
    'electron/wsl-manager.js',
  ]) {
    assert.equal(packageManifest.build.files.includes(runtimeModule), true, `${runtimeModule} must be packaged`);
  }
  for (const buildOnly of [
    'electron/build-windows-helper.js',
    'electron/inject-appliance-manifest.js',
    'electron/installer.nsh',
    'electron/verify-packaged-resources.js',
    'electron/write-checksums.js',
  ]) {
    assert.equal(packageManifest.build.files.includes(buildOnly), false, `${buildOnly} must stay outside app.asar`);
  }

  const offline = fs.readFileSync(path.join(__dirname, '..', '..', 'electron-builder.offline.yml'), 'utf8');
  const offlineBase = require(path.join(__dirname, '..', '..', 'electron-builder.base.cjs'));
  assert.match(offline, /extends: \.\/electron-builder\.base\.cjs/);
  assert.doesNotMatch(offline, /extends: \.\/package\.json/);
  assert.match(offline, /AppleMusicDownloader-\$\{version\}-Offline-Setup\.\$\{ext\}/);
  assert.match(offline, /from: electron\/generated/);
  assert.match(offline, /- "\*\.tar\.gz"/);
  assert.doesNotMatch(offline, /from: electron\/bin\/amdl-windows-probe\.exe/);
  assert.doesNotMatch(offline, /from: electron\/generated\/appliance-manifest\.json/);
  assert.equal(offlineBase.appId, packageManifest.build.appId);
  assert.deepEqual(offlineBase.files, packageManifest.build.files);
  assert.equal(loadBaseSigningSetting(), false);
  assert.equal(loadBaseSigningSetting({ CSC_LINK: 'test-certificate-placeholder' }), true);
  assert.equal(loadBaseSigningSetting({ WIN_CSC_LINK: 'test-certificate-placeholder' }), true);
  assert.equal(loadBaseSigningSetting({ CSC_NAME: 'test-certificate-placeholder' }), true);

  const installer = fs.readFileSync(path.join(__dirname, '..', '..', 'electron', 'installer.nsh'), 'utf8');
  assert.match(installer, /!include "x64\.nsh"/);
  assert.match(installer, /是否保留专用 WSL 发行版/);
  assert.match(installer, /IDYES amdl_preserve_wsl IDNO amdl_remove_wsl/);
  assert.match(installer, /IDYES amdl_preserve_wsl IDNO amdl_remove_wsl\r?\n\s*Abort/);
  assert.doesNotMatch(installer, /MB_DEFBUTTON2/);
  assert.doesNotMatch(installer, /\$SYSDIR\\wsl\.exe|wsl\.exe" --/i);
  assert.doesNotMatch(installer, /\$\{DisableX64FSRedirection\}|\$\{EnableX64FSRedirection\}/);
  assert.doesNotMatch(installer, /desktop-owner-id|amdl-appliance-control probe|--terminate|--unregister/i);
  assert.match(installer, /\$\{If\} \$\{isUpdated\}[\s\S]*Goto amdl_uninstall_done/);
  assert.match(installer, /\$\{If\} \$\{Silent\}[\s\S]*Goto amdl_preserve_wsl/);
  assert.equal((installer.match(/nsExec::ExecToLog/g) || []).length, 0);
  assert.equal((installer.match(/nsExec::ExecToStack/g) || []).length, 1);
  assert.match(installer, /nsExec::ExecToStack \/TIMEOUT=600000/);
  assert.doesNotMatch(installer, /nsExec::ExecToStack \/TIMEOUT=300000/);
  assert.match(installer, /\$INSTDIR\\resources\\bin\\amdl-windows-probe\.exe/);
  assert.match(installer, /uninstall-preserve-owned/);
  assert.match(installer, /uninstall-remove-owned/);
  assert.doesNotMatch(installer, /verify-uninstall-ownership/);
  assert.equal((installer.match(/credentials\.bin\.tmp/g) || []).length, 2);
  assert.equal((installer.match(/Windows\\CurrentVersion\\Run/g) || []).length, 2);
  assert.equal((installer.match(/StartupApproved\\Run/g) || []).length, 2);
  assert.match(installer, /!ifdef BUILD_UNINSTALLER[\s\S]*Function un\.AmdlRunUninstallAction[\s\S]*!endif/);
  assert.doesNotMatch(installer, /\$\{UnStrStr\}/);
  assert.match(installer, /\$AmdlUninstallResult == "AMDL_UNINSTALL_PRESERVED"/);
  assert.match(installer, /\$AmdlUninstallResult == "AMDL_UNINSTALL_ABSENT"/);
  assert.match(installer, /\$AmdlUninstallResult == "AMDL_UNINSTALL_REMOVED"/);
  const preserveStart = installer.indexOf('amdl_preserve_wsl:');
  const preserveEnd = installer.indexOf('amdl_remove_wsl:');
  assert.notEqual(preserveStart, -1);
  assert.notEqual(preserveEnd, -1);
  const preserveBlock = installer.slice(preserveStart, preserveEnd);
  assert.match(preserveBlock, /uninstall-preserve-owned[\s\S]*\$AmdlUninstallResult == "AMDL_UNINSTALL_PRESERVED"/);
  assert.match(preserveBlock, /\$AmdlUninstallExit == 22[\s\S]*SetErrorLevel 1[\s\S]*Abort/);
  assert.match(preserveBlock, /\$AmdlUninstallExit == 23[\s\S]*任务数据库、Wrapper 状态和正式下载均会保留/);
  assert.doesNotMatch(preserveBlock, /--unregister|appliance-state\.json|desktop-state\.json|jobs\.db|(?:Delete|RMDir)[^\r\n]*(?:\\Music|downloads)/i);
  assert.match(installer, /amdl_preserve_cleanup:[\s\S]*credentials\.bin/);
  assert.match(installer, /amdl_remove_wsl:[\s\S]*uninstall-remove-owned[\s\S]*\$AmdlUninstallResult == "AMDL_UNINSTALL_REMOVED"[\s\S]*amdl_remove_state_cleanup/);
  assert.match(installer, /\$AmdlUninstallExit == 24[\s\S]*Goto amdl_remove_credentials_only/);
  assert.match(installer, /amdl_remove_state_cleanup:[\s\S]*lifecycle mutex[\s\S]*Do not delete them here/);
  assert.doesNotMatch(installer, /Delete \"\$LOCALAPPDATA\\AppleMusicDownloader\\(?:appliance-state|desktop-state)\.json\"|Delete \"\$LOCALAPPDATA\\AppleMusicDownloader\\ownership\.id\"/);
  assert.doesNotMatch(installer, /(?:Delete|RMDir)[^\r\n]*(?:\\Music|downloads)/i);

  const mainProcess = fs.readFileSync(path.join(__dirname, '..', '..', 'src', 'main', 'main.js'), 'utf8');
  assert.equal((mainProcess.match(/setLoginItemSettings\(enabledLoginItemSettings\(/g) || []).length, 3);
  assert.match(mainProcess, /return \{ openAtLogin: true, name: APP_NAME, args \}/);
  assert.match(mainProcess, /const packaged = app\.isPackaged === true;/);
  assert.match(mainProcess, /explicitPath: packaged \? undefined : process\.env\.AMDL_APPLIANCE_MANIFEST/);
  assert.match(mainProcess, /packaged,/);

  const checksumWriter = fs.readFileSync(path.join(__dirname, '..', '..', 'electron', 'write-checksums.js'), 'utf8');
  assert.match(checksumWriter, /AppleMusicDownloader-\$\{version\}-Setup\.exe/);
  assert.match(checksumWriter, /AppleMusicDownloader-\$\{version\}-Offline-Setup\.exe/);
  assert.doesNotMatch(checksumWriter, /endsWith\(['"]\.exe/);
  assert.match(checksumWriter, /createReadStream/);
  assert.match(checksumWriter, /pipeline/);
  assert.doesNotMatch(checksumWriter, /readFileSync/);

  const injector = fs.readFileSync(path.join(__dirname, '..', '..', 'electron', 'inject-appliance-manifest.js'), 'utf8');
  assert.match(injector, /AMDL_OFFLINE_ROOTFS/);
  assert.match(injector, /AMDL_OFFLINE_RUNTIME/);
  assert.match(injector, /manifest\.version[^\r\n]*packageManifest\.version/);
  assert.match(injector, /releasePolicySha256/);
});

test('NOTICE and appliance Docker defaults match the locked build identity', () => {
  const versions = loadKeyValueFile('appliance/versions.lock');
  const localInputs = loadKeyValueFile('appliance/local-inputs.poc.lock');
  const notice = fs.readFileSync(path.join(projectRoot, 'docs', 'legacy', 'NOTICE-v1'), 'utf8');
  const noticeSha = require('node:crypto').createHash('sha256').update(notice).digest('hex');
  assert.equal(noticeSha, localInputs.NOTICE_SHA256);
  assert.match(notice, new RegExp('Wrapper: `' + versions.WRAPPER_BUILT_SHA256 + '`'));
  assert.match(notice, new RegExp('Android main: `' + versions.ANDROID_MAIN_BUILT_SHA256 + '`'));
  assert.match(notice, new RegExp('Android network preflight: `' + versions.ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256 + '`'));

  const dockerfile = fs.readFileSync(path.join(projectRoot, 'appliance', 'Dockerfile'), 'utf8');
  const epochMatches = dockerfile.match(/(?:ARG|ENV) SOURCE_DATE_EPOCH=(\d+)/g) || [];
  assert.equal(epochMatches.length, 3);
  assert.deepEqual(epochMatches.map((entry) => entry.match(/(\d+)$/)[1]), [versions.SOURCE_DATE_EPOCH, versions.SOURCE_DATE_EPOCH, versions.SOURCE_DATE_EPOCH]);
});

test('default CI validates native delivery without the legacy Appliance build chain', () => {
  const workflow = fs.readFileSync(path.join(__dirname, '..', '..', '.github', 'workflows', 'verify.yml'), 'utf8');
  assert.match(workflow, /name: Native 2.0 Verify/);
  assert.match(workflow, /npm run verify:native/);
  assert.match(workflow, /npm run build:native-backend/);
  assert.match(workflow, /npm run build:native-service/);
  assert.doesNotMatch(workflow, /uses:\s*actions\/upload-artifact/i);
  assert.doesNotMatch(workflow, /appliance\/scripts|fixture:appliance:ci|dist:win:|build:windows-helper/);
});

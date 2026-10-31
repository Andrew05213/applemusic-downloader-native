'use strict';

// This entry is launched only by run-drm-smoke.js with a fresh synthetic profile.
const { app, components, BrowserWindow, safeStorage, session } = require('electron');
const fs = require('node:fs');
const path = require('node:path');
const { ELECTRON_VERSION, SMOKE_PROFILE_MARKER } = require('./drm-runtime');
const { CredentialStore } = require('./credentials');
const { cookieToken } = require('./official-web-player');

const profile = process.env.AMDL_DRM_FIXTURE_PROFILE;
if (!profile || !path.isAbsolute(profile)
    || fs.readFileSync(path.join(profile, SMOKE_PROFILE_MARKER), 'utf8') !== 'synthetic-only\n') {
  throw new Error('isolated_profile_required');
}
app.setPath('userData', profile);

app.whenReady().then(async () => {
  let stage = 'runtime';
  let fixtureWindow;
  let fixtureServer;
  const receipt = (result) => ({ ...result, stage, runtime: process.versions.electron,
    realCredentialsRead: false, mediaDownloaded: false });
  const timer = setTimeout(() => {
    console.log(JSON.stringify(receipt({ ok: false, code: 'drm_fixture_timeout' })));
    app.exit(2);
  }, 60_000);
  try {
    if (process.versions.electron !== ELECTRON_VERSION || !components) throw new Error('wrong_runtime');
    stage = 'cdm';
    await components.whenReady([components.WIDEVINE_CDM_ID]);

    stage = 'eme';
    fixtureWindow = new BrowserWindow({
      show: false,
      webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false, backgroundThrottling: false },
    });
    fixtureServer = require('node:http').createServer((_request, response) => response.end('<html>DRM fixture</html>'));
    await new Promise((resolve) => fixtureServer.listen(0, '127.0.0.1', resolve));
    await fixtureWindow.loadURL(`http://127.0.0.1:${fixtureServer.address().port}/`);
    const eme = await fixtureWindow.webContents.executeJavaScript(`
      navigator.requestMediaKeySystemAccess('com.widevine.alpha', [{
        initDataTypes: ['cenc'],
        audioCapabilities: [{ contentType: 'audio/mp4; codecs="mp4a.40.2"' }],
      }]).then(() => true, () => false)
    `);
    if (!eme) throw new Error('eme_unavailable');

    stage = 'encryption';
    if (!safeStorage.isEncryptionAvailable()) throw new Error('encryption_unavailable');
    const token = 'synthetic-fixture-only-'.repeat(5);
    const credentialPath = path.join(profile, 'fixture.bin');
    const store = new CredentialStore({ credentialPath, safeStorage });
    store.save({ mediaUserToken: token });
    if (store.load().mediaUserToken !== token) throw new Error('encryption_roundtrip');
    if (fs.readFileSync(credentialPath, 'utf8').includes(token)) throw new Error('plaintext_token');

    stage = 'cookie';
    const cookieJar = session.fromPartition('persist:fixture-cookie-only').cookies;
    await cookieJar.set({ url: 'https://music.apple.com/', name: 'media-user-token', value: token,
      secure: true, expirationDate: Date.now() / 1000 + 3600 });
    const cookies = await cookieJar.get({ url: 'https://music.apple.com/', name: 'media-user-token' });
    if (cookies.length !== 1 || cookieToken(cookies[0]) !== token) throw new Error('cookie_capture');
    await cookieJar.flushStore();
    console.log(JSON.stringify(receipt({ ok: true, cdmReady: true, eme: true,
      encryptedFixture: true, cookieFixture: true })));
    clearTimeout(timer);
    fixtureWindow.destroy();
    fixtureServer.close();
    app.exit(0);
  } catch {
    clearTimeout(timer);
    console.log(JSON.stringify(receipt({ ok: false, code: 'drm_fixture_failed' })));
    if (fixtureWindow && !fixtureWindow.isDestroyed()) fixtureWindow.destroy();
    fixtureServer?.close();
    app.exit(1);
  }
});

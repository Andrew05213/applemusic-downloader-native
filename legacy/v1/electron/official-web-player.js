'use strict';

const { normalizePlaybackTrack, validMediaUserToken, preparePlaybackDRM } = require('./playback-support');

const HOME = 'https://music.apple.com/';

function officialURL(value) {
  try {
    const u = new URL(value);
    return u.protocol === 'https:' && !u.username && !u.password && !u.port
      && ['music.apple.com', 'authorize.music.apple.com', 'appleid.apple.com', 'idmsa.apple.com'].includes(u.hostname);
  } catch {
    return false;
  }
}

function cookieToken(c) {
  return c?.name === 'media-user-token'
    && ['music.apple.com', '.music.apple.com'].includes(c.domain)
    && c.secure === true
    && (!c.expirationDate || c.expirationDate > Date.now() / 1000)
    && validMediaUserToken(c.value) ? c.value : '';
}

// Fixed code only: neither item titles nor credentials are interpolated.
const STOP = `(()=>{document.querySelectorAll('audio,video').forEach(m=>m.pause());return 'stopped';})()`;
const PLAY = `(()=>{const media=[...document.querySelectorAll('audio,video')];const active=media.find(m=>!m.paused&&!m.ended&&m.currentTime>0);if(active){try{const kind=window.MusicKit?.getInstance()?.nowPlayingItem?.playbackType;if(kind===2||kind===3)return 'playing';}catch{}return 'unconfirmed';}const button=[...document.querySelectorAll('button')].find(b=>/^(play|播放|播放歌曲)$/i.test((b.getAttribute('aria-label')||b.textContent||'').trim())&&!b.disabled);if(button)button.click();return 'waiting';})()`;
function createOfficialWebPlayer({ BrowserWindow, components, consent, deliver }) {
  let playerWindow = null;
  let windowOpening = null;
  let credentialSyncAllowed = false;
  let consentAccepted = false;
  let destroyed = false;
  let playbackGeneration = 0;
  let lastDeliveredToken = '';
  let credentialSync = null;
  let syncRequestedAgain = false;
  let playbackPending = false;
  let syncState = 'not_enabled';
  let drmReady = false;

  async function syncCredentialCookie() {
    if (!credentialSyncAllowed || destroyed || !playerWindow || playerWindow.isDestroyed()) {
      return;
    }
    if (credentialSync) {
      syncRequestedAgain = true;
      return credentialSync;
    }
    const target = playerWindow;
    credentialSync = (async () => {
      do {
        syncRequestedAgain = false;
        try {
          const candidates = await target.webContents.session.cookies.get({ url: HOME, name: 'media-user-token' });
          if (destroyed || playerWindow !== target || !credentialSyncAllowed) {
            return;
          }
          const values = [...new Set(candidates.map(cookieToken).filter(Boolean))];
          if (values.length !== 1) {
            syncState = values.length ? 'ambiguous' : 'not_found';
            continue;
          }
          if (values[0] === lastDeliveredToken) {
            continue;
          }
          const receipt = await deliver({ mediaUserToken: values[0] });
          if (receipt?.saved !== true) {
            syncState = 'failed';
            continue;
          }
          lastDeliveredToken = values[0];
          syncState = receipt.pending ? 'saved_pending' : 'saved';
        } catch {
          syncState = 'failed';
        }
      } while (syncRequestedAgain && !destroyed);
    })();
    try {
      await credentialSync;
    } finally {
      credentialSync = null;
    }
  }
  const onCookieChanged = (_event, cookie) => {
    if (cookie?.name === 'media-user-token' && ['music.apple.com', '.music.apple.com'].includes(cookie.domain)) {
      void syncCredentialCookie();
    }
  };

  async function ensurePlayerWindow() {
    if (destroyed) {
      throw Error('player_unavailable');
    }
    if (playerWindow && !playerWindow.isDestroyed()) {
      return;
    }
    if (windowOpening) {
      return windowOpening;
    }
    windowOpening = (async () => {
      // Stock Electron may browse/login, but does not claim DRM support.
      drmReady = await preparePlaybackDRM(components);
      playerWindow = new BrowserWindow({
        width: 1100,
        height: 780,
        title: 'Apple Music 官方播放与登录',
        show: false,
        webPreferences: {
          sandbox: true,
          contextIsolation: true,
          nodeIntegration: false,
          backgroundThrottling: false,
          partition: 'persist:amdl-apple-music-browser',
        },
      });
      const target = playerWindow;
      const cookies = target.webContents.session.cookies;
      target.removeMenu();
      const guard = (event, url) => {
        if (!officialURL(url)) {
          event.preventDefault();
        }
      };
      for (const name of ['will-navigate', 'will-redirect']) {
        target.webContents.on(name, guard);
      }
      target.webContents.setWindowOpenHandler(({ url }) => ({
        action: officialURL(url) ? 'allow' : 'deny',
        overrideBrowserWindowOptions: {
          webPreferences: {
            sandbox: true,
            contextIsolation: true,
            nodeIntegration: false,
            partition: 'persist:amdl-apple-music-browser',
          },
        },
      }));
      target.webContents.on('did-create-window', child => {
        for (const name of ['will-navigate', 'will-redirect']) {
          child.webContents.on(name, guard);
        }
        child.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
      });
      target.webContents.session.cookies.on('changed', onCookieChanged);
      target.webContents.on('did-finish-load', () => {
        void syncCredentialCookie();
      });
      target.on('close', event => {
        if (!destroyed) {
          event.preventDefault();
          target.hide();
        }
      });
      target.on('closed', () => {
        cookies.removeListener('changed', onCookieChanged);
        if (playerWindow === target) {
          playerWindow = null;
        }
      });
      await target.loadURL(HOME);
    })();
    try {
      await windowOpening;
    } catch {
      playerWindow?.destroy();
      playerWindow = null;
      throw Error('player_unavailable');
    } finally {
      windowOpening = null;
    }
  }
  return {
    async open(item) {
      if (playbackPending) {
        return { ok: false, code: 'playback_busy' };
      }
      playbackPending = true;
      const requestGeneration = ++playbackGeneration;
      try {
        const song = item === undefined ? null : normalizePlaybackTrack(item);
        await ensurePlayerWindow();
        if (destroyed || requestGeneration !== playbackGeneration) {
          return { ok: false, code: 'playback_superseded' };
        }
        if (!consentAccepted) {
          credentialSyncAllowed = await consent();
          consentAccepted = credentialSyncAllowed;
          syncState = credentialSyncAllowed ? 'waiting' : 'not_enabled';
        }
        if (destroyed || requestGeneration !== playbackGeneration) {
          return { ok: false, code: 'playback_superseded' };
        }
        await syncCredentialCookie();
        if (destroyed || requestGeneration !== playbackGeneration || !playerWindow || playerWindow.isDestroyed()) {
          return { ok: false, code: 'playback_superseded' };
        }
        if (!song) {
          playerWindow.show();
          playerWindow.focus();
          return { ok: true, syncState, drmReady };
        }
        if (!drmReady) {
          playerWindow.hide();
          return { ok: false, code: 'drm_not_ready', syncState, drmReady: false };
        }
        const target = playerWindow;
        await target.webContents.executeJavaScript(STOP);
        if (destroyed || requestGeneration !== playbackGeneration || target !== playerWindow || target.isDestroyed()) {
          return { ok: false, code: 'playback_superseded' };
        }
        await target.loadURL('https://music.apple.com/us/song/' + song.id);
        for (let attempt = 0; attempt < 20; attempt++) {
          if (destroyed || requestGeneration !== playbackGeneration || target !== playerWindow || target.isDestroyed()) {
            return { ok: false, code: 'playback_superseded' };
          }
          if (!officialURL(target.webContents.getURL()) || new URL(target.webContents.getURL()).hostname !== 'music.apple.com') {
            break;
          }
          const observed = await target.webContents.executeJavaScript(PLAY, true);
          if (destroyed || requestGeneration !== playbackGeneration || target !== playerWindow || target.isDestroyed()) {
            return { ok: false, code: 'playback_superseded' };
          }
          if (observed === 'playing') {
            target.hide();
            return { ok: true, playing: true, syncState };
          }
          if (observed === 'unconfirmed') {
            break;
          }
          await new Promise(resolve => setTimeout(resolve, 500));
        }
        target.hide();
        return { ok: true, playing: false, needsInteraction: true, syncState };
      } finally {
        playbackPending = false;
      }
    },
    async stop() {
      playbackGeneration++;
      if (!playerWindow || playerWindow.isDestroyed()) {
        return { ok: true };
      }
      try {
        await playerWindow.webContents.executeJavaScript(STOP);
        return { ok: true };
      } catch {
        return { ok: false };
      }
    },
    destroy() {
      destroyed = true;
      playbackGeneration++;
      if (playerWindow && !playerWindow.isDestroyed()) {
        playerWindow.destroy();
      }
    },
  };
}

module.exports = { cookieToken, officialURL, createOfficialWebPlayer, PLAY, STOP };

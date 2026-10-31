'use strict';

const path = require('node:path');
const PUBLIC_MUSIC_HOME = 'https://music.apple.com/';

function isAllowedAppleMusicPage(value) {
  if (typeof value !== 'string' || value.length > 2048 || value.includes('#')) return false;
  if (!/^https:\/\/music\.apple\.com(?:[/?]|$)/.test(value)) return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === 'https:' && parsed.hostname === 'music.apple.com'
      && parsed.port === '' && parsed.host === 'music.apple.com' && !parsed.username && !parsed.password;
  } catch { return false; }
}

function queueableAppleMusicPage(value) {
  if (!isAllowedAppleMusicPage(value)) return '';
  const parsed = new URL(value);
  const segments = parsed.pathname.replace(/\/$/, '').split('/').slice(1);
  if (segments.length < 3 || !/^[a-z]{2}$/.test(segments[0]) || !['song', 'album', 'music-video', 'playlist', 'station'].includes(segments[1])) return '';
  if (segments.some((part) => !part || /%(?:2f|5c|00)/i.test(part))) return '';
  const id = segments.at(-1);
  if (segments[1] === 'playlist' ? !/^pl\.[A-Za-z0-9_-]{1,508}$/.test(id)
    : segments[1] === 'station' ? !/^ra\.[A-Za-z0-9_-]{1,508}$/.test(id)
      : !/^(?:id)?\d{1,20}$/.test(id)) return '';
  const seen = new Set();
  for (const [key, item] of parsed.searchParams) {
    if (seen.has(key) || !['i', 'l', 'itsct', 'itscg', 'uo'].includes(key)) return '';
    seen.add(key);
    if (key === 'i' ? !/^\d{1,20}$/.test(item)
      : !/^[A-Za-z0-9._~-]{1,128}$/.test(item)) return '';
  }
  if (segments[1] !== 'playlist' && parsed.searchParams.has('l')) return '';
  if (parsed.searchParams.get('l')?.length > 32) return '';
  const clean = new URL(parsed.origin + parsed.pathname);
  const songID = parsed.searchParams.get('i');
  if (songID != null) {
    if (!/^\d{1,20}$/.test(songID)) return '';
    clean.searchParams.set('i', songID);
  }
  const language = parsed.searchParams.get('l');
  if (language != null) {
    if (!/^[A-Za-z0-9-]{1,32}$/.test(language)) return '';
    clean.searchParams.set('l', language);
  }
  return clean.toString();
}

function createAppleMusicBrowser({ owner, WebContentsView }) {
  if (!owner?.contentView || typeof WebContentsView !== 'function') throw new TypeError('A window and WebContentsView are required');
  let view = null;
  let visible = false;
  let navigationGeneration = 0;
  let navigating = false;
  let destroyed = false;
  const ownerContents = owner.webContents;
  const ownerAlive = () => !owner.isDestroyed?.();
  const viewAlive = () => !destroyed && ownerAlive() && view && !view.webContents.isDestroyed();
  const detach = () => {
    // BrowserWindow's closed event runs after its native contentView is gone.
    if (view && visible && ownerAlive()) owner.contentView.removeChildView(view);
    visible = false;
    navigationGeneration += 1;
  };
  const onOwnerNavigation = () => { if (!destroyed) detach(); };
  ownerContents?.on?.('did-start-navigation', onOwnerNavigation);

  function sendPageState() {
    if (!viewAlive()) return;
    navigationGeneration += 1;
    navigating = false;
    view.webContents.send?.('amdl:music-page-state', {
      generation: navigationGeneration,
      queueable: Boolean(queueableAppleMusicPage(view.webContents.getURL())),
    });
  }

  function ensureView() {
    if (destroyed || !ownerAlive()) throw new Error('browser unavailable');
    if (view) {
      if (view.webContents.isDestroyed()) throw new Error('browser unavailable');
      return view;
    }
    view = new WebContentsView({ webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      // Keep website cookies/storage across restarts, separate from the host UI.
      partition: 'persist:amdl-apple-music-browser',
      preload: path.join(__dirname, '..', 'src', 'preload', 'apple-music-preload.js'),
    } });
    view.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
    view.webContents.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
    const guard = (event, url) => { if (!isAllowedAppleMusicPage(url)) event.preventDefault(); };
    view.webContents.on('will-navigate', guard);
    view.webContents.on('will-redirect', guard);
    view.webContents.on('will-attach-webview', (event) => event.preventDefault());
    view.webContents.on('did-start-navigation', (_event, _url, _inPlace, isMainFrame) => {
      if (!viewAlive() || isMainFrame === false) return;
      navigating = true;
      navigationGeneration += 1;
      view.webContents.send?.('amdl:music-page-state', { generation: navigationGeneration, queueable: false });
    });
    view.webContents.on('did-finish-load', sendPageState);
    view.webContents.on('did-navigate-in-page', (_event, _url, isMainFrame) => {
      if (isMainFrame !== false) sendPageState();
    });
    return view;
  }

  function attach() {
    const current = ensureView();
    if (!visible) {
      owner.contentView.addChildView(current);
      visible = true;
    }
    return current;
  }

  return {
    async open() {
      const wasVisible = visible;
      const current = attach();
      if (!current.webContents.getURL()) await current.webContents.loadURL(PUBLIC_MUSIC_HOME);
      else if (!wasVisible && !navigating) sendPageState();
      if (!viewAlive() || view !== current) throw new Error('browser unavailable');
      return { url: this.currentURL() };
    },
    currentURL() { return viewAlive() && visible && !navigating ? queueableAppleMusicPage(view.webContents.getURL()) : ''; },
    generation() { return navigationGeneration; },
    acceptDownloadIntent(event, generation) {
      if (!viewAlive() || !visible || navigating || !Number.isSafeInteger(generation) || generation !== navigationGeneration) return '';
      if (event?.sender !== view.webContents || event?.senderFrame !== view.webContents.mainFrame) return '';
      if (!isAllowedAppleMusicPage(event.senderFrame?.url)) return '';
      if (event.senderFrame.url !== view.webContents.getURL()) return '';
      return this.currentURL();
    },
    setBounds(bounds) {
      if (!viewAlive() || !visible || !bounds || typeof bounds !== 'object') throw new TypeError('browser bounds are unavailable');
      const { x, y, width, height } = bounds;
      const ownerBounds = owner.getContentBounds();
      if (![x, y, width, height].every(Number.isSafeInteger) || x < 0 || y < 0 || width < 200 || height < 160
          || x + width > ownerBounds.width || y + height > ownerBounds.height) throw new RangeError('browser bounds are invalid');
      view.setBounds({ x, y, width, height });
    },
    hide() {
      if (!destroyed) detach();
    },
    back() { if (viewAlive() && view.webContents.canGoBack?.()) view.webContents.goBack(); },
    forward() { if (viewAlive() && view.webContents.canGoForward?.()) view.webContents.goForward(); },
    reload() { if (viewAlive()) view.webContents.reload?.(); },
    destroy() {
      if (destroyed) return;
      destroyed = true;
      detach();
      ownerContents?.removeListener?.('did-start-navigation', onOwnerNavigation);
      const current = view;
      view = null;
      if (current && !current.webContents.isDestroyed()) current.webContents.close();
    },
  };
}

module.exports = { createAppleMusicBrowser, isAllowedAppleMusicPage, queueableAppleMusicPage };

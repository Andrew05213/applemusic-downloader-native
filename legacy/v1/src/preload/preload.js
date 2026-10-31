'use strict';

const { contextBridge, ipcRenderer } = require('electron');
const NETWORK_PREFLIGHT_STAGES = new Set([
  'helper_identity', 'helper_launch', 'helper_protocol', 'proxy_resolution',
  'native_probe_invalid', 'native_startup', 'dns', 'tcp_connect', 'tls',
  'ca_bundle_missing', 'ca_bundle_invalid', 'url_bag_fetch',
  'url_bag_parse', 'url_bag_key_lookup',
]);

// The downloader UI is served by the loopback WSL appliance (or by Docker in
// server deployments). It intentionally receives no Node, filesystem,
// credential-reading, WSL, Docker, or arbitrary process-control APIs. These
// fixed menu actions are validated again against the exact owner main frame.
contextBridge.exposeInMainWorld('desktopShell', Object.freeze({
  // The main process validates the exact owner window, main frame and current
  // loopback origin before performing any lifecycle operation.
  repairRuntime: () => ipcRenderer.invoke('desktop:repair-runtime'),
  openDownloadSettings: () => ipcRenderer.invoke('desktop:open-download-settings'),
  chooseDownloadLocation: () => ipcRenderer.invoke('desktop:choose-download-location'),
  openCredentials: () => ipcRenderer.invoke('desktop:open-credentials'),
  openPlaybackLogin: () => ipcRenderer.invoke('desktop:playback-open-login'),
  playCatalogTrack: (item) => ipcRenderer.invoke('desktop:playback-play', item),
  stopCatalogPlayback: () => ipcRenderer.invoke('desktop:playback-stop'),
  startService: () => ipcRenderer.invoke('desktop:start-service'),
  showDiagnostics: () => ipcRenderer.invoke('desktop:show-diagnostics'),
  exportDiagnostics: (lines) => Array.isArray(lines)
    ? ipcRenderer.invoke('desktop:export-diagnostics', lines.slice(-100).filter((line) => typeof line === 'string').map((line) => line.slice(0, 2048)))
    : ipcRenderer.invoke('desktop:export-diagnostics'),
  stopService: () => ipcRenderer.invoke('desktop:stop-service'),
  openAppleMusic: () => ipcRenderer.invoke('desktop:music-open'),
  closeAppleMusic: () => ipcRenderer.invoke('desktop:music-close'),
  appleMusicBack: () => ipcRenderer.invoke('desktop:music-back'),
  appleMusicForward: () => ipcRenderer.invoke('desktop:music-forward'),
  appleMusicReload: () => ipcRenderer.invoke('desktop:music-reload'),
  currentAppleMusicURL: () => ipcRenderer.invoke('desktop:music-current'),
  setAppleMusicBounds: (bounds) => ipcRenderer.invoke('desktop:music-bounds', bounds),
  getDownloadPreferences: () => ipcRenderer.invoke('desktop:get-download-preferences'),
  setDownloadPreferences: (value) => ipcRenderer.invoke('desktop:set-download-preferences', value),
  onAppleMusicDownloadIntent: (callback) => {
    if (typeof callback !== 'function') return () => {};
    const listener = (_event, intent) => {
      if (intent && typeof intent.url === 'string' && intent.url.length <= 2048) callback({ url: intent.url });
    };
    ipcRenderer.on('desktop:music-download-intent', listener);
    return () => ipcRenderer.removeListener('desktop:music-download-intent', listener);
  },
  onServiceState: (callback) => {
    if (typeof callback !== 'function') return () => {};
    const listener = (_event, status) => {
      if (status?.state !== 'unavailable') return;
      if (status.code === 'network_preflight_failed') {
        callback({ state: 'unavailable', code: 'network_preflight_failed',
          stage: NETWORK_PREFLIGHT_STAGES.has(status.stage) ? status.stage : 'unknown' });
      } else callback({ state: 'unavailable' });
    };
    ipcRenderer.on('desktop:shell-state', listener);
    return () => ipcRenderer.removeListener('desktop:shell-state', listener);
  },
}));

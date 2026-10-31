'use strict';

const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('applianceSetup', Object.freeze({
  getState: () => ipcRenderer.invoke('setup:get-state'),
  chooseDownloadDirectory: () => ipcRenderer.invoke('setup:choose-download-directory'),
  begin: (options) => ipcRenderer.invoke('setup:begin', options),
  retry: () => ipcRenderer.invoke('setup:retry'),
  getDiagnostics: () => ipcRenderer.invoke('setup:get-diagnostics'),
  repairRuntime: () => ipcRenderer.invoke('setup:repair-runtime'),
  applyWslAction: (code) => ipcRenderer.invoke('setup:apply-wsl-action', code),
  restartWindows: () => ipcRenderer.invoke('setup:restart-windows'),
  onProgress: (listener) => {
    const handler = (_event, progress) => listener(progress);
    ipcRenderer.on('setup:progress', handler);
    return () => ipcRenderer.removeListener('setup:progress', handler);
  },
}));

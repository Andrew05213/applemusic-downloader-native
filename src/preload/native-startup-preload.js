'use strict';
const { contextBridge, ipcRenderer } = require('electron');
const copy = value => ({ phase: ['loading', 'failed', 'complete'].includes(value?.phase) ? value.phase : 'failed' });
contextBridge.exposeInMainWorld('nativeStartup', Object.freeze({
  getState: () => ipcRenderer.invoke('native:startup-state').then(copy),
  enter: () => ipcRenderer.invoke('native:startup-enter'),
  quit: () => ipcRenderer.invoke('native:startup-quit'),
  onState: callback => {
    if (typeof callback !== 'function') return () => {};
    const listener = (_event, value) => callback(copy(value));
    ipcRenderer.on('native:startup-state', listener);
    return () => ipcRenderer.removeListener('native:startup-state', listener);
  },
}));

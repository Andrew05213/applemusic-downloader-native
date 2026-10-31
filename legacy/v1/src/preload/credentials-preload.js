'use strict';

const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('credentials', Object.freeze({
  summary: () => ipcRenderer.invoke('credentials:summary'),
  save: (values) => ipcRenderer.invoke('credentials:save', values),
}));

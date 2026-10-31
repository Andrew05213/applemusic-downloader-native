'use strict';

const fs = require('node:fs');
const path = require('node:path');

const STATE_VERSION = 1;
const ALLOWED_KEYS = new Set([
  'setupStep',
  'downloadDir',
  'localPort',
  'applianceVersion',
  'rebootRequired',
  'resumeSetup',
  'pendingCredentialDelivery',
  'defaultQuality',
  'defaultExistingPolicy',
  'lastErrorCode',
  'updatedAt',
]);

function normalizeState(value) {
  const normalized = { version: STATE_VERSION };
  for (const [key, item] of Object.entries(value || {})) {
    if (!ALLOWED_KEYS.has(key)) continue;
    if (key === 'defaultQuality') {
      if (['aac-lc', 'alac', 'aac', 'atmos'].includes(item)) normalized[key] = item;
      continue;
    }
    if (key === 'defaultExistingPolicy') {
      if (['skip', 'overwrite', 'fail'].includes(item)) normalized[key] = item;
      continue;
    }
    if (key === 'localPort') {
      if (Number.isInteger(item) && item >= 1 && item <= 65535) normalized[key] = item;
      continue;
    }
    if (['rebootRequired', 'resumeSetup', 'pendingCredentialDelivery'].includes(key)) {
      if (typeof item === 'boolean') normalized[key] = item;
      continue;
    }
    if (typeof item === 'string' && item.length <= 4096) normalized[key] = item;
  }
  return normalized;
}

class DesktopStateStore {
  constructor(statePath) {
    this.statePath = statePath;
  }

  load() {
    if (!fs.existsSync(this.statePath)) return { version: STATE_VERSION };
    try {
      const parsed = JSON.parse(fs.readFileSync(this.statePath, 'utf8'));
      if (parsed.version !== STATE_VERSION) return { version: STATE_VERSION };
      return normalizeState(parsed);
    } catch {
      return { version: STATE_VERSION };
    }
  }

  save(next) {
    const state = normalizeState({ ...this.load(), ...next, updatedAt: new Date().toISOString() });
    const directory = path.dirname(this.statePath);
    const temporaryPath = `${this.statePath}.tmp`;
    fs.mkdirSync(directory, { recursive: true });
    fs.writeFileSync(temporaryPath, `${JSON.stringify(state, null, 2)}\n`, { encoding: 'utf8', mode: 0o600 });
    fs.renameSync(temporaryPath, this.statePath);
    return state;
  }
}

module.exports = { DesktopStateStore, normalizeState, STATE_VERSION };

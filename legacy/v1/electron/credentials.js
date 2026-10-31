'use strict';

const fs = require('node:fs');
const path = require('node:path');

const ALLOWED_KEYS = new Set([
  'appleEmail',
  'applePassword',
  'mediaUserToken',
  'authorizationToken',
  'storefront',
]);

class CredentialStore {
  constructor({ credentialPath, safeStorage }) {
    this.credentialPath = credentialPath;
    this.safeStorage = safeStorage;
  }

  encryptionAvailable() {
    return Boolean(this.safeStorage && this.safeStorage.isEncryptionAvailable());
  }

  normalize(credentials) {
    const normalized = {};
    for (const [key, value] of Object.entries(credentials || {})) {
      if (ALLOWED_KEYS.has(key) && typeof value === 'string' && value) normalized[key] = value;
    }
    return normalized;
  }

  save(credentials) {
    if (!this.encryptionAvailable()) throw new Error('Credential encryption is unavailable on this system');
    const plaintext = JSON.stringify(this.normalize(credentials));
    const ciphertext = this.safeStorage.encryptString(plaintext).toString('base64');
    const temporaryPath = `${this.credentialPath}.tmp`;
    fs.mkdirSync(path.dirname(this.credentialPath), { recursive: true });
    fs.writeFileSync(temporaryPath, JSON.stringify({ version: 1, ciphertext }), { encoding: 'utf8', mode: 0o600 });
    fs.renameSync(temporaryPath, this.credentialPath);
  }

  load() {
    if (!fs.existsSync(this.credentialPath)) return {};
    if (!this.encryptionAvailable()) throw new Error('Credential encryption is unavailable on this system');
    const document = JSON.parse(fs.readFileSync(this.credentialPath, 'utf8'));
    if (document.version !== 1 || typeof document.ciphertext !== 'string') {
      throw new Error('Credential store format is invalid');
    }
    return this.normalize(JSON.parse(this.safeStorage.decryptString(Buffer.from(document.ciphertext, 'base64'))));
  }
}

module.exports = { CredentialStore };

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { CredentialStore } = require('../../electron/credentials');

test('credentials are encrypted at rest and round-trip through safeStorage', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-credentials-'));
  const credentialPath = path.join(directory, 'credentials.bin');
  const safeStorage = {
    isEncryptionAvailable: () => true,
    encryptString: (text) => Buffer.from(`cipher:${text}`, 'utf8'),
    decryptString: (buffer) => buffer.toString('utf8').replace(/^cipher:/, ''),
  };
  const store = new CredentialStore({ credentialPath, safeStorage });

  store.save({ appleEmail: 'person@example.test', applePassword: 'private-password' });

  const onDisk = fs.readFileSync(credentialPath, 'utf8');
  assert.equal(onDisk.includes('private-password'), false);
  assert.deepEqual(store.load(), { appleEmail: 'person@example.test', applePassword: 'private-password' });
});

test('credential store refuses to persist plaintext if OS encryption is unavailable', () => {
  const store = new CredentialStore({
    credentialPath: path.join(os.tmpdir(), 'amdl-no-encryption.bin'),
    safeStorage: { isEncryptionAvailable: () => false },
  });

  assert.throws(() => store.save({ applePassword: 'private-password' }), /encryption is unavailable/);
});

'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class FakeElement {
  constructor() {
    this.value = '';
    this.textContent = '';
    this.disabled = false;
    this.listeners = new Map();
  }

  addEventListener(type, listener) { this.listeners.set(type, listener); }
}

async function loadCredentialsPage(saveResult) {
  const form = new FakeElement();
  const submit = new FakeElement();
  const result = new FakeElement();
  const elements = Object.fromEntries([
    'credentials-form', 'apple-email', 'apple-password', 'media-token', 'authorization-token', 'storefront', 'result',
  ].map((id) => [id, new FakeElement()]));
  elements['credentials-form'] = form;
  elements.result = result;
  form.querySelector = () => submit;
  form.reset = () => {
    for (const element of Object.values(elements)) element.value = '';
  };
  const source = fs.readFileSync(path.join(__dirname, '..', '..', 'src', 'main', 'credentials.js'), 'utf8');
  vm.runInNewContext(source, {
    document: { getElementById: (id) => elements[id] },
    window: {
      credentials: {
        summary: async () => ({}),
        save: async () => saveResult,
      },
    },
  }, { filename: 'credentials.js' });
  await new Promise((resolve) => setImmediate(resolve));
  await form.listeners.get('submit')({ preventDefault() {} });
  return { result, submit };
}

test('credential page distinguishes an applied save whose main-window reconnect failed', async () => {
  const page = await loadCredentialsPage({
    ok: true,
    saved: true,
    applied: true,
    pending: false,
    reconnected: false,
    error: { code: 'service_reconnect_failed' },
  });

  assert.match(page.result.textContent, /已加密保存并应用/);
  assert.match(page.result.textContent, /主界面重连失败/);
  assert.equal(page.submit.disabled, false);
});

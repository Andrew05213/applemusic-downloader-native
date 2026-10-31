'use strict';

const form = document.getElementById('credentials-form');
const result = document.getElementById('result');

async function initialize() {
  try {
    const summary = await window.credentials.summary();
    document.getElementById('apple-email').value = summary.appleEmail || '';
    document.getElementById('storefront').value = summary.storefront || '';
  } catch (error) {
    result.textContent = `无法读取凭据设置：${error.message}`;
  }
}

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  const submit = form.querySelector('button[type="submit"]');
  submit.disabled = true;
  result.textContent = '';
  try {
    const response = await window.credentials.save({
      appleEmail: document.getElementById('apple-email').value,
      applePassword: document.getElementById('apple-password').value,
      mediaUserToken: document.getElementById('media-token').value,
      authorizationToken: document.getElementById('authorization-token').value,
      storefront: document.getElementById('storefront').value,
    });
    form.reset();
    if (response.pending) {
      result.textContent = '已加密保存。当前有活动任务，将在任务结束或下次启动时安全应用。';
    } else if (response.applied && response.reconnected === false) {
      result.textContent = '已加密保存并应用到本机服务，但主界面重连失败。请从“服务 → 启动/重新连接”重试。';
    } else {
      result.textContent = '已加密保存并应用到本机服务。';
    }
  } catch (error) {
    result.textContent = `无法保存凭据：${error.message}`;
  } finally {
    submit.disabled = false;
  }
});

initialize();

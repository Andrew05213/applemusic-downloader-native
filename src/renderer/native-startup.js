'use strict';
(() => {
  const api = window.nativeStartup;
  const error = document.getElementById('startup-error');
  let received = false;
  function render(value) {
    const failed = value?.phase === 'failed';
    document.body.dataset.phase = failed ? 'failed' : 'loading';
    error.hidden = !failed;
  }
  api.onState(value => { received = true; render(value); });
  api.getState().then(value => { if (!received) render(value); }).catch(() => { if (!received) render({ phase: 'failed' }); });
  for (const [id, action] of [['startup-enter', 'enter'], ['startup-quit', 'quit']]) {
    const button = document.getElementById(id);
    button.onclick = async () => {
      button.disabled = true;
      try { await api[action](); }
      catch { document.getElementById('startup-message').textContent = '暂时无法完成操作，请重试。'; }
      finally { button.disabled = false; }
    };
  }
})();

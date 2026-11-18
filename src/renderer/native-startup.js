'use strict';
(() => {
  const api = window.nativeStartup;
  const error = document.getElementById('startup-error');
  const message = document.getElementById('startup-message');
  let received = false;
  function render(value) {
    const failed = value?.phase === 'failed';
    document.body.dataset.phase = failed ? 'failed' : 'loading';
    error.hidden = !failed;
    if (failed) message.textContent = value.reason === 'version_mismatch'
      ? '界面与本机服务版本不一致。请退出后用同一安装包重新安装。'
      : value.reason === 'local_service' ? '本机服务未就绪。可以导出诊断，或进入应用重试。'
      : '启动暂未完成，可以进入应用查看状态。';
  }
  api.onState(value => { received = true; render(value); });
  api.getState().then(value => { if (!received) render(value); }).catch(() => { if (!received) render({ phase: 'failed' }); });
  for (const [id, action] of [['startup-enter', 'enter'], ['startup-quit', 'quit'], ['startup-export', 'exportDiagnostics']]) {
    const button = document.getElementById(id);
    button.onclick = async () => {
      button.disabled = true;
      try {
        const result = await api[action]();
        if (action === 'exportDiagnostics' && result) message.textContent = '诊断已保存，不包含账号或会话凭据。';
      }
      catch { message.textContent = '暂时无法完成操作，请重试。'; }
      finally { button.disabled = false; }
    };
  }
})();

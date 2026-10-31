'use strict';

const form = document.getElementById('setup-form');
const downloadDir = document.getElementById('download-dir');
const beginButton = document.getElementById('begin-button');
const repairButton = document.getElementById('repair-button');
const restartButton = document.getElementById('restart-button');
const diagnosticsButton = document.getElementById('diagnostics-button');
const serviceRepairButton = document.getElementById('service-repair-button');
const diagnosticsPanel = document.getElementById('diagnostics-panel');
const diagnosticsOutput = document.getElementById('diagnostics-output');
const title = document.getElementById('status-title');
const message = document.getElementById('status-message');
const action = document.getElementById('status-action');
const copyDiagnostics = document.getElementById('copy-diagnostics');
if (copyDiagnostics) copyDiagnostics.addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(diagnosticsOutput.textContent);
    copyDiagnostics.textContent = '已复制';
  } catch {
    copyDiagnostics.textContent = '请选中诊断文字手动复制';
    diagnosticsOutput.focus();
  }
});

const stepOrder = ['detecting', 'wsl_action', 'downloading', 'directory', 'credentials', 'starting', 'complete'];
const serviceRepairErrorCodes = new Set([
  'distro_corrupt',
  'health_timeout',
  'windows_lock_protocol',
  'path_change_recovery_failed',
]);
const networkPreflightStages = new Set([
  'helper_identity',
  'helper_launch',
  'helper_protocol',
  'proxy_resolution',
  'native_probe_invalid',
  'native_startup',
  'dns',
  'tcp_connect',
  'tls',
  'ca_bundle_missing',
  'ca_bundle_invalid',
  'url_bag_fetch',
  'url_bag_parse',
  'url_bag_key_lookup',
]);
const downloadPathChangeCopy = Object.freeze({
  path_change_active_jobs: Object.freeze({
    title: '当前有下载任务，不能切换目录',
    message: '请等待任务完成或取消任务后重试；当前目录和服务保持不变。',
    action: '请等待任务完成或取消任务后重试。',
  }),
  path_change_failed_recovered: Object.freeze({
    title: '下载目录切换失败，已恢复原目录和服务',
    message: '目标目录未被采用，现有任务和服务已保留。',
    action: '请确认目标目录可写后重试。',
  }),
  path_change_recovery_failed: Object.freeze({
    title: '下载目录切换和自动恢复均未完成',
    message: '应用已停止以保护现有任务数据。',
    action: '请执行“修复服务”后重试。',
  }),
});

function safeNetworkPreflightStage(error) {
  const stage = error?.details?.stage;
  return error?.code === 'network_preflight_failed'
    && typeof stage === 'string'
    && networkPreflightStages.has(stage)
    ? stage
    : '';
}

function networkPreflightCopy(error) {
  const stage = safeNetworkPreflightStage(error);
  if (!stage) {
    return error?.code === 'network_preflight_failed'
      ? {
        title: 'Apple 认证服务网络预检未通过',
        message: '无法确认 Windows 到 Apple 认证服务的受信网络路径。',
        action: '请查看脱敏诊断中的失败阶段后重试；无需重新输入密码或验证码。',
      }
      : null;
  }
  if (['helper_identity', 'helper_launch', 'helper_protocol'].includes(stage)) {
    return {
      title: 'Windows 网络桥接未能启动',
      message: '本机网络桥接组件未能完成安全启动。',
      action: '请重新启动 Apple Music Downloader 后重试；无需重新输入密码或验证码。',
    };
  }
  if (['proxy_resolution', 'dns', 'tcp_connect'].includes(stage)) {
    return {
      title: 'Windows 代理或网络连接失败',
      message: '无法通过 Windows 的代理或网络连接 Apple 认证服务。',
      action: '请检查 Windows 网络和代理是否可用，然后重试；无需重新输入密码或验证码。',
    };
  }
  if (['ca_bundle_missing', 'ca_bundle_invalid'].includes(stage)) {
    return {
      title: 'Appliance 证书库不可用',
      message: 'Android 运行时缺少或无法验证受信证书库。',
      action: '请修复或重新安装 WSL Appliance，然后重试；无需重新输入密码或验证码。',
    };
  }
  if (['native_probe_invalid', 'native_startup'].includes(stage)) {
    return {
      title: 'Appliance 网络预检组件不可用',
      message: 'Android 运行时的独立网络预检组件未能通过安全启动检查。',
      action: '请修复或重新安装 WSL Appliance，然后重试；无需重新输入密码或验证码。',
    };
  }
  if (stage === 'tls') {
    return {
      title: '与 Apple 服务的安全连接失败',
      message: '无法建立到 Apple 认证服务的安全 TLS 连接。',
      action: '请检查 Windows 网络或代理证书设置，然后重试；无需重新输入密码或验证码。',
    };
  }
  return {
    title: 'Apple 认证服务地址不可用',
    message: '无法获取或验证 Apple 认证服务地址。',
    action: '请检查网络或代理，或稍后重试；无需重新输入密码或验证码。',
  };
}

function renderProgress(progress) {
  if (progress?.step === 'updating') {
    const phases = { checking: '检查运行时身份', artifacts: '准备更新组件', network: '检查 Apple 认证网络',
      submitting: '更新运行时并检查健康状态', reconciling: '确认更新结果', starting: '启动新版本服务', complete: '更新完成' };
    const upgrade = progress.upgrade;
    if (!upgrade || !Object.hasOwn(phases, upgrade.phase)
        || !/^\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(upgrade.fromVersion)
        || !/^\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(upgrade.toVersion)) return;
    title.textContent = phases[upgrade.phase];
    message.textContent = `后端 ${upgrade.fromVersion} → ${upgrade.toVersion}`;
    action.textContent = upgrade.phase === 'complete' ? '已核对新运行时身份。' : '正在处理，请等待当前步骤完成。';
    beginButton.disabled = upgrade.phase !== 'complete';
    return;
  }
  if (progress.error) {
    renderError(progress.error);
    return;
  }
  serviceRepairButton.hidden = true;
  const step = progress.step || progress.setupStep || 'detecting';
  const activeIndex = stepOrder.indexOf(step === 'importing' ? 'downloading' : step);
  document.querySelectorAll('.steps li').forEach((item) => {
    const index = stepOrder.indexOf(item.dataset.step);
    item.classList.toggle('active', index === activeIndex);
    item.classList.toggle('complete', activeIndex > index || step === 'complete');
  });
  const labels = {
    detecting: ['正在检测 Windows 与 WSL2…', '不会修改其他 WSL 发行版或默认设置。'],
    wsl_action: ['WSL2 需要处理', '向导可以执行固定的 Windows 官方安装或更新操作。'],
    reboot: ['需要重新启动 Windows', '重启后向导会从当前步骤继续。'],
    downloading: ['正在校验 Appliance…', '下载后会先验证大小和 SHA-256，再进行导入。'],
    importing: ['正在导入专用发行版…', '发行版名固定为 AppleMusicDownloader。'],
    directory: ['正在验证下载目录…', '会检查创建、写入、重命名与删除能力。'],
    credentials: ['正在加密凭据…', '秘密不会写入 .env、日志或进程命令行。'],
    starting: ['正在启动本机服务…', '健康检查最长等待 180 秒。'],
    complete: ['设置完成', '即将打开下载界面。'],
    failed: ['设置未能完成', '数据不会因重试而被删除。'],
  };
  let currentLabels = labels[step] || labels.failed;
  if (step === 'wsl_action') {
    const operation = progress.operation === 'update' ? '更新' : '安装';
    currentLabels = progress.actionCompleted
      ? [`WSL2 ${operation}命令已完成`, 'Windows 功能检查已返回成功。点击“继续安装并启动”完成本应用设置。']
      : [`正在${operation === '安装' ? '启用 Windows 功能' : '更新 WSL2'}…`, '请在可见的管理员终端窗口中等待固定命令完成。'];
    action.textContent = progress.actionCompleted ? '' : '请勿关闭管理员终端或本应用窗口。';
    repairButton.hidden = true;
    restartButton.hidden = true;
    beginButton.textContent = progress.actionCompleted ? '继续安装并启动' : '安装并启动';
  } else {
    action.textContent = '';
  }
  [title.textContent, message.textContent] = currentLabels;
  if (progress.downloadDir) downloadDir.value = progress.downloadDir;
}

function renderError(error) {
  const networkCopy = networkPreflightCopy(error);
  const code = error?.code || '';
  const pathCopy = downloadPathChangeCopy[code] || null;
  title.textContent = networkCopy?.title || pathCopy?.title || error?.message || '设置未能完成';
  if (networkCopy) message.textContent = networkCopy.message;
  else if (code === 'wsl_missing') message.textContent = '安装将启用 Windows Subsystem for Linux 和虚拟机平台，可能需要重启。';
  else if (code === 'wsl_update_required') message.textContent = '更新 WSL2 组件可能需要管理员授权或重启 Windows。';
  else if (code === 'runtime_reinstall_required') message.textContent = '当前 WSL Appliance 已保留，不能由本版本自动原地升级。';
  else if (code === 'path_change_active_jobs') message.textContent = '当前有下载任务，不能切换目录。';
  else if (code === 'path_change_failed_recovered') message.textContent = '下载目录切换失败，已恢复原目录和服务。';
  else if (code === 'path_change_recovery_failed') message.textContent = '下载目录切换和自动恢复均未完成。';
  else if (pathCopy) message.textContent = pathCopy.message;
  else message.textContent = '已保留现有任务数据和下载文件。';
  action.textContent = networkCopy?.action || pathCopy?.action || error?.action || '请查看诊断信息后重试。';
  serviceRepairButton.hidden = !serviceRepairErrorCodes.has(code);
  serviceRepairButton.textContent = '修复服务';
  repairButton.hidden = !['wsl_missing', 'wsl_update_required'].includes(code);
  repairButton.dataset.code = code;
  repairButton.textContent = code === 'wsl_update_required' ? '更新 WSL2' : '安装 WSL2';
  restartButton.hidden = code !== 'reboot_required';
  beginButton.textContent = '安装并启动';
}

document.getElementById('choose-directory').addEventListener('click', async () => {
  const selected = await window.applianceSetup.chooseDownloadDirectory();
  if (selected) downloadDir.value = selected;
});

repairButton.addEventListener('click', async () => {
  const code = repairButton.dataset.code;
  repairButton.disabled = true;
  renderProgress({ step: 'wsl_action', operation: code === 'wsl_update_required' ? 'update' : 'install', visibleTerminal: true });
  try {
    const result = await window.applianceSetup.applyWslAction(code);
    if (!result.ok) renderError(result.error);
    else renderProgress({ step: 'wsl_action', operation: code === 'wsl_update_required' ? 'update' : 'install', actionCompleted: true });
  } catch (error) {
    renderError(error);
  } finally {
    repairButton.disabled = false;
  }
});

restartButton.addEventListener('click', () => window.applianceSetup.restartWindows());

diagnosticsButton.addEventListener('click', async () => {
  diagnosticsButton.disabled = true;
  diagnosticsButton.textContent = '正在读取…';
  try {
    const diagnostic = await window.applianceSetup.getDiagnostics();
    diagnosticsOutput.textContent = typeof diagnostic === 'string' && diagnostic.trim()
      ? diagnostic
      : '暂无诊断输出。';
  } catch (error) {
    diagnosticsOutput.textContent = `无法读取诊断日志：${error?.message || '未知错误'}`;
  } finally {
    diagnosticsPanel.hidden = false;
    diagnosticsButton.disabled = false;
    diagnosticsButton.textContent = '刷新诊断日志';
    diagnosticsButton.setAttribute('aria-expanded', 'true');
    diagnosticsOutput.scrollTop = diagnosticsOutput.scrollHeight;
  }
});

serviceRepairButton.addEventListener('click', async () => {
  serviceRepairButton.disabled = true;
  serviceRepairButton.textContent = '修复中…';
  try {
    const result = await window.applianceSetup.repairRuntime();
    if (result?.cancelled) return;
    if (!result?.ok) {
      renderError(result?.error || { code: 'repair_failed', message: 'WSL Appliance 修复未能完成。' });
      return;
    }
    renderProgress({ step: 'starting' });
  } catch {
    renderError({
      code: 'repair_failed',
      message: 'WSL Appliance 修复未能完成。',
      action: '请查看脱敏诊断日志后重试。',
    });
  } finally {
    serviceRepairButton.disabled = false;
    if (!serviceRepairButton.hidden) serviceRepairButton.textContent = '修复服务';
  }
});

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  beginButton.disabled = true;
  action.textContent = '';
  const result = await window.applianceSetup.begin({
    downloadDir: downloadDir.value,
    credentials: {
      appleEmail: document.getElementById('apple-email').value,
      applePassword: document.getElementById('apple-password').value,
      mediaUserToken: document.getElementById('media-token').value,
      authorizationToken: document.getElementById('authorization-token').value,
      storefront: document.getElementById('storefront').value,
    },
  });
  if (!result.ok) {
    renderError(result.error);
    beginButton.disabled = false;
  }
});

window.applianceSetup.onProgress(renderProgress);
window.applianceSetup.getState().then((state) => renderProgress(state));

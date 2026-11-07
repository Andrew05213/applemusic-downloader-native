'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const motion = window.nativeMotion;
  const accountInput=window.nativeAccountInput.attach($('login-email'));
  window.nativeLoginAccount=accountInput;
  let state = 'checking'; let pending = false;
  const messages = { checking: '正在恢复并验证已有会话…', needs_login: '使用 Apple 账户继续', authenticating: '正在登录，请稍候…', needs_2fa: '请输入本次登录的六位验证码', authenticated: '登录成功', unverified: '暂时无法验证已有会话，请先重试', runtime_unavailable: '本机服务暂不可用，请重试' };
  messages.needs_interaction='请完成下方 Apple 账户验证提示';
  const stageMessages={device_login:'正在验证 Apple 账户…',device_code:'正在验证设备验证码…',music_service_start:'正在启动 Music 服务…',music_tokens:'正在获取 Music 会话…',music_storefront:'正在校验 Music 会话…'};
  const errors = {
    native_login_failed:'登录未完成，请导出诊断后重试。',native_auth_cancelled:'本次登录已取消。',native_auth_required:'请先完成登录。',native_session_unverified:'暂时无法确认 Music 会话，请重试。',native_auth_busy:'正在处理登录，请稍候。',native_auth_input_invalid:'登录信息格式不正确。',
    native_login_process_start_failed:'原生登录进程无法启动。请导出诊断。',
    native_login_process_exited:'原生登录进程异常退出。请导出诊断。',
    native_login_process_signalled:'原生登录进程被中止。请导出诊断。',
    native_login_protocol_failed:'原生登录进程通信失败。请导出诊断。',
    native_login_process_timeout:'原生登录未在时限内完成。请导出诊断。',
    native_login_store_no_response:'原生登录未取得账号验证响应。请导出诊断。',
    native_login_store_response_failed:'原生账号验证未通过。请导出诊断；这不一定是密码错误。',
    native_login_store_bridge_failed:'原生账号验证组件初始化失败。请导出诊断。',
    native_login_input_invalid:'原生登录组件未接受本次输入。请导出诊断。',
    native_phone_invalid:'请检查手机号和国家区号；中国大陆默认 +86，已带区号的号码不必重复填写。',
    native_login_interaction_timeout:'验证提示已超时，请重新登录。',
    native_login_interaction_unavailable:'验证交互通道不可用，请重新登录或导出诊断。',
    native_login_interaction_unsupported:'Apple 要求的此步骤暂不支持在原生窗口完成，请查看提示说明。',
    native_login_internal_error:'原生认证组件发生异常，请导出诊断。',
    native_login_token_cache_failed:'账号认证完成，但保存 Music 会话失败，请导出诊断。',
    native_music_developer_rejected:'目录授权被拒绝（401），不是设备验证码错误；请重新验证会话或导出诊断。',
    native_music_user_rejected:'Music 用户会话被拒绝（403）；请重新登录或导出诊断，不代表设备验证码错误。',
    native_music_rate_limited:'Music 请求过于频繁（429），请稍后重新验证会话。',
    native_music_upstream_failed:'Music 服务暂不可用，请稍后重试。',
    native_music_http_failed:'Music 会话校验请求被拒绝，请导出诊断。',
    native_music_response_invalid:'Music 会话响应格式异常，请导出诊断。',
    native_network_dns_failed:'无法解析 Music 服务地址，请检查网络后重试。',
    native_network_timeout:'Music 会话校验超时，请稍后重试。',
    native_tls_certificate_invalid:'Music 连接证书校验失败，请导出诊断。',
    native_network_connection_failed:'无法连接 Music 服务，请检查网络后重试。',
  };
  function failure(error) {
    const message = String(error?.message || '');
    if (message.includes('native_runtime_isolated')) return '当前资源包仅供离线验证，不能登录。请使用启用网络的原生资源包。';
    for (const code of Object.keys(errors)) if (message.includes(code)) return errors[code];
    return '暂未完成登录，请检查状态后重试。';
  }
  function controls() {
    $('login-recheck').disabled = pending || ['authenticating','needs_interaction','needs_2fa','checking'].includes(state);
    const codeReady = state === 'needs_2fa';
    $('login-submit').hidden = state !== 'needs_login' && !codeReady;
    $('login-submit').disabled = pending;
    $('login-submit').textContent = codeReady ? '验证' : '登录';
  }
  function render(value) {
    const previous = state;
    // A stale retired challenge is not a device-code challenge.
    if(value?.factor!=null)value={state:'needs_login',code:'native_login_failed',diagnostics:value.diagnostics};
    state = messages[value?.state] ? value.state : 'runtime_unavailable';
    $('login-status').textContent = ['authenticating','checking'].includes(state)?stageMessages[value?.stage]||messages[state]:messages[state];
    const lastFailure=['needs_login','unverified'].includes(state)?value?.diagnostics?.lastFailure:undefined;
    const currentCode=value?.code;
    // Show the actual boundary failure rather than its generic provider wrapper.
    // An older failure must not replace an error from another stage.
    const leafIsCurrent=lastFailure&&lastFailure.stage===value?.stage&&['native_gsa_provider_unavailable','native_login_failed','native_session_unverified','native_auth_required'].includes(currentCode);
    const errorCode=leafIsCurrent?lastFailure.code:currentCode||lastFailure?.code;
    $('login-error').textContent = errors[errorCode] || '';
    motion?.feedback($('login-error'),$('login-error').textContent);
    const codeReady = state === 'needs_2fa';
    $('account-fields').hidden = state !== 'needs_login'; $('code-field').hidden = !codeReady;
    $('login-email').required = $('login-password').required = state === 'needs_login'; $('login-code').required = codeReady;
    $('two-factor-help').hidden = !codeReady;
    if (!codeReady) $('two-factor-help').open = false;
    if (codeReady && previous !== 'needs_2fa') $('login-code').focus();
    if(previous!==state)motion?.enter(codeReady?$('code-field'):$('account-fields'),'panel');
    controls();
  }
  window.nativeDesktop.onSession(render);
  window.nativeDesktop.getState().then(render).catch(() => render({ state: 'runtime_unavailable' }));
  $('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    if (pending || (state !== 'needs_login' && state !== 'needs_2fa')) return;
    pending = true; $('login-error').textContent = ''; motion?.feedback($('login-error'),''); controls();
    try {
      let next;
      if (state === 'needs_login') {const account=accountInput.read();$('login-email').value=account;accountInput.refresh();next = await window.nativeDesktop.login({ appleEmail: account, applePassword: $('login-password').value });}
      else next = await window.nativeDesktop.submitCode($('login-code').value);
      render(next);
    } catch (error) { $('login-error').textContent = failure(error);motion?.feedback($('login-error'),$('login-error').textContent); }
    finally { $('login-password').value = ''; $('login-code').value = ''; pending = false; controls(); }
  });
  $('login-recheck').addEventListener('click', async () => { if (pending) return; $('login-error').textContent = ''; motion?.feedback($('login-error'),''); try { render(await window.nativeDesktop.recheck()); } catch { render({ state: 'runtime_unavailable' }); } });
  $('login-cancel').addEventListener('click', () => window.nativeDesktop.cancelLogin().catch(() => {}));
  motion?.enter(document.querySelector('.login-content'),'panel');
})();

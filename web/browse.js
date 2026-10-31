'use strict';

window.amdlBrowse = (({ renderer, desktopShell }) => {
  const { api, jobs, isActive, loadJobs, downloadSubmissionReady, queueNeedsRecovery,
    qualityAvailable, capabilityMessage } = renderer;
  const section = document.getElementById('browse-section');
  const viewport = document.getElementById('embedded-browser-viewport');
  const status = document.getElementById('browse-web-status');
  const quality = document.getElementById('default-quality');
  const policy = document.getElementById('default-existing-policy');
  const state = { generation: 0, visible: false, opening: false, boundsPending: false, boundsDirty: false, queuePending: false };
  let webStatus = { status: 'opening', message: status.textContent };
  // Render both outputs from the result, never infer state from translated copy.
  function renderStatus(result) {
    webStatus = result;
    const { message } = result;
    status.textContent = message;
    const button = document.getElementById('browse-current-download');
    button.title = message || '下载当前页面';
    const feedback = ['resolving', 'submitting'].includes(result.status) ? '…'
      : ['queued', 'duplicate'].includes(result.status) ? '✓'
        : ['unavailable', 'invalid', 'failed'].includes(result.status) ? '!' : '';
    button.dataset.feedback = feedback ? 'message' : 'idle';
    button.querySelector('[data-download-feedback]').textContent = feedback;
  }

  function isBrowseVisible() {
    return !section.hidden && document.body.dataset.view === 'browse' && document.body.dataset.browseMode === 'web';
  }

  function supportedCurrentURL(value, allowLibrary = false) {
    if (typeof value !== 'string' || value.length > 2048 || !/^https:\/\/music\.apple\.com(?:[/?]|$)/.test(value) || value.includes('#')) return '';
    try {
      const parsed = new URL(value);
      if (parsed.origin !== 'https://music.apple.com' || parsed.username || parsed.password || parsed.port) return '';
      const segments = parsed.pathname.replace(/\/$/, '').split('/').slice(1);
      if (allowLibrary && segments.length === 4 && /^[a-z]{2}$/.test(segments[0]) && segments[1] === 'library' && segments[2] === 'playlist' && /^p\.[A-Za-z0-9_-]{1,510}$/.test(segments[3]) && !parsed.search) return parsed.origin + parsed.pathname;
      if (segments.length < 3 || !/^[a-z]{2}$/.test(segments[0]) || !['song', 'album', 'music-video', 'playlist', 'station'].includes(segments[1])) return '';
      if (segments.some((part) => !part || /%(?:2f|5c|00)/i.test(part))) return '';
      const id = segments.at(-1);
      if (segments[1] === 'playlist' ? !/^pl\.[A-Za-z0-9_-]{1,508}$/.test(id)
        : segments[1] === 'station' ? !/^ra\.[A-Za-z0-9_-]{1,508}$/.test(id)
          : !/^(?:id)?\d{1,20}$/.test(id)) return '';
      const seen = new Set();
      for (const [key, item] of parsed.searchParams) {
        if (seen.has(key) || !['i', 'l', 'itsct', 'itscg', 'uo'].includes(key)) return '';
        seen.add(key);
        if (key === 'i' ? !/^\d{1,20}$/.test(item) : !/^[A-Za-z0-9._~-]{1,128}$/.test(item)) return '';
      }
      if (segments[1] !== 'playlist' && parsed.searchParams.has('l')) return '';
      const clean = new URL(parsed.origin + parsed.pathname);
      const song = parsed.searchParams.get('i');
      const language = parsed.searchParams.get('l');
      if (song !== null) {
        if (!/^\d{1,20}$/.test(song)) return '';
        clean.searchParams.set('i', song);
      }
      if (language !== null) {
        if (language.length > 32) return '';
        clean.searchParams.set('l', language);
      }
      return clean.toString();
    } catch { return ''; }
  }

  async function queueCurrent(url, nativeCatalog = false, onProgress = () => {}) {
    if (state.queuePending) return { status: 'busy', message: '下载请求正在处理中，请稍候。' };
    if (!downloadSubmissionReady() || queueNeedsRecovery()) {
      return { status: 'unavailable', message: '当前无法提交下载，请到“设置与状态”检查服务、队列和默认音质。' };
    }
    const clean = supportedCurrentURL(url, nativeCatalog);
    if (!clean) {
      return { status: 'invalid', message: '当前页面不是可下载的公开歌曲、专辑、歌单、音乐视频或电台。' };
    }
    const selectedQuality = quality.value;
    const selectedPolicy = policy.value;
    if (!qualityAvailable(selectedQuality)) {
      return { status: 'unavailable', message: capabilityMessage(selectedQuality) || '默认音质当前不可用。' };
    }
    state.queuePending = true;
    onProgress({ status: 'resolving', message: '正在加入下载队列：检查下载地区…' });
    let resolving = true;
    try {
      const resolution = nativeCatalog ? { schemaVersion: 1, url: clean, sourceStorefront: new URL(clean).pathname.split('/')[1], targetStorefront: new URL(clean).pathname.split('/')[1], converted: false } : await api('/api/v1/browser-download/resolve', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: clean }),
      });
      const resolved = supportedCurrentURL(resolution?.url, nativeCatalog);
      if (resolution?.schemaVersion !== 1 || !resolved
          || resolution.sourceStorefront !== new URL(clean).pathname.split('/')[1]
          || resolution.targetStorefront !== new URL(resolved).pathname.split('/')[1]
          || resolution.converted !== (resolved !== clean)) {
        return { status: 'failed', message: '地区转换回执不完整，未创建下载任务，请更新或修复后端后重试。' };
      }
      if (!downloadSubmissionReady() || queueNeedsRecovery() || !qualityAvailable(selectedQuality)) {
        return { status: 'unavailable', message: '服务状态已变化，未创建下载任务，请到“设置与状态”检查后重试。' };
      }
      if ([...jobs.values()].some((job) => isActive(job)
          && supportedCurrentURL(job.url, nativeCatalog) === resolved && job.quality === selectedQuality
          && (job.existingPolicy || 'skip') === selectedPolicy)) {
        return { status: 'duplicate', message: '当前页面对应资源已在队列中，请到下载任务查看。' };
      }
      resolving = false;
      onProgress({ status: 'submitting', message: '正在加入下载队列…' });
      const receipt = await api('/api/v1/jobs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ urls: [resolved], quality: selectedQuality, existingPolicy: selectedPolicy, existing_policy: selectedPolicy }),
      });
      if (!Array.isArray(receipt?.jobs) || receipt.jobs.length !== 1 || typeof receipt.jobs[0]?.id !== 'string' || !receipt.jobs[0].id) {
        return { status: 'failed', message: '队列回执不完整，请到下载任务核对后再试。' };
      }
      jobs.acceptCreated(receipt.jobs[0]);
      const message = resolution.converted
        ? `已转换到 ${resolution.targetStorefront.toUpperCase()} 地区，当前页面已加入下载队列。`
        : '当前页面已加入下载队列。';
      const result = { status: 'queued', message };
      onProgress(result);
      try { await loadJobs(); } catch { /* The complete receipt is authoritative. */ }
      return result;
    } catch (error) {
      const message = resolving
        ? error.status === 422 ? '配置的下载地区没有可播放的对应资源，未创建任务。请检查账号地区或选择其他地区链接。'
          : error.status === 404 ? '后端尚不支持地区转换，未创建任务，请更新或修复运行时后重试。'
            : '下载地区转换失败，未创建任务，请检查服务连接和账号地区后重试。'
        : '加入下载队列失败，请检查服务连接后重试。';
      return { status: 'failed', message };
    }
    finally { state.queuePending = false; }
  }

  async function queueWebPage(url) {
    if (state.queuePending) return;
    renderStatus(await queueCurrent(url, false, renderStatus));
  }

  async function downloadCurrentPage() {
    if (!desktopShell?.currentAppleMusicURL) {
      renderStatus({ status: 'unavailable', message: '内嵌网页仅在桌面客户端提供。' });
      return;
    }
    const result = await desktopShell.currentAppleMusicURL();
    if (!result?.ok) {
      renderStatus({ status: 'unavailable', message: '当前页面暂时不可下载。' });
      return;
    }
    await queueWebPage(result.url);
  }

  async function updateBounds() {
    if (!isBrowseVisible() || state.opening || !desktopShell?.openAppleMusic) return;
    const generation = state.generation;
    state.boundsDirty = false;
    state.opening = true;
    try {
      if (!state.visible) {
        const opened = await desktopShell.openAppleMusic();
        if (!opened?.ok) throw new Error('browser unavailable');
        state.visible = true;
      }
      if (generation !== state.generation || !isBrowseVisible()) {
        state.visible = false;
        await desktopShell.closeAppleMusic();
        return;
      }
      // Loading can take seconds: measure after it completes, not before a sidebar resize.
      const rect = viewport.getBoundingClientRect();
      const x = Math.max(0, Math.ceil(rect.left));
      const y = Math.max(0, Math.ceil(rect.top));
      const width = Math.min(window.innerWidth, Math.floor(rect.right)) - x;
      const height = Math.min(window.innerHeight, Math.floor(rect.bottom)) - y;
      if (width < 200 || height < 160) {
        state.visible = false;
        await desktopShell.closeAppleMusic();
        return;
      }
      const result = await desktopShell.setAppleMusicBounds({ x, y, width, height });
      if (!result?.ok) throw new Error('bounds unavailable');
      if (generation !== state.generation || !isBrowseVisible()) {
        state.visible = false;
        await desktopShell.closeAppleMusic();
      } else if (webStatus.status === 'opening' || webStatus.code === 'browser_unavailable') {
        renderStatus({ status: 'idle', message: '' });
      }
    } catch {
      if (generation === state.generation) renderStatus({ status: 'unavailable', code: 'browser_unavailable', message: '内嵌网页暂时不可用，请到设置与状态检查桌面环境。' });
    } finally {
      state.opening = false;
      if ((generation !== state.generation || state.boundsDirty) && isBrowseVisible()) scheduleBounds();
    }
  }

  function scheduleBounds() {
    state.boundsDirty = true;
    if (state.boundsPending) return;
    state.boundsPending = true;
    requestAnimationFrame(() => { state.boundsPending = false; updateBounds(); });
  }

  for (const [id, action] of [
    ['browse-web-back', 'appleMusicBack'], ['browse-web-forward', 'appleMusicForward'], ['browse-web-reload', 'appleMusicReload'],
  ]) document.getElementById(id).addEventListener('click', () => desktopShell?.[action]?.());
  document.getElementById('browse-current-download').addEventListener('click', downloadCurrentPage);
  window.addEventListener('amdl:browse-mode', () => {
    if (isBrowseVisible()) scheduleBounds();
    else {
      ++state.generation;
      if (state.visible) { state.visible = false; desktopShell?.closeAppleMusic?.(); }
    }
  });
  desktopShell?.onAppleMusicDownloadIntent?.((intent) => {
    if (isBrowseVisible() && typeof intent?.url === 'string') queueWebPage(intent.url);
  });
  window.addEventListener('amdl:view-changed', ({ detail }) => {
    if (detail.view === 'browse') scheduleBounds();
    else {
      ++state.generation;
      if (state.visible) {
        state.visible = false;
        desktopShell?.closeAppleMusic?.();
      }
    }
  });
  window.addEventListener('resize', scheduleBounds);
  window.addEventListener('scroll', scheduleBounds, { passive: true });
  if (typeof ResizeObserver !== 'undefined') new ResizeObserver(scheduleBounds).observe(viewport);
  // The remote WebContentsView receives neither this local adapter nor tokens.
  return Object.freeze({ queueCatalog: (url) => queueCurrent(url, true) });
})({ renderer: window.amdlRenderer, desktopShell: window.desktopShell });

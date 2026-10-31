'use strict';
(() => {
  const player = document.getElementById('playback-player');
  const title = document.getElementById('playback-title');
  const status = document.getElementById('playback-status');
  const media = document.getElementById('playback-media');
  let previewSelection = null;
  let fullPlaybackSelection = null;
  let playbackPending = false;
  let playbackRequestGeneration = 0;
  // Full playback and public preview share one request generation and must stop before switching.
  function canPlayFullTrack(item) {
    return item.type === 'songs' && /^[1-9]\d{0,19}$/.test(item.id) && typeof window.desktopShell?.playCatalogTrack === 'function';
  }
  function safeURL(value) {
    if (typeof value !== 'string' || value.length > 2048 || /[\r\n\t\\]/.test(value))
      return '';
    try {
      const u = new URL(value);
      // URL.port normalizes explicit :443 away; reject it in the raw authority too.
      const authority = value.match(/^https:\/\/([^/]+)/)?.[1];
      if (!authority || authority.includes(':') || u.username || u.password || u.hash
        || u.protocol !== 'https:' || !(u.hostname.endsWith('.mzstatic.com') || u.hostname === 'audio-ssl.itunes.apple.com'))
        return '';
      return u.href;
    }
    catch {
      return '';
    }
  }
  function syncPlaybackControls() {
    for (const control of document.querySelectorAll('[data-preview-url]')) {
      const selected = !!previewSelection && control.dataset.previewUrl === previewSelection.url;
      const active = selected && !previewSelection.audio.paused || !!fullPlaybackSelection && control.dataset.playId === fullPlaybackSelection.id;
      control.textContent = active ? 'Ⅱ' : '▶';
      control.setAttribute('aria-label', (active ? '暂停 ' : '播放 ') + control.dataset.previewTitle);
      control.setAttribute('aria-pressed', String(active));
      control.disabled = playbackPending || control.dataset.playAvailable !== 'true';
    }
  }
  function disposePreviewAudio() {
    const old = previewSelection;
    previewSelection = null;
    if (old) {
      old.audio.pause();
      old.audio.removeAttribute('src');
      old.audio.load();
      old.audio.remove();
    }
    syncPlaybackControls();
  }
  function stopPlayback() {
    const stopFull = fullPlaybackSelection || playbackPending;
    ++playbackRequestGeneration;
    fullPlaybackSelection = null;
    playbackPending = false;
    disposePreviewAudio();
    player.hidden = true;
    title.textContent = '';
    status.textContent = '';
    if (stopFull && typeof window.desktopShell?.stopCatalogPlayback === 'function') {
      void window.desktopShell.stopCatalogPlayback().catch(() => { });
    }
  }
  function reportPreviewFailure(selection) {
    if (previewSelection !== selection)
      return;
    selection.audio.pause();
    status.textContent = '试听暂时不可用，请稍后重试。';
    syncPlaybackControls();
  }
  async function playPreview(item, request) {
    const url = item.type === 'songs' && safeURL(item.previewUrl);
    if (!url)
      return;
    if (request !== playbackRequestGeneration)
      return;
    if (previewSelection?.url === url && !previewSelection.audio.paused) {
      previewSelection.audio.pause();
      return;
    }
    if (previewSelection?.url !== url || previewSelection.failed) {
      disposePreviewAudio();
      const audio = document.createElement('audio');
      audio.controls = true;
      audio.preload = 'none';
      audio.crossOrigin = 'anonymous';
      audio.setAttribute('controlslist', 'nodownload');
      audio.setAttribute('aria-label', '试听播放控制');
      const selection = { url, audio, failed: false };
      previewSelection = selection;
      for (const [event, message] of [['playing', '正在试听 · Apple Music'], ['pause', '已暂停'], ['ended', '试听结束'], ['waiting', '正在缓冲试听…']]) {
        audio.addEventListener(event, () => { if (previewSelection === selection) {
          status.textContent = message;
          syncPlaybackControls();
        } });
      }
      audio.addEventListener('error', () => { selection.failed = true; reportPreviewFailure(selection); });
      audio.src = url;
      media.replaceChildren(audio);
    }
    player.hidden = false;
    title.textContent = item.title + (item.artist ? ' · ' + item.artist : '');
    status.textContent = '正在加载试听…';
    const selection = previewSelection;
    try {
      await selection.audio.play();
    }
    catch (error) {
      // Pausing while the initial play is pending rejects it with AbortError;
      // that is a user action, not a broken preview or a reason to reload it.
      if (previewSelection === selection && !(error?.name === 'AbortError' && selection.audio.paused)) {
        selection.failed = true;
        reportPreviewFailure(selection);
      }
    }
  }
  async function play(item) {
    if (playbackPending)
      return;
    const url = item.type === 'songs' && safeURL(item.previewUrl);
    const canFull = canPlayFullTrack(item);
    if (!url && !canFull)
      return;
    const request = ++playbackRequestGeneration;
    playbackPending = true;
    syncPlaybackControls();
    const report = message => { document.getElementById('catalog-status').textContent = message; };
    try {
      if (fullPlaybackSelection?.id === item.id) {
        const stopped = await window.desktopShell.stopCatalogPlayback();
        if (request !== playbackRequestGeneration)
          return;
        if (stopped?.ok !== true) {
          report('未能停止完整播放，请稍后重试。');
          return;
        }
        fullPlaybackSelection = null;
        status.textContent = '已暂停';
        return;
      }
      // A preview already selected by this button resumes/pauses without
      // attempting full playback again on every click.
      if (canFull && (!previewSelection || previewSelection.url !== url)) {
        fullPlaybackSelection = null;
        disposePreviewAudio();
        player.hidden = false;
        title.textContent = item.title + (item.artist ? ' · ' + item.artist : '');
        status.textContent = '正在准备播放…';
        report('');
        let result;
        try {
          result = await window.desktopShell.playCatalogTrack({ type: 'songs', id: item.id, title: item.title });
        }
        catch {
          result = { ok: false };
        }
        if (request !== playbackRequestGeneration)
          return;
        if (result?.ok === true && result.playing === true) {
          fullPlaybackSelection = { id: item.id };
          status.textContent = '正在播放 · Apple Music';
          return;
        }
        // A different request still owns the official player. Never start a
        // preview over it, or turn explicit cancellation into fallback.
        if (['playback_busy', 'playback_superseded'].includes(result?.code)) {
          status.textContent = '播放请求已取消，请重试。';
          return;
        }
      }
      if (typeof window.desktopShell?.stopCatalogPlayback === 'function') {
        let stopped;
        try {
          stopped = await window.desktopShell.stopCatalogPlayback();
        }
        catch { /* Fail closed against overlapping audio. */ }
        if (request !== playbackRequestGeneration)
          return;
        if (stopped?.ok !== true) {
          report('未能停止完整播放，暂未切换到试听。');
          status.textContent = '播放暂时不可用';
          return;
        }
      }
      fullPlaybackSelection = null;
      playbackPending = false;
      syncPlaybackControls();
      if (!url) {
        report('完整播放不可用，此歌曲也没有可用试听片段。');
        status.textContent = '播放暂时不可用';
        return;
      }
      report('');
      await playPreview(item, request);
    }
    catch {
      if (request === playbackRequestGeneration)
        report('播放暂时不可用，请稍后重试。');
    }
    finally {
      if (request === playbackRequestGeneration) {
        playbackPending = false;
        syncPlaybackControls();
      }
    }
  }
  function createPlaybackButton(item) {
    const control = document.createElement('button');
    control.type = 'button';
    control.className = 'catalog-playback';
    control.textContent = '▶';
    control.setAttribute('aria-label', '播放 ' + item.title);
    control.setAttribute('aria-pressed', 'false');
    const url = item.type === 'songs' ? safeURL(item.previewUrl) : '';
    const canFull = canPlayFullTrack(item);
    control.dataset.playAvailable = String(!!url || canFull);
    control.dataset.playId = item.id;
    control.disabled = playbackPending || !url && !canFull;
    control.title = url || canFull ? '播放 · ' + item.title : '此歌曲没有可用播放资源';
    control.dataset.previewUrl = url;
    control.dataset.previewTitle = item.title;
    control.addEventListener('click', () => play(item));
    return control;
  }
  document.getElementById('playback-stop').addEventListener('click', stopPlayback);
  window.addEventListener('amdl:view-changed', () => { if (document.body.dataset.view !== 'browse')
    stopPlayback(); });
  window.addEventListener('amdl:browse-mode', () => { if (document.body.dataset.browseMode !== 'native')
    stopPlayback(); });
  window.addEventListener('pagehide', stopPlayback);
  window.catalogPlayback = { button: createPlaybackButton, sync: syncPlaybackControls, stop: stopPlayback };
})();

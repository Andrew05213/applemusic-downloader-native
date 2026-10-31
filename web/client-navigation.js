'use strict';

// Switch visibility only: forms and the live queue retain their DOM/state.
(() => {
  const titles = { browse: '浏览', tasks: '下载任务', settings: '设置与状态' };
  const groups = {
    browse: ['.browse-section'],
    tasks: ['.download-section', '.history-section', '.files-section'],
    settings: ['.status-section', '.settings-section', '.log-section'],
  };
  const sidebar = document.querySelector('.client-nav');
  const toggle = document.getElementById('sidebar-toggle');
  // A new layout gets its compact default even if the previous shell saved "expanded".
  const sidebarPreference = 'amdl.sidebarCollapsed.v2';
  function setCollapsed(collapsed) {
    sidebar.classList.toggle('collapsed', collapsed);
    document.body.classList.toggle('nav-collapsed', collapsed);
    toggle.setAttribute('aria-expanded', String(!collapsed));
    toggle.setAttribute('aria-label', collapsed ? '展开侧边栏' : '收起侧边栏');
    try { window.localStorage.setItem(sidebarPreference, collapsed ? '1' : '0'); } catch { /* Private storage may be unavailable. */ }
  }
  let saved = true;
  try { saved = window.localStorage.getItem(sidebarPreference) !== '0'; } catch { /* Default to compact navigation. */ }
  setCollapsed(saved);
  toggle.addEventListener('click', () => setCollapsed(!sidebar.classList.contains('collapsed')));
  function selectView() {
    const requested = window.location.hash.slice(1);
    const view = requested === 'files' ? 'tasks' : Object.hasOwn(titles, requested) ? requested : 'browse';
    document.body.dataset.view = view;
    document.querySelector('.hero h1').textContent = titles[view];
    document.querySelectorAll('[data-view]').forEach(link => {
      if (link.dataset.view === view) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    for (const [name, selectors] of Object.entries(groups)) {
      for (const selector of selectors) document.querySelector(selector).hidden = name !== view;
    }
    document.getElementById('twofa-section').hidden = view !== 'settings' || document.body.dataset.needsTwoFa !== 'true';
    window.dispatchEvent(new CustomEvent('amdl:view-changed', { detail: { view } }));
  }
  document.getElementById('download-readiness').addEventListener('click', () => {
    requestAnimationFrame(() => document.getElementById('settings-credentials')?.focus());
  });
  window.addEventListener('hashchange', selectView);
  selectView();
})();

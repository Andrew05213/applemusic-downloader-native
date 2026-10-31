'use strict';

// Runs only inside the sandboxed, isolated Apple Music WebContentsView. This
// module exposes no bridge or desktop token to the remote document. The page
// can remove the button; the trusted host toolbar remains the fallback.
const { ipcRenderer } = require('electron');

let generation = -1;
let button;
let host;
let queueable = false;
let scheduled = false;
let observedHeader;
let observedPlay;
let resizeObserver;

// Semantic classes verified on the public Apple Music detail page; never use
// track-row or mini-player Play controls as the page action's anchor.
function positionButton() {
  if (!button) return;
  button.hidden = true;
  host.style.display = 'none';
  if (!queueable) return;
  for (const header of document.querySelectorAll('.container-detail-header')) {
    const play = header.querySelector('.primary-actions__button--play button');
    if (!play) continue;
    const p = play.getBoundingClientRect();
    const h = header.getBoundingClientRect();
    if (p.width <= 0 || p.height <= 0 || p.top < 0 || p.bottom > window.innerHeight || h.width <= 0) continue;
    const top = p.top + p.height / 2 - 16;
    let left = Math.min(h.right, window.innerWidth - 8) - 32;
    const more = header.querySelector('.secondary-actions .contextual-menu__trigger');
    if (more) {
      const r = more.getBoundingClientRect();
      if (r.width > 0 && r.height > 0 && Math.abs((r.top + r.height / 2) - (p.top + p.height / 2)) < 24) {
        left = Math.min(left, r.left - 40);
      }
    }
    // Reserve room for Shuffle/other primary actions, not just Play itself.
    const actions = header.querySelector('.primary-actions');
    const a = actions?.getBoundingClientRect();
    if (left < Math.max(p.right, a?.right || 0) + 8 || left < h.left || top < 0 || top + 32 > window.innerHeight) continue;
    if (!host.isConnected) document.documentElement.append(host);
    host.style.left = `${Math.round(left)}px`;
    host.style.top = `${Math.round(top)}px`;
    host.style.display = 'block';
    button.hidden = false;
    if (resizeObserver && (observedHeader !== header || observedPlay !== play)) {
      resizeObserver.disconnect();
      resizeObserver.observe(header);
      resizeObserver.observe(play);
      observedHeader = header;
      observedPlay = play;
    }
    return;
  }
}

function schedulePosition() {
  if (scheduled) return;
  scheduled = true;
  requestAnimationFrame(() => { scheduled = false; positionButton(); });
}

function installButton() {
  if (button || !document.documentElement) return;
  host = document.createElement('div');
  host.setAttribute('aria-label', 'Apple Music Downloader');
  host.setAttribute('data-amdl-download-action', '');
  host.style.cssText = 'all:initial;position:fixed;width:32px;height:32px;z-index:2147483647;display:none';
  const root = host.attachShadow({ mode: 'closed' });
  const style = document.createElement('style');
  style.textContent = 'button{display:flex;align-items:center;justify-content:center;box-sizing:border-box;width:32px;height:32px;padding:6px;border:0;border-radius:6px;background:transparent;color:#fa2d48;cursor:pointer}button:hover{background:#fa2d481a}button:active{background:#fa2d482b}button:focus-visible{outline:2px solid currentColor;outline-offset:2px}button[hidden]{display:none}svg{width:20px;height:20px;pointer-events:none}';
  button = document.createElement('button');
  button.type = 'button';
  button.setAttribute('aria-label', '下载当前页面');
  button.title = '下载当前页面';
  button.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 4v14m-5-5 5 5 5-5"/></svg>';
  button.hidden = true;
  button.addEventListener('click', (event) => {
    // DOM-generated clicks are never user gestures. The main process also
    // validates sender, main frame, generation, visibility and current URL.
    positionButton();
    if (event.isTrusted && queueable && !button.hidden && Number.isSafeInteger(generation) && generation >= 0) {
      ipcRenderer.send('desktop:music-download-click', generation);
    }
  });
  root.append(style, button);
  document.documentElement.append(host);
  // Ignore our own positioning writes so the observer cannot create a frame loop.
  new MutationObserver((records) => {
    if (records.some((record) => record.target !== host)) schedulePosition();
  }).observe(document.documentElement, { childList: true, subtree: true, attributes: true, attributeFilter: ['class', 'style', 'hidden'] });
  resizeObserver = new ResizeObserver(schedulePosition);
  window.addEventListener('resize', schedulePosition, { passive: true });
  window.addEventListener('scroll', schedulePosition, { capture: true, passive: true });
  schedulePosition();
}

ipcRenderer.on('amdl:music-page-state', (_event, state) => {
  if (!state || !Number.isSafeInteger(state.generation) || state.generation < generation || state.generation < 0 || typeof state.queueable !== 'boolean') return;
  generation = state.generation;
  queueable = state.queueable;
  installButton();
  positionButton();
});

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', installButton, { once: true });
else installButton();

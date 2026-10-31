'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.resolve(__dirname, '../../src/preload/apple-music-preload.js'), 'utf8');

function fixture() {
  const sent = [], frames = [], observers = [], listeners = {};
  let pageState, button, host;
  const rect = (left, top, width, height) => ({ left, top, width, height, right: left + width, bottom: top + height });
  const play = { getBoundingClientRect: () => rect(400, 240, 130, 32) };
  const menu = { getBoundingClientRect: () => rect(1168, 240, 32, 32) };
  const header = { getBoundingClientRect: () => rect(300, 32, 900, 280), querySelector: (s) => s.includes('--play') ? play : s.includes('secondary-actions') ? menu : null };
  let headers = [header];
  const document = {
    readyState: 'complete', documentElement: { append(node) { node.isConnected = true; } },
    querySelectorAll: () => headers,
    createElement(tag) {
      const node = { hidden: false, style: {}, isConnected: false, attributes: {},
        setAttribute(k, v) { this.attributes[k] = v; }, append() {},
        addEventListener(name, fn) { this[name] = fn; },
        attachShadow(options) { assert.equal(options.mode, 'closed'); return { append() {} }; },
      };
      if (tag === 'button') button = node;
      if (tag === 'div') host = node;
      return node;
    },
  };
  class Observer { constructor(fn) { observers.push(fn); } observe() {} disconnect() {} }
  vm.runInNewContext(source, { document, window: { innerWidth: 1280, innerHeight: 800,
    addEventListener(name, fn) { listeners[name] = fn; } },
    MutationObserver: Observer, ResizeObserver: Observer,
    requestAnimationFrame(fn) { frames.push(fn); return frames.length; },
    require(name) { assert.equal(name, 'electron'); return { ipcRenderer: {
      on(channel, fn) { assert.equal(channel, 'amdl:music-page-state'); pageState = fn; },
      send(channel, generation) { sent.push({ channel, generation }); },
    } }; },
  });
  const flush = () => { let n = 0; while (frames.length) { assert.ok(++n < 10); frames.shift()(); } };
  const state = (generation, queueable) => { pageState({}, { generation, queueable }); flush(); };
  flush();
  return { state, flush, sent, play, menu, header, listeners, observers,
    get button() { return button; }, get host() { return host; },
    removeHeader() { headers = []; observers[0]([{ target: header }]); flush(); },
  };
}

test('injected arrow aligns with Play and leaves room for the right-hand overflow menu', () => {
  const f = fixture(); f.state(4, true);
  assert.equal(f.host.style.left, '1128px');
  assert.equal(f.host.style.top, '240px');
  assert.equal(f.button.hidden, false);
  assert.equal(f.button.attributes['aria-label'], '下载当前页面');
});

test('remote preload sends only genuine visible clicks with the current generation', () => {
  const f = fixture(); f.state(4, true);
  f.button.click({ isTrusted: false }); assert.deepEqual(f.sent, []);
  f.button.click({ isTrusted: true });
  assert.deepEqual(f.sent, [{ channel: 'desktop:music-download-click', generation: 4 }]);
  f.state(5, false); f.button.click({ isTrusted: true }); assert.equal(f.sent.length, 1);
  f.state(6, true); f.removeHeader(); f.button.click({ isTrusted: true }); assert.equal(f.sent.length, 1);
});

test('missing, offscreen and overlapping Play regions never produce a floating fallback', () => {
  const f = fixture(); f.state(4, true);
  f.play.getBoundingClientRect = () => ({ left: 400, right: 530, top: -40, bottom: -8, width: 130, height: 32 });
  f.listeners.scroll(); f.flush(); assert.equal(f.button.hidden, true);
  f.play.getBoundingClientRect = () => ({ left: 1100, right: 1190, top: 240, bottom: 272, width: 90, height: 32 });
  f.listeners.resize(); f.flush(); assert.equal(f.button.hidden, true);
  f.removeHeader(); assert.equal(f.button.hidden, true);
});

test('scroll/resize updates and malformed or older state cannot reactivate a stale action', () => {
  const f = fixture(); f.state(8, true);
  f.play.getBoundingClientRect = () => ({ left: 400, right: 530, top: 300, bottom: 332, width: 130, height: 32 });
  f.listeners.resize(); f.flush(); assert.equal(f.host.style.top, '300px');
  f.state(9, false); f.state(8, true); assert.equal(f.button.hidden, true);
  f.state(-1, true); assert.equal(f.button.hidden, true);
});

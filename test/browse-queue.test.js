'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const jobData = require('../web/job-data');

function browseQueue({ jobs = [], ready = true, receipt = { jobs: [{ id: 'job-new', status: 'queued' }] }, submit } = {}) {
  const elements = new Map();
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, { value: '', textContent: '', addEventListener() {} });
    return elements.get(id);
  };
  element('default-quality').value = 'aac-lc';
  element('default-existing-policy').value = 'skip';
  element('browse-web-status').textContent = 'Unrelated web browser status';
  const accepted = [];
  const calls = [];
  const window = {
    addEventListener() {},
    amdlRenderer: {
      api: async (url, options) => { calls.push({ url, options }); return submit ? submit() : receipt; },
      jobs: { values: () => jobData.jobs(jobs).values(), acceptCreated: (job) => accepted.push(job) },
      isActive: (job) => ['running', 'queued', 'cancelling'].includes(job.status),
      loadJobs: async () => {}, downloadSubmissionReady: () => ready,
      queueNeedsRecovery: () => false, qualityAvailable: () => true, capabilityMessage: () => '',
    },
  };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, '../web/browse.js'), 'utf8'), {
    window, URL, requestAnimationFrame() {},
    document: { getElementById: element, body: { dataset: { view: 'browse', browseMode: 'native' } } },
  }, { filename: 'browse.js' });
  return { queue: window.amdlBrowse.queueCatalog, elements, calls, accepted };
}

test('catalog submission returns a structured receipt without reading or changing browser status', async () => {
  const page = browseQueue();
  const result = await page.queue('https://music.apple.com/us/song/123');
  assert.equal(result.status, 'queued');
  assert.equal(result.message, '当前页面已加入下载队列。');
  assert.equal(page.accepted[0].id, 'job-new');
  assert.equal(page.elements.get('browse-web-status').textContent, 'Unrelated web browser status');
  assert.equal(page.calls[0].url, '/api/v1/jobs');
});

test('legacy list aliases still suppress duplicate catalog queue requests', async () => {
  const page = browseQueue({ jobs: [{ jobId: 'job-1', sourceUrl: 'https://music.apple.com/us/song/123', existing_policy: 'skip', quality: 'aac-lc', status: 'queued' }] });
  assert.equal((await page.queue('https://music.apple.com/us/song/123')).status, 'duplicate');
  assert.equal(page.calls.length, 0);
});

test('unavailable and invalid catalog submissions return explicit outcomes without calling the API', async () => {
  const unavailable = browseQueue({ ready: false });
  assert.equal((await unavailable.queue('https://music.apple.com/us/song/123')).status, 'unavailable');
  assert.equal(unavailable.calls.length, 0);
  const invalid = browseQueue();
  assert.equal((await invalid.queue('https://example.com/song/123')).status, 'invalid');
  assert.equal(invalid.calls.length, 0);
});

test('compatibility normalization does not weaken the new-job receipt identity check', async () => {
  const page = browseQueue({ receipt: { jobs: [{ jobId: 'legacy-only' }] } });
  const result = await page.queue('https://music.apple.com/us/song/123');
  assert.equal(result.status, 'failed');
  assert.match(result.message, /队列回执不完整/);
  assert.equal(page.accepted.length, 0);
});

test('concurrent catalog submissions return busy without stealing the first result', async () => {
  let release;
  const page = browseQueue({ submit: () => new Promise((resolve) => { release = resolve; }) });
  const first = page.queue('https://music.apple.com/us/song/123');
  assert.equal((await page.queue('https://music.apple.com/us/song/456')).status, 'busy');
  release({ jobs: [{ id: 'job-new' }] });
  assert.equal((await first).status, 'queued');
  assert.equal(page.calls.length, 1);
});

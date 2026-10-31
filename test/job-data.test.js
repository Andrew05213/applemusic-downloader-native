'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const data = require('../web/job-data');

test('legacy HTTP records enter the renderer with canonical job, item and file fields', () => {
  const original = {
    jobId: 'job-1', sourceUrl: 'https://music.apple.com/us/song/123',
    existing_policy: 'overwrite', error_code: 'metadata_transient',
    tracks: [{ id: 'item-1', source_id: '123', title: 'Song', error_code: 'media_failed' }],
  };
  assert.deepEqual(data.jobs([original]), [{
    id: 'job-1', url: original.sourceUrl, existingPolicy: 'overwrite', errorCode: 'metadata_transient',
    items: [{ id: 'item-1', sourceId: '123', name: 'Song', errorCode: 'media_failed' }],
  }]);
  assert.deepEqual(data.files({ files: [{ fileId: 'file-1', job_id: 'job-1', filename: 'Song.m4a', managed: false }] }), [
    { id: 'file-1', jobId: 'job-1', name: 'Song.m4a', unmanaged: true },
  ]);
  assert.equal(original.tracks[0].title, 'Song', 'normalization leaves caller-owned response objects untouched');
});

test('partial SSE updates preserve omissions while an explicit empty item list clears detail', () => {
  const snapshot = data.job({ id: 'job-1', items: [{ id: 'item-1', name: 'Song' }] });
  const progress = data.job({ succeeded: 1 });
  assert.equal(Object.hasOwn(progress, 'items'), false);
  assert.equal(Object.hasOwn(data.item({ id: 'item-1', status: 'succeeded' }), 'name'), false);
  assert.equal(Object.hasOwn(data.file({ fileId: 'file-1', size: 100 }), 'jobId'), false);
  assert.deepEqual({ ...snapshot, ...progress }.items, snapshot.items);
  assert.deepEqual({ ...snapshot, ...data.job({ tracks: [] }) }.items, []);
  assert.deepEqual({ ...snapshot, ...data.job({ items: [] }) }.items, []);
});

test('canonical responses take precedence and accepted display aliases still fall back', () => {
  assert.deepEqual(data.job({ id: 'canonical', jobId: 'legacy', items: [], tracks: [{ title: 'old' }] }), {
    id: 'canonical', items: [],
  });
  assert.deepEqual(data.file({ id: 'file-1', name: '', filename: 'Fallback.m4a' }), {
    id: 'file-1', name: 'Fallback.m4a',
  });
});

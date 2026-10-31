'use strict';

function validMediaUserToken(value) {
  return typeof value === 'string' && value.length >= 51 && value.length <= 16384
    && /^[\x21-\x7e]+$/.test(value);
}

function normalizePlaybackTrack(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)
      || Object.keys(value).some(key => !['id', 'type', 'title'].includes(key))
      || value.type !== 'songs' || typeof value.id !== 'string'
      || !/^[1-9]\d{0,19}$/.test(value.id)) {
    throw new Error('invalid_playback_item');
  }
  return { type: 'songs', id: value.id, title: typeof value.title === 'string' ? value.title.slice(0, 512) : '' };
}

async function preparePlaybackDRM(components) {
  if (typeof components?.whenReady !== 'function' || !components.WIDEVINE_CDM_ID) return false;
  let timer;
  try {
    await Promise.race([
      components.whenReady([components.WIDEVINE_CDM_ID]),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('timeout')), 30_000); }),
    ]);
    return true;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

module.exports = { validMediaUserToken, normalizePlaybackTrack, preparePlaybackDRM };

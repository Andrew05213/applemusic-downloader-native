'use strict';

// Normalize external HTTP/SSE records once. The renderer stores only these
// canonical fields; omitted fields stay omitted so partial updates do not clear
// existing items, names or ownership. Explicit empty arrays still replace items.
(() => {
  function record(value) {
    return value && typeof value === 'object' && !Array.isArray(value) ? { ...value } : {};
  }

  function alias(value, canonical, alternatives) {
    if (!value[canonical]) {
      const source = alternatives.find((key) => value[key])
        ?? alternatives.find((key) => !Object.hasOwn(value, canonical) && Object.hasOwn(value, key));
      if (source !== undefined) value[canonical] = value[source];
    }
    for (const key of alternatives) delete value[key];
  }

  function item(value) {
    const result = record(value);
    alias(result, 'jobId', ['job_id']);
    alias(result, 'sourceId', ['source_id']);
    alias(result, 'name', ['title']);
    alias(result, 'errorCode', ['error_code']);
    return result;
  }

  function job(value) {
    const result = record(value);
    alias(result, 'id', ['jobId']);
    alias(result, 'url', ['sourceUrl']);
    alias(result, 'existingPolicy', ['existing_policy']);
    alias(result, 'errorCode', ['error_code']);
    const items = Array.isArray(result.items) ? result.items : result.tracks;
    delete result.tracks;
    if (Array.isArray(items)) result.items = items.map(item);
    else delete result.items;
    return result;
  }

  function file(value) {
    const result = record(value);
    alias(result, 'id', ['fileId']);
    alias(result, 'jobId', ['job_id']);
    alias(result, 'name', ['filename', 'relativePath']);
    if (!Object.hasOwn(result, 'unmanaged') && result.managed === false) result.unmanaged = true;
    delete result.managed;
    return result;
  }

  function list(payload, key, normalize) {
    const values = Array.isArray(payload) ? payload : payload?.[key];
    return Array.isArray(values) ? values.map(normalize) : [];
  }

  const data = Object.freeze({
    job, item, file,
    jobs: (payload) => list(payload, 'jobs', job),
    files: (payload) => list(payload, 'files', file),
  });
  if (typeof module !== 'undefined' && module.exports) module.exports = data;
  else window.amdlJobData = data;
})();

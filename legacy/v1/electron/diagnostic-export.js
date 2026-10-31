'use strict';

// Export only bounded text already sanitized by the runtime/UI. Apply a second
// conservative pass without reading a credential store, configuration, or any
// browser session. Never accept a renderer-supplied file path.
const MAX_INPUT_CHARACTERS = 1024 * 1024;
const MAX_SECTION_CHARACTERS = 128 * 1024;

function sanitizeDiagnosticLine(line) {
    let text = line.replace(/\x1b\[[0-9;]*[A-Za-z]/g, '').replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '');
    if (/(?:authorization|media[-_]user[-_]token|apple[-_]password|password|cookie|set-cookie|two[-_ ]?factor[-_ ]?(?:code)?|2fa[-_ ]?code|token|secret)\s*["']?\s*[:=]/i.test(text)) return '[REDACTED sensitive log line]';
    text = text.replace(/https?:\/\/[^\s<>"']+/gi, '[REDACTED URL]')
      .replace(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi, '[REDACTED email]')
      .replace(/[A-Z]:[\\/][^\r\n]*/gi, '[REDACTED local path]')
      .replace(/\/(?:home|Users|mnt)\/[^\s"']+/g, '[REDACTED local path]')
      .replace(/\b[A-Za-z0-9_+\/=-]{80,}\b/g, '[REDACTED long value]');
    return text.length > 2048 ? `${text.slice(0, 2048)} [truncated]` : text;
}

function boundedDiagnosticLines(lines, maxLines, alreadyTruncated = false) {
  let truncated = alreadyTruncated || lines.length > maxLines;
  const selected = lines.slice(-maxLines).map(sanitizeDiagnosticLine);
  const tail = [];
  let size = 0;
  for (let i = selected.length - 1; i >= 0; i--) {
    size += selected[i].length + 1;
    if (size > MAX_SECTION_CHARACTERS) { truncated = true; break; }
    tail.push(selected[i]);
  }
  return [truncated ? '[truncated: older diagnostic records omitted]' : '', ...tail.reverse()].filter(Boolean).join('\n');
}

// Keep structured status apart from actual log records. A compact control
// response is one transport line, not one log line. Flatten only known roots;
// do not export arbitrary extra properties from a control response.
function formatRuntimeDiagnostics(value) {
  if (typeof value !== 'string') return '';
  const inputTruncated = value.length > MAX_INPUT_CHARACTERS;
  let input = value.slice(-MAX_INPUT_CHARACTERS);
  if (inputTruncated) {
    // Drop a partial first transport record instead of parsing/redacting a
    // fragment whose field name or sensitive-data prefix has been cut off.
    const boundary = input.indexOf('\n');
    input = boundary < 0 ? '' : input.slice(boundary + 1);
  }
  const details = [];
  const logs = [];
  function flatten(value, path, depth = 0) {
    if (details.length >= 100 || depth > 6) return;
    if (value === null || ['string', 'number', 'boolean'].includes(typeof value)) {
      // JSON escaping keeps embedded newlines from forging extra fields.
      details.push(`${path}=${typeof value === 'string' ? JSON.stringify(value).slice(1, -1) : String(value)}`);
    } else if (value && !Array.isArray(value) && typeof value === 'object') {
      for (const [key, child] of Object.entries(value).slice(0, 64)) {
        if (/^[A-Za-z][A-Za-z0-9_]{0,63}$/.test(key)) flatten(child, `${path}.${key}`, depth + 1);
      }
    }
  }
  for (const line of input.split(/\r?\n/)) {
    if (!line.trim()) continue;
    if (line.trimStart().startsWith('{')) {
      let record;
      try { record = JSON.parse(line); } catch {
        logs.push('[structured diagnostic record unavailable: invalid JSON]');
        continue;
      }
      if (!record || Array.isArray(record)) continue;
      for (const key of ['ok', 'status', 'error', 'buildIdentity']) {
        if (Object.hasOwn(record, key)) flatten(record[key], key);
      }
      if (Array.isArray(record.logs)) {
        for (const entry of record.logs) {
          if (typeof entry === 'string') logs.push(...entry.split(/\r?\n/));
        }
      }
    } else logs.push(line);
  }
  return [
    boundedDiagnosticLines(details, 100),
    boundedDiagnosticLines(logs, 200, inputTruncated),
  ].filter(Boolean).join('\n');
}

function sanitizeDiagnosticText(value, maxLines) {
  if (typeof value !== 'string') return '';
  return boundedDiagnosticLines(value.split(/\r?\n/), maxLines);
}

function formatDiagnosticReport({ productVersion, runtime, uiLogs, now = new Date() } = {}) {
  const version = typeof productVersion === 'string' && /^1\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(productVersion) ? productVersion : 'unknown';
  const lines = Array.isArray(uiLogs) ? uiLogs.slice(-100).filter((line) => typeof line === 'string').map((line) => line.slice(0, MAX_INPUT_CHARACTERS)) : [];
  return [
    'Apple Music Downloader — sanitized diagnostic report',
    `product_version=${version}`, `exported_at=${now.toISOString()}`,
    'Login completion is startup evidence only; it does not verify a current session or CKC/license availability.',
    '', '=== Runtime (status + last 200 log records) ===', formatRuntimeDiagnostics(runtime) || 'Runtime diagnostics unavailable.',
    '', '=== UI (last 100 messages) ===', sanitizeDiagnosticText(lines.join('\n'), 100) || 'No UI messages.', '',
  ].join('\n');
}

async function exportDiagnosticReport({ dialog, owner, collectRuntime, writeFile, productVersion, uiLogs, now = new Date(), defaultPath } = {}) {
  try {
    const options = {title: '导出脱敏日志', defaultPath: defaultPath || `AMDL-diagnostics-${now.toISOString().replace(/[:.]/g, '-')}.txt`, filters: [{name:'文本日志',extensions:['txt']} ]};
    const choice = owner && !owner.isDestroyed() ? await dialog.showSaveDialog(owner, options) : await dialog.showSaveDialog(options);
    if (choice.canceled || !choice.filePath) return {ok:true,cancelled:true};
    let runtime = '';
    try { runtime = await collectRuntime(); } catch { /* Export UI logs even when runtime diagnostics are unavailable. */ }
    const report = formatDiagnosticReport({productVersion,runtime,uiLogs,now});
    await writeFile(choice.filePath, report, {encoding:'utf8',mode:0o600});
    return {ok:true,cancelled:false};
  } catch {
    return {ok:false,error:{code:'diagnostic_export_failed',message:'日志导出未完成。',action:'请选择可写入的保存位置后重试。'}};
  }
}

module.exports = {formatDiagnosticReport, exportDiagnosticReport};

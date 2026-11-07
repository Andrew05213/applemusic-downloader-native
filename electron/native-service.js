'use strict';
const { spawn } = require('node:child_process');
const crypto = require('node:crypto');
const { EventEmitter } = require('node:events');
const { VERSION } = require('./native-version');

function allowedAPI(method, target) {
  if (typeof target !== 'string' || target.length > 4096 || !target.startsWith('/api/') || /[\r\n\\#]/.test(target)) return false;
  const path = target.split('?')[0];
  if (method === 'GET') return /^\/api\/(?:native\/(?:state|interaction|jobs\/[a-f0-9-]+\/location)|v1\/(?:catalog\/(?:session|search|discover|home|detail|library|library-detail|recommendation|artist-albums)|capabilities|settings\/download(?:-concurrency)?|jobs(?:\/[a-f0-9-]+(?:\/files)?)?|files))$/.test(path);
  if (method === 'POST') return /^\/api\/(?:native\/(?:login|interaction|two-factor|cancel-login|recheck|retry|playback)|v1\/(?:jobs(?:\/[a-f0-9-]+\/(?:retry|cancel))?|browser-download\/resolve))$/.test(path);
  if (method === 'PUT') return path==='/api/native/download-directory'||/^\/api\/v1\/settings\/download(?:-concurrency)?$/.test(path);
  if (method === 'DELETE') return path === '/api/native/playback' || /^\/api\/v1\/(?:jobs\/[a-f0-9-]+(?:\/record)?|settings\/download(?:-concurrency)?)$/.test(path);
  return false;
}
class NativeService extends EventEmitter {
  constructor({ executable, manifest, dataDirectory, outputDirectory, guestPort, toolDirectory, spawnChild = spawn }) {
    super(); this.token = crypto.randomBytes(32).toString('hex'); this.origin = ''; this.closed = false; this.failed = false; this.buffer = ''; this.requests = new Set(); this.catalog = null;this.playbackRequests=new Map();
    this.child = spawnChild(executable, ['__native-desktop', '--manifest', manifest, '--data-dir', dataDirectory, '--output-dir', outputDirectory, '--guest-port', String(guestPort)], {
      shell: false, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
      env: { SystemRoot: process.env.SystemRoot, WINDIR: process.env.WINDIR, TEMP: process.env.TEMP, TMP: process.env.TMP, PATH: toolDirectory || '' },
    });
    this.child.stderr.resume(); this.child.stdin.on('error', () => {});
    this.completion = new Promise((resolve) => this.child.once('close', (code) => {
      this.closed = true; for (const request of this.requests) request.abort(); this.requests.clear();this.cancelPlaybackChunks(); this.emit('closed'); resolve(code);
    }));
    this.ready = new Promise((resolve, reject) => {
      let settled = false;
      const fail = code => {
        if (settled) return;
        settled = true; this.failed = true; this.buffer = ''; clearTimeout(timer);
        reject(new Error(code)); this.child.stdin.end();
      };
      const timer = setTimeout(() => fail('native_service_timeout'), 30000);
      this.child.once('error', () => fail('native_service_unavailable'));
      this.child.once('close', () => fail('native_service_closed'));
      this.child.stdout.setEncoding('utf8');
      this.child.stdout.on('data', (chunk) => {
        if (settled) return;
        this.buffer += chunk;
        if (this.buffer.length > 131072) { fail('native_service_protocol_invalid'); return; }
        let newline;
        while ((newline = this.buffer.indexOf('\n')) !== -1) {
          const line = this.buffer.slice(0, newline); this.buffer = this.buffer.slice(newline + 1);
          if (this.origin) continue;
          let receipt; try { receipt = JSON.parse(line); } catch { continue; }
          if (receipt.nativeServer === false) { fail('native_service_unavailable'); return; }
          if (receipt.nativeServer === true && receipt.version === VERSION && Number.isInteger(receipt.port) && receipt.port > 0 && receipt.port <= 65535) {
            settled = true; this.origin = 'http://127.0.0.1:' + receipt.port; this.buffer = ''; clearTimeout(timer); resolve(this.origin); return;
          }
        }
      });
    });
    this.child.stdin.write(JSON.stringify({ apiAccessToken: this.token }) + '\n');
  }
  mediaHeaders(details, ownerID) {
    if (details.webContentsId !== ownerID || !this.origin || !details.url.startsWith(this.origin + '/api/native/playback/')) return details.requestHeaders;
    return { ...details.requestHeaders, Authorization: 'Bearer ' + this.token };
  }
  async api(target, options = {}) {
    const method = options.method || 'GET';
    if (!allowedAPI(method, target) || this.closed || this.failed) throw new Error('native_api_unavailable');
    await this.ready;
    if(this.closed)throw new Error('native_api_unavailable');
    if(target==='/api/native/playback'){this.playbackPrepare?.abort();this.cancelPlaybackChunks();}
    const abort = new AbortController(); this.requests.add(abort);
    if(target==='/api/native/playback'&&method==='POST')this.playbackPrepare=abort;
    // A region change also refreshes the private sidebar library. It must not
    // cancel the independent public discovery request (or vice versa).
    const catalogSlot = method !== 'GET' || !target.startsWith('/api/v1/catalog/') ? null
      : target.split('?')[0] === '/api/v1/catalog/discover' ? 'discoveryCatalog' : 'catalog';
    if (catalogSlot) { this[catalogSlot]?.abort(); this[catalogSlot] = abort; }
    const timer = setTimeout(() => abort.abort(), target === '/api/native/playback' && method === 'POST' ? 60000 : 90000);
    try {
      const response = await fetch(this.origin + target, { method, redirect: 'error', headers: { Authorization: 'Bearer ' + this.token, 'Content-Type': 'application/json' }, body: options.body === undefined ? undefined : JSON.stringify(options.body), signal: abort.signal });
      if (response.status === 204) return { ok: true };
      const raw = await response.text(); if (raw.length > 4 * 1024 * 1024) throw new Error('native_api_response_invalid');
      let result; try { result = JSON.parse(raw); } catch { throw new Error('native_api_response_invalid'); }
      if (!response.ok) throw new Error(/^[a-z_]+$/.test(result?.error?.code) ? result.error.code : 'native_api_failed');
      if (target === '/api/native/playback' && method === 'POST' && /^\/api\/native\/playback\/[a-f0-9]{32}$/.test(result.mediaPath)) result.mediaURL = this.origin + result.mediaPath;
      return result;
    } finally { clearTimeout(timer); this.requests.delete(abort); if (catalogSlot && this[catalogSlot] === abort) this[catalogSlot] = null;if(this.playbackPrepare===abort)this.playbackPrepare=null; }
  }
  cancelPlaybackChunks(base) {for(const [abort,target] of this.playbackRequests)if(!base||target.startsWith(base+'/'))abort.abort();}
  async playbackChunk(target) {
    if(this.closed||typeof target!=='string'||!/^\/api\/native\/playback\/[a-f0-9]{32}\/(?:init|segment\/[0-9]{1,4})$/.test(target))throw Error('native_playback_path_invalid');
    await this.ready;if(this.closed||this.playbackRequests.size>=4)throw Error('native_playback_unavailable');const abort=new AbortController();this.playbackRequests.set(abort,target);const timer=setTimeout(()=>abort.abort(),45000);
    try {
      const response=await fetch(this.origin+target,{redirect:'error',headers:{Authorization:'Bearer '+this.token},signal:abort.signal});
      if(!response.ok||response.headers.get('content-type')!=='audio/mp4'||Number(response.headers.get('content-length'))>8*1024*1024)throw Error('native_playback_segment_failed');
      const reader=response.body.getReader(),parts=[];let total=0;
      for(;;){const {value,done}=await reader.read();if(done)break;total+=value.byteLength;if(total>8*1024*1024){abort.abort();throw Error('native_playback_segment_failed');}parts.push(value);}
      if(!total)throw Error('native_playback_segment_failed');return new Uint8Array(Buffer.concat(parts,total));
    } catch {throw Error(abort.signal.aborted?'native_playback_cancelled':'native_playback_segment_failed');}
    finally {clearTimeout(timer);this.playbackRequests.delete(abort);}
  }
  async close() {
    if (this.closed) return;
    for (const request of this.requests) request.abort();
    this.cancelPlaybackChunks();
    this.child.stdin.end(); // EOF cancels jobs, closes files, flushes DB and stops owned QEMU.
    const fallback = setTimeout(() => { if (!this.closed) this.child.kill(); }, 60000);
    try { await this.completion; } finally { clearTimeout(fallback); }
  }
}
module.exports = { NativeService, allowedAPI };

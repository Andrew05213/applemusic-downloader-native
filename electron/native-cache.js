'use strict';
// Cache keys: catalog:{normalized route/query}, artwork:{validated CDN URL}.
// No jobs, credentials, session checks or media/license responses are cached.
// Catalog stays cache-first until refresh/account change; LRU enforces the
// user-selected disk budget. Only files owned by this cache are ever removed.
const fs = require('node:fs/promises');
const path = require('node:path');
const crypto = require('node:crypto');
const MB = 1024 * 1024;
function catalogTarget(target) {
  if (typeof target !== 'string' || target.length > 4096) return null;
  const u = new URL(target, 'http://cache');
  if (u.origin !== 'http://cache' || !/^\/api\/v1\/catalog\/(?:home|discover|search|detail|library|library-detail|recommendation|artist-albums)$/.test(u.pathname)) return null;
  u.searchParams.sort();
  return u.pathname + u.search;
}
function validArtwork(target) {
  try { const u=new URL(target);return target.length<=4096&&u.protocol==='https:'&&!u.username&&!u.password&&!u.port&&!u.hash&&u.hostname.endsWith('.mzstatic.com')?u.href:null; } catch { return null; }
}
class NativeCache {
  constructor(directory, { defaultLimitMB = 500 } = {}) {
    this.directory=directory;this.limitMB=defaultLimitMB;this.entries=new Map();this.inflight=new Map();this.serial=Promise.resolve();this.generation=0;this.tick=Date.now();this.hits=0;this.misses=0;this.available=true;
    this.ready=this.initialize().catch(()=>{this.available=false;this.entries.clear();});
  }
  async initialize() {
    await fs.mkdir(this.directory,{recursive:true});
    try{const config=JSON.parse(await fs.readFile(path.join(this.directory,'settings.json'),'utf8'));if(Number.isInteger(config.limitMB)&&config.limitMB>=0&&config.limitMB<=10240)this.limitMB=config.limitMB;}catch{}
    try{const index=JSON.parse(await fs.readFile(path.join(this.directory,'index.json'),'utf8'));if(index.version===1&&Array.isArray(index.entries))for(const entry of index.entries.slice(0,4096).sort((a,b)=>(a.accessed||0)-(b.accessed||0))){if(!/^[a-f0-9]{64}$/.test(entry.id)||!['catalog','personal','artwork'].includes(entry.kind)||!Number.isSafeInteger(entry.size)||entry.size<0)continue;try{const stat=await fs.lstat(this.file(entry.id));if(stat.isFile()&&!stat.isSymbolicLink()&&stat.size===entry.size)this.entries.set(entry.id,{...entry,accessed:++this.tick});}catch{}}}catch{}
    for(const name of await fs.readdir(this.directory)){if(/^[a-f0-9]{64}\.bin$/.test(name)&&!this.entries.has(name.slice(0,64)))await fs.unlink(path.join(this.directory,name)).catch(()=>{});if(/^cache-[a-f0-9-]+\.tmp$/.test(name))await fs.unlink(path.join(this.directory,name)).catch(()=>{});}
    await this.evict();await this.index();
  }
  file(id){return path.join(this.directory,id+'.bin');}
  id(key){return crypto.createHash('sha256').update(key).digest('hex');}
  run(action){const result=this.serial.then(action);this.serial=result.catch(()=>{});return result;}
  size(){return [...this.entries.values()].reduce((sum,e)=>sum+e.size,0);}
  async atomic(name,data){const temporary=path.join(this.directory,'cache-'+crypto.randomUUID()+'.tmp');try{await fs.writeFile(temporary,data);await fs.rename(temporary,path.join(this.directory,name));}finally{await fs.unlink(temporary).catch(()=>{});}}
  async index(){await this.atomic('index.json',JSON.stringify({version:1,entries:[...this.entries.values()]}));}
  async evict(){while(this.size()>this.limitMB*MB||this.entries.size>2048){const first=[...this.entries.values()].sort((a,b)=>a.accessed-b.accessed)[0];if(!first)break;await fs.unlink(this.file(first.id)).catch(error=>{if(error.code!=='ENOENT')throw error;});this.entries.delete(first.id);}}
  async getOrLoad(key,kind,loader){
    await this.ready;const id=this.id(key);
    if(this.inflight.has(id))return this.inflight.get(id);
    const generation=this.generation;
    const pending=(async()=>{
      if(this.available&&this.limitMB>0){const hit=await this.run(async()=>{const entry=this.entries.get(id);if(!entry)return null;try{const data=await fs.readFile(this.file(id));if(data.length!==entry.size)throw new Error('cache_size');entry.accessed=++this.tick;this.hits++;return {data,mime:entry.mime};}catch{this.entries.delete(id);await fs.unlink(this.file(id)).catch(()=>{});return null;}});if(hit)return hit;}
      this.misses++;const value=await loader();if(!Buffer.isBuffer(value.data))throw new Error('native_cache_data_invalid');
      if(this.available&&generation===this.generation&&value.data.length<=this.limitMB*MB&&this.limitMB>0){await this.run(async()=>{if(generation!==this.generation)return;await this.atomic(id+'.bin',value.data);this.entries.set(id,{id,kind,size:value.data.length,mime:value.mime,accessed:++this.tick});await this.evict();await this.index();}).catch(()=>{});}
      return value;
    })();this.inflight.set(id,pending);try{return await pending;}finally{if(this.inflight.get(id)===pending)this.inflight.delete(id);}
  }
  async json(target,loader){const normalized=catalogTarget(target);if(!normalized)return loader();const personal=/\/catalog\/(?:home|library|library-detail|recommendation)(?:\?|$)/.test(normalized);
    // Search v2 includes artists and ranked best results. Old permanent search
    // entries must not hide the new response contract after an app upgrade.
    const query=new URL(normalized,'http://cache');
    const artistHome=query.pathname==='/api/v1/catalog/detail'&&query.searchParams.get('type')==='artists';
    const artistCredits=query.pathname==='/api/v1/catalog/detail'&&['albums','songs'].includes(query.searchParams.get('type'));
    const key=(artistHome?'catalog-artist-home-v2:':artistCredits?'catalog-detail-artists-v2:':normalized.startsWith('/api/v1/catalog/search?')?'catalog-search-v2:':'catalog:')+normalized;
    const load=async()=>({data:Buffer.from(JSON.stringify(await loader())),mime:'application/json'});
    const value=await this.getOrLoad(key,personal?'personal':'catalog',load);
    try{return JSON.parse(value.data.toString('utf8'));}catch{await this.run(async()=>{this.entries.delete(this.id(key));await fs.unlink(this.file(this.id(key))).catch(()=>{});});return JSON.parse((await this.getOrLoad(key,personal?'personal':'catalog',load)).data.toString('utf8'));}
  }
  async clear(kind='all'){++this.generation;this.inflight.clear();await this.ready;if(!this.available)return this.info();await this.run(async()=>{for(const e of [...this.entries.values()])if(kind==='all'||kind==='catalog'&&e.kind!=='artwork'||kind===e.kind){await fs.unlink(this.file(e.id)).catch(error=>{if(error.code!=='ENOENT')throw error;});this.entries.delete(e.id);}await this.index();});return this.info();}
  async setLimit(limitMB){if(!Number.isInteger(limitMB)||limitMB<0||limitMB>10240)throw new Error('native_cache_limit_invalid');await this.ready;if(!this.available)throw new Error('native_cache_unavailable');++this.generation;this.inflight.clear();await this.run(async()=>{this.limitMB=limitMB;await this.evict();await this.atomic('settings.json',JSON.stringify({limitMB}));await this.index();});return this.info();}
  async info(){await this.ready;return {available:this.available,limitMB:this.limitMB,usedBytes:this.size(),entries:this.entries.size,hits:this.hits,misses:this.misses};}
}
async function fetchArtwork(target,fetcher){const url=validArtwork(target);if(!url)throw new Error('native_artwork_rejected');const response=await fetcher(url,{credentials:'omit',cache:'no-store',signal:AbortSignal.timeout(15000)});if(!response.ok)throw new Error('native_artwork_unavailable');const mime=(response.headers.get('content-type')||'').split(';')[0];if(!/^image\/(?:jpeg|png|webp|gif|avif|svg\+xml)$/.test(mime))throw new Error('native_artwork_invalid');const reader=response.body.getReader();const chunks=[];let size=0;try{for(;;){const {done,value}=await reader.read();if(done)break;size+=value.length;if(size>16*MB)throw new Error('native_artwork_too_large');chunks.push(Buffer.from(value));}}finally{await reader.cancel().catch(()=>{});}if(!size)throw new Error('native_artwork_empty');return {data:Buffer.concat(chunks),mime};}
function artworkHandler(cache,fetcher){return async request=>{try{const requestURL=new URL(request.url),target=requestURL.searchParams.get('url');if(requestURL.hostname!=='image'||!validArtwork(target||''))return new Response(null,{status:400});const image=await cache.getOrLoad('artwork:'+target,'artwork',()=>fetchArtwork(target,fetcher));return new Response(image.data,{headers:{'content-type':image.mime,'cache-control':'no-store'}});}catch{return new Response(null,{status:502});}};}
module.exports={NativeCache,catalogTarget,validArtwork,fetchArtwork,artworkHandler};

'use strict';
// A bounded, on-demand MSE adapter for the existing <audio controls>. No remote
// page, credentials or CDN URLs cross into this renderer.
class NativeSegmentPlayer {
  static supported(receipt) {
    const s=receipt?.stream;
    if(!s||receipt.mode!=='stream'||!/^\/api\/native\/playback\/[a-f0-9]{32}$/.test(receipt.streamPath||'')||s.mime!=='audio/mp4; codecs="mp4a.40.2"'||!Number.isFinite(s.duration)||s.duration<=0||s.duration>86400||!Number.isFinite(s.timestampOffset)||s.timestampOffset>0||s.timestampOffset< -86400||!Array.isArray(s.segments)||!s.segments.length||s.segments.length>4096)return false;
    let end=0;for(const p of s.segments){if(!Number.isFinite(p.start)||Math.abs(p.start-end)>.001||!Number.isFinite(p.duration)||p.duration<=0||p.duration>120)return false;end=p.start+p.duration;}
    return Math.abs(end-s.duration)<.001&&!!globalThis.MediaSource?.isTypeSupported(s.mime);
  }
  constructor(audio,receipt,transport,onFailure,onState) {
    if(!NativeSegmentPlayer.supported(receipt)||typeof transport.playbackChunk!=='function')throw Error('stream_unsupported');
    Object.assign(this,{audio,receipt,transport,onFailure,onState,version:0,next:0,busy:false,stopped:false,loaded:new Set(),listeners:[],cancelBarrier:Promise.resolve()});
  }
  listen(event,fn){this.audio.addEventListener(event,fn);this.listeners.push([event,fn]);}
  async start() {
    this.media=new MediaSource();this.url=URL.createObjectURL(this.media);
    const opened=new Promise((resolve,reject)=>{this.openReject=reject;const timer=setTimeout(()=>reject(Error('stream_open_timeout')),10000);this.media.addEventListener('sourceopen',()=>{clearTimeout(timer);resolve();},{once:true});this.openTimer=timer;});
    this.audio.src=this.url;await opened;if(this.stopped)throw Error('stream_cancelled');
    this.buffer=this.media.addSourceBuffer(this.receipt.stream.mime);this.buffer.timestampOffset=this.receipt.stream.timestampOffset;this.media.duration=this.receipt.stream.duration;
    await this.loadPart(this.receipt.streamPath+'/init',this.version);await this.loadIndex(0,this.version);this.next=1;
    this.listen('seeking',()=>this.seek());this.listen('waiting',()=>{this.onState?.('正在缓冲 · 完整播放');void this.pump();});this.listen('playing',()=>this.onState?.('完整播放'));this.listen('timeupdate',()=>{void this.pump();});
    this.timer=setInterval(()=>{void this.pump();},500);await this.audio.play();void this.pump();
  }
  mutate(action,version){
    if(this.stopped||version!==this.version)return Promise.reject(Error('stream_cancelled'));
    return new Promise((resolve,reject)=>{
      const done=()=>{cleanup();resolve();},failed=()=>{cleanup();reject(Error('stream_append_failed'));};
      const cleanup=()=>{clearTimeout(timer);this.buffer.removeEventListener('updateend',done);this.buffer.removeEventListener('error',failed);this.buffer.removeEventListener('abort',failed);};
      const timer=setTimeout(failed,10000);this.buffer.addEventListener('updateend',done,{once:true});this.buffer.addEventListener('error',failed,{once:true});this.buffer.addEventListener('abort',failed,{once:true});
      try{action();}catch(e){cleanup();reject(e);}
    });
  }
  async loadPart(path,version){await this.cancelBarrier;if(this.stopped||version!==this.version)throw Error('stream_cancelled');const data=await this.transport.playbackChunk(path);if(this.stopped||version!==this.version)throw Error('stream_cancelled');if(!(data instanceof Uint8Array)||!data.byteLength||data.byteLength>8*1024*1024)throw Error('stream_data_invalid');await this.mutate(()=>this.buffer.appendBuffer(data),version);}
  async loadIndex(i,version){await this.loadPart(this.receipt.streamPath+'/segment/'+i,version);if(!this.stopped&&version===this.version)this.loaded.add(i);}
  buffered(time){for(let i=0;i<this.audio.buffered.length;i++)if(time>=this.audio.buffered.start(i)-.05&&time<this.audio.buffered.end(i)-.05)return this.audio.buffered.end(i);return time;}
  seek(){
    if(this.stopped)return;const time=this.audio.currentTime,available=this.buffered(time)>time+.2;
    // Even an already buffered destination needs a new forward cursor: a
    // previous distant seek may have left a gap after that buffered island.
    ++this.version;const index=this.receipt.stream.segments.findIndex(p=>p.start+p.duration>time);this.next=index<0?this.receipt.stream.segments.length:index;
    if(!available){this.loaded.delete(this.next);this.onState?.('正在缓冲 · 完整播放');}
    this.cancelBarrier=Promise.resolve(this.transport.cancelPlaybackChunks?.(this.receipt.streamPath)).catch(()=>{});
    if(this.buffer?.updating&&this.media.readyState==='open')try{this.buffer.abort();}catch{}
    void this.pump();
  }
  async pump(){
    if(this.busy||this.stopped||!this.buffer)return;this.busy=true;const version=this.version;
    try{
      await this.cancelBarrier;if(this.stopped||version!==this.version)return;
      const time=this.audio.currentTime,segments=this.receipt.stream.segments;
      // Trim on complete fragment boundaries, never remove the unplayed tail
      // of a long fragment while keeping its index marked as loaded.
      const previous=segments.findIndex(p=>p.start+p.duration>Math.max(0,time-15));
      const keep=previous<0?0:segments[previous].start;
      if(keep>0&&this.buffer.buffered.length&&this.buffer.buffered.start(0)<keep-.5){await this.mutate(()=>this.buffer.remove(0,keep),version);for(const i of this.loaded)if(this.receipt.stream.segments[i].start+this.receipt.stream.segments[i].duration<=keep)this.loaded.delete(i);}
      // Reverse seeking must not retain every formerly buffered future range.
      const ahead=segments.find(p=>p.start>=time+35)?.start??this.receipt.stream.duration;
      if(this.buffer.buffered.length&&this.buffer.buffered.end(this.buffer.buffered.length-1)>ahead+.5){await this.mutate(()=>this.buffer.remove(ahead,this.receipt.stream.duration),version);for(const i of this.loaded)if(this.receipt.stream.segments[i].start+this.receipt.stream.segments[i].duration>ahead)this.loaded.delete(i);}
      for(let n=0;n<3&&!this.stopped&&version===this.version;n++){
        if(this.buffered(this.audio.currentTime)>=this.audio.currentTime+20)break;
        while(this.loaded.has(this.next))this.next++;
        if(this.next>=this.receipt.stream.segments.length){if(this.media.readyState==='open'&&!this.buffer.updating)this.media.endOfStream();break;}
        const index=this.next;await this.loadIndex(index,version);if(version===this.version)this.next=index+1;
      }
    }catch(error){if(!this.stopped&&version===this.version){this.dispose();this.onFailure?.(error);}}
    finally{this.busy=false;if(!this.stopped&&version!==this.version)queueMicrotask(()=>{void this.pump();});}
  }
  dispose(){if(this.stopped)return;this.stopped=true;++this.version;clearInterval(this.timer);clearTimeout(this.openTimer);this.openReject?.(Error('stream_cancelled'));for(const [event,fn]of this.listeners)this.audio.removeEventListener(event,fn);void Promise.resolve(this.transport.cancelPlaybackChunks?.(this.receipt.streamPath)).catch(()=>{});if(this.buffer?.updating&&this.media?.readyState==='open')try{this.buffer.abort();}catch{}if(this.url)URL.revokeObjectURL(this.url);}
}
globalThis.NativeSegmentPlayer=NativeSegmentPlayer;

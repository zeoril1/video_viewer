// Use the pinned hls.js controllers, not a reimplementation of their formulas.
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const Hls=require('../web/vendor/hls.min.js');
const player=fs.readFileSync(path.join(__dirname,'../web/player.js'),'utf8');
const start=player.indexOf('  function playbackHlsConfig('),end=player.indexOf('  // Запуск HLS-потока',start);
const configContext=vm.createContext({DEBUG:false});vm.runInContext(player.slice(start,end),configContext);
const playlist='#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MAP:URI="init.mp4"\n'+
  Array.from({length:3},(_,n)=>'#EXTINF:6,\nseg_'+n+'.m4s\n').join('')+'#EXT-X-ENDLIST\n';
class PlaylistLoader{
  constructor(){this.stats={aborted:false,loaded:playlist.length,total:playlist.length,retry:0,chunkCount:0,bwEstimate:0,loading:{start:1,first:2,end:3},parsing:{start:0,end:0},buffering:{start:0,first:0,end:0}};}
  load(context,config,callbacks){this.context=context;queueMicrotask(()=>callbacks.onSuccess({url:context.url,data:playlist,code:200},this.stats,context,null));}
  getResponseHeader(){return null;}getCacheAge(){return null;}abort(){}destroy(){}
}
async function realHls(run){
  const previous=global.self,previousLocation=global.location;global.self=globalThis;global.location={href:'http://fixture.local/watch.html'};
  const hls=new Hls({...configContext.playbackHlsConfig(),autoStartLoad:false,enableWorker:false,loader:PlaylistLoader});
  try{
    const errors=[];hls.on(Hls.Events.ERROR,(_event,data)=>errors.push({details:data.details,message:data.error?.message||data.reason}));
    await new Promise(resolve=>{hls.once(Hls.Events.MANIFEST_PARSED,resolve);hls.loadSource('http://fixture.local/hls.m3u8');});
    assert.equal(hls.levels.length,1,'The real media playlist parser supplies one level: '+JSON.stringify(errors));
    await run(hls);
  }finally{hls.destroy();if(previous===undefined)delete global.self;else global.self=previous;if(previousLocation===undefined)delete global.location;else global.location=previousLocation;}
}
function loaded(hls,index,bytes){
  const frag=hls.levels[0].details.fragments[index];
  frag.stats.loaded=frag.stats.total=bytes;frag.stats.loading={start:100,first:110,end:1000};
  hls.trigger(Hls.Events.FRAG_LOADED,{frag,payload:new Uint8Array(1)});
  return frag;
}

test('a plain playlist with no declared bitrate learns real fragment bitrate and targets 1 GiB in the actual stream controller',()=>realHls(hls=>{
  assert.equal(Hls.version,'1.5.17');
  const level=hls.levels[0],controller=hls.streamController;
  assert.equal(level.bitrate,0);assert.equal(level.maxBitrate,0);
  assert.equal(controller.getMaxBufferLength(level.maxBitrate),60,'Startup remains bounded before the first bitrate sample');
  loaded(hls,0,6*1024**2);
  assert.equal(level.realBitrate,8*1024**2);assert.equal(level.maxBitrate,level.realBitrate);
  assert.equal(controller.getMaxBufferLength(level.maxBitrate),1024,'The unknown-bitrate playlist expands beyond 60 seconds using actual loaded bytes');
  assert.equal(controller.getMaxBufferLength(level.maxBitrate)*level.maxBitrate/8,1024**3);
  loaded(hls,1,3*1024**2);
  assert.equal(level.loaded.bytes,9*1024**2);assert.equal(level.loaded.duration,12);
  assert.equal(level.realBitrate,6*1024**2);
  assert.ok(Math.abs(controller.getMaxBufferLength(level.maxBitrate)*level.maxBitrate/8-1024**3)<1);
}));

test('init segments do not distort the media bitrate or the byte-based forward target',()=>realHls(hls=>{
  loaded(hls,0,6*1024**2);
  const level=hls.levels[0],init=level.details.fragments[0].initSegment;
  init.stats.loaded=100*1024**2;init.stats.loading={start:1,first:2,end:3};
  hls.trigger(Hls.Events.FRAG_LOADED,{frag:init,payload:new Uint8Array(1)});
  assert.equal(level.loaded.bytes,6*1024**2);assert.equal(level.maxBitrate,8*1024**2);
}));

test('finite time ceiling permits native quota reductions and later bitrate samples preserve the smaller MSE window',()=>realHls(hls=>{
  loaded(hls,0,6*1024**2);
  const controller=hls.streamController,level=hls.levels[0],limits=[];
  assert.ok(Number.isFinite(hls.config.maxMaxBufferLength));assert.equal(hls.config.backBufferLength,30);
  for(let attempt=0;attempt<10;attempt++){
    assert.equal(controller.reduceMaxBufferLength(120,6),true);
    limits.push(hls.config.maxMaxBufferLength);
  }
  assert.ok(limits.every(Number.isFinite));assert.ok(limits[0]<21600);assert.equal(limits.at(-1),102);
  assert.equal(controller.getMaxBufferLength(level.maxBitrate),102);
  loaded(hls,1,1024**2);
  assert.equal(controller.getMaxBufferLength(level.maxBitrate),102,'The browser quota adaptation is not reset by the next bitrate update');
}));

test('the actual buffer-full recovery reduces its ceiling without flushing a useful forward buffer',()=>realHls(hls=>{
  const controller=hls.streamController,frag=loaded(hls,0,6*1024**2);
  controller.media=controller.mediaBuffer={currentTime:0,buffered:{length:1,start:()=>0,end:()=>120},removeEventListener(){}};
  controller.state='PARSING';controller.fragCurrent=frag;
  const flush=controller.reduceLengthAndFlushBuffer({parent:'main',frag,details:Hls.ErrorDetails.BUFFER_FULL_ERROR});
  assert.equal(flush,false);assert.equal(hls.config.maxMaxBufferLength,10800);
  assert.equal(controller.state,'IDLE');
}));

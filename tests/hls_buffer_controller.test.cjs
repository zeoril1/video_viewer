// Use the pinned hls.js controllers, not a reimplementation of their formulas.
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const Hls=require('../web/vendor/hls.min.js');
const player=fs.readFileSync(path.join(__dirname,'../web/player.js'),'utf8');
const start=player.indexOf('  function playbackHlsConfig('),end=player.indexOf('  // Запуск HLS-потока',start);
const configContext=vm.createContext({DEBUG:false});vm.runInContext(player.slice(start,end),configContext);
function configForAgent(userAgent) {
  const context=vm.createContext({DEBUG:false,navigator:{userAgent}});
  vm.runInContext(player.slice(start,end),context);
  return context.playbackHlsConfig();
}
const playlist='#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MAP:URI="init.mp4"\n'+
  Array.from({length:3},(_,n)=>'#EXTINF:6,\nseg_'+n+'.m4s\n').join('')+'#EXT-X-ENDLIST\n';
class PlaylistLoader{
  constructor(){this.stats={aborted:false,loaded:playlist.length,total:playlist.length,retry:0,chunkCount:0,bwEstimate:0,loading:{start:1,first:2,end:3},parsing:{start:0,end:0},buffering:{start:0,first:0,end:0}};}
  load(context,config,callbacks){this.context=context;queueMicrotask(()=>callbacks.onSuccess({url:context.url,data:playlist,code:200},this.stats,context,null));}
  getResponseHeader(){return null;}getCacheAge(){return null;}abort(){}destroy(){}
}
async function realHls(run,userAgent){
  const previous=global.self,previousLocation=global.location;global.self=globalThis;global.location={href:'http://fixture.local/watch.html'};
  const config=userAgent === undefined ? configContext.playbackHlsConfig() : configForAgent(userAgent);
  const hls=new Hls({...config,autoStartLoad:false,enableWorker:false,loader:PlaylistLoader});
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

test('Android Chrome, WebView and the existing TV app keep standard HLS buffering after real fragment downloads', async()=>{
  for(const agent of ['Mozilla/5.0 (Linux; Android 14) Chrome/129.0 Mobile Safari/537.36',
    'Mozilla/5.0 (Linux; Android 9; wv) Version/4.0 Chrome/120.0 VVTV/1.0', 'VVTV/1.0']) {
    await realHls(hls=>{
      for(const name of ['maxBufferLength','maxMaxBufferLength','maxBufferSize','abrMaxWithRealBitrate']) {
        assert.equal(hls.config[name],Hls.DefaultConfig[name],name+' for '+agent);
      }
      const level=hls.levels[0],controller=hls.streamController;
      assert.equal(controller.getMaxBufferLength(level.maxBitrate),30);
      loaded(hls,0,6*1024**2);loaded(hls,1,3*1024**2);
      assert.equal(controller.getMaxBufferLength(level.maxBitrate),30,
        'Real bitrate samples must not expand an Android media playlist to the 1 GiB target');
      assert.equal(hls.config.startPosition,0);assert.equal(hls.config.liveSyncDurationCount,Infinity);
      assert.equal(hls.config.backBufferLength,30);
    },agent);
  }
});

test('desktop clients keep the 1 GiB target and TV presentation alone does not lower their buffer',()=>{
  for(const agent of ['Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/129.0',
    'Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0', 'SMART-TV Desktop']) {
    const config=configForAgent(agent);
    assert.equal(config.maxBufferSize,1024**3);assert.equal(config.maxMaxBufferLength,21600);
    assert.equal(config.maxBufferLength,60);assert.equal(config.abrMaxWithRealBitrate,true);
  }
});

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

test('an asynchronous SourceBuffer failure is distinct from quota pressure and carries the fatal append cause',()=>realHls(hls=>{
  const buffer=hls.bufferController,frag=hls.levels[0].details.fragments[0],errors=[];
  const originalQueue=buffer.operationQueue,originalMediaSource=buffer.mediaSource;
  let append;
  // Capture the actual operation created by the pinned buffer controller. Its
  // callbacks, retry counter and emitted errors all remain production code.
  buffer.operationQueue={append(operation){append=operation;},current(){return append;}};
  buffer.mediaSource={readyState:'open'};
  buffer.appendErrors.video=hls.config.appendErrorMaxRetry-1;
  hls.on(Hls.Events.ERROR,(_event,error)=>errors.push(error));
  try{
    buffer.onBufferAppending(Hls.Events.BUFFER_APPENDING,{data:new Uint8Array(1),type:'video',frag,
      chunkMeta:{buffering:{video:{start:0,executeStart:0,executeEnd:0,end:0}},id:1,sn:frag.sn}});
    buffer._onSBUpdateError('video',{type:'error'});
    assert.deepEqual(errors.map(error=>({details:error.details,fatal:error.fatal})),[
      {details:Hls.ErrorDetails.BUFFER_APPENDING_ERROR,fatal:false},
      {details:Hls.ErrorDetails.BUFFER_APPEND_ERROR,fatal:true},
    ]);
    assert.equal(errors[1].sourceBufferName,'video');
    assert.equal(errors[1].frag,frag);
    assert.equal(errors[1].error,errors[0].error);
    assert.match(errors[1].error.message,/video SourceBuffer error.*readyState: open/);
    assert.notEqual(errors[1].details,Hls.ErrorDetails.BUFFER_FULL_ERROR,
      'An asynchronous decoder/append failure must not be diagnosed as MSE quota exhaustion');
  }finally{
    buffer.operationQueue=originalQueue;buffer.mediaSource=originalMediaSource;
  }
}));

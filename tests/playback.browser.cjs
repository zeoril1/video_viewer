// Browser integration with deterministic media/network fixtures; no external sources.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const web=path.join(__dirname,'../web');
let state=null,updated=0,revision=0,prepareCount=0,roomClosed=false,lastPrepare='';
const participants=new Map();
const series=fs.readFileSync(path.join(web,'series.js'),'utf8');
const voiceOffset=series.indexOf('function matchVoiceOrdinal(');
const voiceMatcher=series.slice(voiceOffset,series.indexOf('\n}',voiceOffset)+2);
const bootstrap=`
const VOICE_ALIASES={};
${voiceMatcher}
const VV={user:{id:1}},DEBUG=false;
let currentItem={id:'tt1',kind:'tv'};
const t=x=>x,dbg=()=>{},fmtTime=x=>String(x),isHevcCodec=()=>false,isSeriesKind=()=>true;
const episodeHistoryEntry=()=>null,rememberWatchProgress=()=>{},seasonEpisodeCount=()=>3,canonicalByNumber=()=>null,titleVoices=()=>[];
const fetchFiles=async(id,magnet)=>magnet.endsWith('456')?[{index:2,season:1,episode:3}]:[{index:0,season:1,episode:1},{index:1,season:1,episode:2}];
const Personal={data:null,get:()=>Personal.data,rank:x=>x,preferences:()=>({}),put:async(k,id,data)=>{Personal.data={data:{...data}};window.dispatchEvent(new Event('personalchange'));}};
const video=document.getElementById('player');
let paused=true;
Object.defineProperty(video,'paused',{get:()=>paused});
Object.defineProperty(video,'readyState',{get:()=>4});
video.load=()=>{video.currentTime=0;};
video.play=async()=>{if(paused){paused=false;video.dispatchEvent(new Event('play'));video.dispatchEvent(new Event('playing'));}};
video.pause=()=>{if(!paused){paused=true;video.dispatchEvent(new Event('pause'));}};
window.hlsRequests=[];
window.Hls=class {
 static Events={MANIFEST_PARSED:'manifest',SUBTITLE_TRACKS_UPDATED:'subs',LEVEL_SWITCHED:'level',FRAG_BUFFERED:'frag',ERROR:'error'};
 static isSupported(){return true;}
 constructor(){this.handlers={};this.subtitleTracks=[];}
 on(name,fn){this.handlers[name]=fn;}
 loadSource(src){window.lastHlsSource=src;window.hlsRequests.push(src);}
 attachMedia(){setTimeout(()=>{if(!this.dead&&this.handlers.manifest)this.handlers.manifest();},150);}
 destroy(){this.dead=true;}
};
`;
const server=http.createServer((req,res)=>{
 const u=new URL(req.url,'http://local');
 const json=data=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(data));};
 if(u.pathname==='/api/rooms'&&req.method==='POST'){
  let body='';req.on('data',d=>body+=d);return req.on('end',()=>{state=JSON.parse(body);updated=Date.now();roomClosed=false;json({room:'a'.repeat(32),host_token:'host'});});
 }
 if(u.pathname.startsWith('/api/rooms/')){
  if(roomClosed){res.statusCode=404;return res.end('Комната закрыта.');}
  if(u.pathname.endsWith('/leave')){participants.delete(req.headers['x-room-member']);res.statusCode=204;return res.end();}
  if(req.method==='GET'){const name=(req.headers.cookie||'').includes('fixture_user=Anna')?'Anna':'198.51.100.7';participants.set(req.headers['x-room-member'],{name,kind:name==='Anna'?'user':'guest'});}
  if(req.method==='DELETE'){roomClosed=true;res.statusCode=204;return res.end();}
  if(req.method==='PUT'){
   if(req.headers['x-room-host']!=='host'){res.statusCode=403;return res.end();}
   let body='';req.on('data',d=>body+=d);return req.on('end',()=>{state=JSON.parse(body);updated=Date.now();json({state,updated,revision:++revision,server_time:Date.now(),participants:Array.from(participants.values())});});
  }
  return json({state,updated,revision,server_time:Date.now()});
 }
 if(u.pathname==='/api/stream/prepare'){prepareCount++;lastPrepare=u.searchParams.get('magnet');res.statusCode=204;return res.end();}
 if(u.pathname.endsWith('/sources'))return json({items:[{magnet:'magnet:?xt=urn:btih:456',title:'Next source'}]});
 if(u.pathname.endsWith('/tracks'))return setTimeout(()=>json({duration:300,codec:'h264',height:720,items:u.searchParams.get('file')==='1'?[{ordinal:0,title:'LostFilm',language:'rus'},{ordinal:1,title:'Original',language:'eng'}]:[{ordinal:0,title:'Original',language:'eng'},{ordinal:1,title:'LostFilm',language:'rus'}],subtitles:[{ordinal:0,language:'rus',title:'Русский'}]}),350);
 if(u.pathname.startsWith('/api/')){res.statusCode=204;return res.end();}
 if(u.pathname==='/watch.html'){
  let html=fs.readFileSync(path.join(web,'watch.html'),'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g,'');
  html=html.replace('</body>',`<script>${bootstrap}</script><script src="/player.js"></script><script src="/playback-extras.js"></script><script src="/watch-room.js"></script></body>`);
  res.setHeader('Content-Type','text/html; charset=utf-8');return res.end(html);
 }
 const file=path.join(web,u.pathname);res.setHeader('Content-Type',u.pathname.endsWith('.js')?'text/javascript':'text/css');
 fs.readFile(file,(e,data)=>{res.statusCode=e?404:200;res.end(e?'':data);});
});
(async()=>{
 await new Promise(resolve=>server.listen(0,'0.0.0.0',resolve));
 const browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_PATH,args:['--no-sandbox']});
 const errors=[];
 try{
  const base='http://127.0.0.1:'+server.address().port;
  const host=await browser.newPage(),guest=await browser.newPage();
  for(const p of [host,guest])p.on('pageerror',e=>errors.push(e.message));
  await host.goto(base+'/watch.html');
  await host.evaluate(()=>PP.start({id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,season:1,ep:1,pos:10}));
  assert.equal(await host.evaluate(()=>window.hlsRequests.length),0,'HLS must wait for track metadata');
  await host.waitForFunction(()=>document.getElementById('loading-state').hidden);
  await host.evaluate(()=>window.dispatchEvent(new CustomEvent('playbackstage',{detail:{stage:'error',message:'Сервер занят'}})));
  assert.match(await host.locator('#loading-detail').textContent(),/Сервер занят/);
  await host.locator('#loading-retry').click();
  await host.waitForFunction(()=>document.getElementById('loading-state').hidden);
  await host.locator('summary').click();
  await host.locator('#subtitle-size').selectOption('130');
  await host.locator('#subtitle-language').selectOption('rus');
  await host.waitForFunction(()=>!document.getElementById('player').paused);
  await host.evaluate(()=>{
    const tr=document.getElementById('player').addTextTrack('subtitles','Test','ru');tr.mode='showing';tr.addCue(new VTTCue(5,10,'Тест'));
  });
  await host.locator('#subtitle-delay').fill('2');await host.locator('#subtitle-delay').dispatchEvent('change');
  await host.evaluate(()=>{for(let i=0;i<3;i++)video.dispatchEvent(new Event('timeupdate'));});
  assert.equal(await host.evaluate(()=>video.textTracks[0].cues[0].startTime),7);
  await host.locator('#subtitle-position').selectOption('top');
  assert.equal(await host.evaluate(()=>video.textTracks[0].cues[0].line),10);
  assert.equal(await host.evaluate(()=>Personal.data.data.size),130);
  await host.evaluate(()=>{video.currentTime=200;video.dispatchEvent(new Event('timeupdate'));});
  await host.waitForFunction(()=>document.getElementById('prepare-status').textContent.includes('подготовлено'));
  await host.evaluate(()=>video.dispatchEvent(new Event('timeupdate')));assert.equal(prepareCount,1);
  await host.locator('#tracks-list .track-btn').filter({hasText:'LostFilm'}).click();
  const launchesBeforeNext=await host.evaluate(()=>window.hlsRequests.length);
  await host.evaluate(()=>PP.playNeighbor(1,true));
  assert.equal(await host.evaluate(()=>window.hlsRequests.length),launchesBeforeNext,'No provisional stream for next episode');
  await host.evaluate(()=>{video.currentTime=25;}); // Simulate stale media time while metadata is pending.
  await host.waitForFunction(()=>!video.paused&&PP.state().episode===2&&document.querySelector('#tracks-list .active')?.textContent.includes('LostFilm'));
  assert.equal(await host.evaluate(()=>new URL(window.lastHlsSource,location.href).searchParams.get('track')),'0');
  assert.equal(await host.evaluate(()=>window.hlsRequests.length),launchesBeforeNext+1,'Audio and subtitles must be selected in one launch');
  assert.equal(await host.evaluate(()=>new URL(window.lastHlsSource,location.href).searchParams.get('start')),null,'New episode starts at zero, not stale currentTime');
  assert.equal(await host.evaluate(()=>new URL(window.lastHlsSource,location.href).searchParams.get('subs')),'0');
  await host.evaluate(()=>{video.currentTime=200;video.dispatchEvent(new Event('timeupdate'));});
  await host.waitForFunction(()=>document.getElementById('prepare-status').textContent.includes('подготовлено'));
  assert.equal(prepareCount,2);assert.equal(lastPrepare,'magnet:?xt=urn:btih:456');
  await host.evaluate(()=>PP.playNeighbor(1,true));
  await host.waitForFunction(()=>PP.state().episode===3&&!video.paused);
  assert.equal(await host.evaluate(()=>PP.state().magnet),'magnet:?xt=urn:btih:456');
  await host.waitForFunction(()=>document.querySelector('#tracks-list .active')?.textContent.includes('LostFilm'));
  assert.equal(await host.evaluate(()=>new URL(window.lastHlsSource,location.href).searchParams.get('track')),'1');
  await host.evaluate(()=>{VV.user=null;});
  await host.locator('#room-create').click();
  await host.waitForFunction(()=>!document.getElementById('room-invite').hidden);
  await guest.goto(base+'/watch.html');await guest.evaluate(()=>{VV.user=null;return WatchRoom.join('a'.repeat(32));});
  await guest.waitForFunction(()=>PP.state().id==='tt1'&&!video.paused);
  await host.waitForFunction(()=>document.getElementById('room-participant-list').textContent.includes('198.51.100.7'));
  assert.equal(await guest.locator('#room-participants').isVisible(),false);
  const signed=await browser.newPage();signed.on('pageerror',e=>errors.push(e.message));
  await signed.context().addCookies([{name:'fixture_user',value:'Anna',url:base}]);
  await signed.goto(base+'/watch.html');await signed.evaluate(()=>WatchRoom.join('a'.repeat(32)));
  await host.waitForFunction(()=>document.getElementById('room-participant-list').textContent.includes('Anna'));
  assert.equal(await host.locator('#room-participant-list li').count(),2);
  await signed.locator('#room-leave').click();
  await host.waitForFunction(()=>document.getElementById('room-participant-list').children.length===1);
  await signed.close();

  await host.evaluate(()=>video.pause());
  await guest.waitForFunction(()=>video.paused);
  await host.evaluate(()=>{video.currentTime=230;video.dispatchEvent(new Event('seeked'));});
  await guest.waitForFunction(()=>PP.state().position>=230,null,{timeout:15000});
  await host.evaluate(()=>video.play());await guest.waitForFunction(()=>!video.paused);
  await host.locator('#room-leave').click();
  await guest.waitForFunction(()=>document.getElementById('room-status').textContent.includes('закрыта'));
  assert.equal(await guest.evaluate(()=>document.body.classList.contains('room-guest')),false);
  assert.equal(await guest.evaluate(()=>video.paused),true);
  await host.setViewportSize({width:390,height:844});
  assert.equal(await host.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true);
  if(process.env.PLAYBACK_SCREENSHOT)await host.screenshot({path:process.env.PLAYBACK_SCREENSHOT,fullPage:true});
  assert.deepEqual(errors,[]);
  console.log('Playback browser checks passed: loading, subtitle preferences/cue timing, prefetch, host/guest pause/seek/resume, room close, mobile layout.');
 }finally{await browser.close();server.close();}
})().catch(error=>{console.error(error);process.exitCode=1;server.close();});

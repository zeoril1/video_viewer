// Offline integration with the actual player and deterministic media: no torrent
// or loopback server is required to reproduce room timing.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const web=path.join(__dirname,'../web');
let remote={id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,season:1,episode:1,position:100,paused:true};
let updated=Date.now(),gets=0,hlsStops=0;
const bootstrap=`
const VV={user:null},DEBUG=false;
let currentItem={id:'tt1',kind:'tv'};
const t=x=>x,dbg=()=>{},fmtTime=x=>String(x),isHevcCodec=()=>false,isSeriesKind=()=>true;
const episodeHistoryEntry=()=>null,rememberWatchProgress=()=>{},seasonEpisodeCount=()=>3,canonicalByNumber=()=>null,titleVoices=()=>[];
const fetchFiles=async()=>[{index:0,season:1,episode:1},{index:1,season:1,episode:2}];
const Personal={get:()=>null,rank:x=>x,preferences:()=>({}),put:async()=>{}};
const video=document.getElementById('player');
let paused=true,mediaReady=0,mediaTime=0,mediaAt=performance.now(),bufferEnd=90;
const mediaPosition=()=>mediaTime+(paused?0:(performance.now()-mediaAt)/1000);
Object.defineProperty(video,'paused',{get:()=>paused});
Object.defineProperty(video,'readyState',{get:()=>mediaReady});
Object.defineProperty(video,'currentTime',{get:mediaPosition,set:value=>{mediaTime=value;mediaAt=performance.now();}});
Object.defineProperty(video,'buffered',{get:()=>({length:mediaReady?1:0,start:()=>0,end:()=>bufferEnd})});
video.load=()=>{mediaTime=0;mediaAt=performance.now();mediaReady=0;};
video.play=async()=>{if(paused){mediaAt=performance.now();paused=false;video.dispatchEvent(new Event('play'));video.dispatchEvent(new Event('playing'));}};
video.pause=()=>{if(!paused){mediaTime=mediaPosition();paused=true;video.dispatchEvent(new Event('pause'));}};
window.fixture={starts:[],seeks:[],bufferedSeeks:[],manifestDelay:80,fragmentDelay:0,setBuffer(end){bufferEnd=end;mediaReady=4;video.dispatchEvent(new Event('progress'));}};
window.Hls=class {
 static Events={MANIFEST_PARSED:'manifest',SUBTITLE_TRACKS_UPDATED:'subs',LEVEL_SWITCHED:'level',FRAG_BUFFERED:'frag',ERROR:'error'};
 static isSupported(){return true;}
 constructor(){this.handlers={};this.subtitleTracks=[];}
 on(name,fn){this.handlers[name]=fn;}
 loadSource(src){fixture.starts.push(src);}
 attachMedia(){
  setTimeout(()=>{if(!this.dead)this.handlers.manifest?.();},fixture.manifestDelay);
  setTimeout(()=>{if(!this.dead){mediaReady=4;video.dispatchEvent(new Event('canplay'));video.dispatchEvent(new Event('progress'));}},Math.max(fixture.manifestDelay,fixture.fragmentDelay));
 }
 destroy(){this.dead=true;}
};
`;
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_PATH,args:['--no-sandbox']});
 try{
  const context=await browser.newContext(),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await context.route('**/*',async route=>{
   const u=new URL(route.request().url());
   const json=data=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(data)});
   if(u.pathname.endsWith('/hls/stop')){hlsStops++;return route.fulfill({status:204});}
   if(u.pathname.startsWith('/api/rooms/')){
    if(route.request().method()!=='GET')return route.fulfill({status:204});
    gets++;
    // A real host continues publishing state even while a guest buffers.
    const now=Date.now(),state={...remote,position:remote.position+(remote.paused?0:(now-updated)/1000)};
    return json({state,updated:now,server_time:now,revision:gets});
   }
   if(u.pathname.endsWith('/tracks'))return json({duration:900,codec:'h264',height:720,items:[{ordinal:0,title:'Original',language:'eng'}],subtitles:[]});
   if(u.pathname==='/api/stream/download-status')return json({complete:false,downloaded:0,total:100});
   if(u.pathname.startsWith('/api/'))return route.fulfill({status:204});
   if(u.pathname==='/watch.html'){
    let html=fs.readFileSync(path.join(web,'watch.html'),'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g,'');
    html=html.replace('</body>',`<script>${bootstrap}</script><script src="/player.js"></script><script src="/playback-extras.js"></script><script src="/watch-room.js"></script></body>`);
    return route.fulfill({status:200,contentType:'text/html; charset=utf-8',body:html});
   }
   const file=path.join(web,u.pathname);
   return route.fulfill(fs.existsSync(file)?{status:200,contentType:u.pathname.endsWith('.js')?'text/javascript':'text/css',body:fs.readFileSync(file)}:{status:404});
  });
  await page.goto('http://fixture.local/watch.html');
  const instrumentAndJoin=()=>{
   const seek=PP.seek,seekBuffered=PP.seekBuffered;
   PP.seek=target=>{fixture.seeks.push(target);return seek(target);};
   PP.seekBuffered=target=>{fixture.bufferedSeeks.push(target);return seekBuffered(target);};
   return WatchRoom.join('a'.repeat(32));
  };
  await page.evaluate(instrumentAndJoin);
  await page.waitForFunction(()=>PP.ready()&&video.paused);
  assert.equal(await page.evaluate(()=>PP.state().position),100);
  const beforeResume=gets;
  await page.evaluate(()=>{video.currentTime-=1;});
  remote={...remote,paused:false};updated=Date.now();
  await page.waitForFunction(()=>!video.paused&&fixture.bufferedSeeks.length>0);
  const expected=remote.position+(Date.now()-updated)/1000;
  assert.ok(Math.abs(await page.evaluate(()=>PP.state().position)-expected)<0.25,'Resume must catch a one-second drift before playing');
  assert.ok(gets>beforeResume,'Follower receives host resume through polling');

  const beforeDrift=await page.evaluate(()=>fixture.bufferedSeeks.length);
  await page.evaluate(()=>{video.currentTime-=1;});
  await page.waitForFunction(count=>fixture.bufferedSeeks.length>count,beforeDrift);
  assert.ok(Math.abs(await page.evaluate(()=>PP.state().position)-(remote.position+(Date.now()-updated)/1000))<0.25,'One-second playing drift must not stay in the old three-second deadband');

  await page.evaluate(()=>{fixture.manifestDelay=1400;});
  remote={...remote,file:1,episode:2,position:200};updated=Date.now();
  await page.waitForFunction(()=>PP.state().file===1&&PP.ready()&&!video.paused&&fixture.starts.length===2);
  assert.ok(Math.abs(await page.evaluate(()=>PP.state().position)-(remote.position+(Date.now()-updated)/1000))<0.25,'New media catches the full time spent preparing and loading');
  remote={...remote,position:230,paused:true};updated=Date.now();
  await page.waitForFunction(()=>video.paused&&Math.abs(PP.state().position-230)<0.01);

  // These are simulated lifecycle events, not proof of real browser bfcache
  // eligibility: routing and overridden media deliberately form an offline fixture.
  const stopsBeforePersistedHide=hlsStops;
  await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true})));
  await page.waitForTimeout(250);
  assert.equal(hlsStops,stopsBeforePersistedHide,'Persisted pagehide keeps the server HLS session available for restoration');
  const hiddenGets=gets;
  await page.waitForTimeout(1250);
  assert.equal(gets,hiddenGets,'pagehide stops room polling');
  remote={...remote,file:0,episode:1,position:400,paused:false};updated=Date.now();
  await page.evaluate(()=>{fixture.manifestDelay=80;window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
  await page.waitForFunction(()=>PP.state().file===0&&PP.ready()&&!video.paused);
  assert.ok(gets>hiddenGets,'Simulated pageshow immediately obtains fresh room state');
  assert.ok(Math.abs(await page.evaluate(()=>PP.state().position)-(remote.position+(Date.now()-updated)/1000))<0.35,'Restoration applies the current source and position');
  await page.evaluate(()=>{window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
  await page.waitForTimeout(250);
  const restoredGets=gets;
  await page.waitForTimeout(2250);
  assert.ok(gets-restoredGets>=2&&gets-restoredGets<=3,'Repeated pageshow leaves one polling loop');
  await page.close();

  remote={id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,season:1,episode:1,position:100,paused:false};updated=Date.now();
  const slowPage=await context.newPage();
  slowPage.on('pageerror',e=>errors.push(e.message));
  await slowPage.goto('http://fixture.local/watch.html');
  await slowPage.evaluate(()=>{fixture.fragmentDelay=7100;fixture.setBuffer(6);});
  await slowPage.evaluate(instrumentAndJoin);
  await slowPage.waitForTimeout(15500);
  const waiting=await slowPage.evaluate(()=>({starts:fixture.starts.length,restarts:fixture.seeks.length,paused:video.paused,status:document.getElementById('room-status').textContent}));
  assert.equal(waiting.starts,1,'A six-second fragment taking 7.1 seconds does not cause repeated HLS launches');
  assert.equal(waiting.restarts,0,'Buffer readiness and polling do not force a source restart');
  assert.equal(waiting.paused,true,'Follower waits until the current host position is buffered');
  assert.match(waiting.status,/синхрониз|буфер|ожида/i,'Follower sees why playback is waiting');
  await slowPage.evaluate(()=>fixture.setBuffer(60));
  await slowPage.waitForFunction(()=>!video.paused);
  assert.equal(await slowPage.evaluate(()=>fixture.starts.length),1,'Growing the existing buffer resumes without creating another stream');
  assert.ok(Math.abs(await slowPage.evaluate(()=>PP.state().position)-(remote.position+(Date.now()-updated)/1000))<0.35,'The follower starts within 0.35 seconds of the host after sufficient data arrives');
  assert.deepEqual(errors,[]);
  console.log('Room browser checks passed: precise resume, buffered drift correction, delayed readiness, paused seek, simulated pagehide/pageshow preserving the HLS session, slow first fragment and progress recovery. Genuine bfcache eligibility was not tested.');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});

// Offline integration with the actual player and deterministic media: no torrent
// or loopback server is required to reproduce room timing.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const web=path.join(__dirname,'../web');
let remote={id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,season:1,episode:1,position:100,paused:true};
let updated=Date.now(),gets=0;
const bootstrap=`
const VV={user:null},DEBUG=false;
let currentItem={id:'tt1',kind:'tv'};
const t=x=>x,dbg=()=>{},fmtTime=x=>String(x),isHevcCodec=()=>false,isSeriesKind=()=>true;
const episodeHistoryEntry=()=>null,rememberWatchProgress=()=>{},seasonEpisodeCount=()=>3,canonicalByNumber=()=>null,titleVoices=()=>[];
const fetchFiles=async()=>[{index:0,season:1,episode:1},{index:1,season:1,episode:2}];
const Personal={get:()=>null,rank:x=>x,preferences:()=>({}),put:async()=>{}};
const video=document.getElementById('player');
let paused=true,mediaReady=0,mediaTime=0,mediaAt=performance.now();
const mediaPosition=()=>mediaTime+(paused?0:(performance.now()-mediaAt)/1000);
Object.defineProperty(video,'paused',{get:()=>paused});
Object.defineProperty(video,'readyState',{get:()=>mediaReady});
Object.defineProperty(video,'currentTime',{get:mediaPosition,set:value=>{mediaTime=value;mediaAt=performance.now();}});
Object.defineProperty(video,'buffered',{get:()=>({length:mediaReady?1:0,start:()=>0,end:()=>90})});
video.load=()=>{mediaTime=0;mediaAt=performance.now();mediaReady=0;};
video.play=async()=>{if(paused){mediaAt=performance.now();paused=false;video.dispatchEvent(new Event('play'));video.dispatchEvent(new Event('playing'));}};
video.pause=()=>{if(!paused){mediaTime=mediaPosition();paused=true;video.dispatchEvent(new Event('pause'));}};
window.fixture={starts:[],seeks:[],manifestDelay:80};
window.Hls=class {
 static Events={MANIFEST_PARSED:'manifest',SUBTITLE_TRACKS_UPDATED:'subs',LEVEL_SWITCHED:'level',FRAG_BUFFERED:'frag',ERROR:'error'};
 static isSupported(){return true;}
 constructor(){this.handlers={};this.subtitleTracks=[];}
 on(name,fn){this.handlers[name]=fn;}
 loadSource(src){fixture.starts.push(src);}
 attachMedia(){setTimeout(()=>{if(!this.dead){mediaReady=4;this.handlers.manifest?.();video.dispatchEvent(new Event('canplay'));}},fixture.manifestDelay);}
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
   if(u.pathname.startsWith('/api/rooms/')){gets++;return json({state:remote,updated,server_time:Date.now(),revision:gets});}
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
  await page.evaluate(()=>{const seek=PP.seek;PP.seek=target=>{fixture.seeks.push(target);return seek(target);};return WatchRoom.join('a'.repeat(32));});
  await page.waitForFunction(()=>PP.ready()&&video.paused);
  assert.equal(await page.evaluate(()=>PP.state().position),100);
  const beforeResume=gets;
  await page.evaluate(()=>{video.currentTime-=1;});
  remote={...remote,paused:false};updated=Date.now();
  await page.waitForFunction(()=>!video.paused&&fixture.seeks.length>0);
  const expected=remote.position+(Date.now()-updated)/1000;
  assert.ok(Math.abs(await page.evaluate(()=>PP.state().position)-expected)<0.25,'Resume must catch a one-second drift before playing');
  assert.ok(gets>beforeResume,'Follower receives host resume through polling');

  const beforeDrift=await page.evaluate(()=>fixture.seeks.length);
  await page.evaluate(()=>{video.currentTime-=1;});
  await page.waitForFunction(count=>fixture.seeks.length>count,beforeDrift);
  assert.ok(Math.abs(await page.evaluate(()=>PP.state().position)-(remote.position+(Date.now()-updated)/1000))<0.25,'One-second playing drift must not stay in the old three-second deadband');

  await page.evaluate(()=>{fixture.manifestDelay=1400;});
  remote={...remote,file:1,episode:2,position:200};updated=Date.now();
  await page.waitForFunction(()=>PP.state().file===1&&PP.ready()&&!video.paused&&fixture.starts.length===2);
  assert.ok(Math.abs(await page.evaluate(()=>PP.state().position)-(remote.position+(Date.now()-updated)/1000))<0.25,'New media catches the full time spent preparing and loading');
  remote={...remote,position:230,paused:true};updated=Date.now();
  await page.waitForFunction(()=>video.paused&&Math.abs(PP.state().position-230)<0.01);
  assert.deepEqual(errors,[]);
  console.log('Room browser checks passed: precise resume, small drift correction, delayed source readiness, exact paused seek.');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});

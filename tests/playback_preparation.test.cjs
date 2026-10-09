const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/playback-extras.js'),'utf8');
const flush=async()=>{for(let i=0;i<30;i++)await Promise.resolve();};

function setup({fetcher,files=[{index:1,season:1,episode:2}],sources=[],fileFetcher}={}) {
  const elements=new Map(),events=new Map(),requests=[],timers=new Map();let clock=0,timerID=0,follower=false;
  class Element {
    constructor(tag='div'){this.tagName=tag.toUpperCase();this.children=[];this.listeners=new Map();this.options=[];this.hidden=false;this.value='';this.textContent='';}
    set innerHTML(html){for(const m of html.matchAll(/<([\w-]+)[^>]*\bid="([^"]+)"[^>]*>/g)){const node=new Element(m[1]);node.id=m[2];elements.set(node.id,node);this.children.push(node);}}
    append(...items){this.children.push(...items);}after(){}add(){}scrollIntoView(){}
    addEventListener(name,fn){if(!this.listeners.has(name))this.listeners.set(name,[]);this.listeners.get(name).push(fn);}
    dispatch(name){const event={type:name,target:this};for(const fn of this.listeners.get(name)||[])fn(event);this['on'+name]?.(event);}
  }
  const video=new Element('video'),wrap=new Element();video.paused=true;video.textTracks=[];video.textTracks.addEventListener=()=>{};video.play=async()=>{};
  elements.set('player',video);elements.set('player-wrap',wrap);
  const document={createElement:tag=>new Element(tag),getElementById:id=>elements.get(id),head:new Element('head'),body:{classList:{contains:()=>follower}}};
  const state={id:'tt1',magnet:'magnet:first',file:0,season:1,episode:1,active:true,playing:true,position:5,duration:1200,voice:'Original'};
  const emit=(type,detail)=>{for(const fn of events.get(type)||[])fn({type,detail});};
  const window={addEventListener(name,fn){if(!events.has(name))events.set(name,[]);events.get(name).push(fn);},dispatchEvent:event=>emit(event.type,event.detail)};
  const Personal={get:()=>null,put:async()=>{},rank:items=>items};
  const PP={available:true,state:()=>({...state}),files:()=>files,release:()=>'Current source',playing:()=>state.active,isFollower:()=>follower,selectSubtitle(){},start(){}};
  class Clock extends Date {static now(){return clock;}}
  const context=vm.createContext({document,window,PP,Personal,VV:{user:null},Date:Clock,URLSearchParams,AbortController,CustomEvent,Event,queueMicrotask,
    seasonEpisodeCount:()=>3,fetchFiles:fileFetcher||(async()=>[]),
    setTimeout(fn,ms){const id=++timerID;timers.set(id,{fn,at:clock+ms});return id;},clearTimeout:id=>timers.delete(id),setInterval:()=>0,
    fetch:async(url,options={})=>{const parsed=new URL(url,'http://local');const request={url:parsed,options};requests.push(request);
      const result=fetcher?await fetcher(request):undefined;
      return result||{ok:true,status:200,json:async()=>parsed.pathname.endsWith('/sources')?{items:sources}:{complete:false,downloaded:20,total:100}};}});
  vm.runInContext(source+'\nthis.extras=PlaybackExtras;',context);
  return {state,video,emit,requests,el:id=>elements.get(id),next:()=>context.extras.next(),follower(value){follower=value;emit('playbackrole');},
    preference(value){elements.get('prepare-next').checked=value;elements.get('prepare-next').dispatch('change');},
    async advance(ms){clock+=ms;for(const [id,timer]of [...timers])if(timer.at<=clock){timers.delete(id);timer.fn();}await flush();}};
}
const ok=data=>({ok:true,status:200,json:async()=>data});
const preparations=h=>h.requests.filter(r=>r.url.pathname.endsWith('/prepare'));

test('next episode waits for the current complete file and begins from its actual index even while paused',async()=>{
  let complete=false,finish;
  const h=setup({fetcher:async request=>{
    if(request.url.pathname.endsWith('/download-status'))return ok({complete:request.url.searchParams.get('file')==='0'?complete:true,downloaded:20,total:100});
    if(request.url.pathname.endsWith('/prepare'))return new Promise(resolve=>{finish=()=>resolve(ok({download_complete:true,analysis_status:'ready'}));});
  }});
  h.emit('playbackstart');await flush();h.emit('playbackchange');h.video.dispatch('timeupdate');await flush();
  assert.equal(preparations(h).length,0);assert.match(h.el('prepare-status').textContent,/20%/);
  complete=true;await h.advance(3000);
  assert.equal(preparations(h).length,1);assert.equal(h.next().file,1);
  const query=preparations(h)[0].url.searchParams;
  assert.equal(query.get('id'),'tt1');assert.equal(query.get('episode'),'2');assert.equal(query.get('previous_file'),'0');assert.equal(query.get('previous_episode'),'1');
  assert.match(h.el('prepare-analysis-status').textContent,/Распознаём/);
  // The old remaining-playback-time threshold is irrelevant to the pipeline.
  assert.equal(h.state.position,5);h.emit('playbackchange');await flush();assert.equal(preparations(h).length,1);
  finish();await flush();assert.match(h.el('prepare-analysis-status').textContent,/сохранены/);
  h.emit('playbackchange');await flush();assert.equal(preparations(h).length,1);h.emit('pagehide');
});

test('source, stop, preference and room-role changes abort current preparation without stale next-source leakage',async()=>{
  const h=setup({fetcher:async request=>{
    if(request.url.pathname.endsWith('/download-status'))return ok({complete:true});
    if(request.url.pathname.endsWith('/prepare'))return new Promise(()=>{});
  }});
  h.emit('playbackstart');await flush();assert.ok(h.next());const first=preparations(h)[0];
  h.state.magnet='magnet:other';h.state.file=4;h.emit('playbackchange');await flush();
  assert.equal(first.options.signal.aborted,true);assert.equal(h.next().magnet,'magnet:other');
  const second=preparations(h)[1];h.preference(false);await flush();assert.equal(second.options.signal.aborted,true);assert.equal(h.next(),null);
  h.preference(true);await flush();const third=preparations(h)[2];h.follower(true);await flush();assert.equal(third.options.signal.aborted,true);assert.equal(h.next(),null);
  h.follower(false);await flush();const fourth=preparations(h)[3];h.emit('playbackstop');assert.equal(fourth.options.signal.aborted,true);
  h.video.dispatch('timeupdate');await flush();assert.equal(preparations(h).length,4);h.emit('pagehide');
});

test('analysis failure keeps the downloaded exact next episode reusable and probes at most three ranked sources',async()=>{
  let probes=0;
  const h=setup({files:[],sources:[{magnet:'magnet:a',title:'A'},{magnet:'magnet:b',title:'B'},{magnet:'magnet:c',title:'C'},{magnet:'magnet:d',title:'D'}],
    fileFetcher:async(id,magnet)=>{probes++;if(magnet==='magnet:a')throw new Error('source unavailable');return magnet==='magnet:c'?[{index:7,season:1,episode:2}]:[{index:3,season:1,episode:3}];},
    fetcher:async request=>request.url.pathname.endsWith('/download-status')?ok({complete:true}):request.url.pathname.endsWith('/prepare')?ok({download_complete:true,analysis_status:'unavailable'}):undefined});
  h.emit('playbackstart');await flush();assert.equal(probes,3);assert.equal(preparations(h).length,1);
  assert.equal(h.next().magnet,'magnet:c');assert.equal(h.next().file,7);assert.equal(h.next().ep,2);
  assert.match(h.el('prepare-analysis-status').textContent,/пока не удалось/);h.emit('pagehide');
});

test('a stale completion cannot prepare a different active episode',async()=>{
  let release;
  const h=setup({fetcher:async request=>request.url.pathname.endsWith('/download-status')?new Promise(resolve=>{release=()=>resolve(ok({complete:true}));}):undefined});
  h.emit('playbackstart');await flush();h.state.episode=2;h.emit('playbackstop');release();await flush();
  assert.equal(preparations(h).length,0);assert.equal(h.next(),null);h.emit('pagehide');
});

test('a busy analyzer retries the same downloaded next file without overlapping or selecting another source',async()=>{
  let probes=0,attempts=0,finish;
  const h=setup({files:[],sources:[{magnet:'magnet:next',title:'Next'}],
    fileFetcher:async()=>{probes++;return [{index:7,season:1,episode:2}];},
    fetcher:async request=>{
      if(request.url.pathname.endsWith('/download-status'))return ok({complete:true});
      if(request.url.pathname.endsWith('/prepare')){
        attempts++;
        if(attempts===1)return ok({download_complete:true,analysis_status:'unavailable'});
        return new Promise(resolve=>{finish=()=>resolve(ok({download_complete:true,analysis_status:'ready'}));});
      }
    }});
  h.emit('playbackstart');await flush();assert.equal(attempts,1);const ready=h.next();
  assert.equal(ready.magnet,'magnet:next');assert.match(h.el('prepare-analysis-status').textContent,/30 секунд/);
  h.video.dispatch('timeupdate');await h.advance(29999);assert.equal(attempts,1);
  await h.advance(1);assert.equal(attempts,2);assert.equal(h.next(),ready);assert.equal(probes,1);
  assert.equal(preparations(h)[0].url.search,preparations(h)[1].url.search);
  assert.match(h.el('prepare-status').textContent,/подготовлено/);
  h.emit('playbackchange');await h.advance(30000);assert.equal(attempts,2,'No retry may overlap an active analysis request');
  finish();await flush();assert.match(h.el('prepare-analysis-status').textContent,/сохранены/);
  await h.advance(90000);assert.equal(attempts,2);assert.equal(h.next(),ready);h.emit('pagehide');
});

test('analysis retries stop after three attempts and cancellation clears a scheduled retry',async()=>{
  const fixture=()=>setup({fetcher:async request=>request.url.pathname.endsWith('/download-status')?ok({complete:true}):
    request.url.pathname.endsWith('/prepare')?ok({download_complete:true,analysis_status:'unavailable'}):undefined});
  const h=fixture();h.emit('playbackstart');await flush();const ready=h.next();
  await h.advance(30000);await h.advance(30000);assert.equal(preparations(h).length,3);assert.equal(h.next(),ready);
  await h.advance(120000);h.emit('playbackchange');await flush();assert.equal(preparations(h).length,3);
  assert.doesNotMatch(h.el('prepare-analysis-status').textContent,/Повторим/);h.emit('pagehide');
  const canceled=fixture();canceled.emit('playbackstart');await flush();canceled.preference(false);
  await canceled.advance(90000);assert.equal(preparations(canceled).length,1);assert.equal(canceled.next(),null);canceled.emit('pagehide');
});

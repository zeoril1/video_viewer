const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(require('node:path').join(__dirname,'../web/watch-room.js'),'utf8');

function harness({position=100,paused=true,ready=true,clockSkew=0}={}){
  let now=0, localPosition=position, positionAt=0, localPaused=paused, playerReady=ready, bufferStart=0, bufferEnd=Infinity, follower=false;
  let state={id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,season:1,episode:1,position,paused};
  let localState={...state};
  const elements=new Map(),timers=new Map(),requests=[],seeks=[],starts=[],restarts=[];
  let timerID=0, responder=async()=>({state:{...state},updated:100000+now,server_time:100000+now});
  function target(){return localPosition+(localPaused||!playerReady?0:(now-positionAt)/1000);}
  function setPosition(value){localPosition=value;positionAt=now;}
  function node(){
    const listeners=new Map();
    return {hidden:false,disabled:false,textContent:'',value:'',classList:{toggle(){}},
      set id(value){elements.set(value,this);},replaceChildren(){},append(){},after(){},focus(){},select(){},
      addEventListener(name,fn){if(!listeners.has(name))listeners.set(name,[]);listeners.get(name).push(fn);},
      emit(name,properties={}){return (listeners.get(name)||[]).map(fn=>fn({stopImmediatePropagation(){},...properties}));}};
  }
  const video=node();
  Object.defineProperty(video,'paused',{get:()=>localPaused});
  video.play=async()=>{if(localPaused){positionAt=now;localPaused=false;video.emit('play');video.emit('playing');}};
  video.pause=()=>{if(!localPaused){setPosition(target());localPaused=true;video.emit('pause');}};
  const win=node();
  const context={
    PP:{available:true,state:()=>({...localState,position:target()}),release:()=>'',ready:()=>playerReady,playing:()=>true,
      duration:()=>0,canSeekBuffered:(value,ahead=0)=>value>=bufferStart&&value+ahead<=bufferEnd,
      seekBuffered(value){if(value<bufferStart||value>bufferEnd)return false;seeks.push(value);setPosition(value);return true;},
      setFollower(value){follower=value;},setRemotePaused(value){if(value)video.pause();else if(follower&&playerReady&&localPaused)video.play();},
      seek(value){restarts.push(value);setPosition(value);playerReady=false;bufferStart=value;bufferEnd=value;},
      start(options){starts.push(options);localState={...localState,...options,episode:options.ep};setPosition(options.pos);localPaused=options.paused;playerReady=false;}},
    document:{body:{classList:{toggle(){}}},createElement:node,getElementById(id){if(id==='player')return video;if(!elements.has(id))elements.set(id,node());return elements.get(id);}},
    window:win,location:{href:'http://fixture/watch.html',origin:'http://fixture'},history:{state:{},replaceState(){}},
    crypto:{getRandomValues(bytes){return bytes.fill(1);}},navigator:{clipboard:{writeText:async()=>{}}},
    performance:{now:()=>now},Date:{now:()=>now+clockSkew},AbortSignal:{timeout(){}},
    setTimeout(fn,delay){const id=++timerID;timers.set(id,{fn,delay});return id;},clearTimeout(id){timers.delete(id);},
    fetch:async(url,options)=>{requests.push({url,...options});const data=await responder(url,options);return {ok:!data.httpStatus,status:data.httpStatus||200,text:async()=>data.message||'',json:async()=>data};},
    URL,URLSearchParams,PlaybackExtras:{loading(){}},currentItem:null,
  };
  vm.createContext(context);vm.runInContext(source+'\nthis.WatchRoom=WatchRoom;',context);
  return {context,video,seeks,starts,restarts,requests,elements,
    advance(ms){now+=ms;},setLocal(value){setPosition(value);},setReady(value){setPosition(target());playerReady=value;},
    respond(fn){responder=fn;},remote(value){state={...state,...value};},
    buffer(start,end){bufferStart=start;bufferEnd=end;},
    async pageEvent(name,properties={}){await Promise.all(win.emit(name,properties));for(let i=0;i<10;i++)await Promise.resolve();},
    timerCount:()=>timers.size,
    isFollower:()=>follower,
    async join(){await context.WatchRoom.join('a'.repeat(32));},
    async poll(){const first=timers.entries().next().value;assert.ok(first,'Room polling must stay scheduled');timers.delete(first[0]);await first[1].fn();},
    nextDelay(){return timers.values().next().value?.delay;},position:target,
  };
}

test('resume catches a one-second polling lag immediately, despite the correction cooldown',async()=>{
  const h=harness();await h.join();
  // A host seek immediately precedes resume; ordinary correction cooldown must
  // not retain the next one-second lag after the remote pause is released.
  h.remote({position:110});await h.poll();assert.equal(h.seeks.at(-1),110);
  h.advance(1000);h.remote({position:111,paused:false});await h.poll();
  assert.equal(h.seeks.at(-1),111);assert.equal(h.position(),111);assert.equal(h.video.paused,false);
  assert.equal(h.requests.every(r=>r.method==='GET'),true,'Follower corrections must not publish host state');
});

test('a new source catches time spent waiting for media readiness without another network poll',async()=>{
  const h=harness({ready:false,position:0,paused:true});
  h.remote({id:'tt2',position:100,paused:false});await h.join();
  assert.equal(h.starts.length,1);assert.equal(h.starts[0].pos,100);
  h.advance(2400);h.setReady(true);h.video.emit('canplay');
  assert.equal(h.seeks.at(-1),102.4);assert.equal(h.video.paused,false);
  assert.equal(h.requests.length,1);
});

test('room timing uses server age, half of request RTT and monotonic elapsed time despite wall-clock skew',async()=>{
  const h=harness({position:0,clockSkew:-86400000});
  h.respond(async()=>{h.advance(600);return {state:{id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,position:100,paused:false},updated:100000,server_time:102000};});
  await h.join();assert.equal(h.seeks.at(-1),102.3);
  h.advance(900);h.setLocal(102.3);h.video.emit('canplay');
  // Readiness events do not force a second seek during ordinary playback.
  assert.equal(h.seeks.length,1);
  h.advance(1200);await h.poll();assert.ok(Math.abs(h.seeks.at(-1)-102.3)<0.001);
});

test('paused samples preserve the exact host position, including after delayed startup',async()=>{
  const h=harness({position:0,ready:false});
  h.respond(async()=>{h.advance(600);return {state:{id:'tt2',magnet:'magnet:?xt=urn:btih:123',file:0,position:100,paused:true},updated:100000,server_time:102000};});
  await h.join();assert.equal(h.starts[0].pos,100);
  h.advance(2500);h.setLocal(99);h.setReady(true);h.video.emit('loadeddata');
  assert.equal(h.seeks.at(-1),100);assert.equal(h.video.paused,true);
});

test('playing drift of one second is corrected while minor jitter stays within the tolerance',async()=>{
  const h=harness({paused:false});await h.join();
  h.advance(1000);h.remote({position:101.2});await h.poll();assert.equal(h.seeks.length,0);
  h.advance(1000);h.remote({position:103});await h.poll();assert.equal(h.seeks.at(-1),103);
});

test('host changes made during an in-flight PUT are published immediately after it completes',async()=>{
  const h=harness({paused:false});let finish;
  h.respond(async(url,options)=>options.method==='POST'?{room:'b'.repeat(32),host_token:'host'}:new Promise(resolve=>{finish=resolve;}));
  const creating=h.elements.get('room-create').onclick();
  // Creation resumes into the initial PUT on the next microtask.
  for(let i=0;i<10&&!finish;i++)await Promise.resolve();
  assert.ok(finish);h.video.pause();
  finish({participants:[]});await creating;
  assert.equal(h.nextDelay(),0);
  h.respond(async()=>({participants:[]}));await h.poll();
  const published=JSON.parse(h.requests.at(-1).body);
  assert.equal(published.paused,true);assert.equal(h.requests.at(-1).method,'PUT');
});

test('lost room connectivity discards the old playing sample until a fresh host response arrives',async()=>{
  const h=harness({paused:false});await h.join();
  h.respond(async()=>{throw new Error('offline');});await h.poll();
  assert.equal(h.video.paused,true);
  h.video.emit('canplay');h.video.emit('click');assert.equal(h.video.paused,true);
  h.respond(async()=>({state:{id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,position:105,paused:false},updated:100000,server_time:100000}));
  await h.poll();assert.equal(h.seeks.at(-1),105);assert.equal(h.video.paused,false);
});

test('a host sample that expires while media loads cannot restart the follower',async()=>{
  const h=harness({ready:false});
  h.respond(async()=>({state:{id:'tt2',magnet:'magnet:?xt=urn:btih:123',file:0,position:100,paused:false},updated:100000,server_time:111000}));
  await h.join();h.advance(2000);h.setReady(true);h.video.emit('canplay');
  assert.equal(h.video.paused,true);assert.equal(h.seeks.length,0);
  h.video.emit('click');assert.equal(h.video.paused,true);
});

test('slow first media keeps downloaded fragments and waits for the host position in its buffer',async()=>{
  const h=harness({ready:false,position:0});
  h.remote({id:'tt2',position:100,paused:false});await h.join();
  assert.equal(h.starts[0].paused,true,'metadata cannot autoplay before synchronization');
  h.advance(7100);h.buffer(100,106);h.setReady(true);
  for(const event of ['loadeddata','canplay','playing'])h.video.emit(event);
  assert.equal(h.video.paused,true);assert.equal(h.restarts.length,0);
  assert.match(h.elements.get('room-status').textContent,/буфер/);
  h.advance(8400);h.remote({position:115.5});await h.poll();
  assert.equal(h.restarts.length,0,'a new server sample is not another startup command');
  assert.equal(h.requests.at(-1).headers['X-Room-Waiting'],'1');
  h.buffer(100,160);h.video.emit('progress');
  assert.equal(h.position(),115.5);assert.equal(h.video.paused,false);
  assert.equal(h.starts.length,1);assert.equal(h.restarts.length,0);
  await h.poll();assert.equal(h.requests.at(-1).headers['X-Room-Waiting'],'0');
});

test('a chronically slow stream permits only one timed catch-up restart for the same host command',async()=>{
  const h=harness({ready:false,position:0});
  h.remote({id:'tt2',position:100,paused:false});await h.join();
  h.advance(7100);h.buffer(100,106);h.setReady(true);h.video.emit('canplay');
  h.advance(23000);h.remote({position:130.1});await h.poll();
  assert.equal(h.restarts.length,1);
  assert.ok(Math.abs(h.restarts[0]-137.2)<0.001,'restart includes the observed startup delay');
  h.advance(20000);h.remote({position:150.1});await h.poll();
  h.buffer(137.2,143.2);h.setReady(true);h.video.emit('canplay');
  for(let i=0;i<4;i++){
    h.advance(30000);h.remote({position:180.1+30*i});await h.poll();
    h.video.emit('canplay');h.video.emit('progress');
  }
  assert.equal(h.restarts.length,1);assert.equal(h.video.paused,true);
  assert.equal(h.elements.get('room-retry').hidden,false);
  assert.match(h.elements.get('room-status').textContent,/слишком медленно/);
  h.remote({position:10,paused:true});await h.poll();
  assert.equal(h.restarts.length,2,'an actual host seek starts a new command');
  assert.equal(h.restarts[1],10);
  h.buffer(10,20);h.setReady(true);h.video.emit('canplay');
  assert.equal(h.position(),10);assert.equal(h.video.paused,true);
});

test('fresh buffer headroom is required before releasing a waiting guest',async()=>{
  const h=harness({position:99});h.remote({position:100,paused:false});h.buffer(90,101);
  await h.join();assert.equal(h.video.paused,true);assert.equal(h.seeks.length,0);
  h.buffer(90,102);h.video.emit('progress');
  assert.equal(h.position(),100);assert.equal(h.video.paused,false);
});

test('variable response latency cannot reset the catch-up budget for an unchanged host timeline',async()=>{
  const h=harness({ready:false,position:0});let latency=0;
  h.respond(async()=>{
    const published=h.context.performance.now();
    h.advance(latency);
    return {state:{id:'tt2',magnet:'magnet:?xt=urn:btih:123',file:0,
      position:100+published/1000,paused:false},updated:100000+published,server_time:100000+published};
  });
  await h.join();h.advance(7100);h.buffer(100,106);h.setReady(true);h.video.emit('canplay');
  h.advance(23000);await h.poll();assert.equal(h.restarts.length,1);
  h.buffer(h.restarts[0],h.restarts[0]+6);h.setReady(true);
  for(const delay of [6000,100,6000,100]){
    latency=delay;h.advance(30000);await h.poll();h.video.emit('canplay');
  }
  assert.equal(h.restarts.length,1,'GET latency is not a host seek command');
  assert.equal(h.video.paused,true);
  assert.equal(h.elements.get('room-retry').hidden,false);
});

test('cached-page restoration gets a fresh sample and creates one polling loop',async()=>{
  const h=harness({paused:false});await h.join();
  await h.pageEvent('pagehide',{persisted:true});assert.equal(h.timerCount(),0);assert.equal(h.video.paused,true);
  h.remote({position:120,paused:false});await h.pageEvent('pageshow',{persisted:true});
  assert.equal(h.position(),120);assert.equal(h.video.paused,false);assert.equal(h.timerCount(),1);
  const count=h.requests.length;
  await h.pageEvent('pageshow',{persisted:true});assert.equal(h.requests.length,count);assert.equal(h.timerCount(),1);
});

test('a response started before pagehide cannot replace restored room state or add another timer',async()=>{
  const h=harness();await h.join();let finish,gets=0;
  h.respond(async(url,options)=>{
    if(options.method==='POST')return {};
    if(++gets===1)return new Promise(resolve=>{finish=resolve;});
    return {state:{id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,position:140,paused:false},updated:100000,server_time:100000};
  });
  const stale=h.poll();for(let i=0;i<10&&!finish;i++)await Promise.resolve();assert.ok(finish);
  await h.pageEvent('pagehide',{persisted:true});await h.pageEvent('pageshow',{persisted:true});
  finish({state:{id:'obsolete',magnet:'old',file:9,position:0,paused:true},updated:100000,server_time:100000});await stale;
  assert.equal(h.context.PP.state().id,'tt1');assert.equal(h.position(),140);assert.equal(h.timerCount(),1);
});

test('restoring a closed room removes guest restrictions and does not resume old media',async()=>{
  const h=harness({paused:false});await h.join();await h.pageEvent('pagehide',{persisted:true});
  h.respond(async()=>({httpStatus:404,message:'Комната закрыта.'}));await h.pageEvent('pageshow',{persisted:true});
  assert.equal(h.isFollower(),false);assert.equal(h.timerCount(),0);assert.equal(h.video.paused,true);
});

test('a restored page without a room can still join later',async()=>{
  const h=harness();await h.pageEvent('pagehide',{persisted:true});await h.pageEvent('pageshow',{persisted:true});
  await h.join();assert.equal(h.timerCount(),1);assert.equal(h.requests.filter(r=>r.method==='GET').length,1);
});

test('host sees buffering guests and can pause everyone without changing guest permissions',async()=>{
  const h=harness({paused:false});
  h.respond(async(url,options)=>options.method==='POST'?{room:'b'.repeat(32),host_token:'host'}:{participants:[{kind:'user',name:'Участник',waiting:true}]});
  await h.elements.get('room-create').onclick();
  assert.match(h.elements.get('room-participant-count').textContent,/Загружаются: 1/);
  assert.equal(h.elements.get('room-pause-all').hidden,false);
  h.elements.get('room-pause-all').onclick();for(let i=0;i<10;i++)await Promise.resolve();
  assert.equal(JSON.parse(h.requests.at(-1).body).paused,true);assert.equal(h.isFollower(),false);
});

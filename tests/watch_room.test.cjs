const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(require('node:path').join(__dirname,'../web/watch-room.js'),'utf8');

function harness({position=100,paused=true,ready=true,clockSkew=0}={}){
  let now=0, localPosition=position, positionAt=0, localPaused=paused, playerReady=ready;
  let state={id:'tt1',magnet:'magnet:?xt=urn:btih:123',file:0,season:1,episode:1,position,paused};
  let localState={...state};
  const elements=new Map(),timers=new Map(),requests=[],seeks=[],starts=[];
  let timerID=0, responder=async()=>({state:{...state},updated:100000,server_time:100000});
  function target(){return localPosition+(localPaused||!playerReady?0:(now-positionAt)/1000);}
  function setPosition(value){localPosition=value;positionAt=now;}
  function node(){
    const listeners=new Map();
    return {hidden:false,disabled:false,textContent:'',value:'',classList:{toggle(){}},
      replaceChildren(){},append(){},focus(){},select(){},
      addEventListener(name,fn){if(!listeners.has(name))listeners.set(name,[]);listeners.get(name).push(fn);},
      emit(name){for(const fn of listeners.get(name)||[])fn({stopImmediatePropagation(){}});}};
  }
  const video=node();
  Object.defineProperty(video,'paused',{get:()=>localPaused});
  video.play=async()=>{if(localPaused){positionAt=now;localPaused=false;video.emit('play');video.emit('playing');}};
  video.pause=()=>{if(!localPaused){setPosition(target());localPaused=true;video.emit('pause');}};
  const win=node();
  const context={
    PP:{available:true,state:()=>({...localState,position:target()}),release:()=>'',ready:()=>playerReady,playing:()=>true,
      setFollower(){},setRemotePaused(value){if(value)video.pause();else if(playerReady&&localPaused)video.play();},
      seek(value){seeks.push(value);setPosition(value);},
      start(options){starts.push(options);localState={...localState,...options,episode:options.ep};setPosition(options.pos);localPaused=options.paused;playerReady=false;}},
    document:{body:{classList:{toggle(){}}},createElement:node,getElementById(id){if(id==='player')return video;if(!elements.has(id))elements.set(id,node());return elements.get(id);}},
    window:win,location:{href:'http://fixture/watch.html',origin:'http://fixture'},history:{state:{},replaceState(){}},
    crypto:{getRandomValues(bytes){return bytes.fill(1);}},navigator:{clipboard:{writeText:async()=>{}}},
    performance:{now:()=>now},Date:{now:()=>now+clockSkew},AbortSignal:{timeout(){}},
    setTimeout(fn,delay){const id=++timerID;timers.set(id,{fn,delay});return id;},clearTimeout(id){timers.delete(id);},
    fetch:async(url,options)=>{requests.push({url,...options});const data=await responder(url,options);return {ok:true,status:200,json:async()=>data};},
    URL,URLSearchParams,PlaybackExtras:{loading(){}},currentItem:null,
  };
  vm.createContext(context);vm.runInContext(source+'\nthis.WatchRoom=WatchRoom;',context);
  return {context,video,seeks,starts,requests,elements,
    advance(ms){now+=ms;},setLocal(value){setPosition(value);},setReady(value){setPosition(target());playerReady=value;},
    respond(fn){responder=fn;},remote(value){state={...state,...value};},
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

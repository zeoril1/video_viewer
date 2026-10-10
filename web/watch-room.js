'use strict';

const WatchRoom = (() => {
  if (!PP.available) return {join:async()=>{}};
  const video=document.getElementById('player');
  const el=id=>document.getElementById(id);
  let room='', token='', timer=null, busy=false, generation=0, hostPaused=false, pendingPaused=false;
  let lastCorrection=-Infinity, remoteSample=null, applying=false, hostUpdatePending=false;
  let synchronization=null, waiting=false, suspended=false, retryRequested=false;
  const pollInterval=1000, correctionInterval=2000, driftTolerance=0.35;
  const bufferWaitTimeout=30000, minimumForward=1.5, maxSyncRestarts=1;
  const retry=document.createElement('button'), pauseAll=document.createElement('button');
  retry.id='room-retry';retry.type='button';retry.hidden=true;retry.textContent='Повторить подключение';
  pauseAll.id='room-pause-all';pauseAll.type='button';pauseAll.hidden=true;pauseAll.textContent='Поставить всех на паузу';
  el('room-status').after(retry,pauseAll);
  const memberID=Array.from(crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,'0')).join('');
  function renderParticipants(items) {
    el('room-participants').hidden=!token;
    const buffering=items.filter(item=>item.waiting).length;
    el('room-participant-count').textContent='Подключены: '+items.length+(buffering?' · Загружаются: '+buffering:'');
    pauseAll.hidden=!token||!buffering||hostPaused;
    el('room-participant-list').replaceChildren();
    for(const item of items){const li=document.createElement('li');li.textContent=(item.kind==='user'?item.name:'Гость — '+item.name)+(item.waiting?' — ждёт загрузки':'');el('room-participant-list').append(li);}
  }
  function leavePresence(){
    if(room&&!token)fetch('/api/rooms/'+encodeURIComponent(room)+'/leave',{method:'POST',headers:{'X-Room-Member':memberID},keepalive:true}).catch(()=>{});
  }
  const status=message=>{el('room-status').textContent=message;};
  async function request(method,body) {
    const response=await fetch('/api/rooms'+(room?'/'+encodeURIComponent(room):''),{
      method,headers:{'Content-Type':'application/json','X-Room-Member':memberID,'X-Room-Waiting':waiting?'1':'0',...(token?{'X-Room-Host':token}:{})},
      ...(body?{body:JSON.stringify(body)}:{}),signal:AbortSignal.timeout(8000)
    });
    if (!response.ok) {const error=new Error((await response.text()).slice(0,250));error.status=response.status;throw error;}
    return response.status===204?null:response.json();
  }
  function snapshot() {
    const s=PP.state();
    return {id:s.id,magnet:s.magnet,file:s.file,season:s.season,episode:s.episode,release:PP.release(),position:s.position,paused:hostPaused};
  }
  function controls() {
    el('room-create').hidden=!!room;
    el('room-leave').hidden=!room;
    el('room-leave').textContent=token?'Закрыть комнату':'Выйти из комнаты';
    el('room-invite').hidden=el('room-copy').hidden=!token;
    document.body.classList.toggle('room-guest',!!room&&!token);
    PP.setFollower(!!room&&!token);
    retry.hidden=true;pauseAll.hidden=true;
    if(!token)renderParticipants([]);
  }
  function stopRoom(message) {
    generation++;clearTimeout(timer);timer=null;room='';token='';busy=false;
    remoteSample=null;synchronization=null;waiting=false;pendingPaused=false;retryRequested=false;hostUpdatePending=false;lastCorrection=-Infinity;
    controls();PP.setRemotePaused(false);PlaybackExtras.loading('');status(message);
    const url=new URL(location.href);url.searchParams.delete('room');
    const saved=Object.assign({},history.state);
    if(saved.playback){const playback=new URLSearchParams(saved.playback);playback.delete('room');saved.playback=playback.toString();}
    history.replaceState(saved,'',url.pathname+url.search);
  }
  function waitForHost(message) {
    remoteSample=null;waiting=true;pendingPaused=true;retry.hidden=true;PP.setRemotePaused(true,message);status(message);
  }
  const sourceKey=s=>[s.id,s.magnet,s.file,s.season||0,s.episode||0].join('|');
  function resetSynchronization(s, kind='source') {
    const now=performance.now();
    synchronization={key:sourceKey(s),kind,state:'loading',started:now,loadingAt:now,loadDuration:0,waitSince:now,restarts:0};
  }
  function waitForBuffer(message) {
    waiting=true;pendingPaused=true;
    synchronization.state=PP.ready()?'buffering':'loading';
    retry.hidden=performance.now()-synchronization.waitSince<bufferWaitTimeout;
    PP.setRemotePaused(true,message);status(message);
  }
  function applyRemoteState() {
    if(!room||token||suspended||!remoteSample||applying)return;
    const sample=remoteSample, s=sample.state, now=performance.now();
    const elapsed=Math.max(0,(now-sample.received)/1000);
    if(sample.age+elapsed>12){
      waitForHost('Связь с ведущим потеряна. Ожидаем его возвращения…');return;
    }
    const local=PP.state();
    if(!synchronization||synchronization.key!==sourceKey(s))resetSynchronization(s);
    if(local.id!==s.id||local.magnet!==s.magnet||local.file!==s.file||!PP.ready()){
      sample.force=true;waitForBuffer('Синхронизируем просмотр. Ждём готовности видео…');return;
    }
    applying=true;
    try {
      // Keep a server-relative sample on a monotonic clock. It continues to
      // advance while a new source is loading, independently of clock skew.
      if(synchronization.state==='loading')synchronization.loadDuration=Math.max(0,(now-synchronization.loadingAt)/1000);
      const duration=PP.duration(), expected=sample.position+(s.paused?0:elapsed);
      const target=duration>0?Math.min(expected,Math.max(0,duration-0.01)):expected, drift=Math.abs(local.position-target);
      const force=sample.force;
      const correction=drift>(force||s.paused?0.1:driftTolerance);
      const ahead=s.paused?0:Math.min(minimumForward,duration>0?Math.max(0,duration-target-0.01):minimumForward);
      if((correction||waiting||synchronization.state==='loading')&&!PP.canSeekBuffered(target,ahead)){
        const expired=now-synchronization.waitSince>=bufferWaitTimeout;
        waitForBuffer(expired?'Видео загружается слишком медленно. Повторите подключение или попросите ведущего поставить просмотр на паузу.':'Синхронизируем просмотр. Накапливаем видео в буфере…');
        // Readiness events cannot reopen FFmpeg. Only a host seek or one timed
        // catch-up attempt may replace this command's stream.
        if(synchronization.restarts<maxSyncRestarts&&(synchronization.kind==='seek'||expired)){
          const lead=s.paused?0:Math.min(20,Math.max(2,synchronization.loadDuration));
          const position=duration>0?Math.min(target+lead,Math.max(0,duration-1)):target+lead;
          synchronization.restarts++;
          synchronization.loadingAt=now;synchronization.waitSince=now;synchronization.state='loading';
          sample.force=true;retry.hidden=true;
          PP.seek(position);lastCorrection=now;
        }
        return;
      }
      if(correction&&(force||s.paused||now-lastCorrection>=correctionInterval)){
        if(!PP.seekBuffered(target)){waitForBuffer('Синхронизируем просмотр. Ждём данные…');return;}
        lastCorrection=now;
      }
      // Seek before releasing a remote pause: merely pressing play leaves the
      // full polling delay behind the host when the difference is small.
      sample.force=false;waiting=false;pendingPaused=s.paused;retry.hidden=true;
      synchronization.state=s.paused?'paused':'playing';synchronization.waitSince=now;
      status(s.paused?'Ведущий поставил просмотр на паузу.':'Вы смотрите вместе. Воспроизведением управляет ведущий.');
      PP.setRemotePaused(s.paused);
      if(PP.ready()){
        if(s.paused)video.pause();
        else if(video.paused)video.play().catch(()=>status('Нажмите ▶ в плеере, чтобы разрешить воспроизведение.'));
      }
    } finally {applying=false;}
  }
  async function tick() {
    if (!room || busy || suspended) return;
    clearTimeout(timer);timer=null;
    busy=true;const gen=generation, started=performance.now();
    if(token)hostUpdatePending=false;
    try {
      const data=await request(token?'PUT':'GET',token?snapshot():undefined);
      if (gen!==generation) return;
      if (!token) {
        const s=data.state, local=PP.state();
        const age=Math.max(0,(data.server_time-data.updated)/1000);
        if (age>12) {waitForHost('Связь с ведущим потеряна. Ожидаем его возвращения…');return;}
        const received=performance.now(), previous=remoteSample, changedPause=previous&&previous.state.paused!==s.paused;
        const target=s.position+(s.paused?0:age+(received-started)/2000);
        // Detect host seeks on the server's publication timeline. Variable GET
        // latency must not manufacture commands and reset the restart budget.
        const previousTarget=previous?previous.state.position+(previous.state.paused?0:Math.max(0,(data.updated-previous.updated)/1000)):s.position;
        const hostSeek=previous&&sourceKey(previous.state)===sourceKey(s)&&Math.abs(s.position-previousTarget)>1.5;
        if(!synchronization||synchronization.key!==sourceKey(s)||hostSeek||changedPause||retryRequested)resetSynchronization(s,hostSeek?'seek':'source');
        remoteSample={state:s,position:target,received,age,updated:data.updated,force:!remoteSample||remoteSample.force||!!changedPause};
        if(hostSeek)remoteSample.force=true;
        if (local.id!==s.id || local.magnet!==s.magnet || local.file!==s.file||retryRequested) {
          retryRequested=false;waiting=true;pendingPaused=true;
          currentItem={id:s.id,kind:s.season?'tv':'movie'};
          PP.start({...s,pos:target,ep:s.episode,paused:true});
          remoteSample.force=true;
          const title=document.getElementById('watch-title');if(title)title.textContent='Совместный просмотр — '+s.id;
          const link=document.getElementById('film-link');if(link)link.href='/film.html?id='+encodeURIComponent(s.id);
        }
        applyRemoteState();
      } else {
        status('Вы — ведущий. Пауза, перемотка и смена серии передаются участникам.');
        renderParticipants(data.participants||[]);
      }
    } catch(error) {
      if(gen!==generation)return;
      if(error.status===404 || error.status===401 || error.status===403){if(!token)video.pause();stopRoom(error.message);return;}
      if(!token)waitForHost('Нет связи с комнатой. Повторяем подключение…');
      else status('Нет связи с комнатой. Повторяем подключение…');
    } finally {
      if(gen===generation){busy=false;if(room&&!suspended)timer=setTimeout(tick,hostUpdatePending?0:pollInterval);}
    }
  }
  async function join(id) {
    if(!/^[a-f0-9]{32}$/.test(id)){status('Некорректная ссылка комнаты.');return;}
    clearTimeout(timer);room=id;token='';generation++;busy=false;remoteSample=null;synchronization=null;waiting=true;pendingPaused=true;lastCorrection=-Infinity;
    controls();status('Подключаемся к комнате…');await tick();
  }
  el('room-create').onclick=async()=>{
    if(!PP.playing()){status('Сначала запустите фильм или серию.');return;}
    el('room-create').disabled=true;
    try {
      hostPaused=PP.ready() && video.paused;
      const data=await request('POST',snapshot());
      room=data.room;token=data.host_token;generation++;
      el('room-link').value=location.origin+'/watch.html?room='+encodeURIComponent(room);
      controls();await tick();
    }catch(error){status(error.message);}finally{el('room-create').disabled=false;}
  };
  el('room-copy').onclick=async()=>{
    try{await navigator.clipboard.writeText(el('room-link').value);status('Ссылка скопирована.');}
    catch(_){el('room-link').focus();el('room-link').select();status('Скопируйте выделенную ссылку.');}
  };
  el('room-leave').onclick=async()=>{
    if(token){try{await request('DELETE');}catch(error){status('Не удалось закрыть комнату: '+error.message);return;}}
    leavePresence();
    stopRoom('Совместный просмотр завершён.');
  };
  function changed(){
    if(!room||!token||suspended||!PP.ready())return;
    hostPaused=video.paused;
    hostUpdatePending=true;
    if(!busy){clearTimeout(timer);tick();}
  }
  video.addEventListener('play',changed);video.addEventListener('pause',changed);video.addEventListener('seeked',changed);
  // Guests can grant browser autoplay with a click, but do not change room state.
  video.addEventListener('click',e=>{if(room&&!token){e.stopImmediatePropagation();if(video.paused&&!pendingPaused)video.play().catch(()=>{});}},true);
  for(const name of ['loadeddata','canplay','playing','progress','seeked'])video.addEventListener(name,applyRemoteState);
  video.addEventListener('waiting',()=>{if(room&&!token&&remoteSample){waiting=true;applyRemoteState();}});
  video.addEventListener('playing',()=>{if(room&&!token&&pendingPaused)video.pause();});
  retry.onclick=()=>{
    if(!room||token||suspended)return;
    retryRequested=true;remoteSample=null;waiting=true;pendingPaused=true;
    PP.setRemotePaused(true,'Повторяем подключение к комнате…');clearTimeout(timer);timer=null;
    return tick();
  };
  window.addEventListener('roomretry',()=>retry.onclick());
  pauseAll.onclick=()=>{if(room&&token&&PP.ready())video.pause();};
  window.addEventListener('pagehide',()=>{
    suspended=true;leavePresence();clearTimeout(timer);timer=null;generation++;busy=false;
    remoteSample=null;retry.hidden=true;hostUpdatePending=false;
    if(room&&!token){waiting=true;pendingPaused=true;PP.setRemotePaused(true,'Восстанавливаем связь с комнатой…');}
  });
  window.addEventListener('pageshow',()=>{
    if(!suspended)return;
    suspended=false;
    if(!room)return;
    generation++;busy=false;remoteSample=null;lastCorrection=-Infinity;
    if(token)hostPaused=video.paused;
    else{waiting=true;pendingPaused=true;PP.setRemotePaused(true,'Восстанавливаем связь с комнатой…');}
    return tick();
  });
  return {join};
})();

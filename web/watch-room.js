'use strict';

const WatchRoom = (() => {
  if (!PP.available) return {join:async()=>{}};
  const video=document.getElementById('player');
  const el=id=>document.getElementById(id);
  let room='', token='', timer=null, busy=false, generation=0, hostPaused=false, pendingPaused=false;
  let lastCorrection=-Infinity, remoteSample=null, applying=false, hostUpdatePending=false;
  const pollInterval=1000, correctionInterval=2000, driftTolerance=0.35;
  const memberID=Array.from(crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,'0')).join('');
  function renderParticipants(items) {
    el('room-participants').hidden=!token;
    el('room-participant-count').textContent='Подключены: '+items.length;
    el('room-participant-list').replaceChildren();
    for(const item of items){const li=document.createElement('li');li.textContent=item.kind==='user'?item.name:'Гость — '+item.name;el('room-participant-list').append(li);}
  }
  function leavePresence(){
    if(room&&!token)fetch('/api/rooms/'+encodeURIComponent(room)+'/leave',{method:'POST',headers:{'X-Room-Member':memberID},keepalive:true}).catch(()=>{});
  }
  const status=message=>{el('room-status').textContent=message;};
  async function request(method,body) {
    const response=await fetch('/api/rooms'+(room?'/'+encodeURIComponent(room):''),{
      method,headers:{'Content-Type':'application/json','X-Room-Member':memberID,...(token?{'X-Room-Host':token}:{})},
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
    if(!token)renderParticipants([]);
  }
  function stopRoom(message) {
    generation++;clearTimeout(timer);timer=null;room='';token='';busy=false;
    remoteSample=null;hostUpdatePending=false;lastCorrection=-Infinity;
    controls();PP.setRemotePaused(false);PlaybackExtras.loading('');status(message);
    const url=new URL(location.href);url.searchParams.delete('room');
    const saved=Object.assign({},history.state);
    if(saved.playback){const playback=new URLSearchParams(saved.playback);playback.delete('room');saved.playback=playback.toString();}
    history.replaceState(saved,'',url.pathname+url.search);
  }
  function waitForHost(message) {
    remoteSample=null;pendingPaused=true;PP.setRemotePaused(true);status(message);
  }
  function applyRemoteState() {
    if(!room||token||!remoteSample||applying)return;
    const sample=remoteSample, s=sample.state, now=performance.now();
    const elapsed=Math.max(0,(now-sample.received)/1000);
    if(sample.age+elapsed>12){
      waitForHost('Связь с ведущим потеряна. Ожидаем его возвращения…');return;
    }
    const local=PP.state();
    if(local.id!==s.id||local.magnet!==s.magnet||local.file!==s.file||!PP.ready()){
      sample.force=true;PP.setRemotePaused(s.paused);return;
    }
    applying=true;
    try {
      // Keep a server-relative sample on a monotonic clock. It continues to
      // advance while a new source is loading, independently of clock skew.
      const target=sample.position+(s.paused?0:elapsed), drift=Math.abs(local.position-target);
      const force=sample.force;
      sample.force=false;
      if(drift>(force||s.paused?0.1:driftTolerance)&&(force||s.paused||now-lastCorrection>=correctionInterval)){
        PP.seek(target);lastCorrection=now;
        if(!PP.ready())sample.force=true;
      }
      // Seek before releasing a remote pause: merely pressing play leaves the
      // full polling delay behind the host when the difference is small.
      PP.setRemotePaused(s.paused);
      if(PP.ready()){
        if(s.paused)video.pause();
        else if(video.paused)video.play().catch(()=>status('Нажмите ▶ в плеере, чтобы разрешить воспроизведение.'));
      }
    } finally {applying=false;}
  }
  async function tick() {
    if (!room || busy) return;
    busy=true;const gen=generation, started=performance.now();
    if(token)hostUpdatePending=false;
    try {
      const data=await request(token?'PUT':'GET',token?snapshot():undefined);
      if (gen!==generation) return;
      if (!token) {
        const s=data.state, local=PP.state();
        const age=Math.max(0,(data.server_time-data.updated)/1000);
        if (age>12) {waitForHost('Связь с ведущим потеряна. Ожидаем его возвращения…');return;}
        const received=performance.now(), changedPause=remoteSample&&remoteSample.state.paused!==s.paused;
        const target=s.position+(s.paused?0:age+(received-started)/2000);
        remoteSample={state:s,position:target,received,age,force:!remoteSample||remoteSample.force||!!changedPause};
        pendingPaused=s.paused;
        if (local.id!==s.id || local.magnet!==s.magnet || local.file!==s.file) {
          currentItem={id:s.id,kind:s.season?'tv':'movie'};
          PP.start({...s,pos:target,ep:s.episode,paused:s.paused});
          remoteSample.force=true;
          const title=document.getElementById('watch-title');if(title)title.textContent='Совместный просмотр — '+s.id;
          const link=document.getElementById('film-link');if(link)link.href='/film.html?id='+encodeURIComponent(s.id);
        }
        applyRemoteState();
        status(s.paused?'Ведущий поставил просмотр на паузу.':'Вы смотрите вместе. Воспроизведением управляет ведущий.');
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
      if(gen===generation){busy=false;if(room)timer=setTimeout(tick,hostUpdatePending?0:pollInterval);}
    }
  }
  async function join(id) {
    if(!/^[a-f0-9]{32}$/.test(id)){status('Некорректная ссылка комнаты.');return;}
    clearTimeout(timer);room=id;token='';generation++;busy=false;remoteSample=null;lastCorrection=-Infinity;
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
    if(!room||!token||!PP.ready())return;
    hostPaused=video.paused;
    hostUpdatePending=true;
    if(!busy){clearTimeout(timer);tick();}
  }
  video.addEventListener('play',changed);video.addEventListener('pause',changed);video.addEventListener('seeked',changed);
  // Guests can grant browser autoplay with a click, but do not change room state.
  video.addEventListener('click',e=>{if(room&&!token){e.stopImmediatePropagation();if(video.paused&&!pendingPaused)video.play().catch(()=>{});}},true);
  for(const name of ['loadeddata','canplay','playing'])video.addEventListener(name,applyRemoteState);
  video.addEventListener('playing',()=>{if(room&&!token&&pendingPaused)video.pause();});
  window.addEventListener('pagehide',()=>{leavePresence();clearTimeout(timer);generation++;});
  return {join};
})();

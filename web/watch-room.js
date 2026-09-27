'use strict';

const WatchRoom = (() => {
  if (!PP.available) return {join:async()=>{}};
  const video=document.getElementById('player');
  const el=id=>document.getElementById(id);
  let room='', token='', timer=null, busy=false, generation=0, hostPaused=false, pendingPaused=false;
  let lastCorrection=0;
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
    controls();PP.setRemotePaused(false);PlaybackExtras.loading('');status(message);
    const url=new URL(location.href);url.searchParams.delete('room');history.replaceState(null,'',url.pathname+url.search);
  }
  async function tick() {
    if (!room || busy) return;
    busy=true;const gen=generation, started=performance.now();
    try {
      const data=await request(token?'PUT':'GET',token?snapshot():undefined);
      if (gen!==generation) return;
      if (!token) {
        const s=data.state, local=PP.state();
        const age=Math.max(0,(data.server_time-data.updated)/1000);
        if (age>12) {PP.setRemotePaused(true);status('Связь с ведущим потеряна. Ожидаем его возвращения…');return;}
        const target=s.position+(s.paused?0:age+(performance.now()-started)/2000);
        pendingPaused=s.paused;
        PP.setRemotePaused(s.paused);
        if (local.id!==s.id || local.magnet!==s.magnet || local.file!==s.file) {
          currentItem={id:s.id,kind:s.season?'tv':'movie'};
          PP.start({...s,pos:target,ep:s.episode,paused:s.paused});
          const title=document.getElementById('watch-title');if(title)title.textContent='Совместный просмотр — '+s.id;
          const link=document.getElementById('film-link');if(link)link.href='/film.html?id='+encodeURIComponent(s.id);
          lastCorrection=Date.now();
        } else if (PP.ready() && Math.abs(local.position-target)>3 && Date.now()-lastCorrection>8000) {
          PP.seek(target);lastCorrection=Date.now();
        }
        if (PP.ready()) {
          if(s.paused)video.pause();else if(video.paused)video.play().catch(()=>status('Нажмите ▶ в плеере, чтобы разрешить воспроизведение.'));
        }
        status(s.paused?'Ведущий поставил просмотр на паузу.':'Вы смотрите вместе. Воспроизведением управляет ведущий.');
      } else {
        status('Вы — ведущий. Пауза, перемотка и смена серии передаются участникам.');
        renderParticipants(data.participants||[]);
      }
    } catch(error) {
      if(gen!==generation)return;
      if(error.status===404 || error.status===401 || error.status===403){if(!token)video.pause();stopRoom(error.message);return;}
      if(!token)PP.setRemotePaused(true);
      status('Нет связи с комнатой. Повторяем подключение…');
    } finally {
      if(gen===generation){busy=false;if(room)timer=setTimeout(tick,1500);}
    }
  }
  async function join(id) {
    if(!/^[a-f0-9]{32}$/.test(id)){status('Некорректная ссылка комнаты.');return;}
    room=id;token='';generation++;controls();status('Подключаемся к комнате…');await tick();
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
    if(!busy){clearTimeout(timer);tick();}
  }
  video.addEventListener('play',changed);video.addEventListener('pause',changed);video.addEventListener('seeked',changed);
  // Guests can grant browser autoplay with a click, but do not change room state.
  video.addEventListener('click',e=>{if(room&&!token){e.stopImmediatePropagation();if(video.paused&&!pendingPaused)video.play().catch(()=>{});}},true);
  video.addEventListener('playing',()=>{if(room&&!token&&pendingPaused)video.pause();});
  window.addEventListener('pagehide',()=>{leavePresence();clearTimeout(timer);generation++;});
  return {join};
})();

'use strict';

const PlaybackExtras = (() => {
  if (!PP.available) return {};
  const video = document.getElementById('player'), wrap = document.getElementById('player-wrap');
  const panel = document.createElement('section');
  panel.className = 'playback-extras';
  panel.innerHTML = `<div id="loading-state" role="status" aria-live="polite" hidden>
    <strong id="loading-title"></strong><p id="loading-detail"></p>
    <button type="button" id="loading-retry">Повторить</button>
    <button type="button" id="loading-source">Другой источник</button>
  </div>
  <details><summary>Настройки просмотра</summary><div class="playback-settings">
    <label>Размер субтитров <select id="subtitle-size"><option value="80">Маленький</option><option value="100">Обычный</option><option value="130">Крупный</option><option value="160">Очень крупный</option></select></label>
    <label>Фон субтитров <select id="subtitle-background"><option value="dark">Тёмный</option><option value="clear">Прозрачный</option></select></label>
    <label>Положение <select id="subtitle-position"><option value="bottom">Снизу</option><option value="top">Сверху</option></select></label>
    <label>Задержка субтитров, сек. <input id="subtitle-delay" type="number" min="-30" max="30" step="0.5" value="0"></label>
    <label>Язык субтитров <select id="subtitle-language"><option value="off">Выключены</option><option value="rus">Русский</option><option value="eng">Английский</option></select></label>
    <label><input type="checkbox" id="prepare-next" checked> Подготавливать следующую серию</label>
    <p id="preferences-status" role="status"></p>
  </div></details>
  <p id="prepare-status" role="status"></p>
  <div class="watch-room"><button type="button" id="room-create">Смотреть вместе</button>
    <button type="button" id="room-leave" hidden>Выйти из комнаты</button>
    <label id="room-invite" hidden>Ссылка для приглашения <input id="room-link" readonly></label>
    <button type="button" id="room-copy" hidden>Копировать ссылку</button>
    <p id="room-status" role="status"></p>
    <div id="room-participants" hidden><p id="room-participant-count"></p><ul id="room-participant-list"></ul></div>
  </div>`;
  wrap.after(panel);
  const el = id => document.getElementById(id);
  el('loading-state').className='playback-loading';
  wrap.append(el('loading-state'));
  let stage = '', since = 0;
  const titles = {searching:'Ищем источник', preparing:'Подключаемся к источнику', buffering:'Буферизуем видео', error:'Не удалось начать просмотр', paused:'Готово к просмотру'};
  function loading(next, message) {
    stage = next; since = Date.now();
    if(next==='searching')wrap.hidden=false;
    el('loading-state').hidden = !next;
    el('loading-title').textContent = titles[next] || next;
    el('loading-detail').textContent = message || (next === 'preparing' ? 'Получаем данные и подготавливаем поток.' : 'Ждём первые кадры.');
    el('loading-retry').hidden = next !== 'error' && next !== 'paused';
    el('loading-source').hidden = next !== 'error';
  }
  window.addEventListener('playbackstage', e => loading(e.detail.stage, e.detail.message));
  video.addEventListener('playing', () => loading(''));
  video.addEventListener('waiting', () => { if (PP.playing()) loading('buffering'); });
  window.addEventListener('playbackfailure', () => loading('error','Источник недоступен, сервер занят или формат не поддерживается. Повторите запуск или выберите другой источник.'));
  window.addEventListener('playbackstop', () => loading(''));
  setInterval(() => {
    if (!['searching','preparing','buffering'].includes(stage)) return;
    const seconds = Math.floor((Date.now()-since)/1000);
    el('loading-detail').textContent = seconds < 20 ? `Ожидание: ${seconds} сек.` : `Ожидание: ${seconds} сек. Источник отвечает медленно. Можно повторить запуск или сменить источник.`;
    el('loading-retry').hidden = el('loading-source').hidden = seconds < 20;
  }, 1000);
  el('loading-retry').onclick = () => {
    if (stage === 'paused') { video.play().catch(() => loading('paused','Нажмите кнопку воспроизведения в плеере.')); return; }
    const s = PP.state(); if(!s.id){window.dispatchEvent(new Event('retryplayback'));return;} if (s.id) PP.start({...s, pos:s.position, ep:s.episode, release:PP.release()});
  };
  el('loading-source').onclick = () => {if(PP.state().id)window.dispatchEvent(new Event('requestsourcechange'));else{loading('');document.getElementById('sources')?.scrollIntoView({block:'center'});}};

  const defaults = {size:100, background:'dark', position:'bottom', delay:0, language:'off', prepare:true};
  let preferences = {...defaults}, cueOrigins = new WeakMap();
  const style = document.createElement('style'); document.head.append(style);
  function applyCues() {
    for (const track of video.textTracks) {
      if (!['subtitles','captions'].includes(track.kind)) continue;
      for (const cue of Array.from(track.cues || [])) {
        if (!cueOrigins.has(cue)) cueOrigins.set(cue, {start:cue.startTime,end:cue.endTime,line:cue.line,snap:cue.snapToLines});
        const base = cueOrigins.get(cue);
        cue.startTime = Math.max(0,base.start+preferences.delay);
        cue.endTime = Math.max(cue.startTime+0.01,base.end+preferences.delay);
        cue.snapToLines = preferences.position === 'top' ? false : base.snap;
        cue.line = preferences.position === 'top' ? 10 : base.line;
      }
    }
  }
  function applyPreferences() {
    style.textContent = `#player::cue {font-size:${preferences.size}%;background-color:${preferences.background==='clear'?'transparent':'rgba(0,0,0,.8)'};color:white;text-shadow:0 1px 3px black}`;
    applyCues();
    for (const key of ['size','background','position','delay','language']) el('subtitle-'+key).value = String(preferences[key]);
    el('prepare-next').checked = preferences.prepare;
  }
  function loadPreferences() {
    const saved = Personal.get('preferences','player');
    const raw = saved && saved.data || {};
    preferences = {
      size:[80,100,130,160].includes(+raw.size)?+raw.size:100,
      background:raw.background==='clear'?'clear':'dark',position:raw.position==='top'?'top':'bottom',
      delay:Math.max(-30,Math.min(30,Number(raw.delay)||0)),language:typeof raw.language==='string'?raw.language:'off',prepare:raw.prepare!==false
    };
    applyPreferences();
  }
  window.addEventListener('personalchange',()=>{loadPreferences();queueMicrotask(chooseLanguage);});
  loadPreferences();
  async function savePreferences() {
    applyPreferences();
    if (!VV.user) {el('preferences-status').textContent='Настройки действуют до закрытия страницы. Войдите, чтобы сохранить их.';return;}
    try { await Personal.put('preferences','player',preferences);el('preferences-status').textContent='Настройки сохранены.'; }
    catch (_) {el('preferences-status').textContent='Не удалось сохранить настройки. Изменения действуют на этой странице.';}
  }
  for (const key of ['size','background','position','delay','language']) el('subtitle-'+key).onchange = () => {
    const value = el('subtitle-'+key).value;
    preferences[key] = key==='delay'?Math.max(-30,Math.min(30,Number(value)||0)):key==='size'?Number(value):value;
    if (key==='language') chooseLanguage();
    savePreferences();
  };
  let subtitles = [];
  function chooseLanguage() {
    const aliases = {ru:'rus',en:'eng'};
    const match = subtitles.find(t => (aliases[t.language] || t.language) === preferences.language);
    PP.selectSubtitle(preferences.language==='off' || !match ? -1 : match.ordinal);
  }
  window.addEventListener('subtitletracks', e => {
    subtitles = e.detail;
    const selector = el('subtitle-language');
    for (const t of subtitles) {
      const lang = ({ru:'rus',en:'eng'})[t.language] || t.language;
      if (lang && !Array.from(selector.options).some(o=>o.value===lang)) selector.add(new Option(lang,lang));
    }
    selector.value = preferences.language;
    chooseLanguage();
  });
  window.addEventListener('subtitlechoice',e=>{preferences.language=({ru:'rus',en:'eng'})[e.detail]||e.detail||'off';savePreferences();});
  video.textTracks.addEventListener('addtrack', applyCues);
  video.addEventListener('timeupdate', applyCues);

  let preparation = null, preparedKey = '', readyNext = null, retryAt = 0;
  function cancelPreparation() { if (preparation) preparation.abort(); preparation=null; readyNext=null; preparedKey='';retryAt=0;el('prepare-status').textContent=''; }
  el('prepare-next').onchange = () => { preferences.prepare=el('prepare-next').checked;if(!preferences.prepare)cancelPreparation();savePreferences(); };
  window.addEventListener('playbackstart', () => {cancelPreparation();preparedKey='';});
  window.addEventListener('playbackstop', cancelPreparation);
  window.addEventListener('pagehide', cancelPreparation);
  video.addEventListener('timeupdate', async () => {
    const s=PP.state();
    if (!preferences.prepare || video.paused || !s.duration || s.duration-s.position>300 || !s.season || !s.episode || document.body.classList.contains('room-guest')) return;
    const count=seasonEpisodeCount(s.id,s.season);
    const season=count&&s.episode>=count?s.season+1:s.season, episode=count&&s.episode>=count?1:s.episode+1;
    let next=PP.files().find(f=>f.season===season && f.episode===episode);
    let source={magnet:s.magnet,title:PP.release()};
    const key=s.id+'|'+s.magnet+'|'+s.file;
    if (key===preparedKey || preparation || Date.now()<retryAt) return;
    preparedKey=key;
    const controller=new AbortController();preparation=controller;
    const timer=setTimeout(()=>controller.abort(),390000);
    el('prepare-status').textContent='Подготавливаем следующую серию…';
    try {
      if(!next){
        const response=await fetch('/api/films/'+encodeURIComponent(s.id)+'/sources',{signal:controller.signal});
        if(!response.ok)throw new Error('sources unavailable');
        const data=await response.json();
        for(const candidate of Personal.rank(data.items||data.sources||[],s.voice).slice(0,3)){
          if(controller.signal.aborted)throw new Error('cancelled');
          const files=await fetchFiles(s.id,candidate.magnet,candidate.title,{signal:controller.signal});
          next=(files||[]).find(f=>f.season===season&&f.episode===episode);
          if(next){source=candidate;break;}
        }
      }
      if(!next)throw new Error('next episode unavailable');
      if(preparation!==controller || controller.signal.aborted)return;
      // Make the chosen source available immediately: auto-next must not wait
      // for the entire background download to finish before reusing it.
      readyNext={key,id:s.id,magnet:source.magnet,release:source.title,file:next.index,season,ep:episode,voice:s.voice,pos:0};
      el('prepare-status').textContent='Скачивается следующая серия…';
      const q=new URLSearchParams({magnet:source.magnet,file:next.index});
      const response=await fetch('/api/stream/prepare?'+q,{method:'POST',signal:controller.signal});
      if (preparation!==controller) return;
      if(!response.ok)throw new Error('preparation deferred');
      el('prepare-status').textContent='Видео следующей серии подготовлено.';
    } catch (_) { if(preparation===controller){preparedKey='';retryAt=Date.now()+30000;el('prepare-status').textContent='Загрузка следующей серии отложена. Повторим через 30 секунд.';} }
    finally {clearTimeout(timer);if(preparation===controller)preparation=null;}
  });
  return {loading, next:()=>{const s=PP.state();return readyNext&&readyNext.key===s.id+'|'+s.magnet+'|'+s.file?readyNext:null;}};
})();

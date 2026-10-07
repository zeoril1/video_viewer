'use strict';

// Exact-file marks and skip modes use separate preference keys from subtitles.
const SkipSegments = (() => {
  if (typeof PP === 'undefined' || !PP.available) return {};
  const video = document.getElementById('player'), wrap = document.getElementById('player-wrap');
  const types = ['recap', 'intro', 'credits'];
  const names = {recap:'Пересказ', intro:'Заставка', credits:'Титры'};
  const verbs = {recap:'Пропустить пересказ', intro:'Пропустить заставку', credits:'Пропустить титры'};
  const sources = {chapter:'Глава видео', audio_match:'Распознано по звуку', theintrodb:'Внешняя разметка', manual:'Ваши отметки'};
  const defaults = {recap:'button', intro:'button', credits:'button'};
  const toolbar = document.createElement('div');
  toolbar.className = 'skip-segments-toolbar';
  toolbar.hidden = true;
  toolbar.innerHTML = `<span id="skip-segment-hint" class="skip-segment-hint"></span>
    <button id="skip-segment" type="button" hidden>Пропустить</button>
    <button id="skip-segment-undo" type="button" hidden>Вернуться</button>
    <button id="skip-segments-open" type="button" aria-expanded="false" aria-controls="skip-segments-panel">Пропуск</button>`;
  wrap.append(toolbar);
  const panel = document.createElement('section');
  panel.id = 'skip-segments-panel';
  panel.className = 'skip-segments-panel';
  panel.hidden = true;
  panel.setAttribute('aria-label', 'Настройки пропуска');
  panel.innerHTML = `<div class="skip-segments-heading"><h3>Пропуск заставок и повторов</h3><button id="skip-segments-close" type="button">Закрыть</button></div>
    <div class="skip-segments-modes">${types.map(type => `<label>${names[type]} <select id="skip-mode-${type}"><option value="off">Выключено</option><option value="button">Кнопка</option><option value="auto">Автоматически</option></select></label>`).join('')}</div>
    <p>Автопропуск использует главы видео и подтверждённые границы. Администраторы и модераторы могут проверить, подтвердить или исправить найденные отметки для этого файла.</p>
    <p>Титры пропускаются до конца выбранного участка. Сцены между участками остаются.</p>
    <p id="skip-segments-source-status" role="status"></p>
    <button id="skip-segments-retry" type="button" hidden>Повторить поиск отметок</button>
    <div id="skip-segments-list" class="skip-segments-list"></div>
    <p id="skip-edit-permission">Добавлять и изменять отметки могут администраторы и модераторы.</p>
    <form id="skip-segments-form" class="skip-segments-form" hidden>
      <h4 id="skip-segments-form-title">Добавить участок для этой серии</h4>
      <label>Участок <select id="skip-edit-type">${types.map(type => `<option value="${type}">${names[type]}</option>`).join('')}</select></label>
      <label>Начало <input id="skip-edit-start" inputmode="decimal" placeholder="0:00" required aria-describedby="skip-time-help"><button id="skip-capture-start" type="button">Текущая позиция</button></label>
      <label>Конец <input id="skip-edit-end" inputmode="decimal" placeholder="1:30" required aria-describedby="skip-time-help"><button id="skip-capture-end" type="button">Текущая позиция</button></label>
      <p id="skip-time-help">Время: секунды, минуты:секунды или часы:минуты:секунды. Сохранение подтверждает указанные границы для этого файла.</p>
      <div><button id="skip-edit-save" type="submit">Сохранить границы</button><button id="skip-edit-cancel" type="button" hidden>Отменить изменение</button></div>
    </form>
    <p id="skip-segments-status" role="status" aria-live="polite"></p>`;
  wrap.append(panel);
  const el = id => document.getElementById(id);
  let identity = '', mediaKey = '', duration = 0, chapter = [], external = [], effective = [];
  let externalRequest = null, externalLookup = '', externalRetryAvailable = false, sourceStatus = '', edit = null;
  let modes = {...defaults}, localModes = false, owner = account(), editorRole = VV.user && VV.user.role || '';
  const localOverrides = new Map(), suppressed = new Set();
  let lastPosition = null, undo = null, ignoredBackwardUntil = 0, generation = 0;
  let preferenceQueue = Promise.resolve();

  function account() { return VV.user ? String(VV.user.id) : 'guest'; }
  function playbackIdentity(state) { return [state.id || '', state.magnet || '', state.file ?? -1].join('|'); }
  function isFollower() { return PP.isFollower ? PP.isFollower() : document.body.classList.contains('room-guest'); }
  function canEdit() { return !!VV.user && ['admin','moderator'].includes(VV.user.role) && !isFollower(); }
  function validMediaKey(key) { return /^segments\.[a-f0-9]{40}\.\d+$/.test(key || ''); }
  function clean(items, source) {
    return (Array.isArray(items) ? items : []).filter(s => s && types.includes(s.type)
      && Number.isFinite(s.start) && Number.isFinite(s.end) && s.start >= 0 && s.end > s.start
      && (!duration || s.end <= duration + 0.25)).map(s => ({
        type:s.type, start:s.start, end:duration ? Math.min(s.end,duration) : s.end,
        source:source || s.source || 'manual', auto_skip:s.auto_skip === true || s.verified === true,
      })).sort((a,b) => a.start-b.start || a.end-b.end);
  }
  function segmentKey(s) { return s.type + ':' + s.start + ':' + s.end; }
  function overriddenTypes(raw) { return (Array.isArray(raw.overridden_types) ? raw.overridden_types : (raw.segments || []).map(s => s.type)).filter(t => types.includes(t)); }
  function override() {
    if (!validMediaKey(mediaKey)) return {segments:[], overridden_types:[]};
    if (localOverrides.has(mediaKey)) return localOverrides.get(mediaKey);
    const saved = VV.user && Personal.get('preferences', mediaKey);
    const raw = saved && saved.data;
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {segments:[], overridden_types:[]};
    return {segments:Array.isArray(raw.segments) ? raw.segments.filter(s => s && typeof s === 'object') : [],
      ...(Array.isArray(raw.overridden_types) ? {overridden_types:raw.overridden_types} : {})};
  }
  function loadModes() {
    const nextOwner = account(), nextRole = VV.user && VV.user.role || '';
    if (nextRole !== editorRole) {
      editorRole = nextRole; localOverrides.clear(); resetEditor();
    }
    if (nextOwner !== owner) {
      owner = nextOwner; localModes = false; localOverrides.clear(); suppressed.clear(); undo = null;
      el('skip-segments-status').textContent = ''; resetEditor();
    }
    if (!localModes) {
      const saved = VV.user && Personal.get('preferences', 'skip_segments');
      const raw = saved && saved.data || {};
      modes = Object.fromEntries(types.map(type => [type, ['off','button','auto'].includes(raw[type]) ? raw[type] : 'button']));
    }
    for (const type of types) el('skip-mode-'+type).value = modes[type];
    compose();
  }
  function compose() {
    const raw = override();
    const manual = clean(raw.segments);
    const overridden = new Set(overriddenTypes(raw));
    effective = types.flatMap(type => overridden.has(type) ? manual.filter(s => s.type === type)
      : chapter.some(s => s.type === type) ? chapter.filter(s => s.type === type) : external.filter(s => s.type === type))
      .sort((a,b) => a.start-b.start || a.end-b.end);
    // Conflicting kinds can otherwise chain two automatic jumps. Keep their
    // buttons available and require the user to correct their boundaries.
    effective = effective.map(segment => ({...segment, overlap:effective.some(other => other.type !== segment.type
      && other.start < segment.end && other.end > segment.start)}));
    renderList(); tick();
  }
  function time(value) {
    const rounded = Math.round(Math.max(0, Number(value) || 0) * 100) / 100;
    const hours = Math.floor(rounded/3600), minutes = Math.floor(rounded/60)%60;
    const seconds = (rounded%60).toFixed(2).replace(/\.00$/, '').replace(/(\.\d)0$/, '$1').split('.');
    return (hours ? hours + ':' + String(minutes).padStart(2,'0') : String(minutes)) + ':' + seconds[0].padStart(2,'0') + (seconds[1] ? '.'+seconds[1] : '');
  }
  function parseTime(value) {
    const parts = String(value).trim().replace(',', '.').split(':');
    if (!parts.length || parts.length > 3 || parts.some(p => !/^\d+(?:\.\d+)?$/.test(p))) return NaN;
    if (parts.length > 1 && parts.slice(1).some(p => Number(p) >= 60)) return NaN;
    return parts.reduce((sum,p) => sum*60+Number(p),0);
  }
  function button(text, action) {
    const node = document.createElement('button'); node.type = 'button'; node.textContent = text;
    node.className = 'skip-segments-edit-action'; node.disabled = !canEdit(); node.hidden = !canEdit();
    node.addEventListener('click', () => {if (canEdit()) action();}); return node;
  }
  function renderList() {
    const list = el('skip-segments-list'); list.replaceChildren();
    el('skip-segments-source-status').textContent = sourceStatus;
    el('skip-segments-retry').hidden = !externalRetryAvailable;
    if (!effective.length) {
      const empty = document.createElement('p'); empty.textContent = canEdit() ? 'Участки пока не отмечены. Можно задать границы вручную.' : 'Участки пока не отмечены.'; list.append(empty);
    }
    for (const segment of effective) {
      const row = document.createElement('div'); row.className = 'skip-segments-row';
      const label = document.createElement('span');
      label.textContent = `${names[segment.type]} · ${time(segment.start)} — ${time(segment.end)} · ${sources[segment.source] || 'Ваши отметки'}${segment.overlap ? ' · пересекается с другим участком' : segment.auto_skip ? '' : ' · требует проверки'}`;
      row.append(label);
      if (!segment.auto_skip) row.append(button('Подтвердить', () => setType(segment.type, effective.filter(s => s.type === segment.type).map(s => segmentKey(s) === segmentKey(segment) ? {...s,source:'manual',auto_skip:true} : s))));
      row.append(button('Исправить', () => {
        if (!canEdit()) return;
        edit = {key:segmentKey(segment), type:segment.type};
        el('skip-edit-type').value = segment.type; el('skip-edit-type').disabled = true;
        el('skip-edit-start').value = time(segment.start); el('skip-edit-end').value = time(segment.end);
        el('skip-segments-form-title').textContent = 'Исправить границы'; el('skip-edit-cancel').hidden = false;
        el('skip-edit-start').focus();
      }));
      row.append(button('Удалить', () => setType(segment.type, effective.filter(s => s.type === segment.type && segmentKey(s) !== segmentKey(segment)))));
      list.append(row);
    }
    for (const type of overriddenTypes(override())) {
      const row = document.createElement('div'); row.className = 'skip-segments-row';
      row.append(button('Восстановить найденные отметки: '+names[type], () => {
        const raw = override();
        saveOverride({segments:(raw.segments || []).filter(s => s.type !== type), overridden_types:overriddenTypes(raw).filter(t => t !== type)});
      }));
      list.append(row);
    }
    refreshRole();
  }
  async function saveOverride(data) {
    if (!canEdit() || !validMediaKey(mediaKey)) return;
    const key = mediaKey, requestOwner = owner, previous = localOverrides.get(mediaKey);
    localOverrides.set(key, data); resetEditor(); compose();
    el('skip-segments-status').textContent = 'Сохраняем отметки…';
    const write = async () => {
      if (account() !== requestOwner || !canEdit()) return;
      try {
        await Personal.put('preferences', key, data);
        if (account() === requestOwner && mediaKey === key) el('skip-segments-status').textContent = 'Отметки для этого файла сохранены.';
      } catch (_) {
        if (account() === requestOwner && mediaKey === key) {
          if (localOverrides.get(key) === data) {
            if (previous) localOverrides.set(key, previous); else localOverrides.delete(key);
            compose();
          }
          el('skip-segments-status').textContent = 'Не удалось сохранить отметки. Проверьте права доступа и повторите попытку.';
        }
      }
    };
    preferenceQueue = preferenceQueue.then(write, write); await preferenceQueue;
  }
  function setType(type, segments) {
    if (!canEdit()) return;
    const raw = override();
    saveOverride({segments:[...(raw.segments || []).filter(s => s.type !== type), ...segments.map(s => ({type:s.type,start:s.start,end:s.end,source:s.source,auto_skip:s.auto_skip,verified:s.auto_skip === true}))],
      overridden_types:Array.from(new Set([...overriddenTypes(raw),type]))});
  }
  function resetEditor() {
    edit = null; el('skip-edit-type').disabled = !canEdit();
    el('skip-edit-start').value = ''; el('skip-edit-end').value = '';
    el('skip-edit-cancel').hidden = true; el('skip-segments-form-title').textContent = 'Добавить участок для этой серии';
  }
  function refreshRole() {
    const follower = isFollower(), editable = canEdit();
    for (const node of panel.querySelectorAll('input, select, button')) node.disabled = follower;
    el('skip-segments-form').hidden = !editable;
    el('skip-edit-permission').hidden = editable;
    for (const node of panel.querySelectorAll('button')) if (node.className === 'skip-segments-edit-action') {node.hidden = !editable; node.disabled = !editable;}
    for (const id of ['skip-edit-type','skip-edit-start','skip-edit-end','skip-capture-start','skip-capture-end','skip-edit-cancel']) el(id).disabled = !editable;
    el('skip-segments-close').disabled = false;
    el('skip-edit-type').disabled = !editable || !!edit;
    el('skip-edit-save').disabled = !editable || !validMediaKey(mediaKey);
    const state = PP.state();
    el('skip-segments-retry').disabled = follower || !!externalRequest || !validMediaKey(mediaKey) || duration <= 0 || !state.episode;
    el('skip-segment').disabled = follower; el('skip-segment-undo').disabled = follower;
    el('skip-segment').title = follower ? 'Пропуском управляет ведущий комнаты.' : '';
  }
  function openPanel(open) {
    panel.hidden = !open; el('skip-segments-open').setAttribute('aria-expanded', String(open));
    if (open) { refreshRole(); el('skip-segments-close').focus(); }
    else { resetEditor(); el('skip-segments-open').focus(); }
  }
  function performSkip(segment) {
    if (!segment || isFollower()) return;
    const state = PP.state();
    if (playbackIdentity(state) !== identity || state.position < segment.start || state.position >= segment.end) return;
    suppressed.add(segmentKey(segment));
    const terminal = duration > 0 && Math.abs(segment.end-duration) < 0.01;
    undo = terminal ? null : {position:state.position, expires:Date.now()+15000}; ignoredBackwardUntil = Date.now()+5000;
    if (PP.skipSegment) PP.skipSegment(segment.end); else PP.seek(segment.end);
    tick();
  }
  function tick() {
    const state = PP.state(), position = Number(state.position), playing = !!state.playing;
    toolbar.hidden = !state.id || !playing;
    if (!playing || playbackIdentity(state) !== identity || !Number.isFinite(position)) {
      el('skip-segment').hidden = true; el('skip-segment-undo').hidden = true; return;
    }
    if (lastPosition !== null && position < lastPosition-1 && Date.now() >= ignoredBackwardUntil) {
      for (const segment of effective) if (segment.end > position && segment.start <= lastPosition) suppressed.add(segmentKey(segment));
    }
    lastPosition = position;
    const active = effective.find(s => position >= s.start && position < s.end && modes[s.type] !== 'off');
    el('skip-segment').hidden = !active;
    if (active) el('skip-segment').textContent = active.type === 'credits' && duration > 0 && Math.abs(active.end-duration) < 0.01
      ? state.season && state.episode ? 'Следующая серия' : 'Завершить просмотр' : verbs[active.type];
    el('skip-segment-hint').textContent = isFollower() ? 'Пропуском управляет ведущий.'
      : active && active.overlap ? 'Участки пересекаются. Исправьте границы.'
      : active && !active.auto_skip ? active.source === 'audio_match' ? 'Проверьте распознанные границы.' : 'Проверьте границы внешней разметки.' : '';
    el('skip-segment-undo').hidden = !undo || undo.expires < Date.now();
    refreshRole();
    if (active && modes[active.type] === 'auto' && active.auto_skip && !active.overlap && !suppressed.has(segmentKey(active))
      && !isFollower() && !video.paused && panel.hidden && (!PP.ready || PP.ready())) performSkip(active);
  }
  function cancelExternal() {
    if (externalRequest) externalRequest.abort();
    externalRequest = null; externalLookup = '';
  }
  function resetPlayback() {
    generation++; cancelExternal(); identity = ''; mediaKey = ''; duration = 0;
    chapter = []; external = []; effective = []; sourceStatus = ''; externalRetryAvailable = false; suppressed.clear(); lastPosition = null; undo = null;
    ignoredBackwardUntil = 0; panel.hidden = true; toolbar.hidden = true;
    el('skip-segments-open').setAttribute('aria-expanded', 'false'); el('skip-segments-status').textContent = ''; resetEditor(); renderList();
  }
  async function lookupExternal(state) {
    if (!state.id || !Number.isInteger(state.season) || state.season < 0 || !state.episode || duration <= 0 || !mediaKey) return;
    const tmdb = typeof currentItem !== 'undefined' && currentItem && currentItem.tmdb_id || '';
    const query = new URLSearchParams({season:state.season, episode:state.episode, duration, media_key:mediaKey, ...(tmdb ? {tmdb} : {})});
    const lookup = state.id+'?'+query;
    if (lookup === externalLookup) return;
    cancelExternal(); externalLookup = lookup;
    const controller = new AbortController(), requestIdentity = identity, requestGeneration = generation, requestMediaKey = mediaKey;
    externalRequest = controller; externalRetryAvailable = false; sourceStatus = 'Ищем готовые отметки…'; compose();
    const timeout = setTimeout(() => controller.abort(),10000);
    try {
      const response = await fetch('/api/films/'+encodeURIComponent(state.id)+'/segments?'+query, {signal:controller.signal});
      if (!response.ok) throw new Error('lookup unavailable');
      const data = await response.json();
      if (externalRequest !== controller || requestGeneration !== generation || requestIdentity !== playbackIdentity(PP.state()) || requestMediaKey !== mediaKey) return;
      external = clean((data.segments || []).map(s => ({...s,source:s.source || 'theintrodb',verified:false})))
        .filter(s => s.source !== 'audio_match' || data.media_key === requestMediaKey)
        .map(s => ({...s,auto_skip:s.source === 'audio_match' && s.auto_skip === true}));
      externalRetryAvailable = !external.length && data.status !== 'disabled';
      sourceStatus = external.some(s => s.source === 'audio_match') ? 'Сохранённые отметки распознавания загружены для этого файла.'
        : external.length ? 'Найдена внешняя разметка. Проверьте границы для этого файла.'
        : data.analysis_status === 'unavailable' ? 'Распознавание временно недоступно. Можно отметить участки вручную.'
        : data.status === 'disabled' ? 'Поиск внешней разметки выключен на сервере.' : data.status === 'unavailable' ? 'Внешняя разметка временно недоступна.' : 'Готовые отметки для этой серии не найдены.';
      compose();
    } catch (_) {
      if (externalRequest === controller && requestGeneration === generation) {externalRetryAvailable = true; sourceStatus = 'Внешняя разметка временно недоступна. Можно отметить участки вручную.'; compose();}
    } finally {
      clearTimeout(timeout); if (externalRequest === controller) {externalRequest = null; refreshRole();}
    }
  }
  function playbackChanged() {
    const state = PP.state(), next = playbackIdentity(state);
    if (identity && next !== identity) resetPlayback();
    if (!state.id) return;
    identity = next; tick(); lookupExternal(state);
  }
  el('skip-segments-open').onclick = () => openPanel(panel.hidden);
  el('skip-segments-close').onclick = () => openPanel(false);
  el('skip-segments-retry').onclick = () => {
    if (isFollower() || externalRequest) return;
    cancelExternal(); lookupExternal(PP.state());
  };
  panel.addEventListener('keydown', event => {if (event.key === 'Escape') {event.preventDefault(); event.stopPropagation(); openPanel(false);}});
  document.addEventListener('keydown', event => {
    if (panel.hidden || !['Escape','BrowserBack','Backspace','GoBack'].includes(event.key)) return;
    // Backspace remains available while editing a time field.
    if (event.key === 'Backspace' && ['INPUT','TEXTAREA'].includes(event.target && event.target.tagName)) return;
    event.preventDefault(); event.stopPropagation(); openPanel(false);
  }, true);
  el('skip-segment').onclick = () => {
    const position = PP.state().position;
    performSkip(effective.find(s => position >= s.start && position < s.end && modes[s.type] !== 'off'));
  };
  el('skip-segment-undo').onclick = () => {
    if (!undo || isFollower() || undo.expires < Date.now()) return;
    const position = undo.position; undo = null; ignoredBackwardUntil = Date.now()+5000; PP.seek(position); tick();
  };
  for (const type of types) el('skip-mode-'+type).onchange = () => {
    if (isFollower()) return;
    modes[type] = el('skip-mode-'+type).value; localModes = true;
    const data = {...modes}, requestOwner = owner; tick();
    if (!VV.user) {el('skip-segments-status').textContent = 'Настройки действуют до закрытия страницы. Войдите, чтобы сохранить их.'; return;}
    const write = async () => {
      if (account() !== requestOwner) return;
      try {await Personal.put('preferences','skip_segments',data); if (account() === requestOwner) el('skip-segments-status').textContent = 'Настройки пропуска сохранены.';}
      catch (_) {if (account() === requestOwner) el('skip-segments-status').textContent = 'Не удалось сохранить настройки. Они действуют на этой странице.';}
    };
    preferenceQueue = preferenceQueue.then(write,write);
  };
  for (const edge of ['start','end']) el('skip-capture-'+edge).onclick = () => {if (canEdit()) el('skip-edit-'+edge).value = time(PP.state().position);};
  el('skip-edit-cancel').onclick = resetEditor;
  el('skip-segments-form').addEventListener('submit', event => {
    event.preventDefault(); if (!canEdit()) return;
    const start = parseTime(el('skip-edit-start').value), end = parseTime(el('skip-edit-end').value), type = el('skip-edit-type').value;
    if (!validMediaKey(mediaKey)) {el('skip-segments-status').textContent = 'Дождитесь определения текущего файла.'; return;}
    if (!types.includes(type) || !Number.isFinite(start) || !Number.isFinite(end) || start < 0 || end <= start || !duration || end > duration) {
      el('skip-segments-status').textContent = 'Укажите корректное начало и конец в пределах длительности видео. Конец должен быть позже начала.'; return;
    }
    const keep = effective.filter(s => s.type === type && (!edit || segmentKey(s) !== edit.key));
    setType(type,[...keep,{type,start,end,source:'manual',auto_skip:true}]);
  });
  window.addEventListener('playbacksegments', event => {
    const state = PP.state(), detail = event.detail || {};
    if (detail.identity && playbackIdentity(detail.identity) !== playbackIdentity(state)) return;
    const next = playbackIdentity(state);
    if (identity && next !== identity) resetPlayback();
    identity = next;
    const nextKey = validMediaKey(detail.media_key) ? detail.media_key : '';
    if (mediaKey && nextKey !== mediaKey) {external=[];cancelExternal();suppressed.clear(); undo = null; resetEditor();}
    mediaKey = nextKey; duration = Number(detail.duration) || (PP.duration ? PP.duration() : state.duration) || 0;
    chapter = clean(detail.segments,'chapter'); compose(); lookupExternal(state);
  });
  window.addEventListener('playbackchange', playbackChanged);
  window.addEventListener('segmentsrefresh', event => {
    const expected=event.detail && event.detail.identity;
    if(expected && playbackIdentity(expected)!==playbackIdentity(PP.state()))return;
    cancelExternal();lookupExternal(PP.state());
  });
  window.addEventListener('playbackstop', resetPlayback);
  window.addEventListener('playbackrole', () => {refreshRole(); tick();});
  window.addEventListener('personalchange', loadModes);
  window.addEventListener('pagehide', cancelExternal);
  video.addEventListener('timeupdate', tick);
  video.addEventListener('playing', tick);
  if (typeof onAuth === 'function') onAuth(loadModes);
  loadModes();
  return {};
})();
